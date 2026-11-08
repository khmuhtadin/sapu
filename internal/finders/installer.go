package finders

import (
	"cmp"
	"context"
	"fmt"
	"html"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

var installerExts = []string{".dmg", ".pkg", ".mpkg", ".iso", ".xip"}

// MountedImages returns the paths of the disk images that are mounted now.
// Tests replace it.
var MountedImages = func(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, "hdiutil", "info", "-plist").Output()
	if err != nil {
		return nil, fmt.Errorf("hdiutil info: %w", err)
	}
	return imagePaths(out), nil
}

var imagePathRe = regexp.MustCompile(`<key>image-path</key>\s*<string>([^<]*)</string>`)

// imagePaths reads the image-path values from the plist of `hdiutil info -plist`.
func imagePaths(plist []byte) []string {
	var paths []string
	for _, m := range imagePathRe.FindAllSubmatch(plist, -1) {
		paths = append(paths, html.UnescapeString(string(m[1])))
	}
	return paths
}

type InstallerOptions struct {
	OlderThan time.Duration // not modified for this long
	Now       time.Time
}

// Installers finds .dmg, .pkg, .mpkg, .iso and .xip files that have not been
// modified for a while, largest first. It looks only at the files directly in
// each root, not in subfolders: a disk image inside a project folder is a
// build result, not a download. A disk image that is mounted right now is
// not offered; it comes back as a skip. Items are moderate: an installer can
// be downloaded again. A root that does not exist is left out.
func Installers(ctx context.Context, e env.Env, roots []string, o InstallerOptions) ([]plan.Item, []plan.Skip, error) {
	noSubfolders := func(string) bool { return true }
	var mounted []fs.FileInfo // loaded when the first disk image turns up
	loaded := false
	var items []plan.Item
	var skips []plan.Skip
	for _, root := range roots {
		root, err := filepath.Abs(root)
		if err != nil {
			return nil, nil, err
		}
		err = walkFiles(ctx, e.Home, root, noSubfolders, func(path string, fi fs.FileInfo) error {
			ext := strings.ToLower(filepath.Ext(path))
			if !slices.Contains(installerExts, ext) || o.Now.Sub(fi.ModTime()) < o.OlderThan {
				return nil
			}
			if ext == ".dmg" || ext == ".iso" {
				if !loaded {
					paths, err := MountedImages(ctx)
					if err != nil {
						return fmt.Errorf("cannot tell which disk images are mounted, so no image is offered: %w", err)
					}
					for _, p := range paths {
						if info, err := os.Stat(p); err == nil {
							mounted = append(mounted, info)
						}
					}
					loaded = true
				}
				if isMounted(fi, mounted) {
					skips = append(skips, plan.Skip{Path: path, Source: "installer", Reason: plan.ReasonInUse, Detail: "mounted disk image"})
					return nil
				}
			}
			explain := fmt.Sprintf("Installer last changed on %s. You can download it again, so it is selected.", day(fi.ModTime()))
			item, ok, err := newItem(ctx, "installer", path, rules.RiskModerate, true, explain)
			if ok {
				items = append(items, item)
			}
			return err
		})
		if err != nil {
			return nil, nil, err
		}
	}
	slices.SortFunc(items, func(a, b plan.Item) int {
		return cmp.Or(cmp.Compare(b.Bytes, a.Bytes), cmp.Compare(a.Path, b.Path))
	})
	return items, skips, nil
}

func isMounted(fi fs.FileInfo, mounted []fs.FileInfo) bool {
	for _, m := range mounted {
		if os.SameFile(fi, m) {
			return true
		}
	}
	return false
}
