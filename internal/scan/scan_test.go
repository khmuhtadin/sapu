package scan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/walk"
)

func write(t *testing.T, home, rel string, size int) {
	t.Helper()
	path := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rule(id string, paths ...string) rules.Rule {
	regen := true
	return rules.Rule{
		ID:          id,
		Category:    rules.CategoryApps,
		Paths:       paths,
		Risk:        rules.RiskSafe,
		Regenerates: &regen,
		Explain:     map[string]string{"en": "English text", "id": "Teks Indonesia"},
		Action:      rules.ActionQuarantine,
		Fixture:     "x",
	}
}

// relPaths returns the item paths relative to home, sorted.
func relPaths(t *testing.T, home string, rs []rules.Rule, e env.Env) []string {
	t.Helper()
	items, err := Scan(context.Background(), e, rs, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, it := range items {
		rel, err := filepath.Rel(home, it.Path)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestScanPathForms(t *testing.T) {
	home := t.TempDir()
	e := env.New(home)
	write(t, home, "Library/Caches/Foo/a/data", 100)
	write(t, home, "Library/Caches/Foo/b.txt", 100)
	write(t, home, "Library/Caches/Bar/c", 100)
	write(t, home, "Library/Caches/Foo2/d", 100)
	write(t, home, "Library/App/p1/Cache/x", 100)
	write(t, home, "Library/App/p2/Cache/y", 100)
	write(t, home, "Library/App/p3/Other/z", 100)
	write(t, home, ".tool/store", 100)

	tests := []struct {
		name string
		path string
		want []string
	}{
		{"trailing double star gives the children", "~/Library/Caches/Foo/**",
			[]string{"Library/Caches/Foo/a", "Library/Caches/Foo/b.txt"}},
		{"plain folder is one item", "~/Library/Caches/Foo",
			[]string{"Library/Caches/Foo"}},
		{"plain file is one item", "~/.tool/store",
			[]string{".tool/store"}},
		{"star in the middle", "~/Library/App/*/Cache",
			[]string{"Library/App/p1/Cache", "Library/App/p2/Cache"}},
		{"star at the end", "~/Library/Caches/Foo*",
			[]string{"Library/Caches/Foo", "Library/Caches/Foo2"}},
		{"star prefix then double star", "~/Library/App/*/Cache/**",
			[]string{"Library/App/p1/Cache/x", "Library/App/p2/Cache/y"}},
		{"question mark", "~/Library/Caches/Ba?",
			[]string{"Library/Caches/Bar"}},
		{"nothing matches", "~/Library/Caches/Nope/**", nil},
		{"folder missing", "~/Library/Nope/Cache", nil},
		{"double star on a file gives nothing", "~/.tool/store/**", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := relPaths(t, home, []rules.Rule{rule("r", tt.path)}, e)
			if !equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestScanExclude(t *testing.T) {
	home := t.TempDir()
	e := env.New(home)
	write(t, home, "Library/Caches/Foo/a", 100)
	write(t, home, "Library/Caches/Foo/keep", 100)
	write(t, home, "Library/Caches/Foo/keep-too", 100)
	write(t, home, "Library/Caches/Foo/sub/deep/file", 100)

	tests := []struct {
		name    string
		exclude []string
		want    []string
	}{
		{"exact path", []string{"~/Library/Caches/Foo/keep"},
			[]string{"Library/Caches/Foo/a", "Library/Caches/Foo/keep-too", "Library/Caches/Foo/sub"}},
		{"glob", []string{"~/Library/Caches/Foo/keep*"},
			[]string{"Library/Caches/Foo/a", "Library/Caches/Foo/sub"}},
		{"children of a folder", []string{"~/Library/Caches/Foo/**"}, nil},
		{"exclude inside an item drops the whole item", []string{"~/Library/Caches/Foo/sub/deep/file"},
			[]string{"Library/Caches/Foo/a", "Library/Caches/Foo/keep", "Library/Caches/Foo/keep-too"}},
		{"glob inside an item drops the whole item", []string{"~/Library/Caches/Foo/sub/*/file"},
			[]string{"Library/Caches/Foo/a", "Library/Caches/Foo/keep", "Library/Caches/Foo/keep-too"}},
		{"exclude elsewhere changes nothing", []string{"~/Library/Caches/Other/**"},
			[]string{"Library/Caches/Foo/a", "Library/Caches/Foo/keep", "Library/Caches/Foo/keep-too", "Library/Caches/Foo/sub"}},
		{"sibling with the same prefix is not inside", []string{"~/Library/Caches/Foo/su"},
			[]string{"Library/Caches/Foo/a", "Library/Caches/Foo/keep", "Library/Caches/Foo/keep-too", "Library/Caches/Foo/sub"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := rule("r", "~/Library/Caches/Foo/**")
			r.Exclude = tt.exclude
			got := relPaths(t, home, []rules.Rule{r}, e)
			if !equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestScanDeduplicatesWithinARule(t *testing.T) {
	home := t.TempDir()
	e := env.New(home)
	write(t, home, "Library/Caches/Foo/a", 100)

	r := rule("r", "~/Library/Caches/Foo/**", "~/Library/Caches/Foo/*", "~/Library/Caches/Foo/a")
	got := relPaths(t, home, []rules.Rule{r}, e)
	if want := []string{"Library/Caches/Foo/a"}; !equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// The same path from two different rules stays two items; engine.Plan merges them.
	got = relPaths(t, home, []rules.Rule{rule("one", "~/Library/Caches/Foo/a"), rule("two", "~/Library/Caches/Foo/a")}, e)
	if len(got) != 2 {
		t.Errorf("got %v, want one item per rule", got)
	}
}

func TestScanItemFields(t *testing.T) {
	home := t.TempDir()
	e := env.New(home)
	write(t, home, "Library/Caches/Foo/data", 10000)

	r := rule("foo.cache", "~/Library/Caches/Foo")
	r.Risk = rules.RiskModerate
	f := false
	r.Regenerates = &f
	r.Action = rules.ActionOfficialCommand
	r.Command = []string{"foo", "clean"}
	r.ProtectIf = []rules.Condition{{ProcessRunning: "Foo"}}

	items, err := Scan(context.Background(), e, []rules.Rule{r}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	it := items[0]
	want, _, err := walk.Size(context.Background(), filepath.Join(home, "Library/Caches/Foo"), nil)
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name string
		ok   bool
	}{
		{"source", it.Source == "foo.cache"},
		{"path", it.Path == filepath.Join(home, "Library/Caches/Foo")},
		{"bytes", it.Bytes == want && it.Bytes > 0},
		{"risk", it.Risk == rules.RiskModerate},
		{"regenerates", !it.Regenerates},
		{"explain", it.Explain == "English text"},
		{"action", it.Action == rules.ActionOfficialCommand},
		{"command", len(it.Command) == 2 && it.Command[0] == "foo"},
		{"protect_if", len(it.ProtectIf) == 1 && it.ProtectIf[0].ProcessRunning == "Foo"},
		{"identity", it.Identity.Ino != 0 && it.Identity.Mode != 0},
		{"id empty", it.ID == ""},
		{"not selected", !it.Selected},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("wrong %s: %+v", c.name, it)
		}
	}
}

func TestScanExplainLanguage(t *testing.T) {
	home := t.TempDir()
	write(t, home, "Library/Caches/Foo/data", 100)
	rs := []rules.Rule{rule("r", "~/Library/Caches/Foo")}

	for lang, want := range map[string]string{"id": "Teks Indonesia", "en": "English text", "de": "English text"} {
		e := env.New(home)
		e.Lang = lang
		items, err := Scan(context.Background(), e, rs, nil)
		if err != nil || len(items) != 1 {
			t.Fatalf("lang %s: %v, %d items", lang, err, len(items))
		}
		if items[0].Explain != want {
			t.Errorf("lang %s: explain = %q, want %q", lang, items[0].Explain, want)
		}
	}
}

func TestScanSymlinksAreItemsNotFollowed(t *testing.T) {
	home := t.TempDir()
	e := env.New(home)
	outside := t.TempDir()
	write(t, outside, "big/file", 1<<20)
	write(t, home, "Library/Caches/Foo/real/data", 100)
	if err := os.Symlink(filepath.Join(outside, "big"), filepath.Join(home, "Library/Caches/Foo/link")); err != nil {
		t.Fatal(err)
	}
	// A folder that is itself a symlink is not opened for its children.
	if err := os.Symlink(filepath.Join(outside, "big"), filepath.Join(home, "Library/Caches/Bar")); err != nil {
		t.Fatal(err)
	}

	rs := []rules.Rule{rule("children", "~/Library/Caches/Foo/**"), rule("linked", "~/Library/Caches/Bar/**"), rule("direct", "~/Library/Caches/Bar")}
	items, err := Scan(context.Background(), e, rs, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, it := range items {
		got[it.Source+" "+mustRel(t, home, it.Path)] = it.Bytes
	}
	if len(got) != 3 {
		t.Fatalf("got %v, want the real folder, the link inside it, and the link itself", got)
	}
	for key, bytes := range got {
		if key == "children Library/Caches/Foo/real" {
			continue
		}
		if bytes >= 1<<20 {
			t.Errorf("%s measured %d bytes: the link was followed", key, bytes)
		}
	}
}

func mustRel(t *testing.T, home, path string) string {
	t.Helper()
	rel, err := filepath.Rel(home, path)
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

func TestScanMinMacOS(t *testing.T) {
	home := t.TempDir()
	e := env.New(home)
	write(t, home, "Library/Caches/Foo/data", 100)

	future := rule("future", "~/Library/Caches/Foo")
	future.MinMacOS = "999"
	past := rule("past", "~/Library/Caches/Foo")
	past.MinMacOS = "10.0"

	items, err := Scan(context.Background(), e, []rules.Rule{future, past}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Source != "past" {
		t.Errorf("got %+v, want only the rule for an older macOS", items)
	}
}

func TestVersionBefore(t *testing.T) {
	tests := []struct {
		have, need string
		before     bool
	}{
		{"15.1", "15", false},
		{"15.1", "15.1", false},
		{"15.1", "15.2", true},
		{"15.1.1", "15.1", false},
		{"14.7.2", "15", true},
		{"26.0", "15.9", false},
		{"9.9", "10", true},
		{"15", "15.0", false},
	}
	for _, tt := range tests {
		got, err := versionBefore(tt.have, tt.need)
		if err != nil || got != tt.before {
			t.Errorf("versionBefore(%q, %q) = %v, %v; want %v", tt.have, tt.need, got, err, tt.before)
		}
	}
	for _, bad := range []string{"", "x", "15.x", "1.2.3.4"} {
		if _, err := versionBefore(bad, "15"); err == nil {
			t.Errorf("versionBefore(%q) should fail", bad)
		}
	}
}

func TestScanHomeNameWithWildcardCharacters(t *testing.T) {
	// A home folder called "h[o]me*" must be matched literally.
	base := t.TempDir()
	home := filepath.Join(base, "h[o]me*")
	write(t, home, "Library/Caches/Foo/data", 100)
	write(t, base, "hxme/Library/Caches/Foo/data", 100) // would match if the name were a glob

	got := relPaths(t, home, []rules.Rule{rule("r", "~/Library/Caches/Foo", "~/Library/Caches/Foo/**")}, env.New(home))
	want := []string{"Library/Caches/Foo", "Library/Caches/Foo/data"}
	if !equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestScanCancel(t *testing.T) {
	home := t.TempDir()
	write(t, home, "Library/Caches/Foo/data", 100)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	items, err := Scan(ctx, env.New(home), []rules.Rule{rule("r", "~/Library/Caches/Foo")}, nil)
	if !errors.Is(err, context.Canceled) || items != nil {
		t.Errorf("got %v, %v; want nil and context.Canceled", items, err)
	}
}

func TestScanProgressTotals(t *testing.T) {
	home := t.TempDir()
	write(t, home, "Library/Caches/Foo/a", 5000)
	write(t, home, "Library/Caches/Foo/b", 5000)

	var last walk.Event
	progress := func(ev walk.Event) { last = ev }
	items, err := Scan(context.Background(), env.New(home), []rules.Rule{rule("r", "~/Library/Caches/Foo/**")}, progress)
	if err != nil {
		t.Fatal(err)
	}
	var sum int64
	for _, it := range items {
		sum += it.Bytes
	}
	if last.Files != 2 || last.Bytes != sum {
		t.Errorf("final progress = %+v, want 2 files and %d bytes", last, sum)
	}
}

func TestScanNeverWrites(t *testing.T) {
	home := t.TempDir()
	write(t, home, "Library/Caches/Foo/a", 100)
	write(t, home, "Library/Caches/Foo/sub/b", 100)
	before := listing(t, home)

	if _, err := Scan(context.Background(), env.New(home), []rules.Rule{rule("r", "~/Library/Caches/Foo/**", "~/Library/Caches/Foo")}, nil); err != nil {
		t.Fatal(err)
	}
	after := listing(t, home)
	if !equal(before, after) {
		t.Errorf("scan changed the tree:\nbefore %v\nafter  %v", before, after)
	}
}

// listing returns every path under dir with its size and modification time.
func listing(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, path+" "+fi.ModTime().String()+" "+fi.Mode().String())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestScanSkipsEmptyItems(t *testing.T) {
	home := t.TempDir()
	e := env.New(home)
	if err := os.MkdirAll(filepath.Join(home, "Library/Caches/App/GPUCache"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, home, "Library/Caches/App/Other/data", 100)

	got := relPaths(t, home, []rules.Rule{rule("r", "~/Library/Caches/App/**")}, e)
	want := []string{"Library/Caches/App/Other"}
	if !equal(got, want) {
		t.Errorf("items = %v, want %v (the empty GPUCache folder is noise)", got, want)
	}
}
