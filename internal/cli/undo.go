package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/fsop"
	"github.com/khmuhtadin/sapu/internal/runlog"
)

func newUndoCmd() *cobra.Command {
	var last bool
	cmd := &cobra.Command{
		Use:   "undo [RUN_ID]",
		Short: "Put back everything a run moved to quarantine",
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "undo", runUndo(cmd, args, last))
		},
	}
	cmd.Flags().BoolVar(&last, "last", false, "undo the most recent run that can still be undone")
	return cmd
}

type undoData struct {
	RunID     string          `json:"run_id"`
	Restored  []fsop.Restored `json:"restored"`
	Conflicts int             `json:"conflicts"`       // items put next to the original because the original path was taken
	Error     string          `json:"error,omitempty"` // why the rest could not come back; they stay in quarantine
}

func runUndo(cmd *cobra.Command, args []string, last bool) error {
	if last == (len(args) == 1) || len(args) > 1 {
		return usageErr(errors.New("give one run id, or --last"))
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	id := ""
	if last {
		if id, err = lastUndoable(e); err != nil {
			return err
		}
	} else {
		id = args[0]
	}

	restored, err := fsop.Restore(e, id)
	out := humanOut(cmd)
	data := undoData{RunID: id, Restored: restored}
	if data.Restored == nil {
		data.Restored = []fsop.Restored{}
	}
	for _, r := range restored {
		if r.Conflict {
			data.Conflicts++
			fmt.Fprintf(out, "Something is already at the original place, so this was restored next to the original as %s\n", tilde(e.Home, r.To))
		} else {
			fmt.Fprintf(out, "Restored %s\n", tilde(e.Home, r.To))
		}
	}
	if err != nil && len(restored) == 0 {
		return err
	}
	fmt.Fprintf(out, "Restored %s from run %s.\n", plural(len(restored), "item"), id)
	if err == nil {
		return printData(cmd, "undo", data)
	}
	// Some items came back before the error: report them, exit 3.
	data.Error = err.Error()
	fmt.Fprintf(out, "The rest could not be restored and stays in quarantine. Fix the problem below, then run sapu undo %s again.\n", id)
	if perr := printData(cmd, "undo", data); perr != nil {
		return errors.Join(err, perr)
	}
	return reported{partialErr(err)}
}

// lastUndoable returns the newest run that has something to put back.
func lastUndoable(e env.Env) (string, error) {
	runs, err := runlog.List(e)
	if err != nil {
		return "", err
	}
	for _, r := range runs {
		if r.Status == runlog.StatusDone || r.Status == runlog.StatusInterrupted {
			return r.ID, nil
		}
	}
	return "", errors.New("no run to undo: none is left that still holds items in quarantine")
}
