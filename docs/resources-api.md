# Resource API

All routes require an authenticated session. Mutating requests also require `X-CSRF-Token` matching the `csrf_token` cookie. Prefix: `/api/v1`. The only supported host is `local`; another `host_id` returns 404. Engine state is authoritative; disconnected Engine errors return errors, never fabricated empty inventories. Ordinary resource requests have a 30-second deadline. Streams stop at session expiration or session revocation.

## Containers

| Method | Path | Request / response |
|---|---|---|
| GET | `/hosts/local/containers` | `?page=1&page_size=50&q=&state=` → `{items: DockerContainerSummary[],total,page,page_size}`; includes stopped and externally created containers |
| GET | `/hosts/local/containers/:id` | Docker Inspect response, including mounts/networks/health/config |
| POST | `/hosts/local/containers/:id/start`, `/stop`, `/restart` | Empty body → HTTP 202 task; task verifies actual final Engine running state |
| DELETE | `/hosts/local/containers/:id` | `{confirmed:true}` → task; requires stopped container, never force-deletes and never removes data volumes |
| POST | `/hosts/local/containers/:id/config/preview` | `{changes:{...}}` → `{fingerprint,mode,changes,before:{config,host_config},after:{config,host_config},warnings}` |
| POST | `/hosts/local/containers/:id/config` | `{changes:{...},fingerprint,confirmed:true}` → task; fingerprint binds both configuration and requested changes, checked again before execution |
| GET | `/hosts/local/containers/:id/logs` | SSE; `tail=0..10000` (default 100), `timestamps=true`, `follow=true`, `since=`; `Last-Event-ID` accepted as `since` fallback |
| GET | `/hosts/local/containers/:id/stats` | Docker stats JSON; `?stream=true` emits SSE `stats` events until disconnect |
| GET | `/hosts/local/containers/:id/terminal` | WebSocket; requires matching `csrf_token` query value and same-host Origin; `shell=/bin/sh` (also `/bin/bash`, `/bin/ash`) |

`changes` accepts these fields; unknown fields are rejected:

```json
{
  "image": "nginx:stable",
  "env": ["MODE=production"],
  "cmd": ["nginx", "-g", "daemon off;"],
  "entrypoint": ["/docker-entrypoint.sh"],
  "labels": {"owner": "operations"},
  "working_dir": "/app",
  "user": "1000:1000",
  "hostname": "web",
  "port_bindings": {"80/tcp": [{"HostIp":"127.0.0.1","HostPort":"8080"}]},
  "mounts": [{"Type":"volume","Source":"existing-data","Target":"/data","ReadOnly":false}],
  "memory": 536870912,
  "nano_cpus": 1000000000,
  "restart_policy": {"Name":"unless-stopped","MaximumRetryCount":0}
}
```

Only memory, NanoCPUs and restart policy update in place. Other fields rebuild. `env`, labels, commands, port bindings and mounts replace their corresponding values; unspecified values are retained. Explicit `mounts` replaces both binds and structured mounts; sources must be authorized bind paths, existing named volumes or tmpfs. Port preflight checks other running Docker containers; Engine performs final host-port validation.

Compose-owned containers must be changed through the Compose project. Rebuild rejects Swarm ownership, auto-remove, links, volumes-from, shared container namespaces, fixed network addresses, transitional states and unsupported mount types. The original full inspect configuration is retained; anonymous volume names are explicitly reused. The original is stopped and renamed, the replacement is created, then its running state/health is checked before the stopped original is removed without deleting volumes. Failure removes only a task-labelled replacement and restores the original name/running state. Recovery checkpoints encrypt the original configuration. Restart recovery compensates interrupted work without replaying the rebuild. Application data writes and container writable-layer contents are not rolled back. If replacement verification succeeded but old-container cleanup fails, the task reports the retained original for manual cleanup.

Log streams demultiplex Docker stdout/stderr frames. SSE `log` payload is `{text}`; Docker timestamps become SSE IDs, enabling reconnect through `Last-Event-ID`. Log timestamps identify a reconnect boundary, so clients should tolerate duplicate boundary lines. `stream-error` carries an error if the upstream fails after headers. TTY logs use the unframed stream.

Terminal messages contain raw terminal input (binary or text). Resize uses a text JSON message `{type:"resize",rows:24,cols:80}`. Output is binary. Connections have message-size and write-deadline limits, close their upstream attachment and stdin on disconnect, and request interactive shell exit. Missing shells produce `SHELL_UNAVAILABLE` before upgrade where possible, or a close message after Docker reports exec exit.

