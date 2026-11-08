package explain

import (
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/trends"
)

var reconcileRows = []Row{
	{ID: "spotlight", Bytes: 2_000},
	{ID: "system_assets", Bytes: -1},
	{ID: "swap", Bytes: 3_000},
	{ID: "user_caches", Bytes: 50_000}, // inside home: not added again
	{ID: "messages", Bytes: -1},        // inside home: not counted as unmeasured system
}

func TestReconcileAddsUp(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	home := &trends.Snapshot{Taken: now.Add(-2 * time.Hour), HomeBytes: 60_000}
	r := Reconcile(reconcileRows, 200_000, 100_000, home, now)

	if r.SystemBytes != 5_000 || r.SystemUnmeasured != 1 {
		t.Errorf("system: %d bytes, %d unmeasured; want 5000 and 1", r.SystemBytes, r.SystemUnmeasured)
	}
	if r.HomeBytes != 60_000 || r.HomeTaken == nil {
		t.Errorf("home: %d, taken %v", r.HomeBytes, r.HomeTaken)
	}
	if r.RestBytes != 35_000 {
		t.Errorf("rest %d, want 35000", r.RestBytes)
	}
	if r.HomeBytes+r.SystemBytes+r.RestBytes != r.UsedBytes {
		t.Errorf("parts %d do not add up to used %d", r.HomeBytes+r.SystemBytes+r.RestBytes, r.UsedBytes)
	}
	if r.Note != "" {
		t.Errorf("unexpected note %q", r.Note)
	}
}

func TestReconcileWithoutFreshHome(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	for name, home := range map[string]*trends.Snapshot{
		"none":  nil,
		"stale": {Taken: now.Add(-25 * time.Hour), HomeBytes: 60_000},
	} {
		r := Reconcile(reconcileRows, 200_000, 100_000, home, now)
		if r.HomeBytes != -1 || r.RestBytes != -1 || r.HomeTaken != nil {
			t.Errorf("%s: home %d, rest %d, taken %v; want -1, -1, nil", name, r.HomeBytes, r.RestBytes, r.HomeTaken)
		}
		if r.UsedBytes != 100_000 || r.SystemBytes != 5_000 {
			t.Errorf("%s: used %d, system %d", name, r.UsedBytes, r.SystemBytes)
		}
	}
}

func TestReconcileNeverNegative(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	home := &trends.Snapshot{Taken: now, HomeBytes: 99_000}
	r := Reconcile(reconcileRows, 200_000, 100_000, home, now)
	if r.RestBytes != 0 || r.Note == "" {
		t.Errorf("rest %d, note %q; want 0 and a note", r.RestBytes, r.Note)
	}
}
