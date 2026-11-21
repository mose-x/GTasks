// Package i18n 承载界面文案。词表是 locales/*.toml 里的扁平键值：加一门语言
// 只需要丢一个 jp.toml 进去重新打包，切换菜单里就会自动多出一项，代码零改动。
package i18n

import (
	"embed"
	"encoding/json"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

//go:embed locales/*.toml
var localeFS embed.FS

// Bundle 是打包进二进制的全部词表。
type Bundle struct {
	codes    []string
	labels   map[string]string
	messages map[string]map[string]string
	fallback string
}

// Load 解析嵌入的所有词表。解析失败的文件直接跳过，宁可少一门语言也不让
// 启动失败；meta.label 是切换菜单里显示的名字（用该语言自己写）。
func Load() *Bundle {
	bundle := &Bundle{
		labels:   map[string]string{},
		messages: map[string]map[string]string{},
	}
	entries, err := fs.ReadDir(localeFS, "locales")
	if err != nil {
		return bundle
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}
		code := strings.TrimSuffix(entry.Name(), ".toml")
		raw, err := localeFS.ReadFile("locales/" + entry.Name())
		if err != nil {
			continue
		}
		parsed := map[string]any{}
		if _, err := toml.Decode(string(raw), &parsed); err != nil {
			continue
		}
		table := map[string]string{}
		for key, value := range parsed {
			if key == "meta" {
				if meta, ok := value.(map[string]any); ok {
					if label, ok := meta["label"].(string); ok && label != "" {
						bundle.labels[code] = label
					}
				}
				continue
			}
			flatten(key, value, table)
		}
		bundle.messages[code] = table
		bundle.codes = append(bundle.codes, code)
	}
	sort.Strings(bundle.codes)
	for _, preferred := range []string{"zh", "en"} {
		if bundle.has(preferred) {
			bundle.fallback = preferred
			break
		}
	}
	if bundle.fallback == "" && len(bundle.codes) > 0 {
		bundle.fallback = bundle.codes[0]
	}
	return bundle
}

// flatten 把 TOML 的嵌套表拍平成 "a.b" 形式的扁平键：词表既可以按 [main] 分节
// 写，也可以直接用引号把带点的键写成一行，两种写法都认；非字符串值（数字、
// 数组、布尔）忽略，词表只承载文案。
func flatten(prefix string, value any, out map[string]string) {
	switch typed := value.(type) {
	case string:
		out[prefix] = typed
	case map[string]any:
		for key, child := range typed {
			next := key
			if prefix != "" {
				next = prefix + "." + key
			}
			flatten(next, child, out)
		}
	}
}

func (b *Bundle) has(code string) bool {
	_, ok := b.messages[code]
	return ok
}

// Has 报告打包的词表里是否有这门语言，HTTP 层校验切换请求时用。
func (b *Bundle) Has(code string) bool {
	return b.has(code)
}

// Codes 返回打包进来的语言代码，切换菜单按这个顺序列。
func (b *Bundle) Codes() []string {
	return append([]string(nil), b.codes...)
}

// Label 是切换菜单里显示的名字；词表没写 meta.label 就直接显示代码。
func (b *Bundle) Label(code string) string {
	if label, ok := b.labels[code]; ok && label != "" {
		return label
	}
	return code
}

// Messages 返回某门语言的完整词表：缺的键用兜底语言补，保证界面不开天窗。
func (b *Bundle) Messages(code string) map[string]string {
	merged := map[string]string{}
	for key, value := range b.messages[b.fallback] {
		merged[key] = value
	}
	for key, value := range b.messages[code] {
		merged[key] = value
	}
	return merged
}

// T 取一条文案；词表里没有时回落到调用方给的兜底字面量。
func (b *Bundle) T(code, key, fallback string) string {
	if table, ok := b.messages[code]; ok {
		if text, ok := table[key]; ok && text != "" {
			return text
		}
	}
	if text, ok := b.messages[b.fallback][key]; ok && text != "" {
		return text
	}
	return fallback
}

// Manager 持有当前语言并广播变更，持久化方式与主题一致：一个纯文本文件。
type Manager struct {
	mu        sync.RWMutex
	bundle    *Bundle
	lang      string
	path      string
	listeners []func(string)
}

// LoadManager 读取持久化的语言；读不到就用兜底语言。
func LoadManager(bundle *Bundle, path string) *Manager {
	manager := &Manager{bundle: bundle, lang: bundle.fallback, path: path}
	if path == "" {
		return manager
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return manager
	}
	code := strings.TrimSpace(string(raw))
	if bundle.has(code) {
		manager.lang = code
	}
	return manager
}

// Bundle 暴露词表给 HTTP 层序列化切换菜单。
func (m *Manager) Bundle() *Bundle {
	return m.bundle
}

// Lang 返回当前语言代码。
func (m *Manager) Lang() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lang
}

// OnChange 注册变更回调，注册时会立刻用当前语言回调一次。
func (m *Manager) OnChange(fn func(string)) {
	m.mu.Lock()
	m.listeners = append(m.listeners, fn)
	lang := m.lang
	m.mu.Unlock()
	fn(lang)
}

// Set 切换语言；语言没变时不做任何事。返回是否真的发生了变化。
func (m *Manager) Set(code string) bool {
	if !m.bundle.has(code) {
		return false
	}
	m.mu.Lock()
	if m.lang == code {
		m.mu.Unlock()
		return false
	}
	m.lang = code
	path := m.path
	listeners := append([]func(string){}, m.listeners...)
	m.mu.Unlock()

	if path != "" {
		// 落盘失败不影响本次切换，下次启动回落到兜底语言。
		_ = os.WriteFile(path, []byte(code+"\n"), 0o600)
	}
	for _, fn := range listeners {
		fn(code)
	}
	return true
}

// T 用当前语言取一条文案。
func (m *Manager) T(key, fallback string) string {
	return m.bundle.T(m.Lang(), key, fallback)
}

// Payload 是推给前端的载荷：当前语言、可切换列表与该语言的完整词表。
// 主窗口 / 工作台 / 会话页共用同一份结构。
func (m *Manager) Payload() []byte {
	lang := m.Lang()
	codes := m.bundle.Codes()
	items := make([]map[string]string, 0, len(codes))
	for _, code := range codes {
		items = append(items, map[string]string{"code": code, "label": m.bundle.Label(code)})
	}
	raw, err := json.Marshal(map[string]any{
		"lang":     lang,
		"items":    items,
		"messages": m.bundle.Messages(lang),
	})
	if err != nil {
		return []byte(`{"lang":"zh","items":[],"messages":{}}`)
	}
	return raw
}
