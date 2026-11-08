package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/apps"
	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/walk"
)

const uninstallLong = `Remove an app and the files it left in your Library.

APP is a name (Slack), a path (/Applications/Slack.app) or a bundle id
(com.tinyspeck.slackmacgap). Sapu looks in /Applications, the folders in it,
and ~/Applications.

Selected: the app and the files in ~/Library named after its bundle id.
Not selected: files that only share the app's name, because another app may
use that name too (select them with --risk risky).

Everything goes to quarantine, so "sapu undo" puts it back. The files go
only with the app: if the app itself is not moved (it changed after the
plan was made, or it is open), its files and permissions stay too.

With --leftovers BUNDLE_ID Sapu finds what an app you already removed left
behind.

With --reset-permissions Sapu also runs "tccutil reset All BUNDLE_ID", which
removes the privacy permissions you gave the app (camera, microphone, files,
screen recording ...). That cannot be undone: if you install the app again, it
asks again. Sapu asks first and shows the command.`

func newUninstallCmd() *cobra.Command {
	var f cleanupFlags
	var risk, leftovers string
	var resetPerms bool
	cmd := &cobra.Command{
		Use:   "uninstall APP",
		Short: "Remove an app and the files it left in your Library",
		Long:  uninstallLong,
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "uninstall", runUninstall(cmd, args, &f, risk, leftovers, resetPerms))
		},
	}
	f.addPlanFlag(cmd)
	f.addApplyFlags(cmd)
	cmd.Flags().StringVar(&risk, "risk", "moderate", "highest risk to select: safe, moderate or risky")
	cmd.Flags().StringVar(&leftovers, "leftovers", "", "find what an app you already removed left behind, by its bundle id")
	cmd.Flags().BoolVar(&resetPerms, "reset-permissions", false, "also remove the app's privacy permissions with tccutil (not undoable)")
	return cmd
}

