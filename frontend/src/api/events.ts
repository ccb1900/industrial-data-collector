import { UIObservation } from "../models/types";

export const OBSERVATION_EVENT = "observation";

type Unsubscribe = () => void;

// Observation Bridge.
//   - Wails: runtime.EventsEmit("observation", ...) -> EventsOn(...)
//   - Web UI: GET /api/stream (SSE, event "observation") via EventSource
// Observation only invalidates; business state always comes from Query.
export function onObservation(handler: (ev: UIObservation) => void): Unsubscribe {
  if (window.runtime) {
    const rt = window.runtime;
    rt.EventsOn(OBSERVATION_EVENT, (payload: unknown) => handler(payload as UIObservation));
    return () => rt.EventsOff(OBSERVATION_EVENT);
  }
  const source = new EventSource("/api/stream");
  const listener = (e: MessageEvent) => {
    try {
      handler(JSON.parse(String(e.data)) as UIObservation);
    } catch {
      /* ignore malformed frame */
    }
  };
  source.addEventListener(OBSERVATION_EVENT, listener);
  return () => source.close();
}
