package review

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// Styles. lipgloss drops all of them when NO_COLOR is set or the output is
// not a terminal, so every state is also plain text: "›" for the cursor,
// [x] [-] [ ] for the selection, and a word next to every risk colour.
// Colour means risk, never size.
var (
	bold      = lipgloss.NewStyle().Bold(true)
	dim       = lipgloss.NewStyle().Faint(true)
	riskStyle = map[rules.Risk]lipgloss.Style{
		rules.RiskSafe:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "28", Dark: "42"}),
		rules.RiskModerate: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "130", Dark: "214"}),
		rules.RiskRisky:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "160", Dark: "203"}),
	}
)

const (
	sizeW        = 8 // "999.9 GB"
	rightPad     = 6 // after the size column: "   [x]" on a group heading
	footLines    = 5 // status, rule, total and two lines of keys
	detailIndent = 8
)

func riskWord(r rules.Risk) string { return strings.ToUpper(string(r)) }

// riskLabel is what follows the risk word on a group heading.
func riskLabel(r rules.Risk) string {
	switch r {
	case rules.RiskSafe:
		return "Safe to recreate"
	case rules.RiskModerate:
		return "Needs review"
	case rules.RiskRisky:
		return "May contain personal data"
	}
	return ""
}

func (m *model) View() string {
	var lines []string
	switch m.screen {
	case screenScanning:
		lines = m.scanView()
	case screenList:
		if m.help {
			lines = m.helpView()
		} else {
			lines = m.listView()
		}
	case screenConfirm:
		lines = m.confirmView()
	case screenApplying:
		lines = m.applyingView()
	case screenReceipt:
		lines = m.receiptView()
	}
	for i := range lines {
		lines[i] = fit(lines[i], m.width)
	}
	return strings.Join(lines, "\n")
}

func (m *model) rule() string { return dim.Render(strings.Repeat("-", max(m.width-4, 0))) }

// title is "sapu / review" with a note at the right edge.
func (m *model) title(screen, right string) string {
	left := bold.Render("sapu / " + screen)
	if right == "" {
		return left
	}
	right = truncateEnd(right, m.width-width(left)-3)
	return left + spaces(m.width-width(left)-width(right)-2) + dim.Render(right)
}

// ---- scanning ----

// cue is the small progress cue next to the scan status line. It moves only
// when motion is on; otherwise it is the same three dots, standing still.
func (m *model) cue() string {
	if m.motion {
		return m.spin.View()
	}
	return "..."
}

func (m *model) scanView() []string {
	ev := m.scanned
	status := "  Scanning caches" + m.cue()
	counts := commas(ev.Files) + " files checked / " + output.Bytes(ev.Bytes) + " measured"
	lines := []string{m.title("review", ""), m.rule(), ""}
	if width(status+"  "+counts) <= m.width {
		lines = append(lines, status+"  "+counts)
	} else {
		lines = append(lines, status, "  "+counts)
	}
	return append(lines,
		dim.Render("  "+truncateMiddle(m.short(ev.Path), m.width-3)),
		"",
		dim.Render("  Nothing has moved. q stop"))
}

// commas formats a count: 1234567 becomes "1,234,567".
func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ---- list ----

// intro is the two lines under the header of the list. A short terminal
// gets none, so the rows keep their room.
func (m *model) intro() []string {
	if m.height < 22 {
		return nil
	}
	return []string{
		"  Review what Sapu found. Nothing has moved.",
		"  Focus a row to read its location, reason and risk.",
		"",
	}
}

func (m *model) bodyHeight() int { return max(m.height-2-len(m.intro())-footLines, 1) }

