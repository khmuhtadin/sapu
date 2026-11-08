package health

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Canned output, shaped like the real commands print it on a laptop.

// ioregPlist is `ioreg -rn AppleSmartBattery -a`. raw is AppleRawMaxCapacity,
// 0 leaves it out (older Macs); maxCap is MaxCapacity (a percentage on Apple
// Silicon, mAh on older Macs).
func ioregPlist(cycles, design, raw, maxCap int) string {
	rawKey := ""
	if raw > 0 {
		rawKey = fmt.Sprintf("<key>AppleRawMaxCapacity</key><integer>%d</integer>", raw)
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<array>
	<dict>
		<key>AdapterDetails</key>
		<dict><key>AdapterID</key><integer>0</integer><key>Name</key><string>pd charger</string></dict>
		<key>BatteryData</key>
		<data>AAECAw==</data>
		<key>BatteryInstalled</key><true/>
		<key>CycleCount</key><integer>%d</integer>
		<key>DesignCapacity</key><integer>%d</integer>
		<key>DesignCycleCount9C</key><integer>1000</integer>
		<key>MaxCapacity</key><integer>%d</integer>
		<key>IsCharging</key><false/>
		%s
	</dict>
</array>
</plist>
`, cycles, design, maxCap, rawKey)
}

// profilerPower is `system_profiler SPPowerDataType -json`.
func profilerPower(percent, condition string) string {
	return `{"SPPowerDataType":[{"_name":"spbattery_information","sppower_battery_health_info":{
"sppower_battery_cycle_count":640,"sppower_battery_health":"` + condition + `",
"sppower_battery_health_maximum_capacity":"` + percent + `"}},{"_name":"sppower_information"}]}`
}

// diskutilPlist is `diskutil info -plist /`.
func diskutilPlist(solid bool, smart string) string {
	s := "false"
	if solid {
		s = "true"
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>APFSPhysicalStores</key><array><dict><key>APFSPhysicalStore</key><string>disk0s2</string></dict></array>
<key>DeviceNode</key><string>/dev/disk3s1s1</string>
<key>SolidState</key><` + s + `/>
<key>SMARTStatus</key><string>` + smart + `</string>
</dict></plist>`
}

// nvmeJSON is `system_profiler SPNVMeDataType -json` with the drive on disk0.
func nvmeJSON(smart, trim string) string {
	return `{"SPNVMeDataType":[{"_name":"Apple SSD Controller","_items":[
{"_name":"EXTERNAL","bsd_name":"disk9","device_model":"OTHER SSD","smart_status":"Failing","spnvme_trim_support":"No"},
{"_name":"APPLE SSD AP0256Z","bsd_name":"disk0","device_model":"APPLE SSD AP0256Z","smart_status":"` + smart + `","spnvme_trim_support":"` + trim + `"}]}]}`
}

const (
	thermOK = "Note: No thermal warning level has been recorded\nNote: No performance warning level has been recorded\nNote: No CPU power status has been recorded\n"
	memOK   = "The system has 17179869184 (1048576 pages with a page size of 16384).\nSystem-wide memory free percentage: 60%\n"
)

func therm(limit int) string {
	return fmt.Sprintf("Note: No thermal warning level has been recorded\nCPU_Scheduler_Limit \t= 100\nCPU_Available_CPUs \t= 8\nCPU_Speed_Limit \t= %d\n", limit)
}

func mem(free int) string {
	return fmt.Sprintf("The system has 17179869184 (1048576 pages with a page size of 16384).\nSystem-wide memory free percentage: %d%%\n", free)
}

// fake is a set of canned answers keyed by the command line.
type fake map[string]any // string output, or an error

func (f fake) run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	switch v := f[line].(type) {
	case string:
		return []byte(v), nil
	case error:
		return nil, v
	}
	return nil, errors.New("no canned answer for " + line)
}

const (
	cmdIoreg   = "ioreg -rn AppleSmartBattery -a"
	cmdPower   = "system_profiler SPPowerDataType -json"
	cmdDisk    = "diskutil info -plist /"
	cmdNVMe    = "system_profiler SPNVMeDataType -json"
	cmdTherm   = "pmset -g therm"
	cmdMemory  = "memory_pressure -Q"
	bootOffset = 3*24*time.Hour + 4*time.Hour
)

