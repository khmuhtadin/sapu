package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/fsop"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/review"
)

// fakeTerminal makes every stdin and stdout look like a terminal, so a test
// can check that the flags alone keep the text flow. A test that uses it
// must never reach the review screen: there is no terminal to draw on.
func fakeTerminal(t *testing.T) {
	t.Helper()
	old := isTerminal
	isTerminal = func(any) bool { return true }
	t.Cleanup(func() { isTerminal = old })
}

func TestUseReview(t *testing.T) {
	// jsonOut is shared by every command test; an earlier --json run can
	// leave it set.
	jsonOut = false
	t.Cleanup(func() { jsonOut = false })
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = devNull.Close() }()
	cmd := &cobra.Command{}
	cmd.SetIn(devNull)
	cmd.SetOut(devNull)
	if useReview(cmd, &cleanupFlags{}) {
		t.Error("/dev/null is not a terminal")
	}
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(&bytes.Buffer{})
	if useReview(cmd, &cleanupFlags{}) {
		t.Error("a pipe is not a terminal")
	}

	fakeTerminal(t)
	if !useReview(cmd, &cleanupFlags{}) {
		t.Fatal("a terminal with no flags should open the review screen")
	}
	for name, f := range map[string]cleanupFlags{
		"--yes":          {yes: true},
		"--plan":         {planFile: "p.json"},
		"--permanent":    {permanent: true},
		"--run-commands": {runCommands: true},
	} {
		if useReview(cmd, &f) {
			t.Errorf("%s should keep the text flow", name)
		}
	}
	jsonOut = true
	if useReview(cmd, &cleanupFlags{}) {
		t.Error("--json should keep the text flow")
	}
	jsonOut = false
	t.Setenv("SAPU_NO_TUI", "1")
	if useReview(cmd, &cleanupFlags{}) {
		t.Error("SAPU_NO_TUI should keep the text flow")
	}
}

// With a terminal, --yes, --plan and --json still run the text flow end to
// end (in a temp home).
func TestCleanFlagsKeepTheTextFlowOnATerminal(t *testing.T) {
	fakeTerminal(t)

	e := newHome(t)
	file := filepath.Join(t.TempDir(), "p.json")
	out, errOut, code := sapu(t, e, "", "clean", "--plan", file)
	if code != 0 || !strings.Contains(out, "Plan written to") {
		t.Fatalf("--plan: exit %d\n%s\n%s", code, out, errOut)
	}
	noState(t, e)

	out, errOut, code = sapu(t, e, "", "clean", "--yes", "--json")
	if code != 0 {
		t.Fatalf("--yes --json: exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "clean", out)

	e = newHome(t)
	out, errOut, code = sapu(t, e, "", "clean", "--yes")
	if code != 0 || !strings.Contains(out, "Moved 4 items") {
		t.Fatalf("--yes: exit %d\n%s\n%s", code, out, errOut)
	}
}

func TestFinishReviewExitCodes(t *testing.T) {
	start := time.Date(2026, 11, 8, 11, 15, 0, 0, time.Local)
	moved := plan.Item{Path: "/nonexistent/home/Library/Caches/x", Bytes: 2_000_000, Action: "quarantine"}
	result := func(skipped int, failed int) *engine.Result {
		r := &engine.Result{RunID: "20261108-111500-a3f9", Moved: []plan.Item{moved}, Bytes: moved.Bytes}
		for range skipped {
			r.Skipped = append(r.Skipped, plan.Skip{Path: "/nonexistent/home/Library/Caches/y", Reason: plan.ReasonChanged, Detail: "changed"})
		}
		for range failed {
			r.Failed = append(r.Failed, engine.Failure{Item: moved, Error: "refused"})
		}
		return r
	}
	for _, tc := range []struct {
		name string
		res  review.Outcome
		err  error
		code int
		out  []string
	}{
		{"quit without moving", review.Outcome{}, nil, 0, []string{"Nothing was changed."}},
		{"cancelled before moving", review.Outcome{Cancelled: true}, nil, 130, []string{"Nothing was changed."}},
		{"moved everything", review.Outcome{Result: result(0, 0), Start: start}, nil, 0, []string{
			"Moved 1 item (2.0 MB) to quarantine, run 20261108-111500-a3f9.",
			"until at least " + start.Add(fsop.DefaultMaxAge).Format("Mon 2 Jan"),
			"`sapu quarantine empty 20261108-111500-a3f9`",
			"Undo: `sapu undo 20261108-111500-a3f9`",
		}},
		{"deleted for good", review.Outcome{Result: result(0, 0), Start: start, Deleted: true}, nil, 0, []string{"for good. There is no undo."}},
		{"something skipped", review.Outcome{Result: result(1, 0), Start: start}, nil, 3, []string{"Skipped while applying"}},
		{"something failed", review.Outcome{Result: result(0, 1), Start: start}, nil, 3, []string{"Failed (1)"}},
		{"stopped with ctrl-c", review.Outcome{Result: result(0, 0), Start: start, Err: context.Canceled, Cancelled: true}, nil, 130, []string{"Moved 1 item"}},
		{"apply failed", review.Outcome{Err: fsop.ErrLocked}, nil, 1, nil},
		{"scan failed", review.Outcome{}, errors.New("scan failed"), 1, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := finishReview(&out, "/nonexistent/home", config.Defaults(), tc.res, tc.err)
			code := 0
			if err != nil {
				code, _ = exitCodeOf(context.Background(), err)
			}
			if code != tc.code {
				t.Errorf("exit %d, want %d (%v)", code, tc.code, err)
			}
			for _, want := range tc.out {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output lacks %q:\n%s", want, out.String())
				}
			}
		})
	}
}