func (m *model) listView() []string {
	listed := 0
	for _, g := range m.groups {
		listed += len(g.items)
	}
	lines := []string{
		m.title("review", fmt.Sprintf("%d found / %d skipped", listed, len(m.plan.Skipped))),
		m.rule(),
	}
	lines = append(lines, m.intro()...)

	body, _, _ := m.body()
	h := m.bodyHeight()
	from := min(m.offset, len(body))
	to := min(from+h, len(body))
	lines = append(lines, body[from:to]...)
	for i := to - from; i < h; i++ {
		lines = append(lines, "") // keeps the footer in place
	}

	status := ""
	switch {
	case m.status != "":
		status = m.status
	case m.filter != "" && !m.filtering:
		status = "Filter: " + m.filter + " / esc clears it"
	case from > 0 && to < len(body):
		status = "more above and below"
	case from > 0:
		status = "more above"
	case to < len(body):
		status = "more below"
	}
	if strings.HasPrefix(status, "RISKY") {
		status = riskStyle[rules.RiskRisky].Render(status)
	} else {
		status = dim.Render(status)
	}
	lines = append(lines, "  "+status, m.rule(), m.totalLine())
	return append(lines, m.keyLines()...)
}

// totalLine is the running total. Counts and sizes sit in fixed widths, so
// the words after them do not move while the selection changes.
func (m *model) totalLine() string {
	listed := 0
	for _, g := range m.groups {
		listed += len(g.items)
	}
	n, bytes, _ := m.selection()
	word := "items"
	if n == 1 {
		word = "item "
	}
	line := fmt.Sprintf("  %*d %s / %s selected", len(strconv.Itoa(listed)), n, word, padLeft(output.Bytes(bytes), sizeW))
	where := " -> quarantine, undo up to " + config.AgeWords(m.cfg.MaxAge)
	if width(line+where) <= m.width {
		line += where
	} else if short := " -> quarantine"; width(line+short) <= m.width {
		line += short
	}
	return line
}

// keyLines lists the keys that work on the row under the cursor, on two
// lines (design 05). A narrow terminal gets the ones that matter most first.
func (m *model) keyLines() []string {
	if m.filtering {
		return []string{"  / " + m.filter + "█   enter done   esc clear", ""}
	}
	r, ok := m.current()
	first := []string{"up/down move"}
	if ok && (r.kind == rowGroup || r.kind == rowItem) {
		first = append(first, "space select")
	}
	switch {
	case ok && r.kind == rowGroup && !m.groups[r.group].open:
		first = append(first, "right open")
	case ok && r.kind == rowItem && !m.pinned[r.index]:
		first = append(first, "right details")
	case ok && (r.kind == rowGroup || r.kind == rowItem || r.kind == rowMore):
		first = append(first, "left close")
	}
	enter := "enter review"
	if m.enterExpands() {
		enter = "enter expand"
	}
	first = append(first, "/ filter", enter)
	second := []string{"a select safe", "? all keys", "q cancel"}

	for _, sep := range []string{"   ", "  "} {
		l1, l2 := strings.Join(first, sep), strings.Join(second, sep)
		if width(l1)+2 <= m.width && width(l2)+2 <= m.width {
			return []string{"  " + l1, "  " + l2}
		}
	}
	// Too narrow for the design: the keys that matter most, in two lines.
	lines := []string{"", ""}
	i := 0
	for _, k := range []string{enter, "? all keys", "q cancel", "space select", "up/down move", "a select safe", "/ filter"} {
		if lines[i] != "" && width(lines[i]+"  "+k)+2 > m.width {
			if i == len(lines)-1 {
				break // both lines are full
			}
			i++
		}
		if lines[i] != "" {
			lines[i] += "  "
		}
		lines[i] += k
	}
	return []string{"  " + lines[0], "  " + lines[1]}
}

// body renders every row. start and end are the lines of the row under the
// cursor, its details included.
func (m *model) body() (lines []string, start, end int) {
	if _, ok := m.current(); !ok {
		text := "Nothing found that Sapu can move."
		if m.filter != "" {
			text = "Nothing matches the filter. esc clears it."
		}
		return []string{"", "  " + text}, 0, 0
	}
	section := ""
	for i, r := range m.rows() {
		// Headings of the skipped list and the commands come before their first row.
		switch {
		case (r.kind == rowSkip || r.kind == rowSkipMore) && section != "skip":
			section = "skip"
			lines = append(lines, "", m.skipHeading())
		case r.kind == rowCommand && section != "cmd":
			section = "cmd"
			lines = append(lines, "", m.heading("Commands you can run yourself", " / Sapu never runs these"))
		}
		at := i == m.cursor
		if at {
			start = len(lines)
		}
		lines = append(lines, m.rowLines(r, at)...)
		if at {
			end = len(lines)
		}
	}
	return lines, start, end
}