// goodMac answers like a healthy laptop.
func goodMac() fake {
	return fake{
		cmdIoreg:  ioregPlist(640, 4629, 3800, 100),
		cmdPower:  profilerPower("82%", "Normal"),
		cmdDisk:   diskutilPlist(true, "Verified"),
		cmdNVMe:   nvmeJSON("Verified", "Yes"),
		cmdTherm:  thermOK,
		cmdMemory: memOK,
	}
}

// useFake runs Check against f, with the Mac having started bootAgo ago.
func useFake(t *testing.T, f fake, bootAgo time.Duration) {
	t.Helper()
	oldRun, oldBoot, oldNow := RunCommand, BootTime, Now
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	RunCommand = f.run
	BootTime = func() (time.Time, error) { return now.Add(-bootAgo), nil }
	Now = func() time.Time { return now }
	t.Cleanup(func() { RunCommand, BootTime, Now = oldRun, oldBoot, oldNow })
}

func rowOf(t *testing.T, id string) Row {
	t.Helper()
	for _, r := range Check(context.Background()) {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no row %q", id)
	return Row{}
}

func TestAllGood(t *testing.T) {
	useFake(t, goodMac(), bootOffset)
	rows := Check(context.Background())
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.ID)
		if r.Status != Good {
			t.Errorf("%s: status %q, want good (%+v)", r.ID, r.Status, r)
		}
		if r.Title == "" || r.Value == "" || r.Meaning == "" {
			t.Errorf("%s: empty field: %+v", r.ID, r)
		}
	}
	if got := strings.Join(ids, " "); got != "battery drive temperature memory uptime" {
		t.Errorf("rows %q", got)
	}

	b := rowOf(t, "battery")
	if b.Value != "82% of design capacity, 640 cycles, condition Normal" {
		t.Errorf("battery value %q", b.Value)
	}
	want := "Battery at 82% of its design capacity after 640 cycles. Apple considers batteries consumed at 80% capacity after 1000 cycles."
	if b.Meaning != want {
		t.Errorf("battery meaning %q", b.Meaning)
	}
	if d := rowOf(t, "drive"); d.Value != "APPLE SSD AP0256Z, SMART Verified, TRIM yes" {
		t.Errorf("drive value %q", d.Value)
	}
	if u := rowOf(t, "uptime"); u.Value != "3 days, 4 hours" {
		t.Errorf("uptime value %q", u.Value)
	}
}

func TestBattery(t *testing.T) {
	cases := []struct {
		name   string
		answer map[string]any
		status string
		value  string // part of the value
	}{
		{"80 percent is still good", map[string]any{cmdPower: profilerPower("80%", "Normal")}, Good, "80% of design"},
		{"79 percent is worth watching", map[string]any{cmdPower: profilerPower("79%", "Normal")}, Watch, "79% of design"},
		{"70 percent is worth watching", map[string]any{cmdPower: profilerPower("70%", "Normal")}, Watch, "70% of design"},
		{"69 percent needs attention", map[string]any{cmdPower: profilerPower("69%", "Normal")}, Attention, "69% of design"},
		{"service recommended needs attention", map[string]any{cmdPower: profilerPower("90%", "Service Recommended")}, Attention, "condition Service Recommended"},
		{"1000 cycles is worth watching", map[string]any{cmdIoreg: ioregPlist(1000, 4629, 4500, 100), cmdPower: profilerPower("97%", "Normal")}, Watch, "1000 cycles"},
		{"capacity from ioreg when system_profiler fails", map[string]any{cmdPower: errors.New("boom"), cmdIoreg: ioregPlist(300, 5000, 4000, 100)}, Good, "80% of design capacity, 300 cycles"},
		{"older Mac: MaxCapacity is mAh", map[string]any{cmdPower: errors.New("boom"), cmdIoreg: ioregPlist(300, 5000, 0, 3500)}, Watch, "70% of design"},
		{"no capacity anywhere", map[string]any{cmdPower: errors.New("boom"), cmdIoreg: ioregPlist(300, 5000, 0, 100)}, Unknown, "300 cycles"},
	}
	for _, c := range cases {
		f := goodMac()
		for k, v := range c.answer {
			f[k] = v
		}
		useFake(t, f, bootOffset)
		r := rowOf(t, "battery")
		if r.Status != c.status || !strings.Contains(r.Value, c.value) {
			t.Errorf("%s: got %q %q, want %q containing %q", c.name, r.Status, r.Value, c.status, c.value)
		}
	}
}

