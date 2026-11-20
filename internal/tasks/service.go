package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"gtasks/internal/batchexec"
)

// Service executes registry-defined operations against the page bridge.
type Service struct {
	registry *Registry
	client   *batchexec.Client
}

// NewService wires a registry to a batchexecute client.
func NewService(registry *Registry, client *batchexec.Client) *Service {
	return &Service{registry: registry, client: client}
}

// Registry exposes the underlying operation table.
func (s *Service) Registry() *Registry { return s.registry }

// Execute renders one operation and runs it as a single batch.
func (s *Service) Execute(ctx context.Context, op Operation, params map[string]any) (*batchexec.BatchResult, error) {
	calls, err := s.registry.Render(op, params)
	if err != nil {
		return nil, err
	}
	result, err := s.client.Batch(ctx, calls, batchexec.Options{})
	if result != nil && result.Status != 0 && (result.Status < 200 || result.Status >= 300) {
		return result, fmt.Errorf("tasks: %s 收到 HTTP %d（%s）", op, result.Status, result.Auth)
	}
	if err != nil {
		return result, fmt.Errorf("tasks: 执行 %s 失败: %w", op, err)
	}
	for _, response := range result.Responses {
		if len(response.Error) > 0 {
			return result, fmt.Errorf("tasks: %s 的 %s 返回错误: %s", op, response.RpcID, string(response.Error))
		}
	}
	return result, nil
}

// Snapshot reads the whole dataset: every list plus every task the server is
// willing to return in one shot. The web app renders 所有任务, 已加星标, each
// individual list and the 已完成 sections from this single payload without
// issuing another request, so it is the client's entire read path.
func (s *Service) Snapshot(ctx context.Context) (*Snapshot, error) {
	result, err := s.Execute(ctx, OpSnapshot, nil)
	if err != nil {
		return nil, err
	}
	for _, response := range result.Responses {
		if len(response.Data) == 0 {
			continue
		}
		snapshot, err := ParseSnapshot(response.Data)
		if err != nil {
			return nil, err
		}
		return snapshot, nil
	}
	return nil, errors.New("tasks: 快照响应里没有可解析的数据（会话可能已失效）")
}

// SnapshotJSON is Snapshot rendered for the frontend.
func (s *Service) SnapshotJSON(ctx context.Context) (json.RawMessage, error) {
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("tasks: 序列化快照失败: %w", err)
	}
	return body, nil
}
