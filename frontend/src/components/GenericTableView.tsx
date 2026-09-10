import React, { useCallback, useEffect, useState } from "react";
import { Table } from "antd";
import { queries } from "../api/client";

// 声明式视图 schema v1：通用表格。
// 页面配置携带 view = { query, params, columns, pageSize }；
// 渲染器按 schema 调用 hub 命名查询并渲染表格——插件零前端代码。
// hub 返回形状：{ columns: string[]（列名）, rows: unknown[][]（按列序取值）}。
export interface ViewColumn {
  key: string; // 对应 hub 查询返回的列名
  title: string;
}

export interface ViewSchema {
  query: string;
  params?: Record<string, string | number>;
  columns: ViewColumn[];
  pageSize?: number;
}

export function GenericTableView({ view }: { view: ViewSchema }) {
  const [page, setPage] = useState<{ columns: string[]; rows: unknown[][] } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const refresh = useCallback(() => {
    queries
      .hubQuery<{ columns: string[]; rows: unknown[][] }>(view.query, view.params ?? {})
      .then(setPage)
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, [view.query, JSON.stringify(view.params ?? {})]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const data = (page?.rows ?? []).map((row) => {
    const obj: Record<string, unknown> = {};
    (page?.columns ?? []).forEach((name, j) => {
      obj[name] = row[j];
    });
    return obj;
  });

  if (error) {
    return <p style={{ color: "#f0655a" }}>{error}</p>;
  }
  return (
    <Table
      size="small"
      rowKey={(_, i) => String(i)}
      loading={loading}
      dataSource={data}
      pagination={{ pageSize: view.pageSize ?? 20, hideOnSinglePage: true }}
      columns={view.columns.map((c) => ({ title: c.title, dataIndex: c.key, key: c.key }))}
    />
  );
}
