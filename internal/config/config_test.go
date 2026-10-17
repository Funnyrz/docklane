package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDockerModeCannotGrantHostDaemonWrite(t *testing.T) {
	t.Setenv("DM_MODE", "docker")
	t.Setenv("DM_ENABLE_DAEMON_WRITE", "true")
	c, e := Load()
	if e != nil {
		t.Fatal(e)
	}
	if c.EnableDaemonWrite {
		t.Fatal("Docker mode acquired daemon write")
	}
}
func TestConfigurationRefusesRootAuthorization(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if e := os.WriteFile(p, []byte(`{"allowed_paths":["/"]}`), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("DM_CONFIG", p)
	if _, e := Load(); e == nil {
		t.Fatal("root authorization accepted")
	}
}

func TestNewerConfigurationIsRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if e := os.WriteFile(p, []byte(`{"version":2}`), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("DM_CONFIG", p)
	if _, e := Load(); e == nil {
		t.Fatal("future config accepted")
	}
}
