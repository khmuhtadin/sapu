package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/finders"
)

const finderAttachments = "Library/Messages/Attachments/ab/11/at_0_UUID/"

func messagesHome(t *testing.T) env.Env {
	t.Helper()
	e := env.New(t.TempDir())
	finderPut(t, e, finderAttachments+"holiday.MOV", 300_000, finderLongAgo)
	finderPut(t, e, finderAttachments+"photo.heic", 200_000, finderLongAgo)
	finderPut(t, e, finderAttachments+"recent.mov", 300_000, time.Now())
	finderPut(t, e, finderAttachments+"small.jpg", 1_000, finderLongAgo)
	finderPut(t, e, "Library/Messages/chat.db", 400_000, finderLongAgo)
	return e
}

func TestMessagesListsAndNeverMakesAPlan(t *testing.T) {
	e := messagesHome(t)
	before := snapshot(t, e)

	out, errOut, code := sapu(t, e, "", "messages", "--min-size", "100KB")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	t.Logf("sapu messages\n%s", out)
	checkHuman(t, out)
	if i, j := strings.Index(out, "holiday.MOV"), strings.Index(out, "photo.heic"); i < 0 || j < i {
		t.Errorf("attachments missing or not biggest first:\n%s", out)
	}
	for _, name := range []string{"recent.mov", "small.jpg", "chat.db"} {
		if strings.Contains(out, name) {
			t.Errorf("%s must not be listed:\n%s", name, out)
		}
	}
	flat := finderFlat(out)
	for _, want := range []string{"mov", "heic", "Messages › Settings › General › Keep messages", "System Settings › General › Storage › Messages", "iCloud", "2 attachments"} {
		if !strings.Contains(flat, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "--pick") {
		t.Errorf("messages offers no picking:\n%s", out)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e) // no run, no plan, no quarantine
}

func TestMessagesTakesNoPickPlanOrYes(t *testing.T) {
	e := messagesHome(t)
	before := snapshot(t, e)
	for _, args := range [][]string{
		{"--pick", "1"}, {"--plan", filepath.Join(t.TempDir(), "p.json")}, {"--yes"}, {"--permanent"}, {"extra"},
		{"--min-size", "huge"}, {"--older-than", "ages"},
	} {
		if out, _, code := sapu(t, e, "", append([]string{"messages"}, args...)...); code != 2 {
			t.Errorf("%v: exit %d, want 2\n%s", args, code, out)
		}
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
}

func TestMessagesFiltersAndLimit(t *testing.T) {
	e := messagesHome(t)
	out, _, code := sapu(t, e, "", "messages", "--min-size", "250KB")
	if code != 0 || !strings.Contains(out, "holiday.MOV") || strings.Contains(out, "photo.heic") {
		t.Errorf("--min-size 250KB: exit %d\n%s", code, out)
	}
	out, _, code = sapu(t, e, "", "messages", "--min-size", "100KB", "--older-than", "5y")
	if code != 0 || !strings.Contains(out, "No Messages attachment of at least 100KB and older than 5y") {
		t.Errorf("--older-than 5y: exit %d\n%s", code, out)
	}

	many := env.New(t.TempDir())
	for i := range 55 {
		finderPut(t, many, fmt.Sprintf("%sfile%02d.png", finderAttachments, i), 100+i, finderLongAgo)
	}
	out, _, code = sapu(t, many, "", "messages", "--min-size", "1")
	if code != 0 || strings.Count(out, ".png") != 50 || !strings.Contains(out, "and 5 more") || !strings.Contains(out, "55 attachments") {
		t.Errorf("limit: exit %d\n%s", code, out)
	}
	out, _, code = sapu(t, many, "", "messages", "--min-size", "1", "-v")
	if code != 0 || strings.Count(out, ".png") != 55 {
		t.Errorf("-v: exit %d\n%s", code, out)
	}
}

func TestMessagesWithoutFullDiskAccess(t *testing.T) {
	e := messagesHome(t)
	finderDenied(t, filepath.Join(e.Home, finders.MessagesDir))
	out, errOut, code := sapu(t, e, "", "messages")
	if code != 1 || out != "" {
		t.Errorf("exit %d, stdout %q", code, out)
	}
	for _, want := range []string{"Full Disk Access", "sapu doctor", "~/Library/Messages/Attachments"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q: %q", want, errOut)
		}
	}
}

func TestMessagesNothingThere(t *testing.T) {
	e := env.New(t.TempDir())
	out, _, code := sapu(t, e, "", "messages")
	if code != 0 || !strings.Contains(out, "No Messages attachment") || !strings.Contains(out, "Keep messages") {
		t.Errorf("exit %d\n%s", code, out)
	}
}

func TestMessagesJSON(t *testing.T) {
	e := messagesHome(t)
	out, errOut, code := sapu(t, e, "", "messages", "--min-size", "100KB", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "messages", out)
	var d struct {
		Items []struct {
			Path string
			Type string
		}
		TotalBytes int64 `json:"total_bytes"`
	}
	decodeData(t, out, &d)
	if len(d.Items) != 2 || filepath.Base(d.Items[0].Path) != "holiday.MOV" || d.Items[0].Type != "mov" || d.TotalBytes < 500_000 {
		t.Errorf("data: %+v", d)
	}
	noState(t, e)
}
