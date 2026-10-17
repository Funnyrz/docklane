# Compose、备份恢复与宿主机工作流 API

所有路径在 `/api/v1` 下，需要管理员会话；修改请求带 `X-CSRF-Token`。主机固定 `local`。异步操作返回 HTTP 202 的任务对象；失败不会返回模拟成功。错误为 `{error:{code,message}}`。命令使用独立 argv，不调用 Shell。

## Compose

| 方法/路径 | 请求与结果 |
|---|---|
| GET `/compose/discover` | 根据实时 Compose 标签发现；数组元素 `{project,candidate_yaml,candidate_status:"pending_validation",warning}`。候选 JSON 是合法 YAML，不代表可完整还原。 |
| GET `/compose/projects` | 已关联项目数组；`changed` 对比配置文件、显式 env 文件、`.env` 及 CLI 展开后的配置；列表不返回插值环境变量内容。 |
| POST `/compose/import` | `{name,workdir,config_paths:[],env_files:[],profiles:[],environment:{},confirm:true}`；仅 CLI config 校验和保存索引，不运行 up/down。 |
| GET `/compose/projects/:id` | `{project,files:[{path,content}]}`，用于显式编辑；可包含敏感配置，应在受信设备使用。 |
| PUT `/compose/projects/:id/files` | `{revision,files:[{path,content}]}`；仅可写已索引配置文件，保留权限，失败尽力回退前面文件。无效草稿可保存并继续修正，部署仍必须通过校验。 |
| POST `/compose/projects/:id/preview` | `{}` → `{revision,configuration,warning}`；展开 services/networks/volumes/env，文件变更后必须重新预览。 |
| POST `/compose/projects/:id/deploy` | `{revision,confirm:true}` → 任务；重验 revision、config，再 up --detach --wait，最后 ps 验证。 |

Project 核心字段：`id,host_id,name,source_type,workdir,config_paths,env_files,profiles,environment,revision,last_deployed_at,changed,unavailable`。数据库中的 environment 使用应用 Vault 加密。必须显式授权绝对路径；Docker 模式必须有 host/local 映射。部署及恢复 Compose 要求两端路径相同，避免相对 Bind Mount 在 Docker daemon 上被解释成错误宿主机路径。配置更新不能回滚已产生的数据写入。

## Restic 仓库与策略

| 方法/路径 | 请求与结果 |
|---|---|
| GET `/backup/repositories` | `{id,name,location,status}` 数组，不返回口令。 |
| POST `/backup/repositories` | `{name,location,password,import:false}` 初始化空目录；`import:true` 验证已有仓库口令，不需要原 SQLite。口令至少 12 字符，Vault 加密保存。 |
| POST `/backup/repositories/:id/export` | `{confirm:true}` → `no-store` 离线恢复 JSON，含仓库位置和明文口令；仅显式请求时返回，审计不包含内容。 |
| GET `/backup/repositories/:id/snapshots` | 直接从 Restic 发现快照，不依赖本地索引；附 `verification_status: verified\|unverified` 与 `verification_warning`。 |
| GET `/backup/repositories/:id/snapshots/:snapshot/metadata` | `{sources,containers:[{id,name,image,compose_project}],projects:[{id,name}],warning}`，不返回配置/环境变量，用于选择原位置源及重建对象。 |
| GET `/backup/policies` | Policy 数组。 |
| POST `/backup/policies` | 创建并持久化 Policy，下见字段。 |
| PUT `/backup/policies/:id` | 替换 Policy 定义，重新预检并更新调度，保留 ID 与历史。 |
| DELETE `/backup/policies/:id` | 删除计划，保留仓库与快照。 |
| POST `/backup/policies/:id/preview` | `{}` → `{revision,sources,stop_containers,warning}`，包括跨项目共享挂载的所有已知写入容器。 |
| POST `/backup/policies/:id/run` | `{confirm:true,revision}` → 任务；stop 模式必须传入预览 revision，范围变化后拒绝执行。 |
| GET `/backup/schedule-instances` | 最近 1000 次 `{policy_id,scheduled_at_utc,task_id,error}`；离线错过记录 `MISSED_OFFLINE`，不补跑。 |

```json
{
  "name": "夜间数据",
  "repository_id": "仓库 ID",
  "targets": [{"type":"volume","name":"data"}],
  "consistency_mode": "stop",
  "schedule": "0 2 * * *",
  "timezone": "Asia/Shanghai",
  "keep_last": 7,
  "excludes": [],
  "retries": 1,
  "retry_seconds": 30,
  "enabled": true
}
```

Target 支持 `directory` (`path`)、`volume` (`name`)、`container` (`name/ID`)、`compose` (`name` 为已关联项目 ID)。Compose 备份也包含工作目录以保留配置和文件依赖；工作目录中包含仓库会被拒绝。显式外部配置、CLI env 文件、service env_file 与 configs/secrets 文件也单独纳入源，源必须获路径授权；可选且不存在的 env_file 保持可选。恢复使用加密快照中保存的规范化 Compose 配置，保留原始多文件以供审查。`schedule:""` 表示仅手动；Cron 为五字段并附 IANA 时区。UTC 计划实例唯一去重，重复夏令时时刻按各自 UTC 实例区分；离线历史最多逐项记录 10000 次，更多按摘要记录。

