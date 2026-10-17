# 验收矩阵 v0.3

45 个 AC，首版适用 44 个；Host Helper 权限 AC-ARCH-07 延期。状态区分实现、自动化覆盖与发布环境验证。此表不把 mock Engine 测试当作真实 Docker 发布验收。

证据索引：[L：正式 Linux 集成](evidence/linux-integration-final.json)、[R：真实资源](evidence/resources-release.json)、[P：私有新层拉取](evidence/private-pull.json)、[W：正式工作流](evidence/workflows-release-official.json)、[F：验证标记/缓存/自保护修复](evidence/workflow-verification-fixes.json)、[U：17 项浏览器验收](../web/qa/release-browser-results.json)、[X：跨主机恢复](evidence/cross-host-recovery.json)、[M：平台矩阵](evidence/platform-matrix.json)、[K：amd64 独立内核与集成](evidence/amd64-kernel-acceptance.json)、[D：daemon 变更/回退](evidence/daemon.json)、[E：最终产物分层诊断](evidence/registry-diagnostics-final.json)、[C：CI 本地预演与远端缺项](evidence/remote-release-readiness.json)、[本轮自动化日志](evidence/checks-kernel-gates.log)。M 的六组主环境均有独立发行版内核：ARM64 为原生 VZ，amd64 为完整 QEMU/TCG VM；原生 amd64 LXD 用户态证据另保留，TCG 不用于原生硬件性能认证。K 的三套 amd64 VM 另各通过完整集成 16/16；其中平台 30 项已包含在 M 的 60 项中，不重复累计。C 是本地 CI 步骤预演，远端 CI 和正式发布尚未执行。

