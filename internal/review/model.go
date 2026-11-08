package review

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/walk"
)

type screen int

const (
	screenScanning screen = iota
	screenList
	screenConfirm
	screenApplying
	screenReceipt
)

// Messages.
type (
	scanMsg walk.Event
	planMsg struct {
		plan *plan.Plan
		err  error
	}
	progressMsg struct{ done int }
	applyMsg    struct{}
)

type applyFunc func(ctx context.Context, e env.Env, p *plan.Plan, opts engine.Options) (*engine.Result, error)

// applyRun is one call of applyFn. done is closed when it returns; result
// and err are set before that.
type applyRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	start  time.Time
	result *engine.Result
	err    error
}

// risks are the groups, in the order they are shown.
var risks = []rules.Risk{rules.RiskSafe, rules.RiskModerate, rules.RiskRisky}

// maxWidth is the widest the screen is drawn.
const maxWidth = 100

// maxShown is how many rows a group or the skipped list shows before
// "… N more".
const maxShown = 5

type group struct {
	risk    rules.Risk
	items   []int // indexes into plan.Items, largest first
	open    bool
	showAll bool
}

type rowKind int

const (
	rowGroup    rowKind = iota // a group heading
	rowItem                    // an item of a group
	rowMore                    // "… N more" in a group
	rowSkip                    // a skipped path
	rowSkipMore                // "… N more" in the skipped list
	rowCommand                 // an official command the user may run
)

// row is one line the cursor can stop on.
type row struct {
	kind  rowKind
	group int // rowGroup, rowItem, rowMore
	index int // rowItem and rowCommand: plan item; rowSkip: plan skip
}

type model struct {
	ctx     context.Context
	env     env.Env
	planFn  PlanFunc
	applyFn applyFunc
	cfg     config.Config // quarantine settings: max_age and max_size
	why     func(detail string) string
	now     func() time.Time

	width, height int
	screen        screen
	help          bool
	status        string // one line above the footer: hints and warnings
	cancelled     bool   // Ctrl-C ended the screen or stopped the move
	fatal         error  // the scan or the plan failed

	// scanning. The spinner is a three-dot cue next to the status line; with
	// motion off (NO_COLOR) it never ticks.
	motion   bool
	spin     spinner.Model
	scanned  walk.Event
	scanCh   chan walk.Event
	planCh   chan planMsg
	scanCtx  context.Context
	stopScan context.CancelFunc

	// list
	plan      *plan.Plan
	groups    []group
	commands  []int        // official-command items: listed, never selected
	cursor    int          // index into rows()
	offset    int          // first body line on screen
	pinned    map[int]bool // items whose details stay open
	armed     int          // risky item waiting for a second space; -1 for none
	filter    string
	filtering bool
	allSkips  bool

	// applying and receipt
	todo     []plan.Item // what Apply moves, in its order
	done     int         // items Apply has finished
	progCh   chan progressMsg
	run      *applyRun
	stopping bool
	elapsed  time.Duration
}

func newModel(ctx context.Context, e env.Env, planFn PlanFunc, applyFn applyFunc, why func(string) string) *model {
	scanCtx, stop := context.WithCancel(ctx)
	return &model{
		ctx:      ctx,
		env:      e,
		planFn:   planFn,
		applyFn:  applyFn,
		cfg:      config.Defaults(),
		why:      why,
		now:      time.Now,
		width:    80,
		height:   24,
		motion:   os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb",
		spin:     spinner.New(spinner.WithSpinner(cue)),
		scanCh:   make(chan walk.Event, 16),
		planCh:   make(chan planMsg, 1),
		scanCtx:  scanCtx,
		stopScan: stop,
		pinned:   map[int]bool{},
		armed:    -1,
		progCh:   make(chan progressMsg, 64),
	}
}

// cue is the progress cue: dots that fill in place, three cells wide.
var cue = spinner.Spinner{Frames: []string{".  ", ".. ", "..."}, FPS: time.Second / 3}

// Init starts the scan in the background.
func (m *model) Init() tea.Cmd {
	go func() {
		p, err := m.planFn(m.scanCtx, func(ev walk.Event) {
			select {
			case m.scanCh <- ev:
			default: // totals are running totals; the next event replaces this one
			}
		})
		m.planCh <- planMsg{plan: p, err: err}
	}()
	if !m.motion {
		return m.waitScan
	}
	return tea.Batch(m.spin.Tick, m.waitScan)
}

func (m *model) waitScan() tea.Msg {
	select {
	case ev := <-m.scanCh:
		return scanMsg(ev)
	case msg := <-m.planCh:
		return msg
	}
}