仅实现 `file` 和 `stop`：file 明确提示在线数据库原始文件没有一致性保证；stop 枚举并持久化原先运行的写入者，停止后复查。在任务失败、取消和进程重启后补偿，只启动原来运行的容器。手动执行在真正停机前再次比较显示过的完整源与写入者；Cron 策略冻结创建/更新时的 allowed_writers，新增共享写入者会使执行失败，要求更新策略，不能静默扩大停机范围。宿主机其他进程不属于 Engine 可观察的写入者。

Volume 只支持无 driver options 的 local 卷。存在 Volume 或 Docker 模式时，通过临时 Restic worker 容器访问：仅选定源只读挂载、仓库显式挂载、无 Socket、无宿主机根目录、无网络、只读 rootfs、drop ALL，仅加 DAC_READ_SEARCH 读取应用所有者的文件。仓库为非 root 所有者时，通过内置 backup-worker 入口临时增加 SETUID/SETGID/SETPCAP，将 Restic 降为仓库所有者 UID/GID，仅保留 ambient DAC_READ_SEARCH，避免创建导致面板无法读取的 root 所有者目录。工作入口仅接受限定 backup 参数，不提供通用命令执行。不会自动拉取 worker 镜像，缺少已锁定镜像时明确失败。二进制的纯目录备份直接运行 Restic。

Manifest v1 在加密快照内包含源路径、原始 Inspect、Compose 上下文与一致性警告；SQLite 仅保存不含配置凭据的快照摘要。备份成功提交后重新从快照读取 manifest 并核对内容，避免 exclude 误排除恢复元数据；然后执行 `check --read-data`，完成运行状态恢复后才允许 `forget --prune`。退出码非零、无 summary/snapshot_id、读取校验失败均不得标记成功或执行清理。空间预检至少要求 64 MiB 可用；备份增量最终大小只能由 Restic 确定，磁盘耗尽作为真实失败处理。

## 恢复

POST `/backup/restore/preview` 请求：

```json
{"repository_id":"ID","snapshot":"完整快照 ID","target":"/授权的新目录","overwrite":false,"recreate":false}
```

返回 `{revision,manifest,files,target,overwrite,warning,verification_status,verification_warning,service_recreation_available,resources}`。预览中的容器敏感配置和环境变量被隐藏；完整 manifest 留在加密仓库及显式恢复的文件中。

快照只有完成 manifest 核对、`check --read-data` 和原运行状态恢复后，才写入仓库内 `dm-verified:v1` 标签。Restic 修改标签会改变快照 ID，应用使用返回的新完整 ID 保存索引；保留清理只计算同策略且带完成标签的快照。非零退出仍可能产生部分快照，这些快照保留可见，但不占成功快照的保留名额。旧版本没有标记的快照也按未验证处理，不能仅凭原 SQLite 的成功记录放行。

未验证快照默认拒绝恢复预览；明确传 `acknowledge_unverified:true` 后，只允许救援到新目录，禁止覆盖和服务重建。此参数也必须保留在最终恢复请求中；界面另外展示缺失数据风险。文件校验完成不代表在线数据库具备一致性。

POST `/backup/restore` 发送同样的预览参数，加 `revision`、`confirm:true`。原位置覆盖必须 `overwrite:true` 且 `confirm_overwrite` 精确等于完整目标路径；多个源时增加 `source`（所选 snapshot_path），单源可以省略。目标必须精确匹配该源原来的已授权宿主机路径。执行时再次预检，源/仓库/目标不能相互包含；默认目标必须不存在或为空，新目录恢复保留快照原绝对层级于目标目录之下；原位置模式使用 `SNAPSHOT:/source` 子树恢复，将内容直接覆盖原路径，不生成嵌套副本。快照之外原有文件保留。开始原位置写入前持久化 hold_writers；恢复失败/中断时清理 worker 与 staging，但保留原写入者停机，任务显示 RESTORE_VALIDATION_REQUIRED。管理员检查数据后可显式启动容器；仅完整内容与元数据验证成功后自动恢复原运行状态。Restic `restore --verify` 验证内容，应用再核对真实源子树的 mode/UID/GID。合成祖先目录没有源文件的元数据保证，不作为源元数据验收。

选择 `recreate:true` 可重建服务（不能同时 overwrite）：

