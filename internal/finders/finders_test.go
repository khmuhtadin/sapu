package finders

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
)

const fixtures = "../../testdata/fixtures/finders"

var (
	now     = time.Now()
	longAgo = now.AddDate(-2, 0, 0)
)

// put writes size bytes at rel inside the home folder and sets its times.
func put(t *testing.T, e env.Env, rel string, size int, when time.Time) string {
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

func paths(files []File) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.Item.Path)
	}
	return out
}

func TestParseSize(t *testing.T) {
	good := map[string]int64{"500MB": 500_000_000, "10M": 10_000_000, "1.5GB": 1_500_000_000, "2048": 2048, "3kb": 3000, "1T": 1_000_000_000_000, " 5 MB ": 5_000_000}
	for in, want := range good {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "MB", "big", "-5MB", "5XB"} {
		if _, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) succeeded", in)
		}
	}
}

func TestParseAge(t *testing.T) {
	good := map[string]time.Duration{"1y": 365 * 24 * time.Hour, "2y": 730 * 24 * time.Hour, "90d": 90 * 24 * time.Hour, "2w": 14 * 24 * time.Hour}
	for in, want := range good {
		if got, err := ParseAge(in); err != nil || got != want {
			t.Errorf("ParseAge(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "y", "-1y", "xy", "5q"} {
		if _, err := ParseAge(in); err == nil {
			t.Errorf("ParseAge(%q) succeeded", in)
		}
	}
}

func TestParsePick(t *testing.T) {
	good := map[string][]int{"1": {1}, "1,3,5": {1, 3, 5}, "2-4": {2, 3, 4}, "1, 3-4,7": {1, 3, 4, 7}, "2-2": {2}}
	for in, want := range good {
		if got, err := ParsePick(in); err != nil || !slices.Equal(got, want) {
			t.Errorf("ParsePick(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "-1", "x", "1,,2", "1,", "4-2", "1-", "-3", "1-x", "1.5"} {
		if got, err := ParsePick(in); err == nil {
			t.Errorf("ParsePick(%q) = %v, want an error", in, got)
		}
	}
}

func TestImagePaths(t *testing.T) {
	plist := `<dict><key>image-path</key><string>/mnt/a/Downloads/R&amp;D &lt;1&gt;.dmg</string>
<key>image-path</key>
<string>/b.iso</string></dict>`
	got := imagePaths([]byte(plist))
	if want := []string{"/mnt/a/Downloads/R&D <1>.dmg", "/b.iso"}; !slices.Equal(got, want) {
		t.Errorf("imagePaths = %q, want %q", got, want)
	}
}

func TestLargeFindsOnlyTheRightFiles(t *testing.T) {
	e := env.New(t.TempDir())
	const big = 200_000
	want := []string{
		put(t, e, "Movies/old.mov", big, longAgo),
		put(t, e, "Documents/archive.zip", big+50_000, longAgo),
		put(t, e, "Desktop/disk.dmg", big+20_000, longAgo),
	}
	// not found, with the reason
	put(t, e, "Movies/new.mov", big, now)             // changed recently
	put(t, e, "Movies/small.mov", 10_000, longAgo)    // too small
	put(t, e, "Library/Caches/big.bin", big, longAgo) // inside ~/Library
	put(t, e, ".sapu/quarantine/run/big.bin", big, longAgo)
	put(t, e, "Apps/Foo.app/Contents/big.bin", big, longAgo)
	put(t, e, "Pictures/Photos.photoslibrary/originals/big.heic", big, longAgo)
	put(t, e, "code/proj/.git/objects/pack/big.pack", big, longAgo)
	// opened recently: old content, but read a minute ago
	opened := put(t, e, "Movies/watched.mov", big, longAgo)
	if err := os.Chtimes(opened, now, longAgo); err != nil {
		t.Fatal(err)
	}

	files, err := Large(context.Background(), e, []string{e.Home}, LargeOptions{
		MinSize: 100_000, OlderThan: 90 * 24 * time.Hour, Type: "any", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	// biggest first
	want = []string{want[1], want[2], want[0]}
	if got := paths(files); !slices.Equal(got, want) {
		t.Errorf("found\n%v\nwant\n%v", got, want)
	}
	for _, f := range files {
		it := f.Item
		if it.Source != "large" || it.Risk != "risky" || it.Action != "quarantine" || it.Bytes < 100_000 || it.Identity.Ino == 0 || it.Explain == "" {
			t.Errorf("bad item %+v", it)
		}
	}
	if files[2].Type != "mov" {
		t.Errorf("type of %s is %q", files[2].Item.Path, files[2].Type)
	}

	// --type
	for kind, only := range map[string]string{"video": want[2], "archive": want[0], "disk-image": want[1]} {
		files, err := Large(context.Background(), e, []string{e.Home}, LargeOptions{
			MinSize: 100_000, OlderThan: 90 * 24 * time.Hour, Type: kind, Now: now,
		})
		if err != nil || !slices.Equal(paths(files), []string{only}) {
			t.Errorf("type %s: %v, %v; want [%s]", kind, paths(files), err, only)
		}
	}
	if _, err := Large(context.Background(), e, []string{e.Home}, LargeOptions{Type: "text", Now: now}); err == nil {
		t.Error("unknown type was accepted")
	}
}

func TestLargeRefusesRootsInsideSkippedFolders(t *testing.T) {
	e := env.New(t.TempDir())
	put(t, e, "Library/Caches/big.bin", 200_000, longAgo)
	put(t, e, "Apps/Foo.app/big.bin", 200_000, longAgo)
	opts := LargeOptions{MinSize: 1, Type: "any", Now: now}
	for _, root := range []string{"Library", "Library/Caches", "Apps/Foo.app", ".sapu"} {
		if err := os.MkdirAll(filepath.Join(e.Home, root), 0o755); err != nil {
			t.Fatal(err)
		}
		files, err := Large(context.Background(), e, []string{filepath.Join(e.Home, root)}, opts)
		if err == nil || len(files) > 0 {
			t.Errorf("root %s: %v, %v; want an error", root, paths(files), err)
		}
	}
}

func TestLargeSkipsLibraryUnderAnotherSpellingOfHome(t *testing.T) {
	e := env.New(t.TempDir())
	put(t, e, "Library/big.bin", 200_000, longAgo)
	put(t, e, "Movies/old.mov", 200_000, longAgo)
	// the temp folder lives behind /var -> /private/var, so the real spelling differs
	real, err := filepath.EvalSymlinks(e.Home)
	if err != nil {
		t.Fatal(err)
	}
	files, err := Large(context.Background(), e, []string{real}, LargeOptions{MinSize: 1, Type: "any", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(files); len(got) != 1 || filepath.Base(got[0]) != "old.mov" {
		t.Errorf("found %v, want only old.mov", got)
	}
}

func TestLargeIgnoresSymlinksAndMissingRoots(t *testing.T) {
	e := env.New(t.TempDir())
	target := put(t, e, "Movies/old.mov", 200_000, longAgo)
	if err := os.Symlink(target, filepath.Join(e.Home, "link.mov")); err != nil {
		t.Fatal(err)
	}
	files, err := Large(context.Background(), e, []string{e.Home, filepath.Join(e.Home, "nothing-here")}, LargeOptions{MinSize: 1, Type: "any", Now: now})
	if err != nil || len(files) != 1 || files[0].Item.Path != target {
		t.Errorf("found %v, %v; want only %s", paths(files), err, target)
	}
}

func TestLargeCancel(t *testing.T) {
	e := env.New(t.TempDir())
	put(t, e, "Movies/old.mov", 200_000, longAgo)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Large(ctx, e, []string{e.Home}, LargeOptions{MinSize: 1, Type: "any", Now: now}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func fakeMounted(t *testing.T, paths ...string) {
	t.Helper()
	old := MountedImages
	MountedImages = func(context.Context) ([]string, error) { return paths, nil }
	t.Cleanup(func() { MountedImages = old })
}

func TestInstallers(t *testing.T) {
	e := env.New(t.TempDir())
	mounted := put(t, e, "Downloads/Mounted.dmg", 5000, longAgo)
	fakeMounted(t, mounted)
	want := []string{
		put(t, e, "Downloads/App.dmg", 20000, longAgo),
		put(t, e, "Downloads/tool.PKG", 15000, longAgo), // extension in capitals
		put(t, e, "Desktop/linux.iso", 9000, longAgo),
		put(t, e, "Downloads/Xcode.xip", 6000, longAgo),
		put(t, e, "Downloads/meta.mpkg", 3000, longAgo),
	}
	put(t, e, "Downloads/zips/Inside.dmg", 9000, longAgo)         // subfolders are not entered
	put(t, e, "Desktop/project/dist/Build.dmg", 9000, longAgo)    // a build result, not a download
	put(t, e, "Downloads/Fresh.dmg", 9000, now)                   // changed recently
	put(t, e, "Downloads/notes.txt", 9000, longAgo)               // not an installer
	put(t, e, "Downloads/Some.app/Contents/x.pkg", 9000, longAgo) // inside an app bundle

	items, skips, err := Installers(context.Background(), e,
		[]string{filepath.Join(e.Home, "Downloads"), filepath.Join(e.Home, "Desktop"), filepath.Join(e.Home, "Nothing")},
		InstallerOptions{OlderThan: 30 * 24 * time.Hour, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.Path)
		if it.Source != "installer" || it.Risk != "moderate" || !it.Regenerates || !strings.Contains(it.Explain, "download it again") {
			t.Errorf("bad item %+v", it)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("found\n%v\nwant\n%v", got, want)
	}
	if len(skips) != 1 || skips[0].Path != mounted || skips[0].Reason != "in_use" || skips[0].Detail != "mounted disk image" {
		t.Errorf("skips = %+v, want the mounted image", skips)
	}
}

func TestInstallersMountedThroughASymlink(t *testing.T) {
	e := env.New(t.TempDir())
	image := put(t, e, "Downloads/Mounted.dmg", 5000, longAgo)
	link := filepath.Join(e.Home, "image-link.dmg")
	if err := os.Symlink(image, link); err != nil {
		t.Fatal(err)
	}
	fakeMounted(t, link) // hdiutil may name the file by another path
	items, skips, err := Installers(context.Background(), e, []string{filepath.Join(e.Home, "Downloads")}, InstallerOptions{Now: now})
	if err != nil || len(items) != 0 || len(skips) != 1 {
		t.Errorf("items %v, skips %v, err %v", items, skips, err)
	}
}

func TestInstallersFailWhenMountsAreUnknown(t *testing.T) {
	e := env.New(t.TempDir())
	put(t, e, "Downloads/App.dmg", 5000, longAgo)
	old := MountedImages
	MountedImages = func(context.Context) ([]string, error) { return nil, errors.New("hdiutil is broken") }
	t.Cleanup(func() { MountedImages = old })
	if items, _, err := Installers(context.Background(), e, []string{filepath.Join(e.Home, "Downloads")}, InstallerOptions{Now: now}); err == nil {
		t.Errorf("offered %v although mounted images are unknown", items)
	}

	// Without a disk image the question never comes up.
	e2 := env.New(t.TempDir())
	want := put(t, e2, "Downloads/App.pkg", 5000, longAgo)
	items, _, err := Installers(context.Background(), e2, []string{filepath.Join(e2.Home, "Downloads")}, InstallerOptions{Now: now})
	if err != nil || len(items) != 1 || items[0].Path != want {
		t.Errorf("items %v, err %v", items, err)
	}
}

// writeBackup makes a backup folder with an Info.plist from the fixture.
func writeBackup(t *testing.T, e env.Env, id, device, udid string, when time.Time, size int) string {
	t.Helper()
	dir := filepath.Join(e.Home, BackupsDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	tmpl, err := os.ReadFile(filepath.Join(fixtures, "backup-Info.plist"))
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
	return dir
}

func TestBackups(t *testing.T) {
	e := env.New(t.TempDir())
	d1 := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	d2 := time.Date(2025, 1, 2, 9, 30, 0, 0, time.UTC)
	d3 := time.Date(2026, 3, 3, 8, 0, 0, 0, time.UTC)
	newest := writeBackup(t, e, "AAA", "Ana's iPhone", "AAA", d1, 30_000)
	older := writeBackup(t, e, "AAA-old", "Ana's iPhone", "AAA", d2, 20_000)
	other := writeBackup(t, e, "BBB", "Budi iPad", "BBB", d3, 10_000)
	broken := filepath.Join(e.Home, BackupsDir, "CCC")
	if err := os.MkdirAll(broken, 0o755); err != nil { // an interrupted backup
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.Home, BackupsDir, ".DS_Store"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Backups(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	byPath := map[string]Backup{}
	for _, b := range got {
		order = append(order, b.Item.Path)
		byPath[b.Item.Path] = b
	}
	// newest first; the one without a date is last
	if want := []string{newest, other, older, broken}; !slices.Equal(order, want) {
		t.Fatalf("order\n%v\nwant\n%v", order, want)
	}
	n := byPath[newest]
	if n.Device != "Ana's iPhone" || n.Model != "iPhone14,2" || !n.LastBackup.Equal(d1) || !n.Newest {
		t.Errorf("newest backup: %+v", n)
	}
	if n.Item.Source != "backups" || n.Item.Risk != "risky" || n.Item.Bytes < 30_000 {
		t.Errorf("newest item: %+v", n.Item)
	}
	if !strings.Contains(n.Item.Explain, "may be the only copy of that iPhone") {
		t.Errorf("no warning on the newest backup: %q", n.Item.Explain)
	}
	if o := byPath[older]; o.Newest || strings.Contains(o.Item.Explain, "only copy") || !strings.Contains(o.Item.Explain, "newer backup") {
		t.Errorf("older backup: %+v", o)
	}
	if o := byPath[other]; !o.Newest || o.Device != "Budi iPad" {
		t.Errorf("the other device's only backup must be newest: %+v", o)
	}
	if b := byPath[broken]; b.Device != "" || !b.LastBackup.IsZero() || !b.Newest || !strings.Contains(b.Item.Explain, "only copy") {
		t.Errorf("backup without Info.plist: %+v", b)
	}
}

func TestBackupsMissingFolderIsEmpty(t *testing.T) {
	got, err := Backups(context.Background(), env.New(t.TempDir()))
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v", got, err)
	}
}

// denied makes dir unreadable for this test, or skips it when permissions do not apply.
func denied(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root can read everything")
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Error(err)
		}
	})
}

func wantAccessError(t *testing.T, err error, path string) {
	t.Helper()
	var ae *AccessError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want *AccessError", err)
	}
	for _, text := range []string{path, "Full Disk Access", "sapu doctor"} {
		if !strings.Contains(err.Error(), text) {
			t.Errorf("error %q lacks %q", err, text)
		}
	}
}

func TestBackupsWithoutFullDiskAccess(t *testing.T) {
	e := env.New(t.TempDir())
	writeBackup(t, e, "AAA", "Ana's iPhone", "AAA", now, 1000)
	denied(t, filepath.Join(e.Home, BackupsDir))
	_, err := Backups(context.Background(), e)
	wantAccessError(t, err, "~/"+BackupsDir)

	// the folder is listed but a backup is closed: still a clear message
	if err := os.Chmod(filepath.Join(e.Home, BackupsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(e.Home, BackupsDir, "AAA")
	denied(t, dir)
	_, err = Backups(context.Background(), e)
	wantAccessError(t, err, "~/"+BackupsDir+"/AAA")
}

func TestMessages(t *testing.T) {
	e := env.New(t.TempDir())
	att := "Library/Messages/Attachments/ab/11/at_0_UUID/"
	huge := put(t, e, att+"movie.MOV", 300_000, longAgo)
	mid := put(t, e, att+"photo.heic", 200_000, longAgo)
	put(t, e, att+"recent.mov", 300_000, now)               // too new
	put(t, e, att+"small.jpg", 5_000, longAgo)              // too small
	put(t, e, "Library/Messages/chat.db", 300_000, longAgo) // not an attachment
	put(t, e, "Documents/big.mov", 300_000, longAgo)

	got, err := Messages(context.Background(), e, MessagesOptions{MinSize: 100_000, OlderThan: 365 * 24 * time.Hour, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Path != huge || got[1].Path != mid {
		t.Fatalf("got %+v", got)
	}
	if got[0].Type != "mov" || got[1].Type != "heic" || got[0].Bytes < 300_000 || got[0].Modified.Sub(longAgo).Abs() > time.Second {
		t.Errorf("details: %+v", got[0])
	}
}

func TestMessagesWithoutFullDiskAccess(t *testing.T) {
	e := env.New(t.TempDir())
	put(t, e, "Library/Messages/Attachments/a/b.mov", 1000, longAgo)
	denied(t, filepath.Join(e.Home, MessagesDir))
	_, err := Messages(context.Background(), e, MessagesOptions{Now: now})
	wantAccessError(t, err, "~/"+MessagesDir)

	// no Messages folder at all is just an empty list
	got, err := Messages(context.Background(), env.New(t.TempDir()), MessagesOptions{Now: now})
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v", got, err)
	}
}

func fakeVolumes(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := Volumes
	Volumes = dir
	t.Cleanup(func() { Volumes = old })
	return dir
}

func TestTrash(t *testing.T) {
	e := env.New(t.TempDir())
	volumes := fakeVolumes(t)
	put(t, e, ".Trash/report.pdf", 3000, now)
	put(t, e, ".Trash/.hidden-config", 100, now)
	put(t, e, ".Trash/old folder/inside/deep.txt", 5000, now)
	uid := strconv.Itoa(os.Getuid())
	onDisk := filepath.Join(volumes, "Backup Disk", ".Trashes", uid, "movie.mov")
	if err := os.MkdirAll(filepath.Dir(onDisk), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(onDisk, bytes.Repeat([]byte("x"), 9000), 0o644); err != nil {
		t.Fatal(err)
	}
	// another user's Trash on the same disk, a disk without Trash, and the startup disk link
	other := filepath.Join(volumes, "Backup Disk", ".Trashes", "0", "theirs.txt")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(volumes, "Empty Disk"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(e.Home, filepath.Join(volumes, "Macintosh HD")); err != nil {
		t.Fatal(err)
	}

	items, err := Trash(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.Path)
		if it.Source != "trash" || it.Risk != "moderate" || it.Action != "quarantine" || it.Bytes == 0 || it.Identity.Ino == 0 {
			t.Errorf("bad item %+v", it)
		}
	}
	want := []string{
		filepath.Join(e.Home, ".Trash/.hidden-config"),
		filepath.Join(e.Home, ".Trash/old folder"),
		filepath.Join(e.Home, ".Trash/report.pdf"),
		onDisk,
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("found\n%v\nwant\n%v", got, want)
	}
	for _, it := range items {
		if it.Path == filepath.Join(e.Home, ".Trash/old folder") && it.Bytes < 5000 {
			t.Errorf("a folder counts what is inside: %d bytes", it.Bytes)
		}
	}
}

func TestTrashWithoutFullDiskAccess(t *testing.T) {
	e := env.New(t.TempDir())
	fakeVolumes(t)
	put(t, e, ".Trash/a.txt", 10, now)
	denied(t, filepath.Join(e.Home, ".Trash"))
	_, err := Trash(context.Background(), e)
	wantAccessError(t, err, "~/.Trash")
}

func TestTrashNothingThere(t *testing.T) {
	e := env.New(t.TempDir())
	fakeVolumes(t)
	if items, err := Trash(context.Background(), e); err != nil || len(items) != 0 {
		t.Errorf("items %v, err %v", items, err)
	}
	old := Volumes
	Volumes = filepath.Join(t.TempDir(), "no-volumes")
	t.Cleanup(func() { Volumes = old })
	if items, err := Trash(context.Background(), e); err != nil || len(items) != 0 {
		t.Errorf("items %v, err %v", items, err)
	}
}

func TestImagePathsOfTheFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(fixtures, "hdiutil-info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	plist := strings.ReplaceAll(string(data), "__IMAGE_PATH__", "/mnt/example/Downloads/App &amp; Co.dmg")
	if got := imagePaths([]byte(plist)); !slices.Equal(got, []string{"/mnt/example/Downloads/App & Co.dmg"}) {
		t.Errorf("imagePaths = %q", got)
	}
}