// setPlan shows a plan. Items the screen does not list (official commands,
// unknown risks or actions) are unselected, so Apply can never get them.
func (m *model) setPlan(p *plan.Plan) {
	m.plan = p
	m.groups = nil
	m.commands = nil
	listed := map[int]bool{}
	for _, r := range risks {
		g := group{risk: r, open: r != rules.RiskRisky}
		for i, it := range p.Items {
			if it.Risk == r && it.Action == rules.ActionQuarantine {
				g.items = append(g.items, i)
				listed[i] = true
				// --risk risky selected some: show them, never hide a selection.
				g.open = g.open || it.Selected
			}
		}
		slices.SortStableFunc(g.items, func(a, b int) int {
			return cmp.Or(cmp.Compare(p.Items[b].Bytes, p.Items[a].Bytes), cmp.Compare(p.Items[a].Path, p.Items[b].Path))
		})
		m.groups = append(m.groups, g)
	}
	for i := range p.Items {
		if p.Items[i].Action == rules.ActionOfficialCommand {
			m.commands = append(m.commands, i)
		}
		if !listed[i] {
			p.Items[i].Selected = false
		}
	}
	m.screen = screenList
	m.cursor, m.offset = 0, 0
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Wider than maxWidth only spreads the columns apart.
		m.width, m.height = min(msg.Width, maxWidth), msg.Height
	case spinner.TickMsg:
		if m.screen == screenScanning {
			var cmd tea.Cmd
			m.spin, cmd = m.spin.Update(msg)
			return m, cmd
		}
	case scanMsg:
		if m.screen == screenScanning {
			m.scanned = walk.Event(msg)
			return m, m.waitScan
		}
	case planMsg:
		if msg.err != nil {
			m.fatal = msg.err
			return m, tea.Quit
		}
		m.setPlan(msg.plan)
	case progressMsg:
		if m.screen == screenApplying {
			m.done = max(m.done, msg.done)
			return m, m.waitApply
		}
	case applyMsg:
		m.applied()
	case tea.KeyMsg:
		cmd := m.key(msg)
		m.scroll()
		return m, cmd
	}
	m.scroll()
	return m, nil
}

func (m *model) key(msg tea.KeyMsg) tea.Cmd {
	k := msg.String()
	if k == "ctrl+c" {
		if m.screen == screenApplying {
			// Never quit mid-item: cancel, and the engine stops between items.
			if !m.stopping {
				m.stopping = true
				m.cancelled = true
				m.run.cancel()
			}
			return nil
		}
		m.cancelled = m.screen != screenReceipt
		m.stopScan()
		return tea.Quit
	}
	switch m.screen {
	case screenScanning:
		if k == "q" || k == "esc" {
			m.stopScan()
			return tea.Quit
		}
	case screenList:
		return m.listKey(msg)
	case screenConfirm:
		return m.confirmKey(k)
	case screenApplying:
		// Only ctrl+c, above, works while items move.
	case screenReceipt:
		if k == "q" || k == "esc" || k == "enter" {
			return tea.Quit
		}
	}
	return nil
}

// ---- list ----

func (m *model) listKey(msg tea.KeyMsg) tea.Cmd {
	k := msg.String()
	if m.help {
		m.help = false
		return nil
	}
	if m.filtering {
		m.filterKey(msg)
		return nil
	}
	if k != " " {
		m.armed = -1
	}
	m.status = ""
	rs := m.rows()
	last := max(len(rs)-1, 0)
	switch k {
	case "q":
		return tea.Quit
	case "?":
		m.help = true
	case "up", "k":
		m.cursor = max(m.cursor-1, 0)
	case "down", "j":
		m.cursor = min(m.cursor+1, last)
	case "pgup":
		m.cursor = max(m.cursor-m.bodyHeight(), 0)
	case "pgdown":
		m.cursor = min(m.cursor+m.bodyHeight(), last)
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		m.cursor = last
	case " ":
		m.toggle()
	case "right", "l":
		m.expand()
	case "left", "h":
		m.collapse()
	case "enter":
		if m.enterExpands() {
			m.expand()
			return nil
		}
		m.toConfirm()
	case "a":
		m.selectSafe()
	case "/":
		m.filtering = true
	case "esc":
		if m.filter != "" {
			m.filter = ""
			m.cursor = 0
		}
	}
	return nil
}

// filterKey edits the filter. Enter keeps it, esc clears it.
func (m *model) filterKey(msg tea.KeyMsg) {
	switch msg.String() {
	case "enter":
		m.filtering = false
	case "esc":
		m.filtering = false
		m.filter = ""
	case "backspace":
		if r := []rune(m.filter); len(r) > 0 {
			m.filter = string(r[:len(r)-1])
		}
	default:
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			m.filter += clean(string(msg.Runes))
		}
	}
	m.cursor = 0
}

