// Package env holds the folders Sapu works with. It is the only package that
// reads $HOME, $SAPU_HOME, $SAPU_CONFIG and the language variables. Every
// other package receives an Env, so tests can point Home at a temporary
// folder.
package env

import (
	"os"
	"path/filepath"
	"strings"
)

type Env struct {
	Home     string // the user's home folder
	StateDir string // Sapu's own folder: $SAPU_HOME, or ~/.sapu
	Lang     string // two-letter language code: "en", "id", "zh", ...

	ConfigFile    string // sapu.toml: $SAPU_CONFIG, or ~/.config/sapu/sapu.toml
	ConfigFromEnv bool   // ConfigFile came from $SAPU_CONFIG
}

// FromOS builds the Env for the real user. Never call it from tests.
func FromOS() (Env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Env{}, err
	}
	return fromVars(home, os.Getenv), nil
}

// fromVars builds the Env for home from environment variables. get is
// os.Getenv, or a fake in tests.
func fromVars(home string, get func(string) string) Env {
	e := New(home)
	if dir := get("SAPU_HOME"); dir != "" {
		e.StateDir = dir
	}
	if file := get("SAPU_CONFIG"); file != "" {
		e.ConfigFile, e.ConfigFromEnv = e.Expand(file), true // not cleaned: config.Path refuses a relative one
	}
	e.Lang = langFrom(get("SAPU_LANG"), get("LC_ALL"), get("LANG"))
	return e
}

// New builds an Env for the given home folder, with the state folder inside it
// and English text. Tests use New(t.TempDir()).
func New(home string) Env {
	return Env{
		Home:       home,
		StateDir:   filepath.Join(home, ".sapu"),
		Lang:       "en",
		ConfigFile: filepath.Join(home, ".config", "sapu", "sapu.toml"),
	}
}

// Expand turns "~" and "~/x" into paths inside Home. Other paths are returned unchanged.
func (e Env) Expand(p string) string {
	if p == "~" {
		return e.Home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(e.Home, p[2:])
	}
	return p
}

func (e Env) RunsDir() string       { return filepath.Join(e.StateDir, "runs") }
func (e Env) QuarantineDir() string { return filepath.Join(e.StateDir, "quarantine") }
func (e Env) WhitelistFile() string { return filepath.Join(e.StateDir, "whitelist.txt") }

// langFrom returns the first two-letter code found in the values, or "en".
// "id_ID.UTF-8" becomes "id"; "C" and "POSIX" are ignored.
func langFrom(values ...string) string {
	for _, v := range values {
		if len(v) < 2 || v == "C" || v == "POSIX" || strings.HasPrefix(v, "C.") {
			continue
		}
		return strings.ToLower(v[:2])
	}
	return "en"
}
