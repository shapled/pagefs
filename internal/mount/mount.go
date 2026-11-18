// Package mount 提供跨平台的 NFS 挂载/卸载：统一入口，GOOS 分文件实现。
// serve 进程把监听地址写入状态文件（StatePath），mount 侧读取后挂载。
package mount

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// State 是 serve 进程留给 mount/umount 的服务信息。
type State struct {
	Address string `json:"address"`
	PID     int    `json:"pid"`
}

// StatePath 返回服务状态文件路径（~/.pagefs/serve.json）。
func StatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pagefs", "serve.json"), nil
}

// WriteState 保存服务状态（serve 启动时调用，退出时移除）。
func WriteState(s State) error {
	p, err := StatePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

// ReadState 读取服务状态；文件不存在返回 false。
func ReadState() (State, bool, error) {
	p, err := StatePath()
	if err != nil {
		return State{}, false, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{}, false, nil
		}
		return State{}, false, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, false, fmt.Errorf("decode %s: %w", p, err)
	}
	return s, true, nil
}

// RemoveState 移除服务状态文件（serve 退出时调用）。
func RemoveState() {
	if p, err := StatePath(); err == nil {
		os.Remove(p)
	}
}
