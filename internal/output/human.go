package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/walk"
)

// Bytes formats n the way Finder does: base 1000, one decimal from KB up.
// A negative n (not measured) prints as "—".
func Bytes(n int64) string {
	if n < 0 {
		return "—"
	}
	if n < 1000 {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n)
	for _, unit := range []string{"KB", "MB", "GB", "TB", "PB"} {
		v /= 1000
		if v < 999.95 {
			return fmt.Sprintf("%.1f %s", v, unit)
		}
	}
	return fmt.Sprintf("%.1f EB", v/1000)
}

// IsTerminal reports whether f is a terminal.
func IsTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// ProgressEvent is one NDJSON line written with --progress ndjson.
type ProgressEvent struct {
	Type  string `json:"type"`  // always "progress"
	Phase string `json:"phase"` // "scan", "apply" or "explain"
	Files int64  `json:"files,omitempty"`
	Bytes int64  `json:"bytes,omitempty"`
	Done  int    `json:"done,omitempty"`
	Total int    `json:"total,omitempty"`
	Path  string `json:"path,omitempty"`
	Text  string `json:"text,omitempty"` // Status only
}

// Progress reports long operations on stderr: one updating line on a
// terminal, NDJSON lines with --progress ndjson, nothing otherwise.
// It is safe to call from several goroutines.
type Progress struct {
	w      io.Writer
	ndjson bool
	line   bool
	mu     sync.Mutex
	shown  int // width of the line on screen, to clear it
}

func NewProgress(w io.Writer, ndjson, terminal bool) *Progress {
	return &Progress{w: w, ndjson: ndjson, line: terminal && !ndjson}
}

// Scan reports scanning progress (walk totals).
func (p *Progress) Scan(ev walk.Event) {
	p.emit(ProgressEvent{Type: "progress", Phase: "scan", Files: ev.Files, Bytes: ev.Bytes, Path: ev.Path},
		fmt.Sprintf("Scanning… %d files, %s", ev.Files, Bytes(ev.Bytes)))
}

// Apply reports moving progress (engine.Options.Progress).
func (p *Progress) Apply(done, total int, it plan.Item) {
	p.emit(ProgressEvent{Type: "progress", Phase: "apply", Done: done, Total: total, Path: it.Path},
		fmt.Sprintf("Moving to quarantine… %d of %d", done, total))
}

// Status reports one step that has no counters, such as "Measuring System Data…".
func (p *Progress) Status(phase, text string) {
	p.emit(ProgressEvent{Type: "progress", Phase: phase, Text: text}, text)
}

// Done clears the progress line.
func (p *Progress) Done() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.line && p.shown > 0 {
		fmt.Fprint(p.w, "\r"+strings.Repeat(" ", p.shown)+"\r")
		p.shown = 0
	}
}

func (p *Progress) emit(ev ProgressEvent, text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.ndjson:
		b, err := json.Marshal(ev)
		if err == nil {
			fmt.Fprintf(p.w, "%s\n", b)
		}
	case p.line:
		pad := max(p.shown-len([]rune(text)), 0)
		fmt.Fprint(p.w, "\r"+text+strings.Repeat(" ", pad))
		p.shown = len([]rune(text))
	}
}
