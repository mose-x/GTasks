// GTasks 逆向工作台
"use strict";

let sniffCursor = 0;
let operations = [];
// 由 Go 直接合成 SOV0tb 同步报文的操作：没有 steps，但同样可执行。
let syncOps = new Set();
let selectedCapture = null;
let timers = [];
let historyNoted = false;

// Upper bound on rendered rows. A restart reloads the whole capture log into the
// ring, and one <li> per capture with its JSON bodies gets heavy quickly.
const MAX_RENDERED = 400;

/* ------------------------------------------------------------------ i18n */
// 两段 hint 里含 <code>/<strong>，走 innerHTML 整体替换（data-i18n 只能改纯文本）。
// 兜底中文就是 HTML 里原本那份，切语言时先存下来，之后按键取。
const HINT_ZH = {};
for (const id of ["sniffHint", "opsHint"]) {
  const node = document.getElementById(id);
  if (node) HINT_ZH[id] = node.innerHTML;
}

function applyHints() {
  for (const [id, fallback] of Object.entries(HINT_ZH)) {
    const node = document.getElementById(id);
    if (node) node.innerHTML = t(`wb.html.${id}`, fallback);
  }
}

// 两个 dump 的初始占位文案跟着语言走；一旦被探测结果 / 配置填上就不再动它。
const DUMP_IDS = ["sessionDump", "configDump"];
for (const id of DUMP_IDS) {
  const node = document.getElementById(id);
  if (node) node.dataset.gtasksPlaceholder = "1";
}

function applyDumpPlaceholders() {
  for (const id of DUMP_IDS) {
    const node = $(id);
    if (node && node.dataset.gtasksPlaceholder === "1") node.textContent = t("wb.html.notFetched", "尚未获取");
  }
}

function fillDump(id, text) {
  const node = $(id);
  if (!node) return;
  delete node.dataset.gtasksPlaceholder;
  node.textContent = text;
}

function renderLangButton() {
  const button = $("btnLang");
  if (!button) return;
  button.title = t("main.langTitle", "语言：{label}", { label: i18nLabel(i18nLang()) });
}

/* ------------------------------------------------------------------ tabs */

function switchTab(name) {
  document.querySelectorAll(".tab").forEach((node) => {
    node.classList.toggle("active", node.dataset.tab === name);
  });
  document.querySelectorAll(".tabpane").forEach((node) => {
    node.classList.toggle("active", node.id === `pane-${name}`);
  });
}

/* ----------------------------------------------------------------- state */

async function refreshState() {
  try {
    const state = await api("/api/state");
    $("readyDot").classList.toggle("on", !!state.ready);
    setBadge("badgeBridge", t("wb.bridge", "桥接: {state}", { state: state.ready ? t("wb.ready", "就绪") : t("wb.notReady", "未就绪") }), state.ready ? "ok" : "warn");

    const session = state.session;
    const health = (state.watcher && state.watcher.health) || "";
    const expired = !!(state.watcher && state.watcher.expired);
    if (session) {
      const note = expired ? t("wb.expired", "会话失效") : health && health !== "ok" ? health : "";
      setBadge(
        "badgeLogin",
        t("wb.login", "登录: {state}", { state: (session.loggedIn ? t("wb.yes", "是") : t("wb.no", "否")) + (note ? `（${note}）` : "") }),
        session.loggedIn ? (expired ? "warn" : "ok") : "bad",
      );
      setBadge("badgeToken", `at: ${session.at ? session.at.slice(0, 12) + "…" : "—"}`, session.at ? "ok" : "bad");
      setBadge("badgeBuild", `bl: ${session.bl || "—"}`, session.bl ? "ok" : "");
    } else {
      setBadge("badgeLogin", t("wb.loginUnknown", "登录: 未知"), "");
      setBadge("badgeToken", "at: —", "");
      setBadge("badgeBuild", "bl: —", "");
    }
    setBadge("badgeCount", t("wb.sniffBadge", "抓包: {count}{drop}", { count: state.sniffed, drop: state.dropped ? t("wb.dropped", " (丢弃 {count})", { count: state.dropped }) : "" }), state.sniffed ? "ok" : "");
    setBadge("badgeOps", t("wb.opsBadge", "操作: {ready}/{total}", { ready: state.opsReady, total: state.opsTotal }), state.opsReady ? "ok" : "");

    $("btnShowSession").textContent = state.sessionWindowVisible ? t("wb.showSessionChecked", "显示会话窗口 ✓") : t("wb.showSession", "显示会话窗口");
    $("chkAutoManage").checked = !!state.autoManage;
    $("chkAutoSniff").checked = state.watcher ? true : $("chkAutoSniff").checked;
  } catch (error) {
    setBadge("badgeBridge", t("wb.bridgeErr", "桥接: {message}", { message: error.message }), "bad");
  }
}

