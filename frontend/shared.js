// Shared helpers for both windows.
"use strict";

const $ = (id) => document.getElementById(id);

function toast(message, isError = false) {
  const node = $("toast");
  if (!node) return;
  node.textContent = message;
  node.classList.toggle("error", isError);
  node.classList.add("show");
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => node.classList.remove("show"), isError ? 4500 : 2200);
}

// 控制端口要求带自定义头（跨站防护）；内置资源服务器不校验，多带一个头无害。
const API_HEADERS = { "X-GTasks-Client": "1" };

async function api(path, { method = "GET", body } = {}) {
  const response = await fetch(path, {
    method,
    headers: body === undefined ? API_HEADERS : { ...API_HEADERS, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(payload.error || `HTTP ${response.status}`);
  }
  return payload;
}

function pretty(value) {
  if (value === undefined || value === null) return "";
  if (typeof value === "string") {
    try {
      return JSON.stringify(JSON.parse(value), null, 2);
    } catch {
      return value;
    }
  }
  return JSON.stringify(value, null, 2);
}

function setBadge(id, text, tone) {
  const node = $(id);
  if (!node) return;
  node.textContent = text;
  node.className = "badge" + (tone ? ` ${tone}` : "");
}

function ensureToast() {
  if ($("toast")) return;
  const node = document.createElement("div");
  node.id = "toast";
  node.className = "toast";
  document.body.append(node);
}

/* ----------------------------------------------------------------- theme */

const THEME_KEY = "gtasks-theme";
const THEME_MODES = ["light", "dark", "system"];

/* ------------------------------------------------------------------ i18n */
// 词表由 Go 侧统一持有（internal/i18n/locales/*.toml），前端只拿当前这份副本：
// 首屏 fetch /api/i18n，切语言后 Go 往每个窗口推 window.gtasksApplyI18n。
// 键缺失时回落到代码里的中文兜底字面量，词表没写全也不会开天窗。
const I18N = { lang: "zh", messages: {}, items: [] };
const i18nListeners = new Set();

function t(key, fallback, params) {
  let text = I18N.messages[key];
  if (typeof text !== "string" || text === "") text = fallback !== undefined ? fallback : key;
  if (params) {
    for (const [name, value] of Object.entries(params)) {
      text = text.split(`{${name}}`).join(String(value));
    }
  }
  return text;
}

function i18nLang() {
  return I18N.lang;
}

// 切换菜单里显示的名字（用该语言自己写，来自 toml 的 meta.label）。
function i18nLabel(code) {
  const item = I18N.items.find((entry) => entry.code === code);
  return item ? item.label : code;
}

// 动态文案（列表、横幅、toast）注册重渲染回调；切换语言时统一触发。
function onI18n(fn) {
  i18nListeners.add(fn);
  return () => i18nListeners.delete(fn);
}

// 静态文案走 data-i18n 属性：首次应用时把 DOM 里的中文存成兜底，之后按键取。
function applyI18nDom(scope) {
  const root = scope || document;
  root.querySelectorAll("[data-i18n]").forEach((node) => {
    if (node.dataset.i18nZh === undefined) node.dataset.i18nZh = node.textContent;
    node.textContent = t(node.getAttribute("data-i18n"), node.dataset.i18nZh);
  });
  root.querySelectorAll("[data-i18n-title]").forEach((node) => {
    if (node.dataset.i18nTitleZh === undefined) node.dataset.i18nTitleZh = node.title || "";
    node.title = t(node.getAttribute("data-i18n-title"), node.dataset.i18nTitleZh);
  });
  root.querySelectorAll("[data-i18n-placeholder]").forEach((node) => {
    if (node.dataset.i18nPlaceholderZh === undefined) {
      node.dataset.i18nPlaceholderZh = node.placeholder || "";
    }
    node.placeholder = t(node.getAttribute("data-i18n-placeholder"), node.dataset.i18nPlaceholderZh);
  });
}

function applyI18n(payload) {
  if (!payload) return;
  I18N.lang = payload.lang || I18N.lang;
  I18N.messages = payload.messages || {};
  I18N.items = Array.isArray(payload.items) ? payload.items : I18N.items;
  document.documentElement.setAttribute("lang", I18N.lang);
  applyI18nDom(document);
  for (const fn of i18nListeners) fn();
  document.dispatchEvent(new CustomEvent("gtasks-i18n", { detail: I18N.lang }));
}

async function loadI18n() {
  try {
    applyI18n(await api("/api/i18n"));
  } catch {
    applyI18nDom(document);
  }
  return I18N.lang;
}

// 原生菜单要弹回发起请求的那个窗口：主窗口与工作台共用这套 HTTP 接口，坐标却
// 是各自 webview 里的，所以把来源窗口一起报给 Go。
function windowSource() {
  return location.pathname.indexOf("workbench") >= 0 ? "workbench" : "main";
}

// 语言菜单和主题菜单一样是 Go 弹的原生单选菜单（菜单项由打包的 toml 决定），
// 前端只负责把按钮位置报上去，主窗口和会话页顶栏那颗按钮弹出来的东西一致。
async function openLangMenu(button) {
  const rect = button.getBoundingClientRect();
  await api("/api/i18n/menu", {
    method: "POST",
    body: { x: Math.round(rect.left), y: Math.round(rect.bottom + 4), source: windowSource() },
  });
}

// Go 切换语言后往每个窗口推这个函数。
window.gtasksApplyI18n = (payload) => applyI18n(payload);

function themeLabel(mode) {
  const keys = { light: "theme.light", dark: "theme.dark", system: "theme.system" };
  const zh = { light: "明亮", dark: "暗黑", system: "跟随系统" };
  return t(keys[mode] || "theme.system", zh[mode] || mode);
}

// Go 侧是主题的唯一事实来源：它同时切换 macOS 原生外观（标题栏与所有
// webview 的 prefers-color-scheme 一起变）。localStorage 只作首屏缓存，
// 避免窗口先闪一帧错误配色再去等 /api/theme 的往返。
let serverThemeMode = null;

function themeMode() {
  if (serverThemeMode) return serverThemeMode;
  try {
    const stored = localStorage.getItem(THEME_KEY);
    return THEME_MODES.includes(stored) ? stored : "system";
  } catch {
    return "system";
  }
}

function systemPrefersDark() {
  return typeof matchMedia === "function" && matchMedia("(prefers-color-scheme: dark)").matches;
}

// Applies the mode to <html data-theme> and reports whether it resolved to dark.
function applyTheme(mode) {
  const dark = mode === "dark" || (mode === "system" && systemPrefersDark());
  document.documentElement.dataset.theme = dark ? "dark" : "light";
  return dark;
}

function rememberTheme(mode) {
  serverThemeMode = THEME_MODES.includes(mode) ? mode : "system";
  try {
    localStorage.setItem(THEME_KEY, serverThemeMode);
  } catch {}
  return applyTheme(serverThemeMode);
}

// 主题菜单是 Go 弹的原生单选菜单，前端只负责把按钮位置报上去，
// 这样主窗口和会话页顶栏那颗按钮弹出来的东西完全一致。
async function openThemeMenu(button) {
  const rect = button.getBoundingClientRect();
  await api("/api/theme/menu", {
    method: "POST",
    body: { x: Math.round(rect.left), y: Math.round(rect.bottom + 4), source: windowSource() },
  });
}

// 拉一次服务端主题；失败就退回本地缓存，界面不至于没配色。
async function loadServerTheme() {
  try {
    const payload = await api("/api/theme");
    rememberTheme(payload.mode);
  } catch {
    applyTheme(themeMode());
  }
  return themeMode();
}

// Go 切换原生外观后往每个窗口推这个函数。
window.__gtasksTheme = (mode) => {
  rememberTheme(mode);
  document.dispatchEvent(new CustomEvent("gtasks-theme", { detail: mode }));
};

function initTheme() {
  applyTheme(themeMode());
  if (typeof matchMedia !== "function") return;
  matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
    if (themeMode() === "system") applyTheme("system");
  });
}