// current returns the row under the cursor; ok is false on an empty list.
func (m *model) current() (row, bool) {
	rs := m.rows()
	if len(rs) == 0 {
		return row{}, false
	}
	m.cursor = min(max(m.cursor, 0), len(rs)-1)
	return rs[m.cursor], true
}

// rows lists the lines the cursor can stop on, in screen order.
func (m *model) rows() []row {
	var rs []row
	for gi, g := range m.groups {
		vis := m.visible(g)
		if len(vis) == 0 {
			continue
		}
		rs = append(rs, row{kind: rowGroup, group: gi})
		if !g.open {
			continue
		}
		shown := vis
		if !g.showAll && len(vis) > maxShown+1 {
			shown = vis[:maxShown]
		}
		for _, i := range shown {
			rs = append(rs, row{kind: rowItem, group: gi, index: i})
		}
		if len(shown) < len(vis) {
			rs = append(rs, row{kind: rowMore, group: gi})
		}
	}
	skips := m.visibleSkips()
	shown := skips
	if !m.allSkips && len(skips) > maxShown+1 {
		shown = skips[:maxShown]
	}
	for _, i := range shown {
		rs = append(rs, row{kind: rowSkip, index: i})
	}
	if len(shown) < len(skips) {
		rs = append(rs, row{kind: rowSkipMore})
	}
	for _, i := range m.commands {
		if m.matches(m.plan.Items[i].Source, strings.Join(m.plan.Items[i].Command, " ")) {
			rs = append(rs, row{kind: rowCommand, index: i})
		}
	}
	return rs
}

// visible returns the items of g that match the filter.
func (m *model) visible(g group) []int {
	var out []int
	for _, i := range g.items {
		it := m.plan.Items[i]
		if m.matches(itemName(it), it.Source, m.short(it.Path)) {
			out = append(out, i)
		}
	}
	return out
}

func (m *model) visibleSkips() []int {
	var out []int
	for i, s := range m.plan.Skipped {
		if m.matches(s.Source, s.Reason, m.short(s.Path)) {
			out = append(out, i)
		}
	}
	return out
}

// matches reports whether any of the texts holds the filter, ignoring case.
func (m *model) matches(texts ...string) bool {
	f := strings.ToLower(m.filter)
	for _, t := range texts {
		if strings.Contains(strings.ToLower(t), f) {
			return true
		}
	}
	return false
}

// toggle is space: select or unselect the row under the cursor.
func (m *model) toggle() {
	r, ok := m.current()
	if !ok {
		return
	}
	switch r.kind {
	case rowGroup:
		m.toggleGroup(r.group)
	case rowItem:
		m.toggleItem(r.index)
	case rowMore, rowSkipMore:
		m.expand()
	case rowSkip:
		m.status = "Skipped items stay where they are. Sapu does not move them."
	case rowCommand:
		m.status = "Sapu does not run commands. Type it in Terminal yourself if you want it."
	}
}

func (m *model) toggleItem(i int) {
	it := &m.plan.Items[i]
	switch {
	case it.Selected:
		it.Selected = false
	case it.Risk == rules.RiskRisky && m.armed != i:
		// First space: the details below the item say why it is risky.
		m.armed = i
		return
	default:
		it.Selected = true
	}
	m.armed = -1
}

// toggleGroup selects every visible item of a group, or unselects them when
// all are selected. A RISKY group is never selected in bulk.
func (m *model) toggleGroup(gi int) {
	g := &m.groups[gi]
	vis := m.visible(*g)
	n := 0
	for _, i := range vis {
		if m.plan.Items[i].Selected {
			n++
		}
	}
	switch {
	case g.risk == rules.RiskRisky && n == 0:
		g.open = true
		m.status = "Risky items are picked one at a time: press space on each one."
		return
	case g.risk == rules.RiskRisky || n == len(vis):
		m.setSelected(vis, false)
	default:
		m.setSelected(vis, true)
	}
}

func (m *model) setSelected(items []int, on bool) {
	for _, i := range items {
		m.plan.Items[i].Selected = on
	}
}

// selectSafe is `a`: select every visible SAFE item. Never RISKY.
func (m *model) selectSafe() {
	for _, g := range m.groups {
		if g.risk == rules.RiskSafe {
			vis := m.visible(g)
			m.setSelected(vis, true)
			m.status = fmt.Sprintf("Selected all %s.", plural(len(vis), "safe item"))
		}
	}
}

