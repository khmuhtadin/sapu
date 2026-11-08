// Package explain answers "what is my System Data?" (blueprint D1).
// It is strictly read-only: it measures and explains, and only suggests
// actions. It never runs the commands it shows, and the only commands it runs
// itself are the read-only `tmutil listlocalsnapshots` and `sysctl`.
// Implemented in Wave 2 (W2-D).
package explain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/walk"
)

type Row struct {
	ID         string   `json:"id"`                   // e.g. "tm_snapshots", "ios_backups"
	Title      string   `json:"title"`                // short, in the user's language
	Bytes      int64    `json:"bytes"`                // -1 when it could not be measured
	Unmeasured string   `json:"unmeasured,omitempty"` // why Bytes is -1, e.g. "needs Full Disk Access"
	Explain    string   `json:"explain"`              // what it is, whether it is safe, what happens next
	Action     string   `json:"action"`               // the official safe way to reclaim it, in words
	Command    []string `json:"command,omitempty"`    // the command for it, if there is one
	NeedsAdmin bool     `json:"needs_admin"`          // the command needs sudo
}

// System paths live in variables so tests can point them at fixtures.
var (
	spotlightDir = "/System/Volumes/Data/.Spotlight-V100"
	sleepImage   = "/private/var/vm/sleepimage"
	systemAssets = "/System/Library/AssetsV2"
)

const (
	needsFDA      = "needs Full Disk Access"
	needsAdminMsg = "needs admin rights to measure"
)

// run runs one command and returns its output. Tests replace it.
var run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// Explain returns one Row per source, in a fixed order. A source that cannot
// be read becomes a row with Bytes -1 and a reason. Only a cancelled ctx is an
// error; the rows measured so far are returned with it.
func Explain(ctx context.Context, e env.Env) ([]Row, error) {
	sources := []func(context.Context, env.Env) Row{
		timeMachine,
		purgeable,
		spotlight,
		iosBackups,
		messages,
		vmImages,
		xcode,
		cloudDrives,
		systemAssetsRow,
		swapAndSleep,
		userCaches,
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Sources are independent, so they run at the same time: a folder that
	// does not respond then costs one measureTimeout, not one per row.
	rows := make([]Row, len(sources))
	var wg sync.WaitGroup
	for i, source := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows[i] = source(ctx, e)
		}()
	}
	wg.Wait()
	for i := range rows {
		if rows[i].Bytes == 0 {
			rows[i] = nothingToDo(rows[i])
		}
	}
	return rows, ctx.Err()
}

// nothingToDo changes the action of a row that measured zero: there is
// nothing to reclaim, so no way to reclaim it and no command.
func nothingToDo(r Row) Row {
	r.Action = "Nothing to do."
	r.Command = nil
	r.NeedsAdmin = false
	return r
}

// unmeasured marks a row as not measurable.
func unmeasured(r Row, reason string) Row {
	r.Bytes = -1
	r.Unmeasured = reason
	return r
}

// measureTimeout limits one row. A folder that never answers (a stuck iCloud
// Drive placeholder, for example) would otherwise block Explain forever, and a
// blocked system call cannot be cancelled.
var measureTimeout = 20 * time.Second

// measure sets r.Bytes to the allocated size of all paths that exist.
// A path that is missing counts as zero. Any path that cannot be read makes
// the whole row unmeasured, because a partial number would be a wrong one.
func measure(ctx context.Context, r Row, paths []string, denied string) Row {
	tctx, cancel := context.WithTimeout(ctx, measureTimeout)
	defer cancel()
	type result struct {
		bytes  int64
		reason string
	}
	done := make(chan result, 1) // buffered: a late answer must not block the goroutine
	go func() {
		var total int64
		for _, p := range paths {
			n, reason := sizeOf(tctx, p, denied)
			if reason != "" {
				done <- result{reason: reason}
				return
			}
			total += n
		}
		done <- result{bytes: total}
	}()
	select {
	case res := <-done:
		if tctx.Err() == nil && res.reason == "" {
			r.Bytes = res.bytes
			return r
		}
		if tctx.Err() == nil {
			return unmeasured(r, res.reason)
		}
	case <-tctx.Done():
	}
	if ctx.Err() != nil {
		return unmeasured(r, "cancelled")
	}
	return unmeasured(r, "took too long: a folder did not respond")
}

