package task

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/docker-manager/docker-manager/internal/database"
	"strings"
	"testing"
	"time"
)

func setupManager(t *testing.T) (*Manager, context.CancelFunc) {
	t.Helper()
	db, e := database.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := New(db)
	if e = m.Start(ctx); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { cancel(); m.Wait(); db.Close() })
	return m, cancel
}
func await(t *testing.T, m *Manager, id string) Task {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		v, e := m.Get(id)
		if e != nil {
			t.Fatal(e)
		}
		if v.Status == "Succeeded" || v.Status == "Failed" || v.Status == "Canceled" {
			return v
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("task did not finish")
	return Task{}
}
func TestTaskPersistenceIdempotencyAndRedaction(t *testing.T) {
	m, _ := setupManager(t)
	first, e := m.Submit(context.Background(), "pull", "image", "actor", "same-key", true, func(r *Run) error {
		r.Progress("pulling", 50)
		r.Log("password=secret-value")
		return errors.New("token=private-value")
	})
	if e != nil {
		t.Fatal(e)
	}
	second, e := m.Submit(context.Background(), "pull", "image", "actor", "same-key", true, func(r *Run) error { return nil })
	if e != nil || first.ID != second.ID {
		t.Fatal("idempotency failed", e)
	}
	if _, e = m.Submit(context.Background(), "restore", "other", "actor", "same-key", false, func(r *Run) error { return nil }); e == nil {
		t.Fatal("cross-operation key reused")
	}
	finished := await(t, m, first.ID)
	if finished.Status != "Failed" || strings.Contains(finished.ErrorMessage, "private-value") {
		t.Fatal(finished)
	}
	logs, e := m.Logs(first.ID, 0)
	if e != nil {
		t.Fatal(e)
	}
	for _, l := range logs {
		if strings.Contains(l.Message, "secret-value") {
			t.Fatal("secret in logs")
		}
	}
}
func TestInterruptedTaskCompensationNeverReplays(t *testing.T) {
	db, e := database.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	_, e = db.Exec("INSERT INTO tasks(id,type,target,actor_id,status,checkpoint,created_at) VALUES('interrupted','backup','volume','actor','Running','{\"running\":[\"old\"]}',?)", database.Now())
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := New(db)
	compensated := false
	m.Recover("backup", func(ctx context.Context, checkpoint json.RawMessage) error {
		compensated = strings.Contains(string(checkpoint), "old")
		return nil
	})
	if e = m.Start(ctx); e != nil {
		t.Fatal(e)
	}
	cancel()
	m.Wait()
	v, e := m.Get("interrupted")
	if e != nil || !compensated || v.Status != "Failed" || v.ErrorCode != "INTERRUPTED" {
		t.Fatal(v, e)
	}
}
func TestCancellation(t *testing.T) {
	m, _ := setupManager(t)
	started := make(chan struct{})
	v, e := m.Submit(context.Background(), "scan", "dir", "actor", "", true, func(r *Run) error { close(started); <-r.Context().Done(); return r.Context().Err() })
	if e != nil {
		t.Fatal(e)
	}
	<-started
	if e = m.Cancel(v.ID); e != nil {
		t.Fatal(e)
	}
	if v := await(t, m, v.ID); v.Status != "Canceled" {
		t.Fatal(v)
	}
}

func TestCompensationFailureIsDurableAndBlocksNewTasks(t *testing.T) {
	db, e := database.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	_, e = db.Exec("INSERT INTO tasks(id,type,target,actor_id,status,checkpoint,created_at) VALUES('unsafe','backup','volume','actor','Running','{\"pending\":true}',?)", database.Now())
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := New(db)
	available := false
	m.Recover("backup", func(context.Context, json.RawMessage) error {
		if !available {
			return errors.New("Engine offline")
		}
		return nil
	})
	if e = m.Start(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() { cancel(); m.Wait() }()
	if m.Ready() {
		t.Fatal("manager opened writes before compensation")
	}
	if _, e = m.Submit(ctx, "pull", "image", "actor", "", false, func(*Run) error { return nil }); e == nil {
		t.Fatal("unsafe task accepted")
	}
	v, _ := m.Get("unsafe")
	if v.ErrorCode != "RECOVERY_REQUIRED" {
		t.Fatal(v)
	}
	available = true
	if e = m.recoverInterrupted(ctx, false); e != nil {
		t.Fatal(e)
	}
	if !m.Ready() {
		t.Fatal("manager did not recover")
	}
	v, _ = m.Get("unsafe")
	if v.ErrorCode != "INTERRUPTED" || v.Status != "Failed" {
		t.Fatal(v)
	}
}

func TestCanceledPendingTaskNeverExecutes(t *testing.T) {
	m, _ := setupManager(t)
	ctx := context.Background()
	block := make(chan struct{})
	started := make(chan struct{})
	first, e := m.Submit(ctx, "block", "one", "actor", "", false, func(*Run) error { close(started); <-block; return nil })
	if e != nil {
		t.Fatal(e)
	}
	<-started
	called := false
	second, e := m.Submit(ctx, "scan", "two", "actor", "", true, func(*Run) error { called = true; return nil })
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Cancel(second.ID); e != nil {
		t.Fatal(e)
	}
	close(block)
	await(t, m, first.ID)
	time.Sleep(25 * time.Millisecond)
	if v := await(t, m, second.ID); v.Status != "Canceled" || called {
		t.Fatal("canceled task ran", v)
	}
}

func TestStructuredEventRedactionPreservesJSON(t *testing.T) {
	m, _ := setupManager(t)
	m.Publish("test", "", map[string]any{"message": `failed token="private value" path="a\\b"`, "password": "secret value", "nested": []any{`Authorization: Bearer abc123`}})
	events, e := m.Events(0)
	if e != nil {
		t.Fatal(e)
	}
	if len(events) != 1 || !json.Valid(events[0].Data) {
		t.Fatalf("invalid event JSON: %+v", events)
	}
	for _, secret := range []string{"private value", "secret value", "abc123"} {
		if strings.Contains(string(events[0].Data), secret) {
			t.Fatal("secret in event", string(events[0].Data))
		}
	}
}

func TestInterruptedOverwriteRequiresManualValidationWithoutBlockingReadiness(t *testing.T) {
	db, e := database.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	_, e = db.Exec("INSERT INTO tasks(id,type,target,actor_id,status,checkpoint,created_at) VALUES('overwrite','restore','directory','actor','Running','{\"hold_writers\":true}',?)", database.Now())
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := New(db)
	m.Recover("restore", func(context.Context, json.RawMessage) error { return nil })
	if e = m.Start(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() { cancel(); m.Wait() }()
	result, e := m.Get("overwrite")
	if e != nil || result.ErrorCode != "RESTORE_VALIDATION_REQUIRED" || !m.Ready() {
		t.Fatal(result, e)
	}
	if !strings.Contains(result.ErrorMessage, "remain stopped") {
		t.Fatal("missing partial-restore warning", result)
	}
}

func TestTaskResultAndAuditCommitAtomically(t *testing.T) {
	m, _ := setupManager(t)
	_, e := m.db.Exec("CREATE TRIGGER fail_success_audit BEFORE INSERT ON audit_logs WHEN NEW.result='Succeeded' BEGIN SELECT RAISE(ABORT,'simulated audit failure'); END;")
	if e != nil {
		t.Fatal(e)
	}
	v, e := m.Submit(context.Background(), "probe", "fixture", "actor", "", false, func(*Run) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	until := time.Now().Add(3 * time.Second)
	for m.Ready() && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if m.Ready() {
		t.Fatal("persistence failure did not pause writes")
	}
	v, e = m.Get(v.ID)
	if e != nil || v.Status != "Running" {
		t.Fatal("task success committed without audit", v, e)
	}
}

type pendingJournalError struct{}

func (pendingJournalError) Error() string          { return "compensation checkpoint was not saved" }
func (pendingJournalError) RecoveryRequired() bool { return true }

func TestUncertainCompensationJournalKeepsWritesPaused(t *testing.T) {
	m, _ := setupManager(t)
	m.Recover("journal-probe", func(context.Context, json.RawMessage) error { return nil })
	v, e := m.Submit(context.Background(), "journal-probe", "fixture", "actor", "", false, func(r *Run) error {
		if e := r.Checkpoint(map[string]bool{"written": true}); e != nil {
			return e
		}
		return pendingJournalError{}
	})
	if e != nil {
		t.Fatal(e)
	}
	v = await(t, m, v.ID)
	if v.ErrorCode != "RECOVERY_REQUIRED" || m.Ready() {
		t.Fatal("uncertain journal opened writes", v)
	}
	if e = m.recoverInterrupted(context.Background(), false); e != nil {
		t.Fatal(e)
	}
	if !m.Ready() {
		t.Fatal("durable recovery did not reopen writes")
	}
}
