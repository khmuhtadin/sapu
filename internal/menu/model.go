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
	screenPage // a text page: apps, history, system check, settings, System Data
	screenQuarantine
)

// choices are the menu entries, in order. Keys 1 to 7 open them.
var choices = []string{
	"Scan & review",
	"Explore storage",
	"Apps & build files",
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

	launch launch
	notice string // shown on the menu: what the last screen said
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
		return m.showPage("apps & build files", appsText, nil)
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

// bodyHeight is the lines a page shows between its header and footer.
func (m *model) bodyHeight() int { return max(m.height-4, 3) }

func lines(text string) []string { return strings.Split(strings.TrimRight(text, "\n"), "\n") }