async function probe() {
  try {
    const session = await api("/api/probe", { method: "POST" });
    fillDump("sessionDump", pretty(session));
    toast(t("wb.sessionReady", "会话就绪 · {login} · {origin}", { login: session.loggedIn ? t("wb.loggedIn", "已登录") : t("wb.notLogged", "未登录"), origin: session.origin }));
    await refreshState();
  } catch (error) {
    fillDump("sessionDump", t("wb.probeFailed", "探测失败: {message}", { message: error.message }));
    toast(error.message, true);
  }
}

async function loadConfig() {
  try {
    const config = await api("/api/config");
    fillDump("configDump", pretty(config));
    $("opsPath").textContent = config.operations || t("wb.inMemory", "(内存)");
  } catch {
    /* 非关键路径 */
  }
}

async function sessionAction(action) {
  try {
    await api("/api/session", { method: "POST", body: { action } });
    await refreshState();
  } catch (error) {
    toast(error.message, true);
  }
}

async function setAutoFlags() {
  try {
    await api("/api/session/auto", {
      method: "POST",
      body: {
        autoManage: $("chkAutoManage").checked,
        autoSniff: $("chkAutoSniff").checked,
      },
    });
    toast(t("wb.autoToast", "自动隐藏会话: {a} · 自动抓包: {b}", { a: $("chkAutoManage").checked ? t("wb.on", "开") : t("wb.off", "关"), b: $("chkAutoSniff").checked ? t("wb.on", "开") : t("wb.off", "关") }));
  } catch (error) {
    toast(error.message, true);
  }
}

/* ----------------------------------------------------------------- sniff */

async function setSniff(on) {
  try {
    const control = await api("/api/sniff", { method: "POST", body: { on } });
    toast(control.sniffing ? t("wb.sniffOn", "抓包已开启，去会话窗口里操作吧") : t("wb.sniffOff", "抓包已停止"));
    await pollSniff();
  } catch (error) {
    toast(error.message, true);
  }
}

async function pollSniff() {
  let payload;
  try {
    payload = await api(`/api/sniff?since=${sniffCursor}`);
  } catch {
    return;
  }
  const entries = payload.entries || [];
  if (entries.length === 0) return;
  // Render only the tail when a large history comes back at once. The cursor
  // still advances past the skipped rows, so they are not fetched again.
  let visible = entries;
  if (entries.length > MAX_RENDERED) {
    visible = entries.slice(-MAX_RENDERED);
    if (!historyNoted) {
      historyNoted = true;
      toast(t("wb.sniffTruncated", "历史抓包较多，仅显示最近 {count} 条（完整内容用「导出」）", { count: MAX_RENDERED }));
    }
  }
  for (const entry of visible) {
    appendEntry(entry);
    sniffCursor = entry.seq;
  }
  trimSniffList();
  setBadge("badgeCount", t("wb.sniffBadge", "抓包: {count}{drop}", { count: payload.seq, drop: payload.dropped ? t("wb.dropped", " (丢弃 {count})", { count: payload.dropped }) : "" }), "ok");
}

// Newest entries are prepended, so the oldest ones sit at the bottom.
function trimSniffList() {
  const list = $("sniffList");
  while (list.children.length > MAX_RENDERED) list.removeChild(list.lastElementChild);
}

function parseEntry(entry) {
  try {
    return typeof entry.raw === "string" ? JSON.parse(entry.raw) : entry.raw || {};
  } catch {
    return {};
  }
}

// 连续的「内部同步」抓包（侧栏列表选项为未勾选清单临时显隐产生的 show/hide
// RPC）折叠成一行：它们是桌面化改造的副作用，不是逆向时关心的操作；内容不丢，
// 点开折叠行每条原始记录都在。
let internalGroup = null;

