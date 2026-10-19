# Docker 管理平台 PRD v0.3

> 2026-10-16 · 技术架构确认版 · 待评审

## 一、产品定位

面向个人开发者、中小团队和自托管用户的 Linux Docker 管理与备份恢复平台。首版管理单台服务器，架构预留多机扩展；支持 Docker 容器与 Linux 二进制两种安装方式；基础管理和备份恢复并重。

核心原则：无损发现已有 Docker 资源；Docker Engine 是运行状态的权威来源；高危操作先预检和确认；备份必须可恢复；默认最小权限。

## 二、竞品及差异化

| 产品 | 官网 | 借鉴方向 |
|---|---|---|
| Portainer | https://www.portainer.io/ | 容器、多环境管理 |
| 1Panel | https://1panel.pro/ | 国内服务器运维、镜像源 |
| Dockge | https://github.com/louislam/dockge | Compose 文件管理 |
| Dockhand | https://github.com/Finsys/dockhand | 备份工作流 |
| Arcane | https://github.com/getarcaneapp/arcane | 镜像更新、轻量管理 |

差异化：无损接管、镜像拉取故障诊断、备份覆盖分析、可验证的恢复向导。

## 三、功能范围

| 模块 | 需求 | 优先级 |
|---|---|---|
| 工作台 | Docker 状态、主机资源、容器和备份统计 | P0 |
| 容器 | 启停、配置、日志、终端、资源监控 | P0 |
| Compose | 发现、导入、编辑、校验、部署 | P0 |
| 镜像与仓库 | 拉取、删除、导入导出、仓库凭据 | P0 |
| 镜像加速 | 配置、连通性测试、故障诊断 | P0 |
| 网络与存储 | 网络、Volume、Bind Mount | P0 |
| 备份与恢复 | 本地备份、定时策略、校验、恢复 | P0 |
| 任务中心 | 进度、历史、失败日志 | P0 |
| 告警、远程备份 | 通知、S3/SFTP | P1 |
| 多机与 RBAC | Agent、团队权限 | P2 |

## 四、页面字段、流程与验收

### 4.1 工作台

**字段**：主机名、系统、架构、内核、运行时间、Docker 版本/API 版本、Docker Root Dir、CPU、内存、磁盘、Docker 占用、容器总数/运行/停止/异常、镜像数、最近备份、未受保护资源。

**操作**：刷新、查看容器、拉取镜像、创建备份、查看任务。

**验收**：AC-DASH-01 统计与 Engine 一致；AC-DASH-02 Docker 连接失败显示错误而非零资源；AC-DASH-03 刷新不修改资源。

### 4.2 容器管理

**列表字段**：名称、ID、状态、镜像、端口、CPU、内存、创建时间、运行时长、所属 Compose 项目、备份状态。

**详情标签**：概览（健康检查、重启次数）、配置（环境变量、命令、端口、挂载、网络、重启策略、资源限制）、日志（时间戳、Tail、实时跟踪）、终端（TTY）、监控、数据。

**修改配置流程**：读取 Inspect → 修改 → 区分可原地更新与需重建字段 → 校验端口/挂载/镜像 → 预览差异与停机风险 → 确认 → 执行 → 重新读取实际状态 → 审计。

**验收**：
- AC-CTR-01：识别 CLI 和第三方创建的容器。
- AC-CTR-02：启停重启以 Engine 最终状态为准。
- AC-CTR-03：日志支持 Tail、时间戳、实时跟踪和断线重连。
- AC-CTR-04：终端支持 TTY，Shell 缺失时提示。
- AC-CTR-05：重建前展示差异、风险及回退方案。
- AC-CTR-06：Compose 容器引导至项目级配置修改。
- AC-CTR-07：删除容器默认不删除数据卷或宿主机数据。

### 4.3 Compose 项目

**字段**：名称、来源（面板/本地/候选重建）、工作目录、配置路径、服务数、状态、最近部署时间、文件变更状态。

**导入流程**：读取 Compose 标签 → 定位文件 → 检查依赖 → Docker Compose CLI 校验 → 预览服务/网络/卷 → 确认关联（不重建）→ 保存项目索引。

缺失源文件时可基于 Inspect 生成候选 YAML，必须标记“待验证”，不可声称完全还原。

