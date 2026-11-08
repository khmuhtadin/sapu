package protect

import (
	"errors"
	"os"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/safefile"
)

// The whitelist is Sapu's own file. A symlink in its place is refused, by
// add and by remove, and the file it points to stays as it was.
func TestWhitelistIsNeverWrittenThroughASymlink(t *testing.T) {
	e := env.New(t.TempDir())
	if err := os.MkdirAll(e.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := e.Home + "/precious.txt"
	const content = "~/keep\n"
	if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, e.WhitelistFile()); err != nil {
		t.Fatal(err)
	}

	if err := AddWhitelist(e, "~/other"); !errors.Is(err, safefile.ErrRefused) {
		t.Errorf("AddWhitelist through a symlink: err = %v, want a refusal", err)
	}
	if err := RemoveWhitelist(e, "~/keep"); !errors.Is(err, safefile.ErrRefused) {
		t.Errorf("RemoveWhitelist through a symlink: err = %v, want a refusal", err)
	}
	if b, _ := os.ReadFile(target); string(b) != content {
		t.Errorf("the file behind the symlink changed to %q", b)
	}
}

func TestWhitelistAddAndRemove(t *testing.T) {
	e := env.New(t.TempDir())
	if err := AddWhitelist(e, "~/a"); err != nil {
		t.Fatal(err)
	}
	if err := AddWhitelist(e, "~/b"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveWhitelist(e, "~/a"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadWhitelist(e)
	if err != nil || len(got) != 1 || got[0] != "~/b" {
		t.Errorf("whitelist = %q, %v; want [~/b]", got, err)
	}
}
