package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/loginitems"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/review"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/walk"
)

const loginItemsLong = `List what starts at login: your agents in ~/Library/LaunchAgents, and the
agents and daemons in /Library, which need an admin account to change.

An orphan is an item whose program no longer exists, usually left behind by
an app you removed. "sapu login-items disable" moves one of your own items
to quarantine, so it no longer starts at login; "sapu undo" puts it back.`

func newLoginItemsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login-items",
		Short: "List what starts at login, flag leftovers, and turn items off",
		Long:  loginItemsLong,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "login-items", runLoginItems(cmd))
		},
	}
	cmd.AddCommand(newLoginItemsDisableCmd())
	return cmd
}

// loginItemsData is the data of login-items --json.
type loginItemsData struct {
	Items []loginitems.Item `json:"items"`
}

func runLoginItems(cmd *cobra.Command) error {
	e, err := loadEnv()
	if err != nil {
		return err
	}
	items, err := loginitems.List(cmd.Context(), e)
	if err != nil {
		return err
	}
	if items == nil {
		items = []loginitems.Item{}
	}
	writeLoginItems(humanOut(cmd), e.Home, items)
	return printData(cmd, "login-items", loginItemsData{Items: items})
}

var scopeNames = map[string]string{
	loginitems.ScopeUser:     "Yours",
	loginitems.ScopeAllUsers: "For every user · needs an admin account to change",
	loginitems.ScopeSystem:   "System helpers · need an admin account to change",
}

var stateNames = map[string]string{
	loginitems.StateLoaded:    "loaded",
	loginitems.StateNotLoaded: "not loaded",
}

func writeLoginItems(w io.Writer, home string, items []loginitems.Item) {
	st := newStyle(w)
	orphans := 0
	for i, d := range loginitems.Dirs {
		if i > 0 {
			fmt.Fprintln(w)
		}
		dir := d.Path
		if !strings.HasPrefix(dir, "~") {
			dir = tilde(home, dir)
		}
		fmt.Fprintln(w, st.bold(dir+" · "+scopeNames[d.Scope]))
		n := 0
		for _, it := range items {
			if it.Scope != d.Scope {
				continue
			}
			n++
			if it.Orphan && it.Scope == loginitems.ScopeUser {
				orphans++
			}
			writeLoginItem(w, st, home, it)
		}
		if n == 0 {
			fmt.Fprintln(w, "  (none)")
		}
	}

	fmt.Fprintln(w)
	if orphans > 0 {
		fmt.Fprintf(w, "Orphans of yours: %d. Turn them all off: sapu login-items disable --orphans\n", orphans)
	}
	fmt.Fprintln(w, "Turn off one of your items: sapu login-items disable LABEL")
	writeWrapped(w, st, 0, "Apps can also add background items that macOS manages itself. Sapu needs an admin account to read those; "+
		"see them in System Settings › General › Login Items.")
}

func writeLoginItem(w io.Writer, st style, home string, it loginitems.Item) {
	if state := stateNames[it.State]; state != "" {
		fmt.Fprintln(w, row(2, it.Name(), state))
	} else {
		fmt.Fprintln(w, "  "+it.Name())
	}
	if it.Error != "" {
		writeWrapped(w, st, 4, it.Error)
		return
	}
	if it.Label == "" {
		fmt.Fprintln(w, "    "+st.dim("no label, so launchd ignores it"))
	}
	if it.Program == "" {
		fmt.Fprintln(w, "    "+st.dim("no program"))
	} else {
		fmt.Fprintln(w, "    "+tilde(home, it.Program))
	}
	var how []string
	if it.App != "" {
		how = append(how, "app: "+strings.TrimSuffix(filepath.Base(it.App), ".app"))
	}
	if it.RunAtLoad {
		how = append(how, "starts at login")
	}
	if it.KeepAlive {
		how = append(how, "kept running")
	}
	if len(how) > 0 {
		fmt.Fprintln(w, "    "+st.dim(strings.Join(how, " · ")))
	}
	if it.Orphan {
		fmt.Fprintln(w, "    "+st.bold("orphan:")+" its program no longer exists")
	}
}

func newLoginItemsDisableCmd() *cobra.Command {
	var f cleanupFlags
	var orphans bool
	cmd := &cobra.Command{
		Use:   "disable [LABEL...]",
		Short: "Stop your login items from starting; undo turns them back on",
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "login-items-disable", runLoginItemsDisable(cmd, args, &f, orphans))
		},
	}
	f.addPlanFlag(cmd)
	f.addApplyFlags(cmd)
	cmd.Flags().BoolVar(&orphans, "orphans", false, "every item of yours whose program no longer exists")
	return cmd
}

