package menu

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/khmuhtadin/sapu/internal/mascot"
	"github.com/khmuhtadin/sapu/internal/output"
)

// maxWidth is the widest the menu is drawn: the 80 columns of the design.
const maxWidth = 80

const appsText = `These commands need an app name or a folder, so the menu cannot run them. Type them in your shell. Each one lists what it found and asks before it moves anything to quarantine. Add --plan FILE to only save the plan.

  sapu uninstall APP    Remove an app and the files it left behind
  sapu purge DIR        Find build folders (node_modules, target, ...)
  sapu dupes            Move extra copies of duplicate files to quarantine
  sapu installer        Find old installers (.dmg, .pkg, .iso)
  sapu large            Find big files you have not opened in a long time
`

// settingsCommands follows the settings text: the commands that change what
// the screen shows. This screen only reads.
const settingsCommands = `
This screen only shows your settings. To change them:

  sapu whitelist add PATH      Never touch this path
  sapu whitelist remove PATH   Allow Sapu to touch it again
  sapu config init             Write an example sapu.toml
  sapu config path             Print where Sapu reads sapu.toml from
`

const helpText = `  up/down  j/k   move
  enter          open the highlighted choice
  1 to 7         open a choice directly
  ?              this list
  q              quit; on a screen, go back to the menu
  esc            go back
  ctrl-c         quit

Menu choices open a review or inspection screen. They do not move or delete files by themselves. Moving files to quarantine, and deleting them for good, always needs its own confirmation.`

func (m *model) View() string {
	var out []string
	switch m.screen {
	case screenMenu:
		out = m.menuView()
	case screenHelp:
		out = m.pageView("keys", lines(helpText), "any key goes back")
	case screenExplore:
		out = m.exploreView()
	case screenPage:
		out = m.textPageView()
	case screenQuarantine:
		out = m.quarantineView()
	}
	return strings.Join(out, "\n")
}

// w is the width to draw in: the terminal's, up to maxWidth. Before the
// first size message, or on a terminal that reports 0 columns, it is 80; it
// never goes below 24, so the box arithmetic cannot go negative.
func (m *model) w() int {
	if m.width <= 0 {
		return min(80, maxWidth)
	}
	return min(max(m.width, 24), maxWidth)
}

// ---- the menu ----

// menuView draws design 04. Every line is as wide as the box.
func (m *model) menuView() []string {
	boxW := m.w()
	iw := boxW - 2
	showFish := boxW >= 64 && m.height >= 20
	menuCol := 2
	fishCol := 0
	if showFish {
		menuCol = iw - 31 // 47 in the 80-column design
		fishCol = min(5, menuCol-mascot.Width)
	}

	// Plain text of the right-hand column, by row. Row 3 is MENU, rows 5 to
	// 11 the choices, row 13 q Quit and row 15 the reminder.
	right := map[int]string{3: "MENU", 13: "  q  Quit", 15: "Review before moving files."}
	for i, name := range choices {
		mark := " "
		if i == m.cursor {
			mark = ">"
		}
		right[5+i] = fmt.Sprintf("%s %d  %s", mark, i+1, name)
	}
	if m.notice != "" {
		right[16] = m.notice
	}
	fish := mascot.Render(m.color)

	var rows []string
	for r := range 18 {
		var line string // plain, to measure
		var shown string
		switch r {
		case 0:
			tag := "CLI / OPEN SOURCE"
			at := menuCol + 4
			if !showFish {
				at = iw - 2 - len(tag)
			}
			line = pad("  sapu", at) + tag
			shown = "  " + mascot.Accent("sapu", m.color) + line[len("  sapu"):]
		case 1:
			line = "  A careful Mac cleanup."
		case 17:
			line = "  up/down move   enter open   ? help   q quit"
		default:
			left := strings.Repeat(" ", fishCol)
			shownLeft := left
			if showFish && r >= 4 && r < 4+mascot.Height {
				plain := mascot.Lines()[r-4]
				left += pad(plain, mascot.Width)
				shownLeft += fish[r-4] + strings.Repeat(" ", mascot.Width-len(plain))
			}
			text := right[r]
			line = pad(left, menuCol) + text
			shown = shownLeft + strings.Repeat(" ", max(menuCol-len(left), 0)) + m.styleChoice(r, text)
		}
		if shown == "" {
			shown = line
		}
		if len(line) > iw {
			// Only the notice can be too long: cut it, never the choices.
			line = line[:iw]
			shown = line
		}
		rows = append(rows, "|"+shown+strings.Repeat(" ", iw-len(line))+"|")
	}
	if !showFish && m.height > 0 && m.height < len(rows)+2 {
		rows = dropBlank(rows)
	}
	border := "+" + strings.Repeat("-", iw) + "+"
	return append(append([]string{border}, rows...), border)
}

