package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/finders"
	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/plan"
)

const backupsLong = `Lists the iPhone and iPad backups in
~/Library/Application Support/MobileSync/Backup, newest first, with the
device, model, date of the last backup and size.

Reading them needs Full Disk Access for your terminal app; "sapu doctor"
shows how. Nothing is chosen for you: a backup cannot be made again once the
phone has changed, and the newest backup of a device may be the only copy of
that iPhone. Pick numbers from the list with --pick, e.g. --pick 2 or
--pick 2-3; Sapu then asks before it moves them to quarantine.`

func newBackupsCmd() *cobra.Command {
	var f cleanupFlags
	var pick string
	cmd := &cobra.Command{
		Use:   "backups",
		Short: "List iPhone and iPad backups and remove the ones you pick",
		Long:  backupsLong,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "backups", runBackups(cmd, &f, pick))
		},
	}
	f.addPlanFlag(cmd)
	f.addApplyFlags(cmd)
	cmd.Flags().StringVar(&pick, "pick", "", "move these numbers from the list, e.g. 2 or 2-3")
	return cmd
}

func runBackups(cmd *cobra.Command, f *cleanupFlags, pick string) error {
	picks, err := foundPicks(cmd, pick)
	if err != nil {
		return err
	}
	if err := checkFoundFlags(f, picks); err != nil {
		return err
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	found, err := finders.Backups(ctx, e)
	if err != nil {
		return err
	}
	items := make([]plan.Item, len(found))
	for i, b := range found {
		items[i] = b.Item
	}
	p, err := engine.Plan(ctx, e, plan.KindBackups, items)
	if err != nil {
		return err
	}
	backups := keepFound(p, found, func(b finders.Backup) string { return b.Item.Path })

	data := foundList[backupJSON]{Items: []backupJSON{}, Skipped: p.Skipped}
	paths := make([]string, len(backups))
	for i, b := range backups {
		paths[i] = b.Item.Path
		j := backupJSON{
			Number: i + 1, Path: b.Item.Path, Bytes: b.Item.Bytes, Device: b.Device,
			Model: b.Model, Newest: b.Newest, Explain: b.Item.Explain,
		}
		if !b.LastBackup.IsZero() {
			j.LastBackup = &b.LastBackup
		}
		data.Items = append(data.Items, j)
	}
	show := func(w io.Writer) {
		if len(backups) == 0 {
			fmt.Fprintln(w, "No iPhone or iPad backups found.")
			return
		}
		rows := make([][]string, len(backups))
		var total int64
		anyNewest := false
		for i, b := range backups {
			total += b.Item.Bytes
			device, note := b.Device, ""
			if device == "" {
				device = "(unknown)"
			}
			if b.Newest {
				note, anyNewest = "newest", true
			}
			rows[i] = []string{fmt.Sprint(i + 1), output.Bytes(b.Item.Bytes), foundDay(b.LastBackup), device, b.Model, note}
		}
		writeTable(w, []string{"#", "Size", "Last backup", "Device", "Model", "Note"}, rows, 0, 1)
		fmt.Fprintf(w, "\n%s, %s.\n", plural(len(backups), "backup"), output.Bytes(total))
		if anyNewest {
			writeWrapped(w, newStyle(w), 0, "\"newest\" may be the only copy of that iPhone.")
		}
	}
	return runFound(cmd, "backups", e, p, paths, picks, *f, show, data)
}

// backupJSON is one backup in the list that `sapu backups --json` prints.
type backupJSON struct {
	Number     int        `json:"number"`
	Path       string     `json:"path"`
	Bytes      int64      `json:"bytes"`
	Device     string     `json:"device"`
	Model      string     `json:"model"`
	LastBackup *time.Time `json:"last_backup,omitempty"` // left out when unknown
	Newest     bool       `json:"newest"`                // the newest backup of its device
	Explain    string     `json:"explain"`
}
