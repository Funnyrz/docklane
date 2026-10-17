# 远端 CI 与正式发布的环境要求

当前工作区没有 Git commit、Git remote 或已选定的 GitHub 仓库。GitHub CLI 已能通过只读 API 验证本机登录账户，但这不能证明某个仓库、组织或 GHCR 包的写权限。当前没有远端 CI run、正式 Release URL 或已推送的镜像 Digest。本文件及 `docs/evidence/remote-release-readiness.json` 只报告准备情况。

## 需要确定的具体配置

| 配置 | 需要提供或完成的值 | 验证方式 |
|---|---|---|
| GitHub 仓库 | 明确 `OWNER/REPO`、公开/私有、现有仓库的管理人；配置 `origin` | `gh repo view OWNER/REPO --json nameWithOwner,defaultBranchRef,viewerPermission` |
| 提交与默认分支 | 首次提交包含源码、锁文件、工作流和脱敏证据；选择默认分支，按 Lore 协议提交 | `git rev-parse HEAD`、远端默认分支及 SHA |
| Actions 策略 | 仓库启用 Actions；组织允许所列官方/第三方 action 及固定 commit；允许 GitHub-hosted runner 和外部依赖下载 | 仓库/组织 Settings → Actions；只读 API 不可见时由管理员核对 |
| 原生 runner | `ubuntu-24.04`（amd64）、`ubuntu-24.04-arm`（arm64）；可用并发与分钟额度；VM 内 sudo、iptables、Docker 网络能力 | 四个 Engine/架构 job 的实际 runner、内核与工具报告 |
| 网络与磁盘 | HTTPS 访问 GitHub/API/Release downloads、Go proxy/校验服务、pnpm registry、Docker Hub、download.docker.com；每个 job 足够容纳镜像与 Go 缓存，建议至少 14 GiB 空闲 | job 下载、构建日志；不足时升级 runner 容量 |
| 发行版本 | 当前脚本和镜像默认固定为 `v0.3.0`；建立指向已通过 CI 的同一 commit 的 tag | `release-prepare.yml` 拒绝其他版本，并核对 tag 的精确 SHA |
| 镜像目的地 | 明确小写 `ghcr.io/OWNER/docker-manager`，包公开/私有、归属与关联仓库 | GHCR 包 Settings → Manage Actions access；已有同名包必须授予此仓库访问 |
| 部署镜像引用 | 发布后的 Compose/image 与 `DM_BACKUP_WORKER_IMAGE` 指向同一获准版本或 Digest；当前默认是本地 `docker-manager:v0.3.0`，直接拉 GHCR 名称时必须显式覆盖 worker 引用并预拉到对应主机 | 两架构各实际检查 worker `image inspect` 并执行 Volume 备份/恢复 |
| 发布权限 | 创建 Release 需要目标仓库 contents 写权限；GHCR workflow 发布需要 packages 写权限与包访问；本地 CLI 推包需要适用的 classic PAT `write:packages`，组织 SSO 按要求授权 | 实际目标权限核对；本机 repo/workflow OAuth scope 不能代替 GHCR 授权 |
| 发布审批 | 指定发布负责人、是否使用受保护 `release` Environment/required reviewers、允许的发布 tag、可见性和回退保留规则 | 仓库管理员确认；当前 preparation 工作流没有写权限，也不会创建该 Environment |
| 验收/安全证据 | 归档同一源码与产物的 Linux/内核报告、checksums、CI URL；阅读 `docs/security-notes.md` 中尚未清零的依赖告警 | 不把本地验证或共享内核结果写成远端、独立内核或零告警通过 |

