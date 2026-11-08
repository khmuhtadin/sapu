package cli

import (
	"cmp"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/optimize"
	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/protect"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// Plan and result rendering, shared by clean, purge and apply.

const (
	tableWidth  = 78 // sizes line up at this column
	maxPaths    = 5  // paths listed per group unless --verbose
	maxExamples = 3  // example paths per "not touched" reason unless --verbose
)

// style adds bold and dim on a terminal. It is off when stdout is not a
// terminal or NO_COLOR is set.
type style struct{ color bool }

func newStyle(w io.Writer) style {
	f, ok := w.(*os.File)
	return style{color: ok && os.Getenv("NO_COLOR") == "" && output.IsTerminal(f)}
}

func (s style) bold(t string) string { return s.wrap("1", t) }
func (s style) dim(t string) string  { return s.wrap("2", t) }

func (s style) wrap(code, t string) string {
	if !s.color {
		return t
	}
	return "\x1b[" + code + "m" + t + "\x1b[0m"
}

// tilde shows paths inside home as ~/...
func tilde(home, p string) string {
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

// tildeText shows home as ~ inside a sentence, such as an explanation that names a project folder.
func tildeText(home, text string) string {
	return strings.ReplaceAll(text, home+"/", "~/")
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// row puts left at indent and right at the right edge.
func row(indent int, left, right string) string {
	pad := max(tableWidth-indent-utf8.RuneCountInString(left)-utf8.RuneCountInString(right), 2)
	return strings.Repeat(" ", indent) + left + strings.Repeat(" ", pad) + right
}

func wrapText(text string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) > width:
			lines = append(lines, line)
			line = word
		default:
			line += " " + word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

func writeWrapped(w io.Writer, st style, indent int, text string) {
	for _, line := range wrapText(text, tableWidth-indent) {
		fmt.Fprintln(w, strings.Repeat(" ", indent)+st.dim(line))
	}
}

var plainArg = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellJoin renders a command the way a person would type it.
func shellJoin(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		if plainArg.MatchString(a) {
			parts[i] = a
		} else {
			parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(parts, " ")
}

// protectWhy returns a function that turns the detail of a protected skip
// ("protected:docker") into the reason in the user's language. It gives ""
// for anything else.
func protectWhy(e env.Env) (func(detail string) string, error) {
	c, err := protect.Load(e)
	if err != nil {
		return nil, err
	}
	return func(detail string) string {
		id, ok := strings.CutPrefix(detail, "protected:")
		if !ok {
			return ""
		}
		return c.Reason(id)
	}, nil
}

func riskRank(r rules.Risk) int {
	switch r {
	case rules.RiskSafe:
		return 0
	case rules.RiskModerate:
		return 1
	case rules.RiskRisky:
		return 2
	}
	return 3
}

// group is the items of one source (rule) with one risk and selection.
type group struct {
	risk     rules.Risk
	source   string
	selected bool
	items    []plan.Item
	bytes    int64
}

func groupItems(items []plan.Item) []group {
	type key struct {
		risk     rules.Risk
		source   string
		selected bool
	}
	byKey := map[key]*group{}
	var groups []*group
	for _, it := range items {
		k := key{it.Risk, it.Source, it.Selected}
		g := byKey[k]
		if g == nil {
			g = &group{risk: it.Risk, source: it.Source, selected: it.Selected}
			byKey[k] = g
			groups = append(groups, g)
		}
		g.items = append(g.items, it)
		g.bytes += it.Bytes
	}
	out := make([]group, len(groups))
	for i, g := range groups {
		slices.SortFunc(g.items, func(a, b plan.Item) int {
			return cmp.Or(cmp.Compare(b.Bytes, a.Bytes), cmp.Compare(a.Path, b.Path))
		})
		out[i] = *g
	}
	slices.SortFunc(out, func(a, b group) int {
		selA, selB := 1, 1
		if a.selected {
			selA = 0
		}
		if b.selected {
			selB = 0
		}
		return cmp.Or(cmp.Compare(riskRank(a.risk), riskRank(b.risk)), cmp.Compare(selA, selB), cmp.Compare(a.source, b.source))
	})
	return out
}

func riskHeading(r rules.Risk, anySelected bool) string {
	switch r {
	case rules.RiskSafe:
		return "Safe — rebuilt automatically"
	case rules.RiskModerate:
		return "Moderate — rebuilt, but may take time or a download"
	case rules.RiskRisky:
		if anySelected {
			return "Risky — selected with --risk risky; these may lose something"
		}
		return "Risky — not selected; use --risk risky"
	}
	return fmt.Sprintf("Unknown risk %q", string(r))
}

// notSelectedHeading is the heading of the items that do not regenerate and
// are not selected, with a hint that fits the command: --risk risky only for
// a command that has that flag and an item that flag would select.
func notSelectedHeading(kind string, groups []group) string {
	hasRisky := false
	for _, g := range groups {
		hasRisky = hasRisky || g.risk == rules.RiskRisky
	}
	switch {
	case hasRisky && (kind == plan.KindUninstall || kind == plan.KindReset || kind == plan.KindPackages):
		return "Not selected — use --risk risky to select them"
	case kind == plan.KindClean:
		return "Not selected — left out by --risk, --only or --exclude"
	}
	return "Not selected — Sapu leaves them where they are"
}

// writePlanTable prints the plan: items that regenerate grouped by risk and
// rule, everything else under "To move" and "Not selected", the total of the
// selected bytes, what is not touched, and the official commands.
// runCommands says whether the selected commands will run; then the
// commands that are not selected are listed too. why says in words why a
// path is protected (see protectWhy); it may be nil.
func writePlanTable(w io.Writer, p *plan.Plan, why func(detail string) string, verbose, runCommands bool) {
	st := newStyle(w)
	var moves, commands, unselected []plan.Item
	for _, it := range p.Items {
		switch {
		case it.Action != rules.ActionOfficialCommand:
			moves = append(moves, it)
		case it.Selected:
			commands = append(commands, it)
		case runCommands:
			unselected = append(unselected, it)
		}
	}

	if len(moves) == 0 && (!runCommands || len(commands)+len(unselected) == 0) {
		fmt.Fprintln(w, "Nothing found that Sapu can move.")
	}
	// Risk headings say "rebuilt", so they are only for items that regenerate.
	var caches, others []plan.Item
	for _, it := range moves {
		if it.Regenerates {
			caches = append(caches, it)
		} else {
			others = append(others, it)
		}
	}
	started := false
	section := func(heading string) {
		if started {
			fmt.Fprintln(w)
		}
		started = true
		fmt.Fprintln(w, st.bold(heading))
	}

	groups := groupItems(caches)
	for i := 0; i < len(groups); {
		j := i
		anySelected := false
		for j < len(groups) && groups[j].risk == groups[i].risk {
			anySelected = anySelected || groups[j].selected
			j++
		}
		section(riskHeading(groups[i].risk, anySelected))
		for _, g := range groups[i:j] {
			markUnselected := !g.selected && (g.risk != rules.RiskRisky || anySelected)
			writeGroup(w, st, p.Home, g, markUnselected, verbose)
		}
		i = j
	}

	var toMove, notSelected []group
	for _, g := range groupItems(others) {
		if g.selected {
			toMove = append(toMove, g)
		} else {
			notSelected = append(notSelected, g)
		}
	}
	if len(toMove) > 0 {
		section("To move")
		for _, g := range toMove {
			writeGroup(w, st, p.Home, g, false, verbose)
		}
	}
	if len(notSelected) > 0 {
		section(notSelectedHeading(p.Kind, notSelected))
		for _, g := range notSelected {
			writeGroup(w, st, p.Home, g, false, verbose)
		}
	}

	var count int
	var total int64
	for _, it := range moves {
		if it.Selected {
			count++
			total += it.Bytes
		}
	}
	if len(moves) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, st.bold(row(0, "Total selected · "+plural(count, "item"), output.Bytes(total))))
	}

	writeSkips(w, st, p.Home, "Not touched", p.Skipped, why, verbose)

	switch {
	case len(commands) > 0 && runCommands:
		text := "Each one is the owning tool's own cleanup. These commands are not undoable."
		if count > 0 {
			text = "Each one is the owning tool's own cleanup, run after the moves above. " +
				"These commands are not undoable. Their sizes are not in the total."
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, st.bold("Commands Sapu will run"))
		writeWrapped(w, st, 2, text)
		writeCommands(w, st, p.Home, commands)
	case len(commands) > 0:
		fmt.Fprintln(w)
		fmt.Fprintln(w, st.bold("Commands you can run"))
		writeWrapped(w, st, 2, "Sapu does not run these. Their sizes are not in the total, and they may cover items listed above.")
		writeCommands(w, st, p.Home, commands)
	}
	if len(unselected) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, st.bold("Commands not selected"))
		writeWrapped(w, st, 2, "Sapu does not run these.")
		writeCommands(w, st, p.Home, unselected)
	}
}

