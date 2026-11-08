package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/finders"
	"github.com/khmuhtadin/sapu/internal/output"
)

const messagesLong = `Lists the largest attachments in ~/Library/Messages/Attachments that are at
least --min-size and older than --older-than, biggest first.

This command only looks. Removing files behind Messages' back leaves broken
pictures in your conversations, and with Messages in iCloud the result is
unclear. Sapu prints Apple's own way to free the space instead.

Reading the attachments needs Full Disk Access for your terminal app;
"sapu doctor" shows how.`

const messagesShown = 50 // rows listed unless --verbose

func newMessagesCmd() *cobra.Command {
	var verbose bool
	var minSize, olderThan string
	cmd := &cobra.Command{
		Use:   "messages",
		Short: "Find large Messages attachments",
		Long:  messagesLong,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "messages", runMessages(cmd, minSize, olderThan, verbose))
		},
	}
	cmd.Flags().StringVar(&minSize, "min-size", "10MB", "smallest attachment to list, e.g. 10MB, 100MB")
	cmd.Flags().StringVar(&olderThan, "older-than", "1y", "only attachments older than this, e.g. 90d, 1y")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "list every attachment, not only the largest")
	return cmd
}

func runMessages(cmd *cobra.Command, minSize, olderThan string, verbose bool) error {
	var err error
	opts := finders.MessagesOptions{Now: time.Now()}
	if opts.MinSize, err = finders.ParseSize(minSize); err != nil {
		return usageErr(fmt.Errorf("--min-size: %w", err))
	}
	if opts.OlderThan, err = finders.ParseAge(olderThan); err != nil {
		return usageErr(fmt.Errorf("--older-than: %w", err))
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	found, err := finders.Messages(cmd.Context(), e, opts)
	if err != nil {
		return err
	}

	data := messagesData{Items: []messageJSON{}}
	for _, a := range found {
		data.TotalBytes += a.Bytes
		data.Items = append(data.Items, messageJSON{Path: a.Path, Bytes: a.Bytes, Modified: a.Modified, Type: a.Type})
	}
	out := humanOut(cmd)
	writeMessages(out, found, minSize, olderThan, verbose)
	return printData(cmd, "messages", data)
}

func writeMessages(w io.Writer, found []finders.Attachment, minSize, olderThan string, verbose bool) {
	if len(found) == 0 {
		fmt.Fprintf(w, "No Messages attachment of at least %s and older than %s.\n", minSize, olderThan)
	} else {
		shown := found
		if !verbose && len(shown) > messagesShown {
			shown = shown[:messagesShown]
		}
		rows := make([][]string, len(shown))
		var total int64
		for _, a := range found {
			total += a.Bytes
		}
		for i, a := range shown {
			rows[i] = []string{output.Bytes(a.Bytes), foundDay(a.Modified), a.Type, filepath.Base(a.Path)}
		}
		writeTable(w, []string{"Size", "Date", "Type", "File"}, rows, 0)
		if len(shown) < len(found) {
			fmt.Fprintf(w, "… and %d more; -v lists them all\n", len(found)-len(shown))
		}
		fmt.Fprintf(w, "\n%s, %s.\n", plural(len(found), "attachment"), output.Bytes(total))
	}

	st := newStyle(w)
	fmt.Fprintln(w)
	fmt.Fprintln(w, st.bold("Sapu does not remove these"))
	writeWrapped(w, st, 2, "Deleting the files behind Messages' back leaves broken pictures in your conversations, and with Messages in iCloud the result is unclear. Use Apple's own tools instead:")
	fmt.Fprintln(w, "  Messages › Settings › General › Keep messages")
	fmt.Fprintln(w, "  System Settings › General › Storage › Messages › review large attachments")
}

type messagesData struct {
	Items      []messageJSON `json:"items"`
	TotalBytes int64         `json:"total_bytes"`
}

// messageJSON is one attachment of `sapu messages --json`.
type messageJSON struct {
	Path     string    `json:"path"`
	Bytes    int64     `json:"bytes"`
	Modified time.Time `json:"modified"`
	Type     string    `json:"type"` // the extension; empty when there is none
}
