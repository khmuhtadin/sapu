package review

import (
	"context"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/walk"
)

// A tool's review screen names the tool in its header, and its scan line
// does not say it scans caches.
func TestTitledScreenNamesTheTool(t *testing.T) {
	planned := make(chan struct{})
	planFn := func(ctx context.Context, progress func(walk.Event)) (*plan.Plan, error) {
		progress(walk.Event{Path: home + "/Projects/shop", Files: 120, Bytes: 2 * gb})
		<-planned
		return fakePlan(), nil
	}
	m := newModel(context.Background(), env.New(home), planFn, (&fakeApply{}).apply, why)
	m.titled("build folders")
	m.Init()
	m.Update(m.waitScan())
	view := m.View()
	if !strings.HasPrefix(view, "sapu / build folders") || !strings.Contains(view, "  Looking") || strings.Contains(view, "caches") {
		t.Errorf("scan view of a tool:\n%s", view)
	}
	t.Logf("titled scan at 80x24:\n%s", view)
	close(planned)
	m.Update(m.waitScan())
	view = m.View()
	if first := strings.SplitN(view, "\n", 2)[0]; !strings.HasPrefix(first, "sapu / build folders") || !strings.HasSuffix(first, "13 found / 4 skipped") {
		t.Errorf("list header of a tool: %q", first)
	}

	// Run keeps "review" and the scan line of `sapu clean`.
	m = newModel(context.Background(), env.New(home), planFn, (&fakeApply{}).apply, why)
	if !strings.HasPrefix(m.View(), "sapu / review") || !strings.Contains(m.View(), "Scanning caches") {
		t.Errorf("clean's screen changed:\n%s", m.View())
	}
}

// A long tool name on a narrow terminal still fits.
func TestTitledHeaderFits(t *testing.T) {
	m, _ := listed(t)
	m.titled("empty folders and broken links")
	for _, w := range []int{20, 40, 60, 80} {
		m.width = w
		for _, l := range strings.Split(m.View(), "\n") {
			if width(l) > w {
				t.Errorf("at %d a line is %d wide: %q", w, width(l), l)
			}
		}
	}
}
