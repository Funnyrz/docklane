# Internal implementation contract

Module: `github.com/docker-manager/docker-manager`. Go 1.27. Gin 1.11, Docker SDK v28.5.1.

`platform.App`: `DB *sql.DB`, `Docker *client.Client` (client exists even when Engine offline), `Tasks *task.Manager`, `Vault *security.Vault`, `Config config.Config`.

Feature packages expose `Register(r *gin.RouterGroup, app *platform.App)`; router group is `/api/v1`, authenticated, CSRF-protected. Routes for host resources start `/hosts/:host_id/...`; middleware only permits `local`. Other routes include `/tasks`, `/backup/...`, `/compose/...`, `/host/...`. Return raw JSON objects or arrays. Errors `{error:{code,message}}` with appropriate HTTP status.

`platform.Actor(c) string`, `platform.Fail(c,status,code,err)`, `platform.Submit(c,app,kind,target,cancellable,func(*task.Run)error)` returns 202 task.

`task.Run`: exported `ID string`; `Context() context.Context`, `Log(message string)`, `Progress(stage string,percent int)`, `Checkpoint(value any) error`. `task.Manager.Submit(ctx,kind,target,actor,idempotencyKey string,cancellable bool,fn func(*task.Run)error) (task.Task,error)`; `Recover(kind string,fn func(context.Context,json.RawMessage)error)` registers compensation invoked at startup for interrupted tasks. Raw checkpoint persisted as JSON in tasks.checkpoint. No automatic unsafe replay.

`security.Vault.Seal(plaintext string) (string,error)`, `.Open(ciphertext string) (string,error)`.

`safepath.Resolve(path string, allowed []string) (string,error)` requires allowed root, resolves symlinks including existing ancestor for new targets; `safepath.Disjoint(a,b string) error` rejects equal/ancestor paths; `safepath.AtomicWrite(path string,data []byte,mode os.FileMode) error`.

Database foundation creates users, sessions, settings, hosts, tasks, task_logs, events, audit_logs. Feature modules own idempotent schema setup for compose_projects, registry_credentials, backup_repositories, backup_policies, backup_snapshots, schedule_instances. Use explicit SQL; JSON columns stored TEXT. Timestamp values use UTC RFC3339Nano strings. Foundation `database.ID() string` returns random hex; `database.Now() string`.

Config fields are in `internal/config/config.go`; shared config must not be edited without coordination. Frontend should use server routes listed by feature agents and API docs, API requests credentials=same-origin; csrf_token readable cookie, send X-CSRF-Token for mutations; session HttpOnly. `GET /auth/status` returns `{initialized,authenticated,user?,csrf_token?}`; `POST /auth/setup {username,password}`, `POST /auth/login`, `POST /auth/logout`; `GET /hosts/local/dashboard`, `GET /hosts/local/capabilities`; global `/tasks`, `/tasks/:id`, `/tasks/:id/logs`, `POST /tasks/:id/cancel`, `/audit`.
