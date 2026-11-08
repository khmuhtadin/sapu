package runlog

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/safefile"
)

// The final review's case: journal.jsonl replaced by a symlink to, or a hard
// link of, a document. Before the fix Open followed it, saw a "torn line"
// (the document does not end in a newline) and appended to the document.
func TestOpenRefusesLinkedJournal(t *testing.T) {
	for name, link := range map[string]func(doc, journal string) error{
		"symlink":   os.Symlink,
		"hard link": os.Link,
	} {
		e := env.New(t.TempDir())
		r := start(t, e, plan.KindPurge, now)
		must(t, r.Close(now, true))
		doc := filepath.Join(e.Home, "thesis.txt")
		must(t, os.WriteFile(doc, []byte("my thesis"), 0o600))
		journal := filepath.Join(r.Dir, journalFile)
		must(t, os.Remove(journal))
		must(t, link(doc, journal))

		opened, err := Open(e, r.ID)
		if !errors.Is(err, safefile.ErrRefused) {
			if opened != nil {
				must(t, opened.Close(now, false))
			}
			t.Fatalf("%s: err %v, want safefile.ErrRefused", name, err)
		}
		if b, _ := os.ReadFile(doc); string(b) != "my thesis" {
			t.Fatalf("%s: Open appended to the document: %q", name, b)
		}
	}
}

// A run folder that is a symlink out of the state folder is refused too.
func TestOpenRefusesRunFolderOutsideState(t *testing.T) {
	e := env.New(t.TempDir())
	r := start(t, e, plan.KindPurge, now)
	must(t, r.Close(now, false)) // no end event: the journal ends in a full line
	outside := filepath.Join(e.Home, "elsewhere")
	must(t, os.Rename(r.Dir, outside))
	must(t, os.Symlink(outside, r.Dir))
	before, err := os.ReadFile(filepath.Join(outside, journalFile))
	must(t, err)

	opened, err := Open(e, r.ID)
	if !errors.Is(err, safefile.ErrRefused) {
		if opened != nil {
			must(t, opened.Close(now, true))
		}
		t.Fatalf("err %v, want safefile.ErrRefused", err)
	}
	if after, _ := os.ReadFile(filepath.Join(outside, journalFile)); string(after) != string(before) {
		t.Fatalf("the journal outside the state folder changed: %q", after)
	}
}
