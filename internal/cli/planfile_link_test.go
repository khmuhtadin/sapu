package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The final review's case: --plan names a file that is a hard link to a
// document. Before the fix savePlan opened it with O_TRUNC and replaced the
// document with the plan.
func TestCleanPlanFileRefusesHardLink(t *testing.T) {
	e := newHome(t)
	dir := t.TempDir()
	doc := filepath.Join(dir, "thesis.txt")
	writeFile(t, doc, "my thesis")
	out := filepath.Join(dir, "plan.json")
	if err := os.Link(doc, out); err != nil {
		t.Fatal(err)
	}

	_, errOut, code := sapu(t, e, "", "clean", "--plan", out)
	if code != 1 || !strings.Contains(errOut, "hard links") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
	if got, _ := os.ReadFile(doc); string(got) != "my thesis" {
		t.Errorf("the hard-linked document was overwritten: %q", got)
	}
	noState(t, e)
}

// A plan file reached through a symlinked folder the user named is fine:
// the folder of a plan file is its own root.
func TestCleanPlanFileInSymlinkedFolder(t *testing.T) {
	e := newHome(t)
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}

	if _, errOut, code := sapu(t, e, "", "clean", "--plan", filepath.Join(alias, "plan.json")); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if len(selectedPaths(readPlanFile(t, filepath.Join(real, "plan.json")))) != len(junkItems) {
		t.Error("the plan was not written into the real folder")
	}
	noState(t, e)
}
