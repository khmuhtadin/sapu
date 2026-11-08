package finders

import (
	"cmp"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// File types of `sapu large --type`, by extension.
var largeTypes = map[string][]string{
	"video":      {".mp4", ".mov", ".m4v", ".mkv", ".avi", ".wmv", ".webm", ".mpg", ".mpeg", ".flv"},
	"archive":    {".zip", ".tar", ".gz", ".tgz", ".bz2", ".xz", ".7z", ".rar", ".zst"},
	"disk-image": {".dmg", ".iso", ".img", ".vmdk", ".vdi", ".qcow2"},
	"any":        nil,
}

// LargeTypes lists the values --type accepts.
const LargeTypes = "video, archive, disk-image or any"

// ValidLargeType reports whether kind is a value --type accepts.
func ValidLargeType(kind string) bool {
	_, ok := largeTypes[kind]
	return ok
}

type LargeOptions struct {
	MinSize   int64         // allocated bytes
	OlderThan time.Duration // not opened and not changed for this long
	Type      string        // a key of largeTypes
	Now       time.Time
}

// File is a file a finder found: the plan item, when it was last used or
// changed, and its type (the extension).
type File struct {
	Item plan.Item
	Used time.Time
	Type string
}

// Large finds big files nobody has opened or changed for a while, largest
// first. It never enters ~/Library, Sapu's state folder, app bundles, Photos
// libraries or git repositories, and it refuses a root inside one of them.
// Items are risky: only the user knows whether they are still needed.
func Large(ctx context.Context, e env.Env, roots []string, o LargeOptions) ([]File, error) {
	exts, ok := largeTypes[o.Type]
	if !ok {
		return nil, fmt.Errorf("unknown type %q: use %s", o.Type, LargeTypes)
	}
	skip := skipper(e)
	var files []File
	for _, root := range roots {
		root, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		for p := root; ; p = filepath.Dir(p) {
			if skip(p) {
				return nil, fmt.Errorf("sapu large does not look inside %s", p)
			}
			if p == filepath.Dir(p) {
				break
			}
		}
		err = walkFiles(ctx, e.Home, root, skip, func(path string, fi fs.FileInfo) error {
			ext := strings.ToLower(filepath.Ext(path))
			used := lastUsed(fi)
			if exts != nil && !slices.Contains(exts, ext) ||
				allocated(fi) < o.MinSize ||
				o.Now.Sub(used) < o.OlderThan {
				return nil
			}
			explain := fmt.Sprintf("Not opened or changed since %s. Only you can tell whether you still need it.", day(used))
			item, ok, err := newItem(ctx, "large", path, rules.RiskRisky, false, explain)
			if err != nil || !ok || item.Bytes < o.MinSize {
				return err
			}
			files = append(files, File{Item: item, Used: used, Type: strings.TrimPrefix(ext, ".")})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	slices.SortFunc(files, func(a, b File) int {
		return cmp.Or(cmp.Compare(b.Item.Bytes, a.Item.Bytes), cmp.Compare(a.Item.Path, b.Item.Path))
	})
	return files, nil
}

// skipper says which folders are never entered: ~/Library, the state
// folder, and the folders skipFolder names. Only a folder with the right name
// is compared with the real ~/Library or state folder, by identity, so a path
// spelled through a symlink is caught too.
func skipper(e env.Env) func(path string) bool {
	special := []string{filepath.Join(e.Home, "Library"), e.StateDir}
	return func(path string) bool {
		name := filepath.Base(path)
		if skipFolder(name) {
			return true
		}
		for _, s := range special {
			if !strings.EqualFold(filepath.Base(s), name) {
				continue
			}
			info, err := os.Stat(path)
			si, serr := os.Stat(s)
			if err == nil && serr == nil && os.SameFile(info, si) {
				return true
			}
		}
		return false
	}
}
