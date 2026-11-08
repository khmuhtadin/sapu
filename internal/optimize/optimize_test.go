package optimize

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/rules"
)

// Not parallel: it replaces LookPath.
func TestItems(t *testing.T) {
	dir := t.TempDir()
	ql := filepath.Join(dir, "qlmanage")
	if err := os.WriteFile(ql, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := LookPath
	t.Cleanup(func() { LookPath = old })
	LookPath = func(name string) (string, error) {
		if name == "qlmanage" {
			return ql, nil
		}
		return "", errors.New("not found") // atsutil is missing: left out
	}

	items := Items()

	if len(items) != 1 {
		t.Fatalf("items %+v, want qlmanage only", items)
	}
	it := items[0]
	if it.Path != ql || it.Source != "optimize" || it.Risk != rules.RiskSafe || it.Action != rules.ActionOfficialCommand ||
		strings.Join(it.Command, " ") != ql+" -r cache" || it.Bytes != 0 || it.Identity.Ino == 0 {
		t.Errorf("item %+v", it)
	}
}

// Every action says what it does, why it is safe, what you will notice and
// which problem it fixes, and makes no speed claim.
func TestTextsAreHonest(t *testing.T) {
	var texts []string
	for _, a := range actions {
		texts = append(texts, a.explain)
	}
	for _, a := range Admin() {
		if !strings.HasPrefix(a.Command, "sudo ") {
			t.Errorf("%s: %q does not show that it needs admin rights", a.Title, a.Command)
		}
		texts = append(texts, a.Explain)
	}
	for _, text := range texts {
		for _, want := range []string{"Safe, because", "You will notice", "It fixes"} {
			if !strings.Contains(text, want) {
				t.Errorf("%q lacks %q", text, want)
			}
		}
		for _, claim := range []string{"faster", "speed", "boost", "performance"} {
			if strings.Contains(strings.ToLower(text), claim) {
				t.Errorf("%q makes a speed claim (%q)", text, claim)
			}
		}
	}
	if !strings.Contains(Intro, "none of them makes your Mac faster") || Rollback != "Nothing to roll back: these only clear caches macOS rebuilds." {
		t.Errorf("intro %q, rollback %q", Intro, Rollback)
	}
}