// styleChoice makes the choice under the cursor bold, when color is on.
func (m *model) styleChoice(row int, text string) string {
	if m.color && row == 5+m.cursor {
		return "\x1b[1m" + text + "\x1b[0m"
	}
	return text
}

// dropBlank removes the empty rows of the box, for a short terminal.
func dropBlank(rows []string) []string {
	var out []string
	for _, r := range rows {
		if strings.Trim(r, "| ") != "" {
			out = append(out, r)
		}
	}
	return out
}

// pad fills s with spaces up to n bytes. The menu is plain ASCII.
func pad(s string, n int) string { return s + strings.Repeat(" ", max(n-len(s), 0)) }

// ---- screens ----

// pageView is a screen under the menu: a header, a rule, the body (scrolled
// when it is longer than the terminal), a rule and the keys.
func (m *model) pageView(title string, body []string, keys string) []string {
	return m.frame(title, body, -1, keys)
}

// frame draws the header, the body window and the footer. focus is a body
// line that must stay visible; -1 for none.
func (m *model) frame(title string, body []string, focus int, keys string) []string {
	w := m.w()
	rule := strings.Repeat("-", max(w-4, 1))
	h := m.bodyHeight()

	var wrapped []string
	for _, l := range body {
		wrapped = append(wrapped, wrapLine(l, w-2)...)
	}
	off := min(max(m.offset, 0), max(len(wrapped)-h, 0))
	if focus >= 0 {
		off = min(off, focus)
		off = max(off, focus+2-h)
		off = max(min(off, len(wrapped)-h), 0)
	}
	m.offset = off
	shown := wrapped[off:min(off+h, len(wrapped))]

	more := ""
	switch {
	case off > 0 && off+h < len(wrapped):
		more = "  (more above and below)"
	case off > 0:
		more = "  (more above)"
	case off+h < len(wrapped):
		more = "  (more below)"
	}
	out := []string{mascot.Accent("sapu", m.color) + " / " + title, rule}
	out = append(out, shown...)
	for i := len(shown); i < h; i++ {
		out = append(out, "")
	}
	return append(out, rule, truncate("  "+keys+more, w))
}

func (m *model) exploreView() []string {
	body := []string{"  Look at what takes space. Nothing here moves or deletes files.", ""}
	rows := [][2]string{
		{"Folder explorer", "Browse folder sizes (sapu analyze). It asks before it moves anything."},
		{"System Data", "What fills \"System Data\" and how to reclaim it (sapu explain). Read only."},
	}
	for i, r := range rows {
		mark := " "
		if i == m.sub {
			mark = ">"
		}
		body = append(body, fmt.Sprintf("  %s %d  %s", mark, i+1, r[0]), "         "+r[1], "")
	}
	return m.pageView("explore storage", body, "up/down move   enter open   esc back")
}

func (m *model) textPageView() []string {
	body := lines(m.text)
	if m.err != nil {
		body = append([]string{"  Could not read this: " + m.err.Error(), ""}, body...)
		if m.loading {
			body = body[:2]
		}
	}
	return m.pageView(m.title, body, "up/down scroll   esc back")
}

func (m *model) quarantineView() []string {
	keys := "up/down move   enter restore   d delete   D delete all   esc back"
	var body []string
	switch {
	case m.loading:
		body = []string{"  Reading the quarantine..."}
	case m.err != nil:
		body = []string{"  Could not read the quarantine: " + m.err.Error()}
	case len(m.q.Runs) == 0:
		body = []string{"  The quarantine is empty. There is nothing to restore."}
	default:
		body = []string{"  Runs held in quarantine. Restoring puts a run back where it was.", "  Deleting frees the space now, for good.", ""}
	}
	focus := -1
	for i, r := range m.q.Runs {
		mark := " "
		if i == m.sub {
			mark = ">"
			focus = len(body)
		}
		body = append(body,
			fmt.Sprintf("  %s %s  %s", mark, r.ID, output.Bytes(r.Bytes)),
			"      Started "+r.Started.Local().Format("Mon 2 Jan 15:04")+" / can expire after "+r.ExpiresAfter.Local().Format("Mon 2 Jan"))
	}
	if m.q.Retention != "" {
		body = append(body, "", "  "+m.q.Retention)
	}
	if m.busy {
		body = append(body, "", "  Working...")
		keys = "please wait"
	}
	if m.note != "" {
		body = append(body, "")
		for _, l := range lines(m.note) {
			body = append(body, "  "+l)
		}
	}
	if m.confirming {
		return m.confirmView()
	}
	return m.frame("quarantine", body, focus, keys)
}

