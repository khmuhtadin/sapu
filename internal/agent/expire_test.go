package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/fsop"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// quarantined moves ~/rel to quarantine in a run that started at, with the
// item claiming bytes, and returns the run id.
func quarantined(t *testing.T, e env.Env, rel string, bytes int64, at time.Time) string {
	t.Helper()
	path := filepath.Join(e.Home, rel)
	put(t, filepath.Join(path, "f"), "user run "+rel)
	id, err := plan.IdentityOf(path)
	if err != nil {
		t.Fatal(err)
	}
	it := plan.Item{Source: "test.rule", Path: path, Bytes: bytes, Risk: rules.RiskSafe, Regenerates: true,
		Explain: "Test item.", Action: rules.ActionQuarantine, Identity: id}
	p, err := engine.Plan(t.Context(), e, plan.KindClean, []plan.Item{it})
	if err != nil {
		t.Fatal(err)
	}
	opts := engine.DefaultOptions()
	opts.Now = func() time.Time { return at }
	opts.SkipExpire = true // keep the runs made before this one
	res, err := engine.Apply(t.Context(), e, p, opts)
	if err != nil || len(res.Moved) != 1 {
		t.Fatalf("quarantine %s: %v, %+v", rel, err, res)
	}
	return res.RunID
}

// The scheduled clean never deletes for good: runs already in quarantine
// that are past their age or over the size cap stay. Only a sapu the user
// runs expires them.
func TestScheduledCleanNeverExpiresQuarantine(t *testing.T) {
	_, e := fake(t)
	plans, opts := withTestRules(t)
	fixtureHome(t, e)
	turnOn(t, e, true, false)
	now := time.Now()
	old := quarantined(t, e, "Downloads/old-thing", 1000, now.Add(-30*24*time.Hour))
	big := quarantined(t, e, "Downloads/big-thing", 2*fsop.DefaultMaxBytes, now.Add(-time.Hour))

	run(t, e, now)

	if len(*plans) != 1 {
		t.Fatalf("Apply called %d times", len(*plans))
	}
	if !(*opts)[0].SkipExpire {
		t.Errorf("Apply options %+v, want SkipExpire", (*opts)[0])
	}
	lc := state(t, e).LastClean
	if lc == nil || lc.Result != CleanMoved {
		t.Fatalf("last clean: %+v", lc)
	}
	sizes, err := fsop.QuarantineSize(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{old, big, lc.RunID} {
		if _, ok := sizes[id]; !ok {
			t.Errorf("run %s is no longer in quarantine", id)
		}
	}
	for _, rel := range []string{"Downloads/old-thing", "Downloads/big-thing"} {
		if _, err := os.Lstat(filepath.Join(e.Home, rel)); err == nil {
			t.Errorf("%s is back in place", rel)
		}
	}

	// They were really due: a user's own expiry removes both.
	if _, err := fsop.Expire(e, now, fsop.DefaultMaxAge, fsop.DefaultMaxBytes); err != nil {
		t.Fatal(err)
	}
	if sizes, err = fsop.QuarantineSize(e); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{old, big} {
		if _, ok := sizes[id]; ok {
			t.Errorf("run %s did not expire when the user expired quarantine", id)
		}
	}
}
