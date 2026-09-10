// 声明式页面：按页面的 views 块列表渲染通用视图栈。
// 页面本身只是数据——标题、动作、视图块——由 hub 命名查询/命令供能。
import React from "react";
import { Button, DatePicker, Space } from "antd";
import dayjs, { Dayjs } from "dayjs";
import { ViewBlockRenderer, type ViewBlock, type ViewContext } from "./blocks";

export interface PageActions {
  label: string;
  command: string;
  datePicker?: boolean;
}

export interface GenericPageProps {
  title: string;
  description?: string;
  views: ViewBlock[];
  actions?: PageActions[];
  ctx: ViewContext;
}

export function GenericPage({ title, description, views, actions = [], ctx }: GenericPageProps) {
  const [dates, setDates] = React.useState<Record<string, string>>({});

  const hero = actions.length > 0 && (
    <Space wrap>
      {actions.map((a) =>
        a.datePicker ? (
          <DatePicker
            key={a.label}
            value={dates[a.label] ? dayjs(dates[a.label]) : null}
            onChange={(d) => setDates((m) => ({ ...m, [a.label]: d ? d.format("YYYY-MM-DD") : "" }))}
            placeholder="选择日期（默认按调度策略）"
            style={{ width: 190 }}
          />
        ) : null,
      )}
      {actions.map((a) => (
        <Button
          key={a.label}
          type="primary"
          loading={ctx.busy}
          onClick={() =>
            void ctx.hubCommand(a.command, {
              date: dates[a.label] || undefined,
              reason: a.label,
            })
          }
        >
          {a.label}
        </Button>
      ))}
    </Space>
  );

  return (
    <>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "flex-start", marginBottom: 16, flexWrap: "wrap", gap: 12 }}>
        <div>
          <h1 style={{ margin: 0, fontSize: 22 }}>{title}</h1>
          {description && <p style={{ color: "#8a93a6", marginBottom: 0 }}>{description}</p>}
        </div>
        <div style={{ display: "flex", gap: 10, flexWrap: "wrap", alignItems: "flex-start" }}>{hero}</div>
      </div>
      {views.map((v, i) => (
        <ViewBlockRenderer key={i} block={v} ctx={ctx} />
      ))}
    </>
  );
}

export type { Dayjs };
