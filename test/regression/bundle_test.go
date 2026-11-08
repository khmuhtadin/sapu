package regression

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/protect"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/rulesupdate"
	ruledata "github.com/khmuhtadin/sapu/rules"
)

// hostileRule is a valid cleanup rule of a rule update, aimed at the places
// where user data lives. An allow may not name a rule id (only a dedicated
// command, rules.ReservedSources), so the update cannot open protected
// places to it; TestBundleCannotSpoofDedicatedSource checks that such an
// update is refused.
const hostileRule = `- id: hostile
  category: apps
  paths:
    - ~/Library/Application Support/*
    - ~/Library/Caches/*
    - ~/Library/Containers/*
    - ~/Library/Group Containers/*
    - ~/Library/Mail/*
    - ~/.claude/projects/*
    - ~/.cache/huggingface/*
  risk: safe
  regenerates: true
  explain:
    en: "A rule update that aims at user data."
  action: quarantine
  fixture: hostile
`

// hostileBundle is a signed rule update that tries every way to weaken the
// built-in protection list: it leaves out every other entry, keeps the rest
// under the same ids but narrowed to a folder that does not exist, opens
// them to the dedicated commands (allow, on the built-in paths and
// everything inside them), and ships hostileRule.
func hostileBundle(t *testing.T, key ed25519.PrivateKey) (data, sig []byte) {
	t.Helper()
	built, err := rulesupdate.Build(ruledata.FS, "2099.1.1", "0.1.0", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var b rulesupdate.Bundle
	if err := json.Unmarshal(built, &b); err != nil {
		t.Fatal(err)
	}
	var weakened []protect.Entry
	for i, en := range protectedEntries(t) {
		if i%2 == 0 {
			continue
		}
		w := protect.Entry{ID: en.ID, Reason: map[string]string{"en": "narrowed"}}
		for _, p := range en.Paths {
			w.Paths = append(w.Paths, p+"/sapu-nothing-here")
		}
		for _, src := range []string{"large", "backups", "messages", "trash"} {
			a := protect.Allow{Source: src}
			for _, p := range en.Paths {
				a.Paths = append(a.Paths, p, p+"/*", p+"/*/*", p+"/sapu-nothing-here")
			}
			w.Allow = append(w.Allow, a)
		}
		weakened = append(weakened, w)
	}
	text, err := yaml.Marshal(weakened)
	if err != nil {
		t.Fatal(err)
	}
	b.Protect = map[string]string{"core.yml": string(text)}
	b.Cleaners["hostile.yml"] = hostileRule

	data, err = json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return data, rulesupdate.Sign(key, data)
}

// installHostile installs the hostile update in e's state folder and checks
// that Sapu uses it.
func installHostile(t *testing.T, e env.Env, data, sig []byte) []rules.Rule {
	t.Helper()
	if _, err := rulesupdate.Install(e, data, sig, "dev"); err != nil {
		t.Fatalf("install the hostile update: %v", err)
	}
	r, err := rulesupdate.Active(e)
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != rulesupdate.SourceBundle || r.Version != "2099.1.1" {
		t.Fatalf("the hostile update is not in use: %+v", r)
	}
	all, err := rules.Load(r.Cleaners)
	if err != nil {
		t.Fatal(err)
	}
	// Like embeddedRules: only quarantine rules, only paths in the home.
	var kept []rules.Rule
	for _, rl := range all {
		var paths []string
		for _, p := range rl.Paths {
			if strings.HasPrefix(p, "~/") {
				paths = append(paths, p)
			}
		}
		if rl.Action == rules.ActionQuarantine && len(paths) > 0 {
			rl.Paths = paths
			kept = append(kept, rl)
		}
	}
	return kept
}

// Case 9: a signed rule update can only add protection. With the hostile
// update installed and in use, every protected case and every entry of the
// built-in list survives the aggressive rules plus the update's own rules,
// and Plan skips them as protected.
//
// The test is not parallel: it sets protect.Extra (the way package cli
// does, without sapu.toml) and the trusted key. Its subtests run in
// parallel with each other only.
func TestHostileRuleUpdateKeepsEveryProtection(t *testing.T) {
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
	data, sig := hostileBundle(t, key)

	cases := map[string]protectedCase{
		"DataGrip": dataGripCase, "HuggingFace": huggingFaceCase, "GitHub Desktop": gitHubDesktopCase,
		"Claude memory": claudeMemoryCase, "Apple Notes": appleNotesCase, "Apple Mail": appleMailCase,
		"browsers": browsersCase,
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := buildCase(t, c)
			update := installHostile(t, e, data, sig)
			before := snapshotEach(t, e, c.keep)

			p, res := clean(t, e, append(aggressiveRules(), update...))

			for _, rel := range c.skipped {
				requireSkip(t, e, p.Skipped, rel, plan.ReasonProtected)
				requireNotPlanned(t, e, p, rel)
			}
			requireSurvived(t, e, before)
			requireMoved(t, e, res, junkItem)
		})
	}

	for _, en := range protectedEntries(t) {
		for _, path := range en.Paths {
			rel, ok := fakePath(path)
			if !ok {
				continue
			}
			t.Run(fmt.Sprintf("%s %s", en.ID, path), func(t *testing.T) {
				t.Parallel()
				e := newHome(t, map[string]string{junkFile: "ordinary cache", rel + "/sapu-regression.txt": "user data"})
				update := installHostile(t, e, data, sig)
				before := snapshotEach(t, e, []string{rel})
				rs := append(aggressiveRules(), synthRule("synthetic.parent", rules.RiskSafe, parentChildrenGlob(rel)))

				p, _ := clean(t, e, append(rs, update...))

				requireSkip(t, e, p.Skipped, rel, plan.ReasonProtected)
				requireNotPlanned(t, e, p, rel)
				requireSurvived(t, e, before)
				requireMissing(t, e, junkFile)
			})
		}
	}
}
