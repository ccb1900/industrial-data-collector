import React, { useCallback, useEffect, useState } from "react";
import { Table, Select, Input, Button, Space } from "antd";
import type { ColumnsType } from "antd/es/table";
import { queries } from "../api/client";
import { EmptyState, ErrorNote } from "@gocordis/console-client";
import { UISource } from "../models/types";

// DataExplorer queries the typed relational sink through the console hub:
// named query "rows" with source/date/limit/filters. This is the read side
// of the industrial collection — plain, typed, paginated rows.
export function DataExplorer() {
  const [sources, setSources] = useState<UISource[]>([]);
  const [sourceId, setSourceId] = useState<string>("");
  const [date, setDate] = useState<string>("");
  const [columns, setColumns] = useState<ColumnsType<Record<string, unknown>>>([]);
  const [rows, setRows] = useState<Record<string, unknown>[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    queries.listSources().then((srcs) => {
      setSources(srcs);
      if (srcs.length > 0 && !sourceId) {
        setSourceId(srcs[0].id);
      }
    });
    // default business date: yesterday
    const d = new Date(Date.now() - 86400000).toISOString().slice(0, 10);
    setDate(d);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const load = useCallback(() => {
    if (!sourceId || !date) return;
    setLoading(true);
    queries
      .listRows({ sourceId, date, limit: 200 })
      .then((page) => {
        setColumns(
          page.columns.map((c) => ({
            title: c,
            dataIndex: c,
            key: c,
            render: (v: unknown) => (v === null || v === undefined ? "—" : String(v)),
          })) as ColumnsType<Record<string, unknown>>
        );
        const keyed = page.rows.map((r) => {
          const obj: Record<string, unknown> = {};
          page.columns.forEach((c, i) => {
            obj[c] = r[i];
          });
          return obj;
        });
        setRows(keyed);
        setError(null);
      })
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, [sourceId, date]);

  useEffect(() => {
    load();
  }, [sourceId, date, load]);

  return (
    <div className="page-content">
      <Space wrap style={{ marginBottom: 14 }}>
        <Select
          aria-label="Source"
          style={{ minWidth: 180 }}
          value={sourceId || undefined}
          onChange={(v) => setSourceId(v)}
          options={sources.map((s) => ({ value: s.id, label: s.name }))}
          placeholder="Source"
        />
        <Input
          aria-label="Date"
          style={{ width: 140 }}
          value={date}
          onChange={(e) => setDate(e.target.value)}
          placeholder="YYYY-MM-DD"
        />
        <Button onClick={load}>Refresh</Button>
      </Space>
      {error && <p className="error-inline">{error}</p>}
      {rows.length === 0 && !loading ? (
        <EmptyState>No rows. Check the source, date, and table configuration.</EmptyState>
      ) : (
        <Table
          size="small"
          columns={columns}
          dataSource={rows}
          rowKey={(_, i) => String(i)}
          loading={loading}
          pagination={{ pageSize: 20, hideOnSinglePage: true }}
        />
      )}
    </div>
  );
}