func runUninstall(cmd *cobra.Command, args []string, f *cleanupFlags, risk, leftoversID string, resetPerms bool) error {
	switch {
	case leftoversID != "" && len(args) > 0:
		return usageErr(errors.New("give an APP or --leftovers BUNDLE_ID, not both"))
	case leftoversID != "" && resetPerms:
		return usageErr(errors.New("--reset-permissions needs an installed APP; with --leftovers there is no app to take the permissions from"))
	case leftoversID == "" && len(args) != 1:
		return usageErr(errors.New("uninstall takes one APP: a name (Slack), a path (/Applications/Slack.app) or a bundle id"))
	}
	maxRisk, err := parseRisk(risk)
	if err != nil {
		return err
	}
	if err := f.check(); err != nil {
		return err
	}
	f.verbose = true // list every file before asking, not just the first five
	if resetPerms {
		// --plan only writes the plan; sapu apply --run-commands runs the command.
		f.runCommands = f.planFile == ""
		f.runWhy = "You asked for it with --reset-permissions."
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	installed, err := apps.List(cmd.Context(), e)
	if err != nil {
		return err
	}
	if leftoversID != "" {
		return uninstallLeftovers(cmd, e, installed, leftoversID, maxRisk, f)
	}
	return uninstallApp(cmd, e, installed, args[0], maxRisk, resetPerms, f)
}

func uninstallApp(cmd *cobra.Command, e env.Env, installed []apps.App, query string, maxRisk rules.Risk, resetPerms bool, f *cleanupFlags) error {
	ctx := cmd.Context()
	t, err := pickApp(e, installed, query, "remove")
	if err != nil {
		return err
	}
	a := t.app
	if resetPerms && a.ID == "" {
		return fmt.Errorf("%s has no bundle id, so Sapu cannot remove its privacy permissions", a.Name)
	}

	movable := apps.Movable(a.Path)
	bundle, err := uninstallItem(ctx, plan.KindUninstall, a.Path, rules.RiskModerate, bundleExplain(a.Name, movable), t.running)
	if err != nil {
		return err
	}
	var items []plan.Item
	if resetPerms {
		// First: engine.Plan drops an item at the bundle's path that comes
		// after the bundle's own item, as covered by it.
		perms, err := resetPermissionsItem(a, t.running)
		if err != nil {
			return err
		}
		items = append(items, perms)
	}
	items = append(items, bundle)

	found, notes, err := appFiles(ctx, e, t, false)
	if err != nil {
		return err
	}
	items = append(items, found...)

	p, err := engine.Plan(ctx, e, plan.KindUninstall, items)
	if err != nil {
		return err
	}
	bundleID := ""
	for _, it := range p.Items {
		if it.Path == a.Path && it.Action == rules.ActionQuarantine {
			bundleID = it.ID
		}
	}
	bundleInPlan := bundleID != ""
	if bundleInPlan {
		// Nothing of the app goes unless the app itself was moved in the
		// same run: not its files, not its permissions (plan.Item.Requires).
		for i := range p.Items {
			if p.Items[i].ID != bundleID {
				p.Items[i].Requires = bundleID
			}
		}
	}
	for _, s := range p.Skipped {
		if s.Path == a.Path && s.Reason == plan.ReasonInUse {
			return fmt.Errorf("quit %s first: it is in use (%s). Then run sapu uninstall again", a.Name, s.Detail)
		}
	}
	switch {
	case !bundleInPlan:
		notes = append(notes, fmt.Sprintf("Sapu will not move the app (see below), so it also leaves alone the files %s still uses.", a.Name))
	case !movable:
		then := "Until then Sapu leaves its files alone, because " + a.Name + " still uses them."
		if a.ID != "" {
			then = fmt.Sprintf("Then run `sapu uninstall --leftovers %s` for the files it left behind; until then Sapu leaves them alone, because %s still uses them.", a.ID, a.Name)
		}
		notes = append(notes, fmt.Sprintf("Sapu cannot move %s: it is owned by the system. Move it to the Trash in Finder, or remove it with an admin account. %s",
			tilde(e.Home, a.Path), then))
	}
	bundleMoves := bundleInPlan && movable
	for i := range p.Items {
		it := &p.Items[i]
		it.Selected = bundleMoves && riskRank(it.Risk) <= riskRank(maxRisk)
	}

	out := humanOut(cmd)
	writeAppTitle(out, e.Home, a)
	why, err := protectWhy(e)
	if err != nil {
		return err
	}
	writeUninstallNotes(out, e.Home, notes, p.Skipped, why)
	return runPlan(cmd, "uninstall", e, p, *f)
}

// appTarget is the installed app sapu uninstall or sapu reset works on.
type appTarget struct {
	app              apps.App
	sameID, sameName []string // other installed copies of it, as ~ paths
	running          []rules.Condition
}

// pickApp finds the one app query names and refuses Apple's apps. verb is
// "remove" or "reset", for the messages.
func pickApp(e env.Env, installed []apps.App, query, verb string) (appTarget, error) {
	a, err := findApp(installed, query, verb)
	if err != nil {
		return appTarget{}, err
	}
	if apps.IsApple(a.ID) {
		return appTarget{}, fmt.Errorf("%s (%s) comes with macOS; Sapu does not %s Apple's apps", a.Name, a.ID, verb)
	}
	if !apps.ValidBundleID(a.ID) {
		a.ID = "" // a word like "Slack" would match by name, not by bundle id
	}
	t := appTarget{
		app: a,
		// Each item is skipped if the app starts again before it is moved.
		running: []rules.Condition{{ProcessRunning: a.FileName()}},
	}
	sameID, sameName := apps.Others(installed, a)
	for _, other := range sameID {
		t.sameID = append(t.sameID, tilde(e.Home, other.Path))
	}
	for _, other := range sameName {
		t.sameName = append(t.sameName, tilde(e.Home, other.Path))
	}
	return t, nil
}

// appFiles turns the files t.app left in ~/Library into plan items, and says
// in notes what it left out and why. data limits the search to the folders
// that hold the app's own settings and data (sapu reset).
func appFiles(ctx context.Context, e env.Env, t appTarget, data bool) (items []plan.Item, notes []string, err error) {
	a := t.app
	if len(t.sameID) > 0 {
		notes = append(notes, fmt.Sprintf("Sapu leaves %s's files in your Library alone: another copy with the same bundle id is installed (%s) and uses them too.",
			a.Name, strings.Join(t.sameID, ", ")))
		return nil, notes, nil
	}
	name := a.Name
	if len(t.sameName) > 0 {
		name = ""
		notes = append(notes, fmt.Sprintf("Files that only share the name %q are left out: another installed app has that name (%s).",
			a.Name, strings.Join(t.sameName, ", ")))
	}
	if a.ID == "" {
		notes = append(notes, fmt.Sprintf("%s has no bundle id, so Sapu can only look for files with its name.", a.Name))
	}
	items, unreadable, err := leftoverItems(ctx, e, a.ID, name, a.Name, t.running, data)
	if err != nil {
		return nil, nil, err
	}
	return items, append(notes, unreadable...), nil
}

// writeAppTitle prints the app's name, version, bundle id and place.
func writeAppTitle(w io.Writer, home string, a apps.App) {
	title := a.Name
	if a.Version != "" {
		title += " " + a.Version
	}
	if a.ID != "" {
		title += " · " + a.ID
	}
	fmt.Fprintln(w, title)
	fmt.Fprintln(w, tilde(home, a.Path))
}

// findApp returns the one installed app that query names. verb is "remove"
// or "reset", for the messages.
func findApp(installed []apps.App, query, verb string) (apps.App, error) {
	if strings.Contains(query, "/") {
		p, err := filepath.Abs(query)
		if err != nil {
			return apps.App{}, err
		}
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real // /Applications/Safari.app leads into /System
		}
		if p == "/System" || strings.HasPrefix(p, "/System/") {
			return apps.App{}, fmt.Errorf("%s is part of macOS; Sapu does not %s it", query, verb)
		}
	}
	found := apps.Find(installed, query)
	switch len(found) {
	case 0:
		return apps.App{}, fmt.Errorf("no app %q in %s", query, strings.Join(apps.Dirs, ", "))
	case 1:
		return found[0], nil
	}
	paths := make([]string, len(found))
	for i, a := range found {
		paths[i] = a.Path
	}
	return apps.App{}, usageErr(fmt.Errorf("%d apps match %q: %s. Give the path of the one to %s",
		len(found), query, strings.Join(paths, ", "), verb))
}

