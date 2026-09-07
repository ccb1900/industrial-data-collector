import { UIObservation } from "../models/types";

export const OBSERVATION_EVENT = "observation";

// Observation Bridge: Go side emits with Wails runtime.EventsEmit("observation",
// UIObservation); this React listener receives the real Wails Event and only
// invalidates (state always comes from Query).
export function onObservation(handler: (ev: UIObservation) => void): () => void {
  const rt = window.runtime;
  if (!rt) {
    throw new Error("wails runtime not available (window.runtime)");
  }
  rt.EventsOn(OBSERVATION_EVENT, (payload: unknown) => {
    handler(payload as UIObservation);
  });
  return () => rt.EventsOff(OBSERVATION_EVENT);
}
