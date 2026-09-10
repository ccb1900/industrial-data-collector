// 通用视图渲染器集：与领域无关的视图块实现。
// 每个渲染器对应 schema 的一个 kind；数据一律来自 hub 命名查询，
// 行动经由 hub 命令——渲染器不做任何领域判断。
import { Button, Descriptions, Empty, List, Progress, Table, Typography } from "antd";
import type { TableColumnsType } from "antd";
import { useEffect, useState } from "react";
import { resolveParams, type ViewBlock } from "./schema";

export type { ViewBlock };

export interface ViewContext {
  hubQuery: <T = unknown>(name: string, params?: Record<string, string>) => Promise<T>;
  hubCommand: (name: string, body: unknown) => Promise<void>;
  focus: { sourceId: string; date: string } | null;
  onFocus: (sourceId: string, date: string) => void;
  busy: boolean;
}

type Row = Record<string, unknown>;

function statusNode(status: unknown) {
  const s = status == null ? "" : String(status);
  const color = s === "Succeeded" ? "#3ecf8e" : s === "Failed" ? "#f0655a" : s === "Pending" ? "#f2b544" : "#8a93a6";
  return <span style={{ color }}>{s || "—"}</span>;
}

function useQueryData(block: ViewBlock, ctx: ViewContext) {
  const [data, setData] = useState<unknown>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const params = JSON.stringify(resolveParams(block.params, ctx.focus));

  useEffect(() => {
    let alive = true;
    setLoading(true);
    ctx
      .hubQuery(block.query ?? "", JSON.parse(params || "{}"))
      .then((d) => {
        if (alive) {
          setData(d);
          setError(null);
        }
      })
      .catch((e) => alive && setError(e instanceof Error ? e.message : String(e)))
      .finally(() => alive && setLoading(false));
  }, [block.query, params, ctx]);

  return { data, error, loading: loading || data === null };
}

function TableBlock({ block, ctx }: { block: ViewBlock; ctx: ViewContext }) {
  const { data, error, loading } = useQueryData(block, ctx);
  const rows = Array.isArray(data) ? (data as Row[]) : [];
  const columns = (block.columns ?? []).map((c) => ({
    title: c.title,
    dataIndex: c.key,
    key: c.key,
    render: (v: unknown) => (c.key === "status" ? statusNode(v) : String(v ?? "—")),
  })) as TableColumnsType<Row>;
  const actions = block.rowActions ?? [];
  const cols: TableColumnsType<Row> = [
    ...columns,
    ...(actions.length
      ? [
          {
            title: "操作",
            key: "__actions",
            render: (_: unknown, row: Row) => (
              <span style={{ display: "flex", gap: 6 }}>
                {actions.map((a) => (
                  <Button
                    key={a.label}
                    size="small"
                    disabled={ctx.busy}
                    onClick={() => {
                      const args: Record<string, unknown> = {};
                      for (const [k, ref] of Object.entries(a.args ?? {})) {
                        const m = /^\$row\.(.+)$/.exec(ref);
                        if (m) args[k] = row[m[1]];
                      }
                      void ctx.hubCommand(a.command, args);
                    }}
                  >
                    {a.label}
                  </Button>
                ))}
              </span>
            ),
          },
        ]
      : []),
  ];
  return (
    <Table<Row>
      size="small"
      rowKey={(_, i) => String(i)}
      loading={loading}
      dataSource={rows}
      pagination={{ pageSize: block.pageSize ?? 20, hideOnSinglePage: true }}
      columns={cols}
      rowClassName={(record) =>
        ctx.focus && record["sourceId"] === ctx.focus.sourceId && record["date"] === ctx.focus.date ? "row-selected" : ""
      }
      onRow={(record) => ({
        onClick: () => {
          if (block.selectFocus && record["sourceId"] && record["date"]) {
            ctx.onFocus(String(record["sourceId"]), String(record["date"]));
          }
        },
        style: block.selectFocus ? { cursor: "pointer" } : undefined,
      })}
    />
  );
}

function KVBlock({ block, ctx }: { block: ViewBlock; ctx: ViewContext }) {
  const { data, error, loading } = useQueryData(block, ctx);
  const obj = (data ?? {}) as Record<string, unknown>;
  return (
    <Descriptions size="small" column={1} bordered>
      {(block.fields ?? []).map((f) => (
        <Descriptions.Item key={f.key} label={f.label}>
          {loading ? "…" : String(obj[f.key] ?? "—")}
        </Descriptions.Item>
      ))}
    </Descriptions>
  );
}

function ListBlock({ block, ctx }: { block: ViewBlock; ctx: ViewContext }) {
  const { data, error, loading } = useQueryData(block, ctx);
  const entries = Array.isArray(data) ? (data as Row[]) : [];
  const titleKey = block.titleKey ?? "sourceId";
  return (
    <List
      size="small"
      loading={loading}
      dataSource={[...entries].reverse()}
      renderItem={(entry: Row) => (
        <List.Item
          actions={(block.rowActions ?? []).map((a) => {
            const args: Record<string, unknown> = {};
            for (const [k, ref] of Object.entries(a.args ?? {})) {
              const m = /^\$row\.(.+)$/.exec(ref);
              if (m) args[k] = entry[m[1]];
            }
            return (
              <Button key={a.label} size="small" disabled={ctx.busy} onClick={() => void ctx.hubCommand(a.command, args)}>
                {a.label}
              </Button>
            );
          })}
        >
          <Typography.Text>
            {String(entry[block.titleKey ?? "sourceId"] ?? "")}
            {entry["date"] ? ` · ${String(entry["date"])}` : ""}
          </Typography.Text>
        </List.Item>
      )}
    />
  );
}

export function ViewBlockRenderer({ block, ctx }: { block: ViewBlock; ctx: ViewContext }) {
  switch (block.kind) {
    case "table":
      return <TableBlock block={block} ctx={ctx} />;
    case "kv":
      return <KVBlock block={block} ctx={ctx} />;
    case "list":
      return <ListBlock block={block} ctx={ctx} />;
    default:
      return <div style={{ color: "#8a93a6", padding: 8 }}>未知视图类型 “{block.kind}”。</div>;
  }
}
