// Package health reports how the hardware is doing, for sapu health
// (blueprint O6): battery, startup drive, temperature, memory and uptime.
// It only reads, and needs no admin rights. It never runs anything that
// changes the Mac.
//
// Each Row has a value, a status and one sentence of meaning. The statuses
// and their thresholds are plain:
//
//   - Battery: good at 80% of design capacity or more, worth watching from
//     70% to 79% or at 1000 cycles, needs attention below 70% or when macOS
//     says the battery needs service.
//   - Drive: good when SMART is Verified, needs attention when it is
//     Failing; TRIM off is worth watching.
//   - Temperature: good when the CPU speed limit is 100%, worth watching
//     from 80% to 99%, needs attention below 80%.
//   - Memory: good with 25% or more free, worth watching from 10% to 24%,
//     needs attention below 10%.
//   - Uptime: good under 14 days, worth watching from 14 days, needs
//     attention from 30 days.
//
// A source that fails gives a row with the status "unknown" and the error;
// one check failing never stops the others.
package health

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	Good      = "good"
	Watch     = "watch"     // worth watching
	Attention = "attention" // needs attention
	Unknown   = "unknown"   // the source did not answer
	Skipped   = "skipped"   // does not apply to this Mac
)

// Row is one line of the health report.
type Row struct {
	ID      string `json:"id"` // "battery", "drive", "temperature", "memory" or "uptime"
	Title   string `json:"title"`
	Value   string `json:"value"`
	Status  string `json:"status"`
	Meaning string `json:"meaning"`
}

// Tests replace these.
var (
	// RunCommand runs a command and returns its standard output.
	RunCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, name, args...).Output()
	}
	// BootTime is when this Mac started.
	BootTime = func() (time.Time, error) {
		tv, err := unix.SysctlTimeval("kern.boottime")
		if err != nil {
			return time.Time{}, err
		}
		return time.Unix(tv.Sec, int64(tv.Usec)*1000), nil
	}
	// Now is the current time.
	Now = time.Now
)

// Check returns the rows in a fixed order.
func Check(ctx context.Context) []Row {
	return []Row{
		checkBattery(ctx),
		checkDrive(ctx),
		checkTemperature(ctx),
		checkMemory(ctx),
		checkUptime(),
	}
}

// worse returns the more serious of two statuses.
func worse(a, b string) string {
	rank := map[string]int{Good: 0, Watch: 1, Attention: 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func unknownRow(id, title string, err error) Row {
	return Row{ID: id, Title: title, Value: "not available", Status: Unknown,
		Meaning: "Sapu could not read this: " + err.Error() + "."}
}

var (
	speedLimitRe   = regexp.MustCompile(`CPU_Speed_Limit\s*=\s*(\d+)`)
	warningLevelRe = regexp.MustCompile(`(?i)warning level\D*(\d+)`)
)

func checkTemperature(ctx context.Context) Row {
	row := Row{ID: "temperature", Title: "Temperature"}
	out, err := RunCommand(ctx, "pmset", "-g", "therm")
	if err != nil {
		return unknownRow(row.ID, row.Title, err)
	}
	limit, warning := 100, 0
	for _, line := range strings.Split(string(out), "\n") {
		if m := speedLimitRe.FindStringSubmatch(line); m != nil {
			limit, _ = strconv.Atoi(m[1])
		}
		if strings.Contains(line, "No ") {
			continue // "Note: No thermal warning level has been recorded"
		}
		if m := warningLevelRe.FindStringSubmatch(line); m != nil {
			level, _ := strconv.Atoi(m[1])
			warning = max(warning, level)
		}
	}

	row.Status = Good
	switch {
	case limit < 80:
		row.Status = Attention
	case limit < 100 || warning > 0:
		row.Status = Watch
	}
	row.Value = fmt.Sprintf("CPU speed limit %d%%", limit)
	if warning > 0 {
		row.Value += fmt.Sprintf(", thermal warning level %d", warning)
	} else {
		row.Value += ", no thermal warnings"
	}
	if row.Status == Good {
		row.Meaning = "The Mac is not slowing itself down to cool off."
	} else {
		row.Meaning = "macOS is slowing the CPU to keep the Mac cool. Check for a heavy app, blocked vents or a hot room."
	}
	return row
}

var freePercentRe = regexp.MustCompile(`free percentage:\s*(\d+)%`)

func checkMemory(ctx context.Context) Row {
	row := Row{ID: "memory", Title: "Memory"}
	out, err := RunCommand(ctx, "memory_pressure", "-Q")
	if err != nil {
		return unknownRow(row.ID, row.Title, err)
	}
	m := freePercentRe.FindStringSubmatch(string(out))
	if m == nil {
		return unknownRow(row.ID, row.Title, fmt.Errorf("memory_pressure did not report free memory"))
	}
	free, _ := strconv.Atoi(m[1])
	row.Value = fmt.Sprintf("%d%% free", free)
	switch {
	case free >= 25:
		row.Status = Good
		row.Meaning = "Plenty of memory is free right now."
	case free >= 10:
		row.Status = Watch
		row.Meaning = "Memory is getting tight. Quit apps you are not using."
	default:
		row.Status = Attention
		row.Meaning = "Memory is very tight, so the Mac swaps to disk and feels slow. Quit heavy apps or restart."
	}
	return row
}

func checkUptime() Row {
	row := Row{ID: "uptime", Title: "Uptime"}
	boot, err := BootTime()
	if err != nil {
		return unknownRow(row.ID, row.Title, err)
	}
	up := Now().Sub(boot)
	days := int(up.Hours()) / 24
	hours := int(up.Hours()) % 24
	row.Value = fmt.Sprintf("%d %s, %d %s", days, plural(days, "day"), hours, plural(hours, "hour"))
	switch {
	case days >= 30:
		row.Status = Attention
		row.Meaning = "The Mac has not restarted for a month. A restart clears caches and applies pending updates."
	case days >= 14:
		row.Status = Watch
		row.Meaning = "The Mac has not restarted for two weeks. A restart clears caches and applies pending updates."
	default:
		row.Status = Good
		row.Meaning = "The Mac restarted recently."
	}
	return row
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