// writeCommands lists commands exactly as they would run, with the size of
// what they clean when Sapu could measure it.
func writeCommands(w io.Writer, st style, home string, commands []plan.Item) {
	for _, it := range commands {
		size := ""
		if it.Bytes > 0 {
			size = output.Bytes(it.Bytes)
		}
		fmt.Fprintln(w, strings.TrimRight(row(2, shellJoin(it.Command), size), " "))
		writeWrapped(w, st, 4, tildeText(home, it.Explain))
	}
}

// writeNotes lists what a command left out on purpose, and why.
func writeNotes(w io.Writer, notes []string) {
	if len(notes) == 0 {
		return
	}
	st := newStyle(w)
	fmt.Fprintln(w)
	fmt.Fprintln(w, st.bold("Left out"))
	for _, n := range notes {
		lines := wrapText(n, tableWidth-4)
		for i, line := range lines {
			if i == 0 {
				fmt.Fprintln(w, "  "+line)
			} else {
				fmt.Fprintln(w, "    "+line)
			}
		}
	}
}

// writeAdmin lists actions that need admin rights. Sapu never runs sudo.
func writeAdmin(w io.Writer, admin []optimize.AdminAction) {
	if len(admin) == 0 {
		return
	}
	st := newStyle(w)
	fmt.Fprintln(w)
	fmt.Fprintln(w, st.bold("Needs admin — Sapu never runs sudo"))
	writeWrapped(w, st, 2, "Type these in Terminal yourself, and only if you have the problem they fix.")
	for _, a := range admin {
		fmt.Fprintln(w, "  "+a.Title)
		fmt.Fprintln(w, "    "+a.Command)
		writeWrapped(w, st, 4, a.Explain)
	}
}

