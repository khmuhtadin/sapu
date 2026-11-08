package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/khmuhtadin/sapu/internal/analyze"
	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/doctor"
	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/explain"
	"github.com/khmuhtadin/sapu/internal/fsop"
	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/rulesupdate"
	"github.com/khmuhtadin/sapu/internal/runlog"
	"github.com/khmuhtadin/sapu/internal/scan"
	"github.com/khmuhtadin/sapu/internal/walk"
)

// cleanPlan scans with the cleanup rules in use (an installed rule update
// or the built-in rules) and returns the engine's plan. Both only read.
func cleanPlan(ctx context.Context, e env.Env) (*plan.Plan, error) {
	r, err := rulesupdate.Active(e)
	if err != nil {
		return nil, err
	}
	rs, err := rules.Load(r.Cleaners)
	if err != nil {
		return nil, err
	}
	items, err := scan.Scan(ctx, e, rs, nil)
	if err != nil {
		return nil, err
	}
	return engine.Plan(ctx, e, plan.KindClean, items)
}

type summaryData struct {
	FreeBytes      int64          `json:"free_bytes"`
	TotalBytes     int64          `json:"total_bytes"`
	Reclaimable    summaryReclaim `json:"reclaimable"`
	ProtectedItems int            `json:"protected_items"` // protected or whitelisted
	InUseItems     int            `json:"in_use_items"`
	FullDiskAccess bool           `json:"full_disk_access"`
}

// summaryReclaim counts what a cleanup could move to quarantine.
// Selected is safe plus moderate; risky items are never selected.
type summaryReclaim struct {
	SafeBytes     int64 `json:"safe_bytes"`
	ModerateBytes int64 `json:"moderate_bytes"`
	SelectedBytes int64 `json:"selected_bytes"`
	RiskyBytes    int64 `json:"risky_bytes"`
	Items         int   `json:"items"`
}