**验收**：AC-CMP-01 导入不改变容器 ID、端口和挂载；AC-CMP-02 保留 env_file、多文件、profiles 和变量插值上下文；AC-CMP-03 部署前校验并展示影响范围。

### 4.4 镜像、仓库与加速

**镜像字段**：Repository、Tag、Digest、ID、大小、创建时间、关联容器。

**拉取表单**：镜像引用、Tag/Digest、平台架构、仓库认证、进度、错误原因。

**仓库字段**：URL、用户名、加密凭据、连接测试。

**加速流程**：读取 daemon.json → 添加 Mirror URL → 测试网络及仓库 API → 预览差异 → 备份原配置 → 确认 daemon 重启 → 验证生效。

**注意**：registry-mirrors 主要针对 Docker Hub；容器安装方式不一定有权限操作宿主机 daemon.json 和 systemd，缺少授权时提供只读及手动指引。

**验收**：AC-IMG-01 拉取显示进度、错误和 Digest；AC-IMG-02 删除前显示引用容器；AC-IMG-03 不覆盖 daemon.json 其他字段；AC-IMG-04 daemon 重启前提示风险；AC-IMG-05 区分网络可达、API 可用和镜像可拉取。

### 4.5 网络与存储

**网络字段**：名称、ID、Driver、Subnet、Gateway、IPv6、Internal、关联容器数、创建时间。

**存储字段**：Volume 名称、Driver、Mountpoint、关联容器、大小估算、备份状态；Bind Mount 的宿主机与容器路径。

**验收**：AC-STO-01 容器与挂载双向跳转；AC-STO-02 不因无运行容器引用就自动删除 Volume；AC-STO-03 目录大小扫描异步限流。

### 4.6 备份策略与恢复

| 字段 | 类型 | 必填 |
|---|---|---|
| 策略名称 | 文本 | 是 |
| 对象 | 容器/Compose/Volume/目录 | 是 |
| 内容 | 配置、文件、数据库逻辑导出 | 是 |
| 一致性模式 | 在线逻辑/停机/文件级 | 是 |
| 仓库 | 本地目录 | 是 |
| 计划 | Cron/手动 | 是 |
| 时区 | IANA 时区 | 是 |
| 保留 | 最近 N 个或周期 | 是 |
| 排除路径 | 路径规则 | 否 |
| 失败重试 | 次数和间隔 | 是 |

**执行流程**：等待 → 权限/空间/对象预检 → 一致性处理 → 写入仓库 → 校验 → 记录元数据 → 保留清理 → 完成。失败记录阶段、错误码与日志；如停止过容器，必须尽力恢复原运行状态。

**技术建议**：集成 Restic，利用加密、快照和恢复能力；普通仓库结构检查不等于完整数据校验。MySQL/PostgreSQL 等数据库不得把运行中的原始文件复制标记为一致性备份，需停机或使用逻辑导出。

**恢复向导**：选择备份点 → 选择新目录（默认）或原位置 → 检查空间/路径/权限/镜像/端口 → 展示覆盖范围与停机风险 → 二次确认 → 恢复 → 校验文件及服务健康。

**验收**：
- AC-BKP-01：完整备份并恢复普通 Volume 文件和必要元数据。
- AC-BKP-02：面板重启后定时计划保留，同一计划实例不重复执行。
- AC-BKP-03：失败备份不标记成功，不进行破坏性清理。
- AC-BKP-04：脱离 SQLite 仍可发现并恢复 Restic 仓库。
- AC-BKP-05：拒绝备份源与目标相互包含的递归配置。
- AC-BKP-06：测试环境完成端到端恢复验证。
- AC-BKP-07：没有一致性保障的数据库文件备份显著提示风险。

### 4.7 任务中心

**类型**：镜像拉取、Compose 部署、容器重建、备份、恢复、清理、目录扫描、daemon 配置变更。

**字段**：任务 ID、类型、目标、状态、进度、操作者、开始/结束时间、错误码、日志、重试次数。

**状态**：Pending、Running、Succeeded、Failed、Canceled；无法安全取消时禁用取消。

**验收**：AC-TASK-01 长任务异步运行且可查看进度；AC-TASK-02 重启后历史可查、中断任务可识别；AC-TASK-03 日志不泄露敏感凭据。

### 4.8 系统设置与安全

**字段/功能**：初始化管理员、密码哈希、会话、CSRF 防护、敏感配置加密、操作审计、Docker 连接状态、宿主机能力检测。

