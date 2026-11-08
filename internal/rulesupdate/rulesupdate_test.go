package rulesupdate

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/safefile"
	ruledata "github.com/khmuhtadin/sapu/rules"
)

// Tests in this package change PublicKeys, so none of them is parallel.

// trustKey makes a new key the only key this build trusts, for one test.
func trustKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	old := PublicKeys
	PublicKeys = []string{EncodeKey(pub)}
	t.Cleanup(func() { PublicKeys = old })
	return priv
}

// newBundle builds a bundle from the built-in rule files, lets change edit
// it (it may make it invalid), and signs it with key.
func newBundle(t *testing.T, key ed25519.PrivateKey, version string, change func(b *Bundle)) (data, sig []byte) {
	t.Helper()
	built, err := Build(ruledata.FS, version, "0.1.0", time.Now())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var b Bundle
	if err := json.Unmarshal(built, &b); err != nil {
		t.Fatal(err)
	}
	if change != nil {
		change(&b)
	}
	data, err = json.MarshalIndent(b, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return data, Sign(key, data)
}

// extraRule is a cleanup rule only bundles have.
const extraRule = `- id: test.bundle-only
  category: apps
  paths:
    - ~/Library/Caches/com.example.bundle-only
  risk: safe
  regenerates: true
  explain:
    en: "A cache that only the rule update knows."
  action: quarantine
  fixture: bundle-only
`

func withExtraRule(b *Bundle) { b.Cleaners["extra.yml"] = extraRule }

func ruleIDs(t *testing.T, fsys fs.FS) []string {
	t.Helper()
	rs, err := rules.Load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rs {
		ids = append(ids, r.ID)
	}
	return ids
}

func requireEmbedded(t *testing.T, e env.Env, warning string) {
	t.Helper()
	r, err := Active(e)
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != SourceEmbedded || r.Version != EmbeddedVersion || r.Protect != nil {
		t.Fatalf("Active = %s %s (protect %v), want the built-in rules", r.Source, r.Version, r.Protect)
	}
	if slices.Contains(ruleIDs(t, r.Cleaners), "test.bundle-only") {
		t.Error("the built-in rules hold the bundle's rule")
	}
	if !strings.Contains(r.Warning, warning) {
		t.Errorf("Warning = %q, want it to contain %q", r.Warning, warning)
	}
}

func TestInstallAndUse(t *testing.T) {
	key := trustKey(t)
	e := env.New(t.TempDir())
	requireEmbedded(t, e, "")

	data, sig := newBundle(t, key, "2026.12.1", withExtraRule)
	b, err := Install(e, data, sig, "1.0.0")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if b.Version != "2026.12.1" {
		t.Errorf("Install returned version %s", b.Version)
	}

	r, err := Active(e)
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != SourceBundle || r.Version != "2026.12.1" || r.Warning != "" || r.MinSapu != "0.1.0" {
		t.Fatalf("Active = %+v", r)
	}
	if !slices.Contains(ruleIDs(t, r.Cleaners), "test.bundle-only") {
		t.Error("the bundle's cleanup rules are not used")
	}
	if r.Protect == nil || r.Purge == nil {
		t.Error("the bundle's protect or purge files are missing")
	}

	// Stored byte for byte, next to its signature and the active file.
	dir := filepath.Join(e.StateDir, "rules")
	for name, want := range map[string]string{"2026.12.1.json": string(data), "2026.12.1.json.sig": string(sig), "active": "2026.12.1\n"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v", name, got, err)
		}
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode: %v", name, err)
		}
	}
}

func TestPlaceholderKeyRefusesEverything(t *testing.T) {
	key := trustKey(t)
	e := env.New(t.TempDir())
	data, sig := newBundle(t, key, "2026.12.1", nil)

	PublicKeys = []string{placeholderKey}
	if KeyConfigured() {
		t.Error("KeyConfigured with only the placeholder")
	}
	_, err := Install(e, data, sig, "1.0.0")
	if !errors.Is(err, ErrNoKey) || err.Error() != "no signing key configured in this build" {
		t.Errorf("Install: err = %v, want ErrNoKey", err)
	}
	requireNoState(t, e)
}