func runSummary(ctx context.Context, e env.Env, args json.RawMessage) (toolResult, error) {
	if err := decodeArgs(args, &struct{}{}); err != nil {
		return toolResult{}, err
	}
	var st unix.Statfs_t
	if err := unix.Statfs(e.Home, &st); err != nil {
		return toolResult{}, fmt.Errorf("read free space: %w", err)
	}
	pl, err := cleanPlan(ctx, e)
	if err != nil {
		return toolResult{}, err
	}

	d := summaryData{
		FreeBytes:      int64(st.Bavail) * int64(st.Bsize),
		TotalBytes:     int64(st.Blocks) * int64(st.Bsize),
		FullDiskAccess: doctor.HasFullDiskAccess(e),
	}
	// Items that Sapu would only suggest a command for are not counted:
	// Sapu does not move them.
	for _, it := range pl.Items {
		if it.Action != rules.ActionQuarantine {
			continue
		}
		d.Reclaimable.Items++
		switch it.Risk {
		case rules.RiskSafe:
			d.Reclaimable.SafeBytes += it.Bytes
		case rules.RiskModerate:
			d.Reclaimable.ModerateBytes += it.Bytes
		case rules.RiskRisky:
			d.Reclaimable.RiskyBytes += it.Bytes
		}
	}
	d.Reclaimable.SelectedBytes = d.Reclaimable.SafeBytes + d.Reclaimable.ModerateBytes
	for _, s := range pl.Skipped {
		switch s.Reason {
		case plan.ReasonProtected, plan.ReasonWhitelisted:
			d.ProtectedItems++
		case plan.ReasonInUse:
			d.InUseItems++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Free space: %s of %s\n", output.Bytes(d.FreeBytes), output.Bytes(d.TotalBytes))
	fmt.Fprintf(&b, "Can be cleaned: %s (safe %s, moderate %s) in %d items\n",
		output.Bytes(d.Reclaimable.SelectedBytes), output.Bytes(d.Reclaimable.SafeBytes),
		output.Bytes(d.Reclaimable.ModerateBytes), d.Reclaimable.Items)
	fmt.Fprintf(&b, "Risky items hold %s. Sapu never selects them.\n", output.Bytes(d.Reclaimable.RiskyBytes))
	fmt.Fprintf(&b, "Protected and left alone: %d\n", d.ProtectedItems)
	if d.InUseItems > 0 {
		fmt.Fprintf(&b, "Skipped because an app has them open: %d\n", d.InUseItems)
	}
	if d.FullDiskAccess {
		b.WriteString("Full Disk Access: on\n")
	} else {
		b.WriteString("Full Disk Access: off (Sapu cannot measure Messages, iPhone backups and other protected folders; the user can turn it on with `sapu doctor --open`)\n")
	}
	b.WriteString("Next: plan_clean prepares a plan for the user to review. explain shows what fills System Data.")
	return toolResult{b.String(), d}, nil
}

type explainData struct {
	Rows          []explain.Row `json:"rows"`
	MeasuredBytes int64         `json:"measured_bytes"` // sum of the measured rows: a lower bound
	Unmeasured    int           `json:"unmeasured"`     // rows that could not be measured
}

func runExplain(ctx context.Context, e env.Env, args json.RawMessage) (toolResult, error) {
	if err := decodeArgs(args, &struct{}{}); err != nil {
		return toolResult{}, err
	}
	rows, err := explain.Explain(ctx, e)
	if err != nil {
		return toolResult{}, err
	}
	d := explainData{Rows: rows}
	if d.Rows == nil {
		d.Rows = []explain.Row{}
	}

	var b strings.Builder
	b.WriteString("What fills \"System Data\":\n")
	for _, r := range rows {
		size := output.Bytes(r.Bytes)
		if r.Bytes < 0 {
			d.Unmeasured++
			size = "not measured (" + r.Unmeasured + ")"
		} else {
			d.MeasuredBytes += r.Bytes
		}
		fmt.Fprintf(&b, "\n%s: %s\n  %s\n  What to do: %s\n", r.Title, size, r.Explain, r.Action)
		if len(r.Command) > 0 {
			cmd := output.ShellCommand(r.Command)
			if r.NeedsAdmin {
				cmd = "sudo " + cmd
			}
			fmt.Fprintf(&b, "  Command for the user to run: $ %s\n", cmd)
		}
	}
	fmt.Fprintf(&b, "\nMeasured total: at least %s", output.Bytes(d.MeasuredBytes))
	if d.Unmeasured > 0 {
		fmt.Fprintf(&b, " (%d not measured)", d.Unmeasured)
	}
	b.WriteString("\nSapu never runs these commands.")
	return toolResult{b.String(), d}, nil
}

type analyzeNode struct {
	Name     string        `json:"name"`
	Path     string        `json:"path"`
	Bytes    int64         `json:"bytes"`
	Files    int64         `json:"files"`
	IsDir    bool          `json:"is_dir"`
	Children []analyzeNode `json:"children,omitempty"`
}

type analyzeData struct {
	Top   int         `json:"top"`   // children kept per folder
	Depth int         `json:"depth"` // levels shown below the root
	Root  analyzeNode `json:"root"`  // Bytes and Files are the totals of the whole tree
}

func runAnalyze(ctx context.Context, e env.Env, args json.RawMessage) (toolResult, error) {
	a := struct {
		Path  string `json:"path"`
		Top   *int   `json:"top"`
		Depth *int   `json:"depth"`
	}{}
	if err := decodeArgs(args, &a); err != nil {
		return toolResult{}, err
	}
	top, depth := 20, 2
	if a.Top != nil {
		top = *a.Top
	}
	if a.Depth != nil {
		depth = *a.Depth
	}
	if top < 1 || top > 100 {
		return toolResult{}, errors.New("top must be between 1 and 100")
	}
	if depth < 1 || depth > 4 {
		return toolResult{}, errors.New("depth must be between 1 and 4")
	}

	root := e.Home
	if a.Path != "" {
		root = e.Expand(a.Path)
	}
	if !filepath.IsAbs(root) {
		return toolResult{}, fmt.Errorf("path must be absolute or start with ~, got %q", a.Path)
	}
	root = filepath.Clean(root)
	if _, err := os.Lstat(root); err != nil {
		return toolResult{}, fmt.Errorf("cannot read %s: %w", root, err)
	}

	tree, err := analyze.Top(ctx, root, top, depth)
	if err != nil {
		return toolResult{}, err
	}
	d := analyzeData{Top: top, Depth: depth, Root: toNode(tree)}

	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s in %d files\n", output.Tilde(e.Home, tree.Path), output.Bytes(tree.Bytes), tree.Files)
	if len(tree.Children) == 0 {
		b.WriteString("Nothing inside.")
	}
	writeTree(&b, tree, 1)
	return toolResult{strings.TrimRight(b.String(), "\n"), d}, nil
}

func toNode(n *walk.Node) analyzeNode {
	out := analyzeNode{Name: n.Name, Path: n.Path, Bytes: n.Bytes, Files: n.Files, IsDir: n.IsDir}
	for _, c := range n.Children {
		out.Children = append(out.Children, toNode(c))
	}
	return out
}

func writeTree(b *strings.Builder, n *walk.Node, level int) {
	for _, c := range n.Children {
		name := c.Name
		if c.IsDir {
			name += "/"
		}
		fmt.Fprintf(b, "%s%9s  %s\n", strings.Repeat("  ", level-1), output.Bytes(c.Bytes), name)
		writeTree(b, c, level+1)
	}
}

type historyData struct {
	Runs []runlog.Summary `json:"runs"`
}

func runHistory(ctx context.Context, e env.Env, args json.RawMessage) (toolResult, error) {
	if err := decodeArgs(args, &struct{}{}); err != nil {
		return toolResult{}, err
	}
	runs, err := runlog.List(e)
	if err != nil {
		return toolResult{}, err
	}
	if runs == nil {
		runs = []runlog.Summary{}
	}
	if len(runs) == 0 {
		return toolResult{"No runs yet.", historyData{runs}}, nil
	}
	var b strings.Builder
	for _, r := range runs {
		fmt.Fprintf(&b, "%s  %s  %s  %d items  %s  %s\n", r.ID, r.Started.Local().Format("2006-01-02 15:04"),
			r.Kind, r.Items, output.Bytes(r.Bytes), r.Status)
	}
	return toolResult{strings.TrimRight(b.String(), "\n"), historyData{runs}}, nil
}

// held is one run that still has items in quarantine.
type held struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	Started      time.Time `json:"started"`
	Bytes        int64     `json:"bytes"`
	ExpiresAfter time.Time `json:"expires_after"` // earliest time a later run may expire it
}

type quarantineData struct {
	Runs       []held `json:"runs"`
	TotalBytes int64  `json:"total_bytes"`
}

func runQuarantine(ctx context.Context, e env.Env, args json.RawMessage) (toolResult, error) {
	if err := decodeArgs(args, &struct{}{}); err != nil {
		return toolResult{}, err
	}
	sizes, err := fsop.QuarantineSize(e)
	if err != nil {
		return toolResult{}, err
	}
	runs, err := runlog.List(e)
	if err != nil {
		return toolResult{}, err
	}
	cfg, err := config.Load(e) // sapu.toml may set a longer or shorter retention
	if err != nil {
		return toolResult{}, err
	}
	d := quarantineData{Runs: []held{}}
	for _, r := range runs {
		if bytes, ok := sizes[r.ID]; ok {
			d.Runs = append(d.Runs, held{r.ID, r.Kind, r.Started, bytes, r.Started.Add(cfg.MaxAge)})
			d.TotalBytes += bytes
		}
	}
	if len(d.Runs) == 0 {
		return toolResult{"The quarantine is empty.", d}, nil
	}
	var b strings.Builder
	for _, r := range d.Runs {
		fmt.Fprintf(&b, "%s  %s  %s  started %s  expires after %s\n", r.ID, r.Kind, output.Bytes(r.Bytes),
			r.Started.Local().Format("2006-01-02 15:04"), r.ExpiresAfter.Local().Format("Mon 2 Jan"))
	}
	fmt.Fprintf(&b, "In quarantine: %s. The user can put a run back with `sapu undo RUN_ID`.", output.Bytes(d.TotalBytes))
	return toolResult{b.String(), d}, nil
}