func uninstallLeftovers(cmd *cobra.Command, e env.Env, installed []apps.App, id string, maxRisk rules.Risk, f *cleanupFlags) error {
	if !apps.ValidBundleID(id) {
		return usageErr(fmt.Errorf("--leftovers takes a bundle id such as com.tinyspeck.slackmacgap, not %q", id))
	}
	if apps.IsApple(id) {
		return fmt.Errorf("%s belongs to macOS; Sapu does not remove Apple's files", id)
	}
	for _, a := range installed {
		if strings.EqualFold(a.ID, id) {
			return fmt.Errorf("%s is still installed at %s; to remove it with its files, run: sapu uninstall %s",
				id, a.Path, shellJoin([]string{a.Path}))
		}
	}
	ctx := cmd.Context()
	items, notes, err := leftoverItems(ctx, e, id, "", id, nil, false)
	if err != nil {
		return err
	}
	p, err := engine.Plan(ctx, e, plan.KindUninstall, items)
	if err != nil {
		return err
	}
	for i := range p.Items {
		p.Items[i].Selected = riskRank(p.Items[i].Risk) <= riskRank(maxRisk)
	}
	out := humanOut(cmd)
	fmt.Fprintf(out, "Files left behind by %s (no installed app has this bundle id)\n", id)
	why, err := protectWhy(e)
	if err != nil {
		return err
	}
	writeUninstallNotes(out, e.Home, notes, p.Skipped, why)
	return runPlan(cmd, "uninstall", e, p, *f)
}

// leftoverItems turns the leftovers of the app with bundle id id (and, when
// name is not empty, its name) into plan items. who names the app in the
// explanations. notes say which folders Sapu could not look in. data is for
// sapu reset: only the app's data folders, with the items marked as reset.
func leftoverItems(ctx context.Context, e env.Env, id, name, who string, protectIf []rules.Condition, data bool) (items []plan.Item, notes []string, err error) {
	find, source, explain := apps.Leftovers, plan.KindUninstall, leftoverExplain
	if data {
		find, source, explain = apps.DataLeftovers, plan.KindReset, resetExplain
	}
	found, unreadable, err := find(e, id, name)
	if err != nil {
		return nil, nil, err
	}
	for _, dir := range unreadable {
		notes = append(notes, fmt.Sprintf("Sapu could not look in %s: macOS allows that only with Full Disk Access (see sapu doctor). Anything %s left there is not listed.",
			tilde(e.Home, dir), who))
	}
	for _, lo := range found {
		risk := rules.RiskModerate
		if lo.ByName {
			risk = rules.RiskRisky
		}
		it, err := uninstallItem(ctx, source, lo.Path, risk, explain(who, lo), protectIf)
		if errors.Is(err, fs.ErrNotExist) {
			continue // gone since the folder was read
		}
		if err != nil {
			return nil, nil, err
		}
		items = append(items, it)
	}
	return items, notes, nil
}