func TestSummaryDesignLines(t *testing.T) {
	t.Setenv("COLUMNS", "80")
	e := junkHome(t)
	out, _, code := sapu(t, e, "", "")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	checkHuman(t, out)
	if !strings.Contains(out, "  Quarantine      Empty") {
		t.Errorf("summary lacks the empty quarantine:\n%s", out)
	}

	// Put one run in quarantine (temp home), then look again.
	if _, errOut, code := sapu(t, e, "", "clean", "--yes"); code != 0 {
		t.Fatalf("clean: exit %d\n%s", code, errOut)
	}
	out, _, code = sapu(t, e, "", "")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	checkHuman(t, out)
	expires := time.Now().Add(fsop.DefaultMaxAge).Format("Mon 2 Jan")
	for _, want := range []string{
		"  sapu / CLI / Open source",
		"  A careful Mac cleanup.",
		"  Disk ", " used / ", " total",
		"  [", "#", "]   ", " free",
		"  FOUND FOR REVIEW",
		"    SAFE ", "Recreated by their applications",
		"    MODERATE ", "Review the download or rebuild cost",
		"    RISKY ", "0 items    May contain personal data",
		"  Left alone      1 protected",
		"  Quarantine      1 run / ", "First can expire after " + expires,
		"  Disk access     Partial", "Some folders were not measured",
		"  NEXT",
		"    sapu clean --plan plan.json        Save a plan. Changes no files.",
		"    sapu clean                         Review candidates interactively.",
		"    sapu explain                       Understand System Data.",
		"    sapu doctor                        Check access and setup.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "+###") {
		t.Errorf("the summary has no mascot:\n%s", out)
	}

	// --json keeps its fields with a run in quarantine.
	out, _, code = sapu(t, e, "", "--json")
	if code != 0 {
		t.Fatalf("--json: exit %d", code)
	}
	validate(t, "summary", out)
}

// Below 60 columns the summary stacks its rows (design 10).
func TestNarrowSummary(t *testing.T) {
	t.Setenv("COLUMNS", "48")
	e := junkHome(t)
	out, _, code := sapu(t, e, "", "")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, line := range strings.Split(out, "\n") {
		if n := utf8.RuneCountInString(line); n > 48 {
			t.Errorf("line is %d wide, want at most 48: %q", n, line)
		}
	}
	for _, want := range []string{
		"  sapu / CLI / Open source\n", "  Disk\n", "  Free ", " GB\n",
		"  SAFE ", "\n  1 item / recreated by applications\n",
		"  MODERATE ", "\n  1 item / needs review\n",
		"  RISKY ", "\n  0 items / not selected\n",
		"  Save a plan:\n  sapu clean --plan plan.json\n",
		"  Review candidates:\n  sapu clean\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("narrow summary lacks %q:\n%s", want, out)
		}
	}
	// The sizes end in one right-aligned column.
	var ends []int
	for _, line := range strings.Split(out, "\n") {
		for _, word := range []string{"  SAFE ", "  MODERATE ", "  RISKY ", "  Free "} {
			if strings.HasPrefix(line, word) {
				ends = append(ends, utf8.RuneCountInString(line))
			}
		}
	}
	if len(ends) != 4 || ends[0] != ends[1] || ends[1] != ends[2] || ends[2] != ends[3] {
		t.Errorf("sizes are not right-aligned: line widths %v\n%s", ends, out)
	}
	t.Logf("48 columns:\n%s", out)
}

func TestQuarantinePartsAndDiskBar(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.Local)
	runs := []held{
		{ID: "b", Bytes: 6_300_000_000, ExpiresAfter: now.Add(6 * 24 * time.Hour)},
		{ID: "a", Bytes: 3_100_000_000, ExpiresAfter: now.Add(4 * 24 * time.Hour)},
	}
	if value, hint := quarantineParts(runs, now); value != "2 runs / 9.4 GB" || hint != "First can expire after Thu 12 Nov" {
		t.Errorf("quarantineParts = %q, %q", value, hint)
	}
	runs[1].ExpiresAfter = now.Add(-time.Hour)
	if _, hint := quarantineParts(runs, now); hint != "The oldest can expire now" {
		t.Errorf("an overdue run: %q", hint)
	}
	if value, _ := quarantineParts(nil, now); value != "Empty" {
		t.Errorf("no runs: %q", value)
	}
	for _, tc := range []struct {
		used, total int64
		want        string
	}{
		{0, 100, ".........."},
		{50, 100, "#####....."},
		{100, 100, "##########"},
		{5, 0, ".........."},
	} {
		if got := diskBar(tc.used, tc.total, 10); got != tc.want {
			t.Errorf("diskBar(%d, %d) = %q, want %q", tc.used, tc.total, got, tc.want)
		}
	}
}
