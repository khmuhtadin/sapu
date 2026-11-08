package rules

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	ruledata "github.com/khmuhtadin/sapu/rules"
)

func loadSeed(t *testing.T) []Rule {
	t.Helper()
	cleaners, err := fs.Sub(ruledata.FS, "cleaners")
	if err != nil {
		t.Fatal(err)
	}
	rs, err := Load(cleaners)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func TestSeedRules(t *testing.T) {
	rs := loadSeed(t)
	if len(rs) < 50 {
		t.Errorf("%d seed rules, want at least 50", len(rs))
	}

	tooBroad := map[string]bool{
		"~/Library/Caches/**":              true,
		"~/Library/Logs/**":                true,
		"~/Library/Application Support/**": true,
		"~/Library/Containers/**":          true,
	}
	fixtures := map[string]string{}
	for _, r := range rs {
		if r.Risk == RiskRisky {
			t.Errorf("%s: no risky rules in this wave", r.ID)
		}
		for _, p := range r.Paths {
			if tooBroad[p] {
				t.Errorf("%s: %s is too broad; use one rule per app or tool", r.ID, p)
			}
		}
		if strings.ContainsAny(r.Explain["en"], "\n") {
			t.Errorf("%s: explain.en should be one short paragraph", r.ID)
		}
		if other, ok := fixtures[r.Fixture]; ok {
			t.Errorf("%s and %s share fixture %q", r.ID, other, r.Fixture)
		}
		fixtures[r.Fixture] = r.ID

		dir := filepath.Join("..", "..", "testdata", "fixtures", "rules", r.Fixture)
		for _, name := range []string{"expect.txt", "home"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				t.Errorf("%s: fixture %s is missing %s", r.ID, r.Fixture, name)
			}
		}
	}

	// Every fixture folder belongs to a rule.
	entries, err := os.ReadDir(filepath.Join("..", "..", "testdata", "fixtures", "rules"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() && fixtures[e.Name()] == "" {
			t.Errorf("fixture folder %s has no rule", e.Name())
		}
	}
}

// protectedPaths reads every protection file the same way the rules are read.
func protectedPaths(t *testing.T) []string {
	t.Helper()
	protect, err := fs.Sub(ruledata.FS, "protect")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	err = fs.WalkDir(protect, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(name) != ".yml" {
			return err
		}
		data, err := fs.ReadFile(protect, name)
		if err != nil {
			return err
		}
		var entries []struct {
			ID    string   `yaml:"id"`
			Paths []string `yaml:"paths"`
		}
		if err := yaml.Unmarshal(data, &entries); err != nil {
			return err
		}
		for _, e := range entries {
			paths = append(paths, e.Paths...)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no protected paths found")
	}
	return paths
}

func TestSeedRulesNeverTouchProtectedPaths(t *testing.T) {
	protected := protectedPaths(t)
	for _, r := range loadSeed(t) {
		for _, p := range r.Paths {
			for _, prot := range protected {
				if overlaps(t, p, prot) {
					t.Errorf("%s: path %s equals, contains or sits inside protected path %s", r.ID, p, prot)
				}
			}
		}
	}
}

// overlaps reports whether two path patterns can name the same place or one
// can sit inside the other: every segment of the shorter pattern may match the
// segment of the longer one. A final "**" counts as a wildcard segment.
func overlaps(t *testing.T, a, b string) bool {
	t.Helper()
	sa := strings.Split(strings.TrimPrefix(a, "~/"), "/")
	sb := strings.Split(strings.TrimPrefix(b, "~/"), "/")
	for i := 0; i < len(sa) && i < len(sb); i++ {
		if !segmentsMeet(t, sa[i], sb[i]) {
			return false
		}
	}
	return true
}

func segmentsMeet(t *testing.T, a, b string) bool {
	t.Helper()
	if a == "**" {
		a = "*"
	}
	if b == "**" {
		b = "*"
	}
	if a == b {
		return true
	}
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		ok, err := path.Match(pair[0], pair[1])
		if err != nil {
			t.Fatalf("bad glob %q: %v", pair[0], err)
		}
		if ok {
			return true
		}
	}
	return false
}

func TestOverlaps(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"~/Library/Caches/**", "~/Library/Caches/ms-playwright", true},
		{"~/Library/Caches/*/Cache", "~/Library/Caches/JetBrains", true},
		{"~/.cache/huggingface/hub/**", "~/.cache/huggingface", true},
		{"~/.cache/huggingface", "~/.cache/huggingface", true},
		{"~/Library/Caches/ms-playwright/chromium-1/x", "~/Library/Caches/ms-playwright", true},
		{"~/Pictures/x.photoslibrary/y", "~/Pictures/*.photoslibrary", true},
		{"~/Library/Caches/Google/Chrome/*/Cache", "~/Library/Caches/JetBrains", false},
		{"~/Library/Caches/ms-playwright-x", "~/Library/Caches/ms-playwright", false},
		{"~/Pictures/foo", "~/Pictures/*.photoslibrary", false},
		{"~/.claude.json.bak", "~/.claude.json", false},
		{"~/Library/Caches/camoufox", "~/Library/Application Support/camoufox", false},
	}
	for _, tt := range tests {
		if got := overlaps(t, tt.a, tt.b); got != tt.want {
			t.Errorf("overlaps(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
