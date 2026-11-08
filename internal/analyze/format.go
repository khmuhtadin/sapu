package analyze

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
)

// humanBytes formats a size with base-1000 units, like Finder: "512 B",
// "1.5 KB", "999.9 MB". The result is at most 8 characters wide.
func humanBytes(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d B", max(n, 0))
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	v := float64(n) / 1000
	i := 0
	for v >= 999.95 && i < len(units)-1 {
		v /= 1000
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// commas formats a count: 1234567 becomes "1,234,567".
func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

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

// truncateMiddle shortens s to at most w cells, keeping both ends:
// "node_modules_backup_2021" -> "node_mo…_2021".
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
func padLeft(s string, w int) string {
	if gap := w - width(s); gap > 0 {
		return strings.Repeat(" ", gap) + s
	}
	return s
}

// padRight left-aligns s in w cells.
func padRight(s string, w int) string {
	if gap := w - width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}
