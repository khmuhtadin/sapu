package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
)

// quarantineSettings writes a sapu.toml with the given [quarantine] values.
func quarantineSettings(t *testing.T, e env.Env, maxAge, maxSize string) {
	t.Helper()
	writeSettings(t, e, "[quarantine]\nmax_age = \""+maxAge+"\"\nmax_size = \""+maxSize+"\"\n")
}

// dateIn is the "Mon 2 Jan" text of now+d. A run that straddles midnight
// may print the next day, so tests accept both.
func dateIn(d time.Duration) []string {
	return []string{time.Now().Add(d).Format("Mon 2 Jan"), time.Now().Add(d + time.Minute).Format("Mon 2 Jan")}
}

func containsAny(text string, options []string) bool {
	for _, o := range options {
		if strings.Contains(text, o) {
			return true
		}
	}
	return false
}

// sapu quarantine expire uses max_age of sapu.toml: a 3 day old run goes
// with max_age 2d, and stays with the default 7d or with 30d.
func TestQuarantineExpireUsesConfiguredAge(t *testing.T) {
	for _, tc := range []struct {
		maxAge  string // "" means no sapu.toml
		expires bool
	}{{"2d", true}, {"30d", false}, {"", false}} {
		e := newHome(t)
		if tc.maxAge != "" {
			quarantineSettings(t, e, tc.maxAge, "20GB")
		}
		run, _, _ := sapu(t, e, "", "clean", "--yes")
		ageRun(t, e, runIDOf(t, run), 3*24*time.Hour)

		out, _, code := sapu(t, e, "", "quarantine", "expire")
		if code != 0 {
			t.Fatalf("max_age %q: exit %d\n%s", tc.maxAge, code, out)
		}
		if got := strings.Contains(out, "Deleted"); got != tc.expires {
			t.Errorf("max_age %q: expired = %v, want %v\n%s", tc.maxAge, got, tc.expires, out)
		}
		if !tc.expires {
			want := "Runs are kept for 7 days, within 20.0 GB."
			if tc.maxAge == "30d" {
				want = "Runs are kept for 30 days, within 20.0 GB."
			}
			if !strings.Contains(out, want) {
				t.Errorf("max_age %q: output lacks %q:\n%s", tc.maxAge, want, out)
			}
		}
	}
}

// The list shows the earliest expiry from max_age.
func TestQuarantineListUsesConfiguredAge(t *testing.T) {
	e := newHome(t)
	quarantineSettings(t, e, "2d", "20GB")
	sapu(t, e, "", "clean", "--yes")
	out, _, code := sapu(t, e, "", "quarantine", "list")
	if code != 0 || !containsAny(out, dateIn(2*24*time.Hour)) {
		t.Errorf("exit %d, want the date in 2 days:\n%s", code, out)
	}
}

// sapu clean says how long the quarantine keeps items and how big it may get
// in the words of sapu.toml.
func TestCleanResultTextUsesConfiguredQuarantine(t *testing.T) {
	e := newHome(t)
	quarantineSettings(t, e, "3d", "5GB")
	out, _, code := sapu(t, e, "", "clean", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !containsAny(out, dateIn(3*24*time.Hour)) || !strings.Contains(out, "quarantine passes 5.0 GB") {
		t.Errorf("the result does not name 3d and 5 GB:\n%s", out)
	}
	if strings.Contains(out, "20.0 GB") {
		t.Errorf("the result names the built-in 20.0 GB:\n%s", out)
	}
}

func TestWriteResultAndExpiresAfterUseTheConfig(t *testing.T) {
	cfg := config.Defaults()
	cfg.MaxAge, cfg.MaxBytes = 48*time.Hour, 3_000_000_000
	start := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	if got := expiresAfter(start, cfg); !got.Equal(start.Add(48 * time.Hour)) {
		t.Errorf("expiresAfter = %s", got)
	}
	var buf strings.Builder
	res := &engine.Result{RunID: "r1", Moved: []plan.Item{{ID: "a", Path: "/h/a"}}, Bytes: 10}
	writeResult(&buf, "/nonexistent/home", res, cfg, start, false, false)
	for _, want := range []string{"until at least " + start.Add(48*time.Hour).Format("Mon 2 Jan"), "quarantine passes 3.0 GB"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("result lacks %q:\n%s", want, buf.String())
		}
	}
}

// sapu dupes applies with max_age and max_size of sapu.toml: an older run
// that is 3 days old expires under max_age 2d, and not under the default.
func TestDupesUsesConfiguredQuarantine(t *testing.T) {
	for _, tc := range []struct {
		maxAge  string
		expires bool
	}{{"2d", true}, {"", false}} {
		e := newDupesHome(t)
		if tc.maxAge != "" {
			quarantineSettings(t, e, tc.maxAge, "4GB")
		}
		out, errOut, code := sapu(t, e, "", "dupes", "--min-size", "1KB", "--yes")
		if code != 0 {
			t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
		}
		ageRun(t, e, runIDOf(t, out), 3*24*time.Hour)

		dupesFile(t, e, "Downloads/again.bin", []byte(strings.Repeat("n", 5000)))
		dupesFile(t, e, "Documents/again copy.bin", []byte(strings.Repeat("n", 5000)))
		out, errOut, code = sapu(t, e, "", "dupes", "--min-size", "1KB", "--yes")
		if code != 0 {
			t.Fatalf("second run: exit %d\n%s\n%s", code, out, errOut)
		}
		if got := strings.Contains(out, "from older runs whose quarantine had expired"); got != tc.expires {
			t.Errorf("max_age %q: older run expired = %v, want %v\n%s", tc.maxAge, got, tc.expires, out)
		}
		if tc.maxAge != "" && (!containsAny(out, dateIn(2*24*time.Hour)) || !strings.Contains(out, "quarantine passes 4.0 GB")) {
			t.Errorf("the result does not name 2d and 4 GB:\n%s", out)
		}
	}
}

func TestWhitelistListShowsSapuToml(t *testing.T) {
	e := env.New(t.TempDir())
	writeSettings(t, e, "[whitelist]\npaths = [\"~/Projects/keep\"]\n")
	sapu(t, e, "", "whitelist", "add", "~/Documents/keep")

	out, _, code := sapu(t, e, "", "whitelist", "list")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || lines[0] != "~/Documents/keep" ||
		!strings.HasPrefix(lines[1], "~/Projects/keep  (from sapu.toml, read-only") {
		t.Errorf("list:\n%s", out)
	}

	out, _, _ = sapu(t, e, "", "whitelist", "list", "--json")
	validate(t, "whitelist-list", out)
	var d whitelistListData
	decodeData(t, out, &d)
	if len(d.Entries) != 1 || d.Entries[0] != "~/Documents/keep" || len(d.ConfigEntries) != 1 || d.ConfigEntries[0] != "~/Projects/keep" {
		t.Errorf("data %+v", d)
	}

	// Only sapu.toml: not "empty".
	e2 := env.New(t.TempDir())
	writeSettings(t, e2, "[whitelist]\npaths = [\"~/Projects/keep\"]\n")
	if out, _, _ := sapu(t, e2, "", "whitelist", "list"); strings.Contains(out, "empty") {
		t.Errorf("a whitelist with a sapu.toml path is not empty:\n%s", out)
	}
}
