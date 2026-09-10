import React from "react";

// TrendPoint：按业务日聚合的采集量与失败数。
export interface TrendPoint {
  date: string;
  records: number;
  failed: number;
}

// 轻量 SVG 柱状趋势图（无图表库依赖）：展示按天采集量与失败占比。
export function TrendChart({ points }: { points: TrendPoint[] }) {
  if (points.length === 0) {
    return null;
  }
  const max = Math.max(...points.map((p) => p.records), 1);
  const w = 28;
  const gap = 14;
  const height = 120;
  return (
    <div style={{ overflowX: "auto" }}>
      <svg
        role="img"
        aria-label="按日采集量趋势"
        width={points.length * (w + gap) + gap}
        height={height + 24}
        style={{ display: "block" }}
      >
        {points.map((p, i) => {
          const x = gap + i * (w + gap);
          const h = Math.round((p.records / max) * (height - 20));
          const failH = Math.round((p.failed / Math.max(p.records, 1)) * h);
          const y = height - h;
          return (
            <g key={p.date}>
              <rect x={x} y={y} width={w} height={h} rx={3} fill={i % 2 ? "#3b4b6b" : "#4d6bfe"} opacity={0.85}>
                <title>{`${p.date}：采集 ${p.records} 条，失败 ${p.failed} 条`}</title>
              </rect>
              {p.failed > 0 && (
                <rect x={x} y={height - Math.round((p.failed / max) * (height - 20))} width={w} height={Math.round((p.failed / max) * (height - 20))} rx={3} fill="#f0655a" opacity={0.9}>
                  <title>{`${p.date}：失败 ${p.failed} 条`}</title>
                </rect>
              )}
              <text x={x + w / 2} y={height + 16} textAnchor="middle" fontSize={10} fill="currentColor" opacity={0.6}>
                {p.date.slice(5)}
              </text>
            </g>
          );
        })}
      </svg>
    </div>
  );
}
