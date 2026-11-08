# Build-artifact types for `sapu purge`

Format is defined on `Type` in `internal/purge/types.go`; read it before
editing `types.yml`. Loading is strict: unknown fields, a bad id, empty
markers or artifacts, a missing `explain.en`, a risk other than `safe` or
`moderate`, and an artifact that is not one plain folder name (or is `.`, `..`,
`.git` or `src`) all fail the tests.

The list is adapted from kondo (MIT, https://github.com/tbillington/kondo);
see THIRD_PARTY_LICENSES.

Rules of thumb for a new type:

- `markers` are names directly inside the project root. Pick files the tool
  itself creates or needs. A loose marker such as `*.py` is fine, because the
  folder only matters if an artifact folder sits next to it.
- `artifacts` must be generated output or caches the tool can rebuild. If the
  folder can hold anything the user wrote (Unreal `Saved/`, Unity `Builds/`),
  leave it out.
- `risk: safe` for caches and build output. `risk: moderate` when getting it
  back costs a download or a long wait: virtualenvs, installed packages,
  Unity and Unreal caches.
- Add a project of the new type to `testdata/fixtures/purge/all-types.tree` and
  its folders to `all-types.expected`. The test fails if a type has no project.
