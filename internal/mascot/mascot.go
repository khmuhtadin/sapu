// Package mascot holds the approved terminal mascot: the small hash-based
// ikan sapu-sapu, 30 columns by 10 rows. mascot.txt is a copy of
// assets/brand/ascii/mascot.txt (go:embed cannot reach assets/); a test keeps
// the two byte-identical.
//
// The mascot belongs in the main menu and in the help of an interactive
// terminal only. Never print it in --json, piped output, completion or
// progress lines.
//
// The "=" characters are brand lime #BDEE63 when color is allowed. Everything
// else stays in the terminal's own foreground.
package mascot

import (
	_ "embed"
	"strings"
)

//go:embed mascot.txt
var art string

const (
	// Width and Height of the drawing.
	Width  = 30
	Height = 10

	lime  = "\x1b[38;2;189;238;99m" // #BDEE63
	reset = "\x1b[0m"
)

// Lines returns the 10 rows of the mascot, plain, without trailing spaces.
func Lines() []string {
	return strings.Split(strings.TrimRight(art, "\n"), "\n")
}

// Render returns the mascot as lines. With color, each "=" is lime.
func Render(color bool) []string {
	lines := Lines()
	if !color {
		return lines
	}
	for i, l := range lines {
		lines[i] = strings.ReplaceAll(l, "=", lime+"="+reset)
	}
	return lines
}

// Accent writes s in brand lime when color is true, as plain text otherwise.
func Accent(s string, color bool) string {
	if !color {
		return s
	}
	return lime + s + reset
}