**验收**：AC-SYS-01 未认证不得执行管理操作；AC-SYS-02 凭据不写普通日志；AC-SYS-03 高危操作记录操作者/目标/时间/结果；AC-SYS-04 缺少宿主机权限时禁用操作并说明原因。

## 五、技术架构

```text
Vue 3 + TypeScript
       |
Go API（REST + WebSocket）
       |
鉴权 / Docker SDK / 任务调度 / 备份编排
       |                      |
Docker Engine API         Restic + 本地仓库
       |
容器 / 镜像 / 网络 / Volume

SQLite：用户、Compose 索引、策略、任务、审计
```

首版只配置一个本机 Host，但接口与数据库保留 host_id 和 capabilities；未来通过 Agent 扩展多机。Docker 实时资源状态以 Engine 为准，不把 SQLite 副本当作权威状态。

### 数据模型初稿

| 实体 | 核心字段 |
|---|---|
| Host | id、name、docker_endpoint、capabilities、status |
| ComposeProject | id、host_id、name、source_type、workdir、config_paths、revision |
| RegistryCredential | id、registry_url、username、encrypted_secret |
| BackupRepository | id、type、location、encrypted_key_ref、status |
| BackupPolicy | id、host_id、name、targets、consistency_mode、schedule、timezone、retention |
| BackupSnapshot | id、policy_id、repository_id、snapshot_ref、manifest、status |
| Task | id、host_id、type、target、status、progress、error_code、started_at、finished_at |
| AuditLog | id、actor_id、action、resource_type、resource_id、result、created_at |

## 六、非功能需求

- 首版 Linux Docker Engine，Compose 依赖 Docker Compose v2。
- 资源列表分页，Stats 流式传输，大目录扫描异步限流。
- 任务支持幂等键、超时、失败恢复和审计。
- 默认不直接暴露公网；支持 HTTPS/反向代理；谨慎授权 Docker Socket。
- 恢复默认新目录，原位置覆盖需要二次确认。
- 结构化日志、错误码、任务追踪 ID；数据库迁移与配置版本管理。

## 七、开发里程碑

| 阶段 | 交付内容 |
|---|---|
| M1 | 项目骨架、认证、Docker 连接、工作台 |
| M2 | 容器、镜像、网络、存储、任务中心 |
| M3 | Compose 发现、导入、编辑、部署 |
| M4 | 镜像加速诊断与宿主机权限检测 |
| M5 | Restic、本地备份、定时策略 |
| M6 | 恢复向导、异常测试、双安装包、文档 |

## 八、发布验收场景

1. 在已有容器的 Linux 主机安装，原容器不停机、不重建。
2. CLI 新建容器后，面板能发现。
3. 导入运行中的 Compose 项目，容器 ID 不变。
4. 镜像拉取失败时展示可诊断原因。
5. 镜像加速配置保留原 daemon.json 其他字段并可回退。
6. Volume 定时备份成功生成备份点。
7. 恢复到新目录，文件内容校验一致。
8. 在另一测试环境导入备份仓库并恢复。
9. 磁盘不足、权限不足、任务中断时状态和日志准确。
10. 没有宿主机权限时无法执行未授权的 daemon 操作。

## 九、待评审事项

- 首版是否提供 MySQL/PostgreSQL 专用逻辑备份，还是先做停机文件备份？
- 独立容器配置重建是否列为 P0，还是优先 Compose 项目修改？
- 镜像加速是否在首版实现宿主机配置写入，还是先提供安全手动向导？
- Restic 仓库密钥如何初始化、导出和灾难恢复？
- 支持的 Linux 发行版、CPU 架构及 Docker 最低版本如何确定？

---

*本版为需求细化草案，下一版可进一步拆分 API、数据库 DDL、页面交互和测试用例。*


## 十、已确定的技术架构（v0.3 新增）

### 10.1 技术栈决策

