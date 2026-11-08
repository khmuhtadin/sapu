package regression

import (
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/protect"
	"github.com/khmuhtadin/sapu/internal/rules"
	ruledata "github.com/khmuhtadin/sapu/rules"
)

// protectedCase is a fake home holding data that a cleaner once deleted.
type protectedCase struct {
	files   map[string]string // path relative to the home -> contents
	links   map[string]string // symlink relative to the home -> target
	keep    []string          // folders that must survive byte-identical
	skipped []string          // items the aggressive rules produce that Plan must skip as protected
}

// buildCase creates the case's fake home plus the junk cache.
func buildCase(t *testing.T, c protectedCase) env.Env {
	t.Helper()
	e := newHome(t, merge(c.files, map[string]string{junkFile: "ordinary cache"}))
	for rel, target := range c.links {
		symlink(t, homePath(e, rel), target)
	}
	fixTimes(t, e.Home)
	return e
}

// requireProtected runs the case twice, each time in a fresh fake home:
//   - with the aggressive rules, which aim straight at the data: the data
//     survives, Plan reports its items as skipped "protected", and the junk
//     cache is moved;
//   - with the real embedded rules: the data survives.
func requireProtected(t *testing.T, c protectedCase) {
	t.Helper()
	t.Run("aggressive rules", func(t *testing.T) {
		t.Parallel()
		e := buildCase(t, c)
		before := snapshotEach(t, e, c.keep)

		p, res := clean(t, e, aggressiveRules())

		for _, rel := range c.skipped {
			requireSkip(t, e, p.Skipped, rel, plan.ReasonProtected)
			requireNotPlanned(t, e, p, rel)
		}
		requireSurvived(t, e, before)
		requireMoved(t, e, res, junkItem)
	})
	t.Run("embedded rules", func(t *testing.T) {
		t.Parallel()
		e := buildCase(t, c)
		before := snapshotEach(t, e, c.keep)

		clean(t, e, embeddedRules(t))

		requireSurvived(t, e, before)
	})
}

// Case 1, V2EX 1163304: a cleaner wiped DataGrip's data sources and query
// consoles because they sit next to JetBrains caches.
var dataGripCase = protectedCase{
	files: map[string]string{
		"Library/Application Support/JetBrains/DataGrip2026.2/options/dataSources.xml": `<?xml version="1.0" encoding="UTF-8"?>
<project version="4">
  <component name="DataSourceManagerImpl" format="xml" multifile-model="true">
    <data-source source="LOCAL" name="production@db.internal" uuid="5c1e2f7a">
      <jdbc-url>jdbc:postgresql://db.internal:5432/production</jdbc-url>
    </data-source>
  </component>
</project>
`,
		"Library/Application Support/JetBrains/DataGrip2026.2/options/dataSources.local.xml":    "<application/>",
		"Library/Application Support/JetBrains/DataGrip2026.2/consoles/db/5c1e2f7a/console.sql": "select * from orders where id = 42;\n",
		"Library/Caches/JetBrains/DataGrip2026.2/caches/content.dat":                            "index",
		"Library/Caches/JetBrains/DataGrip2026.2/log/idea.log":                                  "log",
	},
	keep:    []string{"Library/Application Support/JetBrains", "Library/Caches/JetBrains"},
	skipped: []string{"Library/Application Support/JetBrains", "Library/Caches/JetBrains"},
}

func TestDataGripDataSourcesSurvive(t *testing.T) {
	t.Parallel()
	requireProtected(t, dataGripCase)
}

// Case 2, V2EX 1163304: downloaded HuggingFace models in ~/.cache are data,
// slow and large to download again. The hub stores snapshots as symlinks.
var huggingFaceCase = protectedCase{
	files: map[string]string{
		".cache/huggingface/hub/models--bert-base-uncased/blobs/9a3f1c":       "model weights",
		".cache/huggingface/hub/models--bert-base-uncased/refs/main":          "86b5e0934494bd15c9632b12f734a8a67f723594",
		".cache/huggingface/hub/models--bert-base-uncased/snapshots/86b5e09/": "",
		".cache/huggingface/token":                                            "hf_secret",
	},
	links: map[string]string{
		".cache/huggingface/hub/models--bert-base-uncased/snapshots/86b5e09/model.safetensors": "../../blobs/9a3f1c",
	},
	keep:    []string{".cache/huggingface"},
	skipped: []string{".cache/huggingface"},
}