[GitHub 官方 runner 表](https://docs.github.com/en/actions/reference/runners/github-hosted-runners) 列出两种所需 runner，私有仓库使用其 Actions 额度。GitHub-hosted Ubuntu job 是临时 VM；当前 CI 不把 Ubuntu runner 当作 Debian 独立内核验收。Debian 独立内核报告由专用隔离 VM 验收另行提供，见[发布验收范围](release-acceptance.md)；若要求每次远端 CI 重跑 Debian 12/13 开机、升级和 systemd 场景，需再配置可创建/销毁这两种完整 VM 的专用设施、镜像和凭据，以及受信任手动入口，不能直接在共享业务 self-hosted runner 上运行平台安装脚本。

本轮 amd64 独立内核已通过完整 QEMU/TCG VM 补测，原生 CPU 运行另有 LXD 证据。如发布方还要求在原生 amd64 完整 VM 复跑，需提供专用 x86_64 KVM/虚拟化宿主或可销毁的 Debian 12/13 VM：至少 2 CPU、2 GiB 内存、10 GiB 磁盘，guest 有 sudo 和普通 Docker bridge/firewall 能力。KVM 宿主须开放可访问的 `/dev/kvm` 及 VMX/SVM；当前可达的嵌套测试宿主未开放这些能力，不能把本轮 TCG 结果写成 KVM 或裸机性能验收。这项原生硬件复跑与已完成的独立内核功能验收分别记录。

## 已准备好的远端工作流

`.github/workflows/ci.yml` 在 push、pull request 或手动触发时运行锁文件、前端类型/测试/构建、Go 测试/race/vet/格式与脚本检查。真实集成是原生 `amd64/arm64 × Engine 28.5.1/29.5.2` 四格，直接运行现有 `release.sh` 生成的本机架构正式二进制，Compose 2.40.3、Restic 0.19.1 校验上游 checksums 后安装。Engine 使用 Docker 官方指定版本静态包，报告记录下载文件 SHA256；该 SHA 是传输后摘要，不冒充上游签名验证。

每格在 GitHub 临时 VM 建立自己的 Engine 数据目录、执行目录、Socket 和 bridge，不重配 runner 的原 daemon；任务结束只停止该 PID 并删除本 job 创建的 bridge。Docker 将[单主机多 daemon](https://docs.docker.com/reference/cli/dockerd/#run-multiple-daemons)列为实验能力，网络规则可能相互影响，因此这个步骤只用于一次性的 GitHub-hosted VM，不应用在业务宿主机。集成报告、Engine 日志、系统/内核、工具版本和 worker 镜像元数据分别归档 14 天。CI action 均固定 commit，额外校验器固定 actionlint 1.7.12；Buildx 固定 0.29.1。

`.github/workflows/release-prepare.yml` **只有手动入口**，要求既存 `v0.3.0` tag，且精确 commit 已有成功完成的 `ci.yml` push/手动 run。它复用现有构建脚本生成 Linux 双架构二进制、双架构离线镜像包、安装/运维材料、工具版本、安全说明、CI URL 与 `RELEASE-SHA256SUMS`，只上传 Actions artifact（保留 14 天）。它没有 contents/packages 写权限，不登录 registry、不推镜像、不创建或公开 Release。需要长期保留时，维护者应下载审核后的候选包。

手动 workflow 需先存在于默认分支，具有仓库写权限的操作者才能从 UI/CLI 触发；参见 [GitHub 手动运行说明](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/manually-run-a-workflow)。配置目标并完成获准提交/推送后，可使用下列模板，当前没有执行：

```sh
# OWNER/REPO、BRANCH 和 COMMIT 均为维护者确认的真实值。
gh workflow run ci.yml --repo OWNER/REPO --ref BRANCH
# 确认实际 CI 成功，然后把 v0.3.0 tag 推到同一个 COMMIT。
gh workflow run release-prepare.yml --repo OWNER/REPO --ref BRANCH -f release_tag=v0.3.0
```

本轮已在独立 Debian 13 arm64 VZ VM 中预演这个私有 Engine 方案：28.5.1 与默认 containerd image store 的 29.5.2 各完成 16 项真实集成，固定 Buildx 0.29.1 在两版 Engine 上均构建、加载并运行了原生测试镜像。已验证原 daemon 的 ID、配置摘要和库存对照一致，专属 daemon、Socket、bridge 和数据目录已清理。预演修复了 `--bridge`/`--bip` 互斥问题；本地小容量 tmpfs 引发的 ENOSPC 已改用 guest ext4 重跑。该结果及明确限制见[准备证据](evidence/remote-release-readiness.json)，不等于 GitHub job、runner 分配或发布权限已经通过。

## 只读 preflight

```sh
python3 scripts/release-preflight.py \
  --actionlint /path/to/actionlint \
  --online --repo OWNER/REPO --tag v0.3.0 \
  --registry ghcr.io/owner/docker-manager \
  --report /tmp/docker-manager-release-readiness.json
```

不传目标时检查本地状态并列出缺项；`--online` 仅通过既存 gh 登录只读查询账户、目标仓库/Actions、精确 SHA 的 CI 及远端 tag。报告不保存 token、原始认证输出或带凭据的 remote URL。退出码 0 表示所检查门禁通过，1 表示产物/校验等检查失败，2 表示缺配置或尚未验证。正式发布始终保留人工发布门禁；preflight 不用一次试推来探测 registry 权限。

## 候选物审核后的正式签发

发布负责人先下载候选 artifact，并运行 `sha256sum --check RELEASE-SHA256SUMS`。二进制在 Release 中使用原有文件名；安装材料和完整证据随同归档。镜像包包含 `docker-manager:v0.3.0-amd64` 与 `docker-manager:v0.3.0-arm64`，可在获准登录 GHCR 后分别打 tag/push，再使用 `docker buildx imagetools create` 建立一个同时含 `linux/amd64`、`linux/arm64` 的 `v0.3.0` manifest。随后用 `imagetools inspect` 检查两架构及 Digest，分别拉取运行验证；将最终 registry Digest 和 Release URL 记录回验收矩阵。

GHCR 支持仓库关联的 `GITHUB_TOKEN` 发布，但既有同名包的关联/Actions 权限必须正确；首次创建的包默认私有，既存包可能已有其他可见性。因此推送包本身也属于发布动作，不能把“创建 draft Release”当作镜像不会对外可见的保证。参见 [GHCR 官方认证、关联与可见性说明](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)。

GitHub Release 可先使用 `gh release create v0.3.0 --verify-tag --draft ...` 创建草稿，审查后由负责人明确签发；`--verify-tag` 可防止 CLI 自动创建缺失 tag。具体目的仓库、Release 内容/可见性和 registry 归属未确定前，不执行这些写入。参见 [gh release create 官方说明](https://cli.github.com/manual/gh_release_create)。
