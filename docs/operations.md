# 安装、权限、升级与灾难恢复

## 一键在线安装

支持 Ubuntu 24.04、Debian 12/13，架构为 `amd64` / `arm64`，要求 root 和正在运行的 systemd。在线入口位于公开仓库，默认安装最新正式 GitHub Release；解析版本后固定该版本的下载地址，所有安装材料与 worker 镜像必须来自同一 Release。

```sh
curl -fsSL https://raw.githubusercontent.com/Funnyrz/docklane/main/scripts/install-online.sh | sudo bash -s --
```

脚本在修改前检查系统与架构，下载后检查 SHA-256、架构及归档路径；缺少资产、下载中断、校验不符或不安全归档时停止。检查和安装过程不依赖交互输入，可用于 `curl | sudo bash`。首次访问页面仍需自行创建管理员，不创建默认密码。

| 参数 | 行为 |
| --- | --- |
| `--version v0.3.0` | 安装指定正式版本；省略时使用一次解析得到的最新正式版本 |
| `--listen :8080` | 设置监听地址；新装默认 `:8080`，已有安装保留原值，显式指定时覆盖 |
| `--migrate-from /绝对路径/data` | 显式迁移手动实例的完整应用数据；保留源目录 |
| `--config /绝对路径/config.json` | 提供配置；迁移时用于无法可靠还原旧启动配置的情况 |
| `--dry-run` | 预览检查结果与计划，不安装工具、写配置、停止实例或注册服务 |
| `--help` | 查看参数和使用说明 |

脚本补齐基础工具（包括 Python 3）。Docker 未安装时从 Docker 官方软件源安装稳定 29.x 并启用开机自启；已有可用 Docker 28/29 不升级、不重启，冲突安装明确报错。复用 Compose v2，缺失时安装 v2.40.3；使用 Restic 0.19.1，其他应用的不同版本不被覆盖。Docklane 专用工具与同 Release 的 worker 镜像由脚本准备，镜像加载后检查版本和架构。

安装路径为 `/usr/local/bin/docklane`，账号和服务名为 `docker-manager`，默认配置为 `/etc/docker-manager/config.json`，默认数据目录为 `/var/lib/docker-manager`。服务以专用账号运行，保留 `ProtectHome=true` 等限制；不授予 daemon 配置写权限，不改防火墙，不递归修改外部业务目录的所有者。使用外部 Compose、备份源、仓库和恢复目录前仍需显式授权路径与文件权限。

结束时脚本检查 `systemctl is-active`、`systemctl is-enabled` 和有界 `/healthz` 请求，成功后显示访问地址、配置位置及日志命令。`:8080` 监听所有网卡，请通过可信网络或 HTTPS 反向代理访问。服务器重新启动后由 systemd 自动启动，无需再次运行脚本。

```sh
sudo systemctl status docker-manager
sudo journalctl -u docker-manager -f
sudo systemctl restart docker-manager
sudo systemctl stop docker-manager
sudo systemctl disable docker-manager
```

### 手动实例迁移

已有手动部署不要直接执行全新安装。先预览迁移，再使用相同参数去掉 `--dry-run` 执行：

```sh
curl -fsSL https://raw.githubusercontent.com/Funnyrz/docklane/main/scripts/install-online.sh \
  | sudo bash -s -- --migrate-from /home/ubuntu/docklane/data --dry-run
```

若无法可靠取得旧实例配置，追加 `--config /绝对路径/config.json`。配置中的相对路径按旧工作目录转换为绝对路径，应用数据和临时目录按受管安装调整；有效配置、数据库内保存的设置及密钥继续保留。不要为了通过路径检查，把旧配置替换成空白示例。

迁移前检查外部 Compose 项目、备份源与仓库是否能被专用账号访问。`ProtectHome=true` 会屏蔽 home 下的业务路径，即使 Unix 权限允许也无法访问；先安排迁移到 `/srv` 等适当的授权目录。脚本不会自动放宽隔离或对这些路径递归 `chown`。非空目标数据目录、未知进程或无法还原的配置会使迁移停止。

对正在运行的来源，脚本核对数据锁、持锁进程和程序身份，只对确认的进程发送 SIGTERM；等待正常退出并取得真实文件锁后才备份和复制。PID 不匹配、身份不明或退出超时都停止，不强杀进程。迁移需要停机窗口；不要在迁移过程中重新启动旧实例。

