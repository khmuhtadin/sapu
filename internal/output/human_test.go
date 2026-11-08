package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/walk"
)

func TestBytes(t *testing.T) {
	cases := map[int64]string{
		-1:            "—",
		0:             "0 B",
		999:           "999 B",
		1000:          "1.0 KB",
		1_500_000:     "1.5 MB",
		999_960:       "1.0 MB",
		3_200_000_000: "3.2 GB",
	}
	for n, want := range cases {
		if got := Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestProgressNDJSON(t *testing.T) {
	var buf bytes.Buffer
	p := NewProgress(&buf, true, true)
	p.Scan(walk.Event{Files: 3, Bytes: 4096, Path: "/x"})
	p.Apply(1, 2, plan.Item{Path: "/y"})
	p.Done()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %q", buf.String())
	}
	var ev ProgressEvent
	if err := json.Unmarshal([]byte(lines[1]), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "progress" || ev.Phase != "apply" || ev.Done != 1 || ev.Total != 2 || ev.Path != "/y" {
		t.Fatalf("unexpected event %+v", ev)
	}
}

func TestProgressLineAndSilent(t *testing.T) {
	var buf bytes.Buffer
	p := NewProgress(&buf, false, true)
	p.Scan(walk.Event{Files: 10, Bytes: 2000})
	p.Done()
	if !strings.Contains(buf.String(), "Scanning… 10 files, 2.0 KB") || !strings.HasSuffix(buf.String(), "\r") {
		t.Fatalf("unexpected line output %q", buf.String())
	}

	buf.Reset()
	quiet := NewProgress(&buf, false, false)
	quiet.Scan(walk.Event{Files: 1})
	quiet.Done()
	if buf.Len() != 0 {
		t.Fatalf("non-terminal progress should be silent, got %q", buf.String())
	}
}
