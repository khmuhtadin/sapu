// Package finders holds the finder commands of M2: large files, iPhone
// backups, Messages attachments, the Trash and old installers. Each finder
// only reads. The CLI turns what a finder returns into a plan and runs it
// through engine.Plan, so protection, whitelist and in-use checks apply as
// for every other command.
//
// What each finder reads is a package variable (Volumes, MountedImages,
// PlistValue), so tests can point them at temp folders and fake output.
// Folders inside the home folder come from env.Env.
//
// Reading a folder that macOS protects fails without Full Disk Access.
// Finders return *AccessError then, with a sentence that points to
// `sapu doctor`.
package finders

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/walk"
)

// AccessError says macOS did not let Sapu read a folder.
type AccessError struct{ Path string }

func (e *AccessError) Error() string {
	return fmt.Sprintf("macOS did not let Sapu read %s. Your terminal app probably needs Full Disk Access; `sapu doctor` shows how to turn it on", e.Path)
}

// readError turns a permission failure into an *AccessError and returns
// other errors unchanged. home is used to show the path as ~/...
func readError(home, path string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return &AccessError{Path: output.Tilde(home, path)}
	}
	return err
}

// ParseSize parses sizes like "500MB", "10M", "1.5GB" or "2048". Units are
// base 1000, as in Finder and in the sizes Sapu prints.
func ParseSize(s string) (int64, error) {
	t := strings.TrimSuffix(strings.ToUpper(strings.TrimSpace(s)), "B")
	mult := int64(1)
	for _, unit := range "KMGT" {
		mult *= 1000
		if num, ok := strings.CutSuffix(t, string(unit)); ok {
			return sizeOf(s, num, mult)
		}
	}
	return sizeOf(s, t, 1)
}

func sizeOf(original, number string, mult int64) (int64, error) {
	n, err := strconv.ParseFloat(strings.TrimSpace(number), 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q: use a number with an optional K, M, G or T, like 500MB", original)
	}
	return int64(n * float64(mult)), nil
}

// ParseAge is rules.ParseAge plus "y" for years of 365 days ("1y").
func ParseAge(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "y"); ok {
		years, err := strconv.Atoi(n)
		if err != nil || years < 0 {
			return 0, fmt.Errorf("invalid age %q", s)
		}
		return time.Duration(years) * 365 * 24 * time.Hour, nil
	}
	return rules.ParseAge(s)
}

// ParsePick parses "1,3,5" and ranges like "2-4" into item numbers, from 1.
func ParsePick(s string) ([]int, error) {
	var nums []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		first, last, isRange := strings.Cut(part, "-")
		from, err := strconv.Atoi(first)
		if err != nil || from < 1 {
			return nil, fmt.Errorf("%q is not an item number; use numbers like 1,3,5 or a range like 2-4", part)
		}
		to := from
		if isRange {
			to, err = strconv.Atoi(last)
			if err != nil || to < from {
				return nil, fmt.Errorf("%q is not a range; use 2-4, smallest number first", part)
			}
		}
		for n := from; n <= to; n++ {
			nums = append(nums, n)
		}
	}
	return nums, nil
}

// skipFolder reports folders that are never entered: app bundles, Photos
// libraries and git repositories (a pack file taken out of .git breaks the
// repository).
func skipFolder(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".app") || strings.HasSuffix(lower, ".photoslibrary") || lower == ".git"
}

// walkFiles calls visit for every regular file under root. It does not
// follow symlinks and does not enter a folder when skip says so. A root that
// does not exist is not an error; a root macOS blocks is an *AccessError.
// Entries further down that cannot be read are left out.
func walkFiles(ctx context.Context, home, root string, skip func(path string) bool, visit func(path string, fi fs.FileInfo) error) error {
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			if path == root {
				return err
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != root && skip(path) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil // gone since the folder was read
		}
		return visit(path, fi)
	})
	switch {
	case errors.Is(err, fs.ErrNotExist) && ctx.Err() == nil:
		return nil
	case err != nil:
		return readError(home, root, err)
	}
	return nil
}

// allocated is the space a file uses on disk, as walk counts it.
func allocated(fi fs.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512
	}
	return fi.Size()
}

// lastUsed is the later of the last access and the last change.
func lastUsed(fi fs.FileInfo) time.Time {
	t := fi.ModTime()
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		if a := time.Unix(st.Atimespec.Unix()); a.After(t) {
			t = a
		}
	}
	return t
}

// newItem measures path and builds its plan item, which is moved by quarantine.
// ok is false when the path vanished since it was found.
func newItem(ctx context.Context, source, path string, risk rules.Risk, regenerates bool, explain string) (item plan.Item, ok bool, err error) {
	size, _, err := walk.Size(ctx, path, nil)
	if errors.Is(err, fs.ErrNotExist) {
		return plan.Item{}, false, nil
	}
	if err != nil {
		return plan.Item{}, false, err
	}
	id, err := plan.IdentityOf(path)
	if errors.Is(err, fs.ErrNotExist) {
		return plan.Item{}, false, nil
	}
	if err != nil {
		return plan.Item{}, false, err
	}
	return plan.Item{
		Source:      source,
		Path:        path,
		Bytes:       size,
		Risk:        risk,
		Regenerates: regenerates,
		Explain:     explain,
		Action:      rules.ActionQuarantine,
		Identity:    id,
	}, true, nil
}

// day formats a time as 2026-03-14.
func day(t time.Time) string { return t.Format("2006-01-02") }
