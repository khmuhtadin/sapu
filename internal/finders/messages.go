package finders

import (
	"cmp"
	"context"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
)

// MessagesDir is where Messages keeps attachments, inside the home folder.
const MessagesDir = "Library/Messages/Attachments"

type Attachment struct {
	Path     string
	Bytes    int64
	Modified time.Time
	Type     string // the extension, e.g. "heic"; empty when there is none
}

type MessagesOptions struct {
	MinSize   int64
	OlderThan time.Duration
	Now       time.Time
}

// Messages lists the largest attachments, biggest first. It only reads: there
// are no plan items, because removing files behind Messages' back leaves
// broken references.
func Messages(ctx context.Context, e env.Env, o MessagesOptions) ([]Attachment, error) {
	var found []Attachment
	root := filepath.Join(e.Home, MessagesDir)
	err := walkFiles(ctx, e.Home, root, func(string) bool { return false }, func(path string, fi fs.FileInfo) error {
		if allocated(fi) >= o.MinSize && o.Now.Sub(fi.ModTime()) >= o.OlderThan {
			found = append(found, Attachment{
				Path:     path,
				Bytes:    allocated(fi),
				Modified: fi.ModTime(),
				Type:     strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(found, func(a, b Attachment) int {
		return cmp.Or(cmp.Compare(b.Bytes, a.Bytes), cmp.Compare(a.Path, b.Path))
	})
	return found, nil
}
