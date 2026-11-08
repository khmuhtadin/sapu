package regression

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/fsop"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// richHome builds a fake home whose cache items hold everything a copy would
// get wrong: modes, xattrs on files, folders and symlinks, hard links inside
// an item and across the item boundary, absolute, relative and dangling
// symlinks, a symlink as an item of its own, empty files and folders, and a
// 1 MiB file.
func richHome(t *testing.T) env.Env {
	t.Helper()
	e := newHome(t, map[string]string{
		"Documents/report.pdf":                               "%PDF-1.7 quarterly report",
		"Documents/notes.txt":                                "notes",
		"Library/Caches/com.example.app/Cache.db":            "sqlite cache",
		"Library/Caches/com.example.app/Cache.db-wal":        "wal",
		"Library/Caches/com.example.app/fsCachedData/0A1B":   "blob",
		"Library/Caches/com.example.app/big.bin":             strings.Repeat("sapu", 256*1024),
		"Library/Caches/com.example.app/empty.txt":           "",
		"Library/Caches/com.example.app/empty-folder/":       "",
		"Library/Caches/com.example.app/private/token.cache": "token",
		"Library/Caches/com.example.app/run.sh":              "#!/bin/sh\n",
		"Library/Caches/com.example.app/readonly.dat":        "read only",
		"Library/Caches/com.example.app/pair-a":              "one inode, two names",
		"Library/Caches/loose.tmp":                           "a file item",
		".cache/pip/http/a/b/c/d/entry":                      "pip",
	})
	app := homePath(e, "Library/Caches/com.example.app")
	hardlink(t, filepath.Join(app, "pair-b"), filepath.Join(app, "pair-a"))
	hardlink(t, filepath.Join(app, "report-link.pdf"), homePath(e, "Documents/report.pdf"))
	symlink(t, filepath.Join(app, "docs-abs"), homePath(e, "Documents"))
	symlink(t, filepath.Join(app, "report-rel"), "../../../Documents/report.pdf")
	symlink(t, filepath.Join(app, "dangling"), "does/not/exist")
	symlink(t, homePath(e, "Library/Caches/docs-link"), "../../Documents")
	chmod(t, filepath.Join(app, "Cache.db-wal"), 0o600)
	chmod(t, filepath.Join(app, "private"), 0o700)
	chmod(t, filepath.Join(app, "run.sh"), 0o755)
	chmod(t, filepath.Join(app, "readonly.dat"), 0o400)
	setXattr(t, app, "com.example.on-folder", "folder")
	setXattr(t, filepath.Join(app, "fsCachedData/0A1B"), "com.example.where-from", "https://example.com/0A1B")
	setXattr(t, filepath.Join(app, "docs-abs"), "com.example.on-symlink", "symlink")
	setXattr(t, homePath(e, "Library/Caches/loose.tmp"), "com.example.on-file", "file")
	fixTimes(t, e.Home)
	return e
}

func richRules() []rules.Rule {
	return []rules.Rule{
		synthRule("synthetic.caches", rules.RiskSafe, "~/Library/Caches/**"),
		synthRule("synthetic.dot-cache", rules.RiskModerate, "~/.cache/**"),
	}
}

// richItems are items richRules must find in richHome and move. The symlink
// ~/Library/Caches/docs-link is a match too; whether scan turns a symlink
// into an item is up to scan, so the tests below work either way and use
// Result.Moved for the full list.
var richItems = []string{
	"Library/Caches/com.example.app",
	"Library/Caches/loose.tmp",
	".cache/pip",
}

// Case 15: undo puts back exactly what apply moved: contents, mode, mtime,
// xattrs, symlinks and hard links. Only the mtimes of the folders the items
// were moved out of may differ, because every rename changes them.
func TestUndoRestoresIdentically(t *testing.T) {
	t.Parallel()
	e := richHome(t)
	before := snapHome(t, e)
	_, res := clean(t, e, richRules())
	for _, rel := range richItems {
		requireMoved(t, e, res, rel)
	}

	restored, err := fsop.Restore(e, res.RunID)

	if err != nil {
		t.Fatalf("fsop.Restore: %v", err)
	}
	if len(restored) != len(res.Moved) {
		t.Errorf("restored %d items, want %d", len(restored), len(res.Moved))
	}
	for _, r := range restored {
		if r.Conflict {
			t.Errorf("unexpected conflict restoring %s to %s", r.From, display(e, r.To))
		}
		if !inItems(res.Moved, r.To) {
			t.Errorf("restored to %s, which is not where a moved item came from", display(e, r.To))
		}
	}
	after := snapHome(t, e)
	parents := parentDirs(t, e, res.Moved)
	before.clearMtime(parents...)
	after.clearMtime(parents...)
	requireSameTree(t, "home after undo", before, after)
}

// Case 21: --permanent moves the items to quarantine and then empties that
// run's quarantine. The items are gone for good; everything else, including
// the targets of symlinks and the other names of hard links inside the
// deleted items, is byte-identical.
func TestPermanentEmptiesOnlyTheRunQuarantine(t *testing.T) {
	t.Parallel()
	e := richHome(t)
	before := snapHome(t, e)
	p := makePlan(t, e, scanItems(t, e, richRules()))
	opts := engine.DefaultOptions()
	opts.Permanent = true

	res := applyPlan(t, e, p, opts)

	for _, rel := range richItems {
		requireMoved(t, e, res, rel)
	}
	requireRunQuarantineEmpty(t, e, res.RunID)
	if res.Freed <= 0 {
		t.Errorf("Result.Freed = %d, want the bytes deleted for good", res.Freed)
	}
	want := before
	for _, it := range res.Moved {
		want.removeTree(relPath(t, e, it.Path))
	}
	want.setNlink("Documents/report.pdf", 1) // its second name was inside com.example.app
	got := snapHome(t, e)
	parents := parentDirs(t, e, res.Moved)
	want.clearMtime(parents...)
	got.clearMtime(parents...)
	requireSameTree(t, "home outside the deleted items", want, got)
}

// Case 22: expiry deletes quarantine content only. The rest of the home, the
// whitelist and the run history are untouched, even though the quarantined
// items hold symlinks into ~/Documents and a hard link to a file there.
func TestExpireDeletesOnlyQuarantine(t *testing.T) {
	t.Parallel()
	e := richHome(t)
	whitelist(t, e, "~/Documents/notes.txt")
	_, res := clean(t, e, richRules())
	for _, rel := range richItems {
		requireMoved(t, e, res, rel)
	}
	before := snapHome(t, e)
	beforeWhitelist := takeSnapshot(t, e.WhitelistFile())
	farFuture := time.Now().AddDate(100, 0, 0)

	freed, err := fsop.Expire(e, farFuture, fsop.DefaultMaxAge, fsop.DefaultMaxBytes)

	if err != nil {
		t.Fatalf("fsop.Expire: %v", err)
	}
	if freed <= 0 {
		t.Errorf("Expire freed %d bytes, want the run's quarantine", freed)
	}
	requireRunQuarantineEmpty(t, e, res.RunID)
	before.setNlink("Documents/report.pdf", 1) // its second name was in quarantine
	requireSameTree(t, "home outside Sapu's state folder", before, snapHome(t, e))
	requireSameTree(t, "whitelist file", beforeWhitelist, takeSnapshot(t, e.WhitelistFile()))
	requireExists(t, filepath.Join(e.RunsDir(), res.RunID, "plan.json"))
	requireExists(t, filepath.Join(e.RunsDir(), res.RunID, "journal.jsonl"))
}