func TestBatteryMissingOrBroken(t *testing.T) {
	for name, answer := range map[string]any{
		"empty output":      "",
		"empty list":        `<?xml version="1.0"?><plist version="1.0"><array></array></plist>`,
		"battery not there": `<plist version="1.0"><array><dict><key>BatteryInstalled</key><false/></dict></array></plist>`,
	} {
		f := goodMac()
		f[cmdIoreg] = answer
		useFake(t, f, bootOffset)
		r := rowOf(t, "battery")
		if r.Status != Skipped || !strings.Contains(r.Meaning, "desktop") {
			t.Errorf("%s: %+v", name, r)
		}
	}

	for name, answer := range map[string]any{
		"command fails": errors.New("ioreg: not found"),
		"not a plist":   "this is not xml at all <<<",
	} {
		f := goodMac()
		f[cmdIoreg] = answer
		useFake(t, f, bootOffset)
		if r := rowOf(t, "battery"); r.Status != Unknown {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

func TestDrive(t *testing.T) {
	cases := []struct {
		name    string
		answer  map[string]any
		status  string
		value   string
		meaning string
	}{
		{"failing", map[string]any{cmdNVMe: nvmeJSON("Failing", "Yes")}, Attention, "SMART Failing", "failing"},
		{"smart not supported", map[string]any{cmdDisk: diskutilPlist(true, "Not Supported"), cmdNVMe: nvmeJSON("", "Yes")}, Unknown, "SMART Not Supported", "does not report"},
		{"trim off", map[string]any{cmdNVMe: nvmeJSON("Verified", "No")}, Watch, "TRIM no", "TRIM is off"},
		{"nvme list fails: diskutil only", map[string]any{cmdNVMe: errors.New("boom")}, Good, "SSD, SMART Verified", "healthy"},
		{"hard disk", map[string]any{cmdDisk: diskutilPlist(false, "Verified"), cmdNVMe: errors.New("boom")}, Good, "hard disk, SMART Verified", "healthy"},
		{"another drive in the list is ignored", map[string]any{}, Good, "APPLE SSD AP0256Z", "healthy"},
	}
	for _, c := range cases {
		f := goodMac()
		for k, v := range c.answer {
			f[k] = v
		}
		useFake(t, f, bootOffset)
		r := rowOf(t, "drive")
		if r.Status != c.status || !strings.Contains(r.Value, c.value) || !strings.Contains(r.Meaning, c.meaning) {
			t.Errorf("%s: got %+v", c.name, r)
		}
	}

	for name, answer := range map[string]any{"error": errors.New("diskutil failed"), "garbage": "nope", "list": "<plist><array></array></plist>"} {
		f := goodMac()
		f[cmdDisk] = answer
		useFake(t, f, bootOffset)
		if r := rowOf(t, "drive"); r.Status != Unknown {
			t.Errorf("diskutil %s: %+v", name, r)
		}
	}
}

func TestTemperature(t *testing.T) {
	cases := []struct {
		name   string
		answer any
		status string
		value  string
	}{
		{"no warnings", thermOK, Good, "CPU speed limit 100%, no thermal warnings"},
		{"limit 100", therm(100), Good, "limit 100%"},
		{"limit 99", therm(99), Watch, "limit 99%"},
		{"limit 80", therm(80), Watch, "limit 80%"},
		{"limit 79", therm(79), Attention, "limit 79%"},
		{"a warning level", "Thermal warning level = 2\nCPU_Speed_Limit = 100\n", Watch, "thermal warning level 2"},
		{"command fails", errors.New("pmset failed"), Unknown, "not available"},
	}
	for _, c := range cases {
		f := goodMac()
		f[cmdTherm] = c.answer
		useFake(t, f, bootOffset)
		r := rowOf(t, "temperature")
		if r.Status != c.status || !strings.Contains(r.Value, c.value) {
			t.Errorf("%s: got %+v", c.name, r)
		}
	}
}

func TestMemory(t *testing.T) {
	cases := []struct {
		name   string
		answer any
		status string
	}{
		{"60 free", mem(60), Good},
		{"25 free", mem(25), Good},
		{"24 free", mem(24), Watch},
		{"10 free", mem(10), Watch},
		{"9 free", mem(9), Attention},
		{"no number", "System-wide memory pressure\n", Unknown},
		{"command fails", errors.New("memory_pressure failed"), Unknown},
	}
	for _, c := range cases {
		f := goodMac()
		f[cmdMemory] = c.answer
		useFake(t, f, bootOffset)
		if r := rowOf(t, "memory"); r.Status != c.status {
			t.Errorf("%s: got %+v", c.name, r)
		}
	}
}

func TestUptime(t *testing.T) {
	day := 24 * time.Hour
	for _, c := range []struct {
		ago    time.Duration
		status string
		value  string
	}{
		{90 * time.Minute, Good, "0 days, 1 hour"},
		{13*day + 23*time.Hour, Good, "13 days, 23 hours"},
		{14 * day, Watch, "14 days, 0 hours"},
		{29 * day, Watch, "29 days, 0 hours"},
		{30 * day, Attention, "30 days, 0 hours"},
	} {
		useFake(t, goodMac(), c.ago)
		if r := rowOf(t, "uptime"); r.Status != c.status || r.Value != c.value {
			t.Errorf("up %v: got %+v", c.ago, r)
		}
	}

	useFake(t, goodMac(), 0)
	BootTime = func() (time.Time, error) { return time.Time{}, errors.New("sysctl failed") }
	if r := rowOf(t, "uptime"); r.Status != Unknown {
		t.Errorf("boot time fails: %+v", r)
	}
}

// Every command failing gives five unknown rows, not a crash or a stop.
func TestEveryCommandFails(t *testing.T) {
	f := fake{}
	for _, c := range []string{cmdIoreg, cmdPower, cmdDisk, cmdNVMe, cmdTherm, cmdMemory} {
		f[c] = errors.New("failed")
	}
	useFake(t, f, bootOffset)
	for _, r := range Check(context.Background()) {
		if r.ID == "uptime" {
			continue
		}
		if r.Status != Unknown || !strings.Contains(r.Meaning, "could not read") {
			t.Errorf("%s: %+v", r.ID, r)
		}
	}
}

func TestParsePlist(t *testing.T) {
	v, err := parsePlist([]byte(`<?xml version="1.0"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>s</key><string>a &amp; b</string>
<key>n</key><integer>-42</integer>
<key>r</key><real>1.5</real>
<key>t</key><true/><key>f</key><false/>
<key>d</key><data>AAEC</data><key>when</key><date>2026-11-08T00:00:00Z</date>
<key>list</key><array><integer>1</integer><dict><key>x</key><string>y</string></dict></array>
<key>empty</key><dict/>
</dict></plist>`))
	if err != nil {
		t.Fatal(err)
	}
	m := v.(map[string]any)
	if m["s"] != "a & b" || m["n"] != int64(-42) || m["r"] != 1.5 || m["t"] != true || m["f"] != false || m["d"] != "AAEC" {
		t.Errorf("scalars: %v", m)
	}
	list := m["list"].([]any)
	if len(list) != 2 || list[0] != int64(1) || list[1].(map[string]any)["x"] != "y" {
		t.Errorf("list: %v", list)
	}
	if e, ok := m["empty"].(map[string]any); !ok || len(e) != 0 {
		t.Errorf("empty dict: %v", m["empty"])
	}

	for _, bad := range []string{"", "not xml", "<plist><dict><key>a</key><integer>x</integer></dict></plist>", "<plist><dict><key>a</key>"} {
		if _, err := parsePlist([]byte(bad)); err == nil {
			t.Errorf("parsePlist(%q) succeeded", bad)
		}
	}
}