// heading is a bold title with a dim note that is cut to fit.
func (m *model) heading(title, note string) string {
	return "      " + bold.Render(title) + dim.Render(truncateEnd(note, m.width-6-width(title)))
}

// skipHeading: "      SKIPPED / Protected and in use                    4 items"
func (m *model) skipHeading() string {
	count := padLeft(plural(len(m.visibleSkips()), "item"), 9)
	left := "      " + bold.Render("SKIPPED")
	note := truncateEnd(" / Protected and in use", m.width-width(left)-width(count))
	return left + dim.Render(note) + spaces(m.width-width(left)-width(note)-width(count)-groupRightW+9) + count
}

// groupRightW is the width of the right side of a group heading:
// "  24 items    6.2 GB   [x]".
const groupRightW = 9 + 2 + sizeW + 3 + 3

// scroll keeps the row under the cursor, with its details, on screen.
func (m *model) scroll() {
	if m.screen != screenList || m.plan == nil {
		return
	}
	body, start, end := m.body()
	h := m.bodyHeight()
	end = min(end, start+h)
	if start < m.offset {
		m.offset = start
	}
	if end > m.offset+h {
		m.offset = end - h
	}
	m.offset = max(min(m.offset, len(body)-h), 0)
}

// pointer is the cursor column: "> " on the row under the cursor.
func pointer(at bool) string {
	if at {
		return bold.Render(">") + " "
	}
	return "  "
}

func (m *model) rowLines(r row, at bool) []string {
	switch r.kind {
	case rowGroup:
		return m.groupLines(r.group, at)
	case rowItem:
		lines := []string{m.itemLine(r.index, at)}
		if at || m.pinned[r.index] {
			lines = append(lines, m.itemDetails(r.index)...)
		}
		return lines
	case rowMore:
		g := m.groups[r.group]
		return []string{m.moreLine(len(m.visible(g))-maxShown, at)}
	case rowSkip:
		return m.skipLines(r.index, at)
	case rowSkipMore:
		return []string{m.moreLine(len(m.visibleSkips())-maxShown, at)}
	case rowCommand:
		return m.commandLines(r.index, at)
	}
	return nil
}

// checkbox is [x] when all items are selected, [-] when some are, [ ] when none.
func checkbox(selected, total int) string {
	switch {
	case total > 0 && selected == total:
		return "[x]"
	case selected > 0:
		return "[-]"
	}
	return "[ ]"
}

// groupLines: "> [v] SAFE / Safe to recreate          24 items     6.2 GB   [x]".
// A closed group says below how much of it is selected.
func (m *model) groupLines(gi int, at bool) []string {
	g := m.groups[gi]
	vis := m.visible(g)
	selected := 0
	var bytes int64
	for _, i := range vis {
		bytes += m.plan.Items[i].Bytes
		if m.plan.Items[i].Selected {
			selected++
		}
	}
	fold := "[>]"
	if g.open {
		fold = "[v]"
	}
	left := pointer(at) + fold + " " + riskStyle[g.risk].Render(riskWord(g.risk))
	right := padLeft(plural(len(vis), "item"), 9) + "  " + padLeft(output.Bytes(bytes), sizeW) + "   " + checkbox(selected, len(vis))
	label := ""
	if lw := m.width - width(left) - groupRightW - 1; lw >= 6 {
		label = truncateEnd(" / "+riskLabel(g.risk), lw)
	}
	lines := []string{left + label + spaces(m.width-width(left)-width(label)-width(right)) + right}
	if g.open {
		return lines
	}
	note := "Not selected."
	switch {
	case selected > 0:
		note = fmt.Sprintf("%d of %d selected.", selected, len(vis))
	case g.risk == rules.RiskRisky:
		note = "Not selected. Read the explanation before choosing an item."
	}
	return append(lines, dim.Render(spaces(6)+truncateEnd(note, m.width-7)))
}

