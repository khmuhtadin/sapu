package purge

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// exists reports whether something is at path, without following a final symlink.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// insideGit reports whether dir, or a folder above it, has a .git entry.
func insideGit(dir string) bool {
	for {
		if exists(filepath.Join(dir, ".git")) {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// tracked reports whether git tracks any file in the folder dir/name. Outside
// a git work tree nothing is tracked. When git is not installed nothing is
// reported as tracked either, so tracked folders are not protected then. When
// git is installed but fails inside a work tree, the answer is "tracked":
// when in doubt the folder is kept.
func tracked(ctx context.Context, dir, name string) (bool, error) {
	if !insideGit(dir) {
		return false, nil
	}
	cmd := exec.CommandContext(ctx, "git", "--literal-pathspecs", "-C", dir, "ls-files", "-z", "--", name)
	out, err := cmd.Output()
	if errors.Is(err, exec.ErrNotFound) {
		return false, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err != nil {
		return true, nil
	}
	return len(out) > 0, nil
}

// holdsRepo reports whether a git repository sits anywhere in dir, e.g. a
// clone inside node_modules/a/b or vendor/<vendor>/<package>. Such a clone may
// hold work that exists nowhere else, and the outer repository cannot see it.
// The whole tree is searched; symlinks are not followed. A folder that cannot
// be read counts as holding one.
func holdsRepo(dir string) bool {
	found := false
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found || err != nil
}

// worktreeOf returns the main repository of dir when dir is a linked git
// worktree, and "" otherwise. A linked worktree has a .git FILE that reads
// "gitdir: <main>/.git/worktrees/<name>". Submodules also have a .git file,
// but it points into "modules", so they are not matched. A worktree whose
// gitdir is gone is not matched either: git could not remove it.
func worktreeOf(dir string) string {
	gitFile := filepath.Join(dir, ".git")
	fi, err := os.Lstat(gitFile)
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	f, err := os.Open(gitFile)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }() // read only; nothing to lose on close
	data, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return ""
	}

	line, _, _ := strings.Cut(string(data), "\n")
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
	if !ok {
		return ""
	}
	gitdir := strings.TrimSpace(rest)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(dir, gitdir)
	}
	gitdir = filepath.Clean(gitdir)

	worktrees := filepath.Dir(gitdir)
	if filepath.Base(worktrees) != "worktrees" {
		return ""
	}
	if st, err := os.Stat(gitdir); err != nil || !st.IsDir() {
		return ""
	}
	common := filepath.Dir(worktrees) // the main .git folder, or a bare repository
	main := filepath.Dir(common)
	if filepath.Base(common) != ".git" || !IsMainRepo(main) {
		return "" // a bare repository: Apply runs git only in a main checkout
	}
	return main
}

// WorktreeCommand is the command that removes the linked worktree dir of
// the repository whose main checkout is main.
func WorktreeCommand(main, dir string) []string {
	return []string{"git", "-C", main, "worktree", "remove", dir}
}

// IsMainRepo reports whether dir, an absolute clean path, is a real folder
// (not a symlink) whose .git is a real folder: the main checkout of a
// repository. Apply runs a worktree suggestion's command only in one.
func IsMainRepo(dir string) bool {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return false
	}
	for _, p := range []string{dir, filepath.Join(dir, ".git")} {
		fi, err := os.Lstat(p)
		if err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
}
