package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/safefile"
)

// agent.log replaced by a symlink to, or a hard link of, a document: Run
// refuses it before it appends a line, and the document stays as it was.
func TestRunRefusesLinkedLog(t *testing.T) {
	for name, link := range map[string]func(doc, log string) error{
		"symlink":   os.Symlink,
		"hard link": os.Link,
	} {
		_, e := fake(t)
		doc := filepath.Join(e.Home, "thesis.txt")
		if err := os.WriteFile(doc, []byte("my thesis"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(e.StateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := link(doc, LogPath(e)); err != nil {
			t.Fatal(err)
		}

		err := Run(context.Background(), e, time.Now())
		if !errors.Is(err, safefile.ErrRefused) {
			t.Errorf("%s: err %v, want safefile.ErrRefused", name, err)
		}
		if b, _ := os.ReadFile(doc); string(b) != "my thesis" {
			t.Fatalf("%s: Run appended to the document: %q", name, b)
		}
	}
}
