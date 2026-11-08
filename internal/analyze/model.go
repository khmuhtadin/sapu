package analyze

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/walk"
)

const explainText = "Chosen by you in sapu analyze"

type screen int

const (
	screenScanning screen = iota
	screenBrowsing
	screenPlanning
	screenReviewing
	screenApplying
	screenResult
)

// Messages.
type (
	progressMsg walk.Event
	scanDoneMsg struct {
		root *walk.Node
		err  error
	}
	planMsg struct {
		plan *plan.Plan
		err  error
	}
	applyMsg struct {
		result *engine.Result
		err    error
	}
)

// level is one folder on the way down from the root, and the cursor in it.
type level struct {
	node   *walk.Node
	cursor int
	offset int // first visible row
}

type model struct {
	ctx  context.Context
	env  env.Env
	root string
	cfg  config.Config // quarantine settings: max_age and max_size

	// The two engine calls. Run sets them to engine.Plan and engine.Apply;
	// tests swap in fakes.
	planFn  func(ctx context.Context, e env.Env, kind string, items []plan.Item) (*plan.Plan, error)
	applyFn func(ctx context.Context, e env.Env, p *plan.Plan, opts engine.Options) (*engine.Result, error)

	width, height int
	screen        screen
	help          bool
	status        string // one line under the list: hints and errors
	fatal         error  // the scan failed and there is nothing to show

	// scanning
	spin       spinner.Model
	progress   walk.Event
	progressCh chan walk.Event
	doneCh     chan scanDoneMsg
	stopScan   context.CancelFunc
	scanCtx    context.Context
	partial    bool // the scan was stopped before it finished

	// browsing
	tree   *walk.Node
	trail  []level               // tree root first, current folder last
	marked map[string]*walk.Node // by path; never nested inside each other

	// review and result
	dropped []plan.Skip // marked entries that vanished before planning
	plan    *plan.Plan
	result  *engine.Result
	failure string // why planning or applying failed
}

func newModel(ctx context.Context, e env.Env, root string) *model {
	scanCtx, stop := context.WithCancel(ctx)
	return &model{
		ctx:        ctx,
		env:        e,
		root:       root,
		cfg:        config.Defaults(),
		width:      80,
		height:     24,
		spin:       spinner.New(spinner.WithSpinner(spinner.Dot)),
		progressCh: make(chan walk.Event, 16),
		doneCh:     make(chan scanDoneMsg, 1),
		scanCtx:    scanCtx,
		stopScan:   stop,
		marked:     map[string]*walk.Node{},
	}
}

// Init starts the scan in the background.
func (m *model) Init() tea.Cmd {
	go func() {
		tree, err := walk.Tree(m.scanCtx, m.root, func(ev walk.Event) {
			select {
			case m.progressCh <- ev:
			default: // totals are running totals; the next event replaces this one
			}
		})
		m.doneCh <- scanDoneMsg{root: tree, err: err}
	}()
	return tea.Batch(m.spin.Tick, m.wait)
}

// wait returns the next message from the scan.
func (m *model) wait() tea.Msg {
	select {
	case ev := <-m.progressCh:
		return progressMsg(ev)
	case done := <-m.doneCh:
		return done
	}
}

// setTree shows a measured tree. partial marks a scan that was stopped.
func (m *model) setTree(root *walk.Node, partial bool) {
	m.tree = root
	m.partial = partial
	m.trail = []level{{node: root}}
	m.screen = screenBrowsing
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.keepCursorVisible()
	case spinner.TickMsg:
		if m.screen == screenScanning {
			var cmd tea.Cmd
			m.spin, cmd = m.spin.Update(msg)
			return m, cmd
		}
	case progressMsg:
		if m.screen == screenScanning {
			m.progress = walk.Event(msg)
			return m, m.wait
		}
	case scanDoneMsg:
		if msg.root == nil {
			m.fatal = msg.err
			return m, tea.Quit
		}
		m.setTree(msg.root, msg.err != nil)
	case planMsg:
		m.planned(msg)
	case applyMsg:
		m.applied(msg)
	case tea.KeyMsg:
		return m, m.key(msg)
	}
	return m, nil
}

