package engine

import (
	"os"
	"os/exec"
	"testing"

	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// testCommandSource is the Source of the command items these tests make
// with their own scripts (see command in command_test.go). For it, and only
// in this package's tests, the command itself counts as the one Sapu made,
// so those tests check how commands run. known_test.go checks the real
// sources against sapuCommand.
const testCommandSource = "test.command"

func TestMain(m *testing.M) {
	knownCommand = func(it plan.Item, cleaners []rules.Rule) ([]string, func(string) (string, error), bool) {
		if it.Source == testCommandSource {
			return it.Command, exec.LookPath, len(it.Command) > 0
		}
		return sapuCommand(it, cleaners)
	}
	os.Exit(m.Run())
}
