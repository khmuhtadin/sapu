// Package scan turns cleanup rules into plan items. Scan only reads the disk.
//
// For each rule that applies to this macOS version, every path that matches
// the rule's Paths (see the path semantics in package rules) and none of its
// Exclude globs becomes one item with:
//   - Source = rule ID, Risk/Regenerates/Action/Command/ProtectIf from the rule;
//     a rule whose id is reserved (rules.Reserved) is an error, so a cleaner
//     item never carries a dedicated command's source
//   - Explain = rule.Explain[e.Lang], falling back to "en"
//   - Bytes from walk.Size, Identity from plan.IdentityOf
//   - ID and Selected left empty: engine.Plan sets them
//
// Scan does not check protection, the whitelist or open files; engine.Plan does.
// Implemented in Wave 1 (W1-B).
package scan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/walk"
)

// Scan returns one item per path the rules match; see the package comment.
//
// Exclude globs are full paths with the same syntax as Paths. An item is
// dropped when an exclude matches it, or when an exclude points inside it:
// when in doubt the whole item is skipped. A trailing "/**" in an exclude
// matches the direct children of a folder, as it does in Paths.
//
// An item that cannot be read or measured (permission denied, vanished while
// scanning) is left out, and so is an empty one (0 bytes). A symlink is an
// item of its own, measures 0 bytes, and is never followed.
// On cancel, Scan returns ctx.Err() and no items.
func Scan(ctx context.Context, e env.Env, rs []rules.Rule, progress walk.Progress) ([]plan.Item, error) {
	tot := &totals{progress: progress}
	var items []plan.Item
	macOS := ""

	for _, r := range rs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if rules.Reserved(r.ID) {
			// rules.Validate refuses it; this holds for rules made any other way.
			return nil, fmt.Errorf("rule %s: the id is reserved for a dedicated command", r.ID)
		}
		if r.MinMacOS != "" {
			if macOS == "" {
				v, err := unix.Sysctl("kern.osproductversion")
				if err != nil {
					return nil, fmt.Errorf("read macOS version: %w", err)
				}
				macOS = v
			}
			older, err := versionBefore(macOS, r.MinMacOS)
			if err != nil {
				return nil, fmt.Errorf("rule %s: %w", r.ID, err)
			}
			if older {
				continue
			}
		}

		paths, err := matches(e, r)
		if err != nil {
			return nil, fmt.Errorf("rule %s: %w", r.ID, err)
		}
		for _, path := range paths {
			id, err := plan.IdentityOf(path)
			if err != nil {
				continue // gone or unreadable
			}
			bytes, _, err := walk.Size(ctx, path, tot.report)
			tot.next()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if err != nil {
				continue // vanished while scanning
			}
			if bytes == 0 && id.Mode&syscall.S_IFMT != syscall.S_IFLNK {
				continue // an empty folder only adds noise; a symlink measures 0 but is its own item
			}
			items = append(items, plan.Item{
				Source:      r.ID,
				Path:        path,
				Bytes:       bytes,
				Risk:        r.Risk,
				Regenerates: r.Regenerates != nil && *r.Regenerates,
				Explain:     explainText(r, e.Lang),
				Action:      r.Action,
				Command:     r.Command,
				ProtectIf:   r.ProtectIf,
				Identity:    id,
			})
		}
	}
	tot.finish()
	return items, nil
}

func explainText(r rules.Rule, lang string) string {
	if text := r.Explain[lang]; text != "" {
		return text
	}
	return r.Explain["en"]
}

// matches returns the paths a rule applies to: every match of its Paths,
// without duplicates and without anything excluded.
func matches(e env.Env, r rules.Rule) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, p := range r.Paths {
		found, err := expand(e, p)
		if err != nil {
			return nil, fmt.Errorf("path %q: %w", p, err)
		}
		for _, m := range found {
			if seen[m] || !inside(e.Home, m) {
				continue
			}
			seen[m] = true
			skip, err := excluded(e, r.Exclude, m)
			if err != nil {
				return nil, err
			}
			if !skip {
				out = append(out, m)
			}
		}
	}
	return out, nil
}

