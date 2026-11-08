package updates

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/apps"
	"github.com/khmuhtadin/sapu/internal/env"
)

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"1.2.4", "1.2.3", 1},
		{"1.2.3", "1.2.4", -1},
		{"1.10", "1.9", 1},
		{"2", "2.0.0", 0},
		{"2.0", "2.0.1", -1},
		{"10.0", "9.99", 1},
		{"3.0b1", "3.0", 0}, // text after the digits is ignored
		{"1.2.3 (beta)", "1.2.2", 1},
		{"", "0", 0},
		{"1.0", "", 1},
	} {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

const brewJSON = `{
  "formulae": [],
  "casks": [
    {"name": "ghostty", "installed_versions": ["1.3.1"], "current_version": "1.4.0", "pinned": false, "pinned_version": null},
    {"name": "ngrok", "installed_versions": ["3.39.10,aaa,a", "3.39.11,dy27,a"], "current_version": "3.39.13,msmi,a", "pinned": false, "pinned_version": null},
    {"name": "no-install", "installed_versions": [], "current_version": "2.0"}
  ]
}`

func TestParseBrew(t *testing.T) {
	got, err := parseBrew([]byte(brewJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d casks: %+v", len(got), got)
	}
	g := got[0]
	if g.Name != "ghostty" || g.Source != SourceHomebrew || g.Installed != "1.3.1" || g.Available != "1.4.0" {
		t.Errorf("ghostty: %+v", g)
	}
	if !slices.Equal(g.Command, []string{"brew", "upgrade", "--cask", "ghostty"}) || g.How != "Run: brew upgrade --cask ghostty" {
		t.Errorf("ghostty command: %v, how %q", g.Command, g.How)
	}
	// The newest installed version wins; what brew adds after a comma is dropped.
	if n := got[1]; n.Installed != "3.39.11" || n.Available != "3.39.13" {
		t.Errorf("ngrok: %+v", n)
	}
	if got[2].Installed != "" {
		t.Errorf("no-install: %+v", got[2])
	}

	if _, err := parseBrew([]byte("not json")); err == nil {
		t.Error("garbage was accepted")
	}
	if got, err := parseBrew([]byte(`{"formulae":[],"casks":[]}`)); err != nil || len(got) != 0 {
		t.Errorf("no casks: %v, %v", got, err)
	}
}

func TestParseMas(t *testing.T) {
	got := parseMas([]byte("497799835 Xcode (14.1 -> 14.2)\n1444383602 GoodNotes 5 (5.9.1 -> 5.9.2)\nnot a line\n\n"))
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if g := got[1]; g.Name != "GoodNotes 5" || g.Installed != "5.9.1" || g.Available != "5.9.2" || g.Source != SourceAppStore {
		t.Errorf("GoodNotes: %+v", g)
	}
}

func appcastXML(items ...string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<rss version="2.0" xmlns:sparkle="http://www.andymatuschak.org/xml-namespaces/sparkle">
<channel><title>App</title>` + strings.Join(items, "") + `</channel></rss>`
}

// itemAttrs puts the versions on the enclosure, the usual place.
func itemAttrs(short, build string) string {
	return fmt.Sprintf(`<item><title>%s</title><enclosure url="https://x.example/a.zip" sparkle:version="%s" sparkle:shortVersionString="%s" length="1" type="application/octet-stream"/></item>`, short, build, short)
}

// itemElems puts the versions in elements of the item.
func itemElems(short, build string) string {
	return fmt.Sprintf(`<item><sparkle:version>%s</sparkle:version><sparkle:shortVersionString>%s</sparkle:shortVersionString><enclosure url="https://x.example/a.zip"/></item>`, build, short)
}

func TestNewestItem(t *testing.T) {
	beta := `<item><sparkle:channel>beta</sparkle:channel><enclosure sparkle:version="900" sparkle:shortVersionString="9.0b1"/></item>`
	for name, c := range map[string]struct {
		feed        string
		short, make string
		err         bool
	}{
		"newest first":         {appcastXML(itemAttrs("2.0", "200"), itemAttrs("1.0", "100")), "2.0", "200", false},
		"newest last":          {appcastXML(itemAttrs("1.0", "100"), itemAttrs("2.0", "200"), itemAttrs("1.5", "150")), "2.0", "200", false},
		"elements":             {appcastXML(itemElems("1.0", "100"), itemElems("1.1", "110")), "1.1", "110", false},
		"betas are ignored":    {appcastXML(beta, itemAttrs("1.0", "100")), "1.0", "100", false},
		"only a build":         {appcastXML(`<item><enclosure sparkle:version="42"/></item>`), "", "42", false},
		"only a short":         {appcastXML(`<item><enclosure sparkle:shortVersionString="1.4"/></item>`), "1.4", "", false},
		"no items":             {appcastXML(), "", "", true},
		"only a beta":          {appcastXML(beta), "", "", true},
		"item without version": {appcastXML(`<item><title>x</title></item>`), "", "", true},
		"not xml":              {"<html>sorry", "", "", true},
	} {
		got, err := newestItem([]byte(c.feed))
		if (err != nil) != c.err || got.Short != c.short || got.Build != c.make {
			t.Errorf("%s: got %+v, %v; want short %q build %q error %v", name, got, err, c.short, c.make, c.err)
		}
	}
}

// --- Check -----------------------------------------------------------------

// commands is a set of canned answers keyed by the command line.
type commands struct {
	mu      sync.Mutex
	answers map[string]any // string output, or an error
	ran     []string
}

func (c *commands) run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ran = append(c.ran, line)
	switch v := c.answers[line].(type) {
	case string:
		return []byte(v), nil
	case error:
		return nil, v
	}
	return nil, errors.New("no canned answer for " + line)
}

const (
	brewCmd = "env HOMEBREW_NO_AUTO_UPDATE=1 brew outdated --cask --greedy --json=v2"
	masCmd  = "mas outdated"
)

// setup fakes the commands and points the app search at dir. have says which
// tools are installed. Transport is left alone unless a test sets it.
func setup(t *testing.T, answers map[string]any, have []string, dirs ...string) *commands {
	t.Helper()
	c := &commands{answers: answers}
	oldRun, oldLook, oldDirs, oldTransport, oldTimeout := RunCommand, LookPath, apps.Dirs, Transport, Timeout
	RunCommand = c.run
	LookPath = func(name string) (string, error) {
		if slices.Contains(have, name) {
			return "/fake/" + name, nil
		}
		return "", exec.ErrNotFound
	}
	apps.Dirs = dirs
	t.Cleanup(func() {
		RunCommand, LookPath, apps.Dirs, Transport, Timeout = oldRun, oldLook, oldDirs, oldTransport, oldTimeout
	})
	return c
}

// fakeApp builds dir/name.app with a real Info.plist. feed "" means no Sparkle.
func fakeApp(t *testing.T, dir, name, short, build, feed string) string {
	t.Helper()
	extra := ""
	if feed != "" {
		extra = "<key>SUFeedURL</key><string>" + feed + "</string>"
	}
	p := filepath.Join(dir, name+".app", "Contents", "Info.plist")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>test.` + name + `</string>
<key>CFBundleName</key><string>` + name + `</string>
<key>CFBundleShortVersionString</key><string>` + short + `</string>
<key>CFBundleVersion</key><string>` + build + `</string>
` + extra + `</dict></plist>
`
	if err := os.WriteFile(p, []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(p))
}

// feedServer serves appcasts over HTTPS. Transport trusts it.
func feedServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	Transport = srv.Client().Transport
	return srv
}

func serve(feeds map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := feeds[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	})
}

