package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/analyze"
	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/walk"
)

// analyzeNode is one entry of the tree printed by `sapu analyze --json`.
type analyzeNode struct {
	Name     string        `json:"name"`
	Path     string        `json:"path"`
	Bytes    int64         `json:"bytes"`
	Files    int64         `json:"files"`
	IsDir    bool          `json:"is_dir"`
	Children []analyzeNode `json:"children,omitempty"`
}

// analyzeData is the data of `sapu analyze --json`.
type analyzeData struct {
	Top   int         `json:"top"`   // children kept per folder
	Depth int         `json:"depth"` // levels shown below the root
	Root  analyzeNode `json:"root"`  // Bytes and Files are the totals of the whole tree
}

func newAnalyzeCmd() *cobra.Command {
	var top, depth int
	cmd := &cobra.Command{
		Use:   "analyze [PATH]",
		Short: "Explore what takes space on disk",
		Long: `Explore what takes space on disk. PATH defaults to your home folder.

In a terminal this opens an interactive explorer. With --json, or when the
output is not a terminal, it prints the largest entries instead.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if top < 1 || (cmd.Flags().Changed("depth") && depth < 1) {
				return usageErr(errors.New("--top and --depth must be 1 or more"))
			}
			if !cmd.Flags().Changed("depth") {
				depth = 1 // a table with two levels is too long to read
				if jsonOut {
					depth = 2
				}
			}
			return runAnalyze(cmd, args, top, depth)
		},
	}
	cmd.Flags().IntVar(&top, "top", 20, "entries to show per folder")
	cmd.Flags().IntVar(&depth, "depth", 0, "folder levels to show (default 2 with --json, 1 otherwise)")
	return cmd
}

func runAnalyze(cmd *cobra.Command, args []string, top, depth int) error {
	e, err := loadEnv()
	if err != nil {
		return err
	}
	root := e.Home
	if len(args) == 1 {
		root = e.Expand(args[0])
	}
	if root, err = filepath.Abs(root); err != nil {
		return err
	}
	if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s does not exist", root)
	} else if err != nil {
		return err
	}

	if !jsonOut && output.IsTTY(cmd.InOrStdin()) && output.IsTTY(cmd.OutOrStdout()) {
		return analyze.Run(cmd.Context(), e, root)
	}

	p := newProgress(cmd)
	p.Status("scan", "Measuring "+output.Tilde(e.Home, root)+"…")
	tree, err := analyze.Top(cmd.Context(), root, top, depth)
	p.Done()
	if err != nil {
		return err
	}
	if jsonOut {
		return output.JSON(cmd.OutOrStdout(), "analyze", analyzeData{Top: top, Depth: depth, Root: toAnalyzeNode(tree)})
	}
	printAnalyze(cmd, e.Home, tree)
	return nil
}

func toAnalyzeNode(n *walk.Node) analyzeNode {
	out := analyzeNode{Name: n.Name, Path: n.Path, Bytes: n.Bytes, Files: n.Files, IsDir: n.IsDir}
	for _, c := range n.Children {
		out.Children = append(out.Children, toAnalyzeNode(c))
	}
	return out
}

func printAnalyze(cmd *cobra.Command, home string, tree *walk.Node) {
	w := cmd.OutOrStdout()
	st := output.NewStyle(w)
	fmt.Fprintf(w, "%s  %s in %d files\n\n", st.Bold(output.Tilde(home, tree.Path)), output.Bytes(tree.Bytes), tree.Files)

	var rows [][]string
	var add func(n *walk.Node, level int)
	add = func(n *walk.Node, level int) {
		for _, c := range n.Children {
			name := c.Name
			if c.IsDir {
				name += "/"
			}
			rows = append(rows, []string{output.Bytes(c.Bytes), strings.Repeat("  ", level) + output.Truncate(name, 60)})
			add(c, level+1)
		}
	}
	add(tree, 0)
	if len(rows) == 0 {
		fmt.Fprintln(w, "Nothing inside.")
		return
	}
	output.Table(w, rows, 0)
}
