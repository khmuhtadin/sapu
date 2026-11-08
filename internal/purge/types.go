package purge

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/khmuhtadin/sapu/internal/rules"
	ruledata "github.com/khmuhtadin/sapu/rules"
)

// Type is one kind of project and the folders its tools generate. The types
// live in rules/purge/*.yml, one YAML list per file:
//
//   - id: node                     # lower case words joined by "-"; unique
//     markers: [package.json]      # names of entries (files or folders) that
//     #                              make a folder a project root; globs allowed
//     artifacts: [node_modules]    # folders directly inside the project root
//     risk: safe                   # safe | moderate (never risky)
//     explain:
//     en: "What it is and how it comes back."   # "en" is required
//
// Unknown fields are errors. An artifact is a single folder name: no "/", and
// never ".", "..", ".git" or "src" in any letter case.
type Type struct {
	ID        string            `yaml:"id"`
	Markers   []string          `yaml:"markers"`
	Artifacts []string          `yaml:"artifacts"`
	Risk      rules.Risk        `yaml:"risk"`
	Explain   map[string]string `yaml:"explain"`
}

// worktreeID is the type id of the git worktree suggestion; WorktreeSource
// is its Source.
const (
	worktreeID     = "git-worktree"
	WorktreeSource = "purge:" + worktreeID
)

var idPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// forbidden are names an artifact may never have, whatever the case.
var forbidden = []string{".", "..", ".git", "src"}

// embeddedTypes loads the types built into the binary.
func embeddedTypes() ([]Type, error) {
	fsys, err := fs.Sub(ruledata.FS, "purge")
	if err != nil {
		return nil, err
	}
	return loadTypes(fsys)
}

// ValidateTypes checks type files the way Find reads them.
func ValidateTypes(fsys fs.FS) error {
	_, err := loadTypes(fsys)
	return err
}

// loadTypes reads every *.yml file in fsys and validates each type. Unknown
// YAML fields, invalid types and duplicate ids are errors that name the file
// and the type. The result is sorted by id, so results do not depend on file order.
func loadTypes(fsys fs.FS) ([]Type, error) {
	var all []Type
	idFile := map[string]string{} // id -> file that defined it

	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Ext(name) != ".yml" {
			return nil
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		var ts []Type
		if err := dec.Decode(&ts); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("%s: %w", name, err)
		}
		for i, t := range ts {
			if err := validate(t); err != nil {
				return fmt.Errorf("%s: type #%d %q: %w", name, i+1, t.ID, err)
			}
			if first, ok := idFile[t.ID]; ok {
				return fmt.Errorf("%s: type %q: duplicate id, already defined in %s", name, t.ID, first)
			}
			idFile[t.ID] = name
			all = append(all, t)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	return all, nil
}

// validate reports the first problem with a type.
func validate(t Type) error {
	if !idPattern.MatchString(t.ID) {
		return fmt.Errorf("id %q must match %s", t.ID, idPattern)
	}
	if t.ID == worktreeID {
		return fmt.Errorf("id %q is reserved", t.ID)
	}

	if len(t.Markers) == 0 {
		return errors.New("markers is empty")
	}
	for _, m := range t.Markers {
		if m == "" || strings.Contains(m, "/") {
			return fmt.Errorf("marker %q must be a name without \"/\"", m)
		}
		if _, err := filepath.Match(m, ""); err != nil {
			return fmt.Errorf("marker %q: %w", m, err)
		}
	}

	if len(t.Artifacts) == 0 {
		return errors.New("artifacts is empty")
	}
	for _, a := range t.Artifacts {
		if a == "" || strings.Contains(a, "/") {
			return fmt.Errorf("artifact %q must be one folder name without \"/\"", a)
		}
		for _, f := range forbidden {
			if strings.EqualFold(a, f) {
				return fmt.Errorf("artifact %q is not allowed", a)
			}
		}
	}

	if t.Risk != rules.RiskSafe && t.Risk != rules.RiskModerate {
		return fmt.Errorf("risk %q is not one of safe, moderate", t.Risk)
	}
	if strings.TrimSpace(t.Explain["en"]) == "" {
		return errors.New("explain.en is missing")
	}
	return nil
}

// text returns the explanation in lang, or in English when lang has none.
func (t Type) text(lang string) string {
	if s := strings.TrimSpace(t.Explain[lang]); s != "" {
		return s
	}
	return strings.TrimSpace(t.Explain["en"])
}

// hasMarker reports whether any entry name matches any marker of the type.
func (t Type) hasMarker(names []string) bool {
	for _, m := range t.Markers {
		for _, n := range names {
			if ok, _ := filepath.Match(m, n); ok { // markers were checked by validate
				return true
			}
		}
	}
	return false
}
