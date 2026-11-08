package pkgs

import (
	"slices"
	"strings"
	"testing"
)

// Commands is the whole list of what sapu packages may run. Apply refuses
// any other command of Source "packages", so adding one is a deliberate
// change here.
func TestCommands(t *testing.T) {
	var got []string
	for _, c := range Commands() {
		got = append(got, strings.Join(c, " "))
	}
	want := []string{
		"brew cleanup --prune=all -s",
		"npm cache clean --force",
		"pnpm store prune",
		"yarn cache clean",
		"python3 -m pip cache purge",
		"uv cache clean",
		"go clean -cache",
		"go clean -modcache",
		"pod cache clean --all",
		"xcrun simctl delete unavailable",
		"docker system prune",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Commands()\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