| 层级 | 选型 | 说明 |
|---|---|---|
| 后端语言 | Go | 单机二进制部署、并发任务、Docker 生态 |
| HTTP 框架 | Gin | REST API 与静态资源服务 |
| Docker 管理 | Docker 官方 Go SDK | 容器、镜像、网络、卷、事件、Stats |
| Compose | Docker Compose v2 CLI | 通过受控进程执行并解析结果，保留多文件与环境上下文 |
| 前端 | Vue 3 + TypeScript + Vite | 单页管理后台 |
| UI / 状态 | Naive UI + Pinia | 管理界面和前端状态 |
| 持久化 | SQLite | 用户、配置、任务、备份元数据、审计 |
| 数据访问 | sqlc 或显式 SQL | 可审查迁移与查询，首版不强制 ORM |
| 任务调度 | robfig/cron | 定时备份与维护 |
| 备份引擎 | Restic | 快照、加密、增量备份、恢复与校验 |
| 实时通道 | SSE + WebSocket | SSE 负责日志/进度推送，WebSocket 负责交互终端 |
| 发布 | Go embed + 多阶段 Docker 构建 | 前端构建产物嵌入 Go 二进制 |

### 10.2 Web 服务与启动行为

- 核心 Go 进程同时提供管理页面、`/api/v1/*` REST API、`/api/v1/events` SSE 与受保护的 WebSocket 终端入口；前端无需独立部署 Nginx、Node.js 或 MySQL。
- Vue/Vite 编译到 `web/dist`，通过 Go `embed` 打包；Go 使用 `fs.Sub` 等方式正确定位静态文件，支持 SPA 路由回退，但 API 404 不得回退为 `index.html`。
- 服务默认监听可配置的 `:9999`；安装成功后可从 `http://<服务器IP>:9999` 访问（取决于防火墙、网络和绑定地址）。公网部署要求 HTTPS/反向代理及访问限制。
- 启动顺序：读取配置 → 检查数据目录权限 → 初始化/迁移 SQLite → 检查 Docker Engine → 启动任务调度 → 启动 HTTP 服务。Docker 不可用时仍应允许管理员登录并查看诊断，不得把不可用状态显示为资源数为零。
- 首次访问进入初始化向导：创建管理员、设置强密码、检查 Docker 连接、检测宿主机能力、确认数据目录和备份目录。初始化后不得再次开放匿名管理员注册。
- 配置项至少包括监听地址、端口、数据目录、Docker endpoint、会话安全设置、日志级别、备份临时目录、允许的宿主机操作能力。

### 10.3 双安装方式

**方式 A：Docker 容器部署（优先交付）**

- 发布多架构镜像，持久化挂载应用数据目录，按需挂载 Docker Socket；默认不启用 `--privileged`，不默认挂载宿主机根目录。
- 通过 Docker Socket 完成容器、镜像、网络和卷的管理；该 Socket 具有高权限，应明确安全警示。
- 容器内无法仅凭 Docker Socket 直接安全地修改宿主机 `/etc/docker/daemon.json`、重启 systemd 或读取任意 Bind Mount；这类操作需经受控宿主机 Helper 授权，或提供手动操作引导。
- Compose 文件访问、Bind Mount 数据备份需明确宿主机路径与容器路径映射关系；缺少路径访问时功能显示为不可用并给出解决方案。

**方式 B：Linux 二进制部署**

- 发布 Linux amd64/arm64 二进制与校验和；提供手动安装和 systemd 服务文件，支持开机自启、日志、升级与卸载。
- 不要求系统预装 Node.js、Java、MySQL 或 Nginx；需安装并可访问 Docker Engine，Compose 功能需 Docker Compose v2。
- 推荐使用专用系统账号及最小权限策略；访问 Docker Socket 本身已属高权限，不应误称为低权限运行。
- 启动后使用同一 Web 页面与 API，功能一致性通过能力检测实现，而非依赖两套业务代码。

### 10.4 核心服务与宿主机 Helper

```text
浏览器
  └─ HTTP(S) → Go Web Server（Vue 静态资源 / REST / SSE / WebSocket）
                   ├─ 认证、权限、审计
                   ├─ Docker SDK → Docker Engine
                   ├─ Compose 执行适配层
                   ├─ 任务中心 / Cron / Restic
                   ├─ SQLite + 配置/密钥管理
                   └─ Host Capability Adapter
                         ├─ 本机授权操作（独立二进制模式）
                         └─ 可选受限 Host Helper（Docker 模式）
```