// columns splits the room left of the size column into name and path.
func columns(room int) (nameW, pathW int) {
	if room < 32 {
		return room, 0
	}
	nameW = min(max(room*2/5, 14), 28)
	return nameW, room - nameW - 1
}

// itemLine: "    [x] npm cache        ~/.npm/_cacache        27.3 MB"
func (m *model) itemLine(i int, at bool) string {
	it := m.plan.Items[i]
	box := "[ ]"
	if it.Selected {
		box = "[x]"
	}
	left := "  " + pointer(at) + box + " "
	nameW, pathW := columns(m.width - rightPad - sizeW - 1 - width(left))
	text := padRight(truncateEnd(itemName(it), nameW), nameW)
	if at {
		text = bold.Render(text)
	}
	if pathW > 0 {
		text += " " + dim.Render(truncateMiddle(m.short(it.Path), pathW))
	}
	return left + text + spaces(m.width-rightPad-sizeW-width(left)-width(text)) + padLeft(output.Bytes(it.Bytes), sizeW)
}

// itemDetails: the full path on its own lines, the explanation, and whether
// it comes back. The path is never shortened here: this is where the user
// decides.
func (m *model) itemDetails(i int) []string {
	it := m.plan.Items[i]
	w := m.width - detailIndent - 1
	in := spaces(detailIndent)
	var lines []string
	for _, l := range chunks(m.short(it.Path), w) {
		lines = append(lines, in+l)
	}
	for _, l := range wrap(clean(strings.ReplaceAll(it.Explain, m.env.Home+"/", "~/")), w) {
		lines = append(lines, in+l)
	}
	back := "no"
	if it.Regenerates {
		back = "yes"
	}
	for _, l := range wrap("Regenerates: "+back+" / Risk: "+riskWord(it.Risk)+" / Rule: "+clean(it.Source), w) {
		lines = append(lines, in+dim.Render(l))
	}
	if m.armed == i {
		for _, l := range wrap("RISKY: may contain personal data. Press space again to select it.", w) {
			lines = append(lines, in+riskStyle[rules.RiskRisky].Render(l))
		}
	}
	return lines
}

func (m *model) moreLine(n int, at bool) string {
	left := pointer(at) + "      … " + strconv.Itoa(n) + " more"
	right := "enter expand"
	return left + spaces(m.width-rightPad-width(left)-width(right)) + dim.Render(right)
}

// skipReason is the reason of a skip in words, and why it applies here.
func (m *model) skipReason(s plan.Skip) (label, why string) {
	label = strings.ReplaceAll(s.Reason, "_", " ")
	switch s.Reason {
	case plan.ReasonProtected:
		why = "on Sapu's protection list"
		if m.why != nil && m.why(s.Detail) != "" {
			why = m.why(s.Detail)
		}
	case plan.ReasonWhitelisted:
		why = "you added it to your whitelist"
	case plan.ReasonInUse:
		why = "open in a program, or used very recently"
		if s.Detail != "" {
			why = s.Detail
		}
	case plan.ReasonDuplicate:
		why = "inside another item of this list"
	case plan.ReasonUnreadable:
		why = "couldn't read it; Full Disk Access may help (sapu doctor)"
	case plan.ReasonChanged:
		why = "changed since the plan was made"
	case plan.ReasonGone:
		why = "no longer there"
	default:
		why = s.Detail
	}
	return label, clean(why)
}

// skipLines: "   ~/.claude            protected    Claude Code memory and settings"
func (m *model) skipLines(i int, at bool) []string {
	s := m.plan.Skipped[i]
	label, why := m.skipReason(s)
	room := m.width - 4 - 13
	pathW, whyW := room, 0
	if room >= 40 {
		pathW = room * 45 / 100
		whyW = room - pathW - 1
	}
	line := pointer(at) + "  " + padRight(truncateMiddle(m.short(s.Path), pathW), pathW) + " " + label
	if whyW > 0 {
		line = padRight(line, width(line)+12-width(label)) + " " + dim.Render(truncateEnd(why, whyW))
	}
	lines := []string{line}
	if at {
		in := spaces(detailIndent)
		for _, l := range chunks(m.short(s.Path), m.width-detailIndent-1) {
			lines = append(lines, in+dim.Render(l))
		}
		for _, l := range wrap(label+": "+why+". Sapu leaves it where it is.", m.width-detailIndent-1) {
			lines = append(lines, in+l)
		}
	}
	return lines
}