function appendEntry(entry) {
  const detail = parseEntry(entry);
  if (detail && detail.internal === true) {
    if (internalGroup && internalGroup.isConnected) {
      extendInternalGroup(internalGroup, entry, detail);
      return;
    }
    internalGroup = makeInternalGroup(entry, detail);
    $("sniffList").prepend(internalGroup);
    return;
  }
  internalGroup = null;
  $("sniffList").prepend(makeEntryItem(entry, detail));
}

function makeInternalGroup(entry, detail) {
  const item = document.createElement("li");
  item.className = "sniff-item sniff-group";
  item.dataset.seq = String(entry.seq);
  item._entries = [];
  const head = document.createElement("div");
  head.className = "sniff-head";
  head.innerHTML =
    `<span class="sniff-time"></span><span class="sniff-ids"></span><span class="sniff-status ok"></span>`;
  head.addEventListener("click", () => {
    item.classList.toggle("open");
    if (item.classList.contains("open") && !item._rendered) {
      item._rendered = true;
      const body = item.querySelector(".sniff-body");
      for (const child of item._entries) body.append(makeEntryItem(child.entry, child.detail));
    }
  });
  const body = document.createElement("div");
  body.className = "sniff-body";
  item.append(head, body);
  extendInternalGroup(item, entry, detail);
  return item;
}

function extendInternalGroup(item, entry, detail) {
  item._entries.push({ entry, detail });
  const first = item._entries[0];
  const last = item._entries[item._entries.length - 1];
  const stamp = new Date(detail.t || entry.at || Date.now()).toLocaleTimeString("zh-CN", { hour12: false });
  const head = item.querySelector(".sniff-head");
  head.querySelector(".sniff-time").textContent = `#${first.entry.seq}…#${last.entry.seq} ${stamp}`;
  head.querySelector(".sniff-ids").textContent = t("wb.internalGroup", "内部同步调用（临时显隐清单的副作用） ×{count}", { count: item._entries.length });
  head.querySelector(".sniff-status").textContent = String(detail.status ?? "?");
}

function makeEntryItem(entry, detail) {
  const item = document.createElement("li");
  item.className = "sniff-item";
  item.dataset.seq = String(entry.seq);

  const ids = Array.isArray(detail.rpcids) && detail.rpcids.length ? detail.rpcids.join(", ") : t("wb.unknownRpc", "(未识别)");
  const status = detail.status ?? "?";
  const okStatus = Number(status) >= 200 && Number(status) < 300;
  const stamp = new Date(detail.t || entry.at || Date.now()).toLocaleTimeString("zh-CN", { hour12: false });

  const head = document.createElement("div");
  head.className = "sniff-head";
  head.innerHTML =
    `<span class="sniff-time"></span><span class="sniff-ids"></span><span class="sniff-status ${okStatus ? "ok" : "bad"}"></span>`;
  head.querySelector(".sniff-time").textContent = `#${entry.seq} ${stamp}`;
  head.querySelector(".sniff-ids").textContent = ids;
  head.querySelector(".sniff-status").textContent = String(status);
  head.addEventListener("click", () => item.classList.toggle("open"));

  const body = document.createElement("div");
  body.className = "sniff-body";

  const meta = document.createElement("div");
  meta.className = "hint";
  meta.textContent = `${detail.via || "?"} · ${detail.url || ""}`;

  body.append(meta, section(t("wb.sectionReq", "f.req（请求参数）"), detail.freq !== undefined ? pretty(detail.freq) : detail.freqRaw || t("wb.none", "(无)")));
  body.append(section(t("wb.sectionResp", "原始响应"), detail.response || t("wb.none", "(无)")));

  const tools = document.createElement("div");
  tools.className = "sniff-tools";

  const decodeBtn = document.createElement("button");
  decodeBtn.textContent = t("wb.decode", "解码响应");
  decodeBtn.addEventListener("click", async (event) => {
    event.stopPropagation();
    try {
      const decoded = await api("/api/decode", { method: "POST", body: { raw: detail.response || "" } });
      body.append(section(t("wb.sectionDecoded", "解码结果"), pretty(decoded.responses)));
      item.classList.add("open");
    } catch (error) {
      toast(error.message, true);
    }
  });

  const codegenBtn = document.createElement("button");
  codegenBtn.className = "primary";
  codegenBtn.textContent = t("wb.codegen", "生成代码 / 固化为操作");
  codegenBtn.addEventListener("click", async (event) => {
    event.stopPropagation();
    await selectCapture(entry, detail);
  });

  tools.append(decodeBtn, codegenBtn);
  body.append(tools);

  item.append(head, body);
  return item;
}

