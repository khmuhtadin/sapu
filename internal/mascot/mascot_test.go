package mascot

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The copy in this package is the approved art, byte for byte.
func TestCopyMatchesTheApprovedAsset(t *testing.T) {
	for _, name := range []string{"mascot.txt", "mascot-compact.txt"} {
		want, err := os.ReadFile("../../assets/brand/ascii/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if art != string(want) {
			t.Errorf("internal/mascot/mascot.txt differs from assets/brand/ascii/%s", name)
		}
	}
}

func TestSize(t *testing.T) {
	lines := Lines()
	if len(lines) != Height {
		t.Fatalf("%d rows, want %d", len(lines), Height)
	}
	for i, l := range lines {
		if len(l) > Width {
			t.Errorf("row %d is %d wide, want at most %d", i, len(l), Width)
		}
	}
}

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

func TestPlainHasNoEscapes(t *testing.T) {
	plain := strings.Join(Render(false), "\n")
	if strings.Contains(plain, "\x1b") {
		t.Error("plain mascot has escape codes")
	}
	if plain != strings.Join(Lines(), "\n") {
		t.Error("plain mascot differs from the art")
	}
}

// Color changes only the "=" characters; stripping it leaves the same text.
func TestColorPaintsOnlyTheLimePatch(t *testing.T) {
	colored := strings.Join(Render(true), "\n")
	if got := ansi.ReplaceAllString(colored, ""); got != strings.Join(Lines(), "\n") {
		t.Errorf("color changed the characters:\n%s", got)
	}
	if n, want := strings.Count(colored, "\x1b[38;2;189;238;99m="), strings.Count(art, "="); n != want {
		t.Errorf("%d lime = characters, want %d", n, want)
	}
	for _, l := range strings.Split(colored, "\n") {
		rest := strings.ReplaceAll(l, "\x1b[38;2;189;238;99m=\x1b[0m", "")
		if strings.Contains(rest, "\x1b") {
			t.Errorf("escape code outside a lime =: %q", l)
		}
	}
}
