package rules

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	ruledata "github.com/khmuhtadin/sapu/rules"
)

var translatedLangs = []string{"zh", "ja", "id", "es", "pt", "de"}

// text is one translated sentence: an item explanation or a protection reason.
type text struct {
	name  string // file and id, for messages
	words map[string]string
}

// textEntry is the part of a rule, purge type or protect entry that holds text.
type textEntry struct {
	ID      string            `yaml:"id"`
	Explain map[string]string `yaml:"explain"`
	Reason  map[string]string `yaml:"reason"`
}

// loadTexts reads every YAML file under cleaners, purge and protect and returns
// all explain and reason maps.
func loadTexts(t *testing.T) []text {
	t.Helper()
	var all []text
	err := fs.WalkDir(ruledata.FS, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Ext(name) != ".yml" {
			return nil
		}
		data, err := fs.ReadFile(ruledata.FS, name)
		if err != nil {
			return err
		}
		var entries []textEntry
		if err := yaml.Unmarshal(data, &entries); err != nil {
			return err
		}
		for _, e := range entries {
			words := e.Explain
			if words == nil {
				words = e.Reason
			}
			all = append(all, text{name: name + " " + e.ID, words: words})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func TestTextsAreTranslated(t *testing.T) {
	texts := loadTexts(t)
	if len(texts) < 100 {
		t.Fatalf("found %d texts, want at least 100", len(texts))
	}

	backticks := regexp.MustCompile("`([^`]+)`")

	for _, tx := range texts {
		en := tx.words["en"]
		if en == "" {
			t.Errorf("%s: no English text", tx.name)
			continue
		}
		if en != strings.TrimSpace(en) {
			t.Errorf("%s: en has leading or trailing spaces", tx.name)
		}

		for lang := range tx.words {
			if lang == "en" {
				continue
			}
			known := false
			for _, l := range translatedLangs {
				if l == lang {
					known = true
				}
			}
			if !known {
				t.Errorf("%s: unexpected language %q", tx.name, lang)
			}
		}

		for _, lang := range translatedLangs {
			got, ok := tx.words[lang]
			if !ok || strings.TrimSpace(got) == "" {
				t.Errorf("%s: %s is missing or empty", tx.name, lang)
				continue
			}
			if got != strings.TrimSpace(got) {
				t.Errorf("%s: %s has leading or trailing spaces", tx.name, lang)
			}
			if got == en {
				t.Errorf("%s: %s is the same as the English text", tx.name, lang)
			}
			for _, m := range backticks.FindAllStringSubmatch(en, -1) {
				if !strings.Contains(got, m[1]) {
					t.Errorf("%s: %s lost %q from the English text", tx.name, lang, m[1])
				}
			}
		}
	}
}