// writeCommandWarning is said before the question when commands will run.
func writeCommandWarning(w io.Writer, cmds []plan.Item, afterMoves bool, why string) {
	if afterMoves {
		fmt.Fprintln(w, "Sapu will run these commands after moving the items:")
	} else {
		fmt.Fprintln(w, "Sapu will run these commands:")
	}
	for _, it := range cmds {
		fmt.Fprintln(w, "  "+shellJoin(it.Command))
	}
	fmt.Fprintln(w, "These commands are not undoable.")
	if why != "" {
		fmt.Fprintln(w, why)
	}
}

// writeGroup prints one group. markUnselected adds "not selected" to its
// title, for a group whose heading does not say so already.
func writeGroup(w io.Writer, st style, home string, g group, markUnselected, verbose bool) {
	title := g.source + " · " + plural(len(g.items), "item")
	if markUnselected {
		title += " · not selected"
	}
	fmt.Fprintln(w, row(2, title, output.Bytes(g.bytes)))

	// One explanation per rule. Purge items each name their own project, so
	// there the explanation goes under each path.
	same := true
	for _, it := range g.items {
		same = same && it.Explain == g.items[0].Explain
	}
	if same {
		writeWrapped(w, st, 4, tildeText(home, g.items[0].Explain))
	}
	shown := g.items
	if !verbose && len(shown) > maxPaths {
		shown = shown[:maxPaths]
	}
	for _, it := range shown {
		fmt.Fprintln(w, row(4, tilde(home, it.Path), output.Bytes(it.Bytes)))
		if !same {
			writeWrapped(w, st, 6, tildeText(home, it.Explain))
		}
	}
	if len(shown) < len(g.items) {
		fmt.Fprintf(w, "    … and %d more\n", len(g.items)-len(shown))
	}
}

// skipReasons are the reasons in the order they are shown.
type skipReason struct{ reason, name, why string }

var skipReasons = []skipReason{
	{plan.ReasonProtected, "protected", "on Sapu's protection list"},
	{plan.ReasonWhitelisted, "whitelisted", "on your whitelist"},
	{plan.ReasonInUse, "in use", "open in a program, or used very recently"},
	{plan.ReasonDuplicate, "duplicate", "already covered by another item"},
	{plan.ReasonChanged, "changed", "changed since the plan was made"},
	{plan.ReasonUnreadable, "unreadable", "couldn't read it (Full Disk Access may be needed; see sapu doctor)"},
	{plan.ReasonGone, "gone", "no longer there"},
}