- Helper 是**可选的受限宿主机操作服务**，只开放白名单能力，例如读取/安全合并 daemon 配置、请求受控重启、访问显式授权的备份路径。
- Helper 与核心服务采用本机 Unix Socket 等受限通道通信，执行身份认证、参数验证、路径白名单、审计和超时控制；不得提供任意 Shell 执行 API。
- 核心服务按能力矩阵决定 UI 可操作性：`docker.manage`、`compose.manage`、`host.daemon_config`、`host.service_restart`、`backup.bind_mount` 等。
- Docker daemon 修改采用读取 → 语法校验 → 差异预览 → 备份原文件 → 原子写入 → 用户确认重启 → 状态验证 → 失败回退的流程。重启 daemon 可能影响容器，不能承诺零停机。

### 10.5 目录结构建议

```text
docker-manager/
├── cmd/server/                 # Web 服务入口
├── cmd/host-helper/            # 可选宿主机 Helper
├── internal/
│   ├── api/                    # REST / SSE / WebSocket
│   ├── auth/                   # 认证与会话
│   ├── docker/                 # Docker SDK 适配
│   ├── container/              # 容器业务
│   ├── image/                  # 镜像与仓库
│   ├── compose/                # Compose 项目
│   ├── network/                # 网络
│   ├── volume/                 # Volume / Bind Mount
│   ├── backup/                 # Restic 与恢复编排
│   ├── task/                   # 队列、调度、进度
│   ├── host/                   # 能力检测与受控宿主机操作
│   ├── database/               # SQLite 与迁移
│   └── config/                 # 配置管理
├── web/src/                    # Vue 3 前端源码
├── web/dist/                   # 前端构建产物（发布时嵌入）
├── migrations/                 # SQL 迁移
├── deployments/                # Docker、systemd、安装脚本
├── docs/                       # PRD、接口与运维文档
├── go.mod
└── Dockerfile
```

### 10.6 未来多机扩展约束

- 首版仅有一个本地 Host，但资源、任务、备份策略均携带 `host_id`（对外 API 可使用 `node_id` 别名，内部命名必须统一）。
- Docker 操作通过 HostClient 接口隔离本地 Engine，避免业务代码直接绑定全局 Socket。
- 后续远程 Agent 需独立认证、加密通信、能力上报和断线处理；不在首版实现。
- SQLite 保存管理元数据，容器的真实运行状态始终以 Docker Engine 为准。

### 10.7 新增技术与安装验收标准

| 编号 | 验收场景 | 通过标准 |
|---|---|---|
| AC-ARCH-01 | 单二进制启动 | 不安装 Node、Nginx、MySQL 即可提供完整 Web 页面与 API |
| AC-ARCH-02 | Docker 部署 | 配置持久化后容器重建，用户、策略与历史记录仍存在 |
| AC-ARCH-03 | 二进制部署 | systemd 启动、重启、开机自启正常；服务退出后能定位日志 |
| AC-ARCH-04 | 首次初始化 | 未初始化只能访问初始化流程；初始化后禁止匿名重建管理员 |
| AC-ARCH-05 | Docker 断连 | Web 可访问并显示连接错误；恢复连接后资源状态自动更新 |
| AC-ARCH-06 | 无 Helper | 宿主机 daemon 修改和未授权 Bind Mount 备份不可执行，界面明确说明 |
| AC-ARCH-07 | Helper 权限 | 非白名单路径、未授权动作及任意命令执行请求均被拒绝并审计 |
| AC-ARCH-08 | 实时日志与终端 | 日志可断线重连；终端经认证授权，关闭会话释放后端资源 |
| AC-ARCH-09 | 跨平台构建 | Linux amd64/arm64 发布产物可启动并正确识别 Docker 能力 |
| AC-ARCH-10 | 升级兼容 | 应用升级执行数据库迁移，已有用户、备份策略和仓库配置不丢失 |

### 10.8 技术实施顺序

1. Go + Gin + Vue 3 项目骨架，前端嵌入 Go，首次初始化与登录。
2. SQLite、Docker SDK、主机能力检测、工作台。
3. 容器、镜像、网络、Volume、实时日志和任务中心。
4. Compose v2 项目导入、编辑与受控部署。
5. Restic 本地仓库、定时备份、恢复向导和端到端校验。
6. 镜像加速诊断、受控宿主机操作及可选 Helper。
7. Docker 镜像、Linux 二进制、systemd、amd64/arm64 发布与升级测试。

**范围说明：** 本章确认的是技术架构和交付要求；不意味着所有 Helper 能力或所有数据库一致性备份策略都必须在第一个可用构建中同时完成。具体分期以 P0 验收和安全测试为准。
