package database

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrationPreservesStateAndRejectsNewerDatabase(t *testing.T) {
	dir := t.TempDir()
	db, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec("INSERT INTO settings VALUES('retained','value')")
	if e != nil {
		t.Fatal(e)
	}
	db.Close()
	db, e = Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	var value string
	if e = db.QueryRow("SELECT value FROM settings WHERE key='retained'").Scan(&value); e != nil || value != "value" {
		t.Fatal(e, value)
	}
	info, _ := os.Stat(filepath.Join(dir, "docker-manager.db"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("database not private")
	}
	_, e = db.Exec("INSERT INTO schema_migrations VALUES(99,'future')")
	if e != nil {
		t.Fatal(e)
	}
	db.Close()
	if db, e = Open(dir); e == nil {
		db.Close()
		t.Fatal("older binary accepted newer database")
	}
}

func TestProcessLockAndSymlinkRootProtection(t *testing.T) {
	dir := t.TempDir()
	lock, e := Lock(dir)
	if e != nil {
		t.Fatal(e)
	}
	if other, e := Lock(dir); e == nil {
		other.Close()
		t.Fatal("second process lock acquired")
	}
	lock.Close()
	again, e := Lock(dir)
	if e != nil {
		t.Fatal(e)
	}
	again.Close()
	link := filepath.Join(t.TempDir(), "root")
	if e = os.Symlink("/", link); e != nil {
		t.Fatal(e)
	}
	if _, e = Open(link); e == nil {
		t.Fatal("symlink root accepted")
	}
}
