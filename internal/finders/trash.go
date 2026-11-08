package finders

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// Volumes is the folder that holds mounted volumes. Tests replace it.
var Volumes = "/Volumes"

const trashExplain = "You already put this in the Trash. Sapu moves it to quarantine, so undo can bring it back for 7 days."

// Trash lists everything in ~/.Trash (hidden files too) and in
// /Volumes/*/.Trashes/<uid> of every mounted volume. Items are moderate and
// the user already threw them away. It returns nothing for a Trash that is
// empty or missing, and an *AccessError when macOS blocks reading it.
func Trash(ctx context.Context, e env.Env) ([]plan.Item, error) {
	dirs := []string{filepath.Join(e.Home, ".Trash")}
	vols, err := os.ReadDir(Volumes)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, readError(e.Home, Volumes, err)
	}
	for _, v := range vols {
		// "Macintosh HD" is a link to the startup disk, whose Trash is ~/.Trash.
		if v.Type()&fs.ModeSymlink != 0 || !v.IsDir() {
			continue
		}
		dirs = append(dirs, filepath.Join(Volumes, v.Name(), ".Trashes", strconv.Itoa(os.Getuid())))
	}

	var items []plan.Item
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, readError(e.Home, dir, fmt.Errorf("read %s: %w", dir, err))
		}
		for _, entry := range entries {
			item, ok, err := newItem(ctx, "trash", filepath.Join(dir, entry.Name()), rules.RiskModerate, false, trashExplain)
			if err != nil {
				return nil, err
			}
			if ok {
				items = append(items, item)
			}
		}
	}
	return items, nil
}