// writeSkips prints counts per reason with a few example paths.
// why says in words why a path is protected; it may be nil.
func writeSkips(w io.Writer, st style, home, title string, skips []plan.Skip, why func(detail string) string, verbose bool) {
	if len(skips) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, st.bold(title))
	handled := map[string]bool{}
	rest := slices.Clone(skipReasons)
	for _, sr := range skipReasons {
		handled[sr.reason] = true
	}
	for _, s := range skips {
		if !handled[s.Reason] {
			handled[s.Reason] = true
			rest = append(rest, skipReason{s.Reason, s.Reason, ""})
		}
	}
	for _, sr := range rest {
		var these []plan.Skip
		for _, s := range skips {
			if s.Reason == sr.reason {
				these = append(these, s)
			}
		}
		if len(these) == 0 {
			continue
		}
		line := fmt.Sprintf("  %d %s", len(these), sr.name)
		if sr.why != "" {
			line += " " + st.dim("— "+sr.why)
		}
		fmt.Fprintln(w, line)
		shown := these
		if !verbose && len(shown) > maxExamples {
			shown = shown[:maxExamples]
		}
		for _, s := range shown {
			line := "    " + tilde(home, s.Path)
			detail := s.Detail
			if s.Reason == plan.ReasonProtected && why != nil && why(s.Detail) != "" {
				detail = why(s.Detail)
			}
			if detail != "" && s.Reason != plan.ReasonDuplicate {
				line += " " + st.dim("("+detail+")")
			}
			fmt.Fprintln(w, line)
		}
		if len(shown) < len(these) {
			fmt.Fprintf(w, "    … and %d more\n", len(these)-len(shown))
		}
	}
}

// writeResult prints what an apply did. cfg gives the quarantine settings
// the texts name; deleted is true when --permanent emptied the quarantine;
// start is when the run began.
func writeResult(w io.Writer, home string, res *engine.Result, cfg config.Config, start time.Time, deleted, verbose bool) {
	st := newStyle(w)
	onlyCommands := len(res.Moved) == 0 && len(res.Commands) > 0
	switch {
	case onlyCommands:
		// the commands below say what happened
	case len(res.Moved) == 0:
		fmt.Fprintln(w, "Nothing was moved.")
	case deleted:
		fmt.Fprintf(w, "Deleted %s (%s) for good. There is no undo.\n", plural(len(res.Moved), "item"), output.Bytes(res.Bytes))
	default:
		fmt.Fprintf(w, "Moved %s (%s) to quarantine, run %s.\n", plural(len(res.Moved), "item"), output.Bytes(res.Bytes), res.RunID)
		fmt.Fprintf(w, "The space is not free yet: these items stay in quarantine until at least %s.\n", expiresAfter(start, cfg).Format("Mon 2 Jan"))
		fmt.Fprintf(w, "After that, a later sapu clean deletes them (sooner if the quarantine passes %s).\n", output.Bytes(cfg.MaxBytes))
		fmt.Fprintf(w, "To free the space now: `sapu quarantine empty %s`\n", res.RunID)
		fmt.Fprintf(w, "Undo: `sapu undo %s`\n", res.RunID)
	}
	if !deleted && res.Freed > 0 {
		fmt.Fprintf(w, "Also deleted %s from older runs whose quarantine had expired.\n", output.Bytes(res.Freed))
	}
	space, err := newRunSpace(home, res, deleted)
	switch {
	case err != nil:
		fmt.Fprintf(w, "Free space could not be read: %v\n", err)
	case space != nil:
		fmt.Fprintln(w, space.text(res.RunID))
	}

	writeCommandResults(w, st, res.Commands, !onlyCommands)
	writeSkips(w, st, home, "Skipped while applying", res.Skipped, nil, verbose)

	// A command that ran and failed is shown above with its output.
	ran := map[string]bool{}
	for _, c := range res.Commands {
		ran[c.Item.ID] = true
	}
	var failed []engine.Failure
	for _, f := range res.Failed {
		if f.Item.Action != rules.ActionOfficialCommand || !ran[f.Item.ID] {
			failed = append(failed, f)
		}
	}
	if len(failed) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, st.bold(fmt.Sprintf("Failed (%d)", len(failed))))
		for _, f := range failed {
			what := tilde(home, f.Item.Path)
			if f.Item.Action == rules.ActionOfficialCommand {
				what = shellJoin(f.Item.Command)
			}
			fmt.Fprintf(w, "  %s %s\n", what, st.dim("("+failureText(res.RunID, f.Error)+")"))
		}
	}
	writeClones(w, home, res, verbose)
}

