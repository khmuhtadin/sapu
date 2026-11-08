package rules

import (
	"strings"
	"testing"
	"testing/fstest"
)

func goodRule() Rule {
	regen := true
	return Rule{
		ID:          "example.cache",
		Category:    CategoryApps,
		Paths:       []string{"~/Library/Caches/Example/**"},
		Risk:        RiskSafe,
		Regenerates: &regen,
		Explain:     map[string]string{"en": "Example cache. Safe to remove; rebuilt on next launch."},
		Action:      ActionQuarantine,
		Fixture:     "example-cache",
	}
}

func TestValidateGoodRule(t *testing.T) {
	if err := Validate(goodRule()); err != nil {
		t.Fatalf("good rule rejected: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name   string
		change func(r *Rule)
		want   string // part of the error
	}{
		{"empty id", func(r *Rule) { r.ID = "" }, "id"},
		{"uppercase id", func(r *Rule) { r.ID = "Example.Cache" }, "id"},
		{"id with double dot", func(r *Rule) { r.ID = "a..b" }, "id"},
		{"id ending in dash", func(r *Rule) { r.ID = "a-" }, "id"},
		{"unknown category", func(r *Rule) { r.Category = "misc" }, "category"},
		{"no paths", func(r *Rule) { r.Paths = nil }, "paths is empty"},
		{"path without tilde", func(r *Rule) { r.Paths = []string{"/Library/Caches/Foo"} }, "~/"},
		{"path is only tilde", func(r *Rule) { r.Paths = []string{"~"} }, "~/"},
		{"dot cache whole", func(r *Rule) { r.Paths = []string{"~/.cache/**"} }, "too broad"},
		{"library star", func(r *Rule) { r.Paths = []string{"~/Library/*"} }, "too broad"},
		{"home star", func(r *Rule) { r.Paths = []string{"~/*"} }, "too broad"},
		{"one folder", func(r *Rule) { r.Paths = []string{"~/Library"} }, "too broad"},
		{"wildcard in second segment", func(r *Rule) { r.Paths = []string{"~/Library/*/Foo"} }, "too broad"},
		{"double star in the middle", func(r *Rule) { r.Paths = []string{"~/Library/Caches/**/Foo"} }, "final segment"},
		{"double star glued to a name", func(r *Rule) { r.Paths = []string{"~/Library/Caches/Foo**"} }, "whole segment"},
		{"dot dot", func(r *Rule) { r.Paths = []string{"~/Library/Caches/../Foo"} }, ".."},
		{"empty segment", func(r *Rule) { r.Paths = []string{"~/Library//Foo"} }, "empty segment"},
		{"bad glob", func(r *Rule) { r.Paths = []string{"~/Library/Caches/[Foo"} }, "glob"},
		{"bad exclude glob", func(r *Rule) { r.Exclude = []string{"~/Library/Caches/Foo/[x"} }, "exclude"},
		{"exclude without tilde", func(r *Rule) { r.Exclude = []string{"keep"} }, "exclude"},
		{"no risk", func(r *Rule) { r.Risk = "" }, "risk"},
		{"unknown risk", func(r *Rule) { r.Risk = "scary" }, "risk"},
		{"no regenerates", func(r *Rule) { r.Regenerates = nil }, "regenerates"},
		{"no explain", func(r *Rule) { r.Explain = nil }, "explain.en"},
		{"blank explain", func(r *Rule) { r.Explain = map[string]string{"en": "  "} }, "explain.en"},
		{"explain only in another language", func(r *Rule) { r.Explain = map[string]string{"id": "x"} }, "explain.en"},
		{"no action", func(r *Rule) { r.Action = "" }, "action"},
		{"unknown action", func(r *Rule) { r.Action = "trash" }, "action"},
		{"official command without command", func(r *Rule) { r.Action = ActionOfficialCommand }, "needs a command"},
		{"command with quarantine", func(r *Rule) { r.Command = []string{"brew", "cleanup"} }, "command"},
		{"empty protect_if", func(r *Rule) { r.ProtectIf = []Condition{{}} }, "exactly one"},
		{"two fields in protect_if", func(r *Rule) {
			r.ProtectIf = []Condition{{ProcessRunning: "Foo", ModifiedWithin: "3d"}}
		}, "exactly one"},
		{"bad age", func(r *Rule) { r.ProtectIf = []Condition{{ModifiedWithin: "3y"}} }, "age"},
		{"no fixture", func(r *Rule) { r.Fixture = "" }, "fixture"},
		{"fixture with slash", func(r *Rule) { r.Fixture = "../x" }, "fixture"},
		{"bad min_macos", func(r *Rule) { r.MinMacOS = "Sonoma" }, "min_macos"},
		{"bad min_macos three parts", func(r *Rule) { r.MinMacOS = "13.0.1" }, "min_macos"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := goodRule()
			tt.change(&r)
			err := Validate(r)
			if err == nil {
				t.Fatal("rule should be rejected")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q should mention %q", err, tt.want)
			}
		})
	}
}

