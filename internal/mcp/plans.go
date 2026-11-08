package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/purge"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/rulesupdate"
)

const maxExamples = 3 // example paths listed per rule

// planGroup is the selected items of one rule.
type planGroup struct {
	Rule     string     `json:"rule"`
	Risk     rules.Risk `json:"risk"`
	Items    int        `json:"items"`
	Bytes    int64      `json:"bytes"`
	Explain  string     `json:"explain"`
	Examples []string   `json:"examples"` // the first few paths
}

// notTouched says what the plan leaves out.
type notTouched struct {
	NotSelectedItems int            `json:"not_selected_items"` // above the risk limit
	NotSelectedBytes int64          `json:"not_selected_bytes"`
	Commands         int            `json:"commands"` // official cleanup commands: never part of an MCP plan
	Skipped          map[string]int `json:"skipped"`  // by reason: protected, whitelisted, in_use, ...
}

type planData struct {
	PlanFile      string      `json:"plan_file"` // empty when nothing was selected
	Kind          string      `json:"kind"`
	MaxRisk       rules.Risk  `json:"max_risk"`
	SelectedItems int         `json:"selected_items"`
	SelectedBytes int64       `json:"selected_bytes"`
	Groups        []planGroup `json:"groups"`
	NotTouched    notTouched  `json:"not_touched"`
	ApplyCommand  string      `json:"apply_command"` // for the human to run; empty when nothing was selected
}

type planArgs struct {
	MaxRisk string `json:"max_risk"`
}

// parseMaxRisk accepts "" (safe), "safe" and "moderate". Never "risky".
func parseMaxRisk(s string) (rules.Risk, error) {
	switch s {
	case "", "safe":
		return rules.RiskSafe, nil
	case "moderate":
		return rules.RiskModerate, nil
	}
	return "", fmt.Errorf("max_risk must be safe or moderate, not %q: Sapu never plans risky items through MCP", s)
}

func runPlanClean(ctx context.Context, e env.Env, args json.RawMessage) (toolResult, error) {
	var a planArgs
	if err := decodeArgs(args, &a); err != nil {
		return toolResult{}, err
	}
	maxRisk, err := parseMaxRisk(a.MaxRisk)
	if err != nil {
		return toolResult{}, err
	}
	p, err := cleanPlan(ctx, e)
	if err != nil {
		return toolResult{}, err
	}
	return finishPlan(e, p, maxRisk)
}

func runPlanPurge(ctx context.Context, e env.Env, args json.RawMessage) (toolResult, error) {
	a := struct {
		planArgs
		Paths     []string `json:"paths"`
		OlderThan string   `json:"older_than"`
	}{}
	if err := decodeArgs(args, &a); err != nil {
		return toolResult{}, err
	}
	maxRisk, err := parseMaxRisk(a.MaxRisk)
	if err != nil {
		return toolResult{}, err
	}
	if len(a.Paths) == 0 {
		return toolResult{}, errors.New("paths must list at least one project folder")
	}
	roots := make([]string, len(a.Paths))
	for i, path := range a.Paths {
		roots[i] = e.Expand(path)
		if !filepath.IsAbs(roots[i]) {
			return toolResult{}, fmt.Errorf("path must be absolute or start with ~, got %q", path)
		}
	}
	r, err := rulesupdate.Active(e)
	if err != nil {
		return toolResult{}, err
	}
	opts := purge.Options{Now: time.Now(), Types: r.Purge}
	if a.OlderThan != "" {
		if opts.OlderThan, err = rules.ParseAge(a.OlderThan); err != nil {
			return toolResult{}, fmt.Errorf("older_than: %w", err)
		}
	}

	items, err := purge.Find(ctx, e, roots, opts)
	if err != nil {
		return toolResult{}, err
	}
	p, err := engine.Plan(ctx, e, plan.KindPurge, items)
	if err != nil {
		return toolResult{}, err
	}
	return finishPlan(e, p, maxRisk)
}

