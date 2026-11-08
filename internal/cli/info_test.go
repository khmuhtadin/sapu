package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/doctor"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/explain"
	"github.com/khmuhtadin/sapu/internal/protect"
)

// writeFile creates ~/rel with n bytes of content.
func writeHomeFile(t *testing.T, e env.Env, rel string, n int) string {
	t.Helper()
	p := filepath.Join(e.Home, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), n), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// checkHuman fails when text output has colour codes or a line wider than 80.
func checkHuman(t *testing.T, out string) {
	t.Helper()
	if strings.Contains(out, "\x1b") {
		t.Errorf("output to a pipe must not have colour codes:\n%q", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if n := utf8.RuneCountInString(line); n > 80 {
			t.Errorf("line is %d characters wide, want at most 80: %q", n, line)
		}
	}
}

// junkHome has safe junk, moderate junk, and a big folder on the whitelist.
func junkHome(t *testing.T) env.Env {
	t.Helper()
	e := env.New(t.TempDir())
	writeHomeFile(t, e, "Library/Logs/DiagnosticReports/a.ips", 10_000)
	old := writeHomeFile(t, e, "Library/iTunes/iPhone Software Updates/x.ipsw", 20_000)
	longAgo := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(old, longAgo, longAgo); err != nil {
		t.Fatal(err)
	}
	// A match for the logs.homebrew rule that the user protects.
	writeHomeFile(t, e, "Library/Logs/Homebrew/big.log", 2_000_000)
	if err := protect.AddWhitelist(e, "~/Library/Logs/Homebrew"); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestSummaryJSON(t *testing.T) {
	e := junkHome(t)
	out, _, code := sapu(t, e, "", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	validate(t, "summary", out)

	var env struct {
		Command string
		Data    summaryData
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	d := env.Data
	if env.Command != "summary" {
		t.Errorf("command = %q", env.Command)
	}
	if d.Reclaimable.SafeBytes < 10_000 || d.Reclaimable.SafeBytes > 500_000 {
		t.Errorf("safe bytes = %d: the protected 2 MB log must not count", d.Reclaimable.SafeBytes)
	}
	if d.Reclaimable.ModerateBytes < 20_000 {
		t.Errorf("moderate bytes = %d, want at least 20000", d.Reclaimable.ModerateBytes)
	}
	if d.Reclaimable.SelectedBytes != d.Reclaimable.SafeBytes+d.Reclaimable.ModerateBytes {
		t.Errorf("selected %d is not safe + moderate", d.Reclaimable.SelectedBytes)
	}
	if d.ProtectedItems != 1 {
		t.Errorf("protected items = %d, want 1", d.ProtectedItems)
	}
	if d.TotalBytes <= 0 || d.FreeBytes <= 0 || d.FreeBytes > d.TotalBytes {
		t.Errorf("free %d of total %d", d.FreeBytes, d.TotalBytes)
	}
	if d.FullDiskAccess {
		t.Error("a temp home has no protected files to read, so Full Disk Access cannot be confirmed")
	}
}

func TestSummaryText(t *testing.T) {
	t.Setenv("COLUMNS", "80")
	e := junkHome(t)
	out, _, code := sapu(t, e, "", "")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	checkHuman(t, out)
	for _, want := range []string{
		" free", "FOUND FOR REVIEW", "SAFE", "MODERATE", "RISKY            0 B",
		"Left alone      1 protected", "Disk access     Partial", "sapu clean", "sapu explain",
		"sapu analyze", "sapu doctor",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
}

func TestAnalyzeJSON(t *testing.T) {
	e := env.New(t.TempDir())
	writeHomeFile(t, e, "big/a.bin", 50_000)
	writeHomeFile(t, e, "small/b.txt", 1)
	out, _, code := sapu(t, e, "", "analyze", e.Home, "--json", "--top", "1")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	validate(t, "analyze", out)

	var env struct{ Data analyzeData }
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	root := env.Data.Root
	if env.Data.Depth != 2 || env.Data.Top != 1 {
		t.Errorf("depth %d top %d, want 2 and 1", env.Data.Depth, env.Data.Top)
	}
	if len(root.Children) != 1 || root.Children[0].Name != "big" {
		t.Fatalf("want only the biggest child, got %+v", root.Children)
	}
	if root.Files != 2 {
		t.Errorf("totals cover the whole tree: files = %d, want 2", root.Files)
	}
}

func TestAnalyzeDefaultsToHome(t *testing.T) {
	e := env.New(t.TempDir())
	writeHomeFile(t, e, "big/a.bin", 50_000)
	out, _, code := sapu(t, e, "", "analyze", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out, filepath.Base(e.Home)) {
		t.Errorf("root should be the home folder:\n%s", out)
	}
}

func TestAnalyzeTable(t *testing.T) {
	e := env.New(t.TempDir())
	writeHomeFile(t, e, "big/a.bin", 50_000)
	writeHomeFile(t, e, "small/b.txt", 1)
	writeHomeFile(t, e, "big/inner/c.bin", 1)

	// stdin is not a terminal, so this prints a table instead of the explorer.
	out, _, code := sapu(t, e, "", "analyze")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	checkHuman(t, out)
	if !strings.HasPrefix(out, "~ ") {
		t.Errorf("header should name the home folder as ~:\n%s", out)
	}
	big, small := strings.Index(out, "big/"), strings.Index(out, "small/")
	if big < 0 || small < 0 || big > small {
		t.Errorf("want big/ before small/:\n%s", out)
	}
	if strings.Contains(out, "inner/") {
		t.Errorf("default depth is 1:\n%s", out)
	}

	out, _, _ = sapu(t, e, "", "analyze", "--depth", "2")
	if !strings.Contains(out, "  inner/") {
		t.Errorf("--depth 2 should show inner/ under big/:\n%s", out)
	}
	out, _, _ = sapu(t, e, "", "analyze", "--top", "1")
	if !strings.Contains(out, "big/") || strings.Contains(out, "small/") {
		t.Errorf("--top 1 should show big/ only:\n%s", out)
	}
}

func TestAnalyzeErrors(t *testing.T) {
	e := env.New(t.TempDir())
	out, errOut, code := sapu(t, e, "", "analyze", filepath.Join(e.Home, "nope"), "--json")
	if code != 1 {
		t.Errorf("missing path: exit %d, want 1", code)
	}
	validate(t, "analyze", out) // an error envelope has no data
	if !strings.Contains(out, `"ok": false`) || !strings.Contains(errOut, "does not exist") {
		t.Errorf("want one error envelope and a message on stderr:\n%s\n%s", out, errOut)
	}
	if _, _, code := sapu(t, e, "", "analyze", "--top", "0"); code != 2 {
		t.Errorf("--top 0: exit %d, want 2", code)
	}
}

func TestJSONUsageError(t *testing.T) {
	out, _, code := sapu(t, env.New(t.TempDir()), "", "version", "--json", "--no-such-flag")
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	validate(t, "version", out)
	if !strings.Contains(out, `"code": "usage"`) {
		t.Errorf("want a usage error envelope:\n%s", out)
	}
}

func TestExplainJSON(t *testing.T) {
	out, _, code := sapu(t, env.New(t.TempDir()), "", "explain", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	validate(t, "explain", out)
}

func TestExplainText(t *testing.T) {
	rows := explainData{
		Rows: []explain.Row{
			{ID: "a", Title: "Time Machine local snapshots (3)", Bytes: -1, Unmeasured: "macOS does not report how much snapshots use",
				Explain: "Hourly snapshots kept on this disk so you can restore files when the backup disk is away.",
				Action:  "Let macOS remove them, or thin them with the command below.",
				Command: []string{"tmutil", "thinlocalsnapshots", "/", "10000000000", "4"}, NeedsAdmin: true},
			{ID: "b", Title: "Xcode", Bytes: 3_200_000_000, Explain: "Build files and simulators.",
				Action: "Remove old simulators.", Command: []string{"xcrun", "simctl", "delete", "unavailable"}},
		},
		MeasuredBytes: 3_200_000_000,
		Unmeasured:    1,
	}
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	printExplain(cmd, rows)
	out := buf.String()

	checkHuman(t, out)
	for _, want := range []string{
		"Time Machine local snapshots (3)", "not measured", "Why: macOS does not report",
		"$ sudo tmutil thinlocalsnapshots / 10000000000 4", "(needs admin rights)",
		"$ xcrun simctl delete unavailable", "3.2 GB",
		"Measured total: 3.2 GB at least (1 not measured", "Sapu never runs these.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "$ sudo xcrun") {
		t.Errorf("only admin commands get sudo:\n%s", out)
	}
	if !strings.HasSuffix(out, "Sapu never runs these.\n") {
		t.Errorf("must close with the promise:\n%s", out)
	}
}

func TestDoctorJSON(t *testing.T) {
	out, _, code := sapu(t, env.New(t.TempDir()), "", "doctor", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	validate(t, "doctor", out)

	var env struct{ Data doctorData }
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	for _, c := range env.Data.Checks {
		if c.ID == "full_disk_access" && c.OK {
			t.Error("a temp home cannot confirm Full Disk Access, so that check must fail")
		}
	}
}

func TestDoctorText(t *testing.T) {
	// The real disk must not decide the count below.
	old := doctor.FreeBytes
	t.Cleanup(func() { doctor.FreeBytes = old })
	doctor.FreeBytes = func(string) (uint64, error) { return 50_000_000_000, nil }
	e := env.New(t.TempDir())
	out, _, code := sapu(t, e, "", "doctor")
	if code != 0 {
		t.Fatalf("a failed check must not fail the command: exit %d", code)
	}
	// A temp folder path is far longer than ~/.sapu.
	checkHuman(t, strings.ReplaceAll(out, e.Home, "~"))
	for _, want := range []string{"✗ Full Disk Access", "Fix: Open System Settings", "✓ Sapu state folder", "1 check needs attention"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}
}

func TestHelpListsCommands(t *testing.T) {
	out, _, code := sapu(t, env.New(t.TempDir()), "", "--help")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("no colour codes in a pipe:\n%q", out)
	}
	// Each command's own one-line description is owned by its command file,
	// so only the lines written in root.go are checked for width.
	for _, line := range strings.Split(rootLong, "\n") {
		if utf8.RuneCountInString(line) > 80 {
			t.Errorf("root help line is wider than 80: %q", line)
		}
	}
	for _, want := range []string{
		"quarantine folder first", "Clean:", "Look:", "Settings:", "completion",
		"clean", "purge", "apply", "undo", "quarantine", "analyze", "explain", "history", "doctor", "whitelist", "version",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("--help lacks %q:\n%s", want, out)
		}
	}
	if clean, look := strings.Index(out, "Clean:"), strings.Index(out, "Look:"); clean > look {
		t.Errorf("Clean must come before Look:\n%s", out)
	}
}
