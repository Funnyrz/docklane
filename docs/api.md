# 公共 API 与运行约定

基地址 `/api/v1`；首版 `host_id` 只能为 `local`。其他主机返回 404。资源与工作流接口见同目录专用文档。

## 认证

| 方法 | 路径 | 行为 |
|---|---|---|
| GET | `/auth/status` | 返回 initialized、authenticated、user、csrf_token，并设置 CSRF Cookie |
| POST | `/auth/setup` | 首次创建管理员，JSON `{username,password}`；密码 12–1024 字节 |
| POST | `/auth/login` | 登录，设置 8 小时 HttpOnly 会话 Cookie |
| POST | `/auth/logout` | 删除会话及 Cookie |

所有 POST/PUT/PATCH/DELETE，包括 setup/login，必须先获取 CSRF Cookie 并发送匹配的 `X-CSRF-Token`。初始化完成后 setup 返回 409。会话 Cookie 为 SameSite=Strict；HTTPS 部署设置 `DM_SESSION_SECURE=true`。不信任任意代理提供的客户端 IP。失败登录限流；密码和会话原文不入库。

除认证入口及 `/healthz`，管理 API、SSE 和 WebSocket 均要求会话。WebSocket 验证 Origin，长连接在会话过期或撤销后关闭。

## 主机与系统

- `GET /hosts/local/dashboard`：Engine 状态、资源统计、宿主机已知指标、最近备份及策略覆盖情况；不可用指标为 null，不伪装为 0。
- `GET /hosts/local/capabilities`：`{host_id,mode,capabilities}`，每项包含 available 与 reason。
- `GET /settings`：非秘密配置及能力；配置授权通过本地配置文件管理。
- `GET /audit?limit=50&offset=0`：审计记录。
- `GET /healthz`：进程与数据库健康。Docker 断连不使整个 Web 服务退出。

Docker 容器内无法访问宿主机 `/proc` 时，只显示 Engine 提供的主机指标。备份覆盖统计按已启用策略定义，不能替代最近备份成功或恢复验证结果。

## 任务

- `GET /tasks?limit=50&offset=0`
- `GET /tasks/:id`
- `GET /tasks/:id/logs?after=0`
- `POST /tasks/:id/cancel`
- `GET /events`：SSE，支持 `Last-Event-ID` 或首次连接 `after` 游标；事件含 id、type、task_id、data、created_at。

长任务返回 202 和任务对象。状态为 Pending/Running/Succeeded/Failed/Canceled。提交可带 `Idempotency-Key`，同一键只能绑定同一操作者、操作类型及目标。取消只对声明可安全取消的任务生效。

首版单工作线程串行执行管理长任务，避免冲突资源并行修改；目录扫描另有限流。重启识别中断任务并执行已注册补偿，然后标记 INTERRUPTED，不盲目重放部署、重建、恢复等外部副作用。

统一错误格式：`{"error":{"code":"ERROR_CODE","message":"…"}}`。错误和任务日志执行敏感字段脱敏。API 未知路径返回 JSON 404，页面路由才使用 SPA 回退。
