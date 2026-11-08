package purge

import (
	"os"
	"path/filepath"
	"testing"
)

// Apply runs `git -C <dir> worktree remove` only in a main checkout (a real
// folder with a .git folder), so a worktree of a bare repository gets no
// suggestion: it would be refused.
func TestWorktreeOfABareRepositoryGetsNoSuggestion(t *testing.T) {
	root := tempRoot(t)
	build(t, root, "bare.git/worktrees/b/HEAD", "wtb/x")
	if err := os.WriteFile(filepath.Join(root, "wtb/.git"), []byte("gitdir: ../bare.git/worktrees/b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if items := find(t, root, Options{}); len(items) != 0 {
		t.Errorf("got %v, want nothing", found(root, items))
	}
}

func TestIsMainRepo(t *testing.T) {
	root := tempRoot(t)
	build(t, root, "repo/.git/HEAD", "wt/x", "plain/x")
	if err := os.WriteFile(filepath.Join(root, "wt/.git"), []byte("gitdir: ../repo/.git/worktrees/wt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "repo"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "linkgit"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "repo/.git"), filepath.Join(root, "linkgit/.git")); err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]bool{
		filepath.Join(root, "repo"):        true,
		filepath.Join(root, "wt"):          false, // .git is a file
		filepath.Join(root, "plain"):       false, // no .git
		filepath.Join(root, "link"):        false, // a symlink to a repo
		filepath.Join(root, "linkgit"):     false, // .git is a symlink
		filepath.Join(root, "missing"):     false,
		filepath.Join(root, "repo") + "/.": false, // not clean
		"repo":                             false, // relative
	} {
		if got := IsMainRepo(dir); got != want {
			t.Errorf("IsMainRepo(%s) = %v, want %v", dir, got, want)
		}
	}
}