## Images and encrypted registry credentials

| Method | Path | Request / response |
|---|---|---|
| GET | `/hosts/local/images` | Paginated image summaries; `q=` filters IDs/tags |
| GET | `/hosts/local/images/:id` | `{image:DockerImageInspect,containers:DockerContainerSummary[]}` |
| POST | `/hosts/local/images/pull` | `{image,credential_id?,platform?}` → task with decoded progress, errors, final image ID and RepoDigests |
| POST | `/hosts/local/images/import` | Raw Docker-save tar body (not multipart), streamed to a temporary file, maximum 8 GiB → asynchronous ImageLoad task |
| GET | `/hosts/local/images/:id/export` | Streamed Docker-save tar response |
| DELETE | `/hosts/local/images/:id` | `{confirmed:true}` → task; refuses references from running **or stopped** containers, never forces or prunes children |
| GET | `/registry-credentials` | Sanitized credential array: `{id,name,server,username,created_at}` |
| POST | `/registry-credentials` | `{name,server,username,password}` → sanitized saved credential; password encrypted by application vault |
| DELETE | `/registry-credentials/:id` | `{confirmed:true}` |
| POST | `/registry-credentials/:id/test` | Empty body → `{authenticated:true,status}` after Docker RegistryLogin; identity tokens are not returned |

A saved credential may only be used for its own image registry. Docker Hub aliases are normalized; credentials cannot silently cross registry hosts. Pull `platform` is an Engine platform string such as `linux/arm64`. Credential plaintext and registry identity tokens are excluded from saved API responses and audit entries. Import temporary files are removed after execution or submission/upload failure. An abrupt server exit can leave an upload file; operators may remove abandoned `data/uploads/image-*.tar` files after confirming no import task uses them.

## Networks, volumes and mounted directories

| Method | Path | Request / response |
|---|---|---|
| GET | `/hosts/local/networks` | Paginated Docker networks |
| GET | `/hosts/local/networks/:id` | Docker network inspect with container references |
| POST | `/hosts/local/networks` | `{name,driver?:"bridge",internal?:false,labels?:{}}` |
| DELETE | `/hosts/local/networks/:id` | `{confirmed:true}`; rejects built-in networks and attached containers |
| GET | `/hosts/local/volumes` | Paginated Docker volumes |
| GET | `/hosts/local/volumes/:id` | `{volume:DockerVolume,containers:DockerContainerSummary[]}` |
| POST | `/hosts/local/volumes` | `{name,driver?:"local",labels?:{}}` |
| DELETE | `/hosts/local/volumes/:id` | `{confirmed:true}`; checks references from all containers and never forces |
| GET | `/hosts/local/mounts` | Paginated `{container_id,names,mount}` entries for bidirectional navigation |
| POST | `/hosts/local/storage/scan` | `{path:"/explicitly/authorized/directory"}` → cancellable task; final checkpoint/log contains bytes/files/directories |

Network and volume creation/deletion and credential changes have explicit audit entries; asynchronous operations use task auditing. An audit-write failure returns an error explaining that the operation completed but audit persistence failed.

Directory scanning is serialized, rate-limited, context-cancellable and does not follow symlinks. Binary mode requires an authorized root; Docker mode additionally requires an explicit host-to-local mapping, and the host path must be within an authorized root. No privileged host root mount is introduced. Volume filesystem size is only available when its host path is explicitly mapped and authorized; lack of access is not reported as zero size.

Pagination defaults to 50, caps at 200, and reports the total after filtering. API errors use `{error:{code,message}}`. Resource mutations do not expose any prune API.

## Verification

`go test -race ./internal/resources` covers external/stopped inventory, host boundary, data-preserving deletion, stopped-volume references, real SDK HTTP requests, failed-start rebuild compensation and anonymous-volume retention, encrypted recovery checkpoints, unsupported configurations, change-bound preview fingerprints, Docker-log demultiplexing/reconnect, terminal Origin rejection, encrypted credential registry binding, path-mapping and symlink boundaries, image-progress errors bounded uploads, and an actual WebSocket/Docker hijack attachment closing on context cancellation. Real Linux Engine, interactive terminal and real image-transfer acceptance are separate release checks and are not simulated as passing.
