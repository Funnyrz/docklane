package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/docker-manager/docker-manager/internal/database"
	"github.com/docker-manager/docker-manager/internal/security"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type Task struct {
	ID           string  `json:"id"`
	HostID       string  `json:"host_id"`
	Type         string  `json:"type"`
	Target       string  `json:"target"`
	ActorID      string  `json:"actor_id"`
	Status       string  `json:"status"`
	Stage        string  `json:"stage"`
	Progress     int     `json:"progress"`
	ErrorCode    string  `json:"error_code"`
	ErrorMessage string  `json:"error_message"`
	Cancellable  bool    `json:"cancellable"`
	RetryCount   int     `json:"retry_count"`
	CreatedAt    string  `json:"created_at"`
	StartedAt    *string `json:"started_at"`
	FinishedAt   *string `json:"finished_at"`
}
type Log struct {
	ID        int64  `json:"id"`
	TaskID    string `json:"task_id"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
}
type Event struct {
	ID        int64           `json:"id"`
	Type      string          `json:"type"`
	TaskID    string          `json:"task_id"`
	Data      json.RawMessage `json:"data"`
	CreatedAt string          `json:"created_at"`
}
type job struct {
	id string
	fn func(*Run) error
}
type CodedError interface {
	error
	ErrorCode() string
}
type Manager struct {
	db         *sql.DB
	queue      chan job
	mu         sync.Mutex
	cancels    map[string]context.CancelFunc
	recovery   map[string]func(context.Context, json.RawMessage) error
	ctx        context.Context
	wg         sync.WaitGroup
	started    bool
	recovering bool
}
type Run struct {
	ID  string
	m   *Manager
	ctx context.Context
}

func New(db *sql.DB) *Manager {
	return &Manager{db: db, queue: make(chan job, 64), cancels: map[string]context.CancelFunc{}, recovery: map[string]func(context.Context, json.RawMessage) error{}}
}
func (r *Run) Context() context.Context { return r.ctx }
func (r *Run) Log(message string) {
	message = security.Redact(message)
	if len(message) > 16384 {
		message = message[:16384] + " [truncated]"
	}
	_, _ = r.m.db.Exec("INSERT INTO task_logs(task_id,message,created_at) VALUES(?,?,?)", r.ID, message, database.Now())
	r.m.Publish("task.log", r.ID, map[string]string{"message": message})
}
func (r *Run) Progress(stage string, percent int) {
	if percent < 0 {
		percent = 0
	}
	if percent > 99 {
		percent = 99
	}
	_, _ = r.m.db.Exec("UPDATE tasks SET stage=?,progress=? WHERE id=? AND status='Running'", stage, percent, r.ID)
	r.m.Publish("task.progress", r.ID, map[string]any{"stage": stage, "progress": percent})
}
func (r *Run) Checkpoint(v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = r.m.db.Exec("UPDATE tasks SET checkpoint=? WHERE id=?", string(b), r.ID)
	return e
}
func (r *Run) Retry(attempt int) {
	if attempt < 0 {
		return
	}
	_, _ = r.m.db.Exec("UPDATE tasks SET retry_count=? WHERE id=?", attempt, r.ID)
	r.m.Publish("task.retry", r.ID, map[string]int{"retry_count": attempt})
}
func (m *Manager) Recover(kind string, fn func(context.Context, json.RawMessage) error) {
	m.recovery[kind] = fn
}
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return errors.New("task manager already started")
	}
	m.ctx = ctx
	m.started = true
	m.recovering = true
	m.mu.Unlock()
	if e := m.recoverInterrupted(ctx, true); e != nil {
		return e
	}
	m.wg.Add(2)
	go m.worker()
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !m.Ready() {
					_ = m.recoverInterrupted(ctx, false)
				}
			}
		}
	}()
	return nil
}
func (m *Manager) Ready() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.started && !m.recovering }
func (m *Manager) WaitReady(ctx context.Context) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if m.Ready() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func (m *Manager) recoverInterrupted(ctx context.Context, initial bool) error {
	query := "SELECT id,type,checkpoint,stage FROM tasks WHERE error_code='RECOVERY_REQUIRED' OR status='Running'"
	if initial {
		query += " OR status IN ('Pending','Running')"
	}
	rows, e := m.db.QueryContext(ctx, query)
	if e != nil {
		return e
	}
	type interrupted struct{ id, kind, checkpoint, stage string }
	var all []interrupted
	for rows.Next() {
		var t interrupted
		if e = rows.Scan(&t.id, &t.kind, &t.checkpoint, &t.stage); e != nil {
			rows.Close()
			return e
		}
		all = append(all, t)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, t := range all {
		message := "Interrupted during " + t.stage + "; not automatically replayed"
		code := "INTERRUPTED"
		var safety struct {
			HoldWriters bool `json:"hold_writers"`
		}
		_ = json.Unmarshal([]byte(t.checkpoint), &safety)
		if t.kind == "restore" && safety.HoldWriters {
			code = "RESTORE_VALIDATION_REQUIRED"
			message += "; original writers remain stopped because restored data may be partial. Validate the data before explicitly starting applications."
		}
		if fn := m.recovery[t.kind]; fn != nil && t.checkpoint != "{}" && t.checkpoint != "" {
			c, cancel := context.WithTimeout(ctx, 2*time.Minute)
			err := fn(c, json.RawMessage(t.checkpoint))
			cancel()
			if err != nil {
				code = "RECOVERY_REQUIRED"
				message += "; compensation failed: " + security.Redact(err.Error())
			} else {
				message += "; compensation completed"
			}
		}
		_, e = m.db.Exec("UPDATE tasks SET status='Failed',error_code=?,error_message=?,finished_at=? WHERE id=?", code, message, database.Now(), t.id)
		if e != nil {
			return e
		}
		(&Run{ID: t.id, m: m, ctx: ctx}).Log(message)
	}
	var pending int
	if e = m.db.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE error_code='RECOVERY_REQUIRED' OR status='Running'").Scan(&pending); e != nil {
		return e
	}
	m.mu.Lock()
	m.recovering = pending > 0
	m.mu.Unlock()
	return nil
}
func (m *Manager) Submit(ctx context.Context, kind, target, actor, key string, cancellable bool, fn func(*Run) error) (Task, error) {
	m.mu.Lock()
	started := m.started
	m.mu.Unlock()
	if !started {
		return Task{}, errors.New("task manager not started")
	}
	if !m.Ready() {
		return Task{}, errors.New("interrupted operation compensation is pending; management writes are blocked")
	}
	if len(key) > 200 {
		return Task{}, errors.New("idempotency key too long")
	}
	if key != "" {
		var id string
		e := m.db.QueryRowContext(ctx, "SELECT id FROM tasks WHERE idempotency_key=?", key).Scan(&id)
		if e == nil {
			t, e := m.Get(id)
			if e != nil {
				return t, e
			}
			if t.Type != kind || t.Target != target || t.ActorID != actor {
				return Task{}, errors.New("idempotency key belongs to another request")
			}
			return t, nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return Task{}, e
		}
	}
	id := database.ID()
	created := database.Now()
	var k any
	if key != "" {
		k = key
	}
	_, e := m.db.ExecContext(ctx, "INSERT INTO tasks(id,type,target,actor_id,status,idempotency_key,cancellable,created_at) VALUES(?,?,?,?,'Pending',?,?,?)", id, kind, target, actor, k, cancellable, created)
	if e != nil {
		if key != "" {
			var existing string
			if m.db.QueryRowContext(ctx, "SELECT id FROM tasks WHERE idempotency_key=?", key).Scan(&existing) == nil {
				t, lookupErr := m.Get(existing)
				if lookupErr == nil && t.Type == kind && t.Target == target && t.ActorID == actor {
					return t, nil
				}
			}
		}
		return Task{}, e
	}
	if e = database.Audit(m.db, actor, kind, "task", id, "queued"); e != nil {
		_, _ = m.db.Exec("UPDATE tasks SET status='Failed',error_code='AUDIT_FAILED',finished_at=? WHERE id=?", database.Now(), id)
		return Task{}, e
	}
	// Capture the accepted state before execution can change it.
	t, e := m.Get(id)
	if e != nil {
		return Task{}, e
	}
	select {
	case m.queue <- job{id, fn}:
		m.Publish("task.created", id, t)
		return t, nil
	default:
		_, _ = m.db.Exec("UPDATE tasks SET status='Failed',error_code='QUEUE_FULL',finished_at=? WHERE id=?", database.Now(), id)
		return Task{}, errors.New("task queue is full")
	}
}
func (m *Manager) worker() {
	defer m.wg.Done()
	for {
		select {
		case <-m.ctx.Done():
			return
		case j := <-m.queue:
			if e := m.WaitReady(m.ctx); e != nil {
				return
			}
			m.execute(j)
		}
	}
}
func (m *Manager) execute(j job) {
	t, e := m.Get(j.id)
	if e != nil || t.Status != "Pending" {
		return
	}
	timeout := 30 * time.Minute
	if t.Type == "backup" || t.Type == "restore" || t.Type == "backup.run" || t.Type == "backup.restore" {
		timeout = 24 * time.Hour
	}
	ctx, cancel := context.WithTimeout(m.ctx, timeout)
	m.mu.Lock()
	m.cancels[j.id] = cancel
	result, claimErr := m.db.Exec("UPDATE tasks SET status='Running',stage='starting',started_at=? WHERE id=? AND status='Pending'", database.Now(), j.id)
	claimed := int64(0)
	if claimErr == nil {
		claimed, _ = result.RowsAffected()
	}
	if claimErr != nil || claimed != 1 {
		delete(m.cancels, j.id)
		m.mu.Unlock()
		cancel()
		return
	}
	m.mu.Unlock()
	defer func() { cancel(); m.mu.Lock(); delete(m.cancels, j.id); m.mu.Unlock() }()
	if ctx.Err() != nil {
		_, _ = m.db.Exec("UPDATE tasks SET status='Failed',error_code='INTERRUPTED',finished_at=? WHERE id=?", database.Now(), j.id)
		return
	}
	r := &Run{ID: j.id, m: m, ctx: ctx}
	m.Publish("task.started", j.id, map[string]string{"status": "Running"})
	err := func() (err error) {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("task panic: %v", p)
			}
		}()
		return j.fn(r)
	}()
	status, code, msg := "Succeeded", "", ""
	if err != nil {
		status = "Failed"
		code = "TASK_FAILED"
		var coded CodedError
		if errors.As(err, &coded) {
			code = coded.ErrorCode()
		}
		msg = security.Redact(err.Error())
		if errors.Is(err, context.Canceled) && t.Cancellable {
			status = "Canceled"
			code = "CANCELED"
		}
		if errors.Is(err, context.DeadlineExceeded) {
			code = "TIMEOUT"
		}
		var pending interface{ RecoveryRequired() bool }
		if errors.As(err, &pending) && pending.RecoveryRequired() {
			// A handler may have completed compensation but failed to persist
			// its checkpoint. Keep the write gate closed until durable recovery
			// resolves that uncertainty, even if the fallback returns nil.
			code, status = "RECOVERY_REQUIRED", "Failed"
			m.mu.Lock()
			m.recovering = true
			m.mu.Unlock()
		}
		var checkpoint string
		if fn := m.recovery[t.Type]; fn != nil && m.db.QueryRow("SELECT checkpoint FROM tasks WHERE id=?", j.id).Scan(&checkpoint) == nil && checkpoint != "{}" && checkpoint != "" {
			cleanup, stop := context.WithTimeout(context.Background(), 2*time.Minute)
			recoveryErr := fn(cleanup, json.RawMessage(checkpoint))
			stop()
			if recoveryErr != nil {
				code = "RECOVERY_REQUIRED"
				status = "Failed"
				msg += "; compensation pending: " + security.Redact(recoveryErr.Error())
				m.mu.Lock()
				m.recovering = true
				m.mu.Unlock()
			}
		}
		r.Log(msg)
	}
	progress := 100
	stage := "completed"
	if status != "Succeeded" {
		current, _ := m.Get(j.id)
		progress = current.Progress
		stage = current.Stage
	}
	tx, saveErr := m.db.Begin()
	if saveErr == nil {
		_, saveErr = tx.Exec("UPDATE tasks SET status=?,stage=?,progress=?,error_code=?,error_message=?,finished_at=? WHERE id=?", status, stage, progress, code, msg, database.Now(), j.id)
		if saveErr == nil {
			_, saveErr = tx.Exec("INSERT INTO audit_logs VALUES(?,?,?,?,?,?,?)", database.ID(), t.ActorID, t.Type, "task", j.id, status, database.Now())
		}
		if saveErr == nil {
			saveErr = tx.Commit()
		} else {
			tx.Rollback()
		}
	}
	if saveErr != nil {
		m.mu.Lock()
		m.recovering = true
		m.mu.Unlock()
		slog.Error("task result could not be persisted; writes paused", "task_id", j.id, "error", security.Redact(saveErr.Error()))
		return
	}
	m.Publish("task.finished", j.id, map[string]any{"status": status, "error_code": code, "error_message": msg})
}
func (m *Manager) Wait() { m.wg.Wait() }
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, e := m.Get(id)
	if e != nil {
		return e
	}
	if !t.Cancellable {
		return errors.New("this task cannot be safely canceled")
	}
	if t.Status == "Pending" {
		_, e = m.db.Exec("UPDATE tasks SET status='Canceled',stage='Canceled',error_code='CANCELED',finished_at=? WHERE id=? AND status='Pending'", database.Now(), id)
		return e
	}
	if t.Status != "Running" {
		return errors.New("task is not active")
	}
	cancel := m.cancels[id]
	if cancel == nil {
		return errors.New("task is not cancellable now")
	}
	cancel()
	return nil
}

const taskColumns = "id,host_id,type,target,actor_id,status,stage,progress,error_code,error_message,cancellable,retry_count,created_at,started_at,finished_at"

func scan(s interface{ Scan(...any) error }) (Task, error) {
	var t Task
	e := s.Scan(&t.ID, &t.HostID, &t.Type, &t.Target, &t.ActorID, &t.Status, &t.Stage, &t.Progress, &t.ErrorCode, &t.ErrorMessage, &t.Cancellable, &t.RetryCount, &t.CreatedAt, &t.StartedAt, &t.FinishedAt)
	return t, e
}
func (m *Manager) Get(id string) (Task, error) {
	return scan(m.db.QueryRow("SELECT "+taskColumns+" FROM tasks WHERE id=?", id))
}
func (m *Manager) List(limit, offset int) ([]Task, error) {
	rows, e := m.db.Query("SELECT "+taskColumns+" FROM tasks ORDER BY created_at DESC LIMIT ? OFFSET ?", limit, offset)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	all := []Task{}
	for rows.Next() {
		t, e := scan(rows)
		if e != nil {
			return nil, e
		}
		all = append(all, t)
	}
	return all, rows.Err()
}
func (m *Manager) Logs(id string, after int64) ([]Log, error) {
	rows, e := m.db.Query("SELECT id,task_id,message,created_at FROM task_logs WHERE task_id=? AND id>? ORDER BY id LIMIT 1000", id, after)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	all := []Log{}
	for rows.Next() {
		var t Log
		if e = rows.Scan(&t.ID, &t.TaskID, &t.Message, &t.CreatedAt); e != nil {
			return nil, e
		}
		all = append(all, t)
	}
	return all, rows.Err()
}
func (m *Manager) Publish(kind, id string, v any) {
	b, e := json.Marshal(v)
	if e != nil {
		return
	}
	var structured any
	if json.Unmarshal(b, &structured) != nil {
		return
	}
	b, e = json.Marshal(redactEvent(structured))
	if e != nil {
		return
	}
	_, _ = m.db.Exec("INSERT INTO events(type,task_id,data,created_at) VALUES(?,?,?,?)", kind, id, string(b), database.Now())
}
func redactEvent(v any) any {
	switch x := v.(type) {
	case string:
		return security.Redact(x)
	case []any:
		for i, item := range x {
			x[i] = redactEvent(item)
		}
	case map[string]any:
		for key, item := range x {
			switch strings.ToLower(key) {
			case "password", "passwd", "token", "secret", "authorization", "registryauth", "api_key", "apikey":
				x[key] = "[REDACTED]"
			default:
				x[key] = redactEvent(item)
			}
		}
	}
	return v
}
func (m *Manager) Events(after int64) ([]Event, error) {
	rows, e := m.db.Query("SELECT id,type,task_id,data,created_at FROM events WHERE id>? ORDER BY id LIMIT 200", after)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	all := []Event{}
	for rows.Next() {
		var t Event
		var b string
		if e = rows.Scan(&t.ID, &t.Type, &t.TaskID, &b, &t.CreatedAt); e != nil {
			return nil, e
		}
		t.Data = json.RawMessage(b)
		all = append(all, t)
	}
	return all, rows.Err()
}
