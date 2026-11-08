package regression

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/protect"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/rulesupdate"
	"github.com/khmuhtadin/sapu/internal/scan"
	ruledata "github.com/khmuhtadin/sapu/rules"
)

const backupRel = "Library/Application Support/MobileSync/Backup/only-copy"

// spoofBackupsRule is the final review's cleaner rule: its id is the source
// of sapu backups, so its items got the built-in exception that lets one
// iPhone backup through, and a normal clean moved the only copy of a backup.
const spoofBackupsRule = `- id: backups
  category: apps
  paths: ["~/Library/Application Support/MobileSync/Backup/*"]
  risk: safe
  regenerates: true
  explain: {en: "disposable cache"}
  action: quarantine
  fixture: fake
`

// spoofBundle is a signed rule update made from the built-in rules plus
// change.
func spoofBundle(t *testing.T, key ed25519.PrivateKey, change func(b *rulesupdate.Bundle)) (data, sig []byte) {
	t.Helper()
	built, err := rulesupdate.Build(ruledata.FS, "2099.1.1", "0.1.0", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var b rulesupdate.Bundle
	if err := json.Unmarshal(built, &b); err != nil {
		t.Fatal(err)
	}
	change(&b)
	data, err = json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return data, rulesupdate.Sign(key, data)
}

// requireBuiltInRules checks that Sapu uses the built-in rules in e, with a
// warning that contains warning.
func requireBuiltInRules(t *testing.T, e env.Env, warning string) {
	t.Helper()
	r, err := rulesupdate.Active(e)
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != rulesupdate.SourceEmbedded || !strings.Contains(r.Warning, warning) {
		t.Fatalf("rules in use: %s %s, warning %q", r.Source, r.Version, r.Warning)
	}
}

// Case 10: a signed rule update cannot take a dedicated command's source.
// The update with a cleaner rule "backups" is refused at install; stored by
// hand it is not used; either way the normal pipeline never moves a backup.
// An update whose protect allow names its own rule is refused too.
//
// Not parallel: it sets protect.Extra and the trusted key.
func TestBundleCannotSpoofDedicatedSource(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldKeys, oldExtra := rulesupdate.PublicKeys, protect.Extra
	t.Cleanup(func() { rulesupdate.PublicKeys, protect.Extra = oldKeys, oldExtra })
	rulesupdate.PublicKeys = []string{rulesupdate.EncodeKey(pub)}
	protect.Extra = func(e env.Env) (fs.FS, []string, error) {
		r, err := rulesupdate.Active(e)
		return r.Protect, nil, err
	}
	data, sig := spoofBundle(t, key, func(b *rulesupdate.Bundle) { b.Cleaners["spoof.yml"] = spoofBackupsRule })

	t.Run("install", func(t *testing.T) {
		e := newHome(t, map[string]string{backupRel + "/Manifest.db": "irreplaceable backup", junkFile: "ordinary cache"})
		if _, err := rulesupdate.Install(e, data, sig, "0.1.0"); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("Install: %v, want the reserved id refused", err)
		}
		requireBuiltInRules(t, e, "")
		before := snapshotEach(t, e, []string{backupRel})
		_, res := clean(t, e, append(aggressiveRules(), embeddedRules(t)...))
		requireSurvived(t, e, before)
		requireMoved(t, e, res, junkItem)
	})

	t.Run("stored by hand", func(t *testing.T) {
		e := newHome(t, map[string]string{backupRel + "/Manifest.db": "irreplaceable backup", junkFile: "ordinary cache"})
		dir := filepath.Join(e.StateDir, "rules")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string][]byte{"2099.1.1.json": data, "2099.1.1.json.sig": sig, "active": []byte("2099.1.1\n")} {
			if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		requireBuiltInRules(t, e, "reserved")
		before := snapshotEach(t, e, []string{backupRel})
		_, res := clean(t, e, append(aggressiveRules(), embeddedRules(t)...))
		requireSurvived(t, e, before)
		requireMoved(t, e, res, junkItem)
	})

	t.Run("rule made in code", func(t *testing.T) {
		e := newHome(t, map[string]string{backupRel + "/Manifest.db": "irreplaceable backup"})
		rs := []rules.Rule{synthRule("backups", rules.RiskSafe, "~/Library/Application Support/MobileSync/Backup/*")}
		if _, err := scan.Scan(t.Context(), e, rs, nil); err == nil {
			t.Fatal("scan made items for a rule with a reserved id")
		}
	})

	t.Run("allow for its own rule", func(t *testing.T) {
		e := newHome(t, nil)
		own, ownSig := spoofBundle(t, key, func(b *rulesupdate.Bundle) {
			b.Cleaners["hostile.yml"] = hostileRule
			b.Protect = map[string]string{"open.yml": `- id: open-mobilesync
  paths: ["~/Library/Application Support/MobileSync"]
  reason: {en: "opened by an update"}
  allow:
    - source: hostile
      paths: ["~/Library/Application Support/MobileSync/Backup/*"]
`}
		})
		if _, err := rulesupdate.Install(e, own, ownSig, "0.1.0"); err == nil || !strings.Contains(err.Error(), "never a rule id") {
			t.Fatalf("Install: %v, want the allow refused", err)
		}
	})
}
