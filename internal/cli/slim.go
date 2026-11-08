package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/slim"
)

// slimShown is how many apps the table lists; --json lists all.
const slimShown = 20

// slimData is the data of `sapu slim --json`.
type slimData struct {
	Architecture        string     `json:"architecture"`         // this Mac: "arm64" or "x86_64"
	ForeignArchitecture string     `json:"foreign_architecture"` // the other one
	Languages           []string   `json:"languages"`            // the languages you use
	AppsScanned         int        `json:"apps_scanned"`
	Apps                []slim.App `json:"apps"` // apps with something to save, the largest first
	ForeignBytes        int64      `json:"foreign_arch_bytes"`
	LanguageBytes       int64      `json:"language_bytes"`
	TotalBytes          int64      `json:"total_bytes"`
}

func newSlimCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "slim",
		Short: "Show how much Intel code and unused languages apps carry",
		Long: `Show how much space apps could save by dropping code for the other Mac
architecture (Intel on an Apple Silicon Mac) and languages you do not use.

Slim only reads and only reports. It never changes an app: taking files out of
a signed app breaks its code signature, so macOS may refuse to open it.`,
		Args: cobra.NoArgs,
		RunE: runSlim,
	}
}

func runSlim(cmd *cobra.Command, args []string) error {
	e, err := loadEnv()
	if err != nil {
		return err
	}
	p := newProgress(cmd)
	p.Status("scan", "Reading apps (this can take a minute)…")
	r, err := slim.Scan(cmd.Context(), e)
	p.Done()
	if err != nil {
		return err
	}

	foreign, languages := r.Total()
	data := slimData{
		Architecture: r.Arch, ForeignArchitecture: r.ForeignArch, Languages: r.Languages,
		AppsScanned: r.Scanned, Apps: r.Apps,
		ForeignBytes: foreign, LanguageBytes: languages, TotalBytes: foreign + languages,
	}
	if data.Apps == nil {
		data.Apps = []slim.App{}
	}
	if jsonOut {
		return output.JSON(cmd.OutOrStdout(), "slim", data)
	}
	printSlim(cmd, data)
	return nil
}

func printSlim(cmd *cobra.Command, d slimData) {
	w := cmd.OutOrStdout()
	st := output.NewStyle(w)

	// foreignName is the code Sapu counts; thisMac is the code that runs here.
	foreignName, thisMac := "Intel", "Apple Silicon"
	if d.ForeignArchitecture == "arm64" {
		foreignName, thisMac = "Apple Silicon", "Intel"
	}

	fmt.Fprintln(w, st.Bold("Apps could be smaller"))
	output.WrapIndent(w, "", fmt.Sprintf("This Mac is %s (%s). Your languages: %s. Looked at %d apps.",
		thisMac, d.Architecture, strings.Join(d.Languages, ", "), d.AppsScanned))
	fmt.Fprintln(w)

	if len(d.Apps) == 0 {
		fmt.Fprintln(w, "No app carries "+foreignName+" code or unused languages.")
		fmt.Fprintln(w)
	} else {
		rows := [][]string{{"App", foreignName + " code", "Languages", "Could save"}}
		rest := int64(0)
		for i, a := range d.Apps {
			if i >= slimShown {
				rest += a.Savings()
				continue
			}
			rows = append(rows, []string{
				output.Truncate(a.Name, 28),
				output.Bytes(a.ForeignBytes), output.Bytes(a.LanguageBytes), output.Bytes(a.Savings()),
			})
		}
		output.Table(w, rows, 1, 2, 3)
		if n := len(d.Apps) - slimShown; n > 0 {
			fmt.Fprintf(w, "…and %d more apps, %s in all (`sapu slim --json` lists them).\n", n, output.Bytes(rest))
		}
		fmt.Fprintln(w)
		fmt.Fprintf(w, "%s %s at most (%s code, %s languages)\n", st.Bold("Total:"),
			output.Bytes(d.TotalBytes), output.Bytes(d.ForeignBytes), output.Bytes(d.LanguageBytes))
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w, st.Bold("Why Sapu does not remove this"))
	output.WrapIndent(w, "", "Developers sign their apps. Taking code or language folders out of a signed app breaks its code signature and notarization: Gatekeeper may refuse to open it, and the keychain may stop trusting it. The app's next update would also put the files back.")
	fmt.Fprintln(w)
	output.WrapIndent(w, "", "What helps: when the developer offers an Apple-Silicon-only download, install that build.")
}
