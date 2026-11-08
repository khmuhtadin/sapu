// Package rulesupdate installs and loads signed rule updates (blueprint 6.2
// and 6.3): the rule files of rules/cleaners, rules/protect and rules/purge,
// updated apart from the binary. docs/rules-updates.md has the owner's side.
//
// A bundle is one JSON document (Bundle) and a detached signature file: the
// base64 ed25519 signature of the exact JSON bytes, by a key in PublicKeys.
//
// Install takes local files. It verifies the signature, checks every rule
// file with the loaders Sapu uses (Check), refuses a version that is not
// newer than the rules in use and a min_sapu newer than this binary, and
// writes three files into <state>/rules/ with plain writes:
//
//	<version>.json      the bundle, byte for byte
//	<version>.json.sig  its signature
//	active              the version in use, or "embedded"
//
// Reset writes "embedded" to active. Nothing here deletes or renames a
// file; old bundles stay where they are.
//
// Active is what every command loads rules through. It reads the bundle
// named by active again on every call and verifies its signature and rule
// files again. Any problem (a changed or unreadable file, a bad signature,
// rules this binary cannot load, a bundle not newer than the built-in rules)
// gives the built-in rules and a Warning that says why.
//
// The safety rule for protection: a bundle can only add protection. Cleaner
// rules and purge types come from the bundle alone, but Rules.Protect is
// never the whole protection list: protect.Load always reads the built-in
// list and ADDS the bundle's entries as entries of their own (see package
// protect). A bundle that leaves out, narrows or opens up (allow) a
// built-in entry changes nothing about that entry: an allow of a bundle's
// entry opens only that entry's own match.
//
// Implemented in Wave 8 (W8-D).
package rulesupdate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/safefile"
	ruledata "github.com/khmuhtadin/sapu/rules"
)

// EmbeddedVersion is the version of the rule files built into this binary.
// Bump it whenever a file in rules/cleaners, rules/protect or rules/purge
// changes (TestEmbeddedVersionIsCurrent fails until you do): a stored bundle
// is only used while it is newer than the built-in rules, so a release that
// fixes a rule is never hidden by an older bundle.
const EmbeddedVersion = "2026.11.8.1"

// Where rules come from.
const (
	SourceEmbedded = "embedded"
	SourceBundle   = "bundle"
)

// Rules is the rule data in use.
type Rules struct {
	Cleaners fs.FS // cleanup rules: rules.Load(Cleaners)
	// Protect holds only a bundle's protection files, nil with the built-in
	// rules. It is never used on its own: protect.Load adds it to the
	// built-in list (package cli connects the two through protect.Extra).
	Protect fs.FS
	Purge   fs.FS // purge types: purge.Options.Types

	Source  string    // SourceEmbedded or SourceBundle
	Version string    // version of the rules in use
	Created time.Time // bundle only
	MinSapu string    // bundle only
	File    string    // bundle only: the stored bundle
	Warning string    // why an installed bundle is not used; empty when nothing is wrong
}

func rulesDir(e env.Env) string               { return filepath.Join(e.StateDir, "rules") }
func activeFile(e env.Env) string             { return filepath.Join(rulesDir(e), "active") }
func bundleFile(e env.Env, ver string) string { return filepath.Join(rulesDir(e), ver+".json") }

// Active returns the rules to use: the installed bundle when active names
// one that still verifies and loads, else the built-in rules.
func Active(e env.Env) (Rules, error) {
	r, err := embedded()
	if err != nil {
		return Rules{}, err
	}
	ver, err := activeVersion(e)
	if err != nil {
		r.Warning = fmt.Sprintf("using the built-in rules: %v", err)
		return r, nil
	}
	if ver == SourceEmbedded {
		return r, nil
	}
	b, err := stored(e, ver)
	if err != nil {
		r.Warning = fmt.Sprintf("using the built-in rules, not the installed rules %s: %v (see sapu rules status)", ver, err)
		return r, nil
	}
	cleaners, protectFiles, purgeFiles := b.FS()
	return Rules{
		Cleaners: cleaners, Protect: protectFiles, Purge: purgeFiles,
		Source: SourceBundle, Version: b.Version, Created: b.Created, MinSapu: b.MinSapu,
		File: bundleFile(e, ver),
	}, nil
}

