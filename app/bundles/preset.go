package bundles

import (
	"fmt"

	"github.com/pelletier/go-toml/v2"

	extconfig "dynamic-runtime/extensions/config"
)

// parsePreset 解析内嵌 TOML 预设为组件行。文件形态与部署配置一致：
// [[components]] 数组（id/type/config），不做任何额外语义。
func parsePreset(data []byte, source string) []extconfig.ComponentConfig {
	var p struct {
		Components []extconfig.ComponentConfig `toml:"components"`
	}
	if err := toml.Unmarshal(data, &p); err != nil {
		panic(fmt.Sprintf("bundle %s: preset unparsable: %v", source, err))
	}
	return p.Components
}