// finishPlan selects the items, writes the plan file and describes it.
func finishPlan(e env.Env, p *plan.Plan, maxRisk rules.Risk) (toolResult, error) {
	// Only items Sapu moves to quarantine are selected, up to maxRisk.
	// Official cleanup commands stay out: the human can add them with
	// `sapu clean` if they want them.
	for i := range p.Items {
		it := &p.Items[i]
		it.Selected = it.Action == rules.ActionQuarantine &&
			(it.Risk == rules.RiskSafe || (maxRisk == rules.RiskModerate && it.Risk == rules.RiskModerate))
	}

	d := planData{
		Kind:    p.Kind,
		MaxRisk: maxRisk,
		Groups:  []planGroup{},
		NotTouched: notTouched{
			Skipped: map[string]int{},
		},
	}
	for _, s := range p.Skipped {
		d.NotTouched.Skipped[s.Reason]++
	}
	for _, it := range p.Items {
		switch {
		case it.Selected:
			d.SelectedItems++
			d.SelectedBytes += it.Bytes
			i := slices.IndexFunc(d.Groups, func(g planGroup) bool { return g.Rule == it.Source })
			if i < 0 {
				d.Groups = append(d.Groups, planGroup{Rule: it.Source, Risk: it.Risk, Explain: it.Explain, Examples: []string{}})
				i = len(d.Groups) - 1
			}
			g := &d.Groups[i]
			g.Items++
			g.Bytes += it.Bytes
			if len(g.Examples) < maxExamples {
				g.Examples = append(g.Examples, it.Path)
			}
		case it.Action == rules.ActionOfficialCommand:
			d.NotTouched.Commands++
		default:
			d.NotTouched.NotSelectedItems++
			d.NotTouched.NotSelectedBytes += it.Bytes
		}
	}
	if d.SelectedItems > 0 { // a plan with nothing selected is not worth a file
		file, err := writePlanFile(e, p)
		if err != nil {
			return toolResult{}, err
		}
		d.PlanFile = file
		d.ApplyCommand = "sapu apply " + shellQuote(file)
	}
	slices.SortStableFunc(d.Groups, func(a, b planGroup) int {
		switch {
		case a.Bytes > b.Bytes:
			return -1
		case a.Bytes < b.Bytes:
			return 1
		}
		return strings.Compare(a.Rule, b.Rule)
	})
	return toolResult{planText(e, d), d}, nil
}

func planText(e env.Env, d planData) string {
	var b strings.Builder
	if d.SelectedItems == 0 {
		fmt.Fprintf(&b, "Nothing to plan: no items up to risk %s were found, so no plan file was written.\n", d.MaxRisk)
	} else {
		fmt.Fprintf(&b, "Plan written: %s, %s, up to risk %s. Nothing has been moved.\n",
			items(d.SelectedItems), output.Bytes(d.SelectedBytes), d.MaxRisk)
		b.WriteString("\nSelected, by rule:\n")
		for _, g := range d.Groups {
			fmt.Fprintf(&b, "- %s (%s): %s, %s. %s\n", g.Rule, g.Risk, items(g.Items), output.Bytes(g.Bytes), g.Explain)
			for _, path := range g.Examples {
				fmt.Fprintf(&b, "    e.g. %s\n", output.Tilde(e.Home, path))
			}
		}
	}

	n := d.NotTouched
	b.WriteString("\nNot touched:\n")
	fmt.Fprintf(&b, "- Above the risk limit: %s, %s\n", items(n.NotSelectedItems), output.Bytes(n.NotSelectedBytes))
	fmt.Fprintf(&b, "- Official cleanup commands (never in an MCP plan): %d\n", n.Commands)
	reasons := make([]string, 0, len(n.Skipped))
	for reason := range n.Skipped {
		reasons = append(reasons, reason)
	}
	slices.Sort(reasons)
	for _, reason := range reasons {
		fmt.Fprintf(&b, "- Skipped as %s: %d\n", reason, n.Skipped[reason])
	}

	if d.PlanFile == "" {
		return strings.TrimRight(b.String(), "\n")
	}
	fmt.Fprintf(&b, "\nPlan file: %s\n", d.PlanFile)
	fmt.Fprintf(&b, "To apply it, the user runs this themselves (Sapu shows the plan and asks first):\n  %s\n", d.ApplyCommand)
	b.WriteString("Do not run it for them.")
	return b.String()
}

func items(n int) string {
	if n == 1 {
		return "1 item"
	}
	return fmt.Sprintf("%d items", n)
}

// shellQuote quotes s for a shell when it holds anything but plain path characters.
func shellQuote(s string) string {
	if strings.IndexFunc(s, func(r rune) bool {
		isPlain := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+=:@", r)
		return !isPlain
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writePlanFile writes p to <state folder>/plans/<timestamp>.json and returns
// the path. The folder is 0700 and the file 0600. This is the only file the
// server writes. It never replaces a file and never writes through a symlink.
func writePlanFile(e env.Env, p *plan.Plan) (string, error) {
	dir := filepath.Join(e.StateDir, "plans")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if fi, err := os.Lstat(dir); err != nil {
		return "", err
	} else if !fi.IsDir() {
		return "", fmt.Errorf("%s is not a folder", dir)
	}

	var buf bytes.Buffer
	if err := plan.Write(&buf, p); err != nil {
		return "", err
	}
	file := filepath.Join(dir, p.CreatedAt.Format("20060102-150405.000")+".json")
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", fmt.Errorf("write plan: %w (try again)", err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		return "", errors.Join(fmt.Errorf("write plan %s: %w", file, err), f.Close())
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return file, nil
}
