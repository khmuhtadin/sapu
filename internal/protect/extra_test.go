package protect

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"gopkg.in/yaml.v3"

	"github.com/khmuhtadin/sapu/internal/env"
	ruledata "github.com/khmuhtadin/sapu/rules"
)

// hostileFiles is a rule update's protection list that leaves out every other
// built-in entry, and narrows and opens up the rest under the same ids.
func hostileFiles(t *testing.T, builtIn []Entry) fs.FS {
	t.Helper()
	var list []Entry
	for i, en := range builtIn {
		if i%2 == 0 {
			continue // left out
		}
		narrowed := Entry{ID: en.ID, Reason: map[string]string{"en": "narrowed by an update"}}
		for _, p := range en.Paths {
			narrowed.Paths = append(narrowed.Paths, p+"/sapu-nothing-here")
		}
		// Exceptions for the built-in paths, everything inside them, and
		// the narrowed paths, for every source the test checks. An allow
		// may only name a dedicated command's source (rules.ReservedSources;
		// "hostile" or a rule id is refused by validate), so the hostile
		// sources here are dedicated ones other than backups and messages.
		for _, src := range []string{"trash", "backups", "messages", "large"} {
			a := Allow{Source: src}
			for _, p := range en.Paths {
				a.Paths = append(a.Paths, p, p+"/*", p+"/*/*", p+"/sapu-nothing-here")
			}
			narrowed.Allow = append(narrowed.Allow, a)
		}
		list = append(list, narrowed)
	}
	list = append(list, Entry{ID: "added-by-update", Paths: []string{"~/Library/Caches/com.example.precious"},
		Reason: map[string]string{"en": "Added by an update"}})
	data, err := yaml.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	return fstest.MapFS{"core.yml": {Data: data}}
}

func builtInEntries(t *testing.T) []Entry {
	t.Helper()
	sub, err := fs.Sub(ruledata.FS, "protect")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := loadEntries(sub)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// testPaths are paths at, inside and around every built-in entry.
func testPaths(e env.Env, entries []Entry) []string {
	var out []string
	for _, en := range entries {
		for _, p := range en.Paths {
			p = e.Expand(strings.NewReplacer("*", "x", "?", "x").Replace(p))
			out = append(out, p, filepath.Join(p, "data.db"), filepath.Join(p, "sapu-nothing-here"), filepath.Dir(p))
		}
	}
	return out
}

// With a rule update that leaves out, narrows and opens up built-in entries,
// every path the built-in list blocks for a source is still blocked for it.
// The update's own entry and the whitelist paths of sapu.toml block too.
func TestExtraOnlyAdds(t *testing.T) {
	e := env.New(t.TempDir())
	builtIn := builtInEntries(t)

	old := Extra
	t.Cleanup(func() { Extra = old })
	Extra = nil
	before, err := Load(e)
	if err != nil {
		t.Fatal(err)
	}
	Extra = func(env.Env) (fs.FS, []string, error) {
		return hostileFiles(t, builtIn), []string{"~/Library/Caches/com.example.from-config"}, nil
	}
	after, err := Load(e)
	if err != nil {
		t.Fatal(err)
	}

	checked := 0
	for _, p := range testPaths(e, builtIn) {
		for _, src := range []string{"", "hostile", "trash", "backups", "messages", "large", "clean", "purge"} {
			was, why := before.CheckSource(p, src)
			if !was {
				continue
			}
			checked++
			if now, _ := after.CheckSource(p, src); !now {
				t.Errorf("%s (source %q) was blocked as %s; with the update it is not", p, src, why)
			}
		}
	}
	if checked < 100 {
		t.Fatalf("only %d blocked path/source pairs checked", checked)
	}

	for _, en := range builtIn {
		if got := after.Reason(en.ID); got != en.Reason["en"] {
			t.Errorf("Reason(%s) = %q, want the built-in reason", en.ID, got)
		}
	}
	if ok, why := after.Check(e.Expand("~/Library/Caches/com.example.precious/x")); !ok || why != "protected:added-by-update" {
		t.Errorf("the update's entry: %v %q", ok, why)
	}
	if ok, why := after.Check(e.Expand("~/Library/Caches/com.example.from-config")); !ok || !strings.HasPrefix(why, "whitelisted:") {
		t.Errorf("the sapu.toml whitelist path: %v %q", ok, why)
	}
	if ok, _ := after.Check(e.Expand("~/Library/Caches/com.example.junk")); ok {
		t.Error("an ordinary cache is blocked")
	}
}

func TestExtraErrorsStopLoad(t *testing.T) {
	e := env.New(t.TempDir())
	old := Extra
	t.Cleanup(func() { Extra = old })

	Extra = func(env.Env) (fs.FS, []string, error) {
		return nil, nil, fs.ErrPermission
	}
	if _, err := Load(e); err == nil {
		t.Error("Load ignored an error of Extra")
	}
	Extra = func(env.Env) (fs.FS, []string, error) {
		return fstest.MapFS{"x.yml": {Data: []byte("- id: x\n  paths: [/etc]\n  reason: {en: x}\n")}}, nil, nil
	}
	if _, err := Load(e); err == nil {
		t.Error("Load took an invalid protection file")
	}
	Extra = func(env.Env) (fs.FS, []string, error) { return nil, []string{"relative/path"}, nil }
	if _, err := Load(e); err == nil {
		t.Error("Load took an invalid whitelist path")
	}
}
