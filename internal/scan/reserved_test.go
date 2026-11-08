package scan

import (
	"context"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// A rule whose id is a dedicated command's source (made in code, past
// rules.Validate) makes no items: its items would carry that source and get
// the command's protection exceptions.
func TestScanRefusesReservedRuleID(t *testing.T) {
	e := env.New(t.TempDir())
	write(t, e.Home, "Library/Application Support/MobileSync/Backup/one/Manifest.db", 10)
	for _, id := range []string{"backups", "trash", "purge.node"} {
		rs := []rules.Rule{rule(id, "~/Library/Application Support/MobileSync/Backup/*")}
		if items, err := Scan(context.Background(), e, rs, nil); err == nil {
			t.Errorf("rule %q: no error, %d items", id, len(items))
		}
	}
}