// runSpace is the free space after a run (blueprint U4). Moving items into
// quarantine is a rename on the same volume, so free space is the same as
// before the run; only emptying the quarantine frees it. Sizes are "about":
// APFS clones and hard links can make the real change smaller.
type runSpace struct {
	FreeBytes           int64 `json:"free_bytes"`                       // free space now
	FreeAfterEmptyBytes int64 `json:"free_after_empty_bytes,omitempty"` // quarantine run: about this much after sapu quarantine empty
	FreedBytes          int64 `json:"freed_bytes,omitempty"`            // --permanent: about this much was freed by the run
}

// newRunSpace reads the free space of the home volume after a run. It returns
// nil when the run moved nothing.
func newRunSpace(home string, res *engine.Result, deleted bool) (*runSpace, error) {
	if len(res.Moved) == 0 {
		return nil, nil
	}
	_, free, err := diskUsage(home)
	if err != nil {
		return nil, err
	}
	s := &runSpace{FreeBytes: free}
	if deleted {
		s.FreedBytes = res.Freed
	} else {
		s.FreeAfterEmptyBytes = free + res.Bytes
	}
	return s, nil
}

func (s *runSpace) text(runID string) string {
	if s.FreeAfterEmptyBytes > 0 {
		return fmt.Sprintf("Free space now %s; after `sapu quarantine empty %s`: about %s.",
			output.Bytes(s.FreeBytes), runID, output.Bytes(s.FreeAfterEmptyBytes))
	}
	return fmt.Sprintf("Free space now %s; this run freed about %s.", output.Bytes(s.FreeBytes), output.Bytes(s.FreedBytes))
}

// maxOutputLines is how many lines of a failed command's output are shown.
const maxOutputLines = 6

// writeCommandResults prints each command that ran with its exit status,
// and the end of its output when it failed. gap puts a blank line first.
func writeCommandResults(w io.Writer, st style, cmds []engine.CommandResult, gap bool) {
	if len(cmds) == 0 {
		return
	}
	if gap {
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, st.bold(fmt.Sprintf("Ran %s (not undoable)", plural(len(cmds), "command"))))
	for _, c := range cmds {
		status := fmt.Sprintf("done (exit code %d)", c.ExitCode)
		switch {
		case c.Error != "":
			status = "failed: " + c.Error
		case c.ExitCode != 0:
			status = fmt.Sprintf("failed (exit code %d)", c.ExitCode)
		}
		fmt.Fprintln(w, row(2, shellJoin(c.Item.Command), c.Duration.Round(time.Second).String()))
		fmt.Fprintln(w, "    "+status)
		if !c.Failed() {
			continue
		}
		lines := strings.Split(strings.TrimRight(c.Output, "\n"), "\n")
		if len(lines) > maxOutputLines {
			lines = lines[len(lines)-maxOutputLines:]
		}
		for _, line := range lines {
			if line = strings.TrimRight(line, " \r"); line != "" {
				fmt.Fprintln(w, "      "+st.dim(line))
			}
		}
	}
}

// expiresAfter is the earliest time expiry may delete the quarantine of a
// run started at start. Nothing is scheduled: a later sapu run expires it.
func expiresAfter(start time.Time, cfg config.Config) time.Time { return start.Add(cfg.MaxAge) }

// ageText says how long ago d was, roughly.
func ageText(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

// writeTable prints a table with two spaces between columns. right says
// which columns are right-aligned.
func writeTable(w io.Writer, header []string, rows [][]string, right ...int) {
	st := newStyle(w)
	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, r := range rows {
		for i, c := range r {
			widths[i] = max(widths[i], utf8.RuneCountInString(c))
		}
	}
	line := func(cells []string) string {
		parts := make([]string, len(cells))
		for i, c := range cells {
			pad := strings.Repeat(" ", widths[i]-utf8.RuneCountInString(c))
			if slices.Contains(right, i) {
				parts[i] = pad + c
			} else {
				parts[i] = c + pad
			}
		}
		return strings.TrimRight(strings.Join(parts, "  "), " ")
	}
	fmt.Fprintln(w, st.bold(line(header)))
	for _, r := range rows {
		fmt.Fprintln(w, line(r))
	}
}
