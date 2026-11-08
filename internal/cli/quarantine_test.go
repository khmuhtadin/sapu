package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/fsop"
)

func TestQuarantineList(t *testing.T) {
	e := newHome(t)
	out, _, code := sapu(t, e, "", "quarantine", "list")
	if code != 0 || !strings.Contains(out, "quarantine is empty") {
		t.Errorf("exit %d\n%s", code, out)
	}
	out, _, _ = sapu(t, e, "", "quarantine", "list", "--json")
	validate(t, "quarantine-list", out)

	run, _, _ := sapu(t, e, "", "clean", "--yes")
	id := runIDOf(t, run)

	out, _, code = sapu(t, e, "", "quarantine", "list")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	freed := time.Now().Add(fsop.DefaultMaxAge).Format("Mon 2 Jan")
	for _, want := range []string{"Run", "Size", "Age", "Expires after", id, "just now", freed, "16.4 KB"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}

	out, _, _ = sapu(t, e, "", "quarantine", "list", "--json")
	validate(t, "quarantine-list", out)
	var d quarantineListData
	decodeData(t, out, &d)
	if len(d.Runs) != 1 || d.Runs[0].ID != id || d.TotalBytes != d.Runs[0].Bytes || d.TotalBytes == 0 {
		t.Errorf("data %+v", d)
	}
}

func TestQuarantineEmpty(t *testing.T) {
	e := newHome(t)
	run, _, _ := sapu(t, e, "", "clean", "--yes")
	id := runIDOf(t, run)

	for _, tc := range []struct {
		name  string
		stdin string
		args  []string
		code  int
	}{
		{"no run and no --all", "", []string{"quarantine", "empty"}, 2},
		{"run and --all", "", []string{"quarantine", "empty", id, "--all"}, 2},
		{"answer no", "n\n", []string{"quarantine", "empty", id}, 2},
		{"empty stdin", "", []string{"quarantine", "empty", "--all"}, 2},
		{"--json needs --yes", "", []string{"quarantine", "empty", id, "--json"}, 2},
		{"unknown run", "", []string{"quarantine", "empty", "19990101-000000-abcd", "--yes"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, code := sapu(t, e, tc.stdin, tc.args...); code != tc.code {
				t.Errorf("exit %d, want %d", code, tc.code)
			}
			list, _, _ := sapu(t, e, "", "quarantine", "list")
			if !strings.Contains(list, id) {
				t.Errorf("the run is gone from quarantine:\n%s", list)
			}
		})
	}

	// The question says plainly that this deletes for good.
	_, errOut, _ := sapu(t, e, "n\n", "quarantine", "empty", id)
	if !strings.Contains(errOut, "for good") || !strings.Contains(errOut, "no longer be undone") {
		t.Errorf("question: %q", errOut)
	}

	out, _, code := sapu(t, e, "y\n", "quarantine", "empty", id)
	if code != 0 || !strings.Contains(out, "Deleted run "+id+" for good") || !strings.Contains(out, "Deleted 16.4 KB") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	list, _, _ := sapu(t, e, "", "quarantine", "list")
	if !strings.Contains(list, "empty") {
		t.Errorf("quarantine is not empty:\n%s", list)
	}
	if _, _, code := sapu(t, e, "", "undo", id); code == 0 {
		t.Error("undo worked after the quarantine was emptied")
	}
	if exists(filepath.Join(e.Home, "Library/Caches/node-gyp")) {
		t.Error("emptying the quarantine put something back")
	}
	assertDecoys(t, e)
}

func TestQuarantineEmptyAllJSON(t *testing.T) {
	e := newHome(t)
	sapu(t, e, "", "clean", "--yes")
	// A second run, so --all has two to do.
	writeFile(t, filepath.Join(e.Home, "Library/Caches/typescript/types.json"), "again")
	sapu(t, e, "", "clean", "--yes")

	out, _, code := sapu(t, e, "", "quarantine", "empty", "--all", "--yes", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	validate(t, "quarantine-empty", out)
	var d quarantineEmptyData
	decodeData(t, out, &d)
	if len(d.Runs) != 2 || d.Freed == 0 {
		t.Errorf("data %+v", d)
	}

	// Nothing left: still one envelope.
	out, _, code = sapu(t, e, "", "quarantine", "empty", "--all", "--yes", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	validate(t, "quarantine-empty", out)
}

func TestQuarantineExpire(t *testing.T) {
	e := newHome(t)
	out, _, code := sapu(t, e, "", "quarantine", "expire")
	if code != 0 || !strings.Contains(out, "Nothing has expired") {
		t.Errorf("exit %d\n%s", code, out)
	}

	run, _, _ := sapu(t, e, "", "clean", "--yes")
	id := runIDOf(t, run)
	out, _, code = sapu(t, e, "", "quarantine", "expire", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	validate(t, "quarantine-expire", out)
	var d quarantineExpireData
	decodeData(t, out, &d)
	if d.Freed != 0 {
		t.Errorf("a new run expired: freed %d", d.Freed)
	}
	if list, _, _ := sapu(t, e, "", "quarantine", "list"); !strings.Contains(list, id) {
		t.Fatalf("the new run is gone:\n%s", list)
	}

	ageRun(t, e, id, 8*24*time.Hour)
	out, _, code = sapu(t, e, "", "quarantine", "expire")
	if code != 0 || !strings.Contains(out, "Deleted 16.4 KB") {
		t.Errorf("exit %d\n%s", code, out)
	}
	if list, _, _ := sapu(t, e, "", "quarantine", "list"); !strings.Contains(list, "empty") {
		t.Errorf("the old run is still in quarantine:\n%s", list)
	}
	assertDecoys(t, e)

	if _, _, code := sapu(t, e, "", "quarantine", "expire", "extra"); code != 2 {
		t.Errorf("extra argument: exit %d, want 2", code)
	}
}

// The menu's delete empties each run it is given, like `sapu quarantine empty`.
func TestMenuDelete(t *testing.T) {
	e := newHome(t)
	out, _, _ := sapu(t, e, "", "clean", "--yes")
	first := runIDOf(t, out)
	for rel, data := range junk {
		writeFile(t, filepath.Join(e.Home, rel), data)
	}
	out, _, _ = sapu(t, e, "", "clean", "--yes")
	second := runIDOf(t, out)

	text, err := menuDelete([]string{first})
	if err != nil || !strings.Contains(text, "Deleted run "+first+" for good") {
		t.Fatalf("%q %v", text, err)
	}
	list, _, _ := sapu(t, e, "", "quarantine", "list")
	if strings.Contains(list, first) || !strings.Contains(list, second) {
		t.Fatalf("only the first run should be gone:\n%s", list)
	}

	text, err = menuDelete([]string{second})
	if err != nil || !strings.Contains(text, "Freed") {
		t.Fatalf("%q %v", text, err)
	}
	if list, _, _ := sapu(t, e, "", "quarantine", "list"); !strings.Contains(list, "quarantine is empty") {
		t.Fatalf("the quarantine should be empty:\n%s", list)
	}
}
