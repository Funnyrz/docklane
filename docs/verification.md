# v0.3 验证记录

> 最新本地重建（2026-10-17，中英文界面版，20261017-133718）：[发布包构建及校验证据](evidence/release-package-20261017-133718.json)。包含 Linux 双架构二进制、离线镜像和安装材料；本次通过 make check 与启动冒烟，未远端发布或重跑全部业务验收。

> 2026-10-17：包含新界面的最新本地发布包已重建，见 [本次构建与校验证据](evidence/release-package-20261017.json)。下文平台/完整业务发布验收保持为此前版本的历史证据；本次未重新执行全部场景，也未远端发布。

验证日期：2026-10-16。本轮继续补齐发布场景并直接修复发现的问题；原始失败、环境限制和最终正式产物的通过证据分别保留。**功能验收证据已覆盖首版适用的 44 项 AC 和 10 条发布场景；六组独立发行版内核功能验收已补齐；TCG 性能边界、远端 CI 与正式发布仍按下述范围记录。**

## 最终构建与自动化

| 检查 | 结果与证据 |
|---|---|
| 格式、Go vet、TypeScript、Go 单元、前端测试、Go race | `DM_TEST_RESTIC=… make check` 通过，[此前完整日志](evidence/checks.log) 与 [本轮门禁日志](evidence/checks-kernel-gates.log)；含真实 Restic 0.19.1，不是默认跳过外部工具的结果 |
| 前端 | 20 项测试 / 9 个文件通过；真实 Naive UI 标签、键盘确认、终端 resize 和快照门禁有回归 |
| Linux 构建 | CGO=0 的 amd64/arm64 二进制、正式 Dockerfile 双镜像、离线归档均完成，[构建来源](evidence/docker-build.json) |
| Shell/Python | 发布、安装、升级、卸载和所有新增验收脚本语法检查通过；移除 dist 后 make check 自动构建前端并完整通过 |
| 产物 | 两套 SHA256SUMS、gzip、双架构 manifest 核对通过，[产物证据](evidence/artifacts.json) |
| 安全扫描 | 生产 npm 审计无已知漏洞；Go SDK 的既有 daemon 插件告警与可达边界见 [安全记录](security-notes.md)，未声称 Go 扫描零告警 |

Go/race 和前端检查运行于 macOS 开发主机；Linux 专项与真实外部工具另按下表执行，不混称为全平台 race。最终独立二进制 SHA256：amd64 `fadb2178791fa2b43cf53ddd48ee3eb8d59e2cd1e78d3eccf70e427466f3ad66`，arm64 `4d008a31bb96074dde455ebf0e087a5fb1d324b36c25ee27c77b74a07fa57b79`。

独立二进制记录了未提交 Git 工作树的 VCS 标记，Docker 构建排除 `.git`，因此两者摘要不同。相同源码以 `-buildvcs=false` 独立重建后，字节完全匹配镜像内 ELF；各自的摘要、编译输入和运行证据已分别记录，没有把不同摘要当作同一产物。

## 实际测试结果

| 入口 | 结果 | 核心覆盖 |
|---|---|---|
| [正式 Linux 集成](evidence/linux-integration-final.json) | 16/16，零跳过，清理无错误 | 统计与只读、认证/CSRF、生命周期、重建回退、镜像流、多文件 Compose、目录/Volume/异 UID 恢复、真实 Cron、256MiB 备份中 SIGKILL、全新 SQLite 仓库发现 |
| [资源](evidence/resources-release.json) | 8/8 | 日志重连、TTY/Shell/释放、数据保留、引用保护、网络、五万文件扫描/限流/取消、路径映射和队列 symlink 攻击 |
| [私有镜像拉取](evidence/private-pull.json) | 3/3 | 实际 Basic 认证、新 20MiB 层 46 条字节进度、Digest，错误凭据/缺 Tag/秘密回显均安全失败 |
| [正式工作流](evidence/workflows-release-official.json) | 8/8 | 真实 Compose 新项目健康重建、共享写入者、EACCES/ENOSPC/损坏/错误口令、原位 EFBIG 失败持停、递归拒绝及离线调度 |
| [浏览器](../web/qa/release-browser-results.json) | 17/17，complete=true | 实际高危弹窗、重建失败、引用跳转、日志/TTY、Compose 变更、任务取消、Docker 代理断连自动恢复、备份/恢复及未验证快照救援 |
| [平台矩阵](evidence/platform-matrix.json) | 6 个独立内核环境 × 两种安装方式，60 项通过；历史 LXD 另 30 项 | 实际安装、重启、自动启服、全管理数据持久化、坏程序/迁移 SQL 故障停服、成组恢复、镜像重建和卸载保留 |
| [amd64 独立内核完整集成](evidence/amd64-kernel-acceptance.json) | Debian 12/13、Ubuntu 24.04 各 16/16；零跳过、清理无错误 | 正式 amd64 ELF，普通 bridge、Compose、异 UID/GID worker、真实 Cron 去重、SIGKILL 补偿和无原 SQLite 恢复 |
| [最终产物分层诊断](evidence/registry-diagnostics-final.json) | 4/4 | TCP/API/实际拉取区分、401、关闭端点；真实 Engine TLS 失败单独报告 |
| [CI 本地预演](evidence/remote-release-readiness.json) | Engine 28.5.1 / 29.5.2 各 16/16 | 原生 Debian 13 ARM VM、专属 Socket/数据/bridge、两版 Buildx 构建加载运行；与远端 Actions 分开 |
| [跨主机恢复](evidence/cross-host-recovery.json) | 两个目的环境通过 | 原 Ubuntu ARM64 完整 VM 关闭后，仅仓库和离线口令到 Debian ARM64 VM / 另一主机原生 amd64 Debian 用户态恢复，内容/权限/UID/GID 一致 |