func find(r Result, name string) (Update, bool) {
	for _, u := range r.Updates {
		if u.Name == name {
			return u, true
		}
	}
	return Update{}, false
}

func noteFor(r Result, name string) string {
	for _, n := range r.Notes {
		if n.Name == name {
			return n.Message
		}
	}
	return ""
}

func TestSparkleNewerEqualOlder(t *testing.T) {
	srv := feedServer(t, serve(map[string]string{
		"/newer.xml": appcastXML(itemAttrs("1.0", "100"), itemAttrs("2.1", "210")),
		"/equal.xml": appcastXML(itemAttrs("3.0", "300")),
		"/older.xml": appcastXML(itemAttrs("1.0", "100")),
		"/build.xml": appcastXML(`<item><enclosure sparkle:version="505"/></item>`), // only a build number
		"/same.xml":  appcastXML(itemElems("7.0", "70")),
	}))
	dir := t.TempDir()
	fakeApp(t, dir, "Newer", "2.0", "200", srv.URL+"/newer.xml")
	fakeApp(t, dir, "Equal", "3.0", "300", srv.URL+"/equal.xml")
	fakeApp(t, dir, "Older", "1.5", "150", srv.URL+"/older.xml")
	fakeApp(t, dir, "Build", "5.0", "500", srv.URL+"/build.xml")
	fakeApp(t, dir, "Same", "7.0.0", "70", srv.URL+"/same.xml")
	fakeApp(t, dir, "NoFeed", "1.0", "1", "")
	setup(t, nil, nil, dir)

	r, err := Check(context.Background(), env.New(t.TempDir()), false)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, u := range r.Updates {
		names = append(names, u.Name)
	}
	if !slices.Equal(names, []string{"Build", "Newer"}) {
		t.Fatalf("updates for %v, want [Build Newer]; notes %v", names, r.Notes)
	}
	n, _ := find(r, "Newer")
	if n.Installed != "2.0" || n.Available != "2.1" || n.Source != SourceSparkle || n.How != "Open the app and choose Check for Updates." || len(n.Command) != 0 {
		t.Errorf("Newer: %+v", n)
	}
	if !strings.HasSuffix(n.Path, "Newer.app") {
		t.Errorf("path %q", n.Path)
	}
	// With no short version in the feed, build numbers are compared.
	if b, _ := find(r, "Build"); b.Installed != "500" || b.Available != "505" {
		t.Errorf("Build: %+v", b)
	}
	if len(r.Notes) != 0 {
		t.Errorf("notes: %v", r.Notes)
	}
	sparkle := r.Sources[2]
	if sparkle.ID != SourceSparkle || sparkle.Checked != 5 || sparkle.Skipped != "" {
		t.Errorf("sparkle source: %+v", sparkle)
	}
}

