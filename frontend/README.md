# Frontend (P2 scaffold)

Minimal React host verification page for the UI Host / Wails Bridge.

Requires (not available in the batch-2 sandbox):

```bash
wails generate      # generates ../wailsjs bindings consumed by src/api/client.ts
npm install
npm run build       # tsc && vite build
npm test
```

The Go side of the bridge is fully covered in-process by
`tests/e2e_ui_p2_test.go` (same DTO surface). Do not treat this folder as
built/tested output; it is a scaffold pending a Wails toolchain.
