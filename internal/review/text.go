package review

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
)

// clean replaces control characters, so a file name cannot move the cursor or
// send escape codes to the terminal.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}

func width(s string) int { return lipgloss.Width(s) }

func spaces(n int) string { return strings.Repeat(" ", max(n, 0)) }

// fit cuts a finished line, colours and all, to at most w cells.
func fit(line string, w int) string {
	if width(line) <= w {
		return line
	}
	return lipgloss.NewStyle().MaxWidth(max(w, 0)).Render(line)
}

// truncateMiddle shortens s to at most w cells, keeping both ends:
// "~/Library/Caches/com.spotify.client" -> "~/Library/Ca…spotify.client".
func truncateMiddle(s string, w int) string {
	if width(s) <= w {
		return s
	}
	if w <= 0 {
		return ""
	}
	if w == 1 {
		return "…"
	}
	runes := []rune(s)
	left := w / 2
	right := (w - 1) / 2
	var head, tail []rune
	used := 0
	for _, r := range runes {
		rw := width(string(r))
		if used+rw > left {
			break
		}
		head = append(head, r)
		used += rw
	}
	used = 0
	for i := len(runes) - 1; i >= 0; i-- {
		rw := width(string(runes[i]))
		if used+rw > right {
			break
		}
		tail = append([]rune{runes[i]}, tail...)
		used += rw
	}
	return string(head) + "…" + string(tail)
}

// truncateEnd shortens s to at most w cells, cutting the end.
func truncateEnd(s string, w int) string {
	if width(s) <= w {
		return s
	}
	if w <= 0 {
		return ""
	}
	var out []rune
	used := 0
	for _, r := range s {
		rw := width(string(r))
		if used+rw > w-1 {
			break
		}
		out = append(out, r)
		used += rw
	}
	return string(out) + "…"
}

// padLeft right-aligns s in w cells.
func padLeft(s string, w int) string { return spaces(w-width(s)) + s }

// padRight left-aligns s in w cells.
func padRight(s string, w int) string { return s + spaces(w-width(s)) }

// wrap breaks text into lines of at most w cells. A word longer than w is cut.
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
	if line != "" {
		lines = append(lines, truncateEnd(line, w))
	}
	return lines
}

// chunks breaks s into pieces of at most w cells, for a path that has no
// spaces to wrap at.
func chunks(s string, w int) []string {
	if w < 1 {
		return []string{s}
	}
	var out []string
	line := ""
	for _, r := range s {
		if line != "" && width(line+string(r)) > w {
			out = append(out, line)
			line = ""
		}
		line += string(r)
	}
	return append(out, line)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// tilde shows a path inside home as ~/...
func tilde(home, p string) string {
	switch {
	case home == "":
	case p == home:
		return "~"
	case strings.HasPrefix(p, home+"/"):
		return "~" + p[len(home):]
	}
	return p
}
