package output

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Width is the line width human output is written for.
const Width = 80

// IsTTY reports whether v (an io.Writer or io.Reader) is a terminal file.
func IsTTY(v any) bool {
	f, ok := v.(*os.File)
	return ok && IsTerminal(f)
}

// Style adds colour to text. It is plain unless the writer is a terminal and
// NO_COLOR is unset.
type Style struct{ on bool }

func NewStyle(w io.Writer) Style {
	return Style{on: IsTTY(w) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"}
}

func (s Style) wrap(code, text string) string {
	if !s.on {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (s Style) Bold(text string) string   { return s.wrap("1", text) }
func (s Style) Dim(text string) string    { return s.wrap("2", text) }
func (s Style) Red(text string) string    { return s.wrap("31", text) }
func (s Style) Green(text string) string  { return s.wrap("32", text) }
func (s Style) Yellow(text string) string { return s.wrap("33", text) }

// Tilde shortens a path inside home to "~/...".
func Tilde(home, path string) string {
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + path[len(home):]
	}
	return path
}

// Table writes rows as aligned columns, two spaces apart. The columns listed
// in right are right-aligned. The last column is never padded.
func Table(w io.Writer, rows [][]string, right ...int) {
	var widths []int
	for _, row := range rows {
		for i, cell := range row {
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], utf8.RuneCountInString(cell))
		}
	}
	for _, row := range rows {
		var line strings.Builder
		for i, cell := range row {
			pad := strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell))
			switch {
			case i == len(row)-1:
				line.WriteString(cell)
			case isRight(right, i):
				line.WriteString(pad + cell + "  ")
			default:
				line.WriteString(cell + pad + "  ")
			}
		}
		fmt.Fprintln(w, strings.TrimRight(line.String(), " "))
	}
}

func isRight(right []int, i int) bool {
	for _, r := range right {
		if r == i {
			return true
		}
	}
	return false
}

// Wrap breaks text into lines of at most width characters. A word longer than
// width stays on a line of its own.
func Wrap(text string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// WrapIndent writes text wrapped to Width, every line starting with indent.
func WrapIndent(w io.Writer, indent, text string) {
	for _, line := range Wrap(text, Width-utf8.RuneCountInString(indent)) {
		fmt.Fprintln(w, indent+line)
	}
}

// Truncate shortens s to at most n characters, ending in "…" when it was cut.
func Truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:max(n, 0)])
	}
	return string(r[:n-1]) + "…"
}

// ShellCommand shows args as one command line, quoting arguments that need it.
func ShellCommand(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./:=@%+,-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
