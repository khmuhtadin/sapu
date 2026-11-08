package explain

import (
	"time"

	"github.com/khmuhtadin/sapu/internal/trends"
)

// homeFreshness is how old a home snapshot may be to be used in Reconcile.
const homeFreshness = 24 * time.Hour

// outsideHome are the rows that measure folders outside the home folder.
// Their bytes are not part of the home total, so they are added to it.
var outsideHome = map[string]bool{
	"spotlight":     true,
	"system_assets": true,
	"swap":          true,
}

// Reconciliation is "How this adds up" (blueprint D2): where the used space
// of the disk went, in parts that do not overlap. Used is what the disk
// reports. Home comes from a size snapshot. System is what the rows above
// measured outside home. Rest is what is left, so it is an estimate: macOS
// itself, other users, and areas macOS protects (SIP) that Sapu cannot see.
type Reconciliation struct {
	TotalBytes int64 `json:"total_bytes"` // size of the volume
	UsedBytes  int64 `json:"used_bytes"`  // from statfs: counts purgeable space as used
	// HomeBytes is -1 when there is no snapshot under 24 hours old.
	HomeBytes int64 `json:"home_bytes"`
	// HomeUnmeasured are parts of home the snapshot left out (they did not
	// answer in time); HomeBytes does not count them.
	HomeUnmeasured []string   `json:"home_unmeasured,omitempty"`
	HomeTaken      *time.Time `json:"home_taken,omitempty"` // when the snapshot was taken
	// SystemBytes adds up the rows outside home that could be measured: a lower bound.
	SystemBytes      int64 `json:"system_bytes"`
	SystemUnmeasured int   `json:"system_unmeasured"` // such rows that could not be measured
	// RestBytes is UsedBytes - HomeBytes - SystemBytes, never below 0; -1 when
	// HomeBytes is -1. It is always an estimate.
	RestBytes int64  `json:"rest_bytes"`
	Note      string `json:"note,omitempty"`
}

// Reconcile adds up rows, used and the newest home snapshot (nil for none).
func Reconcile(rows []Row, total, used int64, home *trends.Snapshot, now time.Time) Reconciliation {
	r := Reconciliation{TotalBytes: total, UsedBytes: used, HomeBytes: -1, RestBytes: -1}
	for _, row := range rows {
		switch {
		case !outsideHome[row.ID]:
		case row.Bytes < 0:
			r.SystemUnmeasured++
		default:
			r.SystemBytes += row.Bytes
		}
	}
	if home == nil || now.Sub(home.Taken) >= homeFreshness {
		return r
	}
	r.HomeBytes = home.HomeBytes
	r.HomeUnmeasured = home.Unmeasured
	taken := home.Taken
	r.HomeTaken = &taken
	r.RestBytes = used - r.HomeBytes - r.SystemBytes
	if r.RestBytes < 0 {
		r.RestBytes = 0
		r.Note = "Sapu measured more than the disk reports as used. The home snapshot may be a few hours old, " +
			"or macOS counts the space differently. Read these numbers as estimates."
	}
	return r
}
