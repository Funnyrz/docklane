package workflows

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker-manager/docker-manager/internal/task"
	"github.com/gin-gonic/gin"
)

func TestPartialSnapshotsStayUnverifiedAndOutsideRetention(t *testing.T) {
	if os.Getenv("DM_TEST_RESTIC") == "" {
		t.Skip("requires pinned real Restic")
	}
	s := testService(t)
	root := s.app.Config.AllowedPaths[0]
	repo, source := filepath.Join(root, "repository"), filepath.Join(root, "source")
	for _, dir := range []string{repo, source} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "payload"), []byte("complete-backup-data"), 0600); err != nil {
		t.Fatal(err)
	}
	sealed, err := s.app.Vault.Seal("verified-repository-password")
	if err != nil {
		t.Fatal(err)
	}
	r := Repository{ID: "verified-repo", Name: "test", Location: repo, secret: sealed}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err = s.restic(ctx, r, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.app.DB.Exec(`INSERT INTO backup_repositories VALUES(?,?,?,?,?,?)`, r.ID, r.Name, r.Location, r.secret, "ready", now()); err != nil {
		t.Fatal(err)
	}
	if err = s.app.Tasks.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); s.app.Tasks.Wait() }()
	p := Policy{ID: "verified-policy", Name: "test", RepositoryID: r.ID, Targets: []Target{{Type: "directory", Path: source}}, Consistency: "file", KeepLast: 1, Timezone: "UTC"}
	backup := func() string {
		t.Helper()
		job, err := s.app.Tasks.Submit(ctx, "backup", p.ID, "test", "", true, func(run *task.Run) error { return s.backup(run, p) })
		if err != nil {
			t.Fatal(err)
		}
		if result := waitTask(t, s.app.Tasks, job.ID); result.Status != "Succeeded" {
			t.Fatal(result.ErrorMessage)
		}
		var ref string
		if err = s.app.DB.QueryRow(`SELECT snapshot_ref FROM backup_snapshots ORDER BY created_at DESC LIMIT 1`).Scan(&ref); err != nil {
			t.Fatal(err)
		}
		return ref
	}
	first := backup()
	stage := filepath.Join(repo, ".dm-manifest-partial")
	if err = os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	m := Manifest{Version: 1, Policy: p, Sources: []backupSource{{HostPath: source, LocalPath: source, SnapshotPath: source}}}
	if err = os.WriteFile(filepath.Join(stage, "manifest.json"), []byte(jsonString(m)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.restic(ctx, r, "backup", "--json", "--tag", "dm-policy:"+p.ID, filepath.Join(stage, "manifest.json"), source, filepath.Join(root, "missing-source")); err == nil {
		t.Fatal("real failed backup unexpectedly succeeded")
	}
	if err = os.RemoveAll(stage); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	s.registerBackup(router.Group("/api/v1"))
	list := func() []map[string]any {
		t.Helper()
		out := httptest.NewRecorder()
		router.ServeHTTP(out, httptest.NewRequest("GET", "/api/v1/backup/repositories/"+r.ID+"/snapshots", nil))
		if out.Code != 200 {
			t.Fatal(out.Code, out.Body.String())
		}
		var rows []map[string]any
		if err = json.Unmarshal(out.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	partial := ""
	for _, row := range list() {
		if row["id"] == first {
			if row["verification_status"] != "verified" {
				t.Fatal("complete snapshot not independently marked verified", row)
			}
		} else {
			partial = row["id"].(string)
			if row["verification_status"] != "unverified" {
				t.Fatal("partial snapshot not visibly unverified", row)
			}
		}
	}
	if partial == "" {
		t.Fatal("real failed backup did not commit test partial snapshot")
	}
	req := restoreRequest{RepositoryID: r.ID, Snapshot: partial, Target: filepath.Join(root, "partial-restore")}
	if _, _, _, _, err = s.restorePreflight(ctx, req); err == nil {
		t.Fatal("partial snapshot restored without explicit risk acknowledgment")
	}
	req.AcknowledgeUnverified = true
	if _, _, _, _, err = s.restorePreflight(ctx, req); err != nil {
		t.Fatal("explicit new-directory rescue refused", err)
	}
	for _, variant := range []restoreRequest{{RepositoryID: r.ID, Snapshot: partial, Target: source, Overwrite: true, AcknowledgeUnverified: true}, {RepositoryID: r.ID, Snapshot: partial, Target: req.Target, Recreate: true, AcknowledgeUnverified: true}} {
		if _, _, _, _, err = s.restorePreflight(ctx, variant); err == nil {
			t.Fatal("unverified overwrite or recreation accepted")
		}
	}
	independent := testService(t)
	independent.app.Config.AllowedPaths = append(independent.app.Config.AllowedPaths, root)
	independentRouter := gin.New()
	independent.registerBackup(independentRouter.Group("/api/v1"))
	importResponse := httptest.NewRecorder()
	importRequest := httptest.NewRequest("POST", "/api/v1/backup/repositories", strings.NewReader(jsonString(map[string]any{"name": "offline-import", "location": repo, "password": "verified-repository-password", "import": true})))
	importRequest.Header.Set("Content-Type", "application/json")
	independentRouter.ServeHTTP(importResponse, importRequest)
	if importResponse.Code != 201 {
		t.Fatal(importResponse.Code, importResponse.Body.String())
	}
	var imported Repository
	if err = json.Unmarshal(importResponse.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	independentList := httptest.NewRecorder()
	independentRouter.ServeHTTP(independentList, httptest.NewRequest("GET", "/api/v1/backup/repositories/"+imported.ID+"/snapshots", nil))
	var independentRows []map[string]any
	if independentList.Code != 200 {
		t.Fatal(independentList.Code, independentList.Body.String())
	}
	if err = json.Unmarshal(independentList.Body.Bytes(), &independentRows); err != nil {
		t.Fatal(err)
	}
	for _, row := range independentRows {
		want := "unverified"
		if row["id"] == first {
			want = "verified"
		}
		if row["verification_status"] != want {
			t.Fatal("fresh SQLite import lost repository verification state", row)
		}
	}
	// A new installation has no success index. Verification still comes from
	// encrypted snapshot tags, while partial recovery remains visible.
	if _, err = s.app.DB.Exec(`DELETE FROM backup_snapshots`); err != nil {
		t.Fatal(err)
	}
	for _, row := range list() {
		if row["id"] == first && row["verification_status"] != "verified" {
			t.Fatal("verification depended on SQLite")
		}
	}
	last := backup()
	if last == first {
		t.Fatal("new backup reused prior snapshot")
	}
	rows := list()
	verified, unverified := 0, 0
	for _, row := range rows {
		switch row["verification_status"] {
		case "verified":
			verified++
			if row["id"] != last {
				t.Fatal("retained wrong complete snapshot")
			}
		case "unverified":
			unverified++
			if row["id"] != partial {
				t.Fatal("unrecognized partial")
			}
		}
	}
	if verified != 1 || unverified != 1 {
		t.Fatal("partial snapshot counted toward retention or disappeared", rows)
	}
	if _, _, err = s.readManifest(ctx, r, last); err != nil {
		t.Fatal("SQL reference does not point to post-tag immutable snapshot", err)
	}
	var logs string
	if err = s.app.DB.QueryRow(`SELECT group_concat(message,' ') FROM task_logs`).Scan(&logs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs, last) {
		t.Fatal("task log omitted final rewritten snapshot identity")
	}
}
