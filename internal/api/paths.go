package api

import (
	"fmt"
	"os"
	"path/filepath"
)

// DataDir returns the per-user directory holding captures and the operation
// registry, creating it when needed.
func DataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("api: 无法定位用户配置目录: %w", err)
	}
	dir := filepath.Join(base, "gtasks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("api: 无法创建 %s: %w", dir, err)
	}
	return dir, nil
}

// CapturesPath is the append-only JSON Lines log of sniffed RPC traffic.
func CapturesPath(dir string) string { return filepath.Join(dir, "captures.jsonl") }

// OperationsPath is the persisted rpcid registry.
func OperationsPath(dir string) string { return filepath.Join(dir, "operations.json") }
