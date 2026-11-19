// GTasks 逆向工作台
"use strict";

let sniffCursor = 0;
let operations = [];
let selectedCapture = null;
let timers = [];
let historyNoted = false;

// Upper bound on rendered rows. A restart reloads the whole capture log into the
// ring, and one <li> per capture with its JSON bodies gets heavy quickly.
const MAX_RENDERED = 400;

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
    setBadge("badgeBridge", `桥接: ${state.ready ? "就绪" : "未就绪"}`, state.ready ? "ok" : "warn");

    const session = state.session;
    if (session) {
      setBadge("badgeLogin", `登录: ${session.loggedIn ? "是" : "否"}`, session.loggedIn ? "ok" : "bad");
      setBadge("badgeToken", `at: ${session.at ? session.at.slice(0, 12) + "…" : "—"}`, session.at ? "ok" : "bad");
      setBadge("badgeBuild", `bl: ${session.bl || "—"}`, session.bl ? "ok" : "");
    } else {
      setBadge("badgeLogin", "登录: 未知", "");
      setBadge("badgeToken", "at: —", "");
      setBadge("badgeBuild", "bl: —", "");
    }
    setBadge("badgeCount", `抓包: ${state.sniffed}${state.dropped ? ` (丢弃 ${state.dropped})` : ""}`, state.sniffed ? "ok" : "");
    setBadge("badgeOps", `操作: ${state.opsReady}/${state.opsTotal}`, state.opsReady ? "ok" : "");

    $("btnShowSession").textContent = state.sessionWindowVisible ? "显示会话窗口 ✓" : "显示会话窗口";
    $("chkAutoManage").checked = !!state.autoManage;
    $("chkAutoSniff").checked = state.watcher ? true : $("chkAutoSniff").checked;
  } catch (error) {
    setBadge("badgeBridge", `桥接: ${error.message}`, "bad");
  }
}

async function probe() {
  try {
    const session = await api("/api/probe", { method: "POST" });
    $("sessionDump").textContent = pretty(session);
    toast(`会话就绪 · ${session.loggedIn ? "已登录" : "未登录"} · ${session.origin}`);
    await refreshState();
  } catch (error) {
    $("sessionDump").textContent = `探测失败: ${error.message}`;
    toast(error.message, true);
  }
}

async function loadConfig() {
  try {
    const config = await api("/api/config");
    $("configDump").textContent = pretty(config);
    $("opsPath").textContent = config.operations || "(内存)";
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
    toast(`自动隐藏会话: ${$("chkAutoManage").checked ? "开" : "关"} · 自动抓包: ${$("chkAutoSniff").checked ? "开" : "关"}`);
  } catch (error) {
    toast(error.message, true);
  }
}

/* ----------------------------------------------------------------- sniff */

async function setSniff(on) {
  try {
    const control = await api("/api/sniff", { method: "POST", body: { on } });
    toast(control.sniffing ? "抓包已开启，去会话窗口里操作吧" : "抓包已停止");
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
      toast(`历史抓包较多，仅显示最近 ${MAX_RENDERED} 条（完整内容用「导出」）`);
    }
  }
  for (const entry of visible) {
    appendEntry(entry);
    sniffCursor = entry.seq;
  }
  trimSniffList();
  setBadge("badgeCount", `抓包: ${payload.seq}${payload.dropped ? ` (丢弃 ${payload.dropped})` : ""}`, "ok");
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

