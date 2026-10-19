# v0.3 验证记录

## 2026-10-19 按需工作镜像实现验证（发布前快照）

本轮将在线及离线安装、升级对预加载 worker 的要求移除，默认只安装二进制及常规依赖。需要工作容器的实际备份/恢复任务才准备同版本、本机架构镜像；已有通用或架构标签的有效镜像会复用。新候选分为两个单架构镜像包，共七附件；现有公开 v0.3.2-beta.1 的六附件和行为未覆盖，新入口会拒绝缺少按需支持的旧 helper，不能混用 `main` 与旧材料。

[本轮脱敏验证记录](evidence/worker-image-on-demand.json)记录了源码摘要和验证边界。完整 `make check` 通过，包含前端 98 项、安装/生命周期/打包/预检共 184 项（其中 1 项 Linux 专属测试在 macOS 跳过）、Go race/vet、Shell/Python 检查；actionlint 与本地文档链接检查通过。新增回归覆盖无镜像安装、单架构下载、SHA/平台/版本/归档拒绝、并发与取消清理、失败重试、本地架构标签复用及 HTTPS 降级拒绝。准备失败不会停止写入者或创建/修改恢复目标，预检不触发下载。

真实 arm64 Docker 29.5.2 从本地 HTTP 验收服务器只下载当前架构镜像，完成校验、加载与别名设置；经典配置摘要与 containerd OCI ID 不同时通过导出内容核验，第二次调用无网络请求。实际 Docker 卷备份恢复、非 root 读取异 UID 数据和恢复原 UID/GID 等 16 项集成通过，测试资源已按专属标识清理。生产下载固定同版本 HTTPS 地址，真实验收服务器仅用于本地传输测试。

本轮未提交、推送或发布新版，未重跑新安装器的完整 systemd/发行版矩阵或公开镜像 URL 验收，也未重新构建和发布双架构新镜像。这些项目须在配套新版本签发时验证；以下已发布 Beta 的历史证据不能代替本轮发布验证。

## 当前发布准备

本轮源码版本为 `0.3.2-beta.2`，目标为 `v0.3.2-beta.2` 预发布版。发布前运行完整项目检查，再提交并推送 `main`；只有准确提交通过 CI 后才创建 Tag，由发布工作流校验候选物、暂存草稿并下载复核后公开。公开安装及首次任务下载验证结果将在签发后补充，以下历史证据不冒充本轮结果。

默认安装原生二进制及常规依赖，工作镜像在实际需要它的任务中按同版本、本机架构准备；两个单架构镜像包使新版发布为七附件，统一清单覆盖其余六项。服务、账号与标准目录继续使用 `docklane`，默认端口 9999，保留测试版警告和 MIT。现有 v0.3.2-beta.1 的 Release、Tag 和六附件保留；不修改正式版 latest，也不恢复已撤回的旧 Beta。

## 2026-10-19 v0.3.2-beta.1 部署命名与旧 Beta 撤回

