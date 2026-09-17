// 金样转储工具：把当前 collector-core / collector-console 预设的组件行
// 转储为 JSON，作为数据化重构前后的字节级比对基准。
//
//	go run ./app/bundles/cmd/dump
package main

import (
	"encoding/json"
	"fmt"

	extbundle "dynamic-runtime/extensions/bundle"

	// 预设内容随包 init 注册进注册表。
	_ "gocordis-csv-collector/app/bundles"
)

func main() {
	out := map[string]any{}
	for _, name := range []string{"collector-core", "collector-console"} {
		rows, err := extbundle.Expand([]string{name})
		if err != nil {
			fmt.Println("ERR", err)
			continue
		}
		out[name] = rows
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(data))
}
