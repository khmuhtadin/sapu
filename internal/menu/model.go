package menu

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type screen int

const (
	screenMenu screen = iota
	screenHelp
	screenExplore
	screenPage // a text page: history, system check, settings, System Data
	screenQuarantine
	screenTools   // the list of tools
	screenFolders // a tool that looks in folders: where it looks
	screenApps    // uninstall: pick the app
)

// choices are the menu entries, in order. Keys 1 to 7 open them.
var choices = []string{
	"Scan & review",
	"Explore storage",
	"Tools",
	"Quarantine",
	"History",
	"System check",
	"Settings",
}

// exploreChoices are the entries of Explore storage.
var exploreChoices = []string{"Folder explorer", "System Data"}

// Messages. seq says which screen asked: an answer for a screen the user
// already left is dropped.
type (
	pageMsg struct {
		seq  int
		text string
		err  error
	}
	quarantineMsg struct {
		seq int
		q   Quarantine
		err error
	}
	doneMsg struct { // a restore or a delete finished
		seq  int
		text string
		err  error
	}
	appsMsg struct {
		seq  int
		apps []App
		err  error
	}
)

// ask is what the quarantine confirmation asks about.
type ask int

const (
	askRestore   ask = iota // put q.Runs[sub] back
	askDelete               // delete q.Runs[sub] for good
	askDeleteAll            // delete every run for good
)

type model struct {
	ctx           context.Context
	b             Backend
	color         bool
	width, height int

	screen screen
	cursor int // the menu choice; Explore storage and Quarantine keep their own in sub
	sub    int
	offset int // first body line shown on a page or the quarantine list
	seq    int

	// a text page
	title   string
	text    string
	loading bool
	err     error

	// quarantine
	q          Quarantine
	confirming bool // asking the question in ask
	ask        ask
	busy       bool   // a restore or delete is running
	note       string // what the last restore or delete did

	// tools
	tools      []Tool
	toolCursor int    // the tool under the cursor on the Tools screen
	folder     string // the folder the user chose, as typed; "" for the defaults
	folderPath string // that folder, ~ expanded
	editing    bool   // typing a folder
	input      string // what is typed so far
	inputErr   string // why the typed folder cannot be used
	apps       []App
	appFilter  string

	launch launch
	run    toolRun // the tool to run, with launchTool
	notice string  // shown on the menu: what the last screen said
}

func newModel(ctx context.Context, b Backend, color bool) *model {
	return &model{ctx: ctx, b: b, color: color, width: 80, height: 24}
}

func (m *model) Init() tea.Cmd { return nil }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case pageMsg:
		if msg.seq == m.seq && m.screen == screenPage {
			m.loading, m.text, m.err = false, msg.text, msg.err
		}
	case quarantineMsg:
		if msg.seq == m.seq && m.screen == screenQuarantine {
			m.loading, m.q, m.err = false, msg.q, msg.err
			m.sub = min(m.sub, max(len(m.q.Runs)-1, 0))
		}
	case appsMsg:
		if msg.seq == m.seq && m.screen == screenApps {
			m.loading, m.apps, m.err = false, msg.apps, msg.err
		}
	case doneMsg:
		if msg.seq == m.seq && m.screen == screenQuarantine {
			m.busy, m.note = false, msg.text
			if msg.err != nil {
				m.note += "\nCould not finish: " + msg.err.Error()
			}
			return m, m.loadQuarantine()
		}
	case tea.KeyMsg:
		return m, m.key(msg)
	}
	return m, nil
}

func (m *model) key(msg tea.KeyMsg) tea.Cmd {
	k := msg.String()
	if msg.Paste {
		k = string(msg.Runes) // pasted text, without the brackets String adds
	}
	if k == "ctrl+c" && !m.busy {
		return tea.Quit
	}
	switch m.screen {
	case screenMenu:
		return m.menuKey(k)
	case screenHelp:
		m.screen = screenMenu // any key goes back
	case screenExplore:
		return m.exploreKey(k)
	case screenPage:
		m.pageKey(k)
	case screenQuarantine:
		return m.quarantineKey(k)
	case screenTools:
		return m.toolsKey(k)
	case screenFolders:
		return m.foldersKey(msg, k)
	case screenApps:
		return m.appsKey(msg, k)
	}
	return nil
}

// back returns to the menu. Answers still on their way are dropped.
func (m *model) back() {
	m.screen = screenMenu
	m.seq++
	m.offset, m.sub, m.err = 0, 0, nil
	m.loading, m.confirming, m.note = false, false, ""
}

func (m *model) menuKey(k string) tea.Cmd {
	m.notice = ""
	switch k {
	case "q":
		return tea.Quit
	case "?":
		m.screen = screenHelp
	case "up", "k":
		m.cursor = max(m.cursor-1, 0)
	case "down", "j":
		m.cursor = min(m.cursor+1, len(choices)-1)
	case "enter":
		return m.open(m.cursor)
	default:
		if len(k) == 1 && k[0] >= '1' && k[0] <= '7' {
			m.cursor = int(k[0] - '1')
			return m.open(m.cursor)
		}
	}
	return nil
}

