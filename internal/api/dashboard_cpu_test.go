package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/docker-manager/docker-manager/internal/config"
	"github.com/docker-manager/docker-manager/internal/database"
	"github.com/docker-manager/docker-manager/internal/hostmetrics"
	"github.com/docker-manager/docker-manager/internal/platform"
	"github.com/docker/docker/client"
	"github.com/gin-gonic/gin"
)

type readyCPUFixture struct{ calls int }

func (f *readyCPUFixture) Snapshot() hostmetrics.CPUSnapshot {
	f.calls++
	percent := 82.5
	at := time.Date(2026, 10, 19, 1, 2, 3, 0, time.UTC)
	return hostmetrics.CPUSnapshot{Percent: &percent, SampledAt: &at, Status: "ready"}
}

func TestDashboardCPUFieldsRemainSafeWhenUnavailable(t *testing.T) {
	warming := "unsupported"
	if runtime.GOOS == "linux" {
		warming = "warming_up"
	}
	for _, tc := range []struct {
		name, mode, endpoint string
		online               bool
		cpu                  interface {
			Snapshot() hostmetrics.CPUSnapshot
		}
		status string
	}{
		{"uninitialized", "binary", "unix:///var/run/docker.sock", true, nil, "unsupported"},
		{"unsupported deployment", "docker", "unix:///var/run/docker.sock", true, hostmetrics.NewCPUSampler(false), "unsupported"},
		{"baseline awaiting next sample", "binary", "unix:///var/run/docker.sock", true, hostmetrics.NewCPUSampler(true), warming},
		{"Engine unavailable retains CPU schema", "binary", "unix:///var/run/docker.sock", false, nil, "unsupported"},
		{"remote TCP rejects injected local CPU", "binary", "tcp://remote:2376", true, &readyCPUFixture{}, "unsupported"},
		{"remote SSH rejects injected local CPU", "binary", "ssh://remote", true, &readyCPUFixture{}, "unsupported"},
		{"missing endpoint rejects injected local CPU", "binary", "", true, &readyCPUFixture{}, "unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet && r.Method != http.MethodHead {
					t.Errorf("CPU snapshot caused a mutation: %s", r.Method)
				}
				w.Header().Set("Content-Type", "application/json")
				if !tc.online {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				switch {
				case strings.HasSuffix(r.URL.Path, "/_ping"):
					io.WriteString(w, "OK")
				case strings.HasSuffix(r.URL.Path, "/info"):
					io.WriteString(w, `{"Name":"sample-host","NCPU":4,"MemTotal":8192,"Architecture":"x86_64","DockerRootDir":"/"}`)
				case strings.HasSuffix(r.URL.Path, "/version"):
					io.WriteString(w, `{"Version":"28.5.1","ApiVersion":"1.51"}`)
				case strings.HasSuffix(r.URL.Path, "/containers/json"), strings.HasSuffix(r.URL.Path, "/images/json"):
					io.WriteString(w, `[]`)
				case strings.HasSuffix(r.URL.Path, "/volumes"):
					io.WriteString(w, `{"Volumes":[]}`)
				case strings.HasSuffix(r.URL.Path, "/system/df"):
					io.WriteString(w, `{}`)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer engine.Close()
			docker, err := client.NewClientWithOpts(client.WithHost(engine.URL), client.WithVersion("1.51"))
			if err != nil {
				t.Fatal(err)
			}
			defer docker.Close()
			db, err := database.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			a := &platform.App{DB: db, Docker: docker, CPU: tc.cpu, Config: config.Config{Mode: tc.mode, DockerEndpoint: tc.endpoint, ComposePath: "/missing/docker", ResticPath: "/missing/restic"}}
			response := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(response)
			c.Request = httptest.NewRequest(http.MethodGet, "/dashboard", nil)
			before := time.Now()
			dashboard(c, a)
			after := time.Now()
			var out map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			host, ok := out["host"].(map[string]any)
			if !ok || host["cpu_status"] != tc.status {
				t.Fatal(out)
			}
			observed, err := time.Parse(time.RFC3339Nano, fmt.Sprint(host["cpu_observed_at"]))
			if err != nil || observed.Before(before) || observed.After(after) {
				t.Fatalf("CPU observation time must use this server response clock: %v", host["cpu_observed_at"])
			}
			for _, field := range []string{"cpu_percent", "cpu_sampled_at"} {
				value, exists := host[field]
				if !exists || value != nil {
					t.Fatalf("unavailable %s must be explicit null: %v", field, host)
				}
			}
			if tc.online && (host["hostname"] != "sample-host" || host["cpus"] != float64(4)) {
				t.Fatalf("existing host fields changed: %v", host)
			}
			if injected, ok := tc.cpu.(*readyCPUFixture); ok {
				if injected.calls != 0 {
					t.Fatal("remote Engine consulted local CPU sampler")
				}
				for _, field := range []string{"memory_available", "load_1m", "uptime", "disk"} {
					if host[field] != nil {
						t.Fatalf("remote Engine leaked local %s: %v", field, host)
					}
				}
				if host["memory_total"] != float64(8192) {
					t.Fatalf("remote Engine memory total was lost: %v", host)
				}
			}
			if got := out["docker"].(map[string]any)["connected"]; got != tc.online {
				t.Fatalf("Engine state changed: %v", out)
			}
		})
	}
}
