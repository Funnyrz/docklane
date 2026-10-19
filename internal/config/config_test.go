package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/docker-manager/docker-manager/internal/buildinfo"
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

func TestApplicationReleaseDoesNotChangeConfigurationSchema(t *testing.T) {
	t.Setenv("DM_CONFIG", "")
	t.Setenv("DM_BACKUP_WORKER_IMAGE", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != 1 {
		t.Fatalf("configuration schema changed: %d", c.Version)
	}
	if c.BackupWorkerImage != "docklane:v"+buildinfo.Version {
		t.Fatal(c.BackupWorkerImage)
	}
}

func TestListenDefaultsAndOverrides(t *testing.T) {
	for _, test := range []struct {
		name   string
		config string
		env    string
		want   string
	}{
		{name: "default", want: ":9999"},
		{name: "configuration omits listener", config: `{}`, want: ":9999"},
		{name: "configured legacy port", config: `{"listen":":8080"}`, want: ":8080"},
		{name: "environment legacy port", config: `{}`, env: "127.0.0.1:8080", want: "127.0.0.1:8080"},
		{name: "environment overrides configuration", config: `{"listen":":9999"}`, env: ":8080", want: ":8080"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := ""
			if test.config != "" {
				p = filepath.Join(t.TempDir(), "config.json")
				if err := os.WriteFile(p, []byte(test.config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("DM_CONFIG", p)
			t.Setenv("DM_LISTEN", test.env)
			c, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if c.Listen != test.want {
				t.Fatalf("listener = %q, want %q", c.Listen, test.want)
			}
		})
	}
}