func (m *model) key(msg tea.KeyMsg) tea.Cmd {
	k := msg.String()
	if k == "ctrl+c" {
		if m.screen == screenApplying {
			// Moves are in progress; quitting now would stop them halfway.
			m.status = "Moving to quarantine; this finishes in a moment."
			return nil
		}
		m.stopScan()
		return tea.Quit
	}
	switch m.screen {
	case screenScanning:
		if k == "esc" || k == "q" {
			m.stopScan() // the walk returns what it has; scanDoneMsg follows
		}
	case screenBrowsing:
		return m.browseKey(k)
	case screenReviewing:
		return m.reviewKey(k)
	case screenResult:
		m.screen = screenBrowsing
		m.status = ""
	case screenPlanning, screenApplying:
		// keys do nothing while the work runs
	}
	return nil
}

// ---- browsing ----

func (m *model) current() *level { return &m.trail[len(m.trail)-1] }

func (m *model) browseKey(k string) tea.Cmd {
	if m.help {
		m.help = false
		return nil
	}
	m.status = ""
	cur := m.current()
	last := len(cur.node.Children) - 1
	switch k {
	case "q":
		m.stopScan()
		return tea.Quit
	case "?":
		m.help = true
	case "up", "k":
		cur.cursor = max(cur.cursor-1, 0)
	case "down", "j":
		cur.cursor = min(cur.cursor+1, max(last, 0))
	case "pgup":
		cur.cursor = max(cur.cursor-m.rows(), 0)
	case "pgdown":
		cur.cursor = min(cur.cursor+m.rows(), max(last, 0))
	case "home":
		cur.cursor = 0
	case "end":
		cur.cursor = max(last, 0)
	case "enter", "right", "l":
		if n := m.selected(); n != nil && n.IsDir {
			m.trail = append(m.trail, level{node: n})
		}
	case "backspace", "left", "h":
		if len(m.trail) > 1 {
			m.trail = m.trail[:len(m.trail)-1]
		}
	case " ":
		m.toggleMark()
	case "x":
		return m.review()
	}
	m.keepCursorVisible()
	return nil
}

// selected returns the entry under the cursor, or nil in an empty folder.
func (m *model) selected() *walk.Node {
	cur := m.current()
	if cur.cursor >= len(cur.node.Children) {
		return nil
	}
	return cur.node.Children[cur.cursor]
}

// rows is how many entries fit on screen.
func (m *model) rows() int { return max(m.height-chromeLines, 1) }

func (m *model) keepCursorVisible() {
	if len(m.trail) == 0 {
		return
	}
	cur := m.current()
	if cur.cursor < cur.offset {
		cur.offset = cur.cursor
	}
	if cur.cursor >= cur.offset+m.rows() {
		cur.offset = cur.cursor - m.rows() + 1
	}
}

// toggleMark marks or unmarks the entry under the cursor. Marks never nest:
// an entry inside a marked folder is already covered, and marking a folder
// drops the marks inside it.
func (m *model) toggleMark() {
	n := m.selected()
	if n == nil {
		return
	}
	if m.marked[n.Path] != nil {
		delete(m.marked, n.Path)
		return
	}
	for p := range m.marked {
		if inside(n.Path, p) {
			m.status = "Already covered by marked " + m.short(p)
			return
		}
	}
	for p := range m.marked {
		if inside(p, n.Path) {
			delete(m.marked, p)
		}
	}
	m.marked[n.Path] = n
}

// inside reports whether path is below dir.
func inside(path, dir string) bool { return strings.HasPrefix(path, dir+"/") }

func (m *model) markedTotal() (count int, bytes int64) {
	for _, n := range m.marked {
		count++
		bytes += n.Bytes
	}
	return count, bytes
}

// ---- review ----

