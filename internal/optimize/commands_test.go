package optimize

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Commands is the whole list of what sapu optimize may run, and every item
// Items returns runs one of them.
func TestCommands(t *testing.T) {
	var got []string
	for _, c := range Commands() {
		got = append(got, strings.Join(c, " "))
	}
	want := []string{"qlmanage -r cache", "atsutil databases -removeUser"}
	if !slices.Equal(got, want) {
		t.Errorf("Commands() = %q, want %q", got, want)
	}
	for _, it := range Items() { // the real programs, when this Mac has them
		cmd := append([]string{filepath.Base(it.Command[0])}, it.Command[1:]...)
		if !slices.Contains(want, strings.Join(cmd, " ")) {
			t.Errorf("item command %v is not one of Commands", it.Command)
		}
	}
}
