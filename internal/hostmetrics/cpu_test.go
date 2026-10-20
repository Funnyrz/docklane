package hostmetrics

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLocalHostSupportRequiresLinuxBinaryAndUnixEngine(t *testing.T) {
	for _, tc := range []struct {
		goos, mode, endpoint string
		want                 bool
	}{
		{"linux", "binary", "unix:///var/run/docker.sock", true},
		{"linux", "binary", "unix:///run/user/1000/docker.sock", true},
		{"darwin", "binary", "unix:///var/run/docker.sock", false},
		{"linux", "docker", "unix:///var/run/docker.sock", false},
		{"linux", "binary", "tcp://remote:2376", false},
		{"linux", "binary", "ssh://remote", false},
		{"linux", "binary", "http://remote:2375", false},
		{"linux", "binary", "", false},
	} {
		if got := SupportsLocalHost(tc.goos, tc.mode, tc.endpoint); got != tc.want {
			t.Errorf("SupportsLocalHost(%q,%q,%q)=%v; want %v", tc.goos, tc.mode, tc.endpoint, got, tc.want)
		}
	}
}

func TestAggregateCPUParsingIgnoresGuestAndPerCPURecords(t *testing.T) {
	got, err := parseCPU([]byte("cpu 10 20 30 40 50 60 70 80 900 800\ncpu0 999 999 999 999\n"))
	if err != nil || got != (cpuCounters{10, 20, 30, 40, 50, 60, 70, 80}) {
		t.Fatalf("%v, %v", got, err)
	}
	old, err := parseCPU([]byte("cpu 10 20 30 40\n"))
	if err != nil || old != (cpuCounters{10, 20, 30, 40}) {
		t.Fatalf("old kernel: %v, %v", old, err)
	}
	for _, invalid := range []string{"", "cpu0 1 2 3 4", "cpu 1 2 3", "cpu -1 2 3 4", "cpu x 2 3 4", "cpu 18446744073709551615 1 0 0"} {
		if _, err := parseCPU([]byte(invalid)); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
}

func TestCPUPercentUsesDeltaAndTreatsIOWaitAsIdle(t *testing.T) {
	previous := cpuCounters{100, 20, 30, 200, 40, 5, 5, 0}
	current := cpuCounters{120, 25, 40, 240, 55, 10, 10, 0}
	got, valid := cpuPercent(previous, current)
	if !valid || math.Abs(got-45) > 0.000001 {
		t.Fatalf("got %v, %v; want 45%%", got, valid)
	}
	for _, tc := range []struct {
		name              string
		previous, current cpuCounters
	}{
		{"unchanged", current, current},
		{"counter reset", current, previous},
		{"one field reset even when total increases", cpuCounters{10, 0, 0, 10}, cpuCounters{9, 100, 0, 20}},
		{"overflow", cpuCounters{}, cpuCounters{math.MaxUint64, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, valid := cpuPercent(tc.previous, tc.current); valid || math.IsNaN(got) || math.IsInf(got, 0) {
				t.Fatalf("got %v, valid %v", got, valid)
			}
		})
	}
}

func TestSamplerInitialBaselineAndErrorRecovery(t *testing.T) {
	s := NewCPUSampler(true)
	var content string
	var readErr error
	s.read = func() ([]byte, error) { return []byte(content), readErr }
	at := time.Date(2026, 10, 19, 1, 2, 3, 456, time.UTC)
	check := func(want string, percent float64) {
		t.Helper()
		got := s.Snapshot()
		if got.Status != want {
			t.Fatalf("got %+v; want %s", got, want)
		}
		if want != "ready" {
			if got.Percent != nil || got.SampledAt != nil {
				t.Fatalf("missing interval fabricated a metric: %+v", got)
			}
			return
		}
		if got.Percent == nil || *got.Percent != percent || got.SampledAt == nil || !got.SampledAt.Equal(at) {
			t.Fatalf("got %+v; want %v at %v", got, percent, at)
		}
	}
	check("warming_up", 0)
	content = "cpu 100 0 0 100 0 0 0 0 9999 9999"
	s.sample(at)
	check("warming_up", 0)
	content = "cpu 125 0 0 175 0 0 0 0 99999 99999"
	s.sample(at)
	check("ready", 25)
	// Readers cannot mutate the sampler's stored values through returned pointers.
	copy := s.Snapshot()
	*copy.Percent = 99
	*copy.SampledAt = time.Time{}
	check("ready", 25)
	readErr = errors.New("read failure")
	s.sample(at)
	check("unavailable", 0)
	readErr = nil
	content = "cpu 200 0 0 300"
	s.sample(at)
	check("warming_up", 0)
	content = "cpu 250 0 0 350"
	s.sample(at)
	check("ready", 50)
	// A reset establishes a baseline; the next valid interval can recover.
	content = "cpu 10 0 0 10"
	s.sample(at)
	check("unavailable", 0)
	content = "cpu 20 0 0 40"
	s.sample(at)
	check("ready", 25)
	s.sample(at)
	check("unavailable", 0)
	content = "cpu 30 0 0 70"
	s.sample(at)
	check("ready", 25)
	content = "invalid"
	s.sample(at)
	check("unavailable", 0)
	content = "cpu 40 0 0 80"
	s.sample(at)
	check("warming_up", 0)
}

func TestUnsupportedSamplerNeverReadsHost(t *testing.T) {
	s := NewCPUSampler(false)
	s.read = func() ([]byte, error) { t.Fatal("unsupported environment accessed host"); return nil, nil }
	s.Start(context.Background())
	s.Wait()
	if got := s.Snapshot(); got.Status != "unsupported" || got.Percent != nil || got.SampledAt != nil {
		t.Fatal(got)
	}
	for _, sampler := range []*CPUSampler{nil, {}} {
		if got := sampler.Snapshot(); got.Status != "unsupported" || got.Percent != nil || got.SampledAt != nil {
			t.Fatal(got)
		}
	}
}

func TestStartEstablishesOnlyOneBaseline(t *testing.T) {
	s := NewCPUSampler(true)
	s.interval = time.Hour
	reads := 0
	s.read = func() ([]byte, error) { reads++; return []byte("cpu 1 2 3 4"), nil }
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	s.Start(ctx)
	if reads != 1 || s.Snapshot().Status != "warming_up" {
		t.Fatalf("baseline reads %d, snapshot %+v", reads, s.Snapshot())
	}
	cancel()
	s.Wait()
}

func TestReadySnapshotSerializesFinitePercentAndSampleTime(t *testing.T) {
	s := NewCPUSampler(true)
	value := "cpu 10 0 0 10"
	s.read = func() ([]byte, error) { return []byte(value), nil }
	at := time.Date(2026, 10, 19, 1, 2, 3, 123456789, time.UTC)
	s.sample(at.Add(-15 * time.Second))
	value = "cpu 20 0 0 40"
	s.sample(at)
	snapshot := s.Snapshot()
	data, err := json.Marshal(map[string]any{"cpu_percent": snapshot.Percent, "cpu_sampled_at": snapshot.SampledAt, "cpu_status": snapshot.Status})
	if err != nil || string(data) != `{"cpu_percent":25,"cpu_sampled_at":"2026-10-19T01:02:03.123456789Z","cpu_status":"ready"}` {
		t.Fatalf("%s, %v", data, err)
	}
}

func TestSamplerLifecycleConcurrentSnapshotsAndCancellation(t *testing.T) {
	s := NewCPUSampler(true)
	s.interval = time.Millisecond
	var reads atomic.Int64
	s.read = func() ([]byte, error) {
		reads.Add(1)
		return []byte("cpu 100 0 0 100"), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	s.Start(ctx)
	if reads.Load() < 1 {
		t.Fatal("baseline was not immediate")
	}
	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 500 {
				s.Snapshot()
			}
		}()
	}
	deadline := time.After(time.Second)
	for reads.Load() < 2 {
		select {
		case <-deadline:
			cancel()
			t.Fatal("ticker did not sample")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	s.Wait()
	readers.Wait()
	finished := reads.Load()
	time.Sleep(5 * time.Millisecond)
	if reads.Load() != finished {
		t.Fatal("sampler read after shutdown")
	}
	// Cancellation before Start must not perform an initial host read.
	canceled := NewCPUSampler(true)
	canceled.read = func() ([]byte, error) { t.Fatal("read after canceled Start"); return nil, nil }
	canceled.Start(ctx)
	canceled.Wait()
}
