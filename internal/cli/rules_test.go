package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/rulesupdate"
	ruledata "github.com/khmuhtadin/sapu/rules"
)

// trustTestKey makes a new key the only one this build trusts, for one test.
func trustTestKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	old := rulesupdate.PublicKeys
	rulesupdate.PublicKeys = []string{rulesupdate.EncodeKey(pub)}
	t.Cleanup(func() { rulesupdate.PublicKeys = old })
	return priv
}

// riskyRule is a cleanup rule only the test rule updates have.
const riskyRule = `- id: test.risky
  category: apps
  paths:
    - ~/Library/Caches/com.example.risky
  risk: risky
  regenerates: true
  explain:
    en: "A risky cache that only the rule update knows."
  action: quarantine
  fixture: test-risky
`

const riskyItem = "Library/Caches/com.example.risky"

// testBundle writes a signed bundle of the built-in rules plus riskyRule to
// a temp folder and returns the bundle and signature files.
func testBundle(t *testing.T, key ed25519.PrivateKey, version string) (bundle, sig string) {
	t.Helper()
	built, err := rulesupdate.Build(ruledata.FS, version, "0.1.0", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var b rulesupdate.Bundle
	if err := json.Unmarshal(built, &b); err != nil {
		t.Fatal(err)
	}
	b.Cleaners["test.yml"] = riskyRule
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bundle, sig = filepath.Join(dir, "rules.json"), filepath.Join(dir, "rules.json.sig")
	writeFile(t, bundle, string(data))
	writeFile(t, sig, string(rulesupdate.Sign(key, data)))
	return bundle, sig
}

// installTestRules installs testBundle in e and puts a risky item in the home.
func installTestRules(t *testing.T, e env.Env) {
	t.Helper()
	bundle, sig := testBundle(t, trustTestKey(t), "2026.12.1")
	if out, errOut, code := sapu(t, e, "", "rules", "install", bundle, "--sig", sig); code != 0 {
		t.Fatalf("rules install: exit %d\n%s\n%s", code, out, errOut)
	}
	writeFile(t, filepath.Join(e.Home, riskyItem, "cache.db"), "risky cache")
}

func rulesStatusOf(t *testing.T, e env.Env) rulesData {
	t.Helper()
	out, errOut, code := sapu(t, e, "", "rules", "status", "--json")
	if code != 0 {
		t.Fatalf("rules status: exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "rules-status", out)
	var d rulesData
	decodeData(t, out, &d)
	return d
}

func planHas(t *testing.T, e env.Env, file, rel string) bool {
	t.Helper()
	for _, it := range readPlanFile(t, file).Items {
		if it.Path == filepath.Join(e.Home, rel) {
			return true
		}
	}
	return false
}

func TestRulesInstallUseAndReset(t *testing.T) {
	e := newHome(t)
	d := rulesStatusOf(t, e)
	if d.Source != "embedded" || d.Version != rulesupdate.EmbeddedVersion || d.KeyConfigured {
		t.Errorf("status before: %+v", d)
	}
	out, _, _ := sapu(t, e, "", "rules", "status")
	if !strings.Contains(out, "built into Sapu") || !strings.Contains(out, "no signing key") {
		t.Errorf("status text:\n%s", out)
	}

	bundle, sig := testBundle(t, trustTestKey(t), "2026.12.1")
	out, errOut, code := sapu(t, e, "", "rules", "install", bundle, "--sig", sig, "--json")
	if code != 0 {
		t.Fatalf("install: exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "rules-status", out)
	d = rulesStatusOf(t, e)
	if d.Source != "bundle" || d.Version != "2026.12.1" || d.Created == nil || d.MinSapu != "0.1.0" || !d.KeyConfigured ||
		d.File != filepath.Join(e.StateDir, "rules", "2026.12.1.json") {
		t.Errorf("status after install: %+v", d)
	}
	out, _, _ = sapu(t, e, "", "rules", "status")
	if !strings.Contains(out, "signed update 2026.12.1") {
		t.Errorf("status text:\n%s", out)
	}

	// clean uses the update's rules.
	writeFile(t, filepath.Join(e.Home, riskyItem, "cache.db"), "risky cache")
	if file := planFileOf(t, e); !planHas(t, e, file, riskyItem) {
		t.Error("clean does not use the installed rules")
	}

	// A stored bundle that changed is not used, and every command says so.
	stored := filepath.Join(e.StateDir, "rules", "2026.12.1.json")
	data, err := os.ReadFile(stored)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stored, []byte(strings.Replace(string(data), "com.example.risky", "com.example.riskz", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "plan.json")
	_, errOut, code = sapu(t, e, "", "clean", "--plan", file)
	if code != 0 || !strings.Contains(errOut, "warning: using the built-in rules, not the installed rules 2026.12.1") {
		t.Errorf("tampered: exit %d, stderr:\n%s", code, errOut)
	}
	if planHas(t, e, file, riskyItem) {
		t.Error("clean used a tampered rule update")
	}
	if d := rulesStatusOf(t, e); d.Source != "embedded" || !strings.Contains(d.Warning, "signature no longer matches") {
		t.Errorf("status of a tampered update: %+v", d)
	}

	// reset: built-in rules again, nothing deleted.
	out, errOut, code = sapu(t, e, "", "rules", "reset", "--json")
	if code != 0 {
		t.Fatalf("reset: exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "rules-status", out)
	if d := rulesStatusOf(t, e); d.Source != "embedded" || d.Warning != "" {
		t.Errorf("status after reset: %+v", d)
	}
	if _, err := os.Stat(stored); err != nil {
		t.Errorf("reset removed the stored bundle: %v", err)
	}
}

func TestRulesInstallRefusals(t *testing.T) {
	e := newHome(t)
	bundle, sig := testBundle(t, trustTestKey(t), "2026.12.1")
	_, otherKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	badSig := filepath.Join(t.TempDir(), "bad.sig")
	writeFile(t, badSig, string(rulesupdate.Sign(otherKey, data)))

	if _, errOut, code := sapu(t, e, "", "rules", "install", bundle, "--sig", badSig); code != 1 ||
		!strings.Contains(errOut, "rules not installed: the signature does not match") {
		t.Errorf("bad signature: exit %d\n%s", code, errOut)
	}
	if _, _, code := sapu(t, e, "", "rules", "install", bundle); code != 2 {
		t.Errorf("no --sig: exit %d, want 2", code)
	}
	if out, _, code := sapu(t, e, "", "rules", "install", "--json"); code != 2 || !strings.Contains(out, `"rules-install"`) {
		t.Errorf("no bundle: exit %d\n%s", code, out)
	}
	if _, err := os.Lstat(filepath.Join(e.StateDir, "rules")); err == nil {
		t.Error("a refused install wrote the rules folder")
	}

	rulesupdate.PublicKeys = []string{"PLACEHOLDER-no-signing-key-configured-in-this-build"}
	if _, errOut, code := sapu(t, e, "", "rules", "install", bundle, "--sig", sig); code != 1 ||
		!strings.Contains(errOut, "no signing key configured in this build") {
		t.Errorf("placeholder key: exit %d\n%s", code, errOut)
	}
}