资源专项报告使用前置二进制 `13ba21…`；报告中四个生产资源模块的源码摘要均与最终构建的 `compiled_inputs` 一致，最终 ELF 另经正式集成与平台测试。分层诊断已用最终 ARM64 ELF 重跑 4/4，见 [最终诊断证据](evidence/registry-diagnostics-final.json)，早期 `amd64-emulated.json` 只作历史记录。

原始正式集成位于独立 Ubuntu 24.04.4 ARM64 VM；新增三套 amd64 为完整 QEMU/TCG VM，CI 本地预演为原生 Debian 13 ARM64 VZ。Engine 实测 28.5.1 / 29.5.2，Compose 2.40.3、Restic 0.19.1。使用明确 Socket，原 Docker context `desktop-linux` 未改变。测试只操作本轮随机标签资源，不使用 prune。

元数据测试数据始终位于 guest 原生 Linux 文件系统。macOS 共享目录会转换 UID/GID，不能用来证明所有者恢复；共享目录只传递程序、加密仓库与报告。完整平台和复现命令见 [发布验收](release-acceptance.md)。

## 本轮发现并修复的问题

| 问题 | 修复与验证 |
|---|---|
| 小 Tail 重连遗漏断线日志；隐藏时间戳后没有游标 | 重连使用 Since 配合完整 Tail，Engine 始终提供游标，展示层单独隐藏时间戳；真实浏览器两种开关均补取完整 |
| 排队扫描前授权，执行前祖先目录可被替换为 symlink | 执行时重新授权，使用有界 `os.Root` 遍历；攻击先复现、回归及真实大目录验证通过 |
| Registry 错误可无标签反射口令 | 已知口令与认证串先精确移除，再统一脱敏；真实恶意返回、API/任务/SQLite/WAL 检查通过，禁止 URL 内嵌凭据 |
| TTY 初始尺寸为零；包装对象标题错误；选择框无真实关联标签 | 初始 resize、解包资源标题、真实 Naive UI 输入标签回归及浏览器通过 |
| Enter 打开高危弹窗，keyup 自动勾选第一项 | 打开后聚焦取消，确认仍必须显式勾选；真实键盘复验通过 |
| Restic 非零退出仍可能生成 partial 快照 | 仓库内完成标记、正确使用 tag 改写后的 ID、保留策略只计算已验证快照；全新 SQLite 仍区分状态，未验证只允许额外确认后的新目录文件救援 |
| 正式只读 worker 镜像默认 HOME 导致缓存失败 | HOME/TMPDIR/RESTIC_CACHE_DIR 显式指向私有 tmpfs；正式镜像实际备份、恢复、删容器重建通过 |
| 管理容器 RW 映射使其进入自己的停机范围 | 有界读取容器安装密钥证明自身，无法识别或命中同安装数据的待停容器即拒绝；自定义 hostname/伪造 HOSTNAME/named Volume 正负例通过 |
| CI 专属 daemon 同时传入 bridge/bip，启动被拒绝 | 先创建并配置任务专属网桥，仅传 bridge；两版 Engine 各 16/16 及 Buildx 实际构建/加载通过 |
| 矩阵引用未定义 E，手工链接计数缺少范围 | 最终 ELF 重跑分层诊断 4/4；新增 PRD/矩阵/链接/索引结构校验并纳入 Makefile 与 CI，报告包含扫描范围和脚本摘要 |

