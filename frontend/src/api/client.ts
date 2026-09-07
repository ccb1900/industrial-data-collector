// Convenience facade: api/queries, api/commands, api/events. Components only
// import this file; swapping Wails for Web/CLI/Remote touches only transport.ts.
export { queries } from "./queries";
export { commands } from "./commands";
export { onObservation, OBSERVATION_EVENT } from "./events";
