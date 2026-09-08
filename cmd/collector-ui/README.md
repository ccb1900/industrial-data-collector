# collector-ui — Real Wails Desktop Host (P2.1)

This is a separate Go module so the Wails dependency never leaks into the core
`industrial-data-collector` module (`go test ./...` in the repo root stays
Wails-free).

Prerequisites (outside this sandbox): a Wails v2 toolchain and the React build:

```bash
# from repo root: build the frontend and generate bindings
(cd frontend && npm install && npm run build)
wails generate module   # in frontend/ produces wailsjs bindings (project-specific)

# then run from repo root with the nested module:
go run ./cmd/collector-ui -config configs/desktop.toml -frontend frontend/dist
```

Lifecycle:

```text
main -> apphost.New -> config Reconcile (Runtime + Plugins + UI Plugin)
     -> find ui component -> Wails App{ui.Host}
     -> wails.Run (OnStartup: SetObservationSink -> runtime.EventsEmit("observation", ...))
     -> on exit: runtime/controller Close unwinds the UI Plugin Effect
```

Notes:

- Wails `Options` fields follow Wails v2 (`Assets fs.FS` via `os.DirFS`); if the
  installed Wails version uses `embed.FS`, adapt the asset wiring accordingly —
  the bridge/plugin code does not change.
- The Observation event name is fixed: `observation`.
- `App` is only a transport; it forwards to `plugins/ui.Host` and never touches
  Collector/Storage/Executor.
- P3 adds `App.ListPages/ListPanels`: the desktop transport consumes the same
  Application UI Composition Registry as the HTTP host. React only fetches
  declarative `UIPage`/`UIPanel` DTOs and maps Renderer identities.
