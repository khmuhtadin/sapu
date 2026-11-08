package scan

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/rules"
	ruledata "github.com/khmuhtadin/sapu/rules"
)

// TestSeedRulesMatchTheirFixtures runs every seed rule alone against its fake
// home under testdata/fixtures/rules and requires exactly the paths listed in
// expect.txt. The fake homes also hold decoys (siblings, protected
// neighbours) that must not match.
func TestSeedRulesMatchTheirFixtures(t *testing.T) {
	cleaners, err := fs.Sub(ruledata.FS, "cleaners")
	if err != nil {
		t.Fatal(err)
	}
	all, err := rules.Load(cleaners)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "rules"))
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range all {
		t.Run(r.ID, func(t *testing.T) {
			dir := filepath.Join(root, r.Fixture)
			home := filepath.Join(dir, "home")
			want := readExpect(t, filepath.Join(dir, "expect.txt"))

			items, err := Scan(context.Background(), env.New(home), []rules.Rule{r}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, it := range items {
				rel, err := filepath.Rel(home, it.Path)
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, rel)
				if it.Bytes <= 0 {
					t.Errorf("%s has %d bytes", rel, it.Bytes)
				}
				if it.Source != r.ID || it.Explain == "" {
					t.Errorf("%s: bad source or explain: %+v", rel, it)
				}
			}
			sort.Strings(got)
			if !equal(got, want) {
				t.Errorf("items differ\n got: %s\nwant: %s", strings.Join(got, "\n      "), strings.Join(want, "\n      "))
			}
		})
	}
}

func readExpect(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatalf("%s lists no paths", path)
	}
	return out
}
