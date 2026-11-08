package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/updates"
)

// updatesData is the data of `sapu updates --json`.
type updatesData struct {
	Offline bool             `json:"offline"`
	Sources []updates.Source `json:"sources"`
	Updates []updates.Update `json:"updates"`
	Notes   []updates.Note   `json:"notes"`
}

func newUpdatesCmd() *cobra.Command {
	var offline bool
	cmd := &cobra.Command{
		Use:   "updates",
		Short: "Check which apps have updates; Sapu never installs them",
		Long: `Check which apps have a newer version, and show the official way to update.

Sources: Homebrew casks (if brew is installed), the Mac App Store (if mas is
installed) and apps that use Sparkle, which publish a feed of their versions.

Updates only tells you. It never downloads or installs anything, and it shows
the command to run (like "brew upgrade --cask x") without running it.

Privacy: checking contacts each app's own update server, the same server the
app asks when it checks for updates itself. Sapu sends a plain request with no
details about your Mac. It only uses HTTPS addresses, and waits at most 10
seconds for each. With --offline Sapu makes no network request.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpdates(cmd, offline)
		},
	}
	cmd.Flags().BoolVar(&offline, "offline", false, "make no network request (skips the Sparkle and App Store checks)")
	return cmd
}

func runUpdates(cmd *cobra.Command, offline bool) error {
	e, err := loadEnv()
	if err != nil {
		return err
	}
	p := newProgress(cmd)
	p.Status("scan", "Asking each app's update server…")
	r, err := updates.Check(cmd.Context(), e, offline)
	p.Done()
	if err != nil {
		return err
	}
	data := updatesData{Offline: offline, Sources: r.Sources, Updates: r.Updates, Notes: r.Notes}
	if data.Updates == nil {
		data.Updates = []updates.Update{}
	}
	if data.Notes == nil {
		data.Notes = []updates.Note{}
	}
	if jsonOut {
		return output.JSON(cmd.OutOrStdout(), "updates", data)
	}
	printUpdates(cmd, data)
	return nil
}

var sourceNames = map[string]string{
	updates.SourceHomebrew: "Homebrew",
	updates.SourceAppStore: "Mac App Store",
	updates.SourceSparkle:  "Apps with their own updater",
}

// sourceLabels are the short names for the table.
var sourceLabels = map[string]string{
	updates.SourceHomebrew: "Homebrew",
	updates.SourceAppStore: "App Store",
	updates.SourceSparkle:  "In-app updater",
}

func printUpdates(cmd *cobra.Command, d updatesData) {
	w := cmd.OutOrStdout()
	st := output.NewStyle(w)

	if len(d.Updates) == 0 {
		fmt.Fprintln(w, st.Bold("No updates found."))
	} else {
		fmt.Fprintln(w, st.Bold(plural(len(d.Updates), "update")+" available"))
		fmt.Fprintln(w)
		rows := [][]string{{"App", "Installed", "Available", "From"}}
		for _, u := range d.Updates {
			rows = append(rows, []string{output.Truncate(u.Name, 30), output.Truncate(u.Installed, 14), output.Truncate(u.Available, 14), sourceLabels[u.Source]})
		}
		output.Table(w, rows)

		fmt.Fprintln(w)
		fmt.Fprintln(w, st.Bold("How to update (Sapu never does it for you)"))
		var casks []string
		other, store := false, false
		for _, u := range d.Updates {
			switch u.Source {
			case updates.SourceHomebrew:
				casks = append(casks, u.Name)
			case updates.SourceAppStore:
				store = true
			default:
				other = true
			}
		}
		if len(casks) > 0 {
			output.WrapIndent(w, "  ", "Homebrew apps: "+st.Bold("brew upgrade --cask "+strings.Join(casks, " ")))
		}
		if store {
			output.WrapIndent(w, "  ", "Mac App Store apps: open the App Store and choose Updates.")
		}
		if other {
			output.WrapIndent(w, "  ", "Other apps: open the app and choose Check for Updates.")
		}
	}

	var skipped []string
	for _, s := range d.Sources {
		if s.Skipped != "" {
			skipped = append(skipped, sourceNames[s.ID]+": "+s.Skipped)
		}
	}
	if len(skipped) > 0 || len(d.Notes) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, st.Bold("Not checked"))
		for _, s := range skipped {
			output.WrapIndent(w, "  ", s)
		}
		for _, n := range d.Notes {
			output.WrapIndent(w, "  ", n.Name+": "+n.Message+".")
		}
	}

	fmt.Fprintln(w)
	for _, s := range d.Sources {
		if s.Checked > 0 {
			output.WrapIndent(w, "", fmt.Sprintf("Checked the update feeds of %s.", plural(s.Checked, "app")))
		}
	}
	if d.Offline {
		fmt.Fprintln(w, "Offline: Sapu made no network request.")
	} else {
		output.WrapIndent(w, "", "Sapu asked each app's own update server, as the app does. It never downloads or installs anything.")
	}
}
