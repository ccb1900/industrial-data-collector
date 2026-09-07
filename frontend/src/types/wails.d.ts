// Ambient types for the Wails desktop runtime and generated Go bindings.
//
// In a real desktop run, Wails injects window.runtime and window.go.main.App.
// `wails generate` produces typed wrappers (frontend/wailsjs); when those are
// absent (e.g. this sandbox, or CI without a Wails toolchain) the API layer
// still compiles against these ambient shapes and fails loudly at runtime if
// the host did not inject them.
interface WailsRuntime {
  EventsOn(name: string, callback: (...args: unknown[]) => void): void;
  EventsOff(name: string): void;
}

interface WailsGoApp {
  App?: Record<string, (...args: unknown[]) => Promise<unknown>>;
}

interface Window {
  runtime?: WailsRuntime;
  go?: { main?: WailsGoApp };
}
