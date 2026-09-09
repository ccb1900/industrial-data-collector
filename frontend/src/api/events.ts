import { UIObservation } from "../models/types";

export const OBSERVATION_EVENT = "observation";

type Unsubscribe = () => void;

// StreamStatus is the state of the acquisition channel across the system
// boundary (the UI observes; it never owns state). "live" means emissions
// can arrive; "connecting"/"offline" mean the shell must re-query on
// recovery, because dropped emissions are not replayed.
export type StreamStatus = "live" | "connecting" | "offline";

// The web transport keeps ONE shared SSE connection for both observation
// events and boundary status; Wails is always live through the event bus.
type Observer = { onEvent?: (ev: UIObservation) => void; onStatus?: (s: StreamStatus) => void };
const observers = new Set<Observer>();
let sharedSource: EventSource | null = null;

function ensureSource(): void {
  if (sharedSource) return;
  const source = new EventSource("/api/stream");
  sharedSource = source;
  source.addEventListener(OBSERVATION_EVENT, (e: MessageEvent) => {
    let ev: UIObservation;
    try {
      ev = JSON.parse(String(e.data)) as UIObservation;
    } catch {
      return; // ignore malformed frame
    }
    for (const o of observers) o.onEvent?.(ev);
  });
  const report = () => {
    const status: StreamStatus =
      source.readyState === EventSource.OPEN
        ? "live"
        : source.readyState === EventSource.CLOSED
        ? "offline"
        : "connecting";
    for (const o of observers) o.onStatus?.(status);
  };
  source.onopen = report;
  source.onerror = report;
}

// Observation Bridge.
//   - Wails: runtime.EventsEmit("observation", ...) -> EventsOn(...)
//   - Web UI: GET /api/stream (SSE, event "observation")
// Observation only invalidates; business state always comes from Query.
export function onObservation(handler: (ev: UIObservation) => void): Unsubscribe {
  if (window.runtime) {
    const rt = window.runtime;
    rt.EventsOn(OBSERVATION_EVENT, (payload: unknown) => handler(payload as UIObservation));
    return () => rt.EventsOff(OBSERVATION_EVENT);
  }
  const o: Observer = { onEvent: handler };
  observers.add(o);
  ensureSource();
  return () => observers.delete(o);
}

export function onStreamStatus(handler: (status: StreamStatus) => void): Unsubscribe {
  if (window.runtime) {
    handler("live");
    return () => undefined;
  }
  const o: Observer = { onStatus: handler };
  observers.add(o);
  ensureSource();
  return () => observers.delete(o);
}
