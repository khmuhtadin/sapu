// Package updates finds apps that have a newer version, for sapu updates
// (blueprint A3). It only tells the user. It never downloads or installs
// anything and never runs a command that changes the Mac; the official way to
// update is shown for the user to run.
//
// Three sources:
//
//   - Homebrew casks: `brew outdated --cask --greedy --json=v2`, when brew is
//     installed. Sapu sets HOMEBREW_NO_AUTO_UPDATE so brew does not update
//     itself while Sapu looks; the answer is as fresh as the last `brew update`.
//   - The Mac App Store: `mas outdated`, when mas is installed. mas asks
//     Apple's servers, so --offline skips it.
//   - Sparkle: apps whose Info.plist has SUFeedURL. Sapu fetches the app's
//     appcast over HTTPS only (an http:// feed is refused, also after a
//     redirect), waits at most Timeout, and compares the newest item with the
//     installed version as dotted numbers. At most MaxParallel feeds are
//     fetched at once. A feed that fails is noted for that app and nothing
//     else is affected.
//
// Privacy: checking contacts each app's own update server, the same one the
// app contacts when it checks for updates itself. Sapu sends a plain GET
// request with no details about your Mac or your apps, and talks to no other
// server. With offline set, Sapu makes no network request at all.
package updates

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/khmuhtadin/sapu/internal/apps"
	"github.com/khmuhtadin/sapu/internal/env"
)

// MaxParallel is how many update servers Sapu asks at the same time.
const MaxParallel = 4

// Tests replace these.
var (
	// RunCommand runs a command and returns its standard output.
	RunCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, name, args...).Output()
	}
	// LookPath finds a command on the PATH.
	LookPath = exec.LookPath
	// Transport sends the appcast requests.
	Transport http.RoundTripper = http.DefaultTransport
	// Timeout is how long one appcast request may take.
	Timeout = 10 * time.Second
)

const (
	SourceHomebrew = "homebrew"
	SourceAppStore = "app-store"
	SourceSparkle  = "sparkle"
)

// Update is an app with a newer version.
type Update struct {
	Name      string   `json:"name"`
	Source    string   `json:"source"`
	Installed string   `json:"installed"`
	Available string   `json:"available"`
	How       string   `json:"how"`               // the official way to update, in words
	Command   []string `json:"command,omitempty"` // the same as a command, shown and never run
	Path      string   `json:"path,omitempty"`    // the app, for Sparkle apps
}

// Note is something that went wrong for one app.
type Note struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

// Source says what happened to one source.
type Source struct {
	ID      string `json:"id"`
	Skipped string `json:"skipped,omitempty"` // why the source was not checked
	Checked int    `json:"checked,omitempty"` // Sparkle: apps with a feed that were tried
}

// Result is what Check found.
type Result struct {
	Sources []Source
	Updates []Update // by name
	Notes   []Note
}

// Check asks every source. With offline set it makes no network request.
func Check(ctx context.Context, e env.Env, offline bool) (Result, error) {
	var r Result

	brew, src := checkBrew(ctx)
	r.Sources = append(r.Sources, src)

	store, src := checkAppStore(ctx, offline)
	r.Sources = append(r.Sources, src)

	sparkle, src, notes, err := checkSparkle(ctx, e, offline)
	if err != nil {
		return r, err
	}
	r.Sources = append(r.Sources, src)
	r.Notes = append(r.Notes, notes...)

	// An app that brew manages shows once, with brew's command.
	managed := map[string]bool{}
	for _, u := range brew {
		managed[normalize(u.Name)] = true
	}
	r.Updates = append(r.Updates, brew...)
	r.Updates = append(r.Updates, store...)
	for _, u := range sparkle {
		if !managed[normalize(u.Name)] {
			r.Updates = append(r.Updates, u)
		}
	}
	slices.SortFunc(r.Updates, func(a, b Update) int {
		return cmp.Or(
			strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)),
			strings.Compare(a.Source, b.Source),
		)
	})
	return r, ctx.Err()
}

