package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLicenseFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"LICENSE.txt", "NOTICE", "README.md", "main.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := licenseFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "LICENSE.txt,NOTICE" {
		t.Fatalf("got %v", got)
	}
}

func TestLicenseFilesMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := licenseFiles(dir); err == nil {
		t.Fatal("a module without a license file must be an error")
	}
}

// TestNoticesUpToDate fails when go.mod changed and nobody ran `make notices`.
func TestNoticesUpToDate(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	// Never download modules from a test.
	t.Setenv("GOPROXY", "off")
	got, err := generate(root)
	if err != nil {
		if strings.Contains(err.Error(), "module lookup disabled by GOPROXY=off") {
			t.Skipf("module cache is incomplete, run go mod download to check %s: %v", outputFile, err)
		}
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(root, outputFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s is out of date; run `make notices` and commit it", outputFile)
	}
}
