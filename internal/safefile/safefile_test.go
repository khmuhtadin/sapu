package safefile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestWriteCreatesAndReplaces(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.json")
	if err := Write(dir, p, []byte("first, longer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, p, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "second" {
		t.Fatalf("content %q", b)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
}

// The M2 review's case: a snapshot path that is a symlink to a document.
func TestWriteRefusesSymlinkAndKeepsTarget(t *testing.T) {
	dir := t.TempDir()
	doc := filepath.Join(dir, "thesis.txt")
	if err := os.WriteFile(doc, []byte("my thesis"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "2026-11-08.json")
	if err := os.Symlink(doc, link); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, link, []byte("{}"), 0o600); !errors.Is(err, ErrRefused) {
		t.Fatalf("err %v, want ErrRefused", err)
	}
	if b, _ := os.ReadFile(doc); string(b) != "my thesis" {
		t.Fatalf("the document was changed: %q", b)
	}
}

func TestWriteRefusesHardLinkAndKeepsOtherName(t *testing.T) {
	dir := t.TempDir()
	doc := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(doc, []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "state.json")
	if err := os.Link(doc, p); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, p, []byte("{}"), 0o600); !errors.Is(err, ErrRefused) {
		t.Fatalf("err %v, want ErrRefused", err)
	}
	if b, _ := os.ReadFile(doc); string(b) != "notes" {
		t.Fatalf("the other name was changed: %q", b)
	}
}

func TestWriteRefusesSymlinkedFolder(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "trends")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, filepath.Join(link, "x.json"), []byte("{}"), 0o600); !errors.Is(err, ErrRefused) {
		t.Fatalf("err %v, want ErrRefused", err)
	}
	if _, err := os.Lstat(filepath.Join(real, "x.json")); err == nil {
		t.Error("a file was written through the symlinked folder")
	}
}

// mkdirs makes each folder (and its parents) or fails the test.
func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

// symlink makes link point to target or fails the test.
func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// The final review's case: a symlink in an ANCESTOR folder of the file
// (state/alias -> real, outside the state folder). The folder that holds
// the file (alias/nested) is a real folder, so only resolving the whole
// path catches it. Before the fix, Write truncated real/nested/data.
func TestWriteRefusesAncestorSymlinkOutOfRoot(t *testing.T) {
	d := t.TempDir()
	state := filepath.Join(d, "state")
	real := filepath.Join(d, "real")
	mkdirs(t, state, filepath.Join(real, "nested"))
	doc := filepath.Join(real, "nested", "data")
	if err := os.WriteFile(doc, []byte("user data"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink(t, real, filepath.Join(state, "alias"))

	err := Write(state, filepath.Join(state, "alias", "nested", "data"), []byte("replacement"), 0o600)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("err %v, want ErrRefused", err)
	}
	if b, _ := os.ReadFile(doc); string(b) != "user data" {
		t.Fatalf("the write followed the ancestor symlink: %q", b)
	}
	// A new file is refused the same way: nothing is created out there.
	err = Write(state, filepath.Join(state, "alias", "nested", "new"), []byte("x"), 0o600)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("new file: err %v, want ErrRefused", err)
	}
	if _, err := os.Lstat(filepath.Join(real, "nested", "new")); err == nil {
		t.Error("a file was created through the ancestor symlink")
	}
}

// A symlink inside the root that points outside it, as the folder that
// holds the file, is refused too.
func TestWriteRefusesSymlinkedFolderOutOfRoot(t *testing.T) {
	d := t.TempDir()
	state := filepath.Join(d, "state")
	outside := filepath.Join(d, "outside")
	mkdirs(t, state, outside)
	symlink(t, outside, filepath.Join(state, "trends"))

	if err := Write(state, filepath.Join(state, "trends", "x.json"), []byte("{}"), 0o600); !errors.Is(err, ErrRefused) {
		t.Fatalf("err %v, want ErrRefused", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "x.json")); err == nil {
		t.Error("a file was written outside the root")
	}
}