func requireNoState(t *testing.T, e env.Env) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(e.StateDir, "rules")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("rules folder exists after a refused install: %v", err)
	}
}

func TestBadSignatureIsRefused(t *testing.T) {
	key := trustKey(t)
	e := env.New(t.TempDir())
	data, sig := newBundle(t, key, "2026.12.1", nil)
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	changed := []byte(strings.Replace(string(data), "Slack", "Slaack", 1))

	cases := map[string][2][]byte{
		"signed by another key":    {data, Sign(other, data)},
		"bundle changed after":     {changed, sig},
		"signature not base64":     {data, []byte("not a signature")},
		"signature of other bytes": {data, Sign(key, changed)},
		"empty signature":          {data, nil},
	}
	for name, c := range cases {
		if _, err := Install(e, c[0], c[1], "1.0.0"); err == nil {
			t.Errorf("%s: installed", name)
		}
	}
	requireNoState(t, e)
}

// A stored bundle that changed after install, or lost its signature, is not
// used: Active falls back to the built-in rules and says why.
func TestTamperedBundleFallsBack(t *testing.T) {
	cases := map[string]func(t *testing.T, file string){
		"bundle edited": func(t *testing.T, file string) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			data = []byte(strings.Replace(string(data), "~/Library/Caches/com.example.bundle-only", "~/Library/Mail/x", 1))
			if err := os.WriteFile(file, data, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"signature replaced": func(t *testing.T, file string) {
			if err := os.WriteFile(file+".sig", []byte("AAAA\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"signature gone": func(t *testing.T, file string) {
			if err := os.Remove(file + ".sig"); err != nil { // inside the test's temp folder
				t.Fatal(err)
			}
		},
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			key := trustKey(t)
			e := env.New(t.TempDir())
			data, sig := newBundle(t, key, "2026.12.1", withExtraRule)
			if _, err := Install(e, data, sig, "1.0.0"); err != nil {
				t.Fatal(err)
			}
			tamper(t, filepath.Join(e.StateDir, "rules", "2026.12.1.json"))
			requireEmbedded(t, e, "not the installed rules 2026.12.1")
		})
	}
}

func TestStoredBundleUnderAnotherNameFallsBack(t *testing.T) {
	key := trustKey(t)
	e := env.New(t.TempDir())
	data, sig := newBundle(t, key, "2026.12.1", nil)
	if _, err := Install(e, data, sig, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(e.StateDir, "rules")
	for name, content := range map[string][]byte{"2026.12.9.json": data, "2026.12.9.json.sig": sig, "active": []byte("2026.12.9\n")} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	requireEmbedded(t, e, "holds version 2026.12.1")
}

func TestActiveFileProblemsFallBack(t *testing.T) {
	e := env.New(t.TempDir())
	dir := filepath.Join(e.StateDir, "rules")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "active"), []byte("../../etc/x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	requireEmbedded(t, e, "not a version")
	if err := os.WriteFile(filepath.Join(dir, "active"), []byte("2026.12.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	requireEmbedded(t, e, "no such file")
}

// A bundle the binary's own rules have caught up with is not used: a
// release that fixes a rule is never hidden by an older bundle.
func TestBundleNotNewerThanBuiltInRulesFallsBack(t *testing.T) {
	key := trustKey(t)
	e := env.New(t.TempDir())
	data, sig := newBundle(t, key, EmbeddedVersion, nil)
	dir := filepath.Join(e.StateDir, "rules")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{EmbeddedVersion + ".json": data, EmbeddedVersion + ".json.sig": sig, "active": []byte(EmbeddedVersion)} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	requireEmbedded(t, e, "built-in rules ("+EmbeddedVersion+") are newer")
}

func TestOlderOrEqualVersionIsRefused(t *testing.T) {
	key := trustKey(t)
	e := env.New(t.TempDir())

	for _, v := range []string{EmbeddedVersion, "2026.11.7", "2025.12.31", "1"} {
		data, sig := newBundle(t, key, v, nil)
		if _, err := Install(e, data, sig, "1.0.0"); err == nil || !strings.Contains(err.Error(), "not newer") {
			t.Errorf("version %s over the built-in %s: err = %v", v, EmbeddedVersion, err)
		}
	}
	requireNoState(t, e)

	data, sig := newBundle(t, key, "2026.12.5", nil)
	if _, err := Install(e, data, sig, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"2026.12.5", "2026.12.5.0", "2026.12.4", "2026.12.1"} {
		data, sig := newBundle(t, key, v, nil)
		if _, err := Install(e, data, sig, "1.0.0"); err == nil || !strings.Contains(err.Error(), "not newer") {
			t.Errorf("version %s over 2026.12.5: err = %v", v, err)
		}
	}
	data, sig = newBundle(t, key, "2026.12.5.1", nil)
	if _, err := Install(e, data, sig, "1.0.0"); err != nil {
		t.Errorf("2026.12.5.1 over 2026.12.5: %v", err)
	}
}

func TestTooNewMinSapuIsRefused(t *testing.T) {
	key := trustKey(t)
	e := env.New(t.TempDir())
	data, sig := newBundle(t, key, "2026.12.1", func(b *Bundle) { b.MinSapu = "1.3.0" })

	for _, sapu := range []string{"1.2.9", "v1.2.0", "1.2.9-rc.1", "0.9"} {
		if _, err := Install(e, data, sig, sapu); err == nil || !strings.Contains(err.Error(), "need Sapu 1.3.0") {
			t.Errorf("Sapu %s: err = %v", sapu, err)
		}
	}
	requireNoState(t, e)
	if _, err := Install(e, data, sig, "v1.3.0"); err != nil {
		t.Errorf("Sapu v1.3.0: %v", err)
	}
}

// A build from source has no version number; it counts as the newest.
func TestDevBuildAcceptsAnyMinSapu(t *testing.T) {
	key := trustKey(t)
	e := env.New(t.TempDir())
	data, sig := newBundle(t, key, "2026.12.1", func(b *Bundle) { b.MinSapu = "99.0.0" })
	if _, err := Install(e, data, sig, "dev"); err != nil {
		t.Errorf("dev build: %v", err)
	}
}

// Every rule file is checked by the loader that will read it; one bad rule
// refuses the whole bundle and nothing is written.
func TestInvalidRuleIsRefused(t *testing.T) {
	cases := map[string]func(b *Bundle){
		"cleaner too broad": func(b *Bundle) {
			b.Cleaners["extra.yml"] = strings.Replace(extraRule, "~/Library/Caches/com.example.bundle-only", "~/Library/*", 1)
		},
		"cleaner unknown field": func(b *Bundle) {
			b.Cleaners["extra.yml"] = extraRule + "  delete_for_good: true\n"
		},
		"cleaner duplicate id": func(b *Bundle) {
			b.Cleaners["extra.yml"] = extraRule
			b.Cleaners["extra2.yml"] = extraRule
		},
		"protect entry without reason": func(b *Bundle) {
			b.Protect["more.yml"] = "- id: more\n  paths: [~/More]\n"
		},
		"protect path outside home": func(b *Bundle) {
			b.Protect["more.yml"] = "- id: more\n  paths: [/Users]\n  reason: {en: x}\n"
		},
		"purge type risky": func(b *Bundle) {
			b.Purge["more.yml"] = "- id: more\n  markers: [x.toml]\n  artifacts: [out]\n  risk: risky\n  explain: {en: x}\n"
		},
		"purge artifact src": func(b *Bundle) {
			b.Purge["more.yml"] = "- id: more\n  markers: [x.toml]\n  artifacts: [src]\n  risk: safe\n  explain: {en: x}\n"
		},
		"no protect files":      func(b *Bundle) { b.Protect = nil },
		"no cleaner files":      func(b *Bundle) { b.Cleaners = map[string]string{} },
		"file name with folder": func(b *Bundle) { b.Cleaners["sub/x.yml"] = extraRule },
		"file name going up":    func(b *Bundle) { b.Protect["../x.yml"] = b.Protect["core.yml"] },
		"bad version":           func(b *Bundle) { b.Version = "2026-12-01" },
		"bad min_sapu":          func(b *Bundle) { b.MinSapu = "latest" },
		"no created":            func(b *Bundle) { b.Created = time.Time{} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			key := trustKey(t)
			e := env.New(t.TempDir())
			data, sig := newBundle(t, key, "2026.12.1", change)
			if _, err := Install(e, data, sig, "1.0.0"); err == nil {
				t.Fatal("installed")
			}
			requireNoState(t, e)
		})
	}
}

func TestCheckRefusesOtherJSON(t *testing.T) {
	key := trustKey(t)
	data, _ := newBundle(t, key, "2026.12.1", nil)
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	m["allow_sources"] = []string{"clean"}
	unknown, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for name, d := range map[string][]byte{
		"unknown field":  unknown,
		"trailing data":  append(slices.Clone(data), []byte(`{}`)...),
		"not JSON":       []byte("version: 1"),
		"empty document": []byte(""),
	} {
		if _, err := Check(d); err == nil {
			t.Errorf("%s: Check passed", name)
		}
	}
	if _, err := Check(data); err != nil {
		t.Errorf("valid bundle: %v", err)
	}
}

func TestReset(t *testing.T) {
	key := trustKey(t)
	e := env.New(t.TempDir())
	if err := Reset(e); err != nil {
		t.Fatal(err)
	}
	requireNoState(t, e) // nothing installed: nothing written

	data, sig := newBundle(t, key, "2026.12.1", withExtraRule)
	if _, err := Install(e, data, sig, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := Reset(e); err != nil {
		t.Fatal(err)
	}
	requireEmbedded(t, e, "")
	dir := filepath.Join(e.StateDir, "rules")
	if got, _ := os.ReadFile(filepath.Join(dir, "active")); string(got) != "embedded\n" {
		t.Errorf("active = %q", got)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "2026.12.1.json")); err != nil || string(got) != string(data) {
		t.Errorf("reset changed the stored bundle: %v", err)
	}

	// The same version can come back after a reset: it is newer than the
	// built-in rules, which are in use again.
	if _, err := Install(e, data, sig, "1.0.0"); err != nil {
		t.Errorf("install after reset: %v", err)
	}
}

func TestBuildReadsOnlyYAMLFiles(t *testing.T) {
	trustKey(t)
	data, err := Build(ruledata.FS, "2026.12.1", "0.1.0", time.Date(2026, 12, 1, 10, 0, 0, 5, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Check(data)
	if err != nil {
		t.Fatal(err)
	}
	for part, files := range map[string]map[string]string{"cleaners": b.Cleaners, "protect": b.Protect, "purge": b.Purge} {
		names, err := fs.Glob(ruledata.FS, part+"/*.yml")
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != len(names) || len(files) == 0 {
			t.Errorf("%s: %d files in the bundle, %d in rules/", part, len(files), len(names))
		}
		if _, ok := files["README.md"]; ok {
			t.Errorf("%s: README.md is in the bundle", part)
		}
	}
	if b.Created.Location() != time.UTC || b.Created.Nanosecond() != 0 {
		t.Errorf("created = %v, want UTC whole seconds", b.Created)
	}
}

func TestVersions(t *testing.T) {
	cmp := map[[2]string]int{
		{"2026.11.8", "2026.11.8"}:    0,
		{"2026.11.8", "2026.11.8.0"}:  0,
		{"2026.11.10", "2026.11.9"}:   1,
		{"2026.11.8", "2026.11.8.1"}:  -1,
		{"2027", "2026.12.31.9"}:      1,
		{"1.10.0", "1.9.0"}:           1,
		{"000002026.1", "2026.1.0.0"}: 0,
	}
	for c, want := range cmp {
		got := compareVersions(c[0], c[1])
		if (got > 0) != (want > 0) || (got < 0) != (want < 0) {
			t.Errorf("compareVersions(%s, %s) = %d, want sign of %d", c[0], c[1], got, want)
		}
	}
	at := map[[2]string]bool{
		{"1.2.0", "1.2.0"}: true, {"v1.2.1", "1.2.0"}: true, {"1.1.9", "1.2.0"}: false,
		{"dev", "9.9.9"}: true, {"1.2.0-3-gabc", "1.2.0"}: true, {"1.2.0+dirty", "1.2.1"}: false,
	}
	for c, want := range at {
		if got := sapuAtLeast(c[0], c[1]); got != want {
			t.Errorf("sapuAtLeast(%s, %s) = %v", c[0], c[1], got)
		}
	}
}

// embeddedDigest is the SHA-256 of the built-in rule files, as
// rulesDigest computes it. When this test fails, a rule file changed: bump
// EmbeddedVersion (a stored bundle must not hide the new built-in rules),
// then set embeddedDigest to the value the failure prints.
const embeddedDigest = "86b4117b4f933a1bb35f60f67f9fa6315dafb6bae2fe2660da4185ef7c78460a"

func TestEmbeddedVersionIsCurrent(t *testing.T) {
	if !versionPattern.MatchString(EmbeddedVersion) {
		t.Fatalf("EmbeddedVersion %q is not a version", EmbeddedVersion)
	}
	if got := rulesDigest(t); got != embeddedDigest {
		t.Errorf("the files in rules/ changed. Bump EmbeddedVersion in rulesupdate.go (now %s), then set embeddedDigest to %q",
			EmbeddedVersion, got)
	}
}

func rulesDigest(t *testing.T) string {
	t.Helper()
	h := sha256.New()
	for _, part := range Parts {
		names, err := fs.Glob(ruledata.FS, part+"/*.yml")
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			data, err := fs.ReadFile(ruledata.FS, name)
			if err != nil {
				t.Fatal(err)
			}
			h.Write([]byte(name + "\x00"))
			h.Write(data)
			h.Write([]byte{0})
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Install and Reset never write through a symlink: the stored bundle, its
// signature and the active marker are each refused, and what the link points
// to stays as it was.
func TestStoredFilesAreNeverWrittenThroughSymlinks(t *testing.T) {
	key := trustKey(t)
	for _, name := range []string{"2026.12.5.json", "2026.12.5.json.sig", "active"} {
		e := env.New(t.TempDir())
		dir := filepath.Join(e.StateDir, "rules")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		precious := filepath.Join(e.Home, "precious")
		if err := os.WriteFile(precious, []byte("precious"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(precious, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}

		data, sig := newBundle(t, key, "2026.12.5", nil)
		if _, err := Install(e, data, sig, "1.0.0"); !errors.Is(err, safefile.ErrRefused) {
			t.Errorf("%s is a symlink: Install err = %v, want a refusal", name, err)
		}
		if name == "active" {
			if err := Reset(e); !errors.Is(err, safefile.ErrRefused) {
				t.Errorf("active is a symlink: Reset err = %v, want a refusal", err)
			}
		}
		if b, _ := os.ReadFile(precious); string(b) != "precious" {
			t.Errorf("%s: the file behind the symlink changed to %q", name, b)
		}
	}
}
