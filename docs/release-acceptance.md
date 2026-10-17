# 发布验收复现与证据范围

验收使用固定版本 Go 1.27.2、Compose 2.40.3、Restic 0.19.1，以及实际运行的 Engine 28.5.1 / 29.5.2。应用运行不依赖 Node 或 Python；Python 3.11 以上仅供验收脚本使用。镜像构建需要 Docker Buildx，脚本会显式检查，避免误用不能解释 `BUILDPLATFORM` 的旧构建器。

## 平台证据

| 系统 | 架构 | 测试环境 | Engine | 证据 |
|---|---|---|---|---|
| Ubuntu 24.04 | arm64 | 独立 VZ VM | 29.5.2 | [平台报告](evidence/platforms/ubuntu24-arm64.json) |
| Debian 12 | arm64 | 独立 VZ VM，Debian 6.1 内核 | 28.5.1 | [平台报告](evidence/platforms/debian12-arm64.json) |
| Debian 13 | arm64 | 独立 VZ VM，Debian 6.12 内核 | 29.5.2 | [平台报告](evidence/platforms/debian13-arm64.json) |
| Ubuntu 24.04 | amd64 | 完整 QEMU/TCG VM，Ubuntu 6.8 内核 | 29.5.2 | [平台报告](evidence/platforms/ubuntu24-amd64-kernel.json) |
| Debian 12 | amd64 | 完整 QEMU/TCG VM，Debian 6.1 内核 | 28.5.1 | [平台报告](evidence/platforms/debian12-amd64-kernel.json) |
| Debian 13 | amd64 | 完整 QEMU/TCG VM，Debian 6.12 内核 | 29.5.2 | [平台报告](evidence/platforms/debian13-amd64-kernel.json) |

新增 amd64 三套环境的完整集成另各 16/16，通过摘要及原始报告链接见 [独立内核汇总](evidence/amd64-kernel-acceptance.json)。这 48 项与主平台矩阵 60 项分开计数；历史 LXD 的 30 项也单独保留。

每份报告分别记录二进制和正式镜像安装、创建管理员/仓库/定时策略、真实备份与恢复、系统重启后的自动启服和全部管理数据保留、升级失败停服、匹配数据库/密钥/配置/程序的恢复、镜像删除重建持久化，以及卸载保留和测试清理。