// The root itself may be reached through a symlink (~/.sapu kept in a
// dotfiles folder; t.TempDir is under /var, a symlink to /private/var).
func TestWriteThroughSymlinkedRoot(t *testing.T) {
	d := t.TempDir()
	real := filepath.Join(d, "dotfiles", "sapu")
	mkdirs(t, filepath.Join(real, "trends"), filepath.Join(d, "home"))
	root := filepath.Join(d, "home", ".sapu")
	symlink(t, real, root)

	for _, name := range []string{"whitelist.txt", filepath.Join("trends", "2026-11-08.json")} {
		if err := Write(root, filepath.Join(root, name), []byte("ok"), 0o600); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if b, _ := os.ReadFile(filepath.Join(real, name)); string(b) != "ok" {
			t.Errorf("%s: content %q in the real folder", name, b)
		}
	}
}

// Open, used for appending (journals, agent.log), refuses a symlink and a
// hard link before anything is written, and leaves the file behind unchanged.
func TestOpenRefusesLinksAndKeepsTarget(t *testing.T) {
	for _, flag := range []int{
		os.O_WRONLY | os.O_CREATE | os.O_APPEND,
		os.O_RDWR | os.O_APPEND,
	} {
		dir := t.TempDir()
		doc := filepath.Join(dir, "thesis.txt")
		if err := os.WriteFile(doc, []byte("my thesis"), 0o600); err != nil {
			t.Fatal(err)
		}
		sym := filepath.Join(dir, "sym.log")
		symlink(t, doc, sym)
		hard := filepath.Join(dir, "hard.log")
		if err := os.Link(doc, hard); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{sym, hard} {
			f, err := Open(dir, p, flag, 0o600)
			if !errors.Is(err, ErrRefused) {
				if f != nil {
					_ = f.Close()
				}
				t.Fatalf("flag %#x, %s: err %v, want ErrRefused", flag, filepath.Base(p), err)
			}
		}
		if b, _ := os.ReadFile(doc); string(b) != "my thesis" {
			t.Fatalf("flag %#x: the document changed: %q", flag, b)
		}
	}
}

func TestOpenAppends(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "agent.log")
	for _, line := range []string{"one\n", "two\n"} {
		f, err := Open(dir, p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(line); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(p); string(b) != "one\ntwo\n" {
		t.Fatalf("content %q", b)
	}
}

// The re-review's race: a folder is checked, then its name is swapped for a
// symlink to somewhere else before the file is opened. Open works on an open
// folder, not on its name, so the swap cannot lead the write out. Both the
// root and a folder below it are swapped here, after openDir and before the
// file is opened inside the folder it returned.
func TestFolderSwapAfterOpenCannotRedirect(t *testing.T) {
	for _, sub := range []string{"", "trends"} {
		d := t.TempDir()
		state := filepath.Join(d, "state")
		outside := filepath.Join(d, "outside")
		mkdirs(t, filepath.Join(state, sub), outside)
		doc := filepath.Join(outside, "data")
		if err := os.WriteFile(doc, []byte("user data"), 0o600); err != nil {
			t.Fatal(err)
		}
		folder := filepath.Join(state, sub)
		fd, err := openDir(state, folder)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(folder, folder+"-old"); err != nil {
			t.Fatal(err)
		}
		symlink(t, outside, folder)

		f, err := openat(fd, "data", unix.O_WRONLY|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if cerr := unix.Close(fd); cerr != nil {
			t.Fatal(cerr)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := unix.Write(f, []byte("replacement")); err != nil {
			t.Fatal(err)
		}
		if err := unix.Close(f); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(doc); string(b) != "user data" {
			t.Errorf("swap of %q: the outside document became %q", sub, b)
		}
		if b, _ := os.ReadFile(filepath.Join(folder+"-old", "data")); string(b) != "replacement" {
			t.Errorf("swap of %q: the write did not land in the folder that was opened: %q", sub, b)
		}
	}
}

// A FIFO where Sapu writes (say agent.log) is refused at once: opening it to
// write must not wait for a reader that never comes.
func TestOpenRefusesFIFOWithoutWaiting(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "agent.log")
	if err := unix.Mkfifo(p, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		f, err := Open(d, p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err == nil {
			err = f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrRefused) {
			t.Fatalf("err %v, want ErrRefused", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Open is still waiting on the FIFO")
	}
}
