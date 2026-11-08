package cli

import (
	"context"
	"debug/macho"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/health"
	"github.com/khmuhtadin/sapu/internal/slim"
	"github.com/khmuhtadin/sapu/internal/updates"
)

// m3Write creates a file with data, and its folders.
func m3Write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// m3App builds dir/name.app with a real Info.plist; feed "" means no Sparkle.
func m3App(t *testing.T, dir, name, short, feed string) string {
	t.Helper()
	extra := ""
	if feed != "" {
		extra = "<key>SUFeedURL</key><string>" + feed + "</string>"
	}
	p := filepath.Join(dir, name+".app")
	m3Write(t, filepath.Join(p, "Contents", "Info.plist"), []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>test.`+strings.ReplaceAll(name, " ", "")+`</string>
<key>CFBundleName</key><string>`+name+`</string>
<key>CFBundleShortVersionString</key><string>`+short+`</string>
<key>CFBundleVersion</key><string>`+short+`</string>
`+extra+`</dict></plist>
`))
	return p
}

// m3Fat builds a fat Mach-O with an x86_64 slice and an arm64 slice of the given sizes.
func m3Fat(intel, arm int) []byte {
	slice := func(cpu macho.Cpu, size int, sub uint32) []byte {
		b := make([]byte, size)
		binary.LittleEndian.PutUint32(b[0:], 0xfeedfacf)
		binary.LittleEndian.PutUint32(b[4:], uint32(cpu))
		binary.LittleEndian.PutUint32(b[8:], sub)
		binary.LittleEndian.PutUint32(b[12:], 2)
		return b
	}
	const align = 1 << 14
	header := make([]byte, 8+40)
	binary.BigEndian.PutUint32(header[0:], 0xcafebabe)
	binary.BigEndian.PutUint32(header[4:], 2)
	var body []byte
	for i, s := range []struct {
		cpu  macho.Cpu
		size int
		sub  uint32
	}{{macho.CpuAmd64, intel, 3}, {macho.CpuArm64, arm, 0}} {
		offset := len(header) + len(body)
		for offset%align != 0 {
			body = append(body, 0)
			offset++
		}
		entry := header[8+20*i:]
		binary.BigEndian.PutUint32(entry[0:], uint32(s.cpu))
		binary.BigEndian.PutUint32(entry[4:], s.sub)
		binary.BigEndian.PutUint32(entry[8:], uint32(offset))
		binary.BigEndian.PutUint32(entry[12:], uint32(s.size))
		binary.BigEndian.PutUint32(entry[16:], 14)
		body = append(body, slice(s.cpu, s.size, s.sub)...)
	}
	return append(header, body...)
}

// m3SlimMac fakes an Apple Silicon Mac with English and Indonesian.
func m3SlimMac(t *testing.T, dir string) {
	t.Helper()
	pointAppDirs(t, dir)
	oldArch, oldLangs := slim.Arch, slim.Languages
	slim.Arch = "arm64"
	slim.Languages = func(context.Context) ([]string, error) { return []string{"en-US", "id-ID"}, nil }
	t.Cleanup(func() { slim.Arch, slim.Languages = oldArch, oldLangs })
}

func TestSlimJSONAndText(t *testing.T) {
	dir := t.TempDir()
	m3SlimMac(t, dir)
	big := m3App(t, dir, "Big App", "1.0", "")
	m3Write(t, filepath.Join(big, "Contents", "MacOS", "Big App"), m3Fat(300_000, 200_000))
	m3Write(t, filepath.Join(big, "Contents", "Resources", "fr.lproj", "a.strings"), make([]byte, 50_000))
	m3Write(t, filepath.Join(big, "Contents", "Resources", "en.lproj", "a.strings"), make([]byte, 7_000))
	small := m3App(t, dir, "Small", "1.0", "")
	m3Write(t, filepath.Join(small, "Contents", "Resources", "de.lproj", "a.strings"), make([]byte, 1_000))
	m3App(t, dir, "Nothing", "1.0", "")
	e := env.New(t.TempDir())

	out, _, code := sapu(t, e, "", "slim", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	validate(t, "slim", out)
	var doc struct{ Data slimData }
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	d := doc.Data
	if d.Architecture != "arm64" || d.ForeignArchitecture != "x86_64" || !slices.Equal(d.Languages, []string{"en-US", "id-ID"}) || d.AppsScanned != 3 {
		t.Errorf("header: %+v", d)
	}
	if len(d.Apps) != 2 || d.Apps[0].Name != "Big App" || d.Apps[0].ForeignBytes != 300_000 || d.Apps[0].LanguageBytes != 50_000 || d.Apps[1].Name != "Small" {
		t.Errorf("apps: %+v", d.Apps)
	}
	if d.ForeignBytes != 300_000 || d.LanguageBytes != 51_000 || d.TotalBytes != 351_000 {
		t.Errorf("totals: %+v", d)
	}

	text, _, code := sapu(t, e, "", "slim")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, text)
	}
	checkHuman(t, text)
	for _, want := range []string{"This Mac is Apple Silicon (arm64)", "Intel code", "Big App", "351.0 KB", "code signature", "Gatekeeper", "Apple-Silicon-only"} {
		if !strings.Contains(text, want) {
			t.Errorf("text has no %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Nothing") {
		t.Errorf("an app with nothing to save is listed:\n%s", text)
	}
}

func TestSlimListsTheFirstTwentyOnly(t *testing.T) {
	dir := t.TempDir()
	m3SlimMac(t, dir)
	for i := range 23 {
		app := m3App(t, dir, fmt.Sprintf("App%02d", i), "1.0", "")
		m3Write(t, filepath.Join(app, "Contents", "Resources", "fr.lproj", "a"), make([]byte, 1000*(i+1)))
	}
	e := env.New(t.TempDir())
	text, _, code := sapu(t, e, "", "slim")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, text)
	}
	checkHuman(t, text)
	if !strings.Contains(text, "App22") || strings.Contains(text, "App02 ") || !strings.Contains(text, "…and 3 more apps, 6.0 KB in all") {
		t.Errorf("text:\n%s", text)
	}
	out, _, _ := sapu(t, e, "", "slim", "--json")
	validate(t, "slim", out)
	if n := strings.Count(out, `"foreign_arch_bytes"`); n != 24 { // 23 apps and the total
		t.Errorf("--json has %d apps, want all 23", n-1)
	}
}

func TestSlimNothingToSave(t *testing.T) {
	dir := t.TempDir()
	m3SlimMac(t, dir)
	m3App(t, dir, "Lean", "1.0", "")
	e := env.New(t.TempDir())
	out, _, code := sapu(t, e, "", "slim", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	validate(t, "slim", out)
	if !strings.Contains(out, `"apps":[]`) && !strings.Contains(out, `"apps": []`) {
		t.Errorf("apps is not an empty list:\n%s", out)
	}
	text, _, _ := sapu(t, e, "", "slim")
	checkHuman(t, text)
	if !strings.Contains(text, "No app carries Intel code or unused languages.") {
		t.Errorf("text:\n%s", text)
	}
}

func TestSlimFailsWhenLanguagesAreUnknown(t *testing.T) {
	dir := t.TempDir()
	m3SlimMac(t, dir)
	slim.Languages = func(context.Context) ([]string, error) { return nil, errors.New("defaults failed") }
	_, errOut, code := sapu(t, env.New(t.TempDir()), "", "slim")
	if code != 1 || !strings.Contains(errOut, "read your languages") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
}

// The three commands only report: they take no flag that could change something.
func TestM3ReadOnlyCommandsTakeNoChangingFlags(t *testing.T) {
	e := env.New(t.TempDir())
	for _, name := range []string{"slim", "health", "updates"} {
		for _, flag := range []string{"--yes", "--pick", "--plan", "--run-commands"} {
			if _, _, code := sapu(t, e, "", name, flag); code != 2 {
				t.Errorf("%s %s: exit %d, want 2", name, flag, code)
			}
		}
		if _, _, code := sapu(t, e, "", name, "extra"); code != 2 {
			t.Errorf("%s extra: exit %d, want 2", name, code)
		}
	}
}

// The long help of the three commands fits an 80-column terminal.
func TestM3HelpFits80Columns(t *testing.T) {
	e := env.New(t.TempDir())
	for _, name := range []string{"slim", "health", "updates"} {
		out, _, code := sapu(t, e, "", name, "--help")
		if code != 0 {
			t.Fatalf("%s --help: exit %d", name, code)
		}
		text, _, _ := strings.Cut(out, "\nFlags:")
		for _, line := range strings.Split(text, "\n") {
			if n := utf8.RuneCountInString(line); n > 80 {
				t.Errorf("%s --help: line is %d columns: %q", name, n, line)
			}
		}
	}
	out, _, _ := sapu(t, e, "", "updates", "--help")
	for _, want := range []string{"own update server", "never downloads or installs", "--offline"} {
		if !strings.Contains(out, want) {
			t.Errorf("updates --help has no %q:\n%s", want, out)
		}
	}
}

// --- health ---------------------------------------------------------------

const m3Ioreg = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><array><dict>
<key>BatteryInstalled</key><true/><key>CycleCount</key><integer>640</integer>
<key>DesignCapacity</key><integer>4629</integer><key>AppleRawMaxCapacity</key><integer>3800</integer>
<key>MaxCapacity</key><integer>100</integer><key>DesignCycleCount9C</key><integer>1000</integer>
</dict></array></plist>`

const m3Power = `{"SPPowerDataType":[{"sppower_battery_health_info":{"sppower_battery_cycle_count":640,
"sppower_battery_health":"Normal","sppower_battery_health_maximum_capacity":"82%"}}]}`

const m3Disk = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>SolidState</key><true/><key>SMARTStatus</key><string>Verified</string>
<key>APFSPhysicalStores</key><array><dict><key>APFSPhysicalStore</key><string>disk0s2</string></dict></array></dict></plist>`

const m3NVMe = `{"SPNVMeDataType":[{"_items":[{"bsd_name":"disk0","device_model":"APPLE SSD AP0256Z","smart_status":"Verified","spnvme_trim_support":"Yes"}]}]}`

// m3HealthMac fakes the commands of a healthy laptop; answers override them.
func m3HealthMac(t *testing.T, answers map[string]string) {
	t.Helper()
	canned := map[string]string{
		"ioreg -rn AppleSmartBattery -a":        m3Ioreg,
		"system_profiler SPPowerDataType -json": m3Power,
		"diskutil info -plist /":                m3Disk,
		"system_profiler SPNVMeDataType -json":  m3NVMe,
		"pmset -g therm":                        "Note: No thermal warning level has been recorded\n",
		"memory_pressure -Q":                    "System-wide memory free percentage: 60%\n",
	}
	for k, v := range answers {
		canned[k] = v
	}
	oldRun, oldBoot, oldNow := health.RunCommand, health.BootTime, health.Now
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	health.RunCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		out, ok := canned[strings.Join(append([]string{name}, args...), " ")]
		if !ok {
			return nil, errors.New("no canned answer")
		}
		return []byte(out), nil
	}
	health.BootTime = func() (time.Time, error) { return now.Add(-50 * time.Hour), nil }
	health.Now = func() time.Time { return now }
	t.Cleanup(func() { health.RunCommand, health.BootTime, health.Now = oldRun, oldBoot, oldNow })
}

func TestHealthJSONAndText(t *testing.T) {
	m3HealthMac(t, map[string]string{"memory_pressure -Q": "System-wide memory free percentage: 8%\n"})
	e := env.New(t.TempDir())

	out, _, code := sapu(t, e, "", "health", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	validate(t, "health", out)
	var doc struct{ Data healthData }
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, r := range doc.Data.Rows {
		status[r.ID] = r.Status
	}
	want := map[string]string{"battery": "good", "drive": "good", "temperature": "good", "memory": "attention", "uptime": "good"}
	for id, s := range want {
		if status[id] != s {
			t.Errorf("%s: status %q, want %q", id, status[id], s)
		}
	}

	// The exit code is 0 even when something needs attention.
	text, _, code := sapu(t, e, "", "health")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, text)
	}
	checkHuman(t, text)
	for _, want := range []string{
		"Battery: good",
		"Battery at 82% of its design capacity after 640 cycles. Apple considers",
		"Memory: needs attention",
		"2 days, 2 hours",
		"1 of 5 checks need attention.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text has no %q:\n%s", want, text)
		}
	}
}

func TestHealthOnADesktopAndWithFailures(t *testing.T) {
	m3HealthMac(t, map[string]string{"ioreg -rn AppleSmartBattery -a": ""})
	e := env.New(t.TempDir())
	out, _, code := sapu(t, e, "", "health", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	validate(t, "health", out)
	if !strings.Contains(out, `"status":"skipped"`) && !strings.Contains(out, `"status": "skipped"`) {
		t.Errorf("no skipped battery row:\n%s", out)
	}
	text, _, _ := sapu(t, e, "", "health")
	checkHuman(t, text)
	if !strings.Contains(text, "desktop Mac") || !strings.Contains(text, "Nothing needs attention.") {
		t.Errorf("text:\n%s", text)
	}

	// Every command fails: still one valid document and exit 0.
	health.RunCommand = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("failed") }
	out, _, code = sapu(t, e, "", "health", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	validate(t, "health", out)
	text, _, _ = sapu(t, e, "", "health")
	checkHuman(t, text)
	if !strings.Contains(text, "Battery: not available") {
		t.Errorf("text:\n%s", text)
	}
}

// --- updates --------------------------------------------------------------

const m3Brew = `{"formulae":[],"casks":[{"name":"ghostty","installed_versions":["1.3.1"],"current_version":"1.4.0"},{"name":"ngrok","installed_versions":["3.39.11,abc"],"current_version":"3.39.13,def"}]}`

func m3Appcast(short string) string {
	return `<?xml version="1.0"?><rss version="2.0" xmlns:sparkle="http://www.andymatuschak.org/xml-namespaces/sparkle"><channel>
<item><enclosure url="https://x.example/a.zip" sparkle:version="2" sparkle:shortVersionString="` + short + `"/></item></channel></rss>`
}

// m3UpdatesMac fakes brew, mas and an update server. It returns the server's URL.
func m3UpdatesMac(t *testing.T, dir string, have ...string) string {
	t.Helper()
	pointAppDirs(t, dir)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/newer.xml":
			fmt.Fprint(w, m3Appcast("2.0"))
		case "/slow.xml":
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	oldRun, oldLook, oldTransport, oldTimeout := updates.RunCommand, updates.LookPath, updates.Transport, updates.Timeout
	updates.Transport = srv.Client().Transport
	updates.Timeout = 300 * time.Millisecond
	updates.LookPath = func(name string) (string, error) {
		if slices.Contains(have, name) {
			return "/fake/" + name, nil
		}
		return "", exec.ErrNotFound
	}
	updates.RunCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch strings.Join(append([]string{name}, args...), " ") {
		case "env HOMEBREW_NO_AUTO_UPDATE=1 brew outdated --cask --greedy --json=v2":
			return []byte(m3Brew), nil
		case "mas outdated":
			return []byte("497799835 Xcode (14.1 -> 14.2)\n"), nil
		}
		return nil, errors.New("unexpected command")
	}
	t.Cleanup(func() {
		srv.Close()
		updates.RunCommand, updates.LookPath, updates.Transport, updates.Timeout = oldRun, oldLook, oldTransport, oldTimeout
	})
	return srv.URL
}

func TestUpdatesJSONAndText(t *testing.T) {
	dir := t.TempDir()
	url := m3UpdatesMac(t, dir, "brew", "mas")
	m3App(t, dir, "Newer", "1.0", url+"/newer.xml")
	m3App(t, dir, "Slow", "1.0", url+"/slow.xml")
	m3App(t, dir, "Insecure", "1.0", "http://example.invalid/feed.xml")
	e := env.New(t.TempDir())

	out, _, code := sapu(t, e, "", "updates", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	validate(t, "updates", out)
	var doc struct{ Data updatesData }
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, u := range doc.Data.Updates {
		names = append(names, u.Name)
	}
	if !slices.Equal(names, []string{"ghostty", "Newer", "ngrok", "Xcode"}) {
		t.Errorf("updates: %v", names)
	}
	if len(doc.Data.Notes) != 2 || doc.Data.Offline {
		t.Errorf("notes %+v, offline %v", doc.Data.Notes, doc.Data.Offline)
	}

	text, _, code := sapu(t, e, "", "updates")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, text)
	}
	checkHuman(t, text)
	for _, want := range []string{
		"4 updates available",
		"ngrok", "3.39.11", "3.39.13",
		"brew upgrade --cask ghostty ngrok",
		"Mac App Store apps: open the App Store",
		"Other apps: open the app and choose Check for Updates.",
		"Slow: the update server did not answer within 0.3 seconds.",
		"Insecure: the update feed is not an HTTPS address",
		"installs anything",
		"Checked the update feeds of 3 apps.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text has no %q:\n%s", want, text)
		}
	}
}

func TestUpdatesOfflineAndNothingFound(t *testing.T) {
	dir := t.TempDir()
	m3UpdatesMac(t, dir) // no brew, no mas
	m3App(t, dir, "App", "1.0", "https://updates.invalid/feed.xml")
	e := env.New(t.TempDir())

	out, _, code := sapu(t, e, "", "updates", "--offline", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	validate(t, "updates", out)
	var doc struct{ Data updatesData }
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if !doc.Data.Offline || len(doc.Data.Updates) != 0 || len(doc.Data.Notes) != 0 {
		t.Errorf("data: %+v", doc.Data)
	}

	text, _, _ := sapu(t, e, "", "updates", "--offline")
	checkHuman(t, text)
	for _, want := range []string{"No updates found.", "Homebrew: Homebrew is not installed", "skipped because of --offline", "Offline: Sapu made no network request."} {
		if !strings.Contains(text, want) {
			t.Errorf("text has no %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "asked each app") {
		t.Errorf("offline text says the servers were asked:\n%s", text)
	}
}
