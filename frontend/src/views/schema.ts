// 声明式视图 schema v2：页面 = 有序视图块列表。
// 插件只贡献数据（TOML/JSON 声明），通用渲染器解释执行；
// 新 kind = 新注册的渲染器，插件无需前端代码。
export interface ViewBlock {
  kind: "table" | "trend" | "stats" | "kv" | "list" | string;
  query?: string;
  params?: Record<string, string>;
  columns?: Array<{ key: string; title: string }>;
  rowActions?: Array<{ label: string; command: string; args?: Record<string, string> }>;
  items?: Array<{ label: string; op?: "count" | "sum"; field?: string; warn?: boolean }>;
  dateKey?: string;
  series?: Array<{ key: string; label: string }>;
  fields?: Array<{ key: string; label: string }>;
  titleKey?: string;
  pageSize?: number;
  /** 点击行设置全局 focus（联动其他视图）。 */
  selectFocus?: boolean;
}

export interface PageViewSchema {
  views: ViewBlock[];
}

// $focus.sourceId / $focus.date 由外壳的选择状态解析。
export function resolveParams(
  params: Record<string, string> | undefined,
  focus: { sourceId: string; date: string } | null
): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(params ?? {})) {
    if (v === "$focus.sourceId") {
      if (focus) out[k] = focus.sourceId;
    } else if (v === "$focus.date") {
      if (focus) out[k] = focus.date;
    } else {
      out[k] = v;
    }
  }
  return out;
}