function section(title, text) {
  const wrapper = document.createDocumentFragment();
  const heading = document.createElement("h4");
  heading.textContent = title;
  const pre = document.createElement("pre");
  pre.textContent = text;
  wrapper.append(heading, pre);
  return wrapper;
}

async function exportSniff() {
  try {
    const response = await fetch("/api/sniff/export");
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    const blob = await response.blob();
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = "gtasks-captures.jsonl";
    link.click();
    URL.revokeObjectURL(url);
    toast(t("wb.exported", "已导出当前内存中的抓包记录"));
  } catch (error) {
    toast(error.message, true);
  }
}

/* --------------------------------------------------------------- codegen */

async function selectCapture(entry, detail) {
  document.querySelectorAll(".sniff-item.selected").forEach((node) => node.classList.remove("selected"));
  const node = document.querySelector(`.sniff-item[data-seq="${entry.seq}"]`);
  if (node) node.classList.add("selected");
  selectedCapture = entry;

  switchTab("sniff");
  $("codegenPanel").hidden = false;
  $("codegenMeta").textContent =
    `#${entry.seq} · ${(detail.rpcids || []).join(", ") || t("wb.unknownRpc", "(未识别)")} · HTTP ${detail.status ?? "?"}`;

  try {
    const generated = await api("/api/codegen", { method: "POST", body: { entry: entry.raw } });
    $("codegenSteps").value = JSON.stringify(generated.steps, null, 2);
    $("codegenGo").textContent = generated.go;
    $("codegenWarnings").innerHTML = "";
    for (const warning of generated.warnings || []) {
      const line = document.createElement("div");
      line.textContent = `⚠ ${warning}`;
      $("codegenWarnings").append(line);
    }
  } catch (error) {
    $("codegenSteps").value = "";
    $("codegenGo").textContent = t("wb.codegenFailed", "生成失败: {message}", { message: error.message });
  }
  await loadOps();
  $("codegenPanel").scrollIntoView({ behavior: "smooth", block: "nearest" });
}

async function saveAsOperation() {
  const op = $("selectOp").value;
  if (!op) {
    toast(t("wb.pickOpFirst", "请先选择要固化的操作"), true);
    return;
  }
  let steps;
  try {
    steps = JSON.parse($("codegenSteps").value);
  } catch (error) {
    toast(t("wb.stepsInvalid", "steps 不是合法 JSON: {message}", { message: error.message }), true);
    return;
  }
  try {
    await api("/api/ops", { method: "POST", body: { op, steps } });
    toast(t("wb.opSaved", "已保存 {op}（{count} 个 RPC）", { op, count: steps.length }));
    await loadOps();
    await refreshState();
  } catch (error) {
    toast(error.message, true);
  }
}

/* ------------------------------------------------------------------- ops */

async function loadOps() {
  try {
    const payload = await api("/api/ops");
    operations = payload.operation || [];
    syncOps = new Set(payload.sync || []);
    renderOps();
    renderOpSelects();
    setBadge("badgeOps", t("wb.opsBadge", "操作: {ready}/{total}", { ready: payload.ready, total: payload.total }), payload.ready ? "ok" : "");
  } catch (error) {
    toast(error.message, true);
  }
}

// opExecutable 判断一个操作能不能跑：要么已经固化了 rpcid，要么写入层已经用
// 同步通道实现。
function opExecutable(definition) {
  return !!(definition.steps && definition.steps.length) || syncOps.has(definition.op);
}

