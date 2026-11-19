// Package tasks maps logical task operations onto Google's internal RPCs.
//
// Neither the rpcids nor their positional argument layouts are published
// anywhere, so they have to be discovered from the live web app. A Registry
// holds what has been discovered so far: each logical operation (star a task,
// set a due time, move a task, ...) maps to one or more batchexecute calls
// whose arguments are rendered from a text/template.
package tasks

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/template"

	"gtasks/internal/batchexec"
)

//go:embed operations.json
var seedOperations []byte

// ErrUnknownOperation is returned when an operation is not in the registry.
var ErrUnknownOperation = errors.New("tasks: 未知操作")

// Operation names a logical task action.
type Operation string

// Step is one RPC inside an operation.
type Step struct {
	RpcID string `json:"rpcid"`
	// Args is a text/template source that renders to a JSON array. Splice
	// values in with {{json .FieldName}} so they stay valid JSON literals.
	// It is a plain string because an unrendered template is not valid JSON.
	Args string `json:"args,omitempty"`
}

// Definition describes how to carry out one operation.
type Definition struct {
	Op    Operation `json:"op"`
	Label string    `json:"label,omitempty"`
	Steps []Step    `json:"steps"`
	Notes string    `json:"notes,omitempty"`
}

// Ready reports whether the definition can actually be executed.
func (d Definition) Ready() bool { return len(d.Steps) > 0 }

// document is the on-disk shape of the registry.
type document struct {
	Version    int          `json:"version"`
	Operations []Definition `json:"operations"`
}

// Registry is a concurrency-safe table of discovered operations.
type Registry struct {
	mu    sync.RWMutex
	order []Operation
	defs  map[Operation]Definition
	path  string
}

// Default returns a registry seeded with the bundled operation catalogue. Every
// operation is present but none carries steps yet.
func Default() (*Registry, error) {
	return Load(bytes.NewReader(seedOperations), "")
}

// Load parses a registry document. When path is non-empty it is remembered so
// Save can write back to it.
func Load(r io.Reader, path string) (*Registry, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("tasks: 读取操作表失败: %w", err)
	}
	var doc document
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("tasks: 操作表不是合法 JSON: %w", err)
	}

	registry := &Registry{defs: make(map[Operation]Definition, len(doc.Operations)), path: path}
	for _, definition := range doc.Operations {
		if definition.Op == "" {
			return nil, errors.New("tasks: 存在缺少 op 字段的条目")
		}
		if _, exists := registry.defs[definition.Op]; exists {
			return nil, fmt.Errorf("tasks: 操作 %q 重复定义", definition.Op)
		}
		registry.defs[definition.Op] = definition
		registry.order = append(registry.order, definition.Op)
	}
	if len(registry.order) == 0 {
		return nil, errors.New("tasks: 操作表为空")
	}
	return registry, nil
}

// LoadFrom reads the registry at path, falling back to the bundled seed when the
// file does not exist yet. The path is remembered for later Save calls.
func LoadFrom(path string) (*Registry, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Seeded(path)
		}
		return nil, fmt.Errorf("tasks: 打开操作表失败: %w", err)
	}
	defer file.Close()
	return Load(file, path)
}

// Seeded returns a registry seeded from the bundled catalogue and bound to path,
// so a later Save writes back to it.
func Seeded(path string) (*Registry, error) {
	return Load(bytes.NewReader(seedOperations), path)
}

// Path reports where Save writes to, or "" for an in-memory registry.
func (r *Registry) Path() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.path
}

// Lookup returns one definition.
func (r *Registry) Lookup(op Operation) (Definition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	definition, ok := r.defs[op]
	return definition, ok
}

// All returns every definition in catalogue order.
func (r *Registry) All() []Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Definition, 0, len(r.order))
	for _, op := range r.order {
		out = append(out, r.defs[op])
	}
	return out
}

// Pending lists operations that still have no steps, sorted for stable output.
func (r *Registry) Pending() []Operation {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Operation
	for _, op := range r.order {
		if !r.defs[op].Ready() {
			out = append(out, op)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Assign records the steps for an operation and persists the registry.
func (r *Registry) Assign(op Operation, steps []Step, notes string) error {
	if op == "" {
		return errors.New("tasks: op 不能为空")
	}
	if len(steps) == 0 {
		return errors.New("tasks: 至少需要一个 step")
	}
	for index, step := range steps {
		if step.RpcID == "" {
			return fmt.Errorf("tasks: 第 %d 个 step 缺少 rpcid", index)
		}
		// Args are text/template sources that render to JSON, so they are not
		// valid JSON on their own. Validate that the template at least parses.
		if strings.TrimSpace(step.Args) != "" {
			if _, err := argsTemplate(step.Args); err != nil {
				return fmt.Errorf("tasks: 第 %d 个 step 的 args 模板无法解析: %w", index, err)
			}
		}
	}

	r.mu.Lock()
	definition, ok := r.defs[op]
	if !ok {
		definition = Definition{Op: op, Label: string(op)}
		r.order = append(r.order, op)
	}
	definition.Steps = steps
	if notes != "" {
		definition.Notes = notes
	}
	r.defs[op] = definition
	doc := document{Version: 1, Operations: make([]Definition, 0, len(r.order))}
	for _, key := range r.order {
		doc.Operations = append(doc.Operations, r.defs[key])
	}
	path := r.path
	r.mu.Unlock()

	if path == "" {
		return nil
	}
	return saveDocument(path, doc)
}

// Render turns a definition plus parameters into concrete batchexecute calls.
func (r *Registry) Render(op Operation, params map[string]any) ([]batchexec.Call, error) {
	definition, ok := r.Lookup(op)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownOperation, op)
	}
	if !definition.Ready() {
		return nil, fmt.Errorf("tasks: 操作 %s 尚未配置 rpcid，请先在工作台里抓包并保存", op)
	}

	calls := make([]batchexec.Call, 0, len(definition.Steps))
	for index, step := range definition.Steps {
		args, err := renderArgs(step.Args, params)
		if err != nil {
			return nil, fmt.Errorf("tasks: 操作 %s 第 %d 步: %w", op, index, err)
		}
		calls = append(calls, batchexec.Call{RpcID: step.RpcID, Args: args})
	}
	return calls, nil
}

var templateFuncs = template.FuncMap{
	// json splices a parameter in as a JSON literal, keeping quotes and
	// escaping correct.
	"json": func(value any) (string, error) {
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	},
}

// argsTemplate parses a step argument template. missingkey=error keeps a typo
// in a parameter name from silently rendering as <no value>.
func argsTemplate(source string) (*template.Template, error) {
	return template.New("args").Funcs(templateFuncs).Option("missingkey=error").Parse(source)
}

func renderArgs(source string, params map[string]any) (any, error) {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" || trimmed == "null" {
		return []any{}, nil
	}
	tmpl, err := argsTemplate(source)
	if err != nil {
		return nil, fmt.Errorf("参数模板无法解析: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, params); err != nil {
		return nil, fmt.Errorf("参数模板渲染失败: %w", err)
	}
	var args any
	if err := json.Unmarshal(buf.Bytes(), &args); err != nil {
		return nil, fmt.Errorf("渲染结果不是合法 JSON: %w（结果: %s）", err, truncate(buf.String(), 200))
	}
	return args, nil
}

func saveDocument(path string, doc document) error {
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("tasks: 序列化操作表失败: %w", err)
	}
	body = append(body, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("tasks: 创建目录失败: %w", err)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, body, 0o600); err != nil {
		return fmt.Errorf("tasks: 写入临时文件失败: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("tasks: 落盘失败: %w", err)
	}
	return nil
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
