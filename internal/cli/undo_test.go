package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An item that cannot go back must not hide the items that did: undo
// reports them with exit 3, and a second undo brings back the rest.
func TestUndoPartial(t *testing.T) {
	e := newHome(t)
	out, _, _ := sapu(t, e, "", "clean", "--yes")
	id := runIDOf(t, out)

	// .npm/_cacache cannot go back into a read-only .npm.
	npm := filepath.Join(e.Home, ".npm")
	if err := os.Chmod(npm, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(npm, 0o755) })

	jsonText, stderr, code := sapu(t, e, "", "undo", id, "--json")
	if code != 3 {
		t.Fatalf("exit %d, want 3\n%s\n%s", code, jsonText, stderr)
	}
	validate(t, "undo", jsonText)
	var d undoData
	decodeData(t, jsonText, &d)
	if len(d.Restored) != len(junkItems)-1 || d.Error == "" {
		t.Errorf("undo data %+v", d)
	}
	for _, r := range d.Restored {
		if strings.Contains(r.To, ".npm") {
			t.Errorf("reported as restored: %s", r.To)
		}
	}
	if exists(filepath.Join(npm, "_cacache")) {
		t.Error("the item came back into the read-only folder")
	}

	if err := os.Chmod(npm, 0o755); err != nil {
		t.Fatal(err)
	}
	text, _, code := sapu(t, e, "", "undo", id)
	if code != 0 || !strings.Contains(text, "Restored 1 item") {
		t.Errorf("second undo: exit %d\n%s", code, text)
	}
	for _, rel := range junkItems {
		if !exists(filepath.Join(e.Home, rel)) {
			t.Errorf("%s is not back", rel)
		}
	}
}