func TestHTTPFeedIsRefused(t *testing.T) {
	var hits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, appcastXML(itemAttrs("9.0", "900")))
	}))
	defer plain.Close()

	dir := t.TempDir()
	fakeApp(t, dir, "Plain", "1.0", "1", plain.URL+"/feed.xml") // http://
	fakeApp(t, dir, "Odd", "1.0", "1", "ftp://example.com/feed.xml")
	fakeApp(t, dir, "NoHost", "1.0", "1", "https:///feed.xml")
	setup(t, nil, nil, dir)

	r, err := Check(context.Background(), env.New(t.TempDir()), false)
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 0 {
		t.Errorf("the http server was asked %d times", hits.Load())
	}
	if len(r.Updates) != 0 {
		t.Errorf("updates: %+v", r.Updates)
	}
	for _, name := range []string{"Plain", "Odd", "NoHost"} {
		if msg := noteFor(r, name); !strings.Contains(msg, "not an HTTPS address") {
			t.Errorf("%s: note %q", name, msg)
		}
	}
}

func TestRedirectToHTTPIsRefused(t *testing.T) {
	var hits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, appcastXML(itemAttrs("9.0", "900")))
	}))
	defer plain.Close()
	srv := feedServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/feed.xml", http.StatusFound)
	}))
	dir := t.TempDir()
	fakeApp(t, dir, "Sneaky", "1.0", "1", srv.URL+"/feed.xml")
	setup(t, nil, nil, dir)

	r, err := Check(context.Background(), env.New(t.TempDir()), false)
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 0 {
		t.Errorf("the http server was asked %d times", hits.Load())
	}
	if msg := noteFor(r, "Sneaky"); !strings.Contains(msg, "non-HTTPS") {
		t.Errorf("note %q", msg)
	}
}