function appendEntry(entry) {
  const detail = parseEntry(entry);
  const item = document.createElement("li");
  item.className = "sniff-item";
  item.dataset.seq = String(entry.seq);

  const ids = Array.isArray(detail.rpcids) && detail.rpcids.length ? detail.rpcids.join(", ") : "(未识别)";
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

  body.append(meta, section("f.req（请求参数）", detail.freq !== undefined ? pretty(detail.freq) : detail.freqRaw || "(无)"));
  body.append(section("原始响应", detail.response || "(无)"));

  const tools = document.createElement("div");
  tools.className = "sniff-tools";

  const decodeBtn = document.createElement("button");
  decodeBtn.textContent = "解码响应";
  decodeBtn.addEventListener("click", async (event) => {
    event.stopPropagation();
    try {
      const decoded = await api("/api/decode", { method: "POST", body: { raw: detail.response || "" } });
      body.append(section("解码结果", pretty(decoded.responses)));
      item.classList.add("open");
    } catch (error) {
      toast(error.message, true);
    }
  });

  const codegenBtn = document.createElement("button");
  codegenBtn.className = "primary";
  codegenBtn.textContent = "生成代码 / 固化为操作";
  codegenBtn.addEventListener("click", async (event) => {
    event.stopPropagation();
    await selectCapture(entry, detail);
  });

  tools.append(decodeBtn, codegenBtn);
  body.append(tools);

  item.append(head, body);
  $("sniffList").prepend(item);
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
    toast("已导出当前内存中的抓包记录");
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
    `#${entry.seq} · ${(detail.rpcids || []).join(", ") || "(未识别)"} · HTTP ${detail.status ?? "?"}`;

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
    $("codegenGo").textContent = `生成失败: ${error.message}`;
  }
  await loadOps();
  $("codegenPanel").scrollIntoView({ behavior: "smooth", block: "nearest" });
}

async function saveAsOperation() {
  const op = $("selectOp").value;
  if (!op) {
    toast("请先选择要固化的操作", true);
    return;
  }
  let steps;
  try {
    steps = JSON.parse($("codegenSteps").value);
  } catch (error) {
    toast(`steps 不是合法 JSON: ${error.message}`, true);
    return;
  }
  try {
    await api("/api/ops", { method: "POST", body: { op, steps } });
    toast(`已保存 ${op}（${steps.length} 个 RPC）`);
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
    renderOps();
    renderOpSelects();
    setBadge("badgeOps", `操作: ${payload.ready}/${payload.total}`, payload.ready ? "ok" : "");
  } catch (error) {
    toast(error.message, true);
  }
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
    state.textContent = definition.steps && definition.steps.length ? "✅" : "⏳";
    const label = document.createElement("span");
    label.className = "op-label";
    label.textContent = definition.label || definition.op;
    const key = document.createElement("span");
    key.className = "op-key";
    key.textContent = definition.op;
    const rpcids = document.createElement("span");
    rpcids.className = "op-rpcids";
    rpcids.textContent = (definition.steps || []).map((step) => step.rpcid).join(", ");
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
    runBtn.textContent = "填入执行面板";
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
  pending.label = "待发现";
  const ready = document.createElement("optgroup");
  ready.label = "已配置";

  for (const definition of operations) {
    const isReady = definition.steps && definition.steps.length > 0;
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
    toast(`参数不是合法 JSON: ${error.message}`, true);
    return;
  }
  try {
    const payload = await api("/api/ops/execute", { method: "POST", body: { op, params } });
    $("execOutput").value = pretty(payload);
    toast(payload.error ? `已返回，但有错误: ${payload.error}` : `${op} 执行完成`, !!payload.error);
  } catch (error) {
    $("execOutput").value = `执行失败: ${error.message}`;
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
    toast("请先填写 rpcids", true);
    return;
  }
  let args;
  try {
    args = parseArgs($("inputArgs").value, rpcids.length);
  } catch (error) {
    toast(`参数 JSON 解析失败: ${error.message}`, true);
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
    if (payload.decodeError) toast(`已返回，但解码失败: ${payload.decodeError}`, true);
    else toast("调用完成");
  } catch (error) {
    $("outputRaw").value = `调用失败: ${error.message}`;
    toast(error.message, true);
  }
}

async function decodeRaw() {
  const raw = $("outputRaw").value;
  if (!raw.trim()) {
    toast("没有可解码的内容", true);
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
    toast(`已复制${label}`);
  } catch {
    toast("复制失败，请手动选择文本", true);
  }
}

function bindUI() {
  document.querySelectorAll(".tab").forEach((node) => {
    node.addEventListener("click", () => switchTab(node.dataset.tab));
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
    toast("已清空内存中的抓包记录（磁盘日志保留）");
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

  $("btnCopySession").addEventListener("click", () => copyText($("sessionDump").textContent, "会话信息"));
  $("btnCopyConfig").addEventListener("click", () => copyText($("configDump").textContent, "路径配置"));
}

bindUI();
refreshState();
probe();
loadConfig();
loadOps();
timers.push(setInterval(refreshState, 4000));
timers.push(setInterval(pollSniff, 1200));
window.addEventListener("beforeunload", () => timers.forEach(clearInterval));