func TestValidateAccepts(t *testing.T) {
	tests := []struct {
		name   string
		change func(r *Rule)
	}{
		{"two literal folders then double star", func(r *Rule) { r.Paths = []string{"~/Library/Caches/**"} }},
		{"two folders, no wildcard", func(r *Rule) { r.Paths = []string{"~/.npm/_cacache"} }},
		{"wildcard in the middle", func(r *Rule) { r.Paths = []string{"~/Library/Caches/Google/Chrome/*/Cache"} }},
		{"official command", func(r *Rule) {
			r.Action = ActionOfficialCommand
			r.Command = []string{"brew", "cleanup"}
		}},
		{"protect_if process and age", func(r *Rule) {
			r.ProtectIf = []Condition{{ProcessRunning: "Foo"}, {ModifiedWithin: "3d"}}
		}},
		{"exclude", func(r *Rule) { r.Exclude = []string{"~/Library/Caches/Example/keep*"} }},
		{"min_macos major", func(r *Rule) { r.MinMacOS = "13" }},
		{"min_macos major.minor", func(r *Rule) { r.MinMacOS = "13.0" }},
		{"false regenerates", func(r *Rule) { f := false; r.Regenerates = &f }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := goodRule()
			tt.change(&r)
			if err := Validate(r); err != nil {
				t.Fatalf("rule should pass: %v", err)
			}
		})
	}
}

const goodYAML = `
- id: example.cache
  category: apps
  paths:
    - ~/Library/Caches/Example/**
  risk: safe
  regenerates: true
  explain:
    en: "Example cache."
  action: quarantine
  fixture: example-cache
`

func TestLoadReadsNestedFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"a.yml":         {Data: []byte(goodYAML)},
		"sub/b.yml":     {Data: []byte(strings.ReplaceAll(goodYAML, "example.cache", "other.cache"))},
		"README.md":     {Data: []byte("not a rule file")},
		"sub/empty.yml": {Data: nil},
	}
	rs, err := Load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 {
		t.Fatalf("got %d rules, want 2", len(rs))
	}
	if rs[0].ID != "example.cache" || rs[1].ID != "other.cache" {
		t.Errorf("unexpected rules: %q, %q", rs[0].ID, rs[1].ID)
	}
	if rs[0].Regenerates == nil || !*rs[0].Regenerates {
		t.Error("regenerates was not read")
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name  string
		files fstest.MapFS
		want  []string // every part must appear in the error
	}{
		{
			"unknown field",
			fstest.MapFS{"x.yml": {Data: []byte(goodYAML + "  colour: red\n")}},
			[]string{"x.yml", "colour"},
		},
		{
			"missing risk names file and rule",
			fstest.MapFS{"dir/y.yml": {Data: []byte(strings.Replace(goodYAML, "  risk: safe\n", "", 1))}},
			[]string{"dir/y.yml", "example.cache", "risk"},
		},
		{
			"missing regenerates",
			fstest.MapFS{"y.yml": {Data: []byte(strings.Replace(goodYAML, "  regenerates: true\n", "", 1))}},
			[]string{"y.yml", "example.cache", "regenerates"},
		},
		{
			"missing explain",
			fstest.MapFS{"y.yml": {Data: []byte(strings.Replace(goodYAML, "  explain:\n    en: \"Example cache.\"\n", "", 1))}},
			[]string{"y.yml", "example.cache", "explain.en"},
		},
		{
			"missing fixture",
			fstest.MapFS{"y.yml": {Data: []byte(strings.Replace(goodYAML, "  fixture: example-cache\n", "", 1))}},
			[]string{"y.yml", "example.cache", "fixture"},
		},
		{
			"duplicate id across files",
			fstest.MapFS{"a.yml": {Data: []byte(goodYAML)}, "b.yml": {Data: []byte(goodYAML)}},
			[]string{"b.yml", "example.cache", "duplicate", "a.yml"},
		},
		{
			"not a list",
			fstest.MapFS{"z.yml": {Data: []byte("id: x\n")}},
			[]string{"z.yml"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(tt.files)
			if err == nil {
				t.Fatal("Load should fail")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q should mention %q", err, w)
				}
			}
		})
	}
}
