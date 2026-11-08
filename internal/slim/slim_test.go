package slim

import (
	"context"
	"crypto/sha256"
	"debug/macho"
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/apps"
	"github.com/khmuhtadin/sapu/internal/env"
)

// thin builds a minimal 64-bit Mach-O file of size bytes for cpu.
func thin(cpu macho.Cpu, size int) []byte {
	b := make([]byte, size)
	binary.LittleEndian.PutUint32(b[0:], 0xfeedfacf) // MH_MAGIC_64
	binary.LittleEndian.PutUint32(b[4:], uint32(cpu))
	binary.LittleEndian.PutUint32(b[8:], subCpu(cpu))
	binary.LittleEndian.PutUint32(b[12:], 2) // MH_EXECUTE
	return b
}

// subCpu is the "all" subtype of a cpu.
func subCpu(cpu macho.Cpu) uint32 {
	if cpu == macho.CpuAmd64 {
		return 3 // CPU_SUBTYPE_X86_64_ALL
	}
	return 0 // CPU_SUBTYPE_ARM64_ALL
}

// fat builds a fat file with one thin slice per cpu; sizes[i] is the size of slice i.
func fat(cpus []macho.Cpu, sizes []int) []byte {
	const align = 1 << 14
	header := make([]byte, 8+20*len(cpus))
	binary.BigEndian.PutUint32(header[0:], 0xcafebabe)
	binary.BigEndian.PutUint32(header[4:], uint32(len(cpus)))
	var body []byte // everything after the header, slices aligned
	for i, cpu := range cpus {
		offset := len(header) + len(body)
		for offset%align != 0 {
			body = append(body, 0)
			offset++
		}
		entry := header[8+20*i:]
		binary.BigEndian.PutUint32(entry[0:], uint32(cpu))
		binary.BigEndian.PutUint32(entry[4:], subCpu(cpu))
		binary.BigEndian.PutUint32(entry[8:], uint32(offset))
		binary.BigEndian.PutUint32(entry[12:], uint32(sizes[i]))
		binary.BigEndian.PutUint32(entry[16:], 14)
		body = append(body, thin(cpu, sizes[i])...)
	}
	return append(header, body...)
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestForeignBytes(t *testing.T) {
	dir := t.TempDir()
	both := filepath.Join(dir, "both")
	writeFile(t, both, fat([]macho.Cpu{macho.CpuAmd64, macho.CpuArm64}, []int{30000, 20000}))
	intelOnly := filepath.Join(dir, "intel-only")
	writeFile(t, intelOnly, fat([]macho.Cpu{macho.CpuAmd64}, []int{30000}))
	plain := filepath.Join(dir, "thin")
	writeFile(t, plain, thin(macho.CpuAmd64, 5000))
	text := filepath.Join(dir, "script.sh")
	writeFile(t, text, []byte("#!/bin/sh\necho hello, this is a script\n"))
	// A Java class file starts with the same magic as a fat file.
	class := filepath.Join(dir, "A.class")
	writeFile(t, class, append([]byte{0xca, 0xfe, 0xba, 0xbe, 0, 0, 0, 52}, make([]byte, 400)...))

	cases := []struct {
		name            string
		path            string
		native, foreign macho.Cpu
		want            int64
	}{
		{"apple silicon counts the intel slice", both, macho.CpuArm64, macho.CpuAmd64, 30000},
		{"intel counts the arm slice", both, macho.CpuAmd64, macho.CpuArm64, 20000},
		{"a file without a slice for this Mac counts nothing", intelOnly, macho.CpuArm64, macho.CpuAmd64, 0},
		{"a thin file counts nothing", plain, macho.CpuArm64, macho.CpuAmd64, 0},
		{"a script counts nothing", text, macho.CpuArm64, macho.CpuAmd64, 0},
		{"a java class counts nothing", class, macho.CpuArm64, macho.CpuAmd64, 0},
		{"a missing file counts nothing", filepath.Join(dir, "missing"), macho.CpuArm64, macho.CpuAmd64, 0},
	}
	for _, c := range cases {
		if got := foreignBytes(c.path, c.native, c.foreign); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestLanguageCode(t *testing.T) {
	for name, want := range map[string]string{
		"en": "en", "en-US": "en", "en_GB": "en", "id-ID": "id", "pt_BR": "pt",
		"zh-Hans": "zh", "zh-Hans-CN": "zh", "English": "en", "French": "fr", "fil": "fil",
	} {
		if got, ok := languageCode(name); !ok || got != want {
			t.Errorf("languageCode(%q) = %q, %v; want %q", name, got, ok, want)
		}
	}
	for _, name := range []string{"Base", "base", "", "x", "weird_name", "Resources", "en1"} {
		if got, ok := languageCode(name); ok {
			t.Errorf("languageCode(%q) = %q, want it unrecognised", name, got)
		}
	}
}

func TestParseLanguages(t *testing.T) {
	got := parseLanguages("(\n    \"en-ID\",\n    \"id-ID\"\n)\n")
	if !slices.Equal(got, []string{"en-ID", "id-ID"}) {
		t.Errorf("got %q", got)
	}
}

// fakeApp builds dir/name.app with a real Info.plist.
func fakeApp(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name+".app")
	writeFile(t, filepath.Join(p, "Contents", "Info.plist"), []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>test.`+name+`</string>
<key>CFBundleName</key><string>`+name+`</string>
<key>CFBundleExecutable</key><string>`+name+`</string>
</dict></plist>
`))
	return p
}

// setup points Scan at dirs and fakes the Mac: arch and languages.
func setup(t *testing.T, arch string, langs []string, dirs ...string) {
	t.Helper()
	oldDirs, oldArch, oldLangs := apps.Dirs, Arch, Languages
	apps.Dirs, Arch = dirs, arch
	Languages = func(context.Context) ([]string, error) { return langs, nil }
	t.Cleanup(func() { apps.Dirs, Arch, Languages = oldDirs, oldArch, oldLangs })
}

// snapshot records every path under root with its mode and a hash of its content.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		desc := info.Mode().String()
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			desc += " " + hash(data)
		}
		snap[p] = desc
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func hash(data []byte) string {
	sum := sha256.Sum256(data)
	return string(sum[:])
}

func TestScanApple(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Applications")

	// Big has a universal executable, a universal helper inside a framework,
	// and four language folders; only fr, de and French are unused.
	big := fakeApp(t, dir, "Big")
	writeFile(t, filepath.Join(big, "Contents", "MacOS", "Big"), fat([]macho.Cpu{macho.CpuAmd64, macho.CpuArm64}, []int{40000, 30000}))
	writeFile(t, filepath.Join(big, "Contents", "Frameworks", "F.framework", "Versions", "A", "F"), fat([]macho.Cpu{macho.CpuArm64, macho.CpuAmd64}, []int{10000, 15000}))
	writeFile(t, filepath.Join(big, "Contents", "Resources", "en.lproj", "Localizable.strings"), make([]byte, 1000))
	writeFile(t, filepath.Join(big, "Contents", "Resources", "id.lproj", "Localizable.strings"), make([]byte, 1100))
	writeFile(t, filepath.Join(big, "Contents", "Resources", "Base.lproj", "Main.nib"), make([]byte, 1200))
	writeFile(t, filepath.Join(big, "Contents", "Resources", "fr.lproj", "Localizable.strings"), make([]byte, 2000))
	writeFile(t, filepath.Join(big, "Contents", "Resources", "fr.lproj", "InfoPlist.strings"), make([]byte, 500))
	writeFile(t, filepath.Join(big, "Contents", "Resources", "French.lproj", "Old.strings"), make([]byte, 300))
	writeFile(t, filepath.Join(big, "Contents", "Frameworks", "F.framework", "Versions", "A", "Resources", "de.lproj", "x.strings"), make([]byte, 4000))
	// A folder Sapu cannot read as a language is not counted.
	writeFile(t, filepath.Join(big, "Contents", "Resources", "weird_name.lproj", "x"), make([]byte, 9000))
	// A symlink to the framework version must not count the file twice.
	if err := os.Symlink("A", filepath.Join(big, "Contents", "Frameworks", "F.framework", "Versions", "Current")); err != nil {
		t.Fatal(err)
	}

	// Small has only an Intel-only helper and a second en folder: nothing to save.
	small := fakeApp(t, dir, "Small")
	writeFile(t, filepath.Join(small, "Contents", "MacOS", "Small"), thin(macho.CpuAmd64, 8000))
	writeFile(t, filepath.Join(small, "Contents", "Resources", "en_US.lproj", "x"), make([]byte, 100))

	// Mid is one folder deep, in ~/Applications-style layout.
	mid := fakeApp(t, filepath.Join(dir, "Suite"), "Mid")
	writeFile(t, filepath.Join(mid, "Contents", "MacOS", "Mid"), fat([]macho.Cpu{macho.CpuAmd64, macho.CpuArm64}, []int{5000, 5000}))

	setup(t, "arm64", []string{"en-US", "id-ID"}, dir)
	before := snapshot(t, root)

	r, err := Scan(context.Background(), env.New(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if r.Arch != "arm64" || r.ForeignArch != "x86_64" {
		t.Errorf("arch %q, foreign %q", r.Arch, r.ForeignArch)
	}
	if r.Scanned != 3 {
		t.Errorf("scanned %d apps, want 3", r.Scanned)
	}
	if len(r.Apps) != 2 {
		t.Fatalf("got %d apps with savings, want 2 (Big and Mid): %+v", len(r.Apps), r.Apps)
	}
	if a := r.Apps[0]; a.Name != "Big" || a.ForeignBytes != 40000+15000 || a.LanguageBytes != 2500+300+4000 {
		t.Errorf("Big: %+v", a)
	}
	if a := r.Apps[1]; a.Name != "Mid" || a.ForeignBytes != 5000 || a.LanguageBytes != 0 {
		t.Errorf("Mid: %+v", a)
	}
	foreign, languages := r.Total()
	if foreign != 60000 || languages != 6800 {
		t.Errorf("total foreign %d, languages %d", foreign, languages)
	}

	// Nothing was changed.
	after := snapshot(t, root)
	if len(after) != len(before) {
		t.Errorf("%d paths before, %d after", len(before), len(after))
	}
	for p, desc := range before {
		if after[p] != desc {
			t.Errorf("%s changed", p)
		}
	}
}

func TestScanIntel(t *testing.T) {
	dir := t.TempDir()
	app := fakeApp(t, dir, "Both")
	writeFile(t, filepath.Join(app, "Contents", "MacOS", "Both"), fat([]macho.Cpu{macho.CpuAmd64, macho.CpuArm64}, []int{40000, 30000}))
	setup(t, "amd64", []string{"en"}, dir)

	r, err := Scan(context.Background(), env.New(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if r.Arch != "x86_64" || r.ForeignArch != "arm64" {
		t.Errorf("arch %q, foreign %q", r.Arch, r.ForeignArch)
	}
	if len(r.Apps) != 1 || r.Apps[0].ForeignBytes != 30000 {
		t.Errorf("apps: %+v", r.Apps)
	}
}

func TestScanWithoutLanguagesFails(t *testing.T) {
	dir := t.TempDir()
	fakeApp(t, dir, "A")
	setup(t, "arm64", nil, dir)
	if _, err := Scan(context.Background(), env.New(t.TempDir())); err == nil || !strings.Contains(err.Error(), "languages") {
		t.Errorf("no languages: error %v", err)
	}

	Languages = func(context.Context) ([]string, error) { return nil, errors.New("defaults failed") }
	if _, err := Scan(context.Background(), env.New(t.TempDir())); err == nil || !strings.Contains(err.Error(), "defaults failed") {
		t.Errorf("failing defaults: error %v", err)
	}
}

func TestScanStopsWhenCancelled(t *testing.T) {
	dir := t.TempDir()
	app := fakeApp(t, dir, "A")
	writeFile(t, filepath.Join(app, "Contents", "MacOS", "A"), []byte("x"))
	setup(t, "arm64", []string{"en"}, dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Scan(ctx, env.New(t.TempDir())); !errors.Is(err, context.Canceled) {
		t.Errorf("error %v, want context.Canceled", err)
	}
}

// The fixture is a real fat file: lipo, Apple's own tool, reads the same
// slice sizes.
func TestFixtureMatchesLipo(t *testing.T) {
	if _, err := exec.LookPath("lipo"); err != nil {
		t.Skip("lipo is not installed")
	}
	p := filepath.Join(t.TempDir(), "both")
	writeFile(t, p, fat([]macho.Cpu{macho.CpuAmd64, macho.CpuArm64}, []int{30000, 20000}))
	out, err := exec.Command("lipo", "-detailed_info", p).CombinedOutput()
	if err != nil {
		t.Fatalf("lipo: %v\n%s", err, out)
	}
	for _, want := range []string{"architecture x86_64", "size 30000", "architecture arm64", "size 20000"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("lipo output has no %q:\n%s", want, out)
		}
	}
}
