// Platform DTOs of the console client. Domain payloads travel through the
// hub's named queries as opaque JSON; these shapes are what every console
// renders regardless of application.

export type Unsubscribe = () => void;

export type StreamStatus = "live" | "connecting" | "offline";

export interface UIObservation {
  type: string;
  sourceId?: string;
  timestamp: string;
}

export interface UIPage {
  id: string;
  title: string;
  route: string;
  renderer: string;
}

export interface UIPanel {
  id: string;
  title: string;
  position: string;
  renderer: string;
}

export interface UIPageList {
  pages: UIPage[];
}

export interface UIPanelList {
  panels: UIPanel[];
}

export interface RemovedPlugin {
  id: string;
  name: string;
}

export interface ExplorerControlRequest {
  pluginId: string;
  enable: boolean;
}

export interface ExplorerControlResult {
  pluginId: string;
  accepted: boolean;
  rejected: boolean;
  failed: boolean;
  state: string;
  error: string;
}