| 编号 | 阶段 | 验证要求 | 自动化证据入口 | 发布验证状态 |
|---|---|---|---|---|
| AC-DASH-01 | S1 | 统计与 Engine 一致 | `internal/api/server_test.go` | 真实 Engine 计数对比与单元通过 [L] |
| AC-DASH-02 | S1 | 断连显示错误而非零资源 | `internal/api/server_test.go` | 单元与镜像断连烟雾通过 |
| AC-DASH-03 | S1 | 刷新不修改资源 | `internal/api/server_test.go` | 真实只读三次刷新及单元通过 [L] |
| AC-CTR-01 | S3 | 发现 CLI/第三方容器 | `internal/resources/resources_test.go` | Ubuntu ARM64 实测通过 [L] |
| AC-CTR-02 | S3 | 启停结果重新 Inspect | `internal/resources/resources_test.go` | Ubuntu ARM64 实测通过 [L] |
| AC-CTR-03 | S3 | Tail/时间戳/实时及重连 | `internal/resources/resources_test.go` | 真实 Tail/时间戳开关、stdout/stderr、流式和断线补取通过 [R/U] |
| AC-CTR-04 | S3 | TTY、Shell缺失和连接释放 | `internal/resources/resources_test.go` | 真实 TTY 命令、尺寸、缺 Shell、关闭后 exec/FD 释放通过 [R/U] |
| AC-CTR-05 | S3 | 重建差异、风险、回退 | `internal/resources/resources_test.go` | 真实健康失败回退及界面差异/风险/失败任务通过 [L/U] |
| AC-CTR-06 | S3 | Compose容器走项目配置 | `internal/resources/resources_test.go` | Compose 容器后端限制与实际界面引导通过 [R/U] |
| AC-CTR-07 | S3 | 删除默认保留卷/宿主数据 | `internal/resources/resources_test.go` | 真实删除后 Volume/Bind 数据保留通过 [R] |
| AC-CMP-01 | S4 | 导入不改变ID/端口/挂载 | `internal/workflows/workflows_test.go` | Ubuntu ARM64 多文件无损导入通过 [L] |
| AC-CMP-02 | S4 | 保留多文件/env_file/profiles/插值 | `internal/workflows/workflows_test.go` | Ubuntu ARM64 env/profiles/插值通过 [L] |
| AC-CMP-03 | S4 | 部署前校验影响范围 | `internal/workflows/workflows_test.go` | 真实配置校验/影响预览/部署、源文件变更拒绝并重新预览通过 [U] |
| AC-IMG-01 | S3/S7 | 拉取进度/错误/Digest | `internal/resources/resources_test.go; internal/workflows/workflows_test.go` | 真实私有认证、新 20MiB 层 46 条字节进度、Digest 与错误通过 [P]；导入导出通过 [L] |
| AC-IMG-02 | S3/S7 | 删除前显示所有引用 | `internal/resources/resources_test.go; internal/workflows/workflows_test.go` | 真实停止容器引用显示及删除拒绝通过 [R/U] |
| AC-IMG-03 | S3/S7 | daemon其他字段保留 | `internal/resources/resources_test.go; internal/workflows/workflows_test.go` | 真实配置合并、重启验证和失败字节回退通过 [D] |
| AC-IMG-04 | S3/S7 | 重启前展示风险 | `internal/resources/resources_test.go; internal/workflows/workflows_test.go` | 真实预览及界面双确认、Enter 不自动勾选通过 [D/U] |
| AC-IMG-05 | S3/S7 | 区分网络/API/镜像拉取 | `internal/resources/resources_test.go; internal/workflows/workflows_test.go` | 真实 TCP/API 成功而 Engine 拉取失败的分层验证通过 [E] |
| AC-STO-01 | S3 | 容器/挂载双向跳转 | `internal/resources/resources_test.go` | 真实容器/Volume 双向跳转及标题通过 [U] |
| AC-STO-02 | S3 | 停止容器引用也不自动删卷 | `internal/resources/resources_test.go` | 真实停止容器引用拒绝删卷通过 [R] |
| AC-STO-03 | S3 | 目录扫描异步限流 | `internal/resources/resources_test.go` | 五万文件异步限流/串行/取消、祖先 symlink 替换拒绝通过 [R] |
| AC-BKP-01 | S5/S6 | 文件与必要元数据恢复 | `internal/workflows/workflows_test.go` | 目录/Volume/异 UID/GID 内容权限恢复实测通过 [L] |
| AC-BKP-02 | S5/S6 | 重启保留计划/实例不重复 | `internal/workflows/workflows_test.go` | 真实 Cron Volume 快照、UTC 实例重启去重及单元通过 [L] |
| AC-BKP-03 | S5/S6 | 失败不成功/不破坏清理 | `internal/workflows/workflows_test.go` | 真实 ENOSPC/EACCES/损坏/错口令、partial 后成功保留策略通过 [W/F] |
| AC-BKP-04 | S5/S6 | 无原SQLite导入仓库恢复 | `internal/workflows/workflows_test.go` | 原 VM 关闭，跨主机/发行版/架构导入仓库、全新 SQLite 恢复通过 [X] |
| AC-BKP-05 | S5/S6 | 拒绝源/仓库相互包含 | `internal/workflows/workflows_test.go` | 真实双向包含、符号链接/路径递归拒绝通过 [W] |
| AC-BKP-06 | S5/S6 | 测试环境完整恢复 | `internal/workflows/workflows_test.go` | 真实文件/Volume/独立容器及 Compose 新项目健康恢复通过 [L/W] |
| AC-BKP-07 | S5/S6 | 无一致性保障显著警示 | `internal/workflows/workflows_test.go` | 真实文件模式风险确认、未验证快照额外救援确认通过 [U/F] |
| AC-TASK-01 | S2 | 长任务异步/进度 | `internal/task/task_test.go` | 真实进度、失败原因、扫描取消与持久历史界面通过 [L/R/U] |
| AC-TASK-02 | S2 | 历史保留/中断可识别 | `internal/task/task_test.go` | 重启历史、真实 SIGKILL 补偿及持久化单元通过 [L/K] |
| AC-TASK-03 | S2 | 日志不泄露凭据 | `internal/task/task_test.go` | 真实 Registry 反射秘密、拉取错误、日志/SQLite/WAL 脱敏通过 [R/P] |
| AC-SYS-01 | S1/S7 | 未认证无管理权限 | `internal/api/server_test.go; internal/security/security_test.go` | 未认证/CSRF 真实请求拒绝通过 [L] |
| AC-SYS-02 | S1/S7 | 凭据不入普通日志 | `internal/api/server_test.go; internal/security/security_test.go` | 真实凭据加密与登录/拉取反射秘密脱敏通过 [R/P]；密钥/日志检查通过 [M] |
| AC-SYS-03 | S1/S7 | 高危操作审计 | `internal/api/server_test.go; internal/security/security_test.go` | 任务结果与审计原子性测试、真实 daemon 审计通过 [D] |
| AC-SYS-04 | S1/S7 | 缺能力禁用/服务端拒绝 | `internal/api/server_test.go; internal/security/security_test.go` | 无授权 API 拒绝且文件不变通过 [L]；浏览器禁用原因通过 |
| AC-ARCH-01 | S0/S8 | 单二进制Web/API | `internal/api/server_test.go; internal/database/database_test.go; scripts/integration.py` | Linux 单二进制 Web/API、嵌入资产通过 [L] |
| AC-ARCH-02 | S0/S8 | 容器重建持久化 | `internal/api/server_test.go; internal/database/database_test.go; scripts/integration.py` | 正式镜像删除重建保留用户/会话/仓库/策略/历史/密钥通过 [M] |
| AC-ARCH-03 | S0/S8 | systemd启动/重启/开机/日志 | `internal/api/server_test.go; internal/database/database_test.go; scripts/integration.py` | 三发行版 × 双架构完整 VM 重启、boot ID 改变、自动启服、日志与历史通过 [M] |
| AC-ARCH-04 | S0/S8 | 初始化仅一次 | `internal/api/server_test.go; internal/database/database_test.go; scripts/integration.py` | 真实 setup 单次/匿名限制与并发单元通过 [L] |
| AC-ARCH-05 | S0/S8 | Docker断连/恢复 | `internal/api/server_test.go; internal/database/database_test.go; scripts/integration.py` | 真实 daemon 恢复与页面无需手动刷新断连/重连通过 [D/U] |
| AC-ARCH-06 | S0/S8 | 无Helper拒绝宿主机未授权操作 | `internal/api/server_test.go; internal/database/database_test.go; scripts/integration.py` | 真实宿主授权拒绝、Docker 停机备份自保护及明确路径映射通过 [L/R/F] |
| AC-ARCH-07 | S0/S8 | Helper白名单/鉴权（延期） | `internal/api/server_test.go; internal/database/database_test.go; scripts/integration.py` | 延期：Host Helper 后续交付；首版拒绝边界仍测试 |
| AC-ARCH-08 | S0/S8 | 实时日志/终端资源释放 | `internal/api/server_test.go; internal/database/database_test.go; scripts/integration.py` | 真实日志断线续传、终端关闭后 exec 和后端 FD 释放通过 [R/U] |
| AC-ARCH-09 | S0/S8 | Linux双架构启动 | `internal/api/server_test.go; internal/database/database_test.go; scripts/integration.py` | 原生双架构运行及六组独立内核 × 双方式功能通过 [M]；amd64 独立内核为 TCG |
| AC-ARCH-10 | S0/S8 | 升级迁移保留管理数据 | `internal/api/server_test.go; internal/database/database_test.go; scripts/integration.py` | schema v1 数据保留、实际升级备份、SQL 迁移故障停服及成组恢复通过 [M/K]；无历史业务版本可做跨版本实测 |

