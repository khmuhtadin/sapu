package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/rulesupdate"
)

// Implemented in Wave 8 (W8-D).
func newRulesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rules",
		Short: "Show, install or reset signed rule updates",
	}
	var sig string
	install := &cobra.Command{
		Use:   "install BUNDLE --sig SIG",
		Short: "Install a signed rule bundle from a local file",
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "rules-install", runRulesInstall(cmd, args, sig))
		},
	}
	install.Flags().StringVar(&sig, "sig", "", "the bundle's signature file (required)")
	cmd.AddCommand(
		&cobra.Command{
			Use:   "status",
			Short: "Show which rules Sapu uses: built-in or an installed bundle",
			RunE: func(cmd *cobra.Command, args []string) error {
				return finish(cmd, "rules-status", runRulesStatus(cmd, args))
			},
		},
		install,
		&cobra.Command{
			Use:   "reset",
			Short: "Use the rules built into Sapu again",
			RunE: func(cmd *cobra.Command, args []string) error {
				return finish(cmd, "rules-reset", runRulesReset(cmd, args))
			},
		},
	)
	return cmd
}

// rulesData is the data of every rules command: the rules in use after it.
type rulesData struct {
	Source          string     `json:"source"`  // "embedded" or "bundle"
	Version         string     `json:"version"` // of the rules in use
	EmbeddedVersion string     `json:"embedded_version"`
	Created         *time.Time `json:"created,omitempty"`  // bundle only
	MinSapu         string     `json:"min_sapu,omitempty"` // bundle only
	File            string     `json:"file,omitempty"`     // bundle only
	Warning         string     `json:"warning,omitempty"`  // why an installed bundle is not used
	KeyConfigured   bool       `json:"key_configured"`     // this build can install bundles
}

func newRulesData(r rulesupdate.Rules) rulesData {
	d := rulesData{
		Source: r.Source, Version: r.Version, EmbeddedVersion: rulesupdate.EmbeddedVersion,
		MinSapu: r.MinSapu, File: r.File, Warning: r.Warning, KeyConfigured: rulesupdate.KeyConfigured(),
	}
	if !r.Created.IsZero() {
		d.Created = &r.Created
	}
	return d
}

func runRulesStatus(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("rules status takes no arguments, got %q", args[0]))
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	r, err := rulesupdate.Active(e)
	if err != nil {
		return err
	}
	writeRules(cmd, e, r)
	return printData(cmd, "rules-status", newRulesData(r))
}

// writeRules says which rules are in use.
func writeRules(cmd *cobra.Command, e env.Env, r rulesupdate.Rules) {
	w := humanOut(cmd)
	if r.Source == rulesupdate.SourceBundle {
		fmt.Fprintf(w, "Rules in use: signed update %s, made %s, for Sapu %s or newer.\n",
			r.Version, r.Created.Local().Format("2 Jan 2006"), r.MinSapu)
		fmt.Fprintf(w, "Stored in %s; its signature is checked at every load.\n", tilde(e.Home, r.File))
		fmt.Fprintf(w, "Built-in rules: %s. Protection is the built-in list plus the update's.\n", rulesupdate.EmbeddedVersion)
	} else {
		fmt.Fprintf(w, "Rules in use: built into Sapu, version %s.\n", r.Version)
	}
	if r.Warning != "" {
		fmt.Fprintf(w, "\nWarning: %s\n", r.Warning)
	}
	if !rulesupdate.KeyConfigured() {
		fmt.Fprintln(w, "This build has no signing key, so it cannot install rule updates.")
	}
}

func runRulesInstall(cmd *cobra.Command, args []string, sigFile string) error {
	if len(args) != 1 {
		return usageErr(errors.New("rules install takes one BUNDLE file"))
	}
	if sigFile == "" {
		return usageErr(errors.New("rules install needs --sig SIG, the bundle's signature file"))
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(sigFile)
	if err != nil {
		return err
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	b, err := rulesupdate.Install(e, data, sig, Version)
	if err != nil {
		return fmt.Errorf("rules not installed: %w", err)
	}
	r, err := rulesupdate.Active(e)
	if err != nil {
		return err
	}
	fmt.Fprintf(humanOut(cmd), "Installed rules %s (made %s). Sapu uses them from now on.\n",
		b.Version, b.Created.Local().Format("2 Jan 2006"))
	fmt.Fprintln(humanOut(cmd), "To go back to the built-in rules: sapu rules reset")
	return printData(cmd, "rules-install", newRulesData(r))
}

func runRulesReset(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("rules reset takes no arguments, got %q", args[0]))
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	if err := rulesupdate.Reset(e); err != nil {
		return err
	}
	r, err := rulesupdate.Active(e)
	if err != nil {
		return err
	}
	fmt.Fprintf(humanOut(cmd), "Sapu uses its built-in rules (%s). Installed updates stay in %s; nothing was deleted.\n",
		rulesupdate.EmbeddedVersion, tilde(e.Home, e.StateDir)+"/rules")
	return printData(cmd, "rules-reset", newRulesData(r))
}

// activeRules returns the rules in use, a verified installed update or the
// built-in rules, and says on stderr why an installed update is not used.
func activeRules(cmd *cobra.Command, e env.Env) (rulesupdate.Rules, error) {
	r, err := rulesupdate.Active(e)
	if err != nil {
		return r, err
	}
	if r.Warning != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "sapu: warning:", r.Warning)
	}
	return r, nil
}

// loadCleaners returns the cleanup rules in use.
func loadCleaners(cmd *cobra.Command, e env.Env) ([]rules.Rule, error) {
	r, err := activeRules(cmd, e)
	if err != nil {
		return nil, err
	}
	return rules.Load(r.Cleaners)
}
