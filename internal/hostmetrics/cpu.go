// Package hostmetrics samples metrics from the local host without consulting Docker.
package hostmetrics

import (
	"context"
	"errors"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CPUSnapshot describes the latest complete sampling interval. Values are absent
// until two valid, increasing counter readings are available.
type CPUSnapshot struct {
	Percent   *float64
	SampledAt *time.Time
	Status    string
}

// SupportsLocalHost prevents local /proc and filesystem metrics from being
// attributed to an Engine reached through a remote endpoint.
func SupportsLocalHost(goos, mode, endpoint string) bool {
	return goos == "linux" && mode == "binary" && strings.HasPrefix(endpoint, "unix://")
}

type cpuCounters [8]uint64

// CPUSampler belongs to one application instance. Enable it only when the
// application runs directly on a Linux host, never inside a Docker deployment.
type CPUSampler struct {
	mu       sync.RWMutex
	snapshot CPUSnapshot
	previous *cpuCounters
	enabled  bool
	read     func() ([]byte, error)
	interval time.Duration
	once     sync.Once
	done     chan struct{}
}

func NewCPUSampler(enabled bool) *CPUSampler {
	status := "unsupported"
	if enabled {
		status = "warming_up"
	}
	return &CPUSampler{
		enabled: enabled, snapshot: CPUSnapshot{Status: status},
		read:     func() ([]byte, error) { return os.ReadFile("/proc/stat") },
		interval: 15 * time.Second, done: make(chan struct{}),
	}
}

// Start establishes a baseline immediately and samples until ctx is canceled.
// Calling it more than once does not create additional sampling goroutines.
func (s *CPUSampler) Start(ctx context.Context) {
	s.once.Do(func() {
		if !s.enabled || ctx.Err() != nil {
			close(s.done)
			return
		}
		s.sample(time.Now())
		go func() {
			defer close(s.done)
			ticker := time.NewTicker(s.interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case at := <-ticker.C:
					if ctx.Err() != nil {
						return
					}
					s.sample(at)
				}
			}
		}()
	})
}

// Wait waits for a started sampler to release its ticker after cancellation.
func (s *CPUSampler) Wait() { <-s.done }

// Snapshot returns an independent copy safe for concurrent API requests. A nil
// or zero-value sampler reports unsupported rather than a fabricated zero.
func (s *CPUSampler) Snapshot() CPUSnapshot {
	if s == nil {
		return CPUSnapshot{Status: "unsupported"}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := s.snapshot
	if result.Status == "" {
		result.Status = "unsupported"
	}
	if result.Percent != nil {
		value := *result.Percent
		result.Percent = &value
	}
	if result.SampledAt != nil {
		value := *result.SampledAt
		result.SampledAt = &value
	}
	return result
}

func (s *CPUSampler) sample(at time.Time) {
	data, err := s.read()
	var current cpuCounters
	if err == nil {
		current, err = parseCPU(data)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.previous = nil
		s.snapshot = CPUSnapshot{Status: "unavailable"}
		return
	}
	previous := s.previous
	s.previous = &current
	if previous == nil {
		s.snapshot = CPUSnapshot{Status: "warming_up"}
		return
	}
	percent, valid := cpuPercent(*previous, current)
	if !valid {
		s.snapshot = CPUSnapshot{Status: "unavailable"}
		return
	}
	s.snapshot = CPUSnapshot{Percent: &percent, SampledAt: &at, Status: "ready"}
}

func parseCPU(data []byte) (cpuCounters, error) {
	var counters cpuCounters
	first, _, _ := strings.Cut(string(data), "\n")
	fields := strings.Fields(first)
	// Linux aggregate counters always contain at least user/nice/system/idle.
	if len(fields) < 5 || fields[0] != "cpu" {
		return counters, errors.New("aggregate CPU counters missing")
	}
	var total uint64
	for i := range counters {
		if i+1 >= len(fields) {
			break // Older kernels may omit iowait, irq, softirq, and steal.
		}
		value, err := strconv.ParseUint(fields[i+1], 10, 64)
		if err != nil || value > math.MaxUint64-total {
			return cpuCounters{}, errors.New("invalid aggregate CPU counters")
		}
		counters[i] = value
		total += value
	}
	// guest and guest_nice are already counted in user and nice above.
	return counters, nil
}

func cpuPercent(previous, current cpuCounters) (float64, bool) {
	var total, idle uint64
	for i, value := range current {
		if value < previous[i] {
			return 0, false // Reset/reboot: the current reading becomes a new baseline.
		}
		delta := value - previous[i]
		if delta > math.MaxUint64-total {
			return 0, false
		}
		total += delta
		if i == 3 || i == 4 {
			idle += delta
		}
	}
	if total == 0 || idle > total {
		return 0, false
	}
	return float64(total-idle) / float64(total) * 100, true
}
