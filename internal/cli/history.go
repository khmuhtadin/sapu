package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/runlog"
)

func newHistoryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "history",
		Short: "List past runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "history", runHistory(cmd, args))
		},
	}
}

type historyData struct {
	Runs []runlog.Summary `json:"runs"`
}

func runHistory(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usageErr(errors.New("history takes no arguments"))
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	runs, err := runlog.List(e)
	if err != nil {
		return err
	}
	if runs == nil {
		runs = []runlog.Summary{}
	}

	out := humanOut(cmd)
	if len(runs) == 0 {
		fmt.Fprintln(out, "No runs yet.")
	} else {
		var rows [][]string
		for _, r := range runs {
			rows = append(rows, []string{r.ID, r.Started.Local().Format("2006-01-02 15:04"), r.Kind,
				fmt.Sprint(r.Items), output.Bytes(r.Bytes), r.Status})
		}
		writeTable(out, []string{"Run", "Date", "Kind", "Items", "Size", "Status"}, rows, 3, 4)
	}
	return printData(cmd, "history", historyData{Runs: runs})
}