func TestHuggingFaceModelsSurvive(t *testing.T) {
	t.Parallel()
	requireProtected(t, huggingFaceCase)
}

// Case 3, V2EX 1163304: GitHub Desktop keeps accounts and the repository
// list in Application Support.
var gitHubDesktopCase = protectedCase{
	files: map[string]string{
		"Library/Application Support/GitHub Desktop/Local Storage/leveldb/000005.ldb":               "repositories",
		"Library/Application Support/GitHub Desktop/IndexedDB/file__0.indexeddb.leveldb/000003.log": "accounts",
		"Library/Application Support/GitHub Desktop/window-state.json":                              `{"width":1200}`,
	},
	keep:    []string{"Library/Application Support/GitHub Desktop"},
	skipped: []string{"Library/Application Support/GitHub Desktop"},
}

func TestGitHubDesktopConfigSurvives(t *testing.T) {
	t.Parallel()
	requireProtected(t, gitHubDesktopCase)
}

// Case 4, Mole #823: Claude Code's project memory was deleted as if it were
// a cache.
var claudeMemoryCase = protectedCase{
	files: map[string]string{
		".claude/projects/-proj-x/memory/MEMORY.md": "- [Prefers simple code](prefs.md)\n",
		".claude/projects/-proj-x/memory/prefs.md":  "The user prefers simple code.\n",
		".claude/projects/-proj-x/7f3a9c.jsonl":     `{"type":"user"}` + "\n",
		".claude/settings.json":                     `{"model":"opus"}`,
	},
	keep:    []string{".claude"},
	skipped: []string{".claude/projects/-proj-x/memory", ".claude/projects", ".claude/settings.json"},
}

func TestClaudeCodeMemorySurvives(t *testing.T) {
	t.Parallel()
	requireProtected(t, claudeMemoryCase)
}

// Case 5, Mole #856: the Apple Notes database lives in a group container.
var appleNotesCase = protectedCase{
	files: map[string]string{
		"Library/Group Containers/group.com.apple.notes/NoteStore.sqlite":                   "notes",
		"Library/Group Containers/group.com.apple.notes/NoteStore.sqlite-wal":               "wal",
		"Library/Group Containers/group.com.apple.notes/Accounts/3B2C/Media/1A/photo.jpg":   "jpeg",
		"Library/Containers/com.apple.Notes/Data/Library/Preferences/com.apple.Notes.plist": "plist",
	},
	keep:    []string{"Library/Group Containers/group.com.apple.notes", "Library/Containers/com.apple.Notes"},
	skipped: []string{"Library/Group Containers/group.com.apple.notes", "Library/Containers/com.apple.Notes"},
}

func TestAppleNotesSurvive(t *testing.T) {
	t.Parallel()
	requireProtected(t, appleNotesCase)
}

// Case 6, Mole #32: Apple Mail messages and the Mail container were removed.
var appleMailCase = protectedCase{
	files: map[string]string{
		"Library/Mail/V10/MailData/Envelope Index":                                        "index",
		"Library/Mail/V10/6E1D/INBOX.mbox/0F2A/Data/Messages/1.emlx":                      "From: boss\n\nKeep this.",
		"Library/Containers/com.apple.mail/Data/Library/Mail Downloads/invoice.pdf":       "%PDF-1.7",
		"Library/Containers/com.apple.mail/Data/Library/Preferences/com.apple.mail.plist": "plist",
	},
	keep:    []string{"Library/Mail", "Library/Containers/com.apple.mail"},
	skipped: []string{"Library/Mail/V10", "Library/Containers/com.apple.mail"},
}

func TestAppleMailSurvives(t *testing.T) {
	t.Parallel()
	requireProtected(t, appleMailCase)
}

// Case 7, Mole #1941 (Camoufox) and Playwright: browsers installed for
// automation live under ~/Library/Caches, and tools break without them.
var browsersCase = protectedCase{
	files: map[string]string{
		"Library/Caches/camoufox/version.json":                                                   `{"version":"135.0"}`,
		"Library/Caches/camoufox/Camoufox.app/Contents/MacOS/camoufox":                           "binary",
		"Library/Caches/ms-playwright/chromium-1140/chrome-mac/Chromium.app/Contents/Info.plist": "plist",
		"Library/Caches/ms-playwright/.links/9c1e":                                               "project",
	},
	keep:    []string{"Library/Caches/camoufox", "Library/Caches/ms-playwright"},
	skipped: []string{"Library/Caches/camoufox", "Library/Caches/ms-playwright"},
}

