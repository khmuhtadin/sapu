package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/protect"
)

func writeSettings(t *testing.T, e env.Env, text string) string {
	t.Helper()
	path := filepath.Join(e.Home, ".config", "sapu", "sapu.toml")
	writeFile(t, path, text)
	return path
}

// The fake home holds 3 safe items and 1 moderate item (see junk), and the
// installed test rules add a risky one.
func TestProfiles(t *testing.T) {
	e := newHome(t)
	installTestRules(t, e)

	for _, tc := range []struct {
		name     string
		settings string
		args     []string
		want     int
	}{
		{"default is standard", "", nil, 4},
		{"conservative", "", []string{"--profile", "conservative"}, 3},
		{"standard", "", []string{"--profile", "standard"}, 4},
		{"aggressive", "", []string{"--profile", "aggressive"}, 5},
		{"--risk wins over --profile", "", []string{"--profile", "aggressive", "--risk", "moderate"}, 4},
		{"--risk wins the other way", "", []string{"--risk", "risky", "--profile", "conservative"}, 5},
		{"sapu.toml profile", `profile = "aggressive"`, nil, 5},
		{"--profile wins over sapu.toml", `profile = "aggressive"`, []string{"--profile", "conservative"}, 3},
		{"--risk wins over sapu.toml", `profile = "aggressive"`, []string{"--risk", "safe"}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeSettings(t, e, tc.settings)
			if got := summaryOf(t, e, tc.args...); got.SelectedItems != tc.want {
				t.Errorf("selected %d items, want %d", got.SelectedItems, tc.want)
			}
		})
	}
	writeSettings(t, e, "")
	if _, errOut, code := sapu(t, e, "", "clean", "--yes", "--profile", "yolo"); code != 2 || !strings.Contains(errOut, "--profile must be") {
		t.Errorf("--profile yolo: exit %d\n%s", code, errOut)
	}
}

// aggressive only pre-selects: the confirmation still lists every item, the
// risky one among them, and nothing moves without a yes.
func TestAggressiveStillAsks(t *testing.T) {
	e := newHome(t)
	installTestRules(t, e)
	before := snapshot(t, e)
	out, _, code := sapu(t, e, "n\n", "clean", "--profile", "aggressive")
	if code != 2 {
		t.Errorf("exit %d, want 2 (not confirmed)", code)
	}
	if !strings.Contains(out, "com.example.risky") || !strings.Contains(out, "npm") {
		t.Errorf("the confirmation does not list everything:\n%s", out)
	}
	sameSnapshot(t, before, snapshot(t, e))
}

func TestReviewScreenStartsWithTheProfile(t *testing.T) {
	e := newHome(t)
	installTestRules(t, e)
	writeSettings(t, e, `profile = "aggressive"`)
	r, err := maxRiskOf("", "", mustConfig(t, e))
	if err != nil || r != "risky" {
		t.Errorf("maxRiskOf with an aggressive sapu.toml = %q, %v", r, err)
	}
	p := &plan.Plan{Items: []plan.Item{{Risk: "safe"}, {Risk: "moderate"}, {Risk: "risky"}}}
	selectItems(p, r, nil, nil, nil)
	for _, it := range p.Items {
		if !it.Selected {
			t.Errorf("%s item not selected at the start", it.Risk)
		}
	}
}

