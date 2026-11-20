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
const THEME_LABEL = { light: "浅色", dark: "深色", system: "跟随系统" };

function themeMode() {
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

function setTheme(mode) {
  try {
    localStorage.setItem(THEME_KEY, mode);
  } catch {}
  return applyTheme(mode);
}

// Cycles 浅色 → 深色 → 跟随系统, mirroring the web app's theme setting.
function cycleTheme() {
  const mode = THEME_MODES[(THEME_MODES.indexOf(themeMode()) + 1) % THEME_MODES.length];
  return { mode, dark: setTheme(mode) };
}

function initTheme() {
  applyTheme(themeMode());
  if (typeof matchMedia !== "function") return;
  matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
    if (themeMode() === "system") applyTheme("system");
  });
}
