package engine

import (
	"path/filepath"
	"testing"

	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// A plan edited by hand cannot borrow the source of a command with an
// exception: "messages" on ~/Library/Messages and "backups" on all of
// MobileSync are skipped as protected, at Plan and again at Apply. One
// backup folder from sapu backups is still moved.
func TestHandEditedSourceCannotOpenProtection(t *testing.T) {
	t.Parallel()
	backup := "Library/Application Support/MobileSync/Backup/00008110-001A2B3C4D5E"
	e := newHome(t, map[string]string{
		"Library/Messages/chat.db":                                                "messages",
		"Library/Messages/Attachments/ab/IMG_1.heic":                              "photo",
		backup + "/Manifest.db":                                                   "backup",
		"Library/Application Support/MobileSync/Backup/00008110-FFFF/Manifest.db": "another backup",
	})
	sourced := func(rel, source string) plan.Item {
		it := item(t, e, rel, rules.RiskSafe)
		it.Source = source
		return it
	}
	blocked := []plan.Item{
		sourced("Library/Messages", plan.KindMessages),
		sourced("Library/Messages/Attachments/ab/IMG_1.heic", plan.KindMessages),
		sourced("Library/Application Support/MobileSync", plan.KindBackups),
		sourced("Library/Application Support/MobileSync/Backup", plan.KindBackups),
		sourced(backup+"/Manifest.db", plan.KindBackups),
		sourced("Library/Application Support/MobileSync/Backup/00008110-FFFF", "large"),
	}

	p := makePlan(t, e, append(blocked, sourced(backup, plan.KindBackups))...)
	if len(p.Items) != 1 || p.Items[0].Path != homePath(e, backup) || len(p.Skipped) != len(blocked) {
		t.Fatalf("plan items %v, skipped %+v", paths(p.Items), p.Skipped)
	}
	for _, s := range p.Skipped {
		if s.Reason != plan.ReasonProtected {
			t.Errorf("plan skip %+v", s)
		}
	}

	// The same items put into the plan file by hand.
	for i, it := range blocked {
		it.ID = "x" + string(rune('a'+i))
		it.Selected = true
		p.Items = append(p.Items, it)
	}
	res, err := Apply(t.Context(), e, p, DefaultOptions())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Moved) != 1 || res.Moved[0].Path != homePath(e, backup) || len(res.Failed) != 0 {
		t.Fatalf("moved %v, failed %+v", paths(res.Moved), res.Failed)
	}
	if len(res.Skipped) != len(blocked) {
		t.Fatalf("skipped %+v", res.Skipped)
	}
	for _, s := range res.Skipped {
		if s.Reason != plan.ReasonProtected {
			t.Errorf("apply skip %+v", s)
		}
	}
	for _, rel := range []string{"Library/Messages/chat.db", "Library/Messages/Attachments/ab/IMG_1.heic",
		"Library/Application Support/MobileSync/Backup/00008110-FFFF/Manifest.db"} {
		requireExists(t, filepath.Join(e.Home, rel))
	}
}
