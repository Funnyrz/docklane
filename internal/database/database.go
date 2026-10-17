package database

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"time"
)

func ID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

const schema = `
CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS sessions(token_hash TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id),csrf_token TEXT NOT NULL,expires_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS hosts(id TEXT PRIMARY KEY,name TEXT NOT NULL,docker_endpoint TEXT NOT NULL,capabilities TEXT NOT NULL DEFAULT '{}');
CREATE TABLE IF NOT EXISTS tasks(id TEXT PRIMARY KEY,host_id TEXT NOT NULL DEFAULT 'local',type TEXT NOT NULL,target TEXT NOT NULL,actor_id TEXT NOT NULL,status TEXT NOT NULL,stage TEXT NOT NULL DEFAULT 'queued',progress INTEGER NOT NULL DEFAULT 0,error_code TEXT NOT NULL DEFAULT '',error_message TEXT NOT NULL DEFAULT '',idempotency_key TEXT UNIQUE,cancellable INTEGER NOT NULL DEFAULT 0,retry_count INTEGER NOT NULL DEFAULT 0,checkpoint TEXT NOT NULL DEFAULT '{}',created_at TEXT NOT NULL,started_at TEXT,finished_at TEXT);
CREATE TABLE IF NOT EXISTS task_logs(id INTEGER PRIMARY KEY AUTOINCREMENT,task_id TEXT NOT NULL REFERENCES tasks(id),message TEXT NOT NULL,created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS task_logs_task ON task_logs(task_id,id);
CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY AUTOINCREMENT,type TEXT NOT NULL,task_id TEXT NOT NULL DEFAULT '',data TEXT NOT NULL,created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS audit_logs(id TEXT PRIMARY KEY,actor_id TEXT NOT NULL,action TEXT NOT NULL,resource_type TEXT NOT NULL,resource_id TEXT NOT NULL,result TEXT NOT NULL,created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS tasks_status ON tasks(status,created_at);
INSERT OR IGNORE INTO schema_migrations VALUES(1,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
`

func Open(dir string) (*sql.DB, error) {
	if e := privateDir(dir); e != nil {
		return nil, e
	}
	f, e := os.CreateTemp(dir, ".write-check-")
	if e != nil {
		return nil, fmt.Errorf("data directory not writable: %w", e)
	}
	f.Close()
	os.Remove(f.Name())
	p := filepath.Join(dir, "docker-manager.db")
	db, e := sql.Open("sqlite", p)
	if e != nil {
		return nil, e
	}
	// A single connection keeps PRAGMAs and transaction serialization deterministic.
	db.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL"} {
		if _, e = db.Exec(q); e != nil {
			db.Close()
			return nil, e
		}
	}
	var version int
	_ = db.QueryRow("SELECT coalesce(max(version),0) FROM schema_migrations").Scan(&version)
	if version > 1 {
		db.Close()
		return nil, fmt.Errorf("database version %d is newer than this application; restore matching binary", version)
	}
	tx, e := db.Begin()
	if e != nil {
		db.Close()
		return nil, e
	}
	if _, e = tx.Exec(schema); e != nil {
		tx.Rollback()
		db.Close()
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		db.Close()
		return nil, e
	}
	if e = os.Chmod(p, 0600); e != nil {
		db.Close()
		return nil, e
	}
	return db, nil
}

func Audit(db *sql.DB, actor, action, kind, id, result string) error {
	_, e := db.Exec("INSERT INTO audit_logs VALUES(?,?,?,?,?,?,?)", ID(), actor, action, kind, id, result, Now())
	return e
}
