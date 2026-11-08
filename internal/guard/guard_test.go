// Package guard enforces the safety rules in AGENTS.md as a plain Go test,
// so `go test ./...` fails without any linter installed.
package guard

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type ban struct {
	re   *regexp.Regexp
	why  string
	skip func(rel string, isTest bool) bool // true = rule does not apply to this file
}

var bans = []ban{
	{
		re:   regexp.MustCompile(`\bos\.(Remove|RemoveAll|Rename|Truncate)\(`),
		why:  "only internal/fsop may delete or move files",
		skip: func(rel string, isTest bool) bool { return isTest || strings.HasPrefix(rel, "internal/fsop/") },
	},
	{
		re:   regexp.MustCompile(`\b(syscall|unix)\.(Unlink|Unlinkat|Rmdir|Rename|Renameat|Renameatx_np|RenamexNp|Truncate|Exchangedata)\(`),
		why:  "only internal/fsop may delete or move files",
		skip: func(rel string, isTest bool) bool { return isTest || strings.HasPrefix(rel, "internal/fsop/") },
	},
	{
		re:   regexp.MustCompile(`"(/bin/)?(rm|mv|rmdir|unlink|srm)"`),
		why:  "no shelling out to rm/mv; only internal/fsop may delete or move files",
		skip: func(rel string, isTest bool) bool { return isTest || strings.HasPrefix(rel, "internal/fsop/") },
	},
	{
		re:   regexp.MustCompile(`os\.UserHomeDir\(|Getenv\("HOME"\)|\$HOME`),
		why:  "only internal/env may read the real home folder; use env.Env",
		skip: func(rel string, isTest bool) bool { return !isTest && strings.HasPrefix(rel, "internal/env/") },
	},
	{
		re:   regexp.MustCompile(`env\.FromOS\(`),
		why:  "tests must use env.New(t.TempDir()), never the real environment",
		skip: func(rel string, isTest bool) bool { return !isTest },
	},
	{
		re:   regexp.MustCompile(`"/Users/`),
		why:  "tests must not use real home paths",
		skip: func(rel string, isTest bool) bool { return !isTest },
	},
}

func TestSafetyRules(t *testing.T) {
	root := moduleRoot(t)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			name := d.Name()
			if rel != "." && (strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor" || rel == "internal/guard") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		isTest := strings.HasSuffix(path, "_test.go")
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			for _, b := range bans {
				if b.skip(rel, isTest) || !b.re.MatchString(line) {
					continue
				}
				t.Errorf("%s:%d: %s\n\t%s", rel, i+1, b.why, trimmed)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func moduleRoot(t *testing.T) string {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
