package rules_test

import (
	"testing"

	"github.com/khmuhtadin/sapu/internal/dupes"
	"github.com/khmuhtadin/sapu/internal/optimize"
	"github.com/khmuhtadin/sapu/internal/pkgs"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/purge"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/similar"
	"github.com/khmuhtadin/sapu/internal/tidy"
)

// Every source a dedicated command gives its items is reserved, so no
// cleaner rule can take it. A new command must add its source to
// rules.ReservedSources.
func TestReservedCoversEveryDedicatedSource(t *testing.T) {
	sources := []string{
		plan.KindPurge, plan.KindAnalyze,
		plan.KindUninstall, plan.KindLarge, plan.KindBackups, plan.KindMessages,
		plan.KindTrash, plan.KindInstaller, plan.KindDupes, plan.KindPackages,
		plan.KindOptimize, plan.KindLoginItems, plan.KindReset, plan.KindSimilar, plan.KindTidy,
		dupes.Source, optimize.Source, pkgs.Source, similar.Source, tidy.Source,
		purge.WorktreeSource, "purge:node_modules",
	}
	for _, s := range sources {
		if !rules.Reserved(s) {
			t.Errorf("source %q is not reserved", s)
		}
	}
}
