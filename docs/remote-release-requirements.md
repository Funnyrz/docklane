# 远端 CI 与发布环境要求

## 当前发布状态

目前最新 Beta 为 [v0.3.2-beta.2 预发布版](https://github.com/Funnyrz/docklane/releases/tag/v0.3.2-beta.2)，当前源码版本为 `0.3.2-beta.2`。旧 v0.3.1-beta.1、v0.3.1-beta.2 的 Release、附件及远端 Tag 已撤回；v0.3.0 Release 也已撤回，但保留其 Tag。当前没有正式 Release 或正式版 latest，显式 `--channel stable` 会停止。当前安装请使用 [README 的一键安装命令](../README.md#一键在线安装推荐)，无参数默认选择最新 Beta，并在修改系统前显示测试版警告。

本版服务、账号、目录、镜像和安装附件统一为 `docklane`，原生程序、在线与离线新装及 Docker 部署默认端口 9999。安装器默认只安装原生二进制及常规依赖，不预加载 worker；旧 `docker-manager` 部署残留会导致安装停止，不自动更名或迁移。

v0.3.2-beta.2 使用七附件：双架构 `docklane-v0.3.2-beta.2-linux-{amd64,arm64}.tar.gz`、`docklane-installation.tar.gz`、`docklane-images-amd64.tar.gz`、`docklane-images-arm64.tar.gz`、`install-online.sh` 和 `RELEASE-SHA256SUMS`。统一清单精确覆盖其余六项，安装材料内 `IMAGE-SHA256SUMS` 覆盖两个镜像包，`IMAGES.txt` 固定两架构镜像 ID。预检验证单架构 Docker save 内容、版本/架构/层摘要、MIT 和可执行文件。

只有需要工作容器的任务且默认镜像引用缺失时，才下载同版本、当前程序架构的已校验镜像；程序与本机 Linux Docker Engine 架构必须一致。已有本地镜像复用，自定义引用由管理员准备。签发前验证无镜像安装/升级、首次任务下载、后续复用、失败不停止写入者、预览不联网和离线预加载，实际结果记录在 [验证记录](verification.md)。

Beta 发布保持 `prerelease=true`、`make_latest=false`；正式 latest 查询实际 HTTP 404 表示尚无正式版，鉴权、限流及服务器错误仍中止。v0.3.2-beta.1 的 Release、Tag 和原六附件保留，不覆盖其产物；不重建已撤回的 v0.3.0 Release。新版必须使用新 Tag 及其精确通过 CI 的提交。

## 历史发布与环境记录

> 以下原有发布、CI、公开下载、latest 状态及清理描述均为相应测试/发布时刻的历史记录，保留用于追溯；它们不代表已撤回的 Release 现在仍可下载。旧 v0.3.1-beta.1、v0.3.1-beta.2 Release、附件及远端 Tag 已撤回，历史链接可能失效；Git 提交及原始 JSON 验收记录保留。

截至 2026-10-19，公开仓库 [Funnyrz/docklane](https://github.com/Funnyrz/docklane) 的首个正式版本 [v0.3.0](https://github.com/Funnyrz/docklane/releases/tag/v0.3.0) 已发布并设为 latest，默认分支为 `main`。Tag 与应用产物来源为 [3a53e098](https://github.com/Funnyrz/docklane/commit/3a53e0986eb4e84f355332974eb6662006673932)，精确提交的 [CI 39216500363](https://github.com/Funnyrz/docklane/actions/runs/39216500363) 和 [发布工作流 39217575907](https://github.com/Funnyrz/docklane/actions/runs/39217575907) 均成功。[公开 curl 验收](evidence/online-install/public-curl.json) 无传输 mock，已验证正式资产安装、重复执行及真实 VM 重启后自启，保留匿名 API 限流的原始失败记录。

一键入口 `scripts/install-online.sh` 的后续通用 `main` 修正 [99ebac6](https://github.com/Funnyrz/docklane/commit/99ebac6ceba60791a46a4f23d5f6b5422a02c0f0) 通过公开 latest 链接处理 API 限流，仍校验并安装原 v0.3.0 资产，不覆盖正式 Tag 或已发布产物。该修正的 [CI 39218732043](https://github.com/Funnyrz/docklane/actions/runs/39218732043) 也已成功；与首版 Tag 的成功 CI 分开记录。六组发行版/架构候选记录、QEMU amd64 边界、worker 转换与集成证据见 [当前验证记录](verification.md#2026-10-19-在线安装与正式发布)。

历史准备阶段尚无 commit、remote 或目标仓库，仅以只读 API 核对本机账户；`docs/evidence/remote-release-readiness.json` 保留该阶段的准备证据，不代表当前仓库状态，也不代替本轮实际远端 CI 与发布结果。GHCR 权限和镜像 Digest 仍需单独核对，本轮在线安装通过 Release 镜像归档提供本地 worker，不依赖公开 GHCR 包。

## 需要确定的具体配置

| 配置 | 需要提供或完成的值 | 验证方式 |
|---|---|---|
| GitHub 仓库 | `Funnyrz/docklane`，公开，默认分支 `main`；`origin` 指向该仓库 | `gh repo view OWNER/REPO --json nameWithOwner,defaultBranchRef,viewerPermission` |
| 提交与默认分支 | 发布提交包含源码、锁文件、工作流和脱敏证据；按 Lore 协议提交，tag 与成功 CI 对应同一 SHA | `git rev-parse HEAD`、远端默认分支及 SHA |
| Actions 策略 | 仓库启用 Actions；组织允许所列官方/第三方 action 及固定 commit；允许 GitHub-hosted runner 和外部依赖下载 | 仓库/组织 Settings → Actions；只读 API 不可见时由管理员核对 |
| 原生 runner | `ubuntu-24.04`（amd64）、`ubuntu-24.04-arm`（arm64）；可用并发与分钟额度；VM 内 sudo、iptables、Docker 网络能力 | 四个 Engine/架构 job 的实际 runner、内核与工具报告 |
| 网络与磁盘 | HTTPS 访问 GitHub/API/Release downloads、Go proxy/校验服务、pnpm registry、Docker Hub、download.docker.com；每个 job 足够容纳镜像与 Go 缓存，建议至少 14 GiB 空闲 | job 下载、构建日志；不足时升级 runner 容量 |
| 发行版本 | 当前脚本和镜像默认固定为 `v0.3.2-beta.2`；建立指向已通过 CI 的同一 commit 的 tag | `release-prepare.yml` 拒绝其他版本，并核对 tag 的精确 SHA |
| 镜像目的地 | 若另行发布 GHCR，明确小写包名、公开/私有、归属与关联仓库；Release 离线镜像安装无需 GHCR | GHCR 包 Settings → Manage Actions access；已有同名包必须授予此仓库访问 |
| 部署镜像引用 | 发布后的 Compose/image 与 `DM_BACKUP_WORKER_IMAGE` 指向同一获准版本或 Digest；当前默认是本地 `docklane:v0.3.2-beta.2`，直接拉 GHCR 名称时必须显式覆盖 worker 引用并预拉到对应主机 | 两架构各实际检查 worker `image inspect` 并执行 Volume 备份/恢复 |
| 发布权限 | 创建 Release 需要目标仓库 contents 写权限；GHCR workflow 发布需要 packages 写权限与包访问；本地 CLI 推包需要适用的 classic PAT `write:packages`，组织 SSO 按要求授权 | 实际目标权限核对；本机 repo/workflow OAuth scope 不能代替 GHCR 授权 |
| 发布审批 | 候选审核后显式设置 `publish=true`；维护者按仓库策略配置受保护 Environment、required reviewers 和回退保留规则 | 准备 job 仅有只读权限；独立发布 job 才有 contents 写权限，不自动创建 Environment |
| 验收/安全证据 | 归档同一源码与产物的 Linux/内核报告、checksums、CI URL；阅读 `docs/security-notes.md` 中尚未清零的依赖告警 | 不把本地验证或共享内核结果写成远端、独立内核或零告警通过 |

[GitHub 官方 runner 表](https://docs.github.com/en/actions/reference/runners/github-hosted-runners) 列出两种所需 runner，私有仓库使用其 Actions 额度。GitHub-hosted Ubuntu job 是临时 VM；当前 CI 不把 Ubuntu runner 当作 Debian 独立内核验收。Debian 独立内核报告由专用隔离 VM 验收另行提供，见[发布验收范围](release-acceptance.md)；若要求每次远端 CI 重跑 Debian 12/13 开机、升级和 systemd 场景，需再配置可创建/销毁这两种完整 VM 的专用设施、镜像和凭据，以及受信任手动入口，不能直接在共享业务 self-hosted runner 上运行平台安装脚本。

历史功能验收中，amd64 独立内核已通过完整 QEMU/TCG VM 补测，原生 CPU 运行另有 LXD 证据。如发布方还要求在原生 amd64 完整 VM 复跑，需提供专用 x86_64 KVM/虚拟化宿主或可销毁的 Debian 12/13 VM：至少 2 CPU、2 GiB 内存、10 GiB 磁盘，guest 有 sudo 和普通 Docker bridge/firewall 能力。KVM 宿主须开放可访问的 `/dev/kvm` 及 VMX/SVM；当前可达的嵌套测试宿主未开放这些能力，不能把该次 TCG 结果写成 KVM 或裸机性能验收。这项原生硬件复跑与历史独立内核功能验收分别记录；它们不代表新在线安装器已完成全矩阵验收。

## 已准备好的远端工作流

`.github/workflows/ci.yml` 在 push、pull request 或手动触发时运行锁文件、前端类型/测试/构建、Go 测试/race/vet/格式与脚本检查。真实集成是原生 `amd64/arm64 × Engine 28.5.1/29.5.2` 四格，直接运行现有 `release.sh` 生成的本机架构正式二进制，Compose 2.40.3、Restic 0.19.1 校验上游 checksums 后安装。Engine 使用 Docker 官方指定版本静态包，报告记录下载文件 SHA256；该 SHA 是传输后摘要，不冒充上游签名验证。

每格在 GitHub 临时 VM 建立自己的 Engine 数据目录、执行目录、Socket 和 bridge，不重配 runner 的原 daemon；任务结束只停止该 PID 并删除本 job 创建的 bridge。Docker 将[单主机多 daemon](https://docs.docker.com/reference/cli/dockerd/#run-multiple-daemons)列为实验能力，网络规则可能相互影响，因此这个步骤只用于一次性的 GitHub-hosted VM，不应用在业务宿主机。集成报告、Engine 日志、系统/内核、工具版本和 worker 镜像元数据分别归档 14 天。CI action 均固定 commit，额外校验器固定 actionlint 1.7.12；Buildx 固定 0.29.1。

`.github/workflows/release-prepare.yml` **只有手动入口**，要求既存且与 `web/package.json` 版本一致的 `v0.3.2-beta.2` tag，且精确 commit 已有成功完成的 `ci.yml` push/手动 run。准备 job 复用构建脚本生成 Linux 双架构二进制、分架构离线镜像、安装/运维材料、在线脚本和 Python helper、MIT 协议、工具版本、安全说明、CI URL 与 `RELEASE-SHA256SUMS`，上传候选 Actions artifact（保留 14 天）。准备阶段保持只读权限，不登录 registry、不推镜像。

`publish` 默认为 `false`，只生成候选物。审核通过后，显式设置 `publish=true` 才运行独立发布 job，以必要的 `contents: write` 创建 Beta 预发布版并保持 `make_latest=false`；签发前重新核对 tag、精确提交的 CI 和资产校验和。安装脚本使用同一版本的二进制、安装材料与 worker 镜像，不混用默认分支文件。长期保留应下载候选包或使用已公开的版本 Release。

手动 workflow 需先存在于默认分支，具有仓库写权限的操作者才能从 UI/CLI 触发；参见 [GitHub 手动运行说明](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/manually-run-a-workflow)。发布提交推送到 `main` 后，可执行以下流程；实际 run URL 和结果另行记录，不将命令示例当作执行证据：

```sh
gh workflow run ci.yml --repo Funnyrz/docklane --ref main
# 确认精确提交的 CI 成功，再把 v0.3.2-beta.2 tag 推到该提交。
gh workflow run release-prepare.yml --repo Funnyrz/docklane --ref main \
  -f release_tag=v0.3.2-beta.2 -f publish=false
# 下载候选物，核对 RELEASE-SHA256SUMS、版本、架构、许可与安装材料。
gh workflow run release-prepare.yml --repo Funnyrz/docklane --ref main \
  -f release_tag=v0.3.2-beta.2 -f publish=true
```

历史准备阶段已在独立 Debian 13 arm64 VZ VM 中预演这个私有 Engine 方案：28.5.1 与默认 containerd image store 的 29.5.2 各完成 16 项真实集成，固定 Buildx 0.29.1 在两版 Engine 上均构建、加载并运行了原生测试镜像。已验证原 daemon 的 ID、配置摘要和库存对照一致，专属 daemon、Socket、bridge 和数据目录已清理。预演修复了 `--bridge`/`--bip` 互斥问题；本地小容量 tmpfs 引发的 ENOSPC 已改用 guest ext4 重跑。该结果及明确限制见[准备证据](evidence/remote-release-readiness.json)，不等于 GitHub job、runner 分配或发布权限已经通过。

## 只读 preflight

```sh
python3 scripts/release-preflight.py \
  --actionlint /path/to/actionlint \
  --online --repo OWNER/REPO --tag v0.3.2-beta.2 \
  --registry ghcr.io/owner/docklane \
  --report /tmp/docklane-release-readiness.json
```

不传目标时检查本地状态并列出缺项；`--online` 仅通过既存 gh 登录只读查询账户、目标仓库/Actions、精确 SHA 的 CI 及远端 tag。报告不保存 token、原始认证输出或带凭据的 remote URL。退出码 0 表示所检查门禁通过，1 表示产物/校验等检查失败，2 表示缺配置或尚未验证。正式发布需要显式的 `publish=true` 入口与候选审核；preflight 不用一次试推来探测 registry 权限。

## 候选物审核后的正式签发

发布负责人先下载候选 artifact，并运行 `sha256sum --check RELEASE-SHA256SUMS`，核对两架构二进制、在线脚本、Python helper、MIT 协议及 worker 版本。双架构程序以仅含 `docklane` 与 `LICENSE` 的精简压缩包上传；安装材料及双架构独立镜像包按七附件布局归档。发布后从公开 URL 验证资产可下载，并以真实 `curl` 命令在可销毁的 systemd VM 验证后台运行、重启自启、升级与迁移。Ubuntu 24.04、Debian 12/13 × amd64/arm64 的结果按实际完成范围记录，未完成项目不得写为通过。

两个分架构镜像包分别包含 `docklane:v0.3.2-beta.2-amd64` 与 `docklane:v0.3.2-beta.2-arm64`，可在获准登录 GHCR 后分别打 tag/push，再使用 `docker buildx imagetools create` 建立一个同时含 `linux/amd64`、`linux/arm64` 的 `v0.3.2-beta.2` manifest。随后用 `imagetools inspect` 检查两架构及 Digest，分别拉取运行验证；将最终 registry Digest 和 Release URL 记录回验收矩阵。

GHCR 支持仓库关联的 `GITHUB_TOKEN` 发布，但既有同名包的关联/Actions 权限必须正确；首次创建的包默认私有，既存包可能已有其他可见性。因此推送包本身也属于发布动作，不能把“创建 draft Release”当作镜像不会对外可见的保证。参见 [GHCR 官方认证、关联与可见性说明](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)。

GitHub Release 可先使用 `gh release create v0.3.2-beta.2 --verify-tag --draft ...` 创建草稿，审查后由负责人明确签发；`--verify-tag` 可防止 CLI 自动创建缺失 tag。本轮 GitHub 目标已确定为 `Funnyrz/docklane`；正式签发以工作流的显式发布入口为准，Beta 始终不设为正式 latest。若另行发布 registry，应先确定包归属与可见性。参见 [gh release create 官方说明](https://cli.github.com/manual/gh_release_create)。
