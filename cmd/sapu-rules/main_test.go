package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/rulesupdate"
	ruledata "github.com/khmuhtadin/sapu/rules"
)

func tool(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// copyRules copies the rules folder of the source tree (as built in) to dir.
func copyRules(t *testing.T, dir string) {
	t.Helper()
	err := fs.WalkDir(ruledata.FS, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(ruledata.FS, name)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestKeygenBundleSignVerify(t *testing.T) {
	dir := t.TempDir()
	rulesDir := filepath.Join(dir, "rules")
	copyRules(t, rulesDir)

	// keygen: a private key only the owner can read, and the public key.
	key := filepath.Join(dir, "owner.key")
	code, out, errOut := tool(t, "keygen", key)
	if code != 0 {
		t.Fatalf("keygen: exit %d\n%s", code, errOut)
	}
	info, err := os.Stat(key)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode: %v, %v", info.Mode(), err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	pub := lines[len(lines)-1]
	if _, err := rulesupdate.ParsePublicKey(pub); err != nil {
		t.Fatalf("keygen printed no public key:\n%s", out)
	}
	keyData, _ := os.ReadFile(key)
	if code, _, _ := tool(t, "keygen", key); code != 1 {
		t.Errorf("keygen over an existing key: exit %d, want 1", code)
	}
	if again, _ := os.ReadFile(key); !bytes.Equal(again, keyData) {
		t.Error("keygen replaced an existing key")
	}

	// bundle, to a file and to stdout.
	bundle := filepath.Join(dir, "rules-2026.12.1.json")
	if code, _, errOut := tool(t, "bundle", "-version", "2026.12.1", "-min-sapu", "0.1.0", "-o", bundle, rulesDir); code != 0 {
		t.Fatalf("bundle: exit %d\n%s", code, errOut)
	}
	data, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rulesupdate.Check(data); err != nil {
		t.Errorf("the bundle does not check: %v", err)
	}
	if code, out, _ := tool(t, "bundle", "-version", "2026.12.1", "-min-sapu", "0.1.0", rulesDir); code != 0 || !strings.Contains(out, `"version": "2026.12.1"`) {
		t.Errorf("bundle to stdout: exit %d", code)
	}

	// sign and verify.
	if code, _, errOut := tool(t, "sign", bundle, key); code != 0 {
		t.Fatalf("sign: exit %d\n%s", code, errOut)
	}
	sig, err := os.ReadFile(bundle + ".sig")
	if err != nil {
		t.Fatal(err)
	}
	code, out, errOut = tool(t, "verify", bundle, pub)
	if code != 0 || !strings.Contains(out, "OK: rules 2026.12.1") {
		t.Errorf("verify: exit %d\n%s%s", code, out, errOut)
	}

	// Sapu, trusting that key, installs and uses it.
	old := rulesupdate.PublicKeys
	t.Cleanup(func() { rulesupdate.PublicKeys = old })
	rulesupdate.PublicKeys = []string{pub}
	e := env.New(t.TempDir())
	if _, err := rulesupdate.Install(e, data, sig, "1.0.0"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if r, err := rulesupdate.Active(e); err != nil || r.Source != rulesupdate.SourceBundle || r.Version != "2026.12.1" {
		t.Errorf("Active = %+v, %v", r, err)
	}

	// verify refuses another key, a changed bundle and a signature of
	// something else.
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := tool(t, "verify", bundle, rulesupdate.EncodeKey(otherPub)); code != 1 || !strings.Contains(errOut, "does not match") {
		t.Errorf("verify with another key: exit %d\n%s", code, errOut)
	}
	changed := filepath.Join(dir, "changed.json")
	if err := os.WriteFile(changed, bytes.Replace(data, []byte("Slack"), []byte("Slaack"), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := tool(t, "verify", "-sig", bundle+".sig", changed, pub); code != 1 {
		t.Errorf("verify of a changed bundle: exit %d, want 1", code)
	}
}

func TestBundleRefusesBadRules(t *testing.T) {
	dir := t.TempDir()
	rulesDir := filepath.Join(dir, "rules")
	copyRules(t, rulesDir)
	bad := "- id: too-broad\n  category: apps\n  paths: [\"~/Library/*\"]\n  risk: safe\n  regenerates: true\n" +
		"  explain: {en: x}\n  action: quarantine\n  fixture: x\n"
	if err := os.WriteFile(filepath.Join(rulesDir, "cleaners", "bad.yml"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "b.json")
	code, _, errOut := tool(t, "bundle", "-version", "2026.12.1", "-min-sapu", "0.1.0", "-o", out, rulesDir)
	if code != 1 || !strings.Contains(errOut, "too broad") {
		t.Errorf("bundle with a bad rule: exit %d\n%s", code, errOut)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("a bundle was written")
	}
	if code, _, _ := tool(t, "sign", filepath.Join(rulesDir, "cleaners", "bad.yml"), filepath.Join(dir, "no.key")); code != 1 {
		t.Errorf("sign of something that is not a bundle: exit %d, want 1", code)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"frobnicate"},
		{"keygen"},
		{"bundle", "rules"},
		{"bundle", "-version", "1", "rules"},
		{"sign", "x"},
		{"verify", "x"},
		{"bundle", "-nope"},
	} {
		if code, _, _ := tool(t, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	if code, out, _ := tool(t, "help"); code != 0 || !strings.Contains(out, "sapu-rules keygen") {
		t.Errorf("help: exit %d", code)
	}
}
