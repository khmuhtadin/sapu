package tidy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
)

func TestAFolderThatCannotBeReadIsNotEmpty(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read everything")
	}
	e := env.New(t.TempDir())
	locked := filepath.Join(e.Home, "Downloads/locked")
	if err := os.MkdirAll(filepath.Join(locked, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(e.Home, "Downloads/outer/locked"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(locked, 0o755); err != nil {
			t.Error(err)
		}
	})

	found, err := Find(context.Background(), e, DefaultRoots(e))
	if err != nil {
		t.Fatal(err)
	}
	// outer/locked is a plain empty folder, so outer is the topmost; "locked" itself is unknown.
	if len(found) != 1 || found[0].Path != filepath.Join(e.Home, "Downloads/outer") {
		t.Errorf("found %+v", found)
	}
}

func TestACancelledContextStops(t *testing.T) {
	e := env.New(t.TempDir())
	if err := os.MkdirAll(filepath.Join(e.Home, "Downloads/a"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Find(ctx, e, DefaultRoots(e)); err == nil {
		t.Error("no error after cancel")
	}
}
