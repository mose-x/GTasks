package tasks

import (
	"context"
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
	if err != nil {
		return nil, fmt.Errorf("tasks: 执行 %s 失败: %w", op, err)
	}
	for _, response := range result.Responses {
		if len(response.Error) > 0 {
			return result, fmt.Errorf("tasks: %s 的 %s 返回错误: %s", op, response.RpcID, string(response.Error))
		}
	}
	return result, nil
}
