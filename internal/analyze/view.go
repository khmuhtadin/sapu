package analyze

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/walk"
)

// Styles. lipgloss drops all of them when NO_COLOR is set or the output is
// not a terminal, so every state is also visible as plain characters: ">" for
// the cursor, "*" for marked entries.
var (
	accent    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "25", Dark: "81"}).Bold(true)
	bold      = lipgloss.NewStyle().Bold(true)
	dim       = lipgloss.NewStyle().Faint(true)
	errorText = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
)

const (
	chromeLines = 6 // header (3) and footer (3) around the entry list
	sizeWidth   = 8
	pctWidth    = 4
)

func (m *model) View() string {
	switch m.screen {
	case screenScanning:
		return m.scanView()
	case screenBrowsing:
		if m.help {
			return m.helpView()
		}
		return m.browseView()
	case screenPlanning:
		return "\n  Checking the marked entries...\n"
	case screenReviewing:
		return m.reviewView()
	case screenApplying:
		return "\n  Moving to quarantine...\n"
	case screenResult:
		return m.resultView()
	}
	return ""
}

// short writes a path with the home folder as "~".
func (m *model) short(path string) string {
	home := m.env.Home
	switch {
	case home == "":
	case path == home:
		return "~"
	case inside(path, home):
		return "~" + path[len(home):]
	}
	return clean(path)
}

// ---- scanning ----

func (m *model) scanView() string {
	ev := m.progress
	lines := []string{
		truncateEnd("  "+m.spin.View()+" Scanning "+m.short(m.root), m.width),
		"",
		"  files   " + padLeft(commas(ev.Files), 12),
		"  size    " + padLeft(humanBytes(ev.Bytes), 12),
		"  errors  " + padLeft(commas(ev.Errors), 12) + dim.Render("  unreadable, skipped"),
		"",
		dim.Render(truncateMiddle("  "+m.short(ev.Path), m.width)),
		"",
		dim.Render("  esc/q to stop"),
	}
	return strings.Join(lines, "\n")
}

// ---- browsing ----

func (m *model) browseView() string {
	cur := m.current()
	node := cur.node

	title := bold.Render("Sapu analyze")
	info := humanBytes(node.Bytes) + " in " + commas(node.Files) + " files"
	if m.partial {
		title += "  " + accent.Render("partial")
		info += ", partial: the scan was stopped, sizes may be low"
	}
	lines := []string{
		title,
		bold.Render(truncateMiddle(m.short(node.Path), m.width)),
		dim.Render(truncateEnd(info, m.width)),
	}

	rows := m.rows()
	body := make([]string, 0, rows)
	if len(node.Children) == 0 {
		body = append(body, dim.Render("  (empty)"))
	}
	end := min(cur.offset+rows, len(node.Children))
	for i := cur.offset; i < end; i++ {
		body = append(body, m.row(node.Children[i], node, i == cur.cursor))
	}
	for len(body) < rows {
		body = append(body, "") // keeps the footer in place
	}
	lines = append(lines, body...)

	status := dim.Render(truncateEnd(m.status, m.width))
	if strings.HasPrefix(m.status, "Could not") {
		status = errorText.Render(truncateEnd(m.status, m.width))
	}
	count, bytes := m.markedTotal()
	marks := dim.Render("Nothing marked")
	if count > 0 {
		marks = accent.Render(fmt.Sprintf("%d marked, %s", count, humanBytes(bytes)))
	}
	lines = append(lines, status, marks, dim.Render(truncateEnd(m.hints(), m.width)))
	return strings.Join(lines, "\n")
}

func (m *model) hints() string {
	if m.width < 64 {
		return "space mark  x review  ? help  q quit"
	}
	return "↑↓ move  enter open  ← back  space mark  x review  ? help  q quit"
}

// row draws one entry:  "> * 12.3 GB  ████░░░░░░  45%  name/"
func (m *model) row(n, parent *walk.Node, cursor bool) string {
	pointer, mark := " ", " "
	if cursor {
		pointer = accent.Render(">")
	}
	if m.marked[n.Path] != nil {
		mark = accent.Render("*")
	}

	barWidth := 0
	switch {
	case m.width >= 100:
		barWidth = 20
	case m.width >= 64:
		barWidth = 10
	}
	fraction := 0.0
	if parent.Bytes > 0 {
		fraction = float64(n.Bytes) / float64(parent.Bytes)
	}

	bar, barSegment := "", 0
	if barWidth > 0 {
		barSegment = barWidth + 1
		filled := int(fraction*float64(barWidth) + 0.5)
		if n.Bytes > 0 {
			filled = max(filled, 1)
		}
		bar = accent.Render(strings.Repeat("█", filled)) + dim.Render(strings.Repeat("░", barWidth-filled)) + " "
	}

	pct := fmt.Sprintf("%d%%", int(fraction*100))
	if n.Bytes > 0 && fraction < 0.01 {
		pct = "<1%"
	}

	nameWidth := max(m.width-(4+sizeWidth+1+barSegment+pctWidth+2), 4)
	name := clean(n.Name)
	if n.IsDir {
		name = truncateMiddle(name, nameWidth-1) + "/"
	} else {
		name = truncateMiddle(name, nameWidth)
	}
	if cursor {
		name = bold.Render(name)
	}

	return pointer + " " + mark + " " + padLeft(humanBytes(n.Bytes), sizeWidth) + " " + bar + dim.Render(padLeft(pct, pctWidth)) + "  " + name
}