func runLoginItemsDisable(cmd *cobra.Command, args []string, f *cleanupFlags, orphans bool) error {
	if len(args) == 0 && !orphans {
		return usageErr(errors.New("name a LABEL to turn off, or use --orphans; sapu login-items lists them"))
	}
	if err := f.check(); err != nil {
		return err
	}
	f.verbose = true // list every item before asking
	e, err := loadEnv()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	all, err := loginitems.List(ctx, e)
	if err != nil {
		return err
	}

	var chosen []loginitems.Item
	seen := map[string]bool{}
	add := func(it loginitems.Item) {
		if !seen[it.Path] {
			seen[it.Path] = true
			chosen = append(chosen, it)
		}
	}
	for _, name := range args {
		found := loginitems.Find(all, name)
		if len(found) == 0 {
			return fmt.Errorf("no login item %q; sapu login-items lists them", name)
		}
		for _, it := range found {
			if it.Scope != loginitems.ScopeUser {
				return fmt.Errorf("%s is in %s, which needs an admin account to change; Sapu turns off only your own items in ~/Library/LaunchAgents",
					name, filepath.Dir(it.Path))
			}
			add(it)
		}
	}
	if orphans {
		for _, it := range all {
			if it.Scope == loginitems.ScopeUser && it.Orphan {
				add(it)
			}
		}
	}

	p, err := planLoginItems(ctx, e, chosen, nil)
	if err != nil {
		return err
	}
	if useReview(cmd, f) {
		err = runReview(cmd, e, planned(p))
	} else {
		err = runPlan(cmd, "login-items-disable", e, p, *f)
	}
	writeStopNow(humanOut(cmd), chosen)
	return err
}

// planLoginItems plans moving the login items to quarantine, which stops
// them from starting at login. When selected is nil every item starts
// selected; else only those it returns true for. sapu login-items disable
// and the login-items tool share it.
func planLoginItems(ctx context.Context, e env.Env, chosen []loginitems.Item, selected func(loginitems.Item) bool) (*plan.Plan, error) {
	var items []plan.Item
	byPath := map[string]loginitems.Item{}
	for _, it := range chosen {
		item, err := uninstallItem(ctx, plan.KindLoginItems, it.Path, rules.RiskModerate, loginItemExplain(e.Home, it), nil)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
		byPath[it.Path] = it
	}
	p, err := engine.Plan(ctx, e, plan.KindLoginItems, items)
	if err != nil {
		return nil, err
	}
	for i := range p.Items {
		p.Items[i].Selected = selected == nil || selected(byPath[p.Items[i].Path])
	}
	return p, nil
}

// loginItemsTool is the plan of the login-items tool: every login item of
// the user (~/Library/LaunchAgents), with the orphans selected, as sapu
// login-items disable --orphans selects them. It has no folders.
func loginItemsTool(e env.Env, _ []string, _ string) review.PlanFunc {
	return func(ctx context.Context, _ func(walk.Event)) (*plan.Plan, error) {
		all, err := loginitems.List(ctx, e)
		if err != nil {
			return nil, err
		}
		var mine []loginitems.Item
		for _, it := range all {
			if it.Scope == loginitems.ScopeUser {
				mine = append(mine, it)
			}
		}
		return planLoginItems(ctx, e, mine, func(it loginitems.Item) bool { return it.Orphan })
	}
}

func loginItemExplain(home string, it loginitems.Item) string {
	text := "Login item " + it.Name()
	if it.Program != "" {
		text += ": runs " + tilde(home, it.Program)
	}
	text += "."
	if it.Orphan {
		text += " Its program no longer exists, so it only fails each time it starts."
	}
	return text + " Moving it to quarantine stops it from starting at your next login; sapu undo puts it back."
}

// writeStopNow tells how to stop the items that this run moved and that are
// still running. Sapu does not stop them itself.
func writeStopNow(w io.Writer, moved []loginitems.Item) {
	var cmds []string
	for _, it := range moved {
		if _, err := os.Lstat(it.Path); !errors.Is(err, fs.ErrNotExist) || it.State != loginitems.StateLoaded {
			continue
		}
		cmds = append(cmds, shellJoin([]string{"launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), it.Label)}))
	}
	if len(cmds) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "These keep running until you log out. To stop them now, run (Sapu does not):")
	for _, c := range cmds {
		fmt.Fprintln(w, "  "+c)
	}
}