// commandLines: "   go clean -cache                                   1.2 GB"
func (m *model) commandLines(i int, at bool) []string {
	it := m.plan.Items[i]
	size := ""
	if it.Bytes > 0 {
		size = output.Bytes(it.Bytes)
	}
	left := pointer(at) + "  "
	text := truncateEnd(clean(output.ShellCommand(it.Command)), m.width-rightPad-sizeW-1-width(left))
	lines := []string{left + text + spaces(m.width-rightPad-sizeW-width(left)-width(text)) + padLeft(size, sizeW)}
	if at {
		in := spaces(detailIndent)
		for _, l := range wrap(clean(strings.ReplaceAll(it.Explain, m.env.Home+"/", "~/")), m.width-detailIndent-1) {
			lines = append(lines, in+l)
		}
		for _, l := range wrap("Sapu does not run it. Type it in Terminal yourself if you want it.", m.width-detailIndent-1) {
			lines = append(lines, in+dim.Render(l))
		}
	}
	return lines
}

func (m *model) helpView() []string {
	keys := [][2]string{
		{"up/down k j", "move"},
		{"space", "select or unselect; a RISKY item asks twice"},
		{"right l", "open a group, or keep an item's details open"},
		{"left h", "close"},
		{"enter", "go to the confirm screen, or open a … more row"},
		{"a", "select all SAFE items (never RISKY)"},
		{"/", "filter by name, path or rule"},
		{"esc", "clear the filter"},
		{"?", "this list"},
		{"q", "quit without changing anything"},
	}
	lines := []string{m.title("keys", ""), m.rule(), ""}
	for _, k := range keys {
		for i, l := range wrap(k[1], m.width-16) {
			if i == 0 {
				lines = append(lines, "  "+padRight(k[0], 12)+" "+l)
			} else {
				lines = append(lines, spaces(15)+l)
			}
		}
	}
	lines = append(lines, "")
	for _, l := range wrap("Nothing moves until you press y on the confirm screen. Items go to quarantine, and sapu undo puts them back.", m.width-3) {
		lines = append(lines, "  "+l)
	}
	return append(lines, "", dim.Render("  Press any key to go back."))
}

// ---- confirm ----

// retention is the sentence that says how long quarantine holds a run.
func (m *model) retention() string {
	return fmt.Sprintf("Quarantined files still use disk space. Runs can expire after %s, or earlier when quarantine exceeds %s. Restore them before expiry.",
		config.AgeWords(m.cfg.MaxAge), output.Bytes(m.cfg.MaxBytes))
}

