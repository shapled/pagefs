// Package config 加载进程级配置：工作目录下 pagefs.json / pagefs.yml，
// 两种格式都支持，json 优先（并存只读 json）。
package config

import (
	"encoding/json"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config 是进程级配置（生命周期级参数；热更参数走 /vars）。
type Config struct {
	Listen string `json:"listen" yaml:"listen"` // NFS 监听地址
	CDP    string `json:"cdp"    yaml:"cdp"`    // CDP http endpoint
}

// Load 在 dir 下查找配置文件；都不存在时返回零值与 false。
func Load(dir string) (Config, bool, error) {
	jsonPath := filepath.Join(dir, "pagefs.json")
	if b, err := os.ReadFile(jsonPath); err == nil {
		var c Config
		if err := json.Unmarshal(b, &c); err != nil {
			return c, true, err
		}
		return c, true, nil
	}
	ymlPath := filepath.Join(dir, "pagefs.yml")
	if b, err := os.ReadFile(ymlPath); err == nil {
		var c Config
		if err := yaml.Unmarshal(b, &c); err != nil {
			return c, true, err
		}
		return c, true, nil
	}
	return Config{}, false, nil
}