// review builds plan items from the marked entries and asks the engine for a
// plan. The engine writes nothing.
func (m *model) review() tea.Cmd {
	if len(m.marked) == 0 {
		m.status = "Nothing marked. Press space on an entry first."
		return nil
	}
	m.dropped = nil
	var items []plan.Item
	for _, n := range m.sortedMarks() {
		id, err := plan.IdentityOf(n.Path)
		if err != nil {
			m.dropped = append(m.dropped, plan.Skip{Path: n.Path, Source: "analyze", Reason: plan.ReasonGone, Detail: err.Error()})
			if errors.Is(err, os.ErrNotExist) {
				m.remove(n.Path)
			}
			delete(m.marked, n.Path)
			continue
		}
		items = append(items, plan.Item{
			Source:      "analyze",
			Path:        n.Path,
			Bytes:       n.Bytes,
			Risk:        rules.RiskRisky,
			Regenerates: false,
			Explain:     explainText,
			Action:      rules.ActionQuarantine,
			Identity:    id,
		})
	}
	m.keepCursorVisible()
	if len(items) == 0 {
		m.status = "The marked entries are gone."
		return nil
	}
	m.screen = screenPlanning
	return func() tea.Msg {
		p, err := m.planFn(m.ctx, m.env, plan.KindAnalyze, items)
		return planMsg{plan: p, err: err}
	}
}

func (m *model) sortedMarks() []*walk.Node {
	nodes := make([]*walk.Node, 0, len(m.marked))
	for _, n := range m.marked {
		nodes = append(nodes, n)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Path < nodes[j].Path })
	return nodes
}

func (m *model) planned(msg planMsg) {
	if msg.err != nil {
		m.screen = screenBrowsing
		m.status = "Could not build the plan: " + msg.err.Error()
		return
	}
	m.plan = msg.plan
	m.screen = screenReviewing
}

func (m *model) reviewKey(k string) tea.Cmd {
	switch k {
	case "n", "esc":
		m.screen = screenBrowsing
		m.plan = nil
	case "y":
		if len(m.plan.Items) == 0 {
			return nil // nothing to confirm
		}
		// The user chose each item, so all of them are selected.
		// engine.Plan leaves risky items unselected.
		p := m.plan
		for i := range p.Items {
			p.Items[i].Selected = true
		}
		m.screen = screenApplying
		return func() tea.Msg {
			opts := engine.DefaultOptions()
			opts.MaxAge, opts.MaxBytes = m.cfg.MaxAge, m.cfg.MaxBytes
			res, err := m.applyFn(m.ctx, m.env, p, opts)
			return applyMsg{result: res, err: err}
		}
	}
	return nil
}

func (m *model) applied(msg applyMsg) {
	m.screen = screenResult
	m.plan = nil
	m.result = msg.result
	m.failure = ""
	if msg.err != nil {
		m.failure = msg.err.Error()
	}
	if msg.result == nil {
		return
	}
	for _, it := range msg.result.Moved {
		m.remove(it.Path)
		delete(m.marked, it.Path)
	}
	m.keepCursorVisible()
}

// ---- tree updates ----

// remove takes the entry at path out of the tree and subtracts its size from
// every folder above it.
func (m *model) remove(path string) {
	removeFrom(m.tree, path)
	for i := range m.trail {
		n := m.trail[i].node
		m.trail[i].cursor = min(m.trail[i].cursor, max(len(n.Children)-1, 0))
	}
}

// removeFrom looks for path under node. It returns the removed entry, or nil.
func removeFrom(node *walk.Node, path string) *walk.Node {
	for i, c := range node.Children {
		var gone *walk.Node
		switch {
		case c.Path == path:
			gone = c
			node.Children = append(node.Children[:i], node.Children[i+1:]...)
		case c.IsDir && inside(path, c.Path):
			gone = removeFrom(c, path)
		}
		if gone != nil {
			node.Bytes -= gone.Bytes
			node.Files -= gone.Files
			return gone
		}
	}
	return nil
}