快照、缓存和自保护的原始失败与修复后记录见 [修复证据](evidence/workflow-verification-fixes.json)。第一次并行集成因全局库存变化而失败，保留在 [并行误差记录](evidence/linux-integration-concurrent-attempt.json)；之后独占窗口串行 16/16 通过，没有放宽断言。Ubuntu 平台工具 wrapper 重复传 host 参数的环境失败也保留在 [harness 记录](evidence/platforms/ubuntu24-arm64-harness-attempt.json)，修正工具后全部通过。

Ubuntu amd64 精简镜像的 iptables 前置准备因完整 apt 索引下载/解压较慢被中断一次；重试后按官方默认源完成安装。尝试的 HTTPS/main 配置被 cloud-init 在启动时重写，未把该尝试写成最终生效配置。[首次准备日志](evidence/platforms/ubuntu24-amd64-bootstrap-attempt.log) 与 [环境来源](evidence/platforms/amd64-kernel-provenance.json) 保留这些环境事实，不作为应用故障。

平台检查交给下一套集成测试时，主控脚本曾只轮询 `Stopped`，抢在前一个停止命令退出前重启 Debian 13/Ubuntu VM，导致这两个主控命令最终超时。全部应用阶段分别成功，完整原始日志已保留；交接已改为等待停止命令退出 0 及新的完成标记，再允许启动。独立重放 [两轮启动/停止交接](evidence/platforms/vm-handoff-verification.json) 均成功，最终 VM 停止；这次复验针对主控交接，应用报告保留原始结果。

## daemon、进程与升级故障

既有 [daemon 实测](evidence/daemon.json) 验证保留其他字段、dockerd 校验、实际重启、失败逐字节回退、审计及面板重启不重复补偿。浏览器本轮只预览影响和确认门禁，未在 UI 提交真实 daemon 重启；实际变更由 API 场景证明。

[Linux 进程回归](evidence/process-regression.json) 覆盖父进程 SIGKILL、取消、超时和子孙进程释放；最终正式集成另在实际停机备份中强杀面板，确认 Restic 退出、重启恢复原写入者且任务为 Failed/INTERRUPTED。

平台脚本真正执行安装、系统重启、升级、失败启动、成组回滚及卸载。迁移测试在真实 SQLite 事务内注入 SQL abort，验证实际新程序退出、服务停机、版本仍为 1、完整恢复材料存在；移除注入故障后恢复用户、会话、仓库、策略、历史和密钥。未虚构首版之前不存在的旧业务版本。

## 边界与后续发布门禁

- 主矩阵三发行版 × 双架构均为独立内核完整 VM。amd64 为 QEMU/TCG 全系统软件 CPU 模拟，覆盖 Debian 6.1/6.12 与 Ubuntu 6.8；ARM64 为原生 VZ。原生 x86_64 LXD 三份历史证据另存，二者不混称为原生硬件性能或裸机认证。
- 只有 schema v1。已验证同版本替换、真实 SQL 故障和恢复；未来出现历史业务版本后仍需加入实际跨版本升级。
- 没有配置 Git remote/registry 目标；未推送镜像、签发正式发布或运行远端 CI。两版专属 Engine 的本地 CI 预演均通过，但 GitHub setup actions、目标 runner 分配与远端权限尚未验证。具体缺项和只读 preflight 见 [远端发布要求](remote-release-requirements.md)。
- 数据库逻辑导出、Host Helper、多机、RBAC 等保持原定延期。ARCH-07 不计入首版适用 44 项。

## 清理与评审入口

全部测试应用、worker、容器、卷、网络、安装账号和离线口令已按归属清理；三套新增 amd64 QEMU VM、既有 Debian ARM VM 均已停止。[本轮清理](evidence/platforms/amd64-kernel-cleanup.json) 记录 48 项集成清理无错误，并回收约 912 MiB 下载分块，保留已校验基础镜像和停止的 VM 供复现。amd64 的历史 LXD 项目/实例/池/网络和传输目录已移除。[基础设施清理](evidence/platforms/amd64-infrastructure-cleanup.json) 保留明细。LXD 测试运行时保持安装，未修改原宿主 Docker。

专用 Colima `docker-manager-test` 保持运行供评审，Engine 没有测试容器残留。macOS 原有 `http://127.0.0.1:18080` 预览已更新到最终源码和资产，保留原临时数据目录，仍未初始化；运行不代表 Linux 安装。前端最终资产为 `index-Cwzy2wtV.js` / `index-BKEA8bZR.css`。如不再评审，可停止该 Colima profile；不会删除源码或发布归档。