六个主矩阵环境均重启完整 VM，报告比较内核 boot ID 与 systemd PID1。ARM64 使用原生 CPU 的 [Lima VZ](https://lima-vm.io/docs/config/vmtype/vz/)；amd64 使用 ARM 主机上的 QEMU 全系统 TCG，拥有各发行版独立内核、普通 Docker bridge/防火墙与端口发布。所有文件元数据验证均在 guest 原生 Linux 文件系统进行。TCG 结果证明独立内核功能兼容性，不用于原生硬件性能认证。

原生 x86_64 CPU 的历史 LXD 报告另保留：[Ubuntu](evidence/platforms/ubuntu24-amd64.json)、[Debian 12](evidence/platforms/debian12-amd64.json)、[Debian 13](evidence/platforms/debian13-amd64.json)。这些环境共享 Ubuntu 6.8 内核、使用受限嵌套网络，只有用户态/原生指令运行及各自 PID1 重启证据；不再充当主矩阵的独立内核证明。原 LXD 测试项目已清理，未修改宿主业务 Docker。

## 安装、重启、升级与镜像持久化

当前源码构建的可执行文件为 `bin/docklane`，发布二进制为 `docklane-linux-amd64` / `docklane-linux-arm64`，精简包内仅含 `docklane`。安装与升级脚本安装 `/usr/local/bin/docklane`，并处理旧二进制路径切换；systemd 单元、服务账号、配置/数据目录、镜像标签和 `DM_*` 标识保留兼容。历史验收报告保留当时的产物名称与摘要，不因更名改写证据。

`scripts/platform-acceptance.py` 必须在明确隔离的 Linux guest 内以 root 运行；首次安装阶段拒绝已有应用安装及数据。源码目录包含 `scripts/` 与 `deployments/`，先准备实际 Docker、固定版本工具、已校验二进制和本机架构正式镜像。

```sh
export DM_PLATFORM_ISOLATED=1
export DM_PLATFORM_ENVIRONMENT=vm
python3 scripts/platform-acceptance.py --phase prepare \
  --state /var/lib/dm-platform-run1 --source "$PWD" \
  --binary "$PWD/bin/release/docklane-linux-arm64" \
  --image docker-manager:v0.3.0 --fixture-image docker-manager:v0.3.0 \
  --report /var/lib/dm-platform-run1-report.json
# 在外部真正关闭并启动这个测试 VM，然后依次执行同样参数：
# --phase resume、--phase migration、--phase docker、--phase cleanup
```

`migration` 使用真实 SQLite BEFORE INSERT 触发器在迁移事务内注入 SQL 失败，再通过实际升级脚本验证停服、一致备份及迁移版本未改变。恢复匹配的一组文件后仅移除本轮注入的触发器，重新启动并核对用户、会话、仓库、策略、历史和密钥。首版只有 schema v1，没有虚构旧业务版本；历史业务版本之间的升级要在未来版本发布时继续验证。

## amd64 独立内核的复现边界

完整 VM 使用 Lima 2.2.1、QEMU 11.1.2，设置 `vmType: qemu`、`arch: x86_64`、2 CPU、2 GiB 内存、10 GiB 磁盘、`plain: true`，关闭 containerd 与共享挂载。基础镜像 URL、完整 SHA-512/SHA-256、实际 QEMU `-accel tcg` 和 CPU 参数见 [环境来源](evidence/platforms/amd64-kernel-provenance.json)。镜像校验后创建本次专用 VM；程序、源码和正式镜像包经 SCP 传入，测试文件和加密仓库留在 guest 原生文件系统。服务使用的外部工具安装在 `/usr/local/bin` 等位置，避免被 systemd `PrivateTmp` 隔离。

在 guest 内准备固定版本 Engine、Compose、Restic 并加载本机架构正式镜像，保持正常 Docker bridge、防火墙和随机发布端口。平台脚本设置 `DM_PLATFORM_ENVIRONMENT=emulated-vm`，使用 `docklane-linux-amd64`，依次运行上述五阶段；`prepare` 后从外部执行 `limactl stop <本次VM>` 和 `limactl start <本次VM>`。报告必须证明内核 boot ID 改变、PID1 为 systemd、原容器 ID 和全部管理数据保留。阶段交接必须等待 `limactl stop` 成功退出，再检查 `Stopped`；单独轮询到该状态仍可能抢先重启，导致前一个停止命令超时。

平台清理后，再以专用非 root、具有 Docker Socket 访问权的测试账号运行完整集成；工具、worker 和临时文件仍都在 guest 内：

```sh
DM_TEST_DOCKER_ENDPOINT=unix:///var/run/docker.sock \
DM_TEST_RESTIC=/usr/local/bin/restic \
DM_TEST_WORKER_IMAGE=docker-manager:v0.3.0 \
DM_TEST_REPORT=/var/tmp/dm-integration-report.json \
DM_TEST_TMPDIR=/var/tmp \
python3 scripts/integration.py --binary /opt/release/docklane-linux-amd64
```

该环境使用 [Lima QEMU 驱动](https://lima-vm.io/docs/config/vmtype/qemu/)和 [QEMU 全系统模拟](https://www.qemu.org/docs/master/system/introduction.html)，运行独立发行版内核；TCG 软件 CPU 模拟不用于证明原生硬件性能。原生 x86_64 CPU 的 LXD 结果作为补充保留，两种环境分别报告。

## 资源、工作流和跨主机恢复

`scripts/resources-acceptance.py` 覆盖真实日志/TTY、数据保留、引用保护、异步扫描及映射边界。`scripts/private-pull-acceptance.py` 使用仅绑定回环地址的 Registry v2，生成唯一随机镜像层，核对私有认证、新层下载进度、Digest 和错误脱敏；不会修改 daemon 配置。

`scripts/workflows-acceptance.py` 覆盖 Compose 重建、共享写入者、ENOSPC/EACCES、损坏、错误口令、原位失败持停、递归拒绝与离线 Cron。夏令时另由真实 Linux 测试二进制执行时区规则与 UTC 实例去重。`scripts/integration.py` 的工作台库存对比需要独占资源变更窗口；即使 namespace 不同，并行创建其他测试容器也会破坏该全局断言。

`scripts/cross-host-recovery.py` 只接收传输后的加密仓库、0600 离线口令材料和源文件期望值；在另一台 Linux 环境创建全新 SQLite，发现仓库内验证标记和 manifest，恢复到新目录并独立核对内容、权限及 UID/GID。源应用 SQLite 和 master.key 不属于传输材料。

所有报告记录具体工具、CPU、系统、开始/结束时间和产物摘要；以 [验收矩阵](acceptance-matrix.md) 与 [验证记录](verification.md) 为索引。本地 CI 专属 Engine 的两版预演见 [准备证据](evidence/remote-release-readiness.json)，缺失的仓库、commit/tag、runner、registry 与发布配置见 [具体要求](remote-release-requirements.md)。未配置这些目标，因此本地验收不包含远端 CI 实跑、镜像推送或正式签发发布。
