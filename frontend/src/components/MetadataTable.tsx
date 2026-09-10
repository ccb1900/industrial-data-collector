import React from "react";
import { Table, Typography } from "antd";
import { metadataEntries } from "../lib/metadata";

// 动态元数据表：键值开放，不硬编码业务字段。
export function MetadataTable({ metadata }: { metadata: Record<string, string> }) {
  const entries = metadataEntries(metadata);
  if (entries.length === 0) {
    return <Typography.Text type="secondary">暂无元数据</Typography.Text>;
  }
  return (
    <Table
      size="small"
      showHeader={false}
      pagination={false}
      dataSource={entries}
      rowKey={(e) => e[0]}
      columns={[
        { title: "键", dataIndex: 0, key: "key", width: "40%" },
        { title: "值", dataIndex: 1, key: "value" },
      ]}
    />
  );
}