function renderOps() {
  const list = $("opsList");
  list.innerHTML = "";
  for (const definition of operations) {
    const item = document.createElement("li");
    item.className = "op-item";

    const head = document.createElement("div");
    head.className = "op-head";
    const state = document.createElement("span");
    state.className = "op-state";
    state.textContent = opExecutable(definition) ? "✅" : "⏳";
    const label = document.createElement("span");
    label.className = "op-label";
    label.textContent = definition.label || definition.op;
    const key = document.createElement("span");
    key.className = "op-key";
    key.textContent = definition.op;
    const rpcids = document.createElement("span");
    rpcids.className = "op-rpcids";
    rpcids.textContent = (definition.steps || []).map((step) => step.rpcid).join(", ")
      || (syncOps.has(definition.op) ? t("wb.syncChannel", "SOV0tb（同步通道）") : "");
    head.append(state, label, key, rpcids);
    head.addEventListener("click", () => item.classList.toggle("open"));

    const body = document.createElement("div");
    body.className = "op-body";
    if (definition.notes) {
      const notes = document.createElement("p");
      notes.className = "op-notes";
      notes.textContent = definition.notes;
      body.append(notes);
    }
    body.append(section("steps", JSON.stringify(definition.steps || [], null, 2)));

    const runBtn = document.createElement("button");
    runBtn.textContent = t("wb.fillExec", "填入执行面板");
    runBtn.addEventListener("click", (event) => {
      event.stopPropagation();
      $("execOp").value = definition.op;
      switchTab("ops");
      $("execParams").focus();
    });
    body.append(runBtn);

    item.append(head, body);
    list.append(item);
  }
}

function renderOpSelects() {
  const saveSelect = $("selectOp");
  const execSelect = $("execOp");
  const previousSave = saveSelect.value;
  const previousExec = execSelect.value;

  saveSelect.innerHTML = "";
  execSelect.innerHTML = "";

  const pending = document.createElement("optgroup");
  pending.label = t("wb.pending", "待发现");
  const ready = document.createElement("optgroup");
  ready.label = t("wb.configured", "已配置");

  for (const definition of operations) {
    const isReady = opExecutable(definition);
    const option = document.createElement("option");
    option.value = definition.op;
    option.textContent = `${definition.label || definition.op} (${definition.op})`;
    (isReady ? ready : pending).append(option);

    const execOption = option.cloneNode(true);
    execOption.disabled = !isReady;
    execSelect.append(execOption);
  }
  saveSelect.append(pending, ready);

  if (previousSave) saveSelect.value = previousSave;
  if (previousExec) execSelect.value = previousExec;
}

async function executeOperation() {
  const op = $("execOp").value;
  let params;
  try {
    params = JSON.parse($("execParams").value || "{}");
  } catch (error) {
    toast(t("wb.paramsInvalid", "参数不是合法 JSON: {message}", { message: error.message }), true);
    return;
  }
  try {
    const payload = await api("/api/ops/execute", { method: "POST", body: { op, params } });
    $("execOutput").value = pretty(payload);
    toast(payload.error ? t("wb.execError", "已返回，但有错误: {message}", { message: payload.error }) : t("wb.execDone", "{op} 执行完成", { op }), !!payload.error);
  } catch (error) {
    $("execOutput").value = t("wb.execFailed", "执行失败: {message}", { message: error.message });
    toast(error.message, true);
  }
}

/* ---------------------------------------------------------------- manual */

function parseArgs(text, count) {
  const lines = text.split("\n").map((line) => line.trim()).filter((line) => line.length > 0);
  const args = [];
  for (let index = 0; index < count; index++) {
    const line = lines[index];
    args.push(line ? JSON.parse(line) : []);
  }
  return args;
}

async function sendCall() {
  const rpcids = $("inputRpcids").value.split(",").map((part) => part.trim()).filter(Boolean);
  if (rpcids.length === 0) {
    toast(t("wb.needRpcids", "请先填写 rpcids"), true);
    return;
  }
  let args;
  try {
    args = parseArgs($("inputArgs").value, rpcids.length);
  } catch (error) {
    toast(t("wb.argsInvalid", "参数 JSON 解析失败: {message}", { message: error.message }), true);
    return;
  }

  const opts = { path: $("inputPath").value.trim() || undefined, rt: $("inputRT").value.trim() };
  const at = $("inputAt").value.trim();
  const bl = $("inputBl").value.trim();
  if (at) opts.at = at;
  if (bl) opts.bl = bl;

  try {
    const payload = await api("/api/call", { method: "POST", body: { rpcids, args, opts } });
    $("outputRaw").value = pretty(payload);
    if (payload.decodeError) toast(t("wb.decodeFailed", "已返回，但解码失败: {message}", { message: payload.decodeError }), true);
    else toast(t("wb.callDone", "调用完成"));
  } catch (error) {
    $("outputRaw").value = t("wb.callFailed", "调用失败: {message}", { message: error.message });
    toast(error.message, true);
  }
}