// normalize makes "Visual Studio Code" and "visual-studio-code" equal.
func normalize(name string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(name) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// compareVersions compares dotted numbers: "1.10" is newer than "1.9", and
// "2.0" equals "2". Text after the digits of a part is ignored. It returns
// -1, 0 or 1.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(as), len(bs)); i++ {
		x, y := 0, 0
		if i < len(as) {
			x = leadingNumber(as[i])
		}
		if i < len(bs) {
			y = leadingNumber(bs[i])
		}
		if c := cmp.Compare(x, y); c != 0 {
			return c
		}
	}
	return 0
}

func leadingNumber(s string) int {
	n := 0
	for _, c := range strings.TrimSpace(s) {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// checkSparkle reads Info.plist of every app and asks the feed of those that
// have one.
func checkSparkle(ctx context.Context, e env.Env, offline bool) ([]Update, Source, []Note, error) {
	src := Source{ID: SourceSparkle}
	if offline {
		src.Skipped = "skipped because of --offline"
		return nil, src, nil, nil
	}
	list, err := apps.List(ctx, e)
	if err != nil {
		return nil, src, nil, err
	}

	type answer struct {
		hasFeed bool
		update  *Update
		note    *Note
	}
	answers := make([]answer, len(list))
	slots := make(chan struct{}, MaxParallel)
	var wg sync.WaitGroup
	for i, a := range list {
		slots <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-slots; wg.Done() }()
			answers[i].hasFeed, answers[i].update, answers[i].note = checkSparkleApp(ctx, a)
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, src, nil, err
	}

	var updates []Update
	var notes []Note
	for _, a := range answers {
		if a.hasFeed {
			src.Checked++
		}
		if a.update != nil {
			updates = append(updates, *a.update)
		}
		if a.note != nil {
			notes = append(notes, *a.note)
		}
	}
	return updates, src, notes, nil
}

// checkSparkleApp returns whether the app has a feed, and an update or a note.
func checkSparkleApp(ctx context.Context, a apps.App) (hasFeed bool, u *Update, n *Note) {
	info, err := apps.ReadPlist(ctx, filepath.Join(a.Path, "Contents", "Info.plist"))
	if err != nil {
		return false, nil, &Note{Name: a.Name, Message: "Info.plist could not be read"}
	}
	feed, _ := info["SUFeedURL"].(string)
	feed = strings.TrimSpace(feed)
	if feed == "" {
		return false, nil, nil
	}
	note := func(msg string) *Note { return &Note{Name: a.Name, Message: msg} }

	data, err := fetch(ctx, feed)
	if err != nil {
		return true, nil, note(err.Error())
	}
	item, err := newestItem(data)
	if err != nil {
		return true, nil, note(err.Error())
	}

	short, _ := info["CFBundleShortVersionString"].(string)
	build, _ := info["CFBundleVersion"].(string)
	installed, available, isNewer := short, item.Short, false
	switch {
	case item.Short != "" && short != "":
		isNewer = compareVersions(item.Short, short) > 0
	case item.Build != "" && build != "":
		installed, available = build, item.Build
		isNewer = compareVersions(item.Build, build) > 0
	default:
		return true, nil, note("the versions could not be compared")
	}
	if !isNewer {
		return true, nil, nil
	}
	return true, &Update{
		Name: a.Name, Source: SourceSparkle, Path: a.Path,
		Installed: installed, Available: available,
		How: "Open the app and choose Check for Updates.",
	}, nil
}

// fetch downloads an appcast. Only HTTPS is allowed, also after a redirect.
func fetch(ctx context.Context, feed string) ([]byte, error) {
	u, err := url.Parse(feed)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("the update feed is not an HTTPS address, so Sapu did not ask it")
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feed, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Sapu")
	client := &http.Client{
		Transport: Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return errors.New("the feed redirected to a non-HTTPS address")
			}
			if len(via) >= 5 {
				return errors.New("the feed redirected too many times")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fetchError(err)
	}
	defer func() { _ = resp.Body.Close() }() // read only: a failed close loses nothing
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the update server answered %s", resp.Status)
	}
	data, err := readLimited(resp.Body, maxFeedBytes)
	if err != nil {
		return nil, fetchError(err)
	}
	return data, nil
}

// fetchError words a network error without the address in it.
func fetchError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("the update server did not answer within %g seconds", Timeout.Seconds())
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("the update server could not be reached: %w", err)
}
