package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
)

var finderLongAgo = time.Now().AddDate(-2, 0, 0)

// finderPut writes size bytes at rel in the home folder and sets its times.
func finderPut(t *testing.T, e env.Env, rel string, size int, when time.Time) string {
	t.Helper()
	path := filepath.Join(e.Home, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), size), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
	return path
}

// finderDenied makes dir unreadable until the test ends.
func finderDenied(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root can read everything")
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

// largeHome has three big old files (film 300 KB, zip 250 KB, trip 200 KB)
// and files that must not be listed.
func largeHome(t *testing.T) env.Env {
	t.Helper()
	e := env.New(t.TempDir())
	finderPut(t, e, "Movies/film.mov", 300_000, finderLongAgo)
	finderPut(t, e, "Documents/backup.zip", 250_000, finderLongAgo)
	finderPut(t, e, "Movies/trip.mov", 200_000, finderLongAgo)
	finderPut(t, e, "Movies/new.mov", 400_000, time.Now())
	finderPut(t, e, "Library/Caches/big.bin", 400_000, finderLongAgo)
	finderPut(t, e, "Documents/small.txt", 100, finderLongAgo)
	return e
}

func TestLargeListsAndChangesNothing(t *testing.T) {
	e := largeHome(t)
	before := snapshot(t, e)

	out, errOut, code := sapu(t, e, "", "large", "--min-size", "100KB")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	t.Logf("sapu large\n%s", out)
	checkHuman(t, out)
	last := -1
	for i, name := range []string{"film.mov", "backup.zip", "trip.mov"} { // biggest first, numbered from 1
		at := strings.Index(out, name)
		if at < 0 || at < last {
			t.Errorf("%s is missing or out of order in:\n%s", name, out)
		}
		last = at
		if !strings.Contains(out, "\n"+string(rune('1'+i))+" ") && !strings.Contains(out, "\n "+string(rune('1'+i))+" ") {
			t.Errorf("no row numbered %d in:\n%s", i+1, out)
		}
	}
	for _, name := range []string{"new.mov", "big.bin", "small.txt"} {
		if strings.Contains(out, name) {
			t.Errorf("%s must not be listed:\n%s", name, out)
		}
	}
	if !strings.Contains(out, "Pick with --pick 1,3, or mark them in sapu analyze") || !strings.Contains(out, "Nothing was changed") {
		t.Errorf("no hint:\n%s", out)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
}

func TestLargePickMovesOnlyThosePicked(t *testing.T) {
	e := largeHome(t)
	before := snapshot(t, e)

	out, errOut, code := sapu(t, e, "y\n", "large", "--min-size", "100KB", "--pick", "1,3")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	t.Logf("sapu large --pick 1,3, answering y\n%s", out)
	moved := out[:strings.Index(out, "Moved")]
	for _, name := range []string{"film.mov", "trip.mov"} {
		if !strings.Contains(moved, name) {
			t.Errorf("the plan does not show %s:\n%s", name, out)
		}
	}
	if strings.Contains(moved, "backup.zip") {
		t.Errorf("the plan shows an item that was not picked:\n%s", out)
	}
	if !strings.Contains(out, "Moved 2 items") {
		t.Errorf("output lacks the result:\n%s", out)
	}
	for _, gone := range []string{"Movies/film.mov", "Movies/trip.mov"} {
		if exists(filepath.Join(e.Home, gone)) {
			t.Errorf("%s was not moved", gone)
		}
	}
	for _, kept := range []string{"Documents/backup.zip", "Movies/new.mov", "Library/Caches/big.bin"} {
		if !exists(filepath.Join(e.Home, kept)) {
			t.Errorf("%s is gone", kept)
		}
	}

	if _, _, code := sapu(t, e, "", "undo", "--last"); code != 0 {
		t.Fatal("undo failed")
	}
	sameSnapshot(t, before, snapshot(t, e))
}

func TestLargePickRangeAndYes(t *testing.T) {
	e := largeHome(t)
	out, _, code := sapu(t, e, "", "large", "--min-size", "100KB", "--pick", "2-3", "--yes")
	if code != 0 || !strings.Contains(out, "Moved 2 items") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !exists(filepath.Join(e.Home, "Movies/film.mov")) || exists(filepath.Join(e.Home, "Documents/backup.zip")) || exists(filepath.Join(e.Home, "Movies/trip.mov")) {
		t.Errorf("--pick 2-3 moved the wrong files")
	}
}

func TestLargeBadPicksAreUsageErrors(t *testing.T) {
	e := largeHome(t)
	before := snapshot(t, e)
	for _, pick := range []string{"0", "x", "3-1", "", "1,,2", "-2", "4", "1-9"} {
		out, errOut, code := sapu(t, e, "y\n", "large", "--min-size", "100KB", "--pick", pick, "--yes")
		if code != 2 {
			t.Errorf("--pick %q: exit %d, want 2\n%s\n%s", pick, code, out, errOut)
		}
		if !strings.Contains(errOut, "--pick") {
			t.Errorf("--pick %q: the error does not name the flag: %q", pick, errOut)
		}
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
}

func TestLargePickAsksFirst(t *testing.T) {
	e := largeHome(t)
	before := snapshot(t, e)
	if _, _, code := sapu(t, e, "n\n", "large", "--min-size", "100KB", "--pick", "1"); code != 2 {
		t.Errorf("answer n: exit %d, want 2", code)
	}
	if _, _, code := sapu(t, e, "", "large", "--min-size", "100KB", "--pick", "1"); code != 2 {
		t.Errorf("no answer: exit %d, want 2", code)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
}

func TestLargePlanNeedsPick(t *testing.T) {
	e := largeHome(t)
	file := filepath.Join(t.TempDir(), "plan.json")
	if _, _, code := sapu(t, e, "", "large", "--min-size", "100KB", "--plan", file); code != 2 {
		t.Errorf("--plan without --pick: exit %d, want 2", code)
	}
	if exists(file) {
		t.Error("a plan was written")
	}

	before := snapshot(t, e)
	out, errOut, code := sapu(t, e, "", "large", "--min-size", "100KB", "--pick", "2", "--plan", file)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	p := readPlanFile(t, file)
	if got := selectedPaths(p); len(got) != 1 || got[0] != filepath.Join(e.Home, "Documents/backup.zip") || p.Kind != plan.KindLarge {
		t.Errorf("plan kind %q selects %v", p.Kind, got)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
}

func TestLargeFilters(t *testing.T) {
	e := largeHome(t)
	out, _, code := sapu(t, e, "", "large", "--min-size", "100KB", "--type", "archive")
	if code != 0 || !strings.Contains(out, "backup.zip") || strings.Contains(out, ".mov") {
		t.Errorf("--type archive: exit %d\n%s", code, out)
	}
	out, _, code = sapu(t, e, "", "large", "--min-size", "100KB", "--older-than", "5y")
	if code != 0 || !strings.Contains(out, "No file of at least 100.0 KB") {
		t.Errorf("--older-than 5y: exit %d\n%s", code, out)
	}
	out, _, code = sapu(t, e, "", "large", "--min-size", "280KB")
	if code != 0 || !strings.Contains(out, "film.mov") || strings.Contains(out, "backup.zip") {
		t.Errorf("--min-size 280KB: exit %d\n%s", code, out)
	}
	// a DIR argument limits the search
	out, _, code = sapu(t, e, "", "large", "--min-size", "100KB", filepath.Join(e.Home, "Documents"))
	if code != 0 || !strings.Contains(out, "backup.zip") || strings.Contains(out, ".mov") {
		t.Errorf("DIR: exit %d\n%s", code, out)
	}
}

func TestLargeUsageErrors(t *testing.T) {
	e := largeHome(t)
	file := finderPut(t, e, "Documents/a-file.txt", 10, finderLongAgo)
	for _, args := range [][]string{
		{"--min-size", "big"},
		{"--older-than", "soon"},
		{"--type", "text"},
		{filepath.Join(e.Home, "no-such-folder")},
		{file},
		{"--permanent", "--plan", filepath.Join(t.TempDir(), "p.json"), "--pick", "1"},
	} {
		if out, errOut, code := sapu(t, e, "", append([]string{"large"}, args...)...); code != 2 {
			t.Errorf("%v: exit %d, want 2\n%s\n%s", args, code, out, errOut)
		}
	}
	// a folder Sapu never enters is refused with a reason
	out, errOut, code := sapu(t, e, "", "large", filepath.Join(e.Home, "Library"))
	if code == 0 || !strings.Contains(errOut, "does not look inside") {
		t.Errorf("~/Library: exit %d\n%s\n%s", code, out, errOut)
	}
}

func TestLargeWhitelistedFileIsNotListedOrPicked(t *testing.T) {
	e := largeHome(t)
	if _, _, code := sapu(t, e, "", "whitelist", "add", filepath.Join(e.Home, "Movies/film.mov")); code != 0 {
		t.Fatal("whitelist add failed")
	}
	out, _, code := sapu(t, e, "", "large", "--min-size", "100KB")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "film.mov") && !strings.HasPrefix(line, "    ") {
			t.Errorf("a whitelisted file is in the list: %q", line)
		}
	}
	if !strings.Contains(out, "1 whitelisted") {
		t.Errorf("the output does not say what was left out:\n%s", out)
	}
	// numbers follow the list that was shown: 1 is now the zip
	if _, _, code := sapu(t, e, "", "large", "--min-size", "100KB", "--pick", "1", "--yes"); code != 0 {
		t.Fatal("pick failed")
	}
	if !exists(filepath.Join(e.Home, "Movies/film.mov")) || exists(filepath.Join(e.Home, "Documents/backup.zip")) {
		t.Error("--pick 1 did not move the first listed file")
	}
}

func TestLargeJSON(t *testing.T) {
	e := largeHome(t)
	out, errOut, code := sapu(t, e, "", "large", "--min-size", "100KB", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "large", out)
	var d struct {
		Items []struct {
			Number int
			Path   string
			Type   string
		}
	}
	decodeData(t, out, &d)
	if len(d.Items) != 3 || d.Items[0].Number != 1 || d.Items[0].Path != filepath.Join(e.Home, "Movies/film.mov") || d.Items[0].Type != "mov" {
		t.Errorf("items: %+v", d.Items)
	}
	noState(t, e)

	out, errOut, code = sapu(t, e, "", "large", "--min-size", "100KB", "--pick", "1", "--yes", "--json")
	if code != 0 {
		t.Fatalf("pick: exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "large", out)

	// --json cannot ask a question, so picking needs --yes
	if _, _, code := sapu(t, e, "", "large", "--min-size", "100KB", "--pick", "1", "--json"); code != 2 {
		t.Errorf("--pick --json without --yes: exit %d, want 2", code)
	}
	if out, _, code := sapu(t, e, "", "large", "--min-size", "100KB", "--type", "text", "--json"); code != 2 {
		t.Errorf("exit %d, want 2", code)
	} else {
		validate(t, "large", out)
	}
}

// The description of each finder command fits an 80-column terminal.
func TestFinderHelpFits80Columns(t *testing.T) {
	e := env.New(t.TempDir())
	for _, name := range []string{"large", "backups", "messages", "trash", "installer"} {
		out, _, code := sapu(t, e, "", name, "--help")
		if code != 0 {
			t.Fatalf("%s --help: exit %d", name, code)
		}
		text, _, _ := strings.Cut(out, "\nFlags:") // flag lines are cobra's own
		for _, line := range strings.Split(text, "\n") {
			if n := utf8.RuneCountInString(line); n > 80 {
				t.Errorf("%s --help: line is %d columns: %q", name, n, line)
			}
		}
	}
}
