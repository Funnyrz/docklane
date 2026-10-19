package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/docker-manager/docker-manager/internal/database"
)

func TestCapabilitySettingsValidation(t *testing.T) {
	for _, p := range []string{"/", "/srv/..", "relative", "", "/srv\x00data"} {
		if _, err := NormalizeCapabilitySettings(CapabilitySettings{AllowedPaths: []string{p}}, "binary"); err == nil {
			t.Errorf("accepted invalid path %q", p)
		}
	}
	for _, in := range []CapabilitySettings{
		{AllowedPaths: []string{"/srv/a", "/srv/./a"}},
		{PathMappings: []PathMapping{{Host: "/srv", Local: "/"}}},
		{PathMappings: []PathMapping{{Host: "/srv", Local: "/mnt"}, {Host: "/srv", Local: "/other"}}},
		{PathMappings: []PathMapping{{Host: "/srv", Local: "/mnt"}, {Host: "/other", Local: "/mnt"}}},
		{AllowedPaths: make([]string, 129)},
		{PathMappings: make([]PathMapping, 129)},
	} {
		if _, err := NormalizeCapabilitySettings(in, "docker"); err == nil {
			t.Errorf("accepted invalid settings %#v", in)
		}
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "root")
	if err := os.Symlink("/", link); err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeCapabilitySettings(CapabilitySettings{AllowedPaths: []string{link}}, "binary"); err == nil {
		t.Fatal("accepted symlink root grant")
	}
	if _, err := NormalizeCapabilitySettings(CapabilitySettings{PathMappings: []PathMapping{{Host: "/srv", Local: link}}}, "docker"); err == nil {
		t.Fatal("accepted symlink root mapping")
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeCapabilitySettings(CapabilitySettings{AllowedPaths: []string{file}}, "binary"); err == nil {
		t.Fatal("accepted file as directory")
	}
	got, err := NormalizeCapabilitySettings(CapabilitySettings{AllowedPaths: []string{dir + "/a/../b"}}, "binary")
	if err != nil || got.AllowedPaths[0] != filepath.Join(dir, "b") || got.PathMappings == nil {
		t.Fatal(got, err)
	}
}

func TestSavedCapabilitySettingsStrictSchemaAndStartupOverlay(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base := Config{Mode: "binary", AllowedPaths: []string{"/from-env"}, EnableDaemonWrite: true, ComposePath: "custom-docker", Listen: ":8181"}
	got, err := ApplySavedCapabilitySettings(db, base)
	if err != nil || !reflect.DeepEqual(EffectiveCapabilitySettings(got), EffectiveCapabilitySettings(base)) {
		t.Fatal(got, err)
	}
	saved := CapabilitySettings{AllowedPaths: []string{"/saved"}, PathMappings: []PathMapping{{Host: "/saved", Local: "/mounted"}}}
	raw, _ := json.Marshal(saved)
	if _, err := db.Exec("INSERT INTO settings(key,value) VALUES(?,?)", CapabilitySettingsKey, string(raw)); err != nil {
		t.Fatal(err)
	}
	got, err = ApplySavedCapabilitySettings(db, base)
	if err != nil || !reflect.DeepEqual(EffectiveCapabilitySettings(got), saved) || got.ComposePath != base.ComposePath || got.Listen != base.Listen {
		t.Fatal(got, err)
	}
	if base.AllowedPaths[0] != "/from-env" || !base.EnableDaemonWrite {
		t.Fatal("mutated original snapshot")
	}
	saved.EnableDaemonWrite = true
	raw, _ = json.Marshal(saved)
	if _, err := db.Exec("UPDATE settings SET value=? WHERE key=?", string(raw), CapabilitySettingsKey); err != nil {
		t.Fatal(err)
	}
	base.Mode = "docker"
	got, err = ApplySavedCapabilitySettings(db, base)
	if err != nil || got.EnableDaemonWrite {
		t.Fatal("docker mode gained daemon authorization", got, err)
	}
	for _, bad := range []string{`{`, `{}`, `{"allowed_paths":null,"path_mappings":[],"enable_daemon_write":false}`, `{"allowed_paths":["/"],"path_mappings":[],"enable_daemon_write":false}`, `{"allowed_paths":[],"path_mappings":[],"enable_daemon_write":false,"future":true}`, `{"allowed_paths":[],"path_mappings":[],"enable_daemon_write":false} {}`} {
		if _, err := db.Exec("UPDATE settings SET value=? WHERE key=?", bad, CapabilitySettingsKey); err != nil {
			t.Fatal(err)
		}
		if _, err := ApplySavedCapabilitySettings(db, base); err == nil {
			t.Errorf("corrupt saved data was ignored: %s", bad)
		}
	}
}