// confirmView is design 06. The safe default is n: it comes first, and
// nothing but y moves anything.
func (m *model) confirmView() []string {
	n, bytes, byRisk := m.selection()
	boxW := min(m.width-4, 76)
	textW := boxW - 4

	var body []string
	add := func(text string) { body = append(body, wrap(text, textW)...) }
	body = append(body, bold.Render(truncateEnd(fmt.Sprintf("Move %s to quarantine?", plural(n, "item")), textW)), "")
	counts := fmt.Sprintf("%s selected / %s %d / %s %d / %s %d", output.Bytes(bytes),
		riskStyle[rules.RiskSafe].Render("SAFE"), byRisk[rules.RiskSafe],
		riskStyle[rules.RiskModerate].Render("MODERATE"), byRisk[rules.RiskModerate],
		riskStyle[rules.RiskRisky].Render("RISKY"), byRisk[rules.RiskRisky])
	if width(counts) <= textW {
		body = append(body, counts)
	} else {
		body = append(body, output.Bytes(bytes)+" selected", fmt.Sprintf("SAFE %d / MODERATE %d / RISKY %d", byRisk[rules.RiskSafe], byRisk[rules.RiskModerate], byRisk[rules.RiskRisky]))
	}
	if r := byRisk[rules.RiskRisky]; r > 0 {
		for _, l := range wrap(fmt.Sprintf("%s may contain personal data.", plural(r, "RISKY item")), textW) {
			body = append(body, riskStyle[rules.RiskRisky].Render(l))
		}
	}
	body = append(body, "")
	add("Sapu rechecks these exact items before moving them.")
	add("Protected, changed and open files are skipped.")
	body = append(body, "")
	add(m.retention())
	body = append(body, "")
	keep, move := bold.Render("[n]")+" Keep reviewing", "[y] Move to quarantine"
	if gap := textW - width(keep) - width(move); gap >= 2 {
		body = append(body, keep+spaces(gap)+move)
	} else {
		body = append(body, keep, move)
	}

	lines := append([]string{""}, box(body, boxW)...)
	return append(lines, "", dim.Render("  Only y moves anything. n or esc goes back."))
}

// box draws an ASCII box boxW cells wide, two cells in from the left edge.
func box(body []string, boxW int) []string {
	border := "  +" + strings.Repeat("-", max(boxW-2, 0)) + "+"
	out := []string{border}
	for _, l := range body {
		out = append(out, "  | "+l+spaces(boxW-4-width(l))+" |")
	}
	return append(out, border)
}

// ---- applying ----

func (m *model) applyingView() []string {
	total := len(m.todo)
	done := min(m.done, total)
	counter := fmt.Sprintf("%d / %d items", done, total)
	lines := []string{m.title("moving to quarantine", ""), m.rule(), ""}
	for _, l := range wrap("Moving selected items. You can stop after the current item.", m.width-3) {
		lines = append(lines, "  "+l)
	}
	lines = append(lines, "")

	bar := ""
	if barW := min(m.width-width(counter)-9, 30); barW >= 8 {
		bar = progressBar(done, total, barW) + "   "
	}
	lines = append(lines, "  "+bar+counter)
	if done < total {
		it := m.todo[done]
		lines = append(lines, "  Current: "+truncateEnd(itemName(it), m.width-12))
		for _, l := range chunks(m.short(it.Path), m.width-5) {
			lines = append(lines, "    "+dim.Render(l))
		}
	} else {
		lines = append(lines, "  Current: finishing up")
	}
	lines = append(lines, "")

	foot, keys := "Completed items stay in quarantine if this run is interrupted.", "ctrl-c stop"
	if m.stopping {
		foot, keys = "Stopping after the current item. Completed items stay in quarantine.", ""
	}
	for _, l := range wrap(foot, m.width-3) {
		lines = append(lines, "  "+dim.Render(l))
	}
	if keys != "" {
		lines = append(lines, "  "+dim.Render(keys))
	}
	return lines
}

// progressBar is "[####....]": # for what is done, . for the rest, so it
// reads without colour too.
func progressBar(done, total, w int) string {
	inner := max(w-2, 0)
	filled := inner
	if total > 0 {
		filled = done * inner / total
	}
	return "[" + strings.Repeat("#", filled) + strings.Repeat(".", inner-filled) + "]"
}

// ---- receipt ----

// sentence turns a reason into a sentence: "changed since the plan was
// made" becomes "Changed since the plan was made."
func sentence(s string) string {
	s = strings.TrimRight(s, ". ")
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[:1])) + string(r[1:]) + "."
}

