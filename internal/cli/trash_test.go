package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/finders"
	"github.com/khmuhtadin/sapu/internal/plan"
)

// finderVolumes points the finder at an empty folder instead of /Volumes.
func finderVolumes(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := finders.Volumes
	finders.Volumes = dir
	t.Cleanup(func() { finders.Volumes = old })
	return dir
}

// trashHome has a file, a hidden file, a folder and a file that will be whitelisted in ~/.Trash.
func trashHome(t *testing.T) env.Env {
	t.Helper()
	finderVolumes(t)
	e := env.New(t.TempDir())
	now := time.Now()
	finderPut(t, e, ".Trash/report.pdf", 3000, now)
	finderPut(t, e, ".Trash/.hidden-settings", 100, now)
	finderPut(t, e, ".Trash/old project/src/main.go", 5000, now)
	finderPut(t, e, ".Trash/keep.txt", 2000, now)
	finderPut(t, e, "Documents/not-trash.txt", 2000, now)
	return e
}

func TestTrashPlanSelectsEverythingAndChangesNothing(t *testing.T) {
	e := trashHome(t)
	before := snapshot(t, e)
	file := filepath.Join(t.TempDir(), "plan.json")

	out, errOut, code := sapu(t, e, "", "trash", "--plan", file)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)

	p := readPlanFile(t, file)
	want := []string{".hidden-settings", "keep.txt", "old project", "report.pdf"}
	var got []string
	for _, path := range selectedPaths(p) {
		got = append(got, strings.TrimPrefix(path, filepath.Join(e.Home, ".Trash")+"/"))
	}
	if !slices.Equal(got, want) || p.Kind != plan.KindTrash {
		t.Errorf("plan kind %q selects %v, want %v", p.Kind, got, want)
	}
	for _, it := range p.Items {
		if it.Risk != "moderate" || it.Source != "trash" || !it.Selected {
			t.Errorf("item %+v", it)
		}
	}
}

func TestTrashMovesEverythingAndUndoRestoresIt(t *testing.T) {
	e := trashHome(t)
	before := snapshot(t, e)

	out, errOut, code := sapu(t, e, "y\n", "trash")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	t.Logf("sapu trash, answering y\n%s", out)
	for _, want := range []string{"trash · 4 items", "~/.Trash/.hidden-settings", "Moved 4 items", "You already put this in the Trash"} {
		if !strings.Contains(finderFlat(out), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	entries, err := os.ReadDir(filepath.Join(e.Home, ".Trash"))
	if err != nil || len(entries) != 0 {
		t.Errorf("the Trash still holds %v (%v)", entries, err)
	}
	if !exists(filepath.Join(e.Home, "Documents/not-trash.txt")) {
		t.Error("a file outside the Trash was touched")
	}

	if _, _, code := sapu(t, e, "", "undo", "--last"); code != 0 {
		t.Fatal("undo failed")
	}
	sameSnapshot(t, before, snapshot(t, e))
}

// A whitelisted item inside the Trash stays there (Mole #163).
func TestTrashKeepsWhitelistedItem(t *testing.T) {
	e := trashHome(t)
	before := snapshot(t, e)
	keep := filepath.Join(e.Home, ".Trash/keep.txt")
	if _, _, code := sapu(t, e, "", "whitelist", "add", keep); code != 0 {
		t.Fatal("whitelist add failed")
	}

	out, errOut, code := sapu(t, e, "", "trash", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	t.Logf("sapu trash --yes\n%s", out)
	if !strings.Contains(out, "Moved 3 items") || !strings.Contains(out, "1 whitelisted") {
		t.Errorf("output:\n%s", out)
	}
	left, err := os.ReadDir(filepath.Join(e.Home, ".Trash"))
	if err != nil || len(left) != 1 || left[0].Name() != "keep.txt" {
		t.Errorf("the Trash holds %v (%v), want only keep.txt", left, err)
	}
	if data, err := os.ReadFile(keep); err != nil || len(data) != 2000 {
		t.Errorf("the whitelisted item changed: %d bytes, %v", len(data), err)
	}

	if _, _, code := sapu(t, e, "", "undo", "--last"); code != 0 {
		t.Fatal("undo failed")
	}
	sameSnapshot(t, before, snapshot(t, e))
}

func TestTrashAsksFirst(t *testing.T) {
	e := trashHome(t)
	before := snapshot(t, e)
	if _, _, code := sapu(t, e, "n\n", "trash"); code != 2 {
		t.Errorf("answer n: exit %d, want 2", code)
	}
	if _, _, code := sapu(t, e, "", "trash"); code != 2 {
		t.Errorf("no answer: exit %d, want 2", code)
	}
	if _, _, code := sapu(t, e, "", "trash", "extra"); code != 2 {
		t.Errorf("extra argument: exit %d, want 2", code)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
}

func TestTrashOnMountedVolumes(t *testing.T) {
	e := trashHome(t)
	volumes := finderVolumes(t)
	uid := strconv.Itoa(os.Getuid())
	onDisk := filepath.Join(volumes, "Photos Disk", ".Trashes", uid, "film.mov")
	if err := os.MkdirAll(filepath.Dir(onDisk), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(onDisk, []byte("movie"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/", filepath.Join(volumes, "Macintosh HD")); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(t.TempDir(), "plan.json")
	if out, errOut, code := sapu(t, e, "", "trash", "--plan", file); code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	got := selectedPaths(readPlanFile(t, file))
	if len(got) != 5 || !slices.Contains(got, onDisk) {
		t.Errorf("plan selects %v, want the four items of ~/.Trash and %s", got, onDisk)
	}
	if !exists(onDisk) {
		t.Error("--plan changed the volume")
	}
}

func TestTrashWithoutFullDiskAccess(t *testing.T) {
	e := trashHome(t)
	finderDenied(t, filepath.Join(e.Home, ".Trash"))
	out, errOut, code := sapu(t, e, "", "trash", "--yes")
	if code != 1 || out != "" {
		t.Errorf("exit %d, stdout %q", code, out)
	}
	for _, want := range []string{"Full Disk Access", "sapu doctor", "~/.Trash"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q: %q", want, errOut)
		}
	}
	noState(t, e)
}

func TestTrashEmpty(t *testing.T) {
	finderVolumes(t)
	e := env.New(t.TempDir())
	out, _, code := sapu(t, e, "", "trash", "--yes")
	if code != 0 || !strings.Contains(out, "Nothing") {
		t.Errorf("exit %d\n%s", code, out)
	}
	noState(t, e)
}

func TestTrashJSON(t *testing.T) {
	e := trashHome(t)
	if _, _, code := sapu(t, e, "", "trash", "--json"); code != 2 {
		t.Errorf("--json without --yes: exit %d, want 2", code)
	}
	out, errOut, code := sapu(t, e, "", "trash", "--yes", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "trash", out)
	var d runData
	decodeData(t, out, &d)
	if d.Summary.SelectedItems != 4 || d.Result == nil || len(d.Result.Moved) != 4 {
		t.Errorf("data: %+v", d)
	}

	file := filepath.Join(t.TempDir(), "plan.json")
	if out, _, code := sapu(t, e, "", "trash", "--plan", file, "--json"); code != 0 {
		t.Fatalf("--plan --json: exit %d", code)
	} else {
		validate(t, "trash", out)
	}
}
