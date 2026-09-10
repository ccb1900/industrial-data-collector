import { useSyncExternalStore } from "react";

// 轻量 history 路由：页面路径即 URL 路径；服务端 SPA 回退保证刷新后
// 仍由前端按路径恢复当前页（配合 composition 的 route 字段）。
const listeners = new Set<() => void>();

function subscribe(fn: () => void) {
  listeners.add(fn);
  window.addEventListener("popstate", fn);
  return () => {
    listeners.delete(fn);
    window.removeEventListener("popstate", fn);
  };
}

function getSnapshot() {
  return window.location.pathname;
}

export function usePath(): string {
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
}

export function navigate(path: string): void {
  if (window.location.pathname !== path) {
    window.history.pushState({}, "", path);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }
}