func (m *model) receiptView() []string {
	r, err := m.run.result, m.run.err
	stopped := errors.Is(err, context.Canceled)
	lines := []string{m.title("receipt", ""), m.rule(), ""}
	para := func(indent, text string) {
		for _, l := range wrap(text, m.width-width(indent)-1) {
			lines = append(lines, indent+l)
		}
	}
	headline := func(tag, text string) {
		for i, l := range wrap(text, m.width-width(tag)-5) {
			if i == 0 {
				l = tag + "  " + l
			} else {
				l = spaces(width(tag)+2) + l
			}
			lines = append(lines, "  "+bold.Render(l))
		}
	}

	moved, skipped, failed := 0, 0, 0
	if r != nil {
		moved, skipped, failed = len(r.Moved), len(r.Skipped), len(r.Failed)
	}
	switch {
	case r == nil:
		headline("FAILED", "Nothing was moved.")
		para("  ", clean(err.Error()))
	case moved == 0 && stopped:
		headline("STOPPED", "Nothing was moved.")
	case moved == 0:
		headline("REVIEW", "Nothing was moved.")
	case stopped:
		headline("STOPPED", fmt.Sprintf("Moved %d of %d items / %s to quarantine.", moved, len(m.todo), output.Bytes(r.Bytes)))
	case skipped+failed > 0:
		headline("REVIEW", fmt.Sprintf("%d moved / %d skipped / %d failed", moved, skipped, failed))
	default:
		headline("OK", fmt.Sprintf("Moved %s / %s to quarantine.", plural(moved, "item"), output.Bytes(r.Bytes)))
	}
	if r != nil && err != nil && !stopped {
		para("  ", clean(err.Error()))
	}
	lines = append(lines, "",
		fmt.Sprintf("  Moved   %*d", 6, moved),
		fmt.Sprintf("  Skipped %*d", 6, skipped),
		fmt.Sprintf("  Failed  %*d", 6, failed))

	if r != nil {
		for _, s := range r.Skipped {
			_, why := m.skipReason(s)
			lines = append(lines, "", "  "+bold.Render("SKIPPED")+"  "+truncateMiddle(m.short(s.Path), m.width-13))
			for _, l := range wrap(sentence(why)+" Left in place.", m.width-12) {
				lines = append(lines, spaces(11)+dim.Render(l))
			}
		}
		for _, f := range r.Failed {
			lines = append(lines, "", "  "+bold.Render("FAILED")+"   "+truncateMiddle(m.short(f.Item.Path), m.width-13))
			for _, l := range wrap(sentence(clean(f.Error)), m.width-12) {
				lines = append(lines, spaces(11)+dim.Render(l))
			}
		}
	}

	if r != nil && moved > 0 {
		lines = append(lines, "")
		if skipped+failed > 0 || stopped {
			para("  ", fmt.Sprintf("The %s completed are in quarantine.", plural(moved, "item")))
		}
		para("  ", "Quarantined files still use space.")
		para("  ", "Eligible to expire after: "+m.run.start.Add(m.cfg.MaxAge).Format("2 January 2006"))
		para("  ", "Older runs may expire sooner if quarantine exceeds "+output.Bytes(m.cfg.MaxBytes)+".")
		lines = append(lines, "")
		lines = append(lines, m.commandPair("Restore this run:", "sapu undo "+r.RunID)...)
		lines = append(lines, m.commandPair("Inspect held runs:", "sapu quarantine list")...)
		lines = append(lines, m.commandPair("Free the space now:", "sapu quarantine empty "+r.RunID)...)
	}
	if r != nil && r.Freed > 0 {
		lines = append(lines, "")
		para("  ", "Also deleted "+output.Bytes(r.Freed)+" from older runs whose quarantine had expired.")
	}
	lines = fitLines(lines, m.height-2)
	return append(lines, "", dim.Render("  q quit"))
}

// commandPair puts a label and a command on one line when they fit, else on two.
func (m *model) commandPair(label, command string) []string {
	if line := "  " + padRight(label, 20) + bold.Render(command); width(line) <= m.width {
		return []string{line}
	}
	// The command is what the user types: indent less rather than cut it.
	return []string{"  " + label, spaces(min(4, m.width-width(command))) + bold.Render(command)}
}

// fitLines cuts lines to at most limit, ending with a count of what was cut.
func fitLines(lines []string, limit int) []string {
	if limit < 2 || len(lines) <= limit {
		return lines
	}
	cut := len(lines) - (limit - 1)
	return append(lines[:limit-1:limit-1], dim.Render(fmt.Sprintf("  … %d more lines (all of them are printed when you quit)", cut)))
}
