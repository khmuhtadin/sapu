package health

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Apple's rule of thumb: a battery is consumed when it holds 80% of its
// design capacity.
const consumedPercent = 80

func checkBattery(ctx context.Context) Row {
	row := Row{ID: "battery", Title: "Battery"}
	out, err := RunCommand(ctx, "ioreg", "-rn", "AppleSmartBattery", "-a")
	if err != nil {
		return unknownRow(row.ID, row.Title, err)
	}
	// A desktop Mac has no battery: ioreg prints nothing or an empty list.
	var b map[string]any
	if len(strings.TrimSpace(string(out))) > 0 {
		v, err := parsePlist(out)
		if err != nil {
			return unknownRow(row.ID, row.Title, err)
		}
		if list, _ := v.([]any); len(list) > 0 {
			b, _ = list[0].(map[string]any)
		}
	}
	if installed, ok := b["BatteryInstalled"].(bool); b == nil || ok && !installed {
		row.Value = "no battery"
		row.Status = Skipped
		row.Meaning = "This looks like a desktop Mac, so there is no battery to check."
		return row
	}

	cycles := intOf(b["CycleCount"])
	designCycles := intOf(b["DesignCycleCount9C"])
	if designCycles == 0 {
		designCycles = 1000
	}
	percent := capacityFromIoreg(b)
	condition := ""
	// System Settings shows the numbers from system_profiler, so they win.
	if out, err := RunCommand(ctx, "system_profiler", "SPPowerDataType", "-json"); err == nil {
		if p, c, ok := healthFromProfiler(out); ok {
			percent, condition = p, c
		}
	}

	status := Good
	if percent < 0 {
		status = Unknown
	} else if percent < 70 {
		status = Attention
	} else if percent < consumedPercent {
		status = Watch
	}
	if cycles >= designCycles {
		status = worse(status, Watch)
	}
	if condition != "" && condition != "Normal" && condition != "Good" {
		status = worse(status, Attention)
	}
	row.Status = status

	var parts []string
	if percent >= 0 {
		parts = append(parts, fmt.Sprintf("%d%% of design capacity", percent))
	}
	parts = append(parts, fmt.Sprintf("%d cycles", cycles))
	if condition != "" {
		parts = append(parts, "condition "+condition)
	}
	row.Value = strings.Join(parts, ", ")

	if percent >= 0 {
		row.Meaning = fmt.Sprintf("Battery at %d%% of its design capacity after %d cycles. Apple considers batteries consumed at %d%% capacity after %d cycles.",
			percent, cycles, consumedPercent, designCycles)
	} else {
		row.Meaning = fmt.Sprintf("The battery has been through %d cycles, but macOS did not report its capacity. Apple considers batteries consumed at %d%% capacity after %d cycles.",
			cycles, consumedPercent, designCycles)
	}
	switch row.Status {
	case Watch:
		row.Meaning += " Plan a battery service when it stops lasting through your day."
	case Attention:
		row.Meaning += " Apple Support can check and replace the battery."
	}
	return row
}

// capacityFromIoreg returns the maximum capacity as a percentage of the
// design capacity, or -1. Apple Silicon reports the real maximum in
// AppleRawMaxCapacity; older Macs report it in MaxCapacity (Apple Silicon
// uses that key for a percentage).
func capacityFromIoreg(b map[string]any) int {
	design := intOf(b["DesignCapacity"])
	maxCap := intOf(b["AppleRawMaxCapacity"])
	if maxCap == 0 && intOf(b["MaxCapacity"]) > 100 {
		maxCap = intOf(b["MaxCapacity"])
	}
	if design <= 0 || maxCap <= 0 {
		return -1
	}
	return maxCap * 100 / design
}

// healthFromProfiler reads the battery's maximum capacity and condition from
// `system_profiler SPPowerDataType -json`.
func healthFromProfiler(out []byte) (percent int, condition string, ok bool) {
	var doc struct {
		Items []struct {
			Health struct {
				Condition string `json:"sppower_battery_health"`
				Max       string `json:"sppower_battery_health_maximum_capacity"`
			} `json:"sppower_battery_health_info"`
		} `json:"SPPowerDataType"`
	}
	if json.Unmarshal(out, &doc) != nil {
		return 0, "", false
	}
	for _, item := range doc.Items {
		h := item.Health
		if p, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(h.Max), "%")); err == nil {
			return p, h.Condition, true
		}
	}
	return 0, "", false
}