func mustConfig(t *testing.T, e env.Env) config.Config {
	t.Helper()
	c, err := config.Load(e)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestConfigExclude(t *testing.T) {
	e := newHome(t)
	writeSettings(t, e, "[clean]\nexclude = [\"npm.cache\", \"logs\"]\n")
	if got := summaryOf(t, e); got.SelectedItems != 2 {
		t.Errorf("selected %d items, want 2 (npm.cache and logs excluded)", got.SelectedItems)
	}
	// The flag adds to the file.
	if got := summaryOf(t, e, "--exclude", "typescript.cache"); got.SelectedItems != 1 {
		t.Errorf("selected %d items, want 1", got.SelectedItems)
	}

	path := writeSettings(t, e, "[clean]\nexclude = [\"npm.caches\"]\n")
	_, errOut, code := sapu(t, e, "", "clean", "--plan", filepath.Join(t.TempDir(), "p.json"))
	if code != 1 || !strings.Contains(errOut, path) || !strings.Contains(errOut, `"npm.caches"`) {
		t.Errorf("unknown exclude name: exit %d\n%s", code, errOut)
	}
}

func TestConfigWhitelistBlocksItems(t *testing.T) {
	e := newHome(t)
	writeSettings(t, e, "[whitelist]\npaths = [\"~/Library/Caches/node-gyp\"]\n")
	p := readPlanFile(t, planFileOf(t, e))
	found := false
	for _, s := range p.Skipped {
		if s.Path == filepath.Join(e.Home, "Library/Caches/node-gyp") && s.Reason == plan.ReasonWhitelisted {
			found = true
		}
	}
	if !found {
		t.Errorf("node-gyp is not skipped as whitelisted: %+v", p.Skipped)
	}
	if slices.Contains(selectedPaths(p), filepath.Join(e.Home, "Library/Caches/node-gyp")) {
		t.Error("a whitelisted item is selected")
	}

	// Apply checks it again: a plan made before the path was whitelisted
	// does not move it.
	writeSettings(t, e, "")
	file := planFileOf(t, e)
	writeSettings(t, e, "[whitelist]\npaths = [\"~/Library/Caches/node-gyp\"]\n")
	if _, errOut, code := sapu(t, e, "", "apply", file, "--yes"); code != 3 {
		t.Errorf("apply: exit %d, want 3 (one skipped)\n%s", code, errOut)
	}
	if !exists(filepath.Join(e.Home, "Library/Caches/node-gyp/headers.tar")) {
		t.Error("apply moved a path whitelisted in sapu.toml")
	}
	// sapu whitelist never writes sapu.toml.
	if entries, err := protect.ReadWhitelist(e); err != nil || len(entries) != 0 {
		t.Errorf("whitelist.txt = %v, %v", entries, err)
	}
}

// [quarantine] reaches the engine: expires_after uses max_age, and expiry
// at the start of the next apply deletes a run older than max_age.
func TestConfigQuarantineReachesApply(t *testing.T) {
	e := newHome(t)
	writeSettings(t, e, "[quarantine]\nmax_age = \"2d\"\nmax_size = \"5GB\"\n")

	start := time.Now()
	out, errOut, code := sapu(t, e, "", "clean", "--yes", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	var first runData
	decodeData(t, out, &first)
	if first.ExpiresAfter == nil || first.ExpiresAfter.Before(start.Add(47*time.Hour)) || first.ExpiresAfter.After(time.Now().Add(49*time.Hour)) {
		t.Errorf("expires_after = %v, want about 2 days from now", first.ExpiresAfter)
	}

	// Three days later the run is past max_age (2d) but not past the
	// default (7d).
	ageRun(t, e, first.Result.RunID, 3*24*time.Hour)
	writeFile(t, filepath.Join(e.Home, "Library/Caches/node-gyp/headers.tar"), "node headers again")
	out, errOut, code = sapu(t, e, "", "clean", "--yes", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	var second runData
	decodeData(t, out, &second)
	if second.Result.Freed == 0 {
		t.Error("the 3-day-old run was not expired with max_age = 2d")
	}
	if exists(filepath.Join(e.QuarantineDir(), first.Result.RunID)) {
		t.Error("the expired run's quarantine is still there")
	}
}

func TestBadConfigStopsCleanBeforeAnything(t *testing.T) {
	e := newHome(t)
	path := writeSettings(t, e, "profile = \"standard\"\n[quarantine]\nmax_age = \"12h\"\n")
	before := snapshot(t, e)
	file := filepath.Join(t.TempDir(), "plan.json")
	for _, args := range [][]string{{"clean", "--plan", file}, {"clean", "--yes"}, {"purge", e.Home, "--yes"}} {
		_, errOut, code := sapu(t, e, "", args...)
		if code != 1 || !strings.Contains(errOut, path+": line 3: quarantine.max_age") {
			t.Errorf("%v: exit %d\n%s", args, code, errOut)
		}
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
	if exists(file) {
		t.Error("a plan was written")
	}
}

func TestPurgeSettings(t *testing.T) {
	e := env.New(t.TempDir())
	code := filepath.Join(e.Home, "code")
	for rel, data := range map[string]string{
		"web/package.json":                   `{"name":"web"}`,
		"web/node_modules/left-pad/index.js": "module.exports = 1",
		"game/Assembly-CSharp.csproj":        "<Project/>",
		"game/Library/cache.bin":             "unity cache",
		"api/Cargo.toml":                     "[package]",
		"api/target/debug/api":               "binary",
	} {
		writeFile(t, filepath.Join(code, rel), data)
	}
	old := time.Now().Add(-60 * 24 * time.Hour)
	for _, project := range []string{"web", "game"} {
		err := filepath.WalkDir(filepath.Join(code, project), func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			return os.Chtimes(p, old, old)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	writeSettings(t, e, "profile = \"conservative\"\n[purge]\nroots = [\"~/code\"]\nolder_than = \"30d\"\n")

	plan := func(args ...string) (selected, listed []string) {
		t.Helper()
		file := filepath.Join(t.TempDir(), "plan.json")
		if out, errOut, exit := sapu(t, e, "", append([]string{"purge", "--plan", file}, args...)...); exit != 0 {
			t.Fatalf("exit %d\n%s\n%s", exit, out, errOut)
		}
		p := readPlanFile(t, file)
		for _, it := range p.Items {
			rel, _ := filepath.Rel(code, it.Path)
			listed = append(listed, rel)
		}
		for _, s := range selectedPaths(p) {
			rel, _ := filepath.Rel(code, s)
			selected = append(selected, rel)
		}
		return selected, listed
	}

	// No folder given: ~/code from sapu.toml; api is too recent; the
	// conservative profile leaves the moderate unity folder unselected.
	selected, listed := plan()
	if strings.Join(selected, ",") != "web/node_modules" || strings.Join(listed, ",") != "game/Library,web/node_modules" {
		t.Errorf("selected %v of %v", selected, listed)
	}
	// Flags win over the file.
	selected, _ = plan("--profile", "standard", "--older-than", "0d")
	if strings.Join(selected, ",") != "api/target,game/Library,web/node_modules" {
		t.Errorf("with flags: selected %v", selected)
	}
	// A folder argument wins over roots.
	selected, _ = plan(filepath.Join(code, "web"))
	if strings.Join(selected, ",") != "web/node_modules" {
		t.Errorf("with a folder: selected %v", selected)
	}
}

func TestConfigCommands(t *testing.T) {
	e := env.New(t.TempDir())
	file := filepath.Join(e.Home, ".config", "sapu", "sapu.toml")

	out, _, code := sapu(t, e, "", "config", "path")
	if code != 0 || strings.TrimSpace(out) != file {
		t.Errorf("config path: exit %d %q", code, out)
	}
	out, _, _ = sapu(t, e, "", "config", "path", "--json")
	validate(t, "config-path", out)

	out, errOut, code := sapu(t, e, "", "config", "show", "--json")
	if code != 0 {
		t.Fatalf("config show: exit %d\n%s", code, errOut)
	}
	validate(t, "config-show", out)
	var d configData
	decodeData(t, out, &d)
	if d.Found || d.Profile != "standard" || d.Quarantine.MaxAgeSeconds != 7*24*3600 || d.Quarantine.MaxBytes != 20_000_000_000 ||
		d.From["profile"] != "default" || len(d.From) != len(config.Keys) {
		t.Errorf("config show defaults: %+v", d)
	}

	out, errOut, code = sapu(t, e, "", "config", "init", "--json")
	if code != 0 {
		t.Fatalf("config init: exit %d\n%s", code, errOut)
	}
	validate(t, "config-path", out)
	data, err := os.ReadFile(file)
	if err != nil || string(data) != config.Example {
		t.Fatalf("config init wrote %q, %v", data, err)
	}
	if _, errOut, code := sapu(t, e, "", "config", "init"); code != 1 || !strings.Contains(errOut, "already exists") {
		t.Errorf("second config init: exit %d\n%s", code, errOut)
	}

	writeSettings(t, e, "profile = \"aggressive\"\n[whitelist]\npaths = [\"~/keep\"]\n")
	if err := protect.AddWhitelist(e, "~/also-keep"); err != nil {
		t.Fatal(err)
	}
	out, _, _ = sapu(t, e, "", "config", "show", "--json")
	validate(t, "config-show", out)
	d = configData{}
	decodeData(t, out, &d)
	want := []whitelistEntry{{"~/keep", "sapu.toml"}, {"~/also-keep", "whitelist.txt"}}
	if !d.Found || d.Profile != "aggressive" || d.From["profile"] != "sapu.toml" || d.From["quarantine.max_age"] != "default" ||
		!slices.Equal(d.Whitelist, want) {
		t.Errorf("config show: %+v", d)
	}
	out, _, _ = sapu(t, e, "", "config", "show")
	for _, s := range []string{"~/.config/sapu/sapu.toml", "profile", "aggressive", "sapu.toml", "~/also-keep", "sapu whitelist"} {
		if !strings.Contains(out, s) {
			t.Errorf("config show text has no %q:\n%s", s, out)
		}
	}

	writeSettings(t, e, "[quarantine]\nmax_agee = \"7d\"\n")
	if _, errOut, code := sapu(t, e, "", "config", "show"); code != 1 || !strings.Contains(errOut, "line 2") {
		t.Errorf("config show of a bad file: exit %d\n%s", code, errOut)
	}
	// config path still answers, so the file can be found and fixed.
	if out, _, code := sapu(t, e, "", "config", "path"); code != 0 || strings.TrimSpace(out) != file {
		t.Errorf("config path of a bad file: exit %d %q", code, out)
	}
}

func TestSapuConfigVariable(t *testing.T) {
	e := env.New(t.TempDir())
	file := filepath.Join(t.TempDir(), "dotfiles", "sapu.toml")
	writeFile(t, file, `profile = "conservative"`)
	e.ConfigFile, e.ConfigFromEnv = file, true
	out, _, code := sapu(t, e, "", "config", "show", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var d configData
	decodeData(t, out, &d)
	if d.File != file || !d.FromEnv || d.Profile != "conservative" {
		t.Errorf("config show with SAPU_CONFIG: %+v", d)
	}
}
