package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/protect"
)

func newWhitelistCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "whitelist",
		Short: "Manage paths Sapu must never touch",
	}
	cmd.AddCommand(
		whitelistEntryCmd("add ENTRY", "add", "Never touch this path (it may use * globs)", protect.AddWhitelist,
			"Added %s. Sapu will never touch it.\n"),
		whitelistEntryCmd("remove ENTRY", "remove", "Allow Sapu to touch this path again", protect.RemoveWhitelist,
			"Removed %s from the whitelist.\n"),
		&cobra.Command{
			Use:   "list",
			Short: "Show the whitelist",
			RunE: func(cmd *cobra.Command, args []string) error {
				return finish(cmd, "whitelist-list", runWhitelistList(cmd, args))
			},
		},
	)
	return cmd
}

// whitelistData is the data of whitelist add and remove: the list of
// whitelist.txt after the change.
type whitelistData struct {
	Entries []string `json:"entries"`
}

// whitelistListData is the data of whitelist list. ConfigEntries are the
// paths of sapu.toml: read-only here, changed by editing that file.
type whitelistListData struct {
	Entries       []string `json:"entries"`
	ConfigEntries []string `json:"config_entries"`
}

func whitelistEntryCmd(use, verb, short string, change func(env.Env, string) error, done string) *cobra.Command {
	name := "whitelist-" + verb
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return finish(cmd, name, usageErr(fmt.Errorf("whitelist %s takes one ENTRY", verb)))
			}
			e, err := loadEnv()
			if err != nil {
				return finish(cmd, name, err)
			}
			if err := change(e, args[0]); err != nil {
				return finish(cmd, name, err)
			}
			fmt.Fprintf(humanOut(cmd), done, args[0])
			return finish(cmd, name, printWhitelist(cmd, e, name, false))
		},
	}
}

func runWhitelistList(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usageErr(errors.New("whitelist list takes no arguments"))
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	return printWhitelist(cmd, e, "whitelist-list", true)
}

// printWhitelist prints the entries of whitelist.txt, as lines (when show is
// true) or only as the envelope of --json. A listing also shows the paths of
// sapu.toml, marked, because Sapu honours them too.
func printWhitelist(cmd *cobra.Command, e env.Env, name string, show bool) error {
	entries, err := protect.ReadWhitelist(e)
	if err != nil {
		return err
	}
	if entries == nil {
		entries = []string{}
	}
	if !show {
		return printData(cmd, name, whitelistData{Entries: entries})
	}

	cfg, err := config.Load(e)
	if err != nil {
		return err
	}
	fromConfig := cfg.Whitelist
	if fromConfig == nil {
		fromConfig = []string{}
	}
	out := humanOut(cmd)
	if len(entries) == 0 && len(fromConfig) == 0 {
		fmt.Fprintln(out, "The whitelist is empty.")
	}
	for _, entry := range entries {
		fmt.Fprintln(out, entry)
	}
	for _, entry := range fromConfig {
		fmt.Fprintf(out, "%s  (from sapu.toml, read-only: edit %s)\n", entry, tilde(e.Home, cfg.File))
	}
	return printData(cmd, name, whitelistListData{Entries: entries, ConfigEntries: fromConfig})
}