func TestSlowFeedIsNotedAndOthersStillWork(t *testing.T) {
	srv := feedServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/slow.xml":
			select { // wait until the client gives up
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
		case "/ok.xml":
			fmt.Fprint(w, appcastXML(itemAttrs("2.0", "2")))
		case "/broken.xml":
			fmt.Fprint(w, "<html>Maintenance</html>")
		default:
			http.NotFound(w, r)
		}
	}))
	dir := t.TempDir()
	fakeApp(t, dir, "Slow", "1.0", "1", srv.URL+"/slow.xml")
	fakeApp(t, dir, "Fine", "1.0", "1", srv.URL+"/ok.xml")
	fakeApp(t, dir, "Broken", "1.0", "1", srv.URL+"/broken.xml")
	fakeApp(t, dir, "Gone", "1.0", "1", srv.URL+"/gone.xml")
	setup(t, nil, nil, dir)
	Transport = srv.Client().Transport
	Timeout = 200 * time.Millisecond

	start := time.Now()
	r, err := Check(context.Background(), env.New(t.TempDir()), false)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
	if _, ok := find(r, "Fine"); !ok || len(r.Updates) != 1 {
		t.Errorf("updates: %+v", r.Updates)
	}
	if msg := noteFor(r, "Slow"); msg != "the update server did not answer within 0.2 seconds" {
		t.Errorf("Slow: note %q", msg)
	}
	if msg := noteFor(r, "Gone"); !strings.Contains(msg, "404") {
		t.Errorf("Gone: note %q", msg)
	}
	if msg := noteFor(r, "Broken"); !strings.Contains(msg, "no versions") {
		t.Errorf("Broken: note %q", msg)
	}
	if len(r.Notes) != 3 {
		t.Errorf("notes: %+v", r.Notes)
	}
}

func TestOfflineMakesNoRequest(t *testing.T) {
	var hits atomic.Int32
	srv := feedServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, appcastXML(itemAttrs("9.0", "900")))
	}))
	dir := t.TempDir()
	fakeApp(t, dir, "App", "1.0", "1", srv.URL+"/feed.xml")
	cmds := setup(t, map[string]any{brewCmd: brewJSON, masCmd: "497799835 Xcode (14.1 -> 14.2)\n"}, []string{"brew", "mas"}, dir)

	r, err := Check(context.Background(), env.New(t.TempDir()), true)
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 0 {
		t.Errorf("%d requests while offline", hits.Load())
	}
	// brew answers from what it already knows; mas and Sparkle need the network.
	if !slices.Equal(cmds.ran, []string{brewCmd}) {
		t.Errorf("commands run: %v", cmds.ran)
	}
	if len(r.Updates) != 3 {
		t.Errorf("updates: %+v", r.Updates)
	}
	for _, s := range r.Sources[1:] {
		if !strings.Contains(s.Skipped, "--offline") {
			t.Errorf("source %s: %+v", s.ID, s)
		}
	}
}

func TestBrewAndMas(t *testing.T) {
	cmds := setup(t, map[string]any{
		brewCmd: brewJSON,
		masCmd:  "497799835 Xcode (14.1 -> 14.2)\n",
	}, []string{"brew", "mas"})

	r, err := Check(context.Background(), env.New(t.TempDir()), false)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, u := range r.Updates {
		names = append(names, u.Name+"/"+u.Source)
	}
	// Sorted by name, ignoring case.
	want := []string{"ghostty/homebrew", "ngrok/homebrew", "no-install/homebrew", "Xcode/app-store"}
	if !slices.Equal(names, want) {
		t.Errorf("updates %v, want %v", names, want)
	}
	// brew is told not to update itself, and only these two commands run.
	if !slices.Equal(cmds.ran, []string{brewCmd, masCmd}) {
		t.Errorf("commands run: %v", cmds.ran)
	}
	for _, s := range r.Sources[:2] {
		if s.Skipped != "" {
			t.Errorf("source %+v", s)
		}
	}
}

func TestMissingAndFailingTools(t *testing.T) {
	// Neither tool is installed.
	cmds := setup(t, nil, nil)
	r, err := Check(context.Background(), env.New(t.TempDir()), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds.ran) != 0 || len(r.Updates) != 0 {
		t.Errorf("ran %v, updates %v", cmds.ran, r.Updates)
	}
	if !strings.Contains(r.Sources[0].Skipped, "Homebrew is not installed") || !strings.Contains(r.Sources[1].Skipped, "mas is not installed") {
		t.Errorf("sources: %+v", r.Sources)
	}

	// Both fail: not fatal, and said.
	setup(t, map[string]any{brewCmd: errors.New("exit status 1"), masCmd: errors.New("not signed in")}, []string{"brew", "mas"})
	r, err = Check(context.Background(), env.New(t.TempDir()), false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Sources[0].Skipped, "brew outdated failed: exit status 1") || !strings.Contains(r.Sources[1].Skipped, "not signed in") {
		t.Errorf("sources: %+v", r.Sources)
	}

	// brew answers with something that is not JSON.
	setup(t, map[string]any{brewCmd: "Error: something"}, []string{"brew"})
	r, err = Check(context.Background(), env.New(t.TempDir()), false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Sources[0].Skipped, "cannot read") {
		t.Errorf("sources: %+v", r.Sources)
	}
}