async function decodeRaw() {
  const raw = $("outputRaw").value;
  if (!raw.trim()) {
    toast(t("wb.nothingToDecode", "没有可解码的内容"), true);
    return;
  }
  let body = raw;
  try {
    const parsed = JSON.parse(raw);
    if (parsed && parsed.result && typeof parsed.result.raw === "string") body = parsed.result.raw;
  } catch {
    /* 用户直接粘贴了原始响应 */
  }
  try {
    const decoded = await api("/api/decode", { method: "POST", body: { raw: body } });
    $("outputRaw").value = pretty(decoded.responses);
  } catch (error) {
    toast(error.message, true);
  }
}

/* ------------------------------------------------------------ misc utils */

async function copyText(text, label) {
  try {
    await navigator.clipboard.writeText(text);
    toast(t("wb.copied", "已复制{label}", { label }));
  } catch {
    toast(t("wb.copyFailed", "复制失败，请手动选择文本"), true);
  }
}

function bindUI() {
  document.querySelectorAll(".tab").forEach((node) => {
    node.addEventListener("click", () => switchTab(node.dataset.tab));
  });

  $("btnLang").addEventListener("click", (event) => {
    openLangMenu(event.currentTarget).catch((error) =>
      toast(t("main.toastLangMenuFailed", "语言菜单打开失败：{message}", { message: error.message }), true),
    );
  });
  $("btnProbe").addEventListener("click", probe);
  $("btnShowSession").addEventListener("click", () => sessionAction("show"));
  $("btnHideSession").addEventListener("click", () => sessionAction("hide"));
  $("btnReloadSession").addEventListener("click", () => sessionAction("reload"));
  $("chkAutoManage").addEventListener("change", setAutoFlags);
  $("chkAutoSniff").addEventListener("change", setAutoFlags);

  $("btnSniffStart").addEventListener("click", () => setSniff(true));
  $("btnSniffStop").addEventListener("click", () => setSniff(false));
  $("btnSniffExport").addEventListener("click", exportSniff);
  $("btnSniffClear").addEventListener("click", async () => {
    await api("/api/sniff/clear", { method: "POST" });
    $("sniffList").innerHTML = "";
    sniffCursor = 0;
    toast(t("wb.cleared", "已清空内存中的抓包记录（磁盘日志保留）"));
    await refreshState();
  });

  $("btnCodegenClose").addEventListener("click", () => {
    $("codegenPanel").hidden = true;
    selectedCapture = null;
    document.querySelectorAll(".sniff-item.selected").forEach((node) => node.classList.remove("selected"));
  });
  $("btnSaveOp").addEventListener("click", saveAsOperation);

  $("btnOpsReload").addEventListener("click", loadOps);
  $("btnExec").addEventListener("click", executeOperation);

  $("btnCall").addEventListener("click", sendCall);
  $("btnDecode").addEventListener("click", decodeRaw);

  $("btnCopySession").addEventListener("click", () => copyText($("sessionDump").textContent, t("wb.sessionInfo", "会话信息")));
  $("btnCopyConfig").addEventListener("click", () => copyText($("configDump").textContent, t("wb.pathConfig", "路径配置")));
}

async function init() {
  await loadI18n();
  document.title = t("app.workbenchTitle", "GTasks 工作台");
  applyHints();
  applyDumpPlaceholders();
  renderLangButton();
  bindUI();
  // 切语言后：静态文案 applyI18n 自己刷，徽标 / 提示 / 列表这些动态内容在这里补。
  onI18n(() => {
    document.title = t("app.workbenchTitle", "GTasks 工作台");
    applyHints();
    applyDumpPlaceholders();
    renderLangButton();
    refreshState();
    loadConfig();
    loadOps();
  });
  refreshState();
  probe();
  loadConfig();
  loadOps();
  timers.push(setInterval(refreshState, 4000));
  timers.push(setInterval(pollSniff, 1200));
}

init();
window.addEventListener("beforeunload", () => timers.forEach(clearInterval));
