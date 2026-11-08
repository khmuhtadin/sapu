package rules

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load reads every *.yml file in fsys (recursively) and validates each rule.
// Unknown YAML fields, invalid rules and duplicate IDs are errors. Every error
// names the file and the rule.
func Load(fsys fs.FS) ([]Rule, error) {
	var all []Rule
	idFile := map[string]string{} // rule ID -> file that defined it

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
		rs, err := decode(data)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		for i, r := range rs {
			if err := Validate(r); err != nil {
				return fmt.Errorf("%s: rule #%d %q: %w", name, i+1, r.ID, err)
			}
			if first, ok := idFile[r.ID]; ok {
				return fmt.Errorf("%s: rule %q: duplicate id, already defined in %s", name, r.ID, first)
			}
			idFile[r.ID] = name
			all = append(all, r)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return all, nil
}

// decode reads one YAML list of rules. An empty file holds no rules.
func decode(data []byte) ([]Rule, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var rs []Rule
	if err := dec.Decode(&rs); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return rs, nil
}

// Validate reports the first problem with a rule. It is the CI gate for the
// rule files: a rule that passes Validate is safe to load.
func Validate(r Rule) error {
	if !idPattern.MatchString(r.ID) {
		return fmt.Errorf("id %q must match %s", r.ID, idPattern)
	}
	if Reserved(r.ID) {
		return fmt.Errorf("id %q is reserved: it is the source of a dedicated command's items", r.ID)
	}
	switch r.Category {
	case CategorySystem, CategoryLogs, CategoryBrowsers, CategoryDev, CategoryApps, CategoryMedia:
	default:
		return fmt.Errorf("category %q is not one of system, logs, browsers, dev, apps, media", r.Category)
	}

	if len(r.Paths) == 0 {
		return errors.New("paths is empty")
	}
	for _, p := range r.Paths {
		if err := validatePath(p); err != nil {
			return fmt.Errorf("path %q: %w", p, err)
		}
		if n := literalSegments(p); n < 2 {
			return fmt.Errorf("path %q is too broad: needs at least 2 folders below ~ before the first wildcard, has %d", p, n)
		}
	}
	for _, p := range r.Exclude {
		if err := validatePath(p); err != nil {
			return fmt.Errorf("exclude %q: %w", p, err)
		}
	}

	switch r.Risk {
	case RiskSafe, RiskModerate, RiskRisky:
	default:
		return fmt.Errorf("risk %q is not one of safe, moderate, risky", r.Risk)
	}
	if r.Regenerates == nil {
		return errors.New("regenerates is missing")
	}
	if strings.TrimSpace(r.Explain["en"]) == "" {
		return errors.New("explain.en is missing")
	}

	switch r.Action {
	case ActionQuarantine:
		if len(r.Command) > 0 {
			return errors.New("command is only for action official_command")
		}
	case ActionOfficialCommand:
		if len(r.Command) == 0 || strings.TrimSpace(r.Command[0]) == "" {
			return errors.New("action official_command needs a command")
		}
	default:
		return fmt.Errorf("action %q is not one of quarantine, official_command", r.Action)
	}

	for i, c := range r.ProtectIf {
		if err := validateCondition(c); err != nil {
			return fmt.Errorf("protect_if #%d: %w", i+1, err)
		}
	}

	if strings.TrimSpace(r.Fixture) == "" {
		return errors.New("fixture is missing")
	}
	if strings.ContainsAny(r.Fixture, `/\`) || r.Fixture == "." || r.Fixture == ".." {
		return fmt.Errorf("fixture %q must be a plain folder name", r.Fixture)
	}
	if r.MinMacOS != "" && !versionPattern.MatchString(r.MinMacOS) {
		return fmt.Errorf("min_macos %q must look like 13 or 13.0", r.MinMacOS)
	}
	return nil
}

func validateCondition(c Condition) error {
	set := 0
	if c.ProcessRunning != "" {
		set++
	}
	if c.ModifiedWithin != "" {
		set++
		if _, err := ParseAge(c.ModifiedWithin); err != nil {
			return err
		}
	}
	if set != 1 {
		return errors.New("must set exactly one of process_running or modified_within")
	}
	if c.ProcessRunning != "" && strings.TrimSpace(c.ProcessRunning) == "" {
		return errors.New("process_running is blank")
	}
	return nil
}

// validatePath checks the syntax shared by paths and excludes: it starts with
// "~/", has no empty or ".." segment, every segment is a valid glob, and "**"
// appears only as the whole final segment.
func validatePath(p string) error {
	if !strings.HasPrefix(p, "~/") {
		return errors.New("must start with ~/")
	}
	segs := strings.Split(p[2:], "/")
	for i, s := range segs {
		switch {
		case s == "":
			return errors.New("has an empty segment")
		case s == "..":
			return errors.New(`must not contain a ".." segment`)
		case s == "**":
			if i != len(segs)-1 {
				return errors.New("** is only allowed as the final segment")
			}
			continue
		case strings.Contains(s, "**"):
			return errors.New("** must be a whole segment")
		}
		if _, err := path.Match(s, ""); err != nil {
			return fmt.Errorf("segment %q is not a valid glob: %w", s, err)
		}
	}
	return nil
}

// literalSegments counts the segments below "~" before the first wildcard.
func literalSegments(p string) int {
	n := 0
	for _, s := range strings.Split(strings.TrimPrefix(p, "~/"), "/") {
		if strings.ContainsAny(s, "*?[") {
			break
		}
		n++
	}
	return n
}
