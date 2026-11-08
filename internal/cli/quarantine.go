package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/fsop"
	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/runlog"
)

func newQuarantineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "quarantine",
		Short: "List, expire or empty the quarantine",
	}
	cmd.AddCommand(newQuarantineListCmd(), newQuarantineEmptyCmd(), newQuarantineExpireCmd())
	return cmd
}

// held is one run that still has items in quarantine.
type held struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	Started      time.Time `json:"started"`
	Bytes        int64     `json:"bytes"`
	ExpiresAfter time.Time `json:"expires_after"` // earliest time a later run may expire it
}

// heldRuns lists the runs with items in quarantine, newest first.
func heldRuns(e env.Env) ([]held, error) {
	cfg, err := config.Load(e)
	if err != nil {
		return nil, err
	}
	sizes, err := fsop.QuarantineSize(e)
	if err != nil {
		return nil, err
	}
	runs, err := runlog.List(e)
	if err != nil {
		return nil, err
	}
	out := []held{}
	for _, r := range runs {
		if b, ok := sizes[r.ID]; ok {
			out = append(out, held{ID: r.ID, Kind: r.Kind, Started: r.Started, Bytes: b, ExpiresAfter: expiresAfter(r.Started, cfg)})
		}
	}
	return out, nil
}

func newQuarantineListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show what is in quarantine and when it can expire",
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "quarantine-list", runQuarantineList(cmd, args))
		},
	}
}

type quarantineListData struct {
	Runs       []held `json:"runs"`
	TotalBytes int64  `json:"total_bytes"`
}

func runQuarantineList(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usageErr(errors.New("quarantine list takes no arguments"))
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	runs, err := heldRuns(e)
	if err != nil {
		return err
	}
	data := quarantineListData{Runs: runs}
	out := humanOut(cmd)
	if len(runs) == 0 {
		fmt.Fprintln(out, "The quarantine is empty.")
	} else {
		var rows [][]string
		now := time.Now()
		for _, r := range runs {
			data.TotalBytes += r.Bytes
			rows = append(rows, []string{r.ID, output.Bytes(r.Bytes), ageText(now.Sub(r.Started)), r.ExpiresAfter.Local().Format("Mon 2 Jan")})
		}
		writeTable(out, []string{"Run", "Size", "Age", "Expires after"}, rows, 1, 2)
		fmt.Fprintf(out, "\nIn quarantine: %s. Free a run now with `sapu quarantine empty RUN_ID`; put it back with `sapu undo RUN_ID`.\n",
			output.Bytes(data.TotalBytes))
	}
	return printData(cmd, "quarantine-list", data)
}

func newQuarantineEmptyCmd() *cobra.Command {
	var all, yes bool
	cmd := &cobra.Command{
		Use:   "empty [RUN_ID | --all]",
		Short: "Delete quarantined items for good, to free the space now",
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "quarantine-empty", runQuarantineEmpty(cmd, args, all, yes))
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "empty every run")
	cmd.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation")
	return cmd
}

type emptied struct {
	ID    string `json:"id"`
	Freed int64  `json:"freed"`
}

type quarantineEmptyData struct {
	Runs  []emptied `json:"runs"`
	Freed int64     `json:"freed"`
}

func runQuarantineEmpty(cmd *cobra.Command, args []string, all, yes bool) error {
	if all == (len(args) == 1) || len(args) > 1 {
		return usageErr(errors.New("give one run id, or --all"))
	}
	if jsonOut && !yes {
		return usageErr(errors.New("--json cannot ask for confirmation; add --yes"))
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	runs, err := heldRuns(e)
	if err != nil {
		return err
	}
	if !all {
		runs = onlyRun(runs, args[0])
		if len(runs) == 0 {
			return fmt.Errorf("run %s has nothing in quarantine (see `sapu quarantine list`)", args[0])
		}
	}
	if len(runs) == 0 {
		fmt.Fprintln(humanOut(cmd), "The quarantine is already empty.")
		return printData(cmd, "quarantine-empty", quarantineEmptyData{Runs: []emptied{}})
	}

	var total int64
	for _, r := range runs {
		total += r.Bytes
	}
	if !yes {
		fmt.Fprintf(cmd.ErrOrStderr(), "This deletes the quarantined items of %s (%s) for good. They can no longer be undone.\n",
			plural(len(runs), "run"), output.Bytes(total))
		if err := confirmYes(cmd, "Delete them for good?"); err != nil {
			return err
		}
	}

	out := humanOut(cmd)
	data := quarantineEmptyData{Runs: []emptied{}}
	for _, r := range runs {
		if err := cmd.Context().Err(); err != nil {
			return err
		}
		freed, err := fsop.Empty(e, r.ID)
		if err != nil {
			return fmt.Errorf("run %s: %w", r.ID, err)
		}
		data.Runs = append(data.Runs, emptied{ID: r.ID, Freed: freed})
		data.Freed += freed
		fmt.Fprintf(out, "Deleted run %s for good (%s).\n", r.ID, output.Bytes(freed))
	}
	fmt.Fprintf(out, "Deleted %s in total.\n", output.Bytes(data.Freed))
	return printData(cmd, "quarantine-empty", data)
}

func onlyRun(runs []held, id string) []held {
	for _, r := range runs {
		if r.ID == id {
			return []held{r}
		}
	}
	return nil
}

func newQuarantineExpireCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "expire",
		Short: "Delete runs past max_age, and the oldest beyond max_size",
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "quarantine-expire", runQuarantineExpire(cmd, args))
		},
	}
}

type quarantineExpireData struct {
	Freed int64 `json:"freed"`
}

func runQuarantineExpire(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usageErr(errors.New("quarantine expire takes no arguments"))
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	cfg, err := config.Load(e)
	if err != nil {
		return err
	}
	freed, err := fsop.Expire(e, time.Now(), cfg.MaxAge, cfg.MaxBytes)
	out := humanOut(cmd)
	if freed > 0 {
		fmt.Fprintf(out, "Deleted %s from runs that had expired.\n", output.Bytes(freed))
	}
	if err != nil {
		return err
	}
	if freed == 0 {
		fmt.Fprintf(out, "Nothing has expired. Runs are kept for %s, within %s.\n", config.AgeWords(cfg.MaxAge), output.Bytes(cfg.MaxBytes))
	}
	return printData(cmd, "quarantine-expire", quarantineExpireData{Freed: freed})
}
