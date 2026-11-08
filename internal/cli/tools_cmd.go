package cli

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/apps"
)

const toolsLong = `Lists the tools you can also run by picking them in the menu (plain sapu):
the commands that work on an app or on folders you choose.

"sapu tools apps" lists the apps "sapu uninstall" can remove. "sapu tools plan
ID" runs one tool with its defaults and prints the plan as JSON, like
"sapu clean --plan -". It changes nothing; "sapu apply -" applies the plan.`

func newToolsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "tools",
		Short:   "List the tools you can run from the menu",
		Long:    toolsLong,
		GroupID: "look",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "tools", runTools(cmd))
		},
	}
	cmd.AddCommand(newToolsAppsCmd(), newToolsPlanCmd())
	return cmd
}

// toolsData is the data of sapu tools --json.
type toolsData struct {
	Tools []toolJSON `json:"tools"`
}

type toolJSON struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Short       string   `json:"short"`
	Folders     bool     `json:"folders"`
	App         bool     `json:"app"`
	DefaultDirs []string `json:"default_dirs"` // empty for a tool without folders
}

func runTools(cmd *cobra.Command) error {
	e, err := loadEnv()
	if err != nil {
		return err
	}
	data := toolsData{Tools: []toolJSON{}}
	rows := [][]string{}
	for _, t := range tools {
		dirs := []string{}
		if t.DefaultDirs != nil {
			found, err := t.DefaultDirs(e)
			if err != nil {
				return err
			}
			dirs = append(dirs, found...)
		}
		data.Tools = append(data.Tools, toolJSON{ID: t.ID, Title: t.Title, Short: t.Short, Folders: t.Folders, App: t.App, DefaultDirs: dirs})

		where := ""
		switch {
		case t.App:
			where = "an app you pick"
		case t.Folders:
			shown := make([]string, len(dirs))
			for i, d := range dirs {
				shown[i] = tilde(e.Home, d)
			}
			where = strings.Join(shown, ", ")
			if where == "" {
				where = "(no default folder exists)"
			}
		}
		rows = append(rows, []string{t.ID, t.Short, where})
	}
	out := humanOut(cmd)
	writeTable(out, []string{"Tool", "What it finds", "Looks in"}, rows)
	fmt.Fprintln(out, "\nPick them in the menu (sapu), or run sapu ID, e.g. sapu purge ~/Projects.")
	return printData(cmd, "tools", data)
}

func newToolsAppsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "apps",
		Short: "List the apps sapu uninstall can remove",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "tools-apps", runToolsApps(cmd))
		},
	}
}

// toolsAppsData is the data of sapu tools apps --json.
type toolsAppsData struct {
	Apps []toolApp `json:"apps"`
}

type toolApp struct {
	Name     string `json:"name"`
	BundleID string `json:"bundle_id"` // empty when the app has none
	Path     string `json:"path"`
}

// runToolsApps lists the installed apps, without Apple's, which sapu
// uninstall refuses. They are sorted by name.
func runToolsApps(cmd *cobra.Command) error {
	e, err := loadEnv()
	if err != nil {
		return err
	}
	installed, err := apps.List(cmd.Context(), e)
	if err != nil {
		return err
	}
	data := toolsAppsData{Apps: []toolApp{}}
	for _, a := range installed {
		if apps.IsApple(a.ID) {
			continue
		}
		data.Apps = append(data.Apps, toolApp{Name: a.Name, BundleID: a.ID, Path: a.Path})
	}
	slices.SortFunc(data.Apps, func(a, b toolApp) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.Path, b.Path))
	})

	out := humanOut(cmd)
	if len(data.Apps) == 0 {
		fmt.Fprintln(out, "No apps Sapu can remove.")
	} else {
		rows := make([][]string, len(data.Apps))
		for i, a := range data.Apps {
			rows[i] = []string{a.Name, a.BundleID, tilde(e.Home, a.Path)}
		}
		writeTable(out, []string{"App", "Bundle id", "Path"}, rows)
		fmt.Fprintln(out, "\nRemove one with: sapu uninstall PATH")
	}
	return printData(cmd, "tools-apps", data)
}

func newToolsPlanCmd() *cobra.Command {
	var app string
	cmd := &cobra.Command{
		Use:   "plan ID [DIR...]",
		Short: "Run one tool and print its plan as JSON; changes nothing",
		Long: `Runs one tool with its defaults and prints the plan to stdout as JSON, the
same document "sapu clean --plan -" prints (schema/v1/plan.json). DIR replaces
the tool's default folders. --app names the app for uninstall, as
"sapu uninstall APP" takes it: a path is the safest. It only reads; apply the
plan with "sapu apply -".`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "tools-plan", runToolsPlan(cmd, args[0], args[1:], app))
		},
	}
	cmd.Flags().StringVar(&app, "app", "", "the app to remove (uninstall only): a path, a name or a bundle id")
	return cmd
}

func runToolsPlan(cmd *cobra.Command, id string, dirs []string, app string) error {
	if jsonOut {
		return usageErr(errors.New("sapu tools plan already prints the plan as JSON; drop --json"))
	}
	t, ok := toolByID(id)
	if !ok {
		ids := make([]string, len(tools))
		for i, t := range tools {
			ids[i] = t.ID
		}
		return usageErr(fmt.Errorf("no tool %q; the tools are %s", id, strings.Join(ids, ", ")))
	}
	switch {
	case len(dirs) > 0 && !t.Folders:
		return usageErr(fmt.Errorf("%s takes no folders", id))
	case app != "" && !t.App:
		return usageErr(fmt.Errorf("--app is only for uninstall, not %s", id))
	case app == "" && t.App:
		return usageErr(errors.New("uninstall needs --app: the app to remove (sapu tools apps lists them)"))
	}
	if err := checkFoundDirs(dirs); err != nil {
		return err
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	if len(dirs) == 0 {
		dirs = nil // the tool's default folders
	}
	prog := newProgress(cmd)
	p, err := t.Plan(e, dirs, app)(cmd.Context(), prog.Scan)
	prog.Done()
	if err != nil {
		return err
	}
	return savePlan(cmd, "tools-plan", p, cleanupFlags{planFile: "-"})
}