[发布验收记录](evidence/beta-release-0.3.2-beta.1.json)对应 [048aae7](https://github.com/Funnyrz/docklane/commit/048aae7774c238ae7af6336769929649a69fc940)。[主分支 CI](https://github.com/Funnyrz/docklane/actions/runs/39270883287)、[Tag CI](https://github.com/Funnyrz/docklane/actions/runs/39272093523)及[发布工作流](https://github.com/Funnyrz/docklane/actions/runs/39272094843)均成功，包含 Docker 28/29 × amd64/arm64 实际集成和草稿上传后的下载复核。新版恰好六个 Docklane 命名附件，公开镜像下载的摘要与 GitHub 记录一致，双架构 ELF、MIT 原文、同版本安装材料及 worker 身份通过检查；包内 arm64 程序与镜像实际在 9999 健康并返回 `0.3.2-beta.1`，Compose 项目、服务和数据卷名称通过实际 Compose v2 配置校验。

原生 arm64 Ubuntu 24.04 隔离 systemd VM 使用无参数公开 curl 完成全新应用安装、管理员初始化、重复安装及真实关机/启动后的自启。新服务、账号、配置和数据目录及权限正确，程序为已发布原生包的准确字节；安装阶段、文件下载提示和双语警告检查通过。实际离线同版本重装生成完整配套备份；刻意安装无效程序后服务按预期停止，使用同一备份组恢复程序、服务、配置、安装记录、数据库及密钥成功。识别后的卸载保留数据、账号及安装记录，随后重装保留管理员、密钥和设置。另通过真实旧实例确认旧部署残留在依赖/下载之前被拒绝，原文件和进程未改动；没有执行旧部署更名迁移。

新版公开验证完成后，精确删除了 v0.3.1-beta.1、v0.3.1-beta.2 的 Release、六附件和远端/本地 Tag；对应 Release 与 Tag API 均返回 404。v0.3.0 Tag 未改变，旧 Git 提交与历史证据保留。当前仅有新版预发布，正式版 latest API 仍为 404。撤回后再次执行真实无参数公开命令，仍选择 `v0.3.2-beta.1`，程序 inode/摘要、密钥、配置、安装记录、应用/Docker 进程及管理员保留。完整本地 `make check` 及最终回归合计覆盖 176 项 Python 检查（macOS 跳过 1 项 Linux 专用场景），98 项前端测试、Go race/vet、Shell/Python、actionlint 通过。本次 VM 预装依赖，未新增完整发行版/架构 systemd 矩阵或独立国内运营商网络验收；失败恢复为同版本无效程序场景，不宣称旧版迁移或跨 schema 恢复。测试 VM 已停止并保留私有数据、配套备份；程序仍仅供学习与测试，请勿用于生产环境。

## 历史发布与验证记录

> 以下内容保留对应执行时刻的结论；旧 Beta Release、附件及远端 Tag 已撤回，历史下载与 Tag 链接可能失效。Git 提交、原始证据和下列历史段落保留用于追溯，不代表当前可下载版本。


[v0.3.1-beta.2 发布验证](evidence/beta-release-0.3.1-beta.2.json)对应 [bc0c775](https://github.com/Funnyrz/docklane/commit/bc0c775fa98d9b249108aff5ab32b27e58114139)。[主分支 CI](https://github.com/Funnyrz/docklane/actions/runs/39261562974)、[Tag CI](https://github.com/Funnyrz/docklane/actions/runs/39262606812)、[候选构建](https://github.com/Funnyrz/docklane/actions/runs/39262607783)及[发布工作流](https://github.com/Funnyrz/docklane/actions/runs/39264009565)全部成功，包含 Docker 28/29 × amd64/arm64 集成及草稿上传后的下载复核。新版恰好六个附件；公开镜像下载的全部摘要与 GitHub 记录一致，内外 SHA-256、双架构 ELF、MIT 原文、同版本材料及 worker 身份检查通过。实际包内 arm64 程序未设置 `DM_LISTEN` / `DM_CONFIG` 时默认在 9999 健康；arm64 Docker 镜像也实际提供 9999 服务，设置接口均返回 `0.3.1-beta.2`。原生程序、在线与离线新装、Compose 及镜像默认统一为 9999，随包入口包含默认 Beta 通道与安装进度。

原生 arm64 Ubuntu 24.04 隔离 systemd VM 通过无参数公开 curl 完成全新应用安装、管理员初始化、重复安装及真实关机/启动后的自动运行，保留会话、密钥及管理员；两次安装均显示八个阶段、下载完成和双语警告。另一既有 beta.1 实例通过公开命令升级为 beta.2，生成配套停机备份，保留原 8080、管理员、已保存设置、密钥及 Docker 进程；重复安装不替换程序或重启进程，显式 `--listen :9999` 后切换成功。旧 beta.1 的说明及全部附件保持原样，beta.2 保持预发布且正式版 latest API 仍为 404。本地完整 `make check`、162 项 Python 检查（1 项 Linux 专用测试在 macOS 跳过）、98 项前端测试、Go race/vet、Shell/Python 及 actionlint 通过。本次 VM 使用预装依赖，未新增完整发行版/架构 systemd 矩阵或独立国内运营商网络验收；程序仍仅供学习与测试，请勿用于生产环境。测试 VM 已停止，保留私有数据及配套备份。

[默认端口 9999 验证记录](evidence/default-port-9999.json)验证当前源码、部署示例与在线新装默认使用 `9999`，已有实例配置不自动改端口。源码 Linux arm64 程序未设置 `DM_LISTEN` / `DM_CONFIG` 时在 9999 健康；隔离 Ubuntu 24.04 VM 通过真实公开 curl 完成应用新装、重复安装及实际重启后自启，管理员、会话和密钥保留。另一旧实例无参数重复执行后仍监听 8080，配置及进程不变。当前 Compose 文件通过实际 Compose v2 校验；本机 162 项 Python 检查（1 项 Linux 专用测试跳过）、Go 配置检查、类型及脚本检查通过，[对应 CI](https://github.com/Funnyrz/docklane/actions/runs/39235072498) 五个 job 全部成功。首次端口可用性探测的失败与后续成功均保留；此为预装依赖的隔离 VM 验证，不代表新增全平台安装矩阵。

[安装进度验证记录](evidence/installer-progress.json)覆盖 156 项脚本相关测试（本机 1 项 Linux 专用测试跳过），包括阶段顺序、失败停止、元数据输出隔离和终端/日志模式。原生 arm64 Ubuntu 24.04 隔离 VM 通过真实公开镜像 curl 验证 8 个安装阶段和下载文件提示，失败请求不误报完成，原程序、管理员、配置、密钥及进程保留。[对应代码提交的 CI](https://github.com/Funnyrz/docklane/actions/runs/39232908207) 五个 job 全部成功，包含 Docker 28/29 × amd64/arm64 集成。本次是既有 Beta 上的重复安装复验；真实终端模式由 PTY 自动化检查进度条参数，未新增全新安装或全平台 VM 矩阵。

[默认 Beta 验证记录](evidence/default-beta-install.json)覆盖 152 项脚本相关测试（本机 1 项 Linux 专用测试跳过），并在原生 arm64 Ubuntu 24.04 隔离 VM 中通过真实公开镜像 curl 验证无参数 Beta 重复安装、测试版警告，以及显式 stable 在暂无正式版时的安全拒绝。管理员、设置、密钥、配置、二进制及应用/Docker 进程均保留；服务 active / enabled，健康检查成功。[对应代码提交的 CI](https://github.com/Funnyrz/docklane/actions/runs/39231392895) 五个 job 全部成功，包含 Docker 28/29 × amd64/arm64 集成。此项仅为既有 Beta 安装上的复验，未新增全新安装或全平台矩阵。

默认通道调整前的[撤回验证记录](evidence/beta-only-withdrawal.json)覆盖 150 项安装/生命周期/打包/预检测试（本机 1 项 Linux 专用测试跳过）、脚本与工作流检查。原生 arm64 Ubuntu 24.04 隔离 VM 通过公开镜像执行真实 curl：无参数请求在暂无正式版时清楚拒绝，显式 Beta 重复安装成功，原管理员、设置、密钥、配置、程序及 Docker 进程保留。[精确代码提交的 CI](https://github.com/Funnyrz/docklane/actions/runs/39229598649) 五个 job 全部成功，包含前后端检查和 Docker 28/29 × amd64/arm64 集成。此为既有 Beta 上的重复安装复验，不代表新增全平台或全新安装验收。

> 以下原有发布、CI、公开下载、latest 状态及清理描述均为相应测试/发布时刻的历史记录，保留用于追溯；它们不代表已撤回的 Release 现在仍可下载。原始 JSON、Tag 与历史验收记录未因本次撤回说明而改写。

## 2026-10-19 Beta 声明与六附件发布

[v0.3.1-beta.1](https://github.com/Funnyrz/docklane/releases/tag/v0.3.1-beta.1) 已作为预发布版发布，来源为 [7a28708](https://github.com/Funnyrz/docklane/commit/7a28708b5fe5cf17a1e01b0f47db1a8b6e2bf1e1)。[主分支 CI](https://github.com/Funnyrz/docklane/actions/runs/39225538379)、[Tag CI](https://github.com/Funnyrz/docklane/actions/runs/39226372174) 和 [发布工作流](https://github.com/Funnyrz/docklane/actions/runs/39226373513) 全部成功。新 Release 恰好六个上传附件，GitHub 自动源码包不计入；旧 v0.3.0 的 Tag、17 个资产身份/大小/摘要及正式版 latest 保持不变，新旧发布说明均有免责声明。

[本轮验证记录](evidence/beta-release-0.3.1.json) 包含六附件公开下载及内外校验链、双架构 ELF、安装材料版本/CI 来源、两个已发布 worker 镜像的 MIT 原文和 ELF 架构检查。本地完整 `make check` 通过，最终安装/生命周期/打包/预检共 141 项（macOS 跳过一项 Linux 专属场景，远端 Linux CI 覆盖），前端 98 项通过；Shell/Python、actionlint 与 Go race/vet 检查通过。

在新建原生 arm64 Ubuntu 24.04 systemd VM 中，默认 stable 从公开资产安装旧版并创建管理员；本地六附件候选完成真实升级，再由升级前同一组备份恢复旧程序、数据、配置、服务及 receipt。发布后无选择器或下载 mock，实际公开 `curl` 验证默认正式版重复安装、stable → beta、beta 重复安装、隐式降级拒绝及实际关机/启动后的自启健康。管理员、master.key、已保存设置及二进制保留检查通过；运行中的 Docker 未因安装或升级被重启。

第一次管理员初始化验收脚本未先取得 CSRF Cookie，应用按设计拒绝；修正验收脚本后完成初始化，保留初始失败记录，不把它算作程序安装失败。此次只覆盖上述 Ubuntu ARM systemd 路径，不能替代完整发行版矩阵或生产环境验证，也未独立验证国内运营商网络。程序仍为 Beta，请勿用于生产环境。本轮新建测试 VM 已停止，保留私有数据和配套备份；原工作区的其他未提交改动未纳入本次发布。

## 2026-10-19 公共镜像下载入口

[镜像传输验证记录](evidence/online-install/public-mirrors.json) 对应 [03760ee](https://github.com/Funnyrz/docklane/commit/03760ee64c7f30baa5c2037453cb13c56b3651a2)。主、备用镜像均返回与该提交字节一致的安装入口；全部所需 v0.3.0 arm64 资产（含 worker 镜像）、Compose 2.40.3 和 Restic 0.19.1 经主镜像下载并通过 SHA-256 校验。公开 latest 网页查询实测主镜像 HTTP 403 后，由备用镜像跳转到正确的正式版本地址。

原生 arm64 Ubuntu 24.04 独立 systemd VM 执行 README 中的真实公开 `curl` 命令成功，重复安装保留 Docker 和应用 PID、二进制及 `master.key`，服务保持 active / enabled，`/healthz` 返回 200。本次是在既有测试安装上复验传输和重复安装，不是新增一次全新安装或三发行版 × 双架构验收；没有独立国内运营商网络测试，Docker apt 仍是官方源，不据此保证国内全流程可达。自有短域名由用户选择暂缓，公开镜像仅替换传输路径。正式 v0.3.0 Tag 和资产未覆盖；测试后 VM 已停止并保留原数据。

本地 116 项安装/生命周期/打包/预检测试通过（macOS 跳过 1 项 Linux 专属测试），Shell/Python 语法、actionlint 和文档结构检查通过。该提交的 [CI](https://github.com/Funnyrz/docklane/actions/runs/39221537031) 五项任务全部成功：Linux 安装测试、前后端检查、双架构二进制构建，以及 Docker 28.5.1 / 29.5.2 × amd64 / arm64 四组真实集成。

## 2026-10-19 在线安装与正式发布

[v0.3.0 正式 Release](https://github.com/Funnyrz/docklane/releases/tag/v0.3.0) 已发布并设为 latest。Tag 与应用产物来源为 [3a53e098](https://github.com/Funnyrz/docklane/commit/3a53e0986eb4e84f355332974eb6662006673932)，该精确提交的 [CI 39216500363](https://github.com/Funnyrz/docklane/actions/runs/39216500363) 与 [发布工作流 39217575907](https://github.com/Funnyrz/docklane/actions/runs/39217575907) 均成功。

[公开 curl 验收](evidence/online-install/public-curl.json) 在原生 arm64 Ubuntu 24.04 独立 systemd VM 中直接执行公开命令，无下载或选择器 mock，覆盖缺少 Docker 时安装 Engine 29.9.1、后台运行、开机自启、首次管理员登录、重复安装保留与实际重启后的健康、密钥和会话保留。原匿名 GitHub API 限流失败记录仍保留；通用 `main` 引导脚本通过 [99ebac6](https://github.com/Funnyrz/docklane/commit/99ebac6ceba60791a46a4f23d5f6b5422a02c0f0) 增加公开 latest 链接回退，安装的仍是同一 v0.3.0 Release 资产，未覆盖正式 Tag 或已签发产物。该后续修正的 [CI 39218732043](https://github.com/Funnyrz/docklane/actions/runs/39218732043) 也已成功，包含静态检查、108 项安装/生命周期/打包/预检测试及 Docker 28/29 × amd64/arm64 四组真实集成。

| 环境与场景 | 本轮独立内核候选验收记录 |
| --- | --- |
| Ubuntu 24.04 | [amd64 安装、重复执行与实际重启](evidence/online-install/ubuntu24-amd64.json) · [arm64 安装](evidence/online-install/ubuntu24-arm64.json) |
| Debian 12 | [amd64 安装与重复执行](evidence/online-install/debian12-amd64.json) · [arm64 安装](evidence/online-install/debian12-arm64.json) · [arm64 失败恢复](evidence/online-install/debian12-arm64-recovery.json) |
| Debian 13 | [amd64 安装与重复执行](evidence/online-install/debian13-amd64.json) · [arm64 手动实例迁移](evidence/online-install/debian13-arm64-migration.json) |
| 正式发布资产 | [公开下载、双架构 ELF、MIT 与来源校验](evidence/online-install/published-assets.json) |
| Worker | [经典与 containerd image store 转换](evidence/online-install/worker-image-stores.json) · [真实备份恢复等集成](evidence/online-install/worker-integration.json) |

候选报告分别记录实际使用的脚本、产物摘要和失败重试，不能合并声称为最终 Tag 的六格公开下载验收。amd64 使用 QEMU/TCG 全系统 CPU 模拟，证明独立内核功能，不代表原生 amd64 性能；arm64 使用原生虚拟机。正式资产的公开传输与重启验收以上述公开 curl 报告为准。[本轮清理](evidence/online-install/cleanup.json) 确认七台验收 VM 均已停止，保留磁盘、源数据和配套备份；既存 Colima 构建 profile 留供镜像复查。现有测试服务器与宿主预览未被接管。

> 以下各节为 2026-10-16/17 的历史验收与本地构建记录；其中当时的发布、工具版本及资源清理状态不代表当前状态。本轮状态以上述 2026-10-19 证据为准。

> 历史本地重建（2026-10-17，中英文界面版，20261017-133718）：[发布包构建及校验证据](evidence/release-package-20261017-133718.json)。包含 Linux 双架构二进制、离线镜像和安装材料；本次通过 make check 与启动冒烟，未远端发布或重跑全部业务验收。

> 2026-10-17：包含新界面的当时最新本地发布包已重建，见 [本次构建与校验证据](evidence/release-package-20261017.json)。下文平台/完整业务发布验收保持为此前版本的历史证据；本次未重新执行全部场景，也未远端发布。

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
- 截至 2026-10-16 历史验收结束时，尚未配置 Git remote/registry 目标，也未推送镜像、签发正式发布或运行远端 CI。两版专属 Engine 的本地 CI 预演均通过，但当时 GitHub setup actions、目标 runner 分配与远端权限尚未验证。原准备要求和只读 preflight 见 [远端发布要求](remote-release-requirements.md)；当前发布状态见顶部 [2026-10-19 记录](#2026-10-19-在线安装与正式发布)。
- 数据库逻辑导出、Host Helper、多机、RBAC 等保持原定延期。ARCH-07 不计入首版适用 44 项。

## 清理与评审入口

全部测试应用、worker、容器、卷、网络、安装账号和离线口令已按归属清理；三套新增 amd64 QEMU VM、既有 Debian ARM VM 均已停止。[本轮清理](evidence/platforms/amd64-kernel-cleanup.json) 记录 48 项集成清理无错误，并回收约 912 MiB 下载分块，保留已校验基础镜像和停止的 VM 供复现。amd64 的历史 LXD 项目/实例/池/网络和传输目录已移除。[基础设施清理](evidence/platforms/amd64-infrastructure-cleanup.json) 保留明细。LXD 测试运行时保持安装，未修改原宿主 Docker。

专用 Colima `docker-manager-test` 保持运行供评审，Engine 没有测试容器残留。macOS 原有 `http://127.0.0.1:18080` 预览已更新到最终源码和资产，保留原临时数据目录，仍未初始化；运行不代表 Linux 安装。前端最终资产为 `index-Cwzy2wtV.js` / `index-BKEA8bZR.css`。如不再评审，可停止该 Colima profile；不会删除源码或发布归档。