// expand opens a group, the "… more" rows, or pins an item's details.
func (m *model) expand() {
	r, ok := m.current()
	if !ok {
		return
	}
	switch r.kind {
	case rowGroup:
		m.groups[r.group].open = true
	case rowItem:
		m.pinned[r.index] = true
	case rowMore:
		m.groups[r.group].showAll = true
	case rowSkipMore:
		m.allSkips = true
	case rowSkip, rowCommand:
		// Their details show while the cursor is on them.
	}
}

// collapse closes pinned details, or the group the cursor is in.
func (m *model) collapse() {
	r, ok := m.current()
	if !ok {
		return
	}
	switch {
	case r.kind == rowItem && m.pinned[r.index]:
		delete(m.pinned, r.index)
	case r.kind == rowGroup || r.kind == rowItem || r.kind == rowMore:
		m.groups[r.group].open = false
		for i, x := range m.rows() {
			if x.kind == rowGroup && x.group == r.group {
				m.cursor = i
			}
		}
	}
}

// enterExpands reports whether enter opens the row under the cursor instead
// of going to the confirm screen: a closed group or a "… more" row.
func (m *model) enterExpands() bool {
	r, ok := m.current()
	if !ok {
		return false
	}
	return r.kind == rowMore || r.kind == rowSkipMore || (r.kind == rowGroup && !m.groups[r.group].open)
}

// selection counts the selected items Apply would move.
func (m *model) selection() (count int, bytes int64, byRisk map[rules.Risk]int) {
	byRisk = map[rules.Risk]int{}
	for _, g := range m.groups {
		for _, i := range g.items {
			if it := m.plan.Items[i]; it.Selected {
				count++
				bytes += it.Bytes
				byRisk[it.Risk]++
			}
		}
	}
	return count, bytes, byRisk
}

func (m *model) toConfirm() {
	if n, _, _ := m.selection(); n == 0 {
		m.status = "Nothing selected. Press space on an item, or a for all safe items."
		return
	}
	m.screen = screenConfirm
}

// ---- confirm and apply ----

func (m *model) confirmKey(k string) tea.Cmd {
	switch k {
	case "y", "Y":
		return m.apply()
	case "n", "N", "esc":
		m.screen = screenList
	}
	return nil
}

// selectedPlan is a copy of the plan with only the selected items that the
// list shows, in plan order.
func (m *model) selectedPlan() *plan.Plan {
	listed := map[int]bool{}
	for _, g := range m.groups {
		for _, i := range g.items {
			listed[i] = true
		}
	}
	p := *m.plan
	p.Items = []plan.Item{}
	p.Skipped = slices.Clone(m.plan.Skipped)
	for i, it := range m.plan.Items {
		if listed[i] && it.Selected {
			p.Items = append(p.Items, it)
		}
	}
	return &p
}

// apply starts the move. It is reached only from y on the confirm screen.
func (m *model) apply() tea.Cmd {
	p := m.selectedPlan()
	ctx, cancel := context.WithCancel(m.ctx)
	run := &applyRun{cancel: cancel, done: make(chan struct{}), start: m.now()}
	m.run = run
	m.todo = p.Items
	m.done = 0
	m.screen = screenApplying
	m.status = ""

	opts := engine.DefaultOptions()
	opts.MaxAge, opts.MaxBytes = m.cfg.MaxAge, m.cfg.MaxBytes
	opts.Progress = func(done, total int, _ plan.Item) {
		select {
		case m.progCh <- progressMsg{done: done}:
		default: // done is a running count; the next event replaces this one
		}
	}
	go func() {
		defer close(run.done)
		run.result, run.err = m.applyFn(ctx, m.env, p, opts)
	}()
	return m.waitApply
}

func (m *model) waitApply() tea.Msg {
	select {
	case msg := <-m.progCh:
		return msg
	case <-m.run.done:
		return applyMsg{}
	}
}

func (m *model) applied() {
	m.elapsed = m.now().Sub(m.run.start)
	m.run.cancel()
	if r := m.run.result; r != nil {
		m.done = len(r.Moved) + len(r.Skipped) + len(r.Failed)
	}
	m.screen = screenReceipt
}

// ---- helpers ----

// short writes a path with the home folder as "~", without control characters.
func (m *model) short(path string) string { return clean(tilde(m.env.Home, path)) }

// itemName is the rule id in words ("xcode derived data"), or the folder name.
func itemName(it plan.Item) string {
	if it.Source == "" {
		return clean(it.Path[strings.LastIndex(it.Path, "/")+1:])
	}
	return clean(strings.NewReplacer(".", " ", "-", " ", "_", " ", ":", " ").Replace(it.Source))
}