// uninstallItem builds one plan item of sapu uninstall or sapu login-items.
func uninstallItem(ctx context.Context, source, path string, risk rules.Risk, explain string, protectIf []rules.Condition) (plan.Item, error) {
	id, err := plan.IdentityOf(path)
	if err != nil {
		return plan.Item{}, err
	}
	bytes, _, err := walk.Size(ctx, path, nil)
	if err != nil {
		return plan.Item{}, err
	}
	return plan.Item{
		Source:    source,
		Path:      path,
		Bytes:     bytes,
		Risk:      risk,
		Explain:   explain,
		Action:    rules.ActionQuarantine,
		ProtectIf: protectIf,
		Identity:  id,
	}, nil
}

func bundleExplain(name string, movable bool) string {
	if !movable {
		return fmt.Sprintf("The %s app itself. Owned by the system, so Sapu cannot move it: move it to the Trash in Finder, or remove it with an admin account.", name)
	}
	return fmt.Sprintf("The %s app itself. Moving it to quarantine uninstalls it; sapu undo puts it back.", name)
}

// leftoverWhat says what each Library folder holds; %s is the app.
var leftoverWhat = map[string]string{
	"Application Scripts":     "Script folder macOS made for %s.",
	"Application Support":     "Data and settings of %s. If you install it again, it starts fresh.",
	"Caches":                  "Cache of %s.",
	"Containers":              "Sandbox of %s, with its data and settings. If you install it again, it starts fresh.",
	"Cookies":                 "Cookies of %s.",
	"Group Containers":        "Data %s shares with its helpers and extensions. If you install it again, it starts fresh.",
	"HTTPStorages":            "Web cookies and storage of %s.",
	"LaunchAgents":            "Login item of %s. Without the app it has nothing to run; it stops at your next login.",
	"Logs":                    "Logs of %s.",
	"Preferences":             "Settings of %s. If you install it again, it starts with default settings.",
	"Preferences/ByHost":      "Settings of %s for this Mac.",
	"Saved Application State": "Windows macOS saved for %s.",
	"WebKit":                  "Web data of %s.",
}

func leftoverExplain(who string, lo apps.Leftover) string {
	if lo.ByName {
		return nameMatchExplain(lo)
	}
	return fmt.Sprintf(leftoverWhat[lo.Folder], who) + " Named after its bundle id; not needed without the app."
}

func nameMatchExplain(lo apps.Leftover) string {
	return fmt.Sprintf("Called %q like the app, but not named after its bundle id, so it may belong to another app. Not selected: look inside first.", filepath.Base(lo.Path))
}

// resetExplain is leftoverExplain for sapu reset, which keeps the app.
func resetExplain(who string, lo apps.Leftover) string {
	if lo.ByName {
		return nameMatchExplain(lo)
	}
	return fmt.Sprintf("Settings, sign-ins and data of %s as it is now; the app starts like the first time. Undo restores them.", who)
}

// resetPermissionsItem is the command that removes the privacy permissions of
// the app. Its path is the bundle, so the same checks apply as for the app.
func resetPermissionsItem(a apps.App, protectIf []rules.Condition) (plan.Item, error) {
	id, err := plan.IdentityOf(a.Path)
	if err != nil {
		return plan.Item{}, err
	}
	return plan.Item{
		Source: plan.KindUninstall,
		Path:   a.Path,
		Risk:   rules.RiskModerate,
		Explain: "Removes the privacy permissions you gave the app (camera, microphone, files, screen recording …). " +
			"Not undoable; if you reinstall the app, it asks again.",
		Action:    rules.ActionOfficialCommand,
		Command:   []string{"tccutil", "reset", "All", a.ID},
		ProtectIf: protectIf,
		Identity:  id,
	}, nil
}

// writeUninstallNotes prints notes and then, plainly, every item kept because it is
// protected or whitelisted, and why. why gives the protection reason in words.
func writeUninstallNotes(w io.Writer, home string, notes []string, skipped []plan.Skip, why func(detail string) string) {
	for _, s := range skipped {
		switch s.Reason {
		case plan.ReasonProtected:
			reason := why(s.Detail)
			if reason == "" {
				reason = "on Sapu's protection list"
			}
			notes = append(notes, fmt.Sprintf("Kept %s: %s (protected).", tilde(home, s.Path), strings.TrimSuffix(reason, ".")))
		case plan.ReasonWhitelisted:
			notes = append(notes, fmt.Sprintf("Kept %s: it is on your whitelist (%s).", tilde(home, s.Path), strings.TrimPrefix(s.Detail, "whitelisted:")))
		}
	}
	for _, n := range notes {
		fmt.Fprintln(w)
		for _, line := range wrapText(n, tableWidth) {
			fmt.Fprintln(w, line)
		}
	}
	fmt.Fprintln(w)
}
