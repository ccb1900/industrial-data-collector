// 报警示例插件的后端：一个完全自包含的进程外插件。
//
// main.go 经 go-cordis 的 proc.Serve 在 stdin/stdout 上提供行分隔
// JSON-RPC 契约：manifest.toml 声明的每个 queries/commands 名就是同名
// 方法。构建：go build -o alarm-demo . （发布前；部署物是二进制）。
//
// 真实插件在这里接入数据库或现场设备；本示例返回静态演示数据。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"dynamic-runtime/extensions/loader/proc"
)

func alarms(_ context.Context, _ json.RawMessage) (any, error) {
	now := time.Now().Format("15:04:05")
	return []map[string]any{
		{"id": "A-01", "level": "warning", "device": "1#空压机", "message": "排气温度偏高（72°C）", "at": now},
		{"id": "A-02", "level": "critical", "device": "3#冷却泵", "message": "压力低于工艺下限", "at": now},
		{"id": "A-03", "level": "warning", "device": "2#风机", "message": "振动速度上升趋势", "at": now},
	}, nil
}

func alarmStats(_ context.Context, _ json.RawMessage) (any, error) {
	return map[string]any{"total": 3, "critical": 1, "warning": 2, "source": "out-of-process"}, nil
}

func ackAlarm(_ context.Context, params json.RawMessage) (any, error) {
	var req struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(params, &req)
	return map[string]any{"acked": req.ID, "by": "operator"}, nil
}

func main() {
	methods := map[string]proc.Handler{
		"alarms":      alarms,
		"alarm-stats": alarmStats,
		"ack-alarm":   ackAlarm,
	}
	if err := proc.Serve(methods); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
