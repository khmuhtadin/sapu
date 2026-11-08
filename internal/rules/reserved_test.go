package rules

import (
	"strings"
	"testing"
)

// The final review's case: a cleaner rule with the id "backups" gave its
// items the source "backups" and so the protect exception meant for sapu
// backups. Every dedicated source is refused as a rule id.
func TestValidateRejectsReservedIDs(t *testing.T) {
	ids := append([]string{"purge", "purge.node", "purge-x", "purge_x"}, ReservedSources...)
	for _, id := range ids {
		r := goodRule()
		r.ID = id
		if err := Validate(r); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("id %q: err %v, want reserved", id, err)
		}
	}
	for _, id := range []string{"purgeable", "backups.helper", "trash.cache", "large-files.cache"} {
		r := goodRule()
		r.ID = id
		if err := Validate(r); err != nil {
			t.Errorf("id %q: %v", id, err)
		}
	}
}