// sizeOf returns the size of one path, or a reason it could not be read.
func sizeOf(ctx context.Context, path, denied string) (int64, string) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, ""
	}
	if errors.Is(err, fs.ErrPermission) {
		return 0, denied
	}
	if err != nil {
		return 0, "could not read: " + err.Error()
	}
	if fi.IsDir() {
		// walk skips unreadable entries without an error, so look at the
		// folder first: an EPERM or EACCES here is the "needs permission" case.
		f, err := os.Open(path)
		if errors.Is(err, fs.ErrPermission) {
			return 0, denied
		}
		if err != nil {
			return 0, "could not read: " + err.Error()
		}
		_, err = f.Readdirnames(1)
		_ = f.Close() // read only: a failed close loses nothing
		if errors.Is(err, fs.ErrPermission) {
			return 0, denied
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, "could not read: " + err.Error()
		}
	}
	n, _, err := walk.Size(ctx, path, nil)
	if err != nil {
		return 0, "could not measure: " + err.Error()
	}
	return n, ""
}

var tmSnapshot = regexp.MustCompile(`^com\.apple\.TimeMachine\.`)

func timeMachine(ctx context.Context, e env.Env) Row {
	r := Row{
		ID:     "tm_snapshots",
		Title:  "Time Machine local snapshots",
		Action: "Let macOS remove them, or thin them with the command below.",
		// thinlocalsnapshots <mount> <bytes to free> <urgency 1-4>
		Command:    []string{"tmutil", "thinlocalsnapshots", "/", "10000000000", "4"},
		NeedsAdmin: true,
	}
	out, err := run(ctx, "tmutil", "listlocalsnapshots", "/")
	if err != nil {
		return unmeasured(r, "could not list snapshots: "+err.Error())
	}
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		if tmSnapshot.MatchString(strings.TrimSpace(line)) {
			count++
		}
	}
	if count == 0 {
		r.Bytes = 0
		r.Explain = "There are no Time Machine local snapshots on this Mac."
		return r
	}
	r.Title = fmt.Sprintf("Time Machine local snapshots (%d)", count)
	r.Bytes = -1
	r.Unmeasured = "macOS does not report how much space snapshots use"
	r.Explain = "Time Machine keeps hourly snapshots on this disk so you can restore files when the backup disk is away. " +
		"Removing them is safe, but you lose those restore points. macOS deletes them by itself after 24 hours " +
		"or when the disk gets full."
	return r
}

// purgeable is always unmeasured: macOS shows purgeable space in Finder and
// System Settings, but `diskutil info` has no key for it, so any number we
// computed would be a guess.
func purgeable(ctx context.Context, e env.Env) Row {
	return unmeasured(Row{
		ID:    "purgeable",
		Title: "Purgeable space",
		Explain: "Purgeable space is data macOS can delete when it needs room, such as caches and iCloud files that are also online. " +
			"You do not need to remove it; macOS does that on demand, and it counts as free space for new files.",
		Action: "Nothing to do. macOS frees it when needed.",
	}, "macOS does not report purgeable space to command-line tools")
}

func spotlight(ctx context.Context, e env.Env) Row {
	r := Row{
		ID:    "spotlight",
		Title: "Spotlight index",
		Explain: "The search index that makes Spotlight fast. It is safe to rebuild: search is slow and the " +
			"fans may run for a while until macOS has indexed the disk again.",
		Action:     "Rebuild the index with the command below.",
		Command:    []string{"mdutil", "-E", "/"},
		NeedsAdmin: true,
	}
	return measure(ctx, r, []string{spotlightDir}, needsAdminMsg)
}

func iosBackups(ctx context.Context, e env.Env) Row {
	r := Row{
		ID:    "ios_backups",
		Title: "iPhone and iPad backups",
		Explain: "Local backups of your devices, made by Finder. Delete one only if the device is also backed up " +
			"in iCloud or you no longer need it: a deleted backup cannot be restored.",
		Action: "In Finder, select the device, click Manage Backups, then pick the backup to delete.",
	}
	return measure(ctx, r, []string{filepath.Join(e.Home, "Library/Application Support/MobileSync/Backup")}, needsFDA)
}

func messages(ctx context.Context, e env.Env) Row {
	r := Row{
		ID:    "messages",
		Title: "Messages attachments",
		Explain: "Photos, videos and files you sent and received in Messages. Deleting them removes them from this Mac, " +
			"and with Messages in iCloud the deletion syncs to your other devices too.",
		Action: "In Messages settings choose \"Keep messages\", or open System Settings > General > Storage > Messages.",
	}
	return measure(ctx, r, []string{filepath.Join(e.Home, "Library/Messages/Attachments")}, needsFDA)
}

