package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
)

func tidyMkdir(t *testing.T, e env.Env, rel string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(e.Home, rel), 0o755); err != nil {
		t.Fatal(err)
	}
}

func tidyLink(t *testing.T, e env.Env, rel, target string) {
	t.Helper()
	tidyMkdir(t, e, filepath.Dir(rel))
	if err := os.Symlink(target, filepath.Join(e.Home, rel)); err != nil {
		t.Fatal(err)
	}
}

// tidySnapshot is snapshot that also copes with broken symlinks: it records
// folders, symlinks with their target, and files with their content.
func tidySnapshot(t *testing.T, e env.Env) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(e.Home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == e.StateDir {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(e.Home, p)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			snap[rel] = "dir " + info.Mode().String()
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			snap[rel] = "link " + target
		default:
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			snap[rel] = info.Mode().String() + " " + string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// tidyHome has seven things to tidy, and many that look like them but must
// not be listed.
func tidyHome(t *testing.T) env.Env {
	t.Helper()
	e := env.New(t.TempDir())

	// to tidy
	tidyMkdir(t, e, "Downloads/empty")
	tidyMkdir(t, e, "Downloads/only-ds")
	writeFile(t, filepath.Join(e.Home, "Downloads/only-ds/.DS_Store"), "finder")
	tidyMkdir(t, e, "Downloads/nest/a/b")
	writeFile(t, filepath.Join(e.Home, "Downloads/nest/a/.DS_Store"), "finder")
	writeFile(t, filepath.Join(e.Home, "Downloads/nest/a/b/.DS_Store"), "finder")
	tidyMkdir(t, e, "Downloads/mixed/hollow")
	writeFile(t, filepath.Join(e.Home, "Downloads/mixed/keep.txt"), "data")
	tidyLink(t, e, "Downloads/dead", filepath.Join(e.Home, "nowhere"))
	tidyLink(t, e, "Downloads/linkdir/dead2", "missing-file")
	writeFile(t, filepath.Join(e.Home, "Downloads/zero.txt"), "")

	// not to tidy
	writeFile(t, filepath.Join(e.Home, "Downloads/real.txt"), "data")
	tidyLink(t, e, "Downloads/alive", filepath.Join(e.Home, "Downloads/real.txt"))
	for _, name := range []string{".hidden", "Icon\r", "x.lock", "y.PID", ".keep", ".gitkeep"} {
		writeFile(t, filepath.Join(e.Home, "Downloads/marks", name), "")
	}
	tidyMkdir(t, e, "Downloads/loc")
	writeFile(t, filepath.Join(e.Home, "Downloads/loc/.localized"), "") // a dotfile is content
	tidyMkdir(t, e, "Downloads/node_modules/pkg/empty")
	tidyMkdir(t, e, "Downloads/Thing.app/Contents/empty")
	writeFile(t, filepath.Join(e.Home, "Downloads/Thing.app/Contents/zero"), "")
	tidyMkdir(t, e, "Downloads/Lib.photoslibrary/empty")
	tidyMkdir(t, e, "Documents/proj/.git")
	tidyMkdir(t, e, "Documents/proj/empty")
	tidyLink(t, e, "Documents/proj/dead", "gone")
	writeFile(t, filepath.Join(e.Home, "Documents/.DS_Store"), "finder") // a root that is empty is never listed
	tidyMkdir(t, e, "Library/Caches/empty")
	tidyMkdir(t, e, "Pictures/empty") // not a default folder
	return e
}

func tidyWant(e env.Env) []string {
	return dupesPaths(e, "Downloads/empty", "Downloads/only-ds", "Downloads/nest", "Downloads/mixed/hollow",
		"Downloads/dead", "Downloads/linkdir/dead2", "Downloads/zero.txt")
}

func TestTidyListsExactlyTheExpectedItems(t *testing.T) {
	e := tidyHome(t)
	before := tidySnapshot(t, e)

	out, errOut, code := sapu(t, e, "", "tidy")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	t.Logf("sapu tidy\n%s", out)
	checkHuman(t, out)
	for _, line := range []string{
		"empty folder  ~/Downloads/empty", "~/Downloads/only-ds", "~/Downloads/nest\n", "~/Downloads/mixed/hollow",
		"broken link   ~/Downloads/dead", "~/Downloads/linkdir/dead2", "empty file    ~/Downloads/zero.txt",
		"4 empty folders, 2 broken links, 1 empty file.",
		"Nothing was changed. Pick with --pick 1,3 or --pick-all.",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("output lacks %q:\n%s", line, out)
		}
	}
	for _, not := range []string{"nest/a", "alive", "real.txt", "marks", "loc", "node_modules", "Thing.app", "photoslibrary", "proj", "Library", "Pictures", "~/Documents\n"} {
		if strings.Contains(out, not) {
			t.Errorf("%s is listed:\n%s", not, out)
		}
	}
	if !slices.Equal(before2(tidySnapshot(t, e)), before2(before)) {
		t.Error("the list changed the disk")
	}
	noState(t, e)

	file := filepath.Join(t.TempDir(), "plan.json")
	if _, _, code := sapu(t, e, "", "tidy", "--plan", file); code != 0 {
		t.Fatal("tidy --plan failed")
	}
	p := readPlanFile(t, file)
	if len(p.Items) != 7 || len(selectedPaths(p)) != 0 {
		t.Fatalf("plan has %d items, %d selected", len(p.Items), len(selectedPaths(p)))
	}
	var got []string
	for _, it := range p.Items {
		got = append(got, it.Path)
		if it.Source != "tidy" || it.Risk != "risky" || it.Explain == "" || it.KeepCopy != nil {
			t.Errorf("item %+v", it)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, tidyWant(e)) {
		t.Errorf("items %q, want %q", got, tidyWant(e))
	}
	if p.Kind != "tidy" {
		t.Errorf("kind %q", p.Kind)
	}
}

// before2 turns a snapshot into a sorted list, so slices.Equal can compare two.
func before2(snap map[string]string) []string {
	var lines []string
	for k, v := range snap {
		lines = append(lines, k+"="+v)
	}
	slices.Sort(lines)
	return lines
}

func TestTidyExplainsEachKind(t *testing.T) {
	e := tidyHome(t)
	file := filepath.Join(t.TempDir(), "plan.json")
	if _, _, code := sapu(t, e, "", "tidy", "--plan", file); code != 0 {
		t.Fatal("tidy --plan failed")
	}
	for _, it := range readPlanFile(t, file).Items {
		var want string
		switch {
		case strings.HasSuffix(it.Path, "zero.txt"):
			want = "A file with nothing in it."
		case strings.HasSuffix(it.Path, "dead"):
			want = "A link to " + filepath.Join(e.Home, "nowhere") + ", which does not exist any more."
		case strings.HasSuffix(it.Path, "dead2"):
			want = "A link to missing-file, which does not exist any more."
		default:
			want = "An empty folder (only Finder's .DS_Store files). Some apps expect their folders; undo puts it back."
		}
		if !strings.Contains(it.Explain, want) {
			t.Errorf("%s: explain %q, want %q", it.Path, it.Explain, want)
		}
	}
}

func TestTidyPickAllMovesEverythingAndUndoRestoresIdentically(t *testing.T) {
	e := tidyHome(t)
	before := tidySnapshot(t, e)

	out, errOut, code := sapu(t, e, "", "tidy", "--pick-all", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "Moved 7 items") {
		t.Errorf("output lacks the result:\n%s", out)
	}
	for _, path := range tidyWant(e) {
		if _, err := os.Lstat(path); err == nil {
			t.Errorf("%s was not moved", path)
		}
	}
	for _, rel := range []string{"Downloads/real.txt", "Downloads/alive", "Downloads/marks/.hidden", "Downloads/loc/.localized", "Downloads/mixed/keep.txt", "Documents/proj/dead", "Downloads", "Documents"} {
		if _, err := os.Lstat(filepath.Join(e.Home, rel)); err != nil {
			t.Errorf("%s is gone", rel)
		}
	}

	if _, _, code := sapu(t, e, "", "undo", "--last"); code != 0 {
		t.Fatal("undo failed")
	}
	after := tidySnapshot(t, e)
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s: %q after undo, was %q", k, after[k], v)
		}
	}
	if len(after) != len(before) {
		t.Errorf("%d entries after undo, was %d", len(after), len(before))
	}
}

