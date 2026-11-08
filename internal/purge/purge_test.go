package purge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// tempRoot returns a fresh folder with symlinks resolved (on macOS /var is a
// link to /private/var, and git writes the real paths).
func tempRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// build creates files under root. A path ending in "/" makes an empty folder.
func build(t *testing.T, root string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		full := filepath.Join(root, p)
		if strings.HasSuffix(p, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func find(t *testing.T, root string, opts Options) []plan.Item {
	t.Helper()
	items, err := Find(context.Background(), env.New(t.TempDir()), []string{root}, opts)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

// found lists "source relative-path" for each item, sorted.
func found(root string, items []plan.Item) []string {
	var out []string
	for _, it := range items {
		rel, _ := filepath.Rel(root, it.Path)
		out = append(out, it.Source+" "+rel)
	}
	slices.Sort(out)
	return out
}

func wantList(t *testing.T, got []string, want ...string) {
	t.Helper()
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("got:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// gitEnv keeps git away from the real user's settings.
func gitEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	args = append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// age sets the mtime of everything under dir, folders last, to when.
func age(t *testing.T, dir string, when time.Time) {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := len(paths) - 1; i >= 0; i-- {
		if err := os.Chtimes(paths[i], when, when); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEmbeddedTypes(t *testing.T) {
	types, err := embeddedTypes()
	if err != nil {
		t.Fatal(err)
	}
	if len(types) < 20 {
		t.Errorf("%d types, want at least 20", len(types))
	}
	byID := map[string]Type{}
	for _, ty := range types {
		byID[ty.ID] = ty
	}
	for _, id := range []string{"cargo", "node", "unity", "stack", "sbt", "maven", "gradle", "cmake", "unreal",
		"jupyter", "python", "pixi", "composer", "dart", "elixir", "swift", "zig", "godot", "dotnet", "terraform", "xcode"} {
		if _, ok := byID[id]; !ok {
			t.Errorf("type %s is missing", id)
		}
	}
	if byID["python-venv"].Risk != rules.RiskModerate {
		t.Errorf("python-venv risk = %q, want moderate", byID["python-venv"].Risk)
	}
	if byID["node"].Risk != rules.RiskSafe {
		t.Errorf("node risk = %q, want safe", byID["node"].Risk)
	}
}

func TestValidate(t *testing.T) {
	ok := Type{ID: "x", Markers: []string{"x.txt"}, Artifacts: []string{"out"}, Risk: rules.RiskSafe, Explain: map[string]string{"en": "text"}}
	if err := validate(ok); err != nil {
		t.Fatalf("valid type rejected: %v", err)
	}

	cases := map[string]func(*Type){
		"bad id":             func(ty *Type) { ty.ID = "Node_JS" },
		"reserved id":        func(ty *Type) { ty.ID = "git-worktree" },
		"no markers":         func(ty *Type) { ty.Markers = nil },
		"empty marker":       func(ty *Type) { ty.Markers = []string{""} },
		"marker with slash":  func(ty *Type) { ty.Markers = []string{"a/b"} },
		"bad marker glob":    func(ty *Type) { ty.Markers = []string{"[a"} },
		"no artifacts":       func(ty *Type) { ty.Artifacts = nil },
		"empty artifact":     func(ty *Type) { ty.Artifacts = []string{""} },
		"artifact path":      func(ty *Type) { ty.Artifacts = []string{"android/build"} },
		"artifact dot":       func(ty *Type) { ty.Artifacts = []string{"."} },
		"artifact dotdot":    func(ty *Type) { ty.Artifacts = []string{".."} },
		"artifact .git":      func(ty *Type) { ty.Artifacts = []string{".git"} },
		"artifact src":       func(ty *Type) { ty.Artifacts = []string{"src"} },
		"artifact SRC":       func(ty *Type) { ty.Artifacts = []string{"SRC"} },
		"risky":              func(ty *Type) { ty.Risk = rules.RiskRisky },
		"no risk":            func(ty *Type) { ty.Risk = "" },
		"no explain":         func(ty *Type) { ty.Explain = nil },
		"blank explain.en":   func(ty *Type) { ty.Explain = map[string]string{"en": "  "} },
		"explain only in id": func(ty *Type) { ty.Explain = map[string]string{"id": "teks"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			ty := ok
			mutate(&ty)
			if err := validate(ty); err == nil {
				t.Errorf("accepted %+v", ty)
			}
		})
	}
}

func TestLoadTypesRejectsBadFiles(t *testing.T) {
	good := "- id: a\n  markers: [a.txt]\n  artifacts: [out]\n  risk: safe\n  explain:\n    en: text\n"
	cases := map[string]fstest.MapFS{
		"unknown field": {"t.yml": {Data: []byte(good + "  extra: 1\n")}},
		"duplicate id":  {"a.yml": {Data: []byte(good)}, "b.yml": {Data: []byte(good)}},
		"invalid type":  {"t.yml": {Data: []byte(strings.Replace(good, "[out]", "[src]", 1))}},
		"not a list":    {"t.yml": {Data: []byte("id: a\n")}},
	}
	for name, fsys := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := loadTypes(fsys); err == nil {
				t.Error("no error")
			}
		})
	}
	if _, err := loadTypes(fstest.MapFS{"t.yml": {Data: []byte(good)}, "README.md": {Data: []byte("not yaml: [")}}); err != nil {
		t.Errorf("good file rejected: %v", err)
	}
}

// TestAllTypes builds one project of every type from the fixture manifest and
// requires exactly the artifact folders in all-types.expected.
func TestAllTypes(t *testing.T) {
	fixtures := filepath.Join("..", "..", "testdata", "fixtures", "purge")
	root := tempRoot(t)
	build(t, root, manifestLines(t, filepath.Join(fixtures, "all-types.tree"))...)

	var wantSources []string
	var want []string
	for _, line := range manifestLines(t, filepath.Join(fixtures, "all-types.expected")) {
		f := strings.Fields(line)
		if len(f) != 3 {
			t.Fatalf("bad expected line %q", line)
		}
		want = append(want, f[0]+" "+f[1]+" "+f[2])
		wantSources = append(wantSources, f[0])
	}

	before := snapshot(t, root)
	items := find(t, root, Options{})
	if after := snapshot(t, root); after != before {
		t.Error("Find changed the tree")
	}

	var got []string
	for _, it := range items {
		rel, _ := filepath.Rel(root, it.Path)
		got = append(got, fmt.Sprintf("%s %s %s", it.Source, rel, it.Risk))
	}
	slices.Sort(got)
	wantList(t, got, want...)

	// every type in rules/purge is covered by the fixture
	types, err := embeddedTypes()
	if err != nil {
		t.Fatal(err)
	}
	for _, ty := range types {
		if !slices.Contains(wantSources, "purge:"+ty.ID) {
			t.Errorf("fixture has no project of type %s", ty.ID)
		}
	}
}

// manifestLines returns the non-empty, non-comment lines of a fixture file.
func manifestLines(t *testing.T, file string) []string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}

// snapshot describes every entry under root, to prove nothing changed.
func snapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s %v %d %d\n", p, fi.Mode(), fi.Size(), fi.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestItemFields(t *testing.T) {
	root := tempRoot(t)
	build(t, root, "app/package.json", "app/node_modules/dep/index.js", "app/.venv/pyvenv.cfg", "app/pyproject.toml")

	e := env.New(t.TempDir())
	e.Lang = "fr" // no French text: falls back to English
	items, err := Find(context.Background(), e, []string{root}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2: %v", len(items), found(root, items))
	}
	for _, it := range items {
		if it.ID != "" || it.Selected {
			t.Errorf("%s: ID %q and Selected %v must be left for engine.Plan", it.Path, it.ID, it.Selected)
		}
		if it.Action != rules.ActionQuarantine || !it.Regenerates || len(it.Command) != 0 {
			t.Errorf("%s: action %q regenerates %v command %v", it.Path, it.Action, it.Regenerates, it.Command)
		}
		if it.Bytes <= 0 {
			t.Errorf("%s: bytes = %d", it.Path, it.Bytes)
		}
		want, err := plan.IdentityOf(it.Path)
		if err != nil || it.Identity != want {
			t.Errorf("%s: identity %+v, want %+v (%v)", it.Path, it.Identity, want, err)
		}
		if !filepath.IsAbs(it.Path) || it.Path != filepath.Clean(it.Path) {
			t.Errorf("path %q is not absolute and clean", it.Path)
		}
		if !strings.HasSuffix(it.Explain, "Project: "+filepath.Join(root, "app")) {
			t.Errorf("%s: explain %q does not end with the project folder", it.Path, it.Explain)
		}
	}
	byName := map[string]plan.Item{}
	for _, it := range items {
		byName[filepath.Base(it.Path)] = it
	}
	if nm := byName["node_modules"]; nm.Source != "purge:node" || nm.Risk != rules.RiskSafe ||
		!strings.HasPrefix(nm.Explain, "Installed npm packages") {
		t.Errorf("node_modules item = %+v", nm)
	}
	if v := byName[".venv"]; v.Source != "purge:python-venv" || v.Risk != rules.RiskModerate {
		t.Errorf(".venv item = %+v", v)
	}
}

func TestNeverSelected(t *testing.T) {
	root := tempRoot(t)
	build(t, root,
		// a project: its sources, src, .git and nested folders are never items
		"proj/package.json", "proj/index.js", "proj/src/app.js", "proj/src/node_modules/x/y.js",
		"proj/sub/.git/pkg/package.json", "proj/sub/.git/pkg/node_modules/x", // .git is never searched
		"proj/lib/node_modules/x", "proj/lib/target/x",
		"proj/dist/bundle.js", "proj/node_modules/dep/index.js",
		// no project here
		"plain/node_modules/x", "plain/target/x", "plain/vendor/x", "plain/build/x", "plain/src/a.txt",
		// a global npm install: its packages are not projects
		"nvm/lib/node_modules/npm/package.json", "nvm/lib/node_modules/npm/node_modules/x/y.js",
	)
	wantList(t, found(root, find(t, root, Options{})), "purge:node proj/node_modules")
}

func TestNesting(t *testing.T) {
	root := tempRoot(t)
	build(t, root,
		"m/package.json", "m/node_modules/dep/package.json", "m/node_modules/dep/node_modules/x",
		"m/packages/a/package.json", "m/packages/a/node_modules/x",
		"m/packages/a/deep/er/b/Cargo.toml", "m/packages/a/deep/er/b/target/x",
		"m/tools/node_modules/pkg/package.json", "m/tools/node_modules/pkg/node_modules/x",
	)
	wantList(t, found(root, find(t, root, Options{})),
		"purge:node m/node_modules",
		"purge:node m/packages/a/node_modules",
		"purge:cargo m/packages/a/deep/er/b/target",
	)
}

func TestSeveralTypesInOneFolder(t *testing.T) {
	root := tempRoot(t)
	build(t, root, "p/Cargo.toml", "p/pom.xml", "p/build.sbt", "p/target/x", "p/package.json", "p/node_modules/x")
	wantList(t, found(root, find(t, root, Options{})), "purge:cargo p/target", "purge:node p/node_modules")
}

func TestSameRootTwice(t *testing.T) {
	root := tempRoot(t)
	build(t, root, "p/Cargo.toml", "p/target/x")
	items, err := Find(context.Background(), env.New(t.TempDir()), []string{root, filepath.Join(root, "p"), root}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	wantList(t, found(root, items), "purge:cargo p/target")
}

func TestSymlinks(t *testing.T) {
	root := tempRoot(t)
	outside := tempRoot(t)
	build(t, outside, "real/index.js", "other/package.json", "other/node_modules/x", "other/.venv/y")
	build(t, root, "proj/package.json", "proj/pyproject.toml", "proj/target-keep/x")
	for link, target := range map[string]string{
		"proj/node_modules": filepath.Join(outside, "real"),  // a symlinked artifact
		"proj/.venv":        filepath.Join(outside, "real"),  // another one
		"proj/linked":       filepath.Join(outside, "other"), // a symlinked folder holding a project
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	if items := find(t, root, Options{}); len(items) != 0 {
		t.Errorf("got %v, want nothing", found(root, items))
	}

	// a root that is a symlink is refused
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Find(context.Background(), env.New(t.TempDir()), []string{link}, Options{}); err == nil {
		t.Error("a symlink root was accepted")
	}
}

func TestBadRoots(t *testing.T) {
	root := tempRoot(t)
	build(t, root, "file.txt")
	for _, r := range []string{filepath.Join(root, "missing"), filepath.Join(root, "file.txt")} {
		if _, err := Find(context.Background(), env.New(t.TempDir()), []string{r}, Options{}); err == nil {
			t.Errorf("root %s was accepted", r)
		}
	}
}

func TestStateFolderIsSkipped(t *testing.T) {
	root := tempRoot(t)
	e := env.New(root) // the state folder is root/.sapu
	build(t, root, "app/package.json", "app/node_modules/x",
		".sapu/quarantine/run1/app/package.json", ".sapu/quarantine/run1/app/node_modules/x")
	items, err := Find(context.Background(), e, []string{root}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	wantList(t, found(root, items), "purge:node app/node_modules")
}

func TestGitTrackedFoldersAreKept(t *testing.T) {
	gitEnv(t)
	root := tempRoot(t)
	build(t, root,
		"repo/composer.json", "repo/vendor/lib.php", // committed vendor
		"repo/package.json", "repo/node_modules/dep/index.js", // untracked
		"repo/pom.xml", "repo/target/out.class",
		"repo/CMakeLists.txt", "repo/build/CMakeCache.txt", "repo/build/script.sh", // partly tracked
	)
	git(t, filepath.Join(root, "repo"), "init")
	git(t, filepath.Join(root, "repo"), "add", "composer.json", "vendor", "package.json", "pom.xml", "CMakeLists.txt", "build/script.sh")
	git(t, filepath.Join(root, "repo"), "commit", "-m", "init")

	wantList(t, found(root, find(t, root, Options{})),
		"purge:node repo/node_modules",
		"purge:maven repo/target",
	)

	// the same happens when only a folder inside the repository is scanned
	sub := filepath.Join(root, "repo")
	wantList(t, found(sub, find(t, sub, Options{})), "purge:node node_modules", "purge:maven target")
}

func TestGitIgnoredFolderIsFound(t *testing.T) {
	gitEnv(t)
	root := tempRoot(t)
	build(t, root, "repo/package.json", "repo/.gitignore", "repo/node_modules/dep/index.js")
	git(t, filepath.Join(root, "repo"), "init")
	if err := os.WriteFile(filepath.Join(root, "repo", ".gitignore"), []byte("node_modules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, filepath.Join(root, "repo"), "add", ".")
	git(t, filepath.Join(root, "repo"), "commit", "-m", "init")
	wantList(t, found(root, find(t, root, Options{})), "purge:node repo/node_modules")
}

func TestGitSubmoduleInVendorIsKept(t *testing.T) {
	gitEnv(t)
	root := tempRoot(t)
	build(t, root, "sub/a.txt", "repo/composer.json")
	git(t, filepath.Join(root, "sub"), "init")
	git(t, filepath.Join(root, "sub"), "add", ".")
	git(t, filepath.Join(root, "sub"), "commit", "-m", "init")
	git(t, filepath.Join(root, "repo"), "init")
	git(t, filepath.Join(root, "repo"), "-c", "protocol.file.allow=always", "submodule", "add", filepath.Join(root, "sub"), "vendor/sub")
	git(t, filepath.Join(root, "repo"), "commit", "-m", "init")
	if items := find(t, filepath.Join(root, "repo"), Options{}); len(items) != 0 {
		t.Errorf("got %v, want nothing", found(root, items))
	}
}

func TestFolderHoldingARepoIsKept(t *testing.T) {
	root := tempRoot(t)
	build(t, root,
		"php/composer.json", "php/vendor/acme/pkg/.git/HEAD", "php/vendor/acme/pkg/a.php",
		"web/package.json", "web/node_modules/@scope/pkg/.git/HEAD",
		"web2/package.json", "web2/node_modules/pkg/.git/HEAD",
		// Found by the second independent review (Nov 2026): a clone deeper
		// than two levels was missed.
		"web4/package.json", "web4/node_modules/a/b/local-repo/.git/HEAD",
		"web5/package.json", "web5/node_modules/a/b/c/d/wt/.git", // a worktree's .git file
		"web3/package.json", "web3/node_modules/pkg/index.js", // no repo: found
	)
	wantList(t, found(root, find(t, root, Options{})), "purge:node web3/node_modules")
}

// Without git, tracked folders cannot be recognised; the doc comment of the
// package says so. Everything else still works.
func TestGitMissing(t *testing.T) {
	gitEnv(t)
	root := tempRoot(t)
	build(t, root, "repo/composer.json", "repo/vendor/lib.php", "repo/package.json", "repo/node_modules/x")
	git(t, filepath.Join(root, "repo"), "init")
	git(t, filepath.Join(root, "repo"), "add", "vendor", "composer.json")
	git(t, filepath.Join(root, "repo"), "commit", "-m", "init")

	t.Setenv("PATH", "")
	wantList(t, found(root, find(t, root, Options{})), "purge:node repo/node_modules", "purge:composer repo/vendor")
}

func TestOlderThan(t *testing.T) {
	gitEnv(t)
	root := tempRoot(t)
	now := time.Now()
	day := 24 * time.Hour
	build(t, root,
		"old/package.json", "old/src/a.js", "old/node_modules/dep/x.js",
		"fresh/package.json", "fresh/src/a.js", "fresh/node_modules/dep/x.js",
		"edited/package.json", "edited/src/deep/a.js", "edited/node_modules/dep/x.js",
		"mono/package.json", "mono/node_modules/x", "mono/packages/a/package.json", "mono/packages/a/src/a.js", "mono/packages/a/node_modules/x",
	)
	git(t, filepath.Join(root, "old"), "init")
	for _, p := range []string{"old", "fresh", "edited", "mono"} {
		age(t, filepath.Join(root, p), now.Add(-90*day))
	}
	// old: the artifact and .git are newer than the sources, which must not count
	touch := func(rel string, when time.Time) {
		t.Helper()
		if err := os.Chtimes(filepath.Join(root, rel), when, when); err != nil {
			t.Fatal(err)
		}
	}
	touch("old/node_modules/dep/x.js", now)
	touch("old/node_modules/dep", now)
	touch("old/node_modules", now)
	touch("old/.git/HEAD", now)
	touch("old/.git", now)
	// fresh and edited: a source file changed 2 days ago, at the top and deep down
	touch("fresh/src/a.js", now.Add(-2*day))
	touch("edited/src/deep/a.js", now.Add(-2*day))
	// mono: only the nested project is active; the whole monorepo counts as active
	touch("mono/packages/a/src/a.js", now.Add(-2*day))
	// folders last, so that no folder mtime is newer than intended
	touch("fresh/src", now.Add(-90*day))
	touch("edited/src/deep", now.Add(-90*day))
	touch("mono/packages/a/src", now.Add(-90*day))

	opts := Options{OlderThan: 30 * day, Now: now}
	wantList(t, found(root, find(t, root, opts)), "purge:node old/node_modules")

	opts.OlderThan = 1 * day
	got := found(root, find(t, root, opts))
	wantList(t, got,
		"purge:node edited/node_modules", "purge:node fresh/node_modules", "purge:node old/node_modules",
		"purge:node mono/node_modules", "purge:node mono/packages/a/node_modules")

	if all := found(root, find(t, root, Options{})); len(all) != 5 {
		t.Errorf("no filter: got %v", all)
	}
}

func TestOlderThanCountsNestedProjectButNotItsArtifacts(t *testing.T) {
	root := tempRoot(t)
	now := time.Now()
	day := 24 * time.Hour
	build(t, root, "mono/package.json", "mono/packages/a/package.json", "mono/packages/a/node_modules/x")
	age(t, filepath.Join(root, "mono"), now.Add(-90*day))
	// the nested artifact is new, its sources are old: the monorepo is old
	touch := now
	for _, rel := range []string{"mono/packages/a/node_modules/x", "mono/packages/a/node_modules"} {
		if err := os.Chtimes(filepath.Join(root, rel), touch, touch); err != nil {
			t.Fatal(err)
		}
	}
	old := now.Add(-90 * day)
	for _, rel := range []string{"mono/packages/a", "mono/packages", "mono"} {
		if err := os.Chtimes(filepath.Join(root, rel), old, old); err != nil {
			t.Fatal(err)
		}
	}
	items := find(t, root, Options{OlderThan: 30 * day, Now: now})
	wantList(t, found(root, items), "purge:node mono/packages/a/node_modules")
}

func TestWorktreeSuggestion(t *testing.T) {
	gitEnv(t)
	root := tempRoot(t)
	build(t, root, "main/package.json", "main/index.js", "main/node_modules/x")
	main := filepath.Join(root, "main")
	git(t, main, "init")
	git(t, main, "add", "package.json", "index.js")
	git(t, main, "commit", "-m", "init")
	wt := filepath.Join(root, "agents", "wt")
	git(t, main, "worktree", "add", "-b", "feature", wt)
	build(t, root, "agents/wt/node_modules/dep/x.js")

	items := find(t, root, Options{})
	wantList(t, found(root, items),
		"purge:node main/node_modules",
		"purge:node agents/wt/node_modules",
		"purge:git-worktree agents/wt",
	)
	for _, it := range items {
		if it.Source != "purge:git-worktree" {
			continue
		}
		wantCmd := []string{"git", "-C", main, "worktree", "remove", wt}
		if !slices.Equal(it.Command, wantCmd) {
			t.Errorf("command = %v, want %v", it.Command, wantCmd)
		}
		if it.Action != rules.ActionOfficialCommand || it.Risk != rules.RiskModerate || it.Regenerates {
			t.Errorf("action %q risk %q regenerates %v", it.Action, it.Risk, it.Regenerates)
		}
		if it.Path != wt || it.Bytes <= 0 || it.Identity.Ino == 0 {
			t.Errorf("path %q bytes %d identity %+v", it.Path, it.Bytes, it.Identity)
		}
		if !strings.Contains(it.Explain, "uncommitted") || !strings.Contains(it.Explain, wt) {
			t.Errorf("explain %q", it.Explain)
		}
	}

	// OlderThan applies to the suggestion: the worktree is new, the main repo is old
	now := time.Now()
	age(t, main, now.Add(-90*24*time.Hour))
	age(t, filepath.Join(root, "agents"), now)
	got := found(root, find(t, root, Options{OlderThan: 30 * 24 * time.Hour, Now: now}))
	wantList(t, got, "purge:node main/node_modules")

	age(t, filepath.Join(root, "agents"), now.Add(-90*24*time.Hour))
	got = found(root, find(t, root, Options{OlderThan: 30 * 24 * time.Hour, Now: now}))
	wantList(t, got, "purge:node main/node_modules", "purge:node agents/wt/node_modules", "purge:git-worktree agents/wt")
}

func TestOnlyLinkedWorktreesGetASuggestion(t *testing.T) {
	root := tempRoot(t)
	build(t, root,
		"sub/.git",   // a submodule: gitdir points into "modules"
		"gone/.git",  // a worktree whose gitdir no longer exists
		"plain/.git", // not a gitdir line
		"repo/.git/HEAD",
	)
	write := func(rel, content string) {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	build(t, root, "repo/.git/modules/sub/HEAD", "repo/.git/worktrees/real/HEAD")
	write("sub/.git", "gitdir: ../repo/.git/modules/sub\n")
	write("gone/.git", "gitdir: "+filepath.Join(root, "repo/.git/worktrees/gone")+"\n")
	write("plain/.git", "hello\n")
	if items := find(t, root, Options{}); len(items) != 0 {
		t.Errorf("got %v, want nothing", found(root, items))
	}

	// the same file for a folder whose gitdir exists is a worktree, relative path or not
	build(t, root, "rel/x")
	write("rel/.git", "gitdir: ../repo/.git/worktrees/real\n")
	items := find(t, root, Options{})
	wantList(t, found(root, items), "purge:git-worktree rel")
	if want := []string{"git", "-C", filepath.Join(root, "repo"), "worktree", "remove", filepath.Join(root, "rel")}; !slices.Equal(items[0].Command, want) {
		t.Errorf("command = %v, want %v", items[0].Command, want)
	}
}

func TestCancel(t *testing.T) {
	root := tempRoot(t)
	for i := range 300 {
		build(t, root, fmt.Sprintf("d%03d/package.json", i), fmt.Sprintf("d%03d/node_modules/x", i), fmt.Sprintf("d%03d/a/b/c.txt", i))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	items, err := Find(ctx, env.New(t.TempDir()), []string{root}, Options{})
	if !errors.Is(err, context.Canceled) || items != nil {
		t.Errorf("cancelled before the start: items %d, err %v", len(items), err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	time.AfterFunc(5*time.Millisecond, cancel)
	items, err = Find(ctx, env.New(t.TempDir()), []string{root}, Options{})
	if err != nil && (!errors.Is(err, context.Canceled) || items != nil) {
		t.Errorf("cancelled while running: items %d, err %v", len(items), err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v", d)
	}
}
