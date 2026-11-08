# AGENTS.md

Rules for every agent and contributor working on Sapu. They are not
optional. Each package's doc comment holds its contract.

Sapu is a macOS cleaner CLI in Go. Its whole value is that it never loses user
data. When in doubt, skip the item and say why.

## Hard rules

1. **Only `internal/fsop` moves or deletes files.** No `os.Remove`,
   `os.RemoveAll`, `os.Rename`, `os.Truncate`, unlink/rename syscalls, or
   shelling out to `rm`/`mv` anywhere else. `internal/guard` fails the build
   otherwise. Tests may delete inside their own `t.TempDir()`.
2. **Never touch the real home folder from tests.** Use
   `env.New(t.TempDir())`. Never call `env.FromOS`, `os.UserHomeDir` or read
   `$HOME` in tests. Only `internal/env` reads the real environment.
3. **On this Mac (the owner's), run only read-only Sapu commands:**
   `--plan FILE`, list-only commands (no `--pick`), `explain`, `analyze`
   without applying, `doctor`, `history`, `version`. Never `apply`, `undo`,
   `quarantine empty`/`expire`, and never any command that changes the disk
   (`clean`, `purge`, `uninstall`, `trash`, `installer`, `dupes`, `packages`,
   `optimize`, `login-items disable`, ...) without `--plan FILE`.
   **Never pass `--yes`, `--pick` or `--run-commands`, and never pipe or
   type an answer into a sapu prompt on this Mac.** To capture output of a
   destructive command, use `--plan FILE` with a temp file, or a `sapu(...)`
   test with a temp home. Destructive behaviour is tested only in temp
   folders.
4. **Tests that need real system state** (Time Machine, Spotlight, launchd,
   sudo) get the build tag `vm` and run only in CI or a VM.
5. **Clean-room.** Never open, clone, copy or port code from Mole
   (tw93/mole, GPL-3.0). Permissive projects (kondo MIT, mac-cleanup-py
   Apache-2.0) may be used only with attribution in `THIRD_PARTY_LICENSES`.
6. **Never delete, skip or weaken a test** to make something pass. That needs
   the owner's approval. If a test looks wrong, stop and report it.
7. **Stay inside the files your task owns.** If you need a change to a shared
   contract (types or signatures in another package), stop and report it to
   the orchestrator instead of editing.
8. **Dependencies:** only these, at these versions, unless the orchestrator
   approves another:
   - `github.com/spf13/cobra v1.10.2`
   - `gopkg.in/yaml.v3 v3.0.1`
   - `github.com/charmbracelet/bubbletea v1.3.10`
   - `github.com/charmbracelet/bubbles v1.0.0`
   - `github.com/charmbracelet/lipgloss v1.1.0`
   - `golang.org/x/sys v0.38.0` (newer needs Go 1.26)
   - `github.com/santhosh-tekuri/jsonschema/v6 v6.0.3` (tests only)
   - `github.com/BurntSushi/toml v1.6.0` (sapu.toml only)
   Always run Go with `GOTOOLCHAIN=local` (the Makefile does).
9. **No AI attribution in commits.** Never add `Co-Authored-By` lines or
   any other Claude/AI credit to commit messages. Commits use the repo's
   configured git user only.
10. **Keep code simple.** Plain functions, no frameworks, no clever
   abstractions, no speculative options, no fallbacks nobody asked for. Handle
   every error.

## Definition of done

- `make check` passes (vet, `go test -race ./...`, golangci-lint if installed).
- New behaviour has tests, on fixture trees in `t.TempDir()`.
- Only your owned files changed. Commit on your branch with a clear message.
- Your final report says what changed, shows the test output, and lists
  anything left open.

## Layout

- `cmd/sapu/`: the entry point. `internal/cli/`: one file per command.
- `internal/env/`: the only package that reads `$HOME` and the environment.
- `internal/fsop/`: quarantine, restore, expire and empty, the only code that
  moves or deletes files. `internal/guard/` fails the build otherwise.
- `internal/engine/`: builds plans and applies them; `internal/protect/` and
  `internal/inuse/` decide what is never touched.
- Rule data lives in `rules/` and is embedded through `rules/embed.go`
  (package `ruledata`). `schema/v1/` has a JSON Schema for every `--json`
  output. `test/regression/` is the data-loss regression suite. Fixtures live
  in `testdata/fixtures/`.
