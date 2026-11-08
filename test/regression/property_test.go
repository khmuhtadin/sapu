package regression

import (
	"fmt"
	"io/fs"
	"maps"
	"math/rand/v2"
	"path"
	"path/filepath"
	"testing"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// randomHome builds a fake home of about 300 random files spread over cache
// folders (items), unrelated folders and protected folders, plus random hard
// links (often across item boundaries), symlinks (absolute, relative,
// dangling, some of them items themselves), xattrs and modes.
func randomHome(t *testing.T, rng *rand.Rand) env.Env {
	t.Helper()
	e := env.New(t.TempDir())
	containers := []string{
		"Library/Caches/app-%d",              // items
		".cache/tool-%d",                     // items
		"Library/Logs/app-%d",                // items
		"Documents/folder-%d",                // unrelated
		"Projects/p%d/src",                   // unrelated
		"Library/Preferences/com.example.%d", // unrelated
		"Library/Caches/JetBrains/IDE-%d",    // protected
		".cache/huggingface/hub/models--m%d", // protected
	}
	var files []string
	for i := range 300 {
		var rel string
		if rng.IntN(10) == 0 {
			rel = fmt.Sprintf("Library/Caches/file-%03d.db", i) // a file item
		} else {
			dir := fmt.Sprintf(containers[rng.IntN(len(containers))], rng.IntN(8))
			for d := rng.IntN(4); d > 0; d-- {
				dir += fmt.Sprintf("/d%d", rng.IntN(3))
			}
			rel = fmt.Sprintf("%s/f%03d", dir, i)
		}
		writeFile(t, homePath(e, rel), randomBytes(rng, rng.IntN(4096)))
		files = append(files, rel)
	}
	pick := func() string { return files[rng.IntN(len(files))] }

	for i := range 10 {
		hardlink(t, homePath(e, path.Join(path.Dir(pick()), fmt.Sprintf("hl-%d", i))), homePath(e, pick()))
	}
	for i := range 12 {
		dir := path.Dir(pick())
		if i < 2 {
			dir = "Library/Caches" // a symlink that is an item itself
		}
		target := pick()
		var to string
		switch rng.IntN(3) {
		case 0:
			to = homePath(e, target)
		case 1:
			var err error
			if to, err = filepath.Rel(homePath(e, dir), homePath(e, target)); err != nil {
				t.Fatal(err)
			}
		default:
			to = "missing/" + target
		}
		symlink(t, homePath(e, path.Join(dir, fmt.Sprintf("sl-%d", i))), to)
	}
	for i := range 15 {
		p := pick()
		if i%3 == 0 {
			p = path.Dir(p)
		}
		setXattr(t, homePath(e, p), "com.example.regression", fmt.Sprintf("value %d", rng.IntN(1000)))
	}
	modes := []fs.FileMode{0o600, 0o400, 0o755, 0o640}
	for range 15 {
		chmod(t, homePath(e, pick()), modes[rng.IntN(len(modes))])
	}
	fixTimes(t, e.Home)
	return e
}

func randomBytes(rng *rand.Rand, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.Uint32())
	}
	return string(b)
}

// Case 12, property (safety invariant): apply touches only selected
// plan items. Over several random homes with a random part of the plan
// unselected, every selected item is gone from its place and sits unchanged
// in quarantine, and every other path is byte-identical.
func TestApplyTouchesOnlyPlanItems(t *testing.T) {
	t.Parallel()
	for seed := uint64(1); seed <= 5; seed++ {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewPCG(seed, 2026))
			e := randomHome(t, rng)
			p := makePlan(t, e, scanItems(t, e, []rules.Rule{
				synthRule("synthetic.caches", rules.RiskSafe, "~/Library/Caches/**"),
				synthRule("synthetic.dot-cache", rules.RiskModerate, "~/.cache/**"),
				synthRule("synthetic.logs", rules.RiskSafe, "~/Library/Logs/**"),
			}))
			if len(p.Items) < 10 {
				t.Fatalf("the plan has %d items; the property needs at least 10", len(p.Items))
			}
			for i := range p.Items {
				p.Items[i].Selected = rng.IntN(3) != 0
			}
			p.Items[0].Selected = false // at least one of each
			p.Items[1].Selected = true
			before := snapHome(t, e)

			res := applyPlan(t, e, p, engine.DefaultOptions())

			for _, f := range res.Failed {
				t.Errorf("unexpected failure on %s: %s", display(e, f.Item.Path), f.Error)
			}
			want := maps.Clone(before)
			var parents []string
			for _, it := range p.Items {
				rel := relPath(t, e, it.Path)
				if !it.Selected {
					requireNotMoved(t, e, res, rel)
					continue
				}
				requireMoved(t, e, res, rel)
				requireSameTree(t, "quarantined ~/"+rel, before.sub(rel), takeSnapshot(t, quarantinePath(e, res.RunID, it)))
				want.removeTree(rel)
				parents = append(parents, path.Dir(rel))
			}
			got := snapHome(t, e)
			want.clearMtime(parents...)
			got.clearMtime(parents...)
			requireSameTree(t, "home outside the selected items", want, got)
		})
	}
}
