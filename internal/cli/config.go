package cli

import (
	"fmt"
	"io/fs"
	"os"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/protect"
	"github.com/khmuhtadin/sapu/internal/rulesupdate"
)

// Every protect.Load, in the engine too, adds the protection files of the
// rule update in use and the whitelist paths of sapu.toml. protect cannot
// import the packages that know them, so the CLI connects them here.
func init() {
	protect.Extra = protectExtra
}

// protectExtra is what protect.Load adds to the built-in protection list and
// whitelist.txt. It can only add (see package protect).
func protectExtra(e env.Env) (fs.FS, []string, error) {
	cfg, err := config.Load(e)
	if err != nil {
		return nil, nil, err
	}
	r, err := rulesupdate.Active(e)
	if err != nil {
		return nil, nil, err
	}
	return r.Protect, cfg.Whitelist, nil
}

// Implemented in Wave 8 (W8-D).
func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show the settings file (sapu.toml) or write an example",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "show",
			Short: "Show the settings in effect and where each one comes from",
			RunE: func(cmd *cobra.Command, args []string) error {
				return finish(cmd, "config-show", runConfigShow(cmd, args))
			},
		},
		&cobra.Command{
			Use:   "path",
			Short: "Print where Sapu reads sapu.toml from",
			RunE: func(cmd *cobra.Command, args []string) error {
				return finish(cmd, "config-path", runConfigPath(cmd, args))
			},
		},
		&cobra.Command{
			Use:   "init",
			Short: "Write a commented example sapu.toml, only if there is none",
			RunE: func(cmd *cobra.Command, args []string) error {
				return finish(cmd, "config-init", runConfigInit(cmd, args))
			},
		},
	)
	return cmd
}

// configPathData is the data of config path and config init.
type configPathData struct {
	File    string `json:"file"`
	FromEnv bool   `json:"from_env"` // $SAPU_CONFIG chose the file
	Found   bool   `json:"found"`
}

// configData is the data of config show: the settings in effect.
type configData struct {
	File       string `json:"file"`
	FromEnv    bool   `json:"from_env"`
	Found      bool   `json:"found"`
	Profile    string `json:"profile"`
	Quarantine struct {
		MaxAgeSeconds int64 `json:"max_age_seconds"`
		MaxBytes      int64 `json:"max_bytes"`
	} `json:"quarantine"`
	Clean struct {
		Exclude []string `json:"exclude"`
	} `json:"clean"`
	Purge struct {
		Roots            []string `json:"roots"`
		OlderThanSeconds int64    `json:"older_than_seconds"` // 0: no filter
	} `json:"purge"`
	Whitelist []whitelistEntry `json:"whitelist"` // both lists Sapu reads
	// From says where each setting of sapu.toml came from: "sapu.toml" or
	// "default", by key ("profile", "quarantine.max_age", ...).
	From map[string]string `json:"from"`
}

type whitelistEntry struct {
	Path string `json:"path"`
	From string `json:"from"` // "sapu.toml" or "whitelist.txt"
}

func runConfigShow(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("config show takes no arguments, got %q", args[0]))
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	cfg, err := config.Load(e)
	if err != nil {
		return err
	}
	lines, err := protect.ReadWhitelist(e)
	if err != nil {
		return err
	}

	d := configData{File: cfg.File, FromEnv: cfg.FromEnv, Found: cfg.Found, Profile: cfg.Profile, From: map[string]string{}}
	d.Quarantine.MaxAgeSeconds = int64(cfg.MaxAge / time.Second)
	d.Quarantine.MaxBytes = cfg.MaxBytes
	d.Clean.Exclude = orEmpty(cfg.CleanExclude)
	d.Purge.Roots = orEmpty(cfg.PurgeRoots)
	d.Purge.OlderThanSeconds = int64(cfg.PurgeOlderThan / time.Second)
	d.Whitelist = []whitelistEntry{}
	for _, p := range cfg.Whitelist {
		d.Whitelist = append(d.Whitelist, whitelistEntry{p, "sapu.toml"})
	}
	for _, p := range lines {
		d.Whitelist = append(d.Whitelist, whitelistEntry{p, "whitelist.txt"})
	}
	for _, k := range config.Keys {
		d.From[k] = "default"
		if cfg.Set[k] {
			d.From[k] = "sapu.toml"
		}
	}

	w := humanOut(cmd)
	fmt.Fprintf(w, "Settings file: %s\n", configFileText(e, cfg.File, cfg.FromEnv, cfg.Found))
	fmt.Fprintln(w)
	olderThan := "none"
	if cfg.PurgeOlderThan > 0 {
		olderThan = config.FormatAge(cfg.PurgeOlderThan)
	}
	values := map[string][]string{
		"profile":             {cfg.Profile},
		"quarantine.max_age":  {config.FormatAge(cfg.MaxAge)},
		"quarantine.max_size": {config.FormatSize(cfg.MaxBytes)},
		"clean.exclude":       orNone(cfg.CleanExclude, "none"),
		"purge.roots":         orNone(cfg.PurgeRoots, "the current folder"),
		"purge.older_than":    {olderThan},
		"whitelist.paths":     orNone(cfg.Whitelist, "none"),
	}
	var rows [][]string
	for _, k := range config.Keys {
		for i, v := range values[k] {
			if i == 0 {
				rows = append(rows, []string{k, v, d.From[k]})
			} else {
				rows = append(rows, []string{"", v, ""})
			}
		}
	}
	for i, p := range orNone(lines, "none") {
		if i == 0 {
			rows = append(rows, []string{"whitelist.txt", p, "sapu whitelist"})
		} else {
			rows = append(rows, []string{"", p, ""})
		}
	}
	writeTable(w, []string{"SETTING", "VALUE", "FROM"}, rows)
	return printData(cmd, "config-show", d)
}

func runConfigPath(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("config path takes no arguments, got %q", args[0]))
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	path, fromEnv, err := config.Path(e)
	if err != nil {
		return err
	}
	_, statErr := os.Stat(path)
	fmt.Fprintln(humanOut(cmd), path)
	return printData(cmd, "config-path", configPathData{File: path, FromEnv: fromEnv, Found: statErr == nil})
}

func runConfigInit(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("config init takes no arguments, got %q", args[0]))
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	path, err := config.Init(e)
	if err != nil {
		return err
	}
	_, fromEnv, err := config.Path(e)
	if err != nil {
		return err
	}
	w := humanOut(cmd)
	fmt.Fprintf(w, "Wrote %s. Every setting in it is commented out.\n", tilde(e.Home, path))
	fmt.Fprintln(w, `Remove the "# " in front of a setting to change it, then check with: sapu config show`)
	return printData(cmd, "config-init", configPathData{File: path, FromEnv: fromEnv, Found: true})
}

// configFileText says where the settings file is and whether it was found.
func configFileText(e env.Env, file string, fromEnv, found bool) string {
	s := tilde(e.Home, file)
	if fromEnv {
		s += " (from $SAPU_CONFIG)"
	}
	if !found {
		s += " (not found, so every setting is the default)"
	}
	return s
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func orNone(s []string, none string) []string {
	if len(s) == 0 {
		return []string{none}
	}
	return slices.Clone(s)
}
