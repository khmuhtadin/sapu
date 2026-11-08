# Cleanup rules

One or more rules per `*.yml` file (a YAML list). Format and path semantics:
`internal/rules/rules.go`. Every rule needs `risk`, `regenerates`, `explain.en`
and a `fixture` folder under `testdata/fixtures/rules/`.

```yaml
- id: capcut.cache
  category: media
  app: com.lemon.lvoverseas
  paths:
    - ~/Movies/CapCut/User Data/Cache/**
  risk: moderate
  regenerates: true
  explain:
    en: "CapCut render and preview cache. Rebuilt when you reopen a project; first playback is slower."
  protect_if:
    - process_running: CapCut
    - modified_within: 3d
  action: quarantine
  fixture: capcut-cache
```