- `container_ids`、`project_ids` 为原 manifest 的选择 ID 数组；省略表示全部，空数组表示不选择。
- `container_names`、`project_names` 为原 ID → 新名称；容器默认原名，Compose 默认带快照后缀的新项目名。
- `resources.containers` 展示 name/image/original_id/mounts；`resources.projects` 展示待恢复项目及新路径。
- 镜像必须已可用；同名资源、已占用端口、缺失网络、静态 IP 或特殊 privileged/device/共享 namespace 配置在执行前拒绝，避免丢失配置。
- 恢复的 Volume 明确转换为新目标目录中的 Bind Mount；原卷不会被覆盖。Compose 保留原配置文件，另写规范化恢复配置，切换到恢复数据与原镜像 ID；只保留已捕获容器的服务并以 --no-deps/--no-build/--pull never 执行，未部署过的服务不会通过恢复启动或触碰原挂载。未捕获依赖、冲突显式 container_name 和缺失外部网络会被拒绝；项目内部网络使用新项目名称。
- 新容器带任务恢复标签，失败或进程重启时仅清理本任务创建的容器，保留数据。Compose 可能创建的空网络/空卷保留供人工检查，防止误删共享对象。
- 内容验证结果和服务验证独立记录。配置了健康检查的服务必须 healthy；没有健康检查只能验证 running，不等于应用业务验证。

仅用仓库目录和离线口令，在全新安装导入仓库后即可发现快照并恢复；本地 SQLite 快照索引不是恢复前提。

## 镜像加速与宿主机配置

| 方法/路径 | 请求与结果 |
|---|---|
| POST `/host/diagnostics` | `{url,image?,confirm_pull?}` → `network` TCP 结果、`registry_api` /v2/ HTTP 结果、`image_pull` 实际拉取任务引用。401 表示 API 可达且需要认证，不代表镜像可拉取。 |
| GET `/host/daemon` | `{available,reason?,configuration?,revision?,manual_guide}`。Docker 模式只返回指引。 |
| POST `/host/daemon/preview` | `{mirrors:[]}` → `{revision,configuration,warning,requires_restart_confirmation:true}`。 |
| POST `/host/daemon/apply` | `{mirrors,revision,confirm:true,confirm_restart:true}` → 任务。 |

实际 pull 通过 Engine 当前的配置执行，不能声称尚未应用的 mirror 被使用。仅 HTTP(S) origin 可作为 mirror，禁止内嵌凭据/路径/query/fragment。mirror 主要作用于 Docker Hub。

写操作仅 Linux 二进制模式、显式 enable_daemon_write、本地 Unix Docker endpoint、可写的常规配置文件/父目录以及 dockerd/systemctl 可用时开放。合并保留未知字段与 JSON 数值精度；先 dockerd --validate，保存 0600 备份，检查 revision 后原子写入并重启，最多等待 90 秒恢复并验证 mirrors 生效。checkpoint 中的原配置 Vault 加密；失败及中断恢复原文件并重启。恢复失败由任务中心保留 RECOVERY_REQUIRED 并重试，不能当作普通失败结束。

## 验证证据与环境限制

快照列表与 metadata 增加 `verification_status: "verified" | "unverified"` 和 `verification_warning`。只有备份命令成功、仓库 manifest 核对、`check --read-data` 及原运行状态恢复全部完成后，才在加密仓库写入 `dm-verified:v1` 标签；不根据本地 SQLite 推断验证完成。旧版本无标签快照和失败任务留下的部分快照仍可浏览，但均显示未验证。未验证快照默认拒绝恢复；请求显式携带 `acknowledge_unverified: true` 时仅允许新目录文件救援，禁止原位覆盖和服务重建。

Restic 修改标签会改变快照 ID。后端读取 `tag --json` 的 `changed.old_snapshot_id/new_snapshot_id` 并复核新对象，SQLite 与任务日志均记录最终 ID。保留清理仅匹配同一策略且带验证标签的快照；未验证快照不占 `keep_last` 名额，也不会被自动清理。管理员需要单独评估这些历史或部分数据的保留空间。[Restic 官方 JSON/tag 说明](https://restic.readthedocs.io/en/stable/075_scripting.html#tag)

临时备份 worker 使用只读根文件系统时，显式设置 `HOME=/tmp`、`TMPDIR=/tmp`、`RESTIC_CACHE_DIR=/tmp/restic-cache`，缓存限定在私有 tmpfs。Docker 模式停机前必须可靠识别本安装所在容器；若写入者范围包含管理容器或共享同一安装数据的容器，则拒绝整个停机操作。建议单独将备份源只读映射、仓库和新恢复目录分别可写映射，避免管理容器成为备份源写入者。

`DM_TEST_RESTIC=/路径/restic go test ./internal/workflows` 运行真实 Restic 初始化、目录备份、全量读校验、删除本地快照索引、仓库 manifest 发现、内容恢复和源 mode/UID/GID 验证。其余测试覆盖受控 Compose 参数、多文件顺序、授权映射和符号链接越界、配置字段保留、Cron/时区、离线去重、恢复预览指纹、输出溢出、任务补偿和 manifest 路径边界。

macOS 工具链检查、fake Engine 测试与真实 Linux 验收分别记录。当前 Linux Volume、Compose、daemon 及双架构实测入口、产物身份和发行版内核边界见 [验证记录](verification.md) 与 [发布复现](release-acceptance.md)，不从编译或模拟结果推断实际部署通过。