func TestCamoufoxAndPlaywrightBrowsersSurvive(t *testing.T) {
	t.Parallel()
	requireProtected(t, browsersCase)
}

// fixedProtectedPaths are the entries of rules/protect/core.yml at Wave 0.
// They are fixed: dropping one fails TestEveryProtectedEntrySurvives. New
// entries are welcome and are tested automatically.
var fixedProtectedPaths = []string{
	"~/Library/Application Support/JetBrains",
	"~/Library/Caches/JetBrains",
	"~/Library/Application Support/GitHub Desktop",
	"~/.cache/huggingface",
	"~/.ollama",
	"~/.lmstudio",
	"~/.cache/lm-studio",
	"~/.cache/whisper",
	"~/.claude",
	"~/.claude.json",
	"~/Library/Application Support/Claude",
	"~/.codex",
	"~/.cursor",
	"~/Library/Application Support/Cursor/User",
	"~/Library/Caches/ms-playwright",
	"~/Library/Caches/camoufox",
	"~/Library/Mail",
	"~/Library/Containers/com.apple.mail",
	"~/Library/Group Containers/group.com.apple.notes",
	"~/Library/Containers/com.apple.Notes",
	"~/Library/Messages",
	"~/Pictures/*.photoslibrary",
	"~/Library/Mobile Documents",
	"~/Library/Keychains",
	"~/.ssh",
	"~/.gnupg",
}

// protectedEntries parses every file of the embedded protection list.
func protectedEntries(t *testing.T) []protect.Entry {
	t.Helper()
	names, err := fs.Glob(ruledata.FS, "protect/*.yml")
	if err != nil {
		t.Fatal(err)
	}
	var all []protect.Entry
	for _, name := range names {
		data, err := fs.ReadFile(ruledata.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		var list []protect.Entry
		if err := yaml.Unmarshal(data, &list); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		all = append(all, list...)
	}
	return all
}

// fakePath turns a protected path into one we can create in a fake home.
// A "*" in a segment becomes "Sapu Regression" ("~/Pictures/*.photoslibrary"
// -> "Pictures/Sapu Regression.photoslibrary"). Paths outside the home and
// other wildcards cannot be built and are reported instead.
func fakePath(p string) (string, bool) {
	if !strings.HasPrefix(p, "~/") || strings.ContainsAny(p, "?[") || strings.Contains(p, "**") {
		return "", false
	}
	return strings.ReplaceAll(strings.TrimPrefix(p, "~/"), "*", "Sapu Regression"), true
}

// parentChildrenGlob is a rule path aimed at every sibling of rel, rel included.
func parentChildrenGlob(rel string) string {
	if dir := path.Dir(rel); dir != "." {
		return "~/" + dir + "/**"
	}
	return "~/**"
}

// Case 8: every entry of the protection list holds, as a folder with a file
// inside and as a plain file, even when a rule targets its parent's children
// on top of the aggressive rules.
func TestEveryProtectedEntrySurvives(t *testing.T) {
	t.Parallel()
	entries := protectedEntries(t)

	var listed []string
	for _, en := range entries {
		listed = append(listed, en.Paths...)
	}
	for _, p := range fixedProtectedPaths {
		if !slices.Contains(listed, p) {
			t.Errorf("protected path %q was removed from rules/protect; existing entries are fixed", p)
		}
	}

	for _, en := range entries {
		for _, p := range en.Paths {
			rel, ok := fakePath(p)
			if !ok {
				t.Logf("entry %s: cannot build %q in a fake home; not tested here", en.ID, p)
				continue
			}
			for _, shape := range []string{"folder", "file"} {
				t.Run(fmt.Sprintf("%s %s as a %s", en.ID, p, shape), func(t *testing.T) {
					t.Parallel()
					files := map[string]string{junkFile: "ordinary cache"}
					if shape == "folder" {
						files[rel+"/sapu-regression.txt"] = "user data"
					} else {
						files[rel] = "user data"
					}
					e := newHome(t, files)
					before := snapshotEach(t, e, []string{rel})
					rs := append(aggressiveRules(), synthRule("synthetic.parent", rules.RiskSafe, parentChildrenGlob(rel)))

					p, _ := clean(t, e, rs)

					requireSkip(t, e, p.Skipped, rel, plan.ReasonProtected)
					requireNotPlanned(t, e, p, rel)
					requireSurvived(t, e, before)
					requireMissing(t, e, junkFile)
				})
			}
		}
	}
}
