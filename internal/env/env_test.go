package env

import (
	"path/filepath"
	"testing"
)

func TestExpand(t *testing.T) {
	e := New("/h")
	cases := map[string]string{
		"~":                 "/h",
		"~/Library/Caches":  "/h/Library/Caches",
		"/abs/path":         "/abs/path",
		"relative/path":     "relative/path",
		"~other/not/expand": "~other/not/expand",
	}
	for in, want := range cases {
		if got := e.Expand(in); got != want {
			t.Errorf("Expand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNew(t *testing.T) {
	home := t.TempDir()
	e := New(home)
	if e.StateDir != filepath.Join(home, ".sapu") || e.Lang != "en" {
		t.Fatalf("unexpected env: %+v", e)
	}
	if e.ConfigFile != filepath.Join(home, ".config", "sapu", "sapu.toml") || e.ConfigFromEnv {
		t.Fatalf("unexpected config file: %+v", e)
	}
	if e.QuarantineDir() != filepath.Join(home, ".sapu", "quarantine") {
		t.Fatalf("unexpected quarantine dir: %s", e.QuarantineDir())
	}
}

func TestLangFrom(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"", "", "id_ID.UTF-8"}, "id"},
		{[]string{"ja", "", "en_US.UTF-8"}, "ja"},
		{[]string{"", "C", "C.UTF-8"}, "en"},
		{[]string{"", "", ""}, "en"},
	}
	for _, c := range cases {
		if got := langFrom(c.in...); got != c.want {
			t.Errorf("langFrom(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFromVars(t *testing.T) {
	vars := map[string]string{}
	get := func(k string) string { return vars[k] }

	e := fromVars("/h", get)
	if e.ConfigFile != "/h/.config/sapu/sapu.toml" || e.ConfigFromEnv || e.StateDir != "/h/.sapu" {
		t.Errorf("no variables: %+v", e)
	}

	vars["SAPU_CONFIG"] = "/dotfiles/sapu.toml"
	vars["SAPU_HOME"] = "/state"
	e = fromVars("/h", get)
	if e.ConfigFile != "/dotfiles/sapu.toml" || !e.ConfigFromEnv || e.StateDir != "/state" {
		t.Errorf("with variables: %+v", e)
	}

	vars["SAPU_CONFIG"] = "~/dotfiles/sapu.toml"
	if e = fromVars("/h", get); e.ConfigFile != "/h/dotfiles/sapu.toml" {
		t.Errorf("~ not expanded: %q", e.ConfigFile)
	}
}
