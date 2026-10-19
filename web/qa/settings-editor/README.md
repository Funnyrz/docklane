# 页面配置与授权验收

本轮提供“系统设置 → 配置功能”，编辑允许访问目录、宿主机/容器映射与支持环境下的 daemon 操作授权。延续浅色控制台样式；表单在手机纵排，长内容可滚动。

## 修改与实现边界

- `internal/config/editable.go`：复用现有配置数据类型，仅增加三个可编辑字段的规范化、严格校验和数据库启动覆盖。未增加依赖或通用配置框架。
- `internal/api/settings.go`、`server.go`：GET 返回 effective / saved / pending_restart；POST 要求当前管理员密码及确认，遵守既有会话、CSRF、恢复任务写暂停。设置和成功审计在同一事务保存，密码不进入设置和审计。
- `cmd/server/main.go`：打开数据库后、任务恢复前应用已保存值。运行时配置保持不可变，避免备份/恢复中途切换目录或权限。
- `web/src/components/CapabilitySettingsEditor.vue`、`web/src/settings.ts`、`SettingsView.vue`：增加编辑、添加/删除映射、取消、保存错误、待重启和撤销待生效修改。密码关闭或提交后清除；不支持的部署无法开启宿主机授权。
- `web/src/i18n/settings-editor.ts` 及已有国际化文件、`style.css`：完整中英文、原因到配置入口的指引、对应错误和响应式表单。
- `README.md`、`README.en.md`、`docs/api.md`、`docs/operations.md`：说明数据库优先级、激活时机和外部前提。

页面保存的三个字段优先于配置文件/环境变量，**重启 Docklane 后生效**。这次不提供面板内重启进程；可沿用现有部署的服务/容器重启方式。具体 Docker 配置修改仍经过原预览和确认，Linux、二进制部署、工具、Unix Socket 及系统文件权限要求均保留。容器挂载和工具安装仍是外部条件。

## 验证

- 96 项前端测试通过；包括新编辑器的密码和确认门槛、错误后的草稿保留、关闭后清除密码、映射编辑、不支持环境与语言切换。类型检查和生产构建通过。
- Go 全部测试与 vet 通过；API 覆盖认证、CSRF、密码错误/限流、严格输入、禁止根目录/重复/非法映射、不支持授权、保存与当前配置隔离，以及审计失败时事务回滚。
- `browser-results.json`：12 项通过。中英文 × 1440×900、1366×768、1280×800、768×1024、390×844；检查页面及弹窗无横向溢出、键盘焦点、取消不提交、失败保留草稿、待生效和部署限制。两次保存请求由模拟 API 截获。
- `real-app-results.json`：无 API 拦截，实际嵌入前端的 Go 程序，临时数据库与不存在的 Engine Socket。通过真实初始化/登录/页面保存、重启本次实例后授权目录生效、页面清空目录后重启撤销，以及两条成功审计。密码不进入返回设置或审计；临时数据自动清理。
- `fixture-results.json`：原全页验收矩阵 120 项通过，与专项合计 132 项浏览器检查。结果与本轮截图保存在本目录，历史目录不改写。
- 浏览器运行错误为零。`visual-verdict.json` 是按用户目标的人工自评；截图不用于宣称像素复刻。

复现时先启动端口 18098 的前端，再在可加载 Playwright 的环境执行 `node web/qa/settings-editor/verify.cjs`。通过 `DM_QA_PLAYWRIGHT` 可指定 Playwright 包路径。`real-app.cjs` 会自行启动和清理指定的 `DM_QA_BINARY`（默认 `/tmp/docklane-editor-native`），使用端口 18099；仅用于测试二进制和可创建临时数据库的本地环境。

## 包与已知边界

`package-results.json` 记录本轮 Linux AMD64 / ARM64 包、SHA-256、ELF 架构及逐字节前端嵌入检查。包在 macOS 交叉编译，未在 Linux 实机运行；真实程序重启验证在 macOS，宿主机授权保持禁用。生产构建仍有已有的大于 500 kB 分块提示。