// embedded returns the rules built into the binary.
func embedded() (Rules, error) {
	cleaners, err := fs.Sub(ruledata.FS, "cleaners")
	if err != nil {
		return Rules{}, err
	}
	purgeFiles, err := fs.Sub(ruledata.FS, "purge")
	if err != nil {
		return Rules{}, err
	}
	return Rules{Cleaners: cleaners, Purge: purgeFiles, Source: SourceEmbedded, Version: EmbeddedVersion}, nil
}

// activeVersion reads the active file: a bundle version, or SourceEmbedded
// when the file says so or does not exist.
func activeVersion(e env.Env) (string, error) {
	data, err := os.ReadFile(activeFile(e))
	if errors.Is(err, fs.ErrNotExist) {
		return SourceEmbedded, nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", activeFile(e), err)
	}
	ver := strings.TrimSpace(string(data))
	if ver != SourceEmbedded && !versionPattern.MatchString(ver) {
		return "", fmt.Errorf("%s names %q, not a version", activeFile(e), ver)
	}
	return ver, nil
}

// stored reads, verifies and checks the stored bundle ver.
func stored(e env.Env, ver string) (Bundle, error) {
	data, err := os.ReadFile(bundleFile(e, ver))
	if err != nil {
		return Bundle{}, err
	}
	sig, err := os.ReadFile(bundleFile(e, ver) + ".sig")
	if err != nil {
		return Bundle{}, err
	}
	if err := Verify(data, sig); err != nil {
		if errors.Is(err, ErrBadSignature) {
			return Bundle{}, errors.New("its signature no longer matches; the file changed after it was installed")
		}
		return Bundle{}, err
	}
	b, err := Check(data)
	if err != nil {
		return Bundle{}, fmt.Errorf("this Sapu cannot load it: %w", err)
	}
	if b.Version != ver {
		return Bundle{}, fmt.Errorf("the file holds version %s", b.Version)
	}
	if compareVersions(b.Version, EmbeddedVersion) <= 0 {
		return Bundle{}, fmt.Errorf("the built-in rules (%s) are newer", EmbeddedVersion)
	}
	return b, nil
}

// Install checks a bundle and its signature and makes it the rules in use.
// sapuVersion is this binary's version, compared with min_sapu.
func Install(e env.Env, data, sig []byte, sapuVersion string) (Bundle, error) {
	if err := Verify(data, sig); err != nil {
		return Bundle{}, err
	}
	b, err := Check(data)
	if err != nil {
		return Bundle{}, err
	}
	if !sapuAtLeast(sapuVersion, b.MinSapu) {
		return Bundle{}, fmt.Errorf("these rules need Sapu %s or newer; this is Sapu %s", b.MinSapu, sapuVersion)
	}
	cur, err := Active(e)
	if err != nil {
		return Bundle{}, err
	}
	if compareVersions(b.Version, cur.Version) <= 0 {
		return Bundle{}, fmt.Errorf("version %s is not newer than the rules in use (%s %s)", b.Version, cur.Source, cur.Version)
	}

	if err := os.MkdirAll(rulesDir(e), 0o700); err != nil {
		return Bundle{}, err
	}
	if err := safefile.Write(e.StateDir, bundleFile(e, b.Version), data, 0o600); err != nil {
		return Bundle{}, err
	}
	if err := safefile.Write(e.StateDir, bundleFile(e, b.Version)+".sig", sig, 0o600); err != nil {
		return Bundle{}, err
	}
	if err := safefile.Write(e.StateDir, activeFile(e), []byte(b.Version+"\n"), 0o600); err != nil {
		return Bundle{}, err
	}
	got, err := Active(e)
	if err != nil {
		return Bundle{}, err
	}
	if got.Source != SourceBundle || got.Version != b.Version {
		return Bundle{}, fmt.Errorf("installed, but not in use: %s", got.Warning)
	}
	return b, nil
}

// Reset makes the built-in rules the rules in use. It only writes the
// active file, and only when there is one.
func Reset(e env.Env) error {
	if _, err := os.Lstat(activeFile(e)); errors.Is(err, fs.ErrNotExist) {
		return nil // nothing was ever installed
	} else if err != nil {
		return err
	}
	return safefile.Write(e.StateDir, activeFile(e), []byte(SourceEmbedded+"\n"), 0o600)
}