## 十条发布场景

| 场景 | 验证 | 证据/状态 |
|---|---|---|
| RELEASE-01 | 已有容器安装不重建/不停机 | Ubuntu ARM64 已有容器 ID/状态不变通过 [L] |
| RELEASE-02 | CLI创建后发现 | Ubuntu ARM64 CLI 发现通过 [L] |
| RELEASE-03 | Compose导入保持ID | 多文件/env/profiles 导入不重建通过 [L] |
| RELEASE-04 | 拉取失败可诊断 | 实际 TCP/API 成功、Engine 拉取失败正确区分通过 [E] |
| RELEASE-05 | daemon字段保留及回退 | 真实保留字段、失败恢复文件/服务通过 [D] |
| RELEASE-06 | Volume定时备份点 | 真实 Cron Volume 快照与重启实例去重通过 [L] |
| RELEASE-07 | 新目录内容验证恢复 | 新目录文件/权限/UID/GID 实测通过 [L] |
| RELEASE-08 | 异环境无原SQLite仓库恢复 | 原 Ubuntu VM 关闭，仅仓库/离线材料到 Debian ARM64 与另一主机 amd64 全新 SQLite 恢复通过 [X] |
| RELEASE-09 | 磁盘/权限/中断故障 | 真实 ENOSPC/EACCES/损坏/错口令、备份 SIGKILL 和原位失败持停通过 [L/W] |
| RELEASE-10 | 未授权宿主机操作拒绝 | 实际 API 拒绝、daemon 文件保持不变通过 [L] |

## 执行证据

具体命令、平台、工具版本、已通过和未运行项记录在 [verification.md](verification.md)。`scripts/integration.sh` 是隔离 Linux 场景入口；新增资源、故障、平台及跨主机入口与各环境边界见 [发布验收复现](release-acceptance.md)。首版 44 项 AC 均有明示范围的行为证据，ARCH-07 延期；独立内核功能与原生硬件性能分别报告。远端 CI/正式签发仍需完成 [具体配置](remote-release-requirements.md)，本地预演不能替代远端 run。
