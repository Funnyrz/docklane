package workflows

import (
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
)

// Exercise the production parser and actual unique key with the two UTC
// occurrences of the same local minute, without changing the system clock.
func TestDSTRepeatedMinuteHasDistinctUTCInstances(t *testing.T) {
	s := testService(t)
	schedule, err := cron.ParseStandard("CRON_TZ=America/New_York 30 1 * * *")
	if err != nil {
		t.Fatal(err)
	}
	first := schedule.Next(time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC))
	second := schedule.Next(first)
	want := []string{"2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"}
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for i, at := range []time.Time{first, second} {
		stamp := at.UTC().Format(time.RFC3339)
		if stamp != want[i] {
			t.Fatalf("occurrence %d: %s != %s", i, stamp, want[i])
		}
		for attempt := 0; attempt < 2; attempt++ {
			if _, err = s.app.DB.Exec(`INSERT OR IGNORE INTO schedule_instances(policy_id,scheduled_at_utc,error) VALUES('dst',?,'')`, stamp); err != nil {
				t.Fatal(err)
			}
		}
		if local := at.In(location); local.Hour() != 1 || local.Minute() != 30 {
			t.Fatalf("unexpected local occurrence: %s", local)
		}
		t.Logf("actual cron occurrence: local=%s UTC=%s", at.In(location).Format(time.RFC3339), stamp)
	}
	var count int
	if err = s.app.DB.QueryRow(`SELECT count(*) FROM schedule_instances WHERE policy_id='dst'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("UTC dedup count=%d err=%v", count, err)
	}
}