func TestTidyPickByNumber(t *testing.T) {
	e := tidyHome(t)
	// numbers: folders by path (1 empty, 2 mixed/hollow, 3 nest, 4 only-ds), links (5 dead, 6 dead2), 7 zero.txt
	out, errOut, code := sapu(t, e, "y\n", "tidy", "--pick", "5,7")
	if code != 0 || !strings.Contains(out, "Moved 2 items") {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if exists(filepath.Join(e.Home, "Downloads/dead")) || exists(filepath.Join(e.Home, "Downloads/zero.txt")) {
		t.Error("picked items were not moved")
	}
	if !exists(filepath.Join(e.Home, "Downloads/empty")) || !exists(filepath.Join(e.Home, "Downloads/nest")) {
		t.Error("items that were not picked are gone")
	}
	if _, _, code := sapu(t, e, "", "tidy", "--pick", "9", "--yes"); code != 2 {
		t.Errorf("--pick 9: exit %d, want 2", code)
	}
}

func TestTidyDirArgumentsAndNothingFound(t *testing.T) {
	e := tidyHome(t)
	out, _, code := sapu(t, e, "", "tidy", filepath.Join(e.Home, "Pictures"))
	if code != 0 || !strings.Contains(out, "No empty folders, broken links or empty files found.") {
		t.Errorf("a root that is empty is never listed: exit %d\n%s", code, out)
	}
	out, _, code = sapu(t, e, "", "tidy", filepath.Join(e.Home, "Downloads/nest"), filepath.Join(e.Home, "Downloads/nest"))
	if code != 0 || !strings.Contains(out, "No empty folders") {
		t.Errorf("exit %d\n%s", code, out)
	}
	for _, dir := range []string{"Library/Caches", "Downloads/Thing.app", "Downloads/node_modules", "Documents/proj", "Documents/proj/empty", "nowhere"} {
		if _, _, code := sapu(t, e, "", "tidy", filepath.Join(e.Home, dir)); code == 0 {
			t.Errorf("%s was searched", dir)
		}
	}
	if _, _, code := sapu(t, e, "", "tidy", "--pick", "1", "--pick-all"); code != 2 {
		t.Errorf("--pick with --pick-all: exit %d, want 2", code)
	}
	if _, _, code := sapu(t, e, "", "tidy", "--pick-all", "--json"); code != 2 {
		t.Errorf("--pick-all --json without --yes: exit %d, want 2", code)
	}
}

func TestTidyJSON(t *testing.T) {
	e := tidyHome(t)

	out, errOut, code := sapu(t, e, "", "tidy", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "tidy", out)
	var d tidyList
	decodeData(t, out, &d)
	if len(d.Items) != 7 || d.Items[0].Number != 1 || d.Items[0].Kind != "empty-folder" || d.Items[6].Kind != "empty-file" {
		t.Errorf("data %+v", d)
	}

	file := filepath.Join(t.TempDir(), "plan.json")
	out, errOut, code = sapu(t, e, "", "tidy", "--plan", file, "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "tidy", out)

	out, errOut, code = sapu(t, e, "", "tidy", "--pick-all", "--yes", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "tidy", out)
	var run runData
	decodeData(t, out, &run)
	if run.Result == nil || len(run.Result.Moved) != 7 {
		t.Errorf("data %+v", run)
	}

	empty := env.New(t.TempDir())
	out, _, code = sapu(t, empty, "", "tidy", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	validate(t, "tidy", out)
	if !strings.Contains(out, `"items": []`) && !strings.Contains(out, `"items":[]`) {
		t.Errorf("items is not an empty list:\n%s", out)
	}
}
