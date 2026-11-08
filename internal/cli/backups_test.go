package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/finders"
)

// finderBackup writes a backup folder with an Info.plist made from the fixture.
func finderBackup(t *testing.T, e env.Env, id, device, udid string, when time.Time, size int) {
	t.Helper()
	dir := filepath.Join(e.Home, finders.BackupsDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	tmpl, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "finders", "backup-Info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	plist := strings.NewReplacer("__DEVICE__", device, "__UDID__", udid, "__DATE__", when.UTC().Format(time.RFC3339)).Replace(string(tmpl))
	if err := os.WriteFile(filepath.Join(dir, "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Manifest.db"), bytes.Repeat([]byte("x"), size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// backupsHome has two backups of one iPhone (the second is the newest) and one of an iPad.
func backupsHome(t *testing.T) env.Env {
	t.Helper()
	e := env.New(t.TempDir())
	finderBackup(t, e, "AAA-old", "Ana's iPhone", "AAA", time.Date(2025, 1, 2, 9, 0, 0, 0, time.UTC), 20_000)
	finderBackup(t, e, "AAA", "Ana's iPhone", "AAA", time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC), 60_000)
	finderBackup(t, e, "BBB", "Budi iPad", "BBB", time.Date(2026, 3, 3, 8, 0, 0, 0, time.UTC), 40_000)
	return e
}

// finderFlat joins wrapped lines, so a test can look for a sentence.
func finderFlat(text string) string { return strings.Join(strings.Fields(text), " ") }

func TestBackupsListsAndChangesNothing(t *testing.T) {
	e := backupsHome(t)
	before := snapshot(t, e)

	out, errOut, code := sapu(t, e, "", "backups")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	t.Logf("sapu backups\n%s", out)
	checkHuman(t, out)
	// newest first: the iPhone of May 2026, the iPad, the old iPhone backup
	ana, budi, old := strings.Index(out, "2026-05-01"), strings.Index(out, "2026-03-03"), strings.Index(out, "2025-01-02")
	if ana < 0 || budi < ana || old < budi {
		t.Errorf("rows are missing or out of order:\n%s", out)
	}
	for _, want := range []string{"Ana's iPhone", "Budi iPad", "iPhone14,2", "newest", "may be the only copy of that iPhone", "Pick with --pick 1,3"} {
		if !strings.Contains(finderFlat(out), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "newest") != 3 { // two rows, and the note
		t.Errorf("want 'newest' on the two newest backups and in the note:\n%s", out)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
}

// The protection list holds MobileSync back for every command except
// sapu backups, so this also proves the pick reaches the backup.
func TestBackupsPickMovesOneBackupAndUndoRestores(t *testing.T) {
	e := backupsHome(t)
	before := snapshot(t, e)
	dir := filepath.Join(e.Home, finders.BackupsDir)

	out, errOut, code := sapu(t, e, "y\n", "backups", "--pick", "3")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	t.Logf("sapu backups --pick 3, answering y\n%s", out)
	if !strings.Contains(out, "Moved 1 item") || !strings.Contains(finderFlat(out), "A newer backup of that device exists") {
		t.Errorf("output:\n%s", out)
	}
	if exists(filepath.Join(dir, "AAA-old")) || !exists(filepath.Join(dir, "AAA")) || !exists(filepath.Join(dir, "BBB")) {
		t.Error("the wrong backup was moved")
	}
	if _, _, code := sapu(t, e, "", "undo", "--last"); code != 0 {
		t.Fatal("undo failed")
	}
	sameSnapshot(t, before, snapshot(t, e))

	// the newest backup is offered and picked like any other, with the warning in the plan
	out, _, code = sapu(t, e, "y\n", "backups", "--pick", "1")
	if code != 0 || !strings.Contains(finderFlat(out), "may be the only copy of that iPhone") || exists(filepath.Join(dir, "AAA")) {
		t.Errorf("exit %d\n%s", code, out)
	}
}

func TestBackupsBadPicks(t *testing.T) {
	e := backupsHome(t)
	before := snapshot(t, e)
	for _, pick := range []string{"0", "4", "a", "3-1"} {
		if _, _, code := sapu(t, e, "y\n", "backups", "--pick", pick); code != 2 {
			t.Errorf("--pick %q: exit %d, want 2", pick, code)
		}
	}
	if _, _, code := sapu(t, e, "", "backups", "extra"); code != 2 {
		t.Errorf("extra argument: exit %d, want 2", code)
	}
	if _, _, code := sapu(t, e, "", "backups", "--plan", filepath.Join(t.TempDir(), "p.json")); code != 2 {
		t.Errorf("--plan without --pick: exit %d, want 2", code)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
}

func TestBackupsWithoutFullDiskAccess(t *testing.T) {
	e := backupsHome(t)
	finderDenied(t, filepath.Join(e.Home, finders.BackupsDir))

	out, errOut, code := sapu(t, e, "", "backups")
	if code != 1 || out != "" {
		t.Errorf("exit %d, stdout %q", code, out)
	}
	for _, want := range []string{"Full Disk Access", "sapu doctor", "~/Library/Application Support/MobileSync/Backup"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q: %q", want, errOut)
		}
	}
	out, _, code = sapu(t, e, "", "backups", "--json")
	if code != 1 || !strings.Contains(out, `"ok": false`) || !strings.Contains(out, "Full Disk Access") {
		t.Errorf("--json: exit %d\n%s", code, out)
	}
	validate(t, "backups", out)
}

func TestBackupsNoneFound(t *testing.T) {
	e := env.New(t.TempDir())
	out, _, code := sapu(t, e, "", "backups")
	if code != 0 || !strings.Contains(out, "No iPhone or iPad backups found.") {
		t.Errorf("exit %d\n%s", code, out)
	}
	noState(t, e)
}

func TestBackupsJSON(t *testing.T) {
	e := backupsHome(t)
	out, errOut, code := sapu(t, e, "", "backups", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "backups", out)
	var d struct {
		Items []struct {
			Number     int
			Device     string
			Model      string
			LastBackup string `json:"last_backup"`
			Newest     bool
		}
	}
	decodeData(t, out, &d)
	if len(d.Items) != 3 || d.Items[0].Device != "Ana's iPhone" || d.Items[0].Model != "iPhone14,2" ||
		!strings.HasPrefix(d.Items[0].LastBackup, "2026-05-01T10:00:00") || !d.Items[0].Newest || d.Items[2].Newest {
		t.Errorf("items: %+v", d.Items)
	}

	out, errOut, code = sapu(t, e, "", "backups", "--pick", "3", "--yes", "--json")
	if code != 0 {
		t.Fatalf("pick: exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "backups", out)
}
