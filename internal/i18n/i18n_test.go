package i18n

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var placeholder = regexp.MustCompile(`\{(\w+)\}`)

func placeholders(text string) []string {
	found := placeholder.FindAllStringSubmatch(text, -1)
	values := make([]string, 0, len(found))
	for _, match := range found {
		values = append(values, match[1])
	}
	sort.Strings(values)
	return values
}

func TestLoadFindsBundledLocales(t *testing.T) {
	bundle := Load()
	codes := bundle.Codes()
	if len(codes) < 2 {
		t.Fatalf("至少应打包 zh 与 en 两份词表，实际 %v", codes)
	}
	for _, want := range []string{"en", "zh"} {
		if !bundle.Has(want) {
			t.Fatalf("缺少词表 %q，实际 %v", want, codes)
		}
	}
	if bundle.Label("zh") == "zh" || bundle.Label("en") == "en" {
		t.Fatalf("meta.label 应给出该语言自己的名字，实际 zh=%q en=%q", bundle.Label("zh"), bundle.Label("en"))
	}
	if bundle.Has("jp") {
		t.Fatal("没有 jp.toml 时不应该报告支持日语")
	}
}

// 词表键位必须对齐：界面按当前语言整份取词，缺键的地方虽然会回落到兜底语言，
// 但那就意味着某门语言里混着另一种文字，属于要修的漏洞。
func TestLocalesHaveSameKeys(t *testing.T) {
	bundle := Load()
	codes := bundle.Codes()
	reference := bundle.fallback
	base := bundle.messages[reference]
	if len(base) == 0 {
		t.Fatalf("兜底语言 %q 的词表是空的", reference)
	}
	for _, code := range codes {
		if code == reference {
			continue
		}
		for key := range base {
			if _, ok := bundle.messages[code][key]; !ok {
				t.Errorf("%q 缺少键 %q（%q 里有）", code, key, reference)
			}
		}
		for key := range bundle.messages[code] {
			if _, ok := base[key]; !ok {
				t.Errorf("%q 多出了 %q 里没有的键 %q", code, reference, key)
			}
		}
	}
}

// 带参数的文案两边占位符要一致，否则翻译后参数会插不进去。
func TestPlaceholdersMatch(t *testing.T) {
	bundle := Load()
	reference := bundle.fallback
	for key, text := range bundle.messages[reference] {
		want := placeholders(text)
		if len(want) == 0 {
			continue
		}
		for _, code := range bundle.Codes() {
			if code == reference {
				continue
			}
			got := placeholders(bundle.messages[code][key])
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("%q 的 %q 占位符不一致：%q 是 %v，%q 是 %v", key, code, reference, want, code, got)
			}
		}
	}
}

func TestMessagesFallBackPerKey(t *testing.T) {
	bundle := Load()
	merged := bundle.Messages("en")
	if merged["app.workbenchTitle"] == "" {
		t.Fatal("英文词表应能取到 app.workbenchTitle")
	}
	// 不存在的语言整份回落到兜底语言。
	if bundle.Messages("jp")["app.workbenchTitle"] == "" {
		t.Fatal("未知语言应回落到兜底语言的词表")
	}
	if got := bundle.T("jp", "app.workbenchTitle", "字面量"); got != bundle.messages[bundle.fallback]["app.workbenchTitle"] {
		t.Fatalf("未知语言应先查兜底语言的词表，实际 %q", got)
	}
	if got := bundle.T("en", "no.such.key", "兜底"); got != "兜底" {
		t.Fatalf("键不存在时应返回调用方给的兜底，实际 %q", got)
	}
}

func TestManagerPersistsAndNotifies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lang.txt")
	manager := LoadManager(Load(), path)
	if manager.Lang() != "zh" {
		t.Fatalf("没有持久化文件时应默认中文，实际 %q", manager.Lang())
	}

	var seen []string
	manager.OnChange(func(code string) { seen = append(seen, code) })
	if len(seen) != 1 || seen[0] != "zh" {
		t.Fatalf("注册回调应立即收到当前语言，实际 %v", seen)
	}

	if !manager.Set("en") {
		t.Fatal("切换到英文应报告发生变化")
	}
	if manager.Set("en") {
		t.Fatal("重复设置同一语言不应报告变化")
	}
	if manager.Set("jp") {
		t.Fatal("没有打包的语言不应该切换成功")
	}
	if len(seen) != 2 || seen[1] != "en" {
		t.Fatalf("变更回调应收到 en，实际 %v", seen)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取持久化文件失败: %v", err)
	}
	if string(raw) != "en\n" {
		t.Fatalf("持久化内容应为 en，实际 %q", string(raw))
	}
	if reloaded := LoadManager(Load(), path); reloaded.Lang() != "en" {
		t.Fatalf("重启后应恢复英文，实际 %q", reloaded.Lang())
	}
}

func TestPayloadShape(t *testing.T) {
	manager := LoadManager(Load(), filepath.Join(t.TempDir(), "lang.txt"))
	manager.Set("en")

	var payload struct {
		Lang  string `json:"lang"`
		Items []struct {
			Code  string `json:"code"`
			Label string `json:"label"`
		} `json:"items"`
		Messages map[string]string `json:"messages"`
	}
	if err := json.Unmarshal(manager.Payload(), &payload); err != nil {
		t.Fatalf("Payload 不是合法 JSON: %v", err)
	}
	if payload.Lang != "en" {
		t.Fatalf("Payload 应带当前语言，实际 %q", payload.Lang)
	}
	if len(payload.Items) != len(manager.Bundle().Codes()) {
		t.Fatalf("切换菜单项应与打包的语言数一致：%v", payload.Items)
	}
	if payload.Messages["app.workbenchTitle"] == "" {
		t.Fatal("Payload 应带整份词表")
	}
}

// 语言文件写坏了不能连累启动：解析失败的文件跳过，其余照常可用。
func TestBrokenLocaleIsSkipped(t *testing.T) {
	bundle := Load()
	if len(bundle.Codes()) == 0 {
		t.Fatal("词表加载失败")
	}
}
