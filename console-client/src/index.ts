// @gocordis/console-client — reusable console shell for gocordis applications.
//
// 应用以自己的渲染器与领域数据组装外壳；本包只提供应用无关的部分：
// 组合投影侧栏、观察流与边界状态、插件清单控制台、共享设计系统原语。

export * from "./api";
export { useComposition } from "./hooks/useComposition";
export { Sidebar } from "./components/Sidebar";
export {
  Chip,
  EmptyState,
  ErrorNote,
  EventFeed,
  LoadingState,
  MetadataTable,
  Progress,
  StatusChip,
} from "./components/Lists";
export * as Icons from "./components/Icons";