// open opens choice i. Scan & review leaves the menu for the review screen;
// nothing here moves a file.
func (m *model) open(i int) tea.Cmd {
	m.seq++
	m.offset, m.sub, m.err = 0, 0, nil
	switch i {
	case 0:
		m.launch = launchReview
		return tea.Quit
	case 1:
		m.screen = screenExplore
	case 2:
		m.screen = screenTools
		m.tools, m.err = m.b.Tools()
		m.toolCursor = min(m.toolCursor, max(len(m.tools)-1, 0))
	case 3:
		m.screen = screenQuarantine
		m.loading = true
		return m.loadQuarantine()
	case 4:
		return m.showPage("history", "Reading the history...", m.b.History)
	case 5:
		return m.showPage("system check", "Checking...", m.b.Doctor)
	case 6:
		return m.showPage("settings", "Reading the settings...", func(ctx context.Context) (string, error) {
			text, err := m.b.Settings(ctx)
			return text + settingsCommands, err
		})
	}
	return nil
}

// showPage opens a text page. With load set, the page shows wait until the
// text arrives.
func (m *model) showPage(title, wait string, load func(context.Context) (string, error)) tea.Cmd {
	m.screen, m.title, m.text = screenPage, title, wait
	if load == nil {
		m.loading = false
		return nil
	}
	m.loading = true
	seq := m.seq
	return func() tea.Msg {
		text, err := load(m.ctx)
		return pageMsg{seq: seq, text: text, err: err}
	}
}

func (m *model) exploreKey(k string) tea.Cmd {
	switch k {
	case "esc", "q", "left", "h":
		m.back()
	case "up", "k":
		m.sub = max(m.sub-1, 0)
	case "down", "j":
		m.sub = min(m.sub+1, len(exploreChoices)-1)
	case "1", "2":
		m.sub = int(k[0] - '1')
		return m.exploreOpen()
	case "enter":
		return m.exploreOpen()
	}
	return nil
}

func (m *model) exploreOpen() tea.Cmd {
	if m.sub == 0 {
		m.launch = launchAnalyze
		return tea.Quit
	}
	m.seq++
	return m.showPage("system data", "Measuring System Data (a folder can take up to 20 seconds)...", m.b.Explain)
}

func (m *model) pageKey(k string) {
	switch k {
	case "esc", "q", "left", "h":
		m.back()
	case "up", "k":
		m.offset = max(m.offset-1, 0)
	case "down", "j":
		m.offset++ // the view limits it
	case "pgup":
		m.offset = max(m.offset-m.bodyHeight(), 0)
	case "pgdown":
		m.offset += m.bodyHeight()
	case "home", "g":
		m.offset = 0
	case "end", "G":
		m.offset = 1 << 30
	}
}

func (m *model) loadQuarantine() tea.Cmd {
	seq := m.seq
	return func() tea.Msg {
		q, err := m.b.Quarantine()
		return quarantineMsg{seq: seq, q: q, err: err}
	}
}

// quarantineKey: enter asks to restore, d to delete the run for good, D to
// delete every run for good. Only y on that question does it.
func (m *model) quarantineKey(k string) tea.Cmd {
	if m.busy {
		return nil
	}
	if m.confirming {
		switch k {
		case "y", "Y":
			return m.do()
		case "n", "N", "esc":
			m.confirming = false
		}
		return nil
	}
	switch k {
	case "esc", "q", "left", "h":
		m.back()
	case "up", "k":
		m.sub = max(m.sub-1, 0)
	case "down", "j":
		m.sub = min(m.sub+1, max(len(m.q.Runs)-1, 0))
	case "enter":
		m.askAbout(askRestore)
	case "d":
		m.askAbout(askDelete)
	case "D":
		m.askAbout(askDeleteAll)
	}
	return nil
}

func (m *model) askAbout(a ask) {
	if len(m.q.Runs) > 0 && !m.loading && m.err == nil {
		m.confirming, m.ask, m.note = true, a, ""
	}
}

// do restores or deletes what the confirmation asked about. It is reached
// only from y on the confirmation.
func (m *model) do() tea.Cmd {
	var ids []string
	if m.ask == askDeleteAll {
		for _, r := range m.q.Runs {
			ids = append(ids, r.ID)
		}
	} else {
		ids = []string{m.q.Runs[m.sub].ID}
	}
	a := m.ask
	m.confirming, m.busy = false, true
	seq := m.seq
	return func() tea.Msg {
		var text string
		var err error
		if a == askRestore {
			text, err = m.b.Restore(ids[0])
		} else {
			text, err = m.b.Delete(ids)
		}
		return doneMsg{seq: seq, text: text, err: err}
	}
}

// ---- tools ----

// toolKeys are the keys that open a tool directly: 1 to 9, then 0.
const toolKeys = "1234567890"