// expand lists what a rule path matches on disk. A trailing "/**" gives the
// direct children of every folder the rest of the pattern matches.
func expand(e env.Env, p string) ([]string, error) {
	prefix, children := strings.CutSuffix(globPattern(e, p), "/**")
	if !children {
		found, err := filepath.Glob(prefix)
		return cleaned(found), err
	}

	parents, err := filepath.Glob(prefix)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, parent := range parents {
		fi, err := os.Lstat(parent)
		if err != nil || !fi.IsDir() {
			continue // a symlink is not opened
		}
		entries, err := os.ReadDir(parent)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			out = append(out, filepath.Join(parent, entry.Name()))
		}
	}
	return cleaned(out), nil
}

func cleaned(paths []string) []string {
	for i, p := range paths {
		paths[i] = filepath.Clean(p)
	}
	return paths
}

// globPattern turns "~/x" into a glob pattern for the home folder's x.
// Wildcard characters in the home folder's own name are escaped.
func globPattern(e env.Env, p string) string {
	rest, ok := strings.CutPrefix(p, "~/")
	if !ok {
		return e.Expand(p)
	}
	return filepath.Join(escapeGlob(e.Home), rest)
}

func escapeGlob(s string) string {
	return strings.NewReplacer(`\`, `\\`, "*", `\*`, "?", `\?`, "[", `\[`).Replace(s)
}

// inside reports whether path is strictly below dir.
func inside(dir, path string) bool {
	return strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/")
}

// excluded reports whether any exclude glob removes path.
func excluded(e env.Env, excludes []string, path string) (bool, error) {
	for _, ex := range excludes {
		pattern := globPattern(e, ex)

		target := path
		if dir, children := strings.CutSuffix(pattern, "/**"); children {
			pattern, target = dir, filepath.Dir(path)
		}
		ok, err := filepath.Match(pattern, target)
		if err != nil {
			return false, fmt.Errorf("exclude %q: %w", ex, err)
		}
		if ok || pointsInside(e, ex, path) {
			return true, nil
		}
	}
	return false, nil
}

// pointsInside reports whether the exclude's fixed part is path itself or lies
// below it, so the exclude protects something inside path.
func pointsInside(e env.Env, exclude, path string) bool {
	fixed := e.Home
	rest, _ := strings.CutPrefix(exclude, "~/")
	for _, seg := range strings.Split(rest, "/") {
		if strings.ContainsAny(seg, "*?[") {
			break
		}
		fixed = filepath.Join(fixed, seg)
	}
	return fixed == path || inside(path, fixed)
}

// versionBefore reports whether macOS version have is older than need.
// Both look like "15", "15.1" or "15.1.1"; only major and minor are compared.
func versionBefore(have, need string) (bool, error) {
	h, err := majorMinor(have)
	if err != nil {
		return false, err
	}
	n, err := majorMinor(need)
	if err != nil {
		return false, err
	}
	return h[0] < n[0] || (h[0] == n[0] && h[1] < n[1]), nil
}

func majorMinor(v string) ([2]int, error) {
	var out [2]int
	parts := strings.Split(v, ".")
	if len(parts) > 3 {
		return out, fmt.Errorf("bad macOS version %q", v)
	}
	var nums []int
	for _, part := range parts[:min(len(parts), 2)] {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return out, fmt.Errorf("bad macOS version %q", v)
		}
		nums = append(nums, n)
	}
	copy(out[:], nums)
	return out, nil
}

// totals adds the progress of the single-item walks into running totals for
// the whole scan, at most about ten events a second.
type totals struct {
	progress walk.Progress
	done     walk.Event // finished walks
	current  walk.Event // the walk in progress
	nextAt   time.Time
}

func (t *totals) report(ev walk.Event) {
	t.current = ev
	if t.progress == nil || time.Now().Before(t.nextAt) {
		return
	}
	t.nextAt = time.Now().Add(100 * time.Millisecond)
	t.progress(t.sum(ev.Path))
}

// next closes the current walk.
func (t *totals) next() {
	t.done = t.sum("")
	t.current = walk.Event{}
}

func (t *totals) sum(path string) walk.Event {
	return walk.Event{
		Path:   path,
		Files:  t.done.Files + t.current.Files,
		Bytes:  t.done.Bytes + t.current.Bytes,
		Errors: t.done.Errors + t.current.Errors,
	}
}

func (t *totals) finish() {
	if t.progress != nil {
		t.progress(t.done)
	}
}