完整停机备份包含数据库、存在的 WAL/SHM、`master.key` 及其他管理数据。源目录保留，配套配置与备份位置在输出中给出。迁移后确认已有管理员可登录、设置保留，并进行一次备份与新目录恢复验证。原始数据副本包含秘密，应按应用数据同等保护。

### 重复执行与在线升级

使用原命令可安装最新正式版本，也可固定版本：

```sh
curl -fsSL https://raw.githubusercontent.com/Funnyrz/docklane/main/scripts/install-online.sh \
  | sudo bash -s -- --version v0.3.0
```

标准受管安装会保留现有配置和实际生效的数据目录；同版本且二进制 SHA-256 一致时跳过替换。版本变化先停服务，再备份实际数据目录、配置、服务单元和旧二进制，之后安装同版本材料并检查健康。显式指定 `--listen` 才更改已有监听地址。需要降级时应按下方回退说明恢复配套备份，不能用旧程序直接打开升级后的数据库。

安装或升级失败返回非零退出码，停止未通过健康检查的新服务，并保留源数据及备份。先阅读错误输出与日志，不反复重启或混合恢复数据库和密钥。管理员按下方[升级和回退](#升级和回退)区分受管升级与首次手动迁移恢复；首次迁移可能没有旧服务单元或旧二进制备份，不能直接套用 systemd 回退步骤。失败前已停机的来源不会被强行启动；恢复由管理员确认。

## 离线安装

离线安装保留单参数入口。提前准备可用的本地 Docker Engine 28/29、Compose v2（验证版本 v2.40.3）、服务 PATH 可用的 Restic 0.19.1、Python 3 与 `runuser`（util-linux）；`docker` 组须存在，服务账号须能通过该附加组访问 Docker Socket。下载同一 Release 的原始二进制、`docker-manager-installation.tar.gz`、`docker-manager-images.tar.gz`、`IMAGE-SHA256SUMS`、`IMAGES.txt` 与 `RELEASE-SHA256SUMS` 到空目录，先校验资产，再加载并为本机架构设置必需的 worker 通用标签：

```sh
sha256sum --check --ignore-missing RELEASE-SHA256SUMS
sha256sum --check IMAGE-SHA256SUMS
tar -xzf docker-manager-installation.tar.gz
sudo docker load -i docker-manager-images.tar.gz
sudo docker tag docker-manager:v0.3.0-amd64 docker-manager:v0.3.0
sudo ./scripts/install.sh ./docklane-linux-amd64
```

arm64 将二进制文件名和镜像标签中的 `amd64` 改为 `arm64`。精简二进制包只包含 `docklane` 和 `LICENSE`；需要 `scripts/`、服务文件和辅助程序时必须同时下载安装材料，不应混用 `main` 上的服务文件与旧版本程序。

## 授权与路径

`DM_CONFIG` 指定 JSON 配置文件。环境变量可覆盖 listen、data_dir、docker_endpoint、mode、compose_path、restic_path、backup_worker_image；`DM_ALLOWED_PATHS` 为逗号分隔的显式绝对路径；`DM_PATH_MAPPINGS` 为 host/local 对象数组的 JSON。根目录 `/` 不能作为授权路径。

也可在“系统设置 → 配置功能”编辑允许目录、目录映射，以及 Linux 二进制部署下的宿主机操作授权。输入当前管理员密码并确认后，保存到应用数据库的 `capability_settings_v1` 设置项；**页面保存的这三个字段优先于配置文件和环境变量，重启 Docklane 后生效**，其他配置仍按原加载方式处理。页面分别显示当前生效值与待生效值；“使用当前生效值”并保存可撤销待生效修改。运行中的备份/恢复沿用本次启动时的配置。容器实际挂载、运行工具和系统账号权限由部署环境提供，页面授权不替代这些条件，也不触发 Docker 重启。

`log_level` / `DM_LOG_LEVEL` 接受 debug、info、warn、error，服务向标准错误输出结构化日志。`backup_temp_dir` / `DM_BACKUP_TEMP_DIR` 默认是数据目录内的 `tmp`，应用建立私有目录并将子进程临时文件和 Restic 缓存放入其中。目录必须有足够空间；不要指向宿主机根目录或公用业务目录。应用数据目录持有进程锁，同一目录不能同时运行两个实例。

数据目录必须可写，应用设置权限 0700，数据库和主密钥 0600。不要将已有通用业务目录用作 data_dir。应用支持的密钥文件必须随应用数据备份。

二进制可执行文件为 `docklane`，systemd 安装路径为 `/usr/local/bin/docklane`。为兼容已有安装，服务名与账号仍为 `docker-manager`，配置和数据目录仍为 `/etc/docker-manager` 与 `/var/lib/docker-manager`。二进制示例默认使用 docker-manager 账号；对需要备份的目录授予读取权限，对仓库/恢复目录授予写入权限。Docker Socket 的访问属于高权限。非本地驱动卷需要单独适配，首版不隐式访问远程驱动。

容器模式的路径填写宿主机路径，配置显式 host/local 映射。例如 `/srv/backups` → `/backups`。Compose 相对 Bind 路径要求保留原宿主机工作目录语义，不允许通过错误映射部署到意外位置。

非 root 服务备份其他 UID 的本地 Volume 时，临时执行容器只获得指定只读数据源、可写仓库和私有临时目录，并以仓库所有者身份写入仓库。恢复不同 UID/GID 的文件需要受限恢复容器：仓库只读、仓库锁目录可写、恢复目标可写，禁用网络，不挂载 Docker Socket；授予恢复文件元数据必需的 Linux capabilities。缺少镜像、Docker 权限或能力时任务失败，不将未校验的恢复标记成功。运行镜像和 `backup_worker_image` 应使用相同版本。

本地构建和离线归档使用 `docker-manager:v0.3.0`；若以后从正式 registry 安装，需同时将应用镜像与 `DM_BACKUP_WORKER_IMAGE`（二进制模式为 `backup_worker_image`）设为实际发布的完整镜像引用，建议固定已核对架构的 Digest。只拉取 `ghcr.io/…` 不会生成默认的本地 tag。启动后先执行一次 Volume 备份和新目录恢复，确认 worker 镜像可用。远端仓库、权限和签发要求见 [发布配置要求](remote-release-requirements.md)。

Docker 模式应将目录备份源单独只读挂入管理容器，将仓库和恢复目录分别可写映射；不要将三者共同的父目录可写挂入面板。停机备份会拒绝停止面板自身或共享其应用数据的容器；无法确认自身身份时也会拒绝，以免停止执行备份和补偿的进程。需要在线文件备份时可显式选择 file 模式并接受一致性风险。临时备份容器将 HOME、临时文件和 Restic 缓存放在私有 `/tmp` tmpfs，兼容只读根目录。

## daemon 操作

Docker 模式永远不允许自动写宿主机 daemon 配置。二进制模式还需要 enable_daemon_write=true、Linux、dockerd/systemctl 和实际文件/服务权限；默认 systemd 专用账号不具备这些权限。需要此功能时应由管理员配置明确的受控授权，不建议为便利将整个服务改为 root。

修改前预览并确认重启风险；保留其他字段、备份原配置、验证候选配置、原子替换、重启并验证。失败回退仍可能影响运行容器。手动操作遵循同样步骤，不能承诺零停机。

## 两类密钥

1. **应用 master.key**：加密 SQLite 中的仓库口令、仓库凭据和敏感执行上下文。将 data_dir/master.key 与数据库作为匹配的一组离线备份；不要写入代码仓库、普通日志或不加密的工单。指纹不匹配或文件丢失时启动拒绝继续，避免生成新密钥覆盖关联。
2. **Restic 仓库口令**：仓库初始化时输入，使用恢复材料导出功能离线保管。独立恢复仓库不需要原应用 master.key 或 SQLite，但必须拥有正确 Restic 口令。

导出恢复材料包含秘密，下载后保存在加密存储中；禁止把导出内容粘贴到日志。没有仓库口令不能解密备份。

## 独立灾难恢复

保留完整 Restic 仓库和离线口令，在新主机启动应用，授权仓库与新恢复目录；使用“导入已有仓库”，浏览快照并读取仓库内版本化 manifest；默认恢复到新目录。验证内容和必要元数据后，按预检选择重建服务，确认镜像、端口、网络和挂载。应用健康失败不能当作数据恢复成功后的自动成功。

也可直接使用锁定版本 Restic：设置私有口令文件，执行 snapshots、check --read-data、restore 到新目录。避免将口令写入 Shell 参数或历史记录。仓库结构检查不等于实际数据读取校验。仓库导入不自动删除快照，原位覆盖不等同于普通新目录恢复。

仓库中的 `dm-verified:v1` 标签表示该快照完成了面板规定的数据读取校验及写入者恢复。失败、部分完成或旧版无标记快照会显示“未完成验证”，不会挤占成功备份的保留名额；救援它们必须额外确认，只能写入新目录，并自行检查缺失文件。不要手动添加验证标签来绕过门禁。标签不代表数据库逻辑一致性，也不代替恢复后的服务健康检查。

## 升级和回退

在线升级见上方流程；离线升级先校验发布 `RELEASE-SHA256SUMS`。二进制使用 `scripts/upgrade.sh /path/to/docklane`，同时保留该版本的 `deployments/docker-manager.service`。脚本停服务后按实际生效的数据目录备份完整数据、配置、原服务单元及存在的 `docklane` / `docker-manager` 二进制，再安装 `/usr/local/bin/docklane` 和匹配的服务单元，执行 daemon-reload 后启动，并在有界等待内检查服务与 `/healthz`。旧 `/usr/local/bin/docker-manager` 保留作为回退材料；已有服务会迁移到新入口，无需迁移数据目录。

已有 systemd 受管安装升级失败时，保持停止状态；从同一次备份恢复配置、完整数据目录（含数据库、存在的 WAL/SHM 及 `master.key`）、原服务单元和对应旧二进制到原路径，再执行 `systemctl daemon-reload` 和 `systemctl start docker-manager`。旧服务单元可能仍指向 `/usr/local/bin/docker-manager`，必须与备份二进制匹配，避免直接用旧程序打开新版本数据库。

首次手动实例迁移失败时，先确认新建的 `docker-manager` 服务已停止；若已注册服务，取消其开机自启，避免恢复旧实例后再次启动新目标。保留失败目标目录用于检查，不把其中的数据库或密钥混拷回原目录。确认原手动二进制仍可用，按原运行账号和工作目录，使用保留的源数据目录以及同次备份的 `startup-config.json` 恢复原实例（通过 `DM_CONFIG` 指定，并避免遗留 `DM_*` 环境变量覆盖配置）。若原二进制路径被替换，使用同组备份中的对应旧程序。首次迁移备份可能没有旧服务单元或旧二进制；没有旧单元时无需恢复单元，也不应以 `systemctl start docker-manager` 代替原手动启动方式。若选择从备份恢复源数据，须让两个实例都保持停止，并完整恢复同一备份组的数据库、WAL/SHM、`master.key` 及其他管理数据；不要混用源、失败目标与不同备份组。

使用 Volume 备份/恢复的二进制部署还需加载或更新本次发布的同版本 worker 镜像：新程序使用镜像内的 `docklane` 入口，旧镜像即使标签相同也不兼容，可从完整离线包导入对应镜像。

容器升级前停服务并备份持久化应用目录，然后替换镜像；备份仓库与应用数据分别保留。数据库迁移失败不删除原数据。应用版本比数据库旧时拒绝启动。

`scripts/uninstall.sh` 移除新旧两个可执行文件及服务单元，默认保留账号、配置、应用数据及备份仓库。仅在确认独立备份可恢复后，人工清理不再需要的数据。

## 故障排查

- Docker 不可用：检查 endpoint、Socket 是否存在、账号组权限及 Engine 状态；不要把页面空统计理解为资源丢失。
- 工具缺失：检查 docker compose version、restic version 及服务 PATH。
- 中断任务：查看任务阶段、补偿日志及原运行状态；不直接重试原位恢复或容器重建。
- `RECOVERY_REQUIRED`：补偿尚未完成，应用保留记录并重试；在完成前拒绝新的资源变更和调度，查询、诊断及退出登录仍可使用。先排查 Engine 连接、资源状态和路径权限。
- `RESTORE_VALIDATION_REQUIRED`：原位覆盖可能已有部分写入，原写入容器保持停止。人工验证数据或从可信快照重新恢复后再启动服务；不能仅凭文件恢复命令退出码认定服务可用。
- 容器启动失败：保留旧容器与任务记录；配置回退不能撤销应用写入。
- 仓库满：释放独立临时空间并检查保留策略；失败备份不触发破坏性清理。
- systemd 日志：journalctl -u docker-manager；确认密钥和备份凭据没有粘贴到配置说明或诊断输出。
