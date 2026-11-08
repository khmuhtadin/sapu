//go:build vm

package fsop

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Invariant 1 on a real second volume: a small disk image attached inside
// the test home. Items on it go to <mount>/.sapu-quarantine/<uid>/<run_id>/,
// still by rename, and come back identical.
func TestSecondVolumeQuarantine(t *testing.T) {
	e := newHome(t)
	img := filepath.Join(t.TempDir(), "sapu-test.dmg")
	mnt := filepath.Join(e.Home, "ext")
	run := func(name string, args ...string) {
		t.Helper()
		out, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
		}
	}
	run("hdiutil", "create", "-size", "32m", "-fs", "APFS", "-volname", "SapuTest", img)
	must(t, os.MkdirAll(mnt, 0o755))
	run("hdiutil", "attach", "-nobrowse", "-mountpoint", mnt, img)
	t.Cleanup(func() { _ = exec.Command("hdiutil", "detach", "-force", mnt).Run() })

	data := filepath.Join(mnt, "data")
	buildTree(t, data, 10, 200, 9)
	writeFile(t, filepath.Join(mnt, "keep.txt"), "keep")
	before := snapshot(t, data)
	items := itemsFor(t, data)

	id := quarantineAll(t, e, items, now)
	vdir := filepath.Join(mnt, ".sapu-quarantine", strconv.Itoa(os.Getuid()), id)
	moved, err := os.ReadDir(vdir)
	must(t, err)
	if len(moved) != len(items) {
		t.Fatalf("volume quarantine holds %d items, want %d", len(moved), len(items))
	}
	if exists(t, filepath.Join(e.QuarantineDir(), id)) {
		t.Fatal("items went to the home quarantine, not the volume's")
	}
	_, err = Restore(e, id)
	must(t, err)
	requireSame(t, before, snapshot(t, data))

	id = quarantineAll(t, e, itemsFor(t, data), now.Add(time.Minute))
	if _, err := Empty(e, id); err != nil {
		t.Fatal(err)
	}
	if exists(t, filepath.Join(mnt, ".sapu-quarantine", strconv.Itoa(os.Getuid()), id)) {
		t.Fatal("volume quarantine not emptied")
	}
	if readFile(t, filepath.Join(mnt, "keep.txt")) != "keep" {
		t.Fatal("a file outside quarantine was deleted")
	}
}
