// Package slim shows how much space installed apps could save, for
// sapu slim (blueprint F8). It only reads. It never changes an app.
//
// Two things make an app bigger than this Mac needs:
//
//   - Foreign-architecture code. A universal binary carries a slice for
//     Intel (x86_64) and one for Apple Silicon (arm64). Only the slice for
//     this Mac's architecture runs. A fat file counts only when it also has
//     a slice for this Mac, so an Intel-only file is never counted.
//   - Unused languages. Every *.lproj folder is a language, except
//     Base.lproj and the languages the user has set. A folder whose name
//     Sapu cannot read as a language is not counted: when in doubt, skip.
//
// This is a report, on purpose. Removing code or .lproj folders from a
// signed app breaks its code signature and notarization: Gatekeeper may
// refuse to open it, the keychain may stop trusting it, and the app's next
// update puts the files back. Sapu shows the numbers and explains this; the
// real alternative is the Apple-Silicon-only build when the developer
// offers one.
package slim

import (
	"bytes"
	"cmp"
	"context"
	"debug/macho"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/khmuhtadin/sapu/internal/apps"
	"github.com/khmuhtadin/sapu/internal/env"
)

// Tests replace these.
var (
	// Arch is this Mac's architecture, as Go names it: "arm64" or "amd64".
	Arch = runtime.GOARCH
	// Languages returns the user's languages, most preferred first, like
	// "en-US" or "id-ID".
	Languages = readLanguages
)

// App is one app that could be smaller.
type App struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	ID            string `json:"bundle_id,omitempty"`
	ForeignBytes  int64  `json:"foreign_arch_bytes"` // slices for the other architecture
	LanguageBytes int64  `json:"language_bytes"`     // *.lproj folders the user does not use
}

// Savings is what the app could lose.
func (a App) Savings() int64 { return a.ForeignBytes + a.LanguageBytes }

// Report is the result of Scan.
type Report struct {
	Arch        string   // this Mac: "arm64" or "x86_64"
	ForeignArch string   // the other one
	Languages   []string // the user's languages
	Scanned     int      // apps looked at
	Apps        []App    // apps with something to save, the largest first
}

// Total is what all apps together could lose.
func (r Report) Total() (foreign, languages int64) {
	for _, a := range r.Apps {
		foreign += a.ForeignBytes
		languages += a.LanguageBytes
	}
	return foreign, languages
}

// Scan measures every app in apps.Dirs. Anything it cannot read is left out
// of the numbers, so they are a lower bound.
func Scan(ctx context.Context, e env.Env) (Report, error) {
	native, foreign, r := macho.CpuArm64, macho.CpuAmd64, Report{Arch: "arm64", ForeignArch: "x86_64"}
	if Arch == "amd64" {
		native, foreign, r = macho.CpuAmd64, macho.CpuArm64, Report{Arch: "x86_64", ForeignArch: "arm64"}
	}

	langs, err := Languages(ctx)
	if err != nil {
		return r, fmt.Errorf("read your languages: %w", err)
	}
	if len(langs) == 0 {
		return r, fmt.Errorf("read your languages: macOS reported none")
	}
	r.Languages = langs
	used := map[string]bool{}
	for _, l := range langs {
		if code, ok := languageCode(l); ok {
			used[code] = true
		}
	}

	list, err := apps.List(ctx, e)
	if err != nil {
		return r, err
	}
	r.Scanned = len(list)
	for _, a := range list {
		res, err := scanApp(ctx, a, native, foreign, used)
		if err != nil {
			return r, err
		}
		if res.Savings() > 0 {
			r.Apps = append(r.Apps, res)
		}
	}
	slices.SortFunc(r.Apps, func(x, y App) int {
		if c := cmp.Compare(y.Savings(), x.Savings()); c != 0 {
			return c
		}
		return strings.Compare(x.Name, y.Name)
	})
	return r, nil
}

func scanApp(ctx context.Context, a apps.App, native, foreign macho.Cpu, used map[string]bool) (App, error) {
	res := App{Name: a.Name, Path: a.Path, ID: a.ID}
	err := filepath.WalkDir(a.Path, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return nil // unreadable: not counted
		}
		if d.IsDir() {
			if strings.HasSuffix(strings.ToLower(d.Name()), ".lproj") {
				if code, ok := languageCode(strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))); ok && !used[code] {
					res.LanguageBytes += dirSize(p)
				}
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			res.ForeignBytes += foreignBytes(p, native, foreign)
		}
		return nil
	})
	return res, err
}

// dirSize adds up the sizes of the files in dir. Unreadable files are not counted.
func dirSize(dir string) int64 {
	var total int64
	// the callback never returns an error, so WalkDir has none to report
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// fatMagic starts every fat Mach-O file. Java class files start with it too;
// macho.OpenFat rejects those.
var fatMagic = []byte{0xca, 0xfe, 0xba, 0xbe}

// foreignBytes returns the size of the foreign slices of the file at path.
// It is 0 for a file that is not a fat Mach-O, or that has no slice for
// this Mac (removing the foreign slices would leave nothing to run).
func foreignBytes(path string, native, foreign macho.Cpu) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	magic := make([]byte, 4)
	_, err = f.Read(magic)
	_ = f.Close() // read only: a failed close loses nothing
	if err != nil || !bytes.Equal(magic, fatMagic) {
		return 0
	}

	fat, err := macho.OpenFat(path)
	if err != nil {
		return 0
	}
	defer func() { _ = fat.Close() }() // read only: a failed close loses nothing
	var foreignSize int64
	hasNative := false
	for _, arch := range fat.Arches {
		if arch.Cpu == native {
			hasNative = true
		}
		if arch.Cpu == foreign {
			foreignSize += int64(arch.Size)
		}
	}
	if !hasNative {
		return 0
	}
	return foreignSize
}

// legacyNames are the old English names of language folders, like
// "English.lproj".
var legacyNames = map[string]string{
	"english": "en", "french": "fr", "german": "de", "spanish": "es", "italian": "it",
	"dutch": "nl", "japanese": "ja", "swedish": "sv", "danish": "da", "finnish": "fi",
	"norwegian": "no", "portuguese": "pt", "korean": "ko", "russian": "ru",
	"polish": "pl", "turkish": "tr", "indonesian": "id",
}

// languageCode reads a language folder name ("en", "pt_BR", "zh-Hans",
// "French") or a user language ("en-US") as a language code like "en". It
// reports false for Base and for anything it does not recognise.
func languageCode(name string) (string, bool) {
	name = strings.ToLower(strings.ReplaceAll(name, "_", "-"))
	head, _, _ := strings.Cut(name, "-")
	if code, ok := legacyNames[head]; ok {
		return code, true
	}
	if len(head) < 2 || len(head) > 3 || head == "base" {
		return "", false
	}
	for _, c := range head {
		if c < 'a' || c > 'z' {
			return "", false
		}
	}
	return head, true
}

// readLanguages runs `defaults read -g AppleLanguages`, which prints
//
//	(
//	    "en-ID",
//	    "id-ID"
//	)
func readLanguages(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, "defaults", "read", "-g", "AppleLanguages").Output()
	if err != nil {
		return nil, err
	}
	return parseLanguages(string(out)), nil
}

func parseLanguages(out string) []string {
	var langs []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.Trim(strings.TrimSpace(line), `",`)
		if line != "" && line != "(" && line != ")" {
			langs = append(langs, line)
		}
	}
	return langs
}