func vmImages(ctx context.Context, e env.Env) Row {
	r := Row{
		ID:    "vm_images",
		Title: "Docker, OrbStack, UTM and Parallels",
		Explain: "Disk images of virtual machines and containers. They only grow, even after you delete things inside. " +
			"Never delete the files yourself: you would lose every container, image and virtual machine in them.",
		Action:  "Clean up inside the app: `docker system prune`, OrbStack's own cleanup, or delete virtual machines in UTM or Parallels.",
		Command: []string{"docker", "system", "prune"},
	}
	paths := []string{
		filepath.Join(e.Home, "Library/Containers/com.docker.docker/Data/vms/0/data/Docker.raw"),
		filepath.Join(e.Home, ".orbstack"),
		filepath.Join(e.Home, "Library/Containers/com.utmapp.UTM/Data/Documents"),
		filepath.Join(e.Home, "Parallels"),
	}
	orb, _ := filepath.Glob(filepath.Join(e.Home, "Library/Group Containers/*.dev.orbstack"))
	paths = append(paths, orb...)
	return measure(ctx, r, paths, needsFDA)
}

func xcode(ctx context.Context, e env.Env) Row {
	r := Row{
		ID:    "xcode",
		Title: "Xcode",
		Explain: "Build files (DerivedData), simulators, device support files and archives. DerivedData and device support " +
			"are rebuilt when needed. Keep Archives you may need: they are required to read crash reports of apps you shipped.",
		Action:  "Remove simulators you no longer use with the command below, and run `sapu clean` for DerivedData.",
		Command: []string{"xcrun", "simctl", "delete", "unavailable"},
	}
	dev := filepath.Join(e.Home, "Library/Developer")
	paths := []string{
		filepath.Join(dev, "Xcode/DerivedData"),
		filepath.Join(dev, "CoreSimulator/Devices"),
		filepath.Join(dev, "Xcode/iOS DeviceSupport"),
		filepath.Join(dev, "Xcode/Archives"),
	}
	return measure(ctx, r, paths, needsFDA)
}

func cloudDrives(ctx context.Context, e env.Env) Row {
	r := Row{
		ID:    "cloud",
		Title: "Cloud drive copies on this Mac",
		Explain: "Files from iCloud Drive, Google Drive, Dropbox and others that are stored on this Mac. " +
			"Deleting a synced file in the folder deletes it online too. Use the app's own option to remove only the local copy.",
		Action: "In Finder, right-click the file or folder and choose \"Remove Download\" (iCloud) or \"Free up space\" (Google Drive).",
	}
	paths := []string{
		filepath.Join(e.Home, "Library/Mobile Documents"),
		filepath.Join(e.Home, "Library/Application Support/Google/DriveFS"),
	}
	cloud, _ := filepath.Glob(filepath.Join(e.Home, "Library/CloudStorage/*"))
	paths = append(paths, cloud...)
	return measure(ctx, r, paths, needsFDA)
}

func systemAssetsRow(ctx context.Context, e env.Env) Row {
	r := Row{
		ID:    "system_assets",
		Title: "System assets (Apple Intelligence and more)",
		Explain: "Models and data that macOS downloads for features such as Apple Intelligence, dictionaries and voices. " +
			"It is managed by macOS: you cannot remove it safely by hand.",
		Action: "Turn the feature off in System Settings and macOS removes its assets.",
	}
	return measure(ctx, r, []string{systemAssets}, needsAdminMsg)
}

var swapTotal = regexp.MustCompile(`total = ([0-9.]+)([KMG])`)

func swapAndSleep(ctx context.Context, e env.Env) Row {
	r := Row{
		ID:    "swap",
		Title: "Swap and sleep image",
		Explain: "Swap holds memory that did not fit in RAM; the sleep image keeps your memory contents while the Mac sleeps. " +
			"Both are managed by macOS and are safe to leave alone.",
		Action: "Restart your Mac to free swap.",
	}
	out, err := run(ctx, "sysctl", "-n", "vm.swapusage")
	if err != nil {
		return unmeasured(r, "could not read swap: "+err.Error())
	}
	swap, ok := parseSwapTotal(string(out))
	if !ok {
		return unmeasured(r, "could not read swap: unexpected output")
	}
	sleep, reason := sizeOf(ctx, sleepImage, needsAdminMsg)
	if reason != "" {
		return unmeasured(r, reason)
	}
	r.Bytes = swap + sleep
	return r
}

// parseSwapTotal reads "total = 1280.00M  used = ..." and returns bytes.
func parseSwapTotal(s string) (int64, bool) {
	m := swapTotal.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	unit := map[string]float64{"K": 1 << 10, "M": 1 << 20, "G": 1 << 30}[m[2]]
	return int64(n * unit), true
}

func userCaches(ctx context.Context, e env.Env) Row {
	r := Row{
		ID:    "user_caches",
		Title: "User caches and logs",
		Explain: "Temporary files and logs that apps keep in your Library. Most can be removed safely; " +
			"apps rebuild them, which can make the next launch slower.",
		Action: "`sapu clean` shows what is safe to remove.",
	}
	paths := []string{
		filepath.Join(e.Home, "Library/Caches"),
		filepath.Join(e.Home, "Library/Logs"),
	}
	return measure(ctx, r, paths, needsFDA)
}