func (m *model) toolsKey(k string) tea.Cmd {
	switch k {
	case "esc", "q", "left", "h":
		m.back()
	case "up", "k":
		m.toolCursor = max(m.toolCursor-1, 0)
	case "down", "j":
		m.toolCursor = min(m.toolCursor+1, max(len(m.tools)-1, 0))
	case "enter":
		return m.openTool()
	default:
		if i := strings.Index(toolKeys, k); len(k) == 1 && i >= 0 && i < len(m.tools) {
			m.toolCursor = i
			return m.openTool()
		}
	}
	return nil
}

// openTool opens the tool under the cursor: where it looks, the app picker,
// or, for a tool with nothing to choose, the review screen.
func (m *model) openTool() tea.Cmd {
	if len(m.tools) == 0 {
		return nil
	}
	t := m.tools[m.toolCursor]
	m.seq++
	m.offset, m.err = 0, nil
	switch {
	case t.Folders:
		m.screen = screenFolders
		m.folder, m.folderPath = "", ""
		m.editing, m.input, m.inputErr = false, "", ""
		return nil
	case t.App:
		m.screen = screenApps
		m.apps, m.appFilter, m.sub = nil, "", 0
		m.loading = true
		seq := m.seq
		return func() tea.Msg {
			apps, err := m.b.Apps(m.ctx)
			return appsMsg{seq: seq, apps: apps, err: err}
		}
	}
	return m.runTool(nil, "")
}

// runTool leaves the menu to run the tool under the cursor. The review
// screen it opens asks before anything moves.
func (m *model) runTool(dirs []string, app string) tea.Cmd {
	m.launch = launchTool
	m.run = toolRun{id: m.tools[m.toolCursor].ID, dirs: dirs, app: app}
	return tea.Quit
}

// backToTools leaves a tool's screen for the list. Answers still on their
// way are dropped.
func (m *model) backToTools() {
	m.screen = screenTools
	m.seq++
	m.offset, m.sub, m.err = 0, 0, nil
	m.loading, m.editing, m.input, m.inputErr = false, false, "", ""
}

// foldersKey: enter looks, f types another folder, esc goes back.
func (m *model) foldersKey(msg tea.KeyMsg, k string) tea.Cmd {
	if m.editing {
		switch k {
		case "esc":
			m.editing, m.input, m.inputErr = false, "", ""
		case "enter":
			m.useFolder()
		case "backspace":
			r := []rune(m.input)
			m.input = string(r[:max(len(r)-1, 0)])
			m.inputErr = ""
		case "ctrl+u":
			m.input, m.inputErr = "", ""
		default:
			if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
				m.input += string(msg.Runes)
				m.inputErr = ""
			}
		}
		return nil
	}
	switch k {
	case "esc", "q", "left", "h":
		m.backToTools()
	case "f":
		m.editing, m.input, m.inputErr = true, m.folder, ""
	case "enter":
		if m.folderPath == "" {
			return m.runTool(nil, "")
		}
		return m.runTool([]string{m.folderPath}, "")
	}
	return nil
}

// useFolder checks the typed folder. A folder that is not there stays in
// the input with the reason.
func (m *model) useFolder() {
	typed := strings.TrimSpace(m.input)
	if typed == "" {
		m.inputErr = "Type the path of a folder, or press esc to keep the default folders."
		return
	}
	p, err := m.b.Folder(typed)
	if err != nil {
		m.inputErr = "Cannot use " + typed + ": " + err.Error() + "."
		return
	}
	m.folder, m.folderPath = typed, p
	m.editing, m.input, m.inputErr = false, "", ""
}

// appsKey: typing filters the list, up/down moves, enter picks, esc clears
// the filter and then goes back.
func (m *model) appsKey(msg tea.KeyMsg, k string) tea.Cmd {
	shown := m.shownApps()
	switch k {
	case "esc":
		if m.appFilter != "" {
			m.appFilter, m.sub = "", 0
			return nil
		}
		m.backToTools()
	case "up":
		m.sub = max(m.sub-1, 0)
	case "down":
		m.sub = min(m.sub+1, max(len(shown)-1, 0))
	case "enter":
		if len(shown) > 0 && !m.loading {
			return m.runTool(nil, shown[m.sub].Path)
		}
	case "backspace":
		r := []rune(m.appFilter)
		m.appFilter, m.sub = string(r[:max(len(r)-1, 0)]), 0
	case "/":
		// The list filters as you type; / is only a habit from other screens.
	default:
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			m.appFilter += string(msg.Runes)
			m.sub = 0
		}
	}
	return nil
}

// shownApps are the apps that match the filter, in name or path.
func (m *model) shownApps() []App {
	f := strings.ToLower(strings.TrimSpace(m.appFilter))
	var out []App
	for _, a := range m.apps {
		if f == "" || strings.Contains(strings.ToLower(a.Name), f) || strings.Contains(strings.ToLower(a.Path), f) {
			out = append(out, a)
		}
	}
	return out
}

// bodyHeight is the lines a page shows between its header and footer.
func (m *model) bodyHeight() int { return max(m.height-4, 3) }

func lines(text string) []string { return strings.Split(strings.TrimRight(text, "\n"), "\n") }
