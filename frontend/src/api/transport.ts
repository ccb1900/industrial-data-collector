// Wails transport boundary.
//
// After `wails generate`, typed bindings exist under ../wailsjs; the runtime
// still injects window.go.main.App. Components never call window.go directly;
// they go through this api layer so the transport can be swapped later
// (Web/CLI/Remote).
export async function invoke<T>(name: string, ...args: unknown[]): Promise<T> {
  const fn = window.go?.main?.App?.[name] as
    | ((...a: unknown[]) => Promise<T>)
    | undefined;
  if (!fn) {
    throw new Error(`wails binding not available: ${name} (run wails generate)`);
  }
  return await fn(...args);
}
