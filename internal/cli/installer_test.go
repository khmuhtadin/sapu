package cli

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/finders"
	"github.com/khmuhtadin/sapu/internal/plan"
)

// finderMounted fakes `hdiutil info`: the disk images at paths are mounted.
func finderMounted(t *testing.T, paths ...string) {
	t.Helper()
	old := finders.MountedImages
	finders.MountedImages = func(context.Context) ([]string, error) { return paths, nil }
	t.Cleanup(func() { finders.MountedImages = old })
}

// installerHome has old installers in Downloads and Desktop, a mounted
// image, a new installer, and old installers elsewhere.
func installerHome(t *testing.T) (e env.Env, mounted string) {
	t.Helper()
	e = env.New(t.TempDir())
	old := finderLongAgo
	finderPut(t, e, "Downloads/Xcode.xip", 40_000, old)
	finderPut(t, e, "Downloads/App.dmg", 30_000, old)
	finderPut(t, e, "Downloads/tool.pkg", 20_000, old)
	finderPut(t, e, "Desktop/linux.iso", 10_000, old)
	mounted = finderPut(t, e, "Downloads/Mounted.dmg", 50_000, old)
	finderPut(t, e, "Downloads/Fresh.dmg", 30_000, time.Now())
	finderPut(t, e, "Downloads/notes.txt", 30_000, old)
	finderPut(t, e, "Documents/Elsewhere.dmg", 30_000, old)
	finderPut(t, e, "Downloads/unzipped/Setup.pkg", 30_000, old) // subfolders are not entered
	finderPut(t, e, "Desktop/app/dist/Build.dmg", 30_000, old)   // a build result, not a download
	finderMounted(t, mounted)
	return e, mounted
}

func TestInstallerPlanSelectsOldInstallersAndSkipsMounted(t *testing.T) {
	e, mounted := installerHome(t)
	before := snapshot(t, e)
	file := filepath.Join(t.TempDir(), "plan.json")

	out, errOut, code := sapu(t, e, "", "installer", "--plan", file)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)

	p := readPlanFile(t, file)
	want := []string{
		filepath.Join(e.Home, "Desktop/linux.iso"),
		filepath.Join(e.Home, "Downloads/App.dmg"),
		filepath.Join(e.Home, "Downloads/Xcode.xip"),
		filepath.Join(e.Home, "Downloads/tool.pkg"),
	}
	if got := selectedPaths(p); !slices.Equal(got, want) || p.Kind != plan.KindInstaller {
		t.Errorf("plan kind %q selects\n%v\nwant\n%v", p.Kind, got, want)
	}
	for _, it := range p.Items {
		if it.Risk != "moderate" || it.Source != "installer" || !it.Regenerates || !strings.Contains(it.Explain, "download it again") {
			t.Errorf("item %+v", it)
		}
	}
	if len(p.Skipped) != 1 || p.Skipped[0].Path != mounted || p.Skipped[0].Reason != plan.ReasonInUse {
		t.Errorf("skipped %+v, want the mounted image", p.Skipped)
	}
}

func TestInstallerMovesOldInstallersAndUndoRestores(t *testing.T) {
	e, mounted := installerHome(t)
	before := snapshot(t, e)

	out, errOut, code := sapu(t, e, "y\n", "installer")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	t.Logf("sapu installer, answering y\n%s", out)
	for _, want := range []string{"installer · 4 items", "Moved 4 items", "~/Downloads/Xcode.xip", "Mounted.dmg", "mounted disk image"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, gone := range []string{"Downloads/Xcode.xip", "Downloads/App.dmg", "Downloads/tool.pkg", "Desktop/linux.iso"} {
		if exists(filepath.Join(e.Home, gone)) {
			t.Errorf("%s was not moved", gone)
		}
	}
	for _, kept := range []string{"Downloads/Fresh.dmg", "Downloads/notes.txt", "Documents/Elsewhere.dmg", "Downloads/unzipped/Setup.pkg", "Desktop/app/dist/Build.dmg"} {
		if !exists(filepath.Join(e.Home, kept)) {
			t.Errorf("%s is gone", kept)
		}
	}
	if !exists(mounted) {
		t.Error("the mounted disk image was moved")
	}

	if _, _, code := sapu(t, e, "", "undo", "--last"); code != 0 {
		t.Fatal("undo failed")
	}
	sameSnapshot(t, before, snapshot(t, e))
}

func TestInstallerFlags(t *testing.T) {
	e, _ := installerHome(t)
	before := snapshot(t, e)

	// DIR replaces the default folders
	out, _, code := sapu(t, e, "", "installer", filepath.Join(e.Home, "Documents"), "--yes")
	if code != 0 || !strings.Contains(out, "Moved 1 item") || exists(filepath.Join(e.Home, "Documents/Elsewhere.dmg")) || !exists(filepath.Join(e.Home, "Downloads/App.dmg")) {
		t.Errorf("DIR: exit %d\n%s", code, out)
	}
	if _, _, code := sapu(t, e, "", "undo", "--last"); code != 0 {
		t.Fatal("undo failed")
	}
	sameSnapshot(t, before, snapshot(t, e))

	// nothing is old enough for 5y
	out, _, code = sapu(t, e, "", "installer", "--older-than", "5y", "--yes")
	if code != 0 || !strings.Contains(out, "Nothing") {
		t.Errorf("--older-than 5y: exit %d\n%s", code, out)
	}

	for _, args := range [][]string{
		{"--older-than", "soon"},
		{filepath.Join(e.Home, "no-such-folder")},
		{filepath.Join(e.Home, "Downloads/notes.txt")},
		{"--json"},
	} {
		if out, errOut, code := sapu(t, e, "", append([]string{"installer"}, args...)...); code != 2 {
			t.Errorf("%v: exit %d, want 2\n%s\n%s", args, code, out, errOut)
		}
	}
	sameSnapshot(t, before, snapshot(t, e))
}

func TestInstallerAsksFirst(t *testing.T) {
	e, _ := installerHome(t)
	before := snapshot(t, e)
	if _, _, code := sapu(t, e, "n\n", "installer"); code != 2 {
		t.Errorf("answer n: exit %d, want 2", code)
	}
	if _, _, code := sapu(t, e, "", "installer"); code != 2 {
		t.Errorf("no answer: exit %d, want 2", code)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
}

func TestInstallerWhenNoFolderExists(t *testing.T) {
	finderMounted(t)
	e := env.New(t.TempDir())
	out, _, code := sapu(t, e, "", "installer", "--yes")
	if code != 0 || !strings.Contains(out, "Nothing") {
		t.Errorf("exit %d\n%s", code, out)
	}
	noState(t, e)
}

func TestInstallerJSON(t *testing.T) {
	e, _ := installerHome(t)
	out, errOut, code := sapu(t, e, "", "installer", "--yes", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "installer", out)
	var d runData
	decodeData(t, out, &d)
	if d.Summary.SelectedItems != 4 || d.Summary.Skipped != 1 || d.Result == nil || len(d.Result.Moved) != 4 {
		t.Errorf("data: %+v", d)
	}
}