// confirmView asks before a restore or a delete. n is the safe default: only
// y does it.
func (m *model) confirmView() []string {
	r := m.q.Runs[m.sub]
	w := min(m.w()-2, 72)
	inner := w - 4
	var text []string
	choice, short := "[n] Keep in quarantine   [y] Restore this run", "[n] Keep   [y] Restore"
	switch m.ask {
	case askRestore:
		text = []string{
			"Restore run " + r.ID + "?",
			"",
			output.Bytes(r.Bytes) + " go back to where they were.",
			"If something is already at an original path, the item is restored next to it. Nothing is overwritten.",
		}
	case askDelete:
		text = []string{
			"Delete run " + r.ID + " for good?",
			"",
			"This frees " + output.Bytes(r.Bytes) + " now. There is no undo: the run can no longer be restored.",
		}
		choice, short = "[n] Keep in quarantine   [y] Delete for good", "[n] Keep   [y] Delete"
	case askDeleteAll:
		var total int64
		for _, h := range m.q.Runs {
			total += h.Bytes
		}
		text = []string{
			fmt.Sprintf("Delete all %d runs for good?", len(m.q.Runs)),
			"",
			"This empties the quarantine and frees " + output.Bytes(total) + " now. There is no undo: none of these runs can be restored.",
		}
		choice, short = "[n] Keep in quarantine   [y] Delete all for good", "[n] Keep   [y] Delete all"
	}
	var body []string
	for _, l := range append(text, "") {
		body = append(body, wrapLine(l, inner)...)
	}
	if len(choice) > inner {
		choice = short
	}
	body = append(body, choice)

	border := "+" + strings.Repeat("-", w-2) + "+"
	out := []string{"", border}
	for _, l := range body {
		out = append(out, "| "+l+strings.Repeat(" ", max(inner-lipgloss.Width(l), 0))+" |")
	}
	return append(out, border)
}

// ---- text ----

func width(s string) int { return lipgloss.Width(s) }

// truncate cuts s to w cells, ending in "..." when it was cut.
func truncate(s string, w int) string {
	if width(s) <= w {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := width(string(r))
		if used+rw > w-3 {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String() + "..."
}

// wrapLine breaks one line at spaces into lines of at most w cells. A
// continuation line keeps the indent of the line, two spaces deeper for an
// indented line, or lines up under the second column of a two-column line.
// A word longer than w is cut.
func wrapLine(s string, w int) []string {
	w = max(w, 8)
	if width(s) <= w {
		return []string{s}
	}
	indent := len(s) - len(strings.TrimLeft(s, " "))
	cont := strings.Repeat(" ", min(indent, w/2))
	if indent > 0 {
		cont = strings.Repeat(" ", min(indent+2, w/2))
	}
	if col := secondColumn(s); col > 0 && col <= w/2 {
		cont = strings.Repeat(" ", col) // a two-column line wraps under its second column
	}
	var out []string
	prefix := ""
	for width(prefix+s) > w {
		cut := breakPoint(s, w-width(prefix))
		out = append(out, prefix+strings.TrimRight(s[:cut], " "))
		s = strings.TrimLeft(s[cut:], " ")
		prefix = cont
	}
	return append(out, prefix+s)
}

// secondColumn is where the second column of a line starts: the first text
// after a run of two or more spaces. It is 0 for a line of plain prose.
func secondColumn(s string) int {
	text := strings.TrimLeft(s, " ")
	start := len(s) - len(text)
	gap := strings.Index(text, "  ")
	if gap < 0 {
		return 0
	}
	rest := strings.TrimLeft(text[gap:], " ")
	return start + gap + len(text[gap:]) - len(rest)
}

// breakPoint is the byte index to cut s at, so that the first part fits in
// budget cells: after its last space, or inside a word that has none.
func breakPoint(s string, budget int) int {
	lastSpace, used := 0, 0
	seenText := false
	for i, r := range s {
		rw := width(string(r))
		if used+rw > budget {
			if lastSpace > 0 {
				return lastSpace
			}
			return max(i, 1)
		}
		used += rw
		if r == ' ' && seenText {
			lastSpace = i
		} else if r != ' ' {
			seenText = true
		}
	}
	return len(s)
}