func (m *model) helpView() string {
	lines := []string{
		"  ↑ ↓  k j       move",
		"  enter → l      open a folder",
		"  ⌫ ← h          go up",
		"  space          mark or unmark",
		"  x              review the marked entries",
		"  ?              this help",
		"  q              quit",
	}
	for i := range lines {
		lines[i] = truncateEnd(lines[i], m.width)
	}
	out := []string{bold.Render("Sapu analyze: keys"), ""}
	out = append(out, lines...)
	out = append(out, "")
	out = append(out, wrap("Nothing changes until you confirm on the review screen. Marked entries go to quarantine. Put them back with sapu undo.", m.width)...)
	out = append(out, "", dim.Render("Press any key to go back."))
	return strings.Join(out, "\n")
}

// ---- review ----

func (m *model) reviewView() string {
	p := m.plan
	var lines []string

	if len(p.Items) > 0 {
		var total int64
		for _, it := range p.Items {
			total += it.Bytes
		}
		lines = append(lines, bold.Render(truncateEnd(fmt.Sprintf("Will move %s (%s)", entries(len(p.Items)), humanBytes(total)), m.width)))
		for _, it := range p.Items {
			lines = append(lines, "  "+padLeft(humanBytes(it.Bytes), sizeWidth)+"  "+truncateMiddle(m.short(it.Path), m.width-12))
		}
	}
	skipped := append(append([]plan.Skip{}, m.dropped...), p.Skipped...)
	if len(skipped) > 0 {
		lines = append(lines, "", bold.Render("Skipped "+entries(len(skipped))))
		lines = append(lines, m.skipLines(skipped)...)
	}

	prompt := append(wrap("These go to quarantine. Put them back with `sapu undo`.", m.width), wrap("Press y to confirm, n to go back.", m.width)...)
	if len(p.Items) == 0 {
		prompt = append(wrap("Nothing can be moved.", m.width), wrap("Press n to go back.", m.width)...)
	}

	lines = fitLines(lines, m.height-2-1-len(prompt))
	out := []string{accent.Render("Review"), ""}
	out = append(out, lines...)
	out = append(out, "")
	out = append(out, prompt...)
	return strings.Join(out, "\n")
}

// skipLines lists skipped entries, each with its reason.
func (m *model) skipLines(skipped []plan.Skip) []string {
	var lines []string
	for _, s := range skipped {
		label := strings.ReplaceAll(s.Reason, "_", " ")
		lines = append(lines, "  "+dim.Render(padRight(label, 11))+" "+truncateMiddle(m.short(s.Path), m.width-14))
		if s.Detail != "" {
			lines = append(lines, dim.Render("  "+strings.Repeat(" ", 12)+truncateEnd(clean(s.Detail), m.width-14)))
		}
	}
	return lines
}

// ---- result ----

func (m *model) resultView() string {
	var lines []string
	if m.failure != "" {
		lines = append(lines, errorText.Render(truncateEnd("Apply failed: "+clean(m.failure), m.width)), "")
	}
	if r := m.result; r != nil {
		if len(r.Moved) > 0 {
			lines = append(lines, bold.Render(truncateEnd(fmt.Sprintf("Moved %s (%s) to quarantine.", entries(len(r.Moved)), humanBytes(r.Bytes)), m.width)))
			lines = append(lines, wrap("Space is freed when the quarantine expires, or now with `sapu quarantine empty`.", m.width)...)
			lines = append(lines, truncateEnd("Put them back with: sapu undo "+r.RunID, m.width))
		} else {
			lines = append(lines, bold.Render("Nothing was moved."))
		}
		if len(r.Skipped) > 0 {
			lines = append(lines, "", bold.Render("Skipped "+entries(len(r.Skipped))))
			lines = append(lines, m.skipLines(r.Skipped)...)
		}
		if len(r.Failed) > 0 {
			lines = append(lines, "", errorText.Render("Failed "+entries(len(r.Failed))))
			for _, f := range r.Failed {
				lines = append(lines, "  "+truncateMiddle(m.short(f.Item.Path), m.width-4))
				lines = append(lines, dim.Render("    "+truncateEnd(clean(f.Error), m.width-4)))
			}
		}
	}
	lines = fitLines(lines, m.height-2)
	return strings.Join(append(lines, "", dim.Render(truncateEnd("Press any key to go back.", m.width))), "\n")
}

// fitLines cuts lines to at most limit, ending with a count of what was cut.
func fitLines(lines []string, limit int) []string {
	if limit < 2 || len(lines) <= limit {
		return lines
	}
	cut := len(lines) - (limit - 1)
	return append(lines[:limit-1:limit-1], dim.Render(fmt.Sprintf("  ... %d more lines", cut)))
}

// wrap breaks text into lines of at most w cells.
func wrap(text string, w int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case width(line)+1+width(word) <= w:
			line += " " + word
		default:
			lines = append(lines, truncateEnd(line, w))
			line = word
		}
	}
	return append(lines, truncateEnd(line, w))
}

func entries(n int) string {
	if n == 1 {
		return "1 entry"
	}
	return fmt.Sprintf("%d entries", n)
}