func TestCommandErrorUsesStderr(t *testing.T) {
	err := &exec.ExitError{Stderr: []byte("Error: no such cask\nmore detail\n")}
	if got := commandError(err); got != "Error: no such cask" {
		t.Errorf("got %q", got)
	}
}

// An app that brew manages shows once, with brew's command.
func TestBrewAppIsNotListedTwice(t *testing.T) {
	srv := feedServer(t, serve(map[string]string{"/feed.xml": appcastXML(itemAttrs("1.4.0", "14"))}))
	dir := t.TempDir()
	fakeApp(t, dir, "Visual Studio Code", "1.0", "1", srv.URL+"/feed.xml")
	fakeApp(t, dir, "Other", "1.0", "1", srv.URL+"/feed.xml")
	setup(t, map[string]any{brewCmd: `{"casks":[{"name":"visual-studio-code","installed_versions":["1.0"],"current_version":"1.4.0"}]}`}, []string{"brew"}, dir)
	Transport = srv.Client().Transport

	r, err := Check(context.Background(), env.New(t.TempDir()), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Updates) != 2 {
		t.Fatalf("updates: %+v", r.Updates)
	}
	if u, _ := find(r, "visual-studio-code"); u.Source != SourceHomebrew {
		t.Errorf("code: %+v", u)
	}
	if u, _ := find(r, "Other"); u.Source != SourceSparkle {
		t.Errorf("other: %+v", u)
	}
}

// At most MaxParallel feeds are fetched at once.
func TestFetchesAreBounded(t *testing.T) {
	var running, peak atomic.Int32
	srv := feedServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := running.Add(1)
		defer running.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		fmt.Fprint(w, appcastXML(itemAttrs("2.0", "2")))
	}))
	dir := t.TempDir()
	for i := range 12 {
		fakeApp(t, dir, fmt.Sprintf("App%02d", i), "1.0", "1", srv.URL+"/feed.xml")
	}
	setup(t, nil, nil, dir)
	Transport = srv.Client().Transport

	r, err := Check(context.Background(), env.New(t.TempDir()), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Updates) != 12 {
		t.Errorf("updates: %d", len(r.Updates))
	}
	if p := peak.Load(); p > MaxParallel || p < 2 {
		t.Errorf("up to %d feeds at once, want 2 to %d", p, MaxParallel)
	}
}

func TestCancelledCheckReturnsTheError(t *testing.T) {
	dir := t.TempDir()
	fakeApp(t, dir, "App", "1.0", "1", "https://updates.example/feed.xml")
	setup(t, nil, nil, dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Check(ctx, env.New(t.TempDir()), false); !errors.Is(err, context.Canceled) {
		t.Errorf("error %v, want context.Canceled", err)
	}
}

// Sapu only reads: the app folder is the same after a check.
func TestCheckChangesNothing(t *testing.T) {
	srv := feedServer(t, serve(map[string]string{"/feed.xml": appcastXML(itemAttrs("2.0", "2"))}))
	dir := t.TempDir()
	app := fakeApp(t, dir, "App", "1.0", "1", srv.URL+"/feed.xml")
	plist := filepath.Join(app, "Contents", "Info.plist")
	before, _ := os.ReadFile(plist)
	info, _ := os.Stat(plist)
	setup(t, nil, nil, dir)
	Transport = srv.Client().Transport

	if _, err := Check(context.Background(), env.New(t.TempDir()), false); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(plist)
	info2, _ := os.Stat(plist)
	if string(before) != string(after) || !info.ModTime().Equal(info2.ModTime()) {
		t.Error("Info.plist changed")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("%d entries in the app folder", len(entries))
	}
}
