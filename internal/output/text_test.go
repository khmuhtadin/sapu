package output

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestTilde(t *testing.T) {
	cases := map[string]string{
		"/h":           "~",
		"/h/Library/x": "~/Library/x",
		"/hx/Library":  "/hx/Library", // another folder with the same prefix
		"/other":       "/other",
	}
	for path, want := range cases {
		if got := Tilde("/h", path); got != want {
			t.Errorf("Tilde(/h, %q) = %q, want %q", path, got, want)
		}
	}
}

func TestTable(t *testing.T) {
	var buf bytes.Buffer
	Table(&buf, [][]string{
		{"a", "1.0 KB", "first"},
		{"longer", "20.0 MB", "second"},
	}, 1)
	want := "a        1.0 KB  first\nlonger  20.0 MB  second\n"
	if buf.String() != want {
		t.Fatalf("got\n%q\nwant\n%q", buf.String(), want)
	}
}

func TestWrap(t *testing.T) {
	got := Wrap("one two three four", 9)
	want := []string{"one two", "three", "four"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := Wrap("averyveryverylongword", 5); len(got) != 1 {
		t.Fatalf("a long word stays whole, got %q", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("abcdef", 4); got != "abc…" {
		t.Errorf("got %q", got)
	}
	if got := Truncate("abc", 4); got != "abc" {
		t.Errorf("got %q", got)
	}
}

func TestShellCommand(t *testing.T) {
	got := ShellCommand([]string{"tmutil", "thinlocalsnapshots", "/", "it's here", "a b"})
	want := `tmutil thinlocalsnapshots / 'it'\''s here' 'a b'`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestStyle(t *testing.T) {
	if got := (Style{on: true}).Bold("x"); !strings.Contains(got, "\x1b[") {
		t.Errorf("colour on should add escapes, got %q", got)
	}
	var buf bytes.Buffer
	if got := NewStyle(&buf).Green("x"); got != "x" {
		t.Errorf("a buffer is not a terminal, got %q", got)
	}
}
