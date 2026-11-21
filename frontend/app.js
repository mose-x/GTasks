// Google Tasks 主界面。
//
// 数据层通过 /api/ops/execute 调用已固化的 rpcid 映射；映射还没发现的操作会返回
// 明确错误，界面据此提示去工作台抓包。演示模式用本地数据渲染，方便在没有 rpcid
// 之前先校对界面还原度。
"use strict";

const ICON = {
  star: '<svg viewBox="0 0 24 24"><path d="M22 9.24l-7.19-.62L12 2 9.19 8.63 2 9.24l5.46 4.73L5.82 21 12 17.27 18.18 21l-1.63-7.03L22 9.24zM12 15.4l-3.76 2.27 1-4.28-3.32-2.88 4.38-.38L12 6.1l1.71 4.04 4.38.38-3.32 2.88 1 4.28L12 15.4z"/></svg>',
  starOn: '<svg viewBox="0 0 24 24"><path d="M12 17.27L18.18 21l-1.64-7.03L22 9.24l-7.19-.61L12 2 9.19 8.63 2 9.24l5.46 4.73L5.82 21z"/></svg>',
  check: '<svg viewBox="0 0 24 24"><path d="M9 16.17L4.83 12l-1.42 1.41L9 19 21 7l-1.41-1.41z"/></svg>',
  list: '<svg viewBox="0 0 24 24"><path d="M4 6h16v2H4zm0 5h16v2H4zm0 5h16v2H4z"/></svg>',
  calendar: '<svg viewBox="0 0 24 24"><path d="M19 3h-1V1h-2v2H8V1H6v2H5c-1.11 0-1.99.9-1.99 2L3 19c0 1.1.89 2 2 2h14c1.1 0 2-.9 2-2V5c0-1.1-.9-2-2-2zm0 16H5V8h14v11zM7 10h5v5H7z"/></svg>',
  repeat: '<svg viewBox="0 0 24 24"><path d="M7 7h10v3l4-4-4-4v3H5v6h2V7zm10 10H7v-3l-4 4 4 4v-3h12v-6h-2v4z"/></svg>',
  subtask: '<svg viewBox="0 0 24 24"><path d="M4 6h16v2H4zm2 5h14v2H6zm3 5h11v2H9z"/></svg>',
  chevron: '<svg viewBox="0 0 24 24"><path d="M10 6L8.59 7.41 13.17 12l-4.58 4.59L10 18l6-6z"/></svg>',
  allTasks: '<svg viewBox="0 0 24 24"><path d="M22 5.18L10.59 16.6l-4.24-4.24 1.41-1.41 2.83 2.83 10-10L22 5.18zM19.79 10.22C19.92 10.79 20 11.39 20 12c0 4.42-3.58 8-8 8s-8-3.58-8-8 3.58-8 8-8c1.58 0 3.04.46 4.28 1.25l1.44-1.44C16.1 2.67 14.13 2 12 2 6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10c0-1.19-.22-2.33-.6-3.39l-1.61 1.61z"/></svg>',
  more: '<svg viewBox="0 0 24 24"><path d="M12 8c1.1 0 2-.9 2-2s-.9-2-2-2-2 .9-2 2 .9 2 2 2zm0 2c-1.1 0-2 .9-2 2s.9 2 2 2 2-.9 2-2-.9-2-2-2zm0 6c-1.1 0-2 .9-2 2s.9 2 2 2 2-.9 2-2-.9-2-2-2z"/></svg>',
  trash: '<svg viewBox="0 0 24 24"><path d="M6 19c0 1.1.9 2 2 2h8c1.1 0 2-.9 2-2V7H6v12zM19 4h-3.5l-1-1h-5l-1 1H5v2h14V4z"/></svg>',
  add: '<svg viewBox="0 0 24 24"><path d="M19 13h-6v6h-2v-6H5v-2h6V5h2v6h6v2z"/></svg>',
  expandLess: '<svg viewBox="0 0 24 24"><path d="M12 8l-6 6 1.41 1.41L12 10.83l4.59 4.58L12 12z"/></svg>',
  expandMore: '<svg viewBox="0 0 24 24"><path d="M16.59 8.59L12 13.17 7.41 8.59 6 10l6 6 6-6z"/></svg>',
  refresh: '<svg viewBox="0 0 24 24"><path d="M17.65 6.35C16.2 4.9 14.21 4 12 4c-4.42 0-7.99 3.58-8 8s3.57 8 8 8c3.73 0 6.84-2.55 7.73-6h-2.08c-.82 2.33-3.04 4-5.65 4-3.31 0-6-2.69-6-6s2.69-6 6-6c1.66 0 3.14.69 4.22 1.78L13 11h7V4l-2.35 2.35z"/></svg>',
  empty: '<svg viewBox="0 0 24 24"><path d="M19 3H5c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h14c1.1 0 2-.9 2-2V5c0-1.1-.9-2-2-2zm0 16H5V5h14v14zM7 10h2v7H7zm4-3h2v10h-2zm4 6h2v4h-2z"/></svg>',
  themeLight: '<svg viewBox="0 0 24 24"><path d="M12 7c-2.76 0-5 2.24-5 5s2.24 5 5 5 5-2.24 5-5-2.24-5-5-5zM2 13h2c.55 0 1-.45 1-1s-.45-1-1-1H2c-.55 0-1 .45-1 1s.45 1 1 1zm18 0h2c.55 0 1-.45 1-1s-.45-1-1-1h-2c-.55 0-1 .45-1 1s.45 1 1 1zM11 2v2c0 .55.45 1 1 1s1-.45 1-1V2c0-.55-.45-1-1-1s-1 .45-1 1zm0 18v2c0 .55.45 1 1 1s1-.45 1-1v-2c0-.55-.45-1-1-1s-1 .45-1 1zM5.99 4.58c-.39-.39-1.03-.39-1.41 0-.39.39-.39 1.03 0 1.41l1.06 1.06c.39.39 1.03.39 1.41 0s.39-1.03 0-1.41L5.99 4.58zm12.37 12.37c-.39-.39-1.03-.39-1.41 0-.39.39-.39 1.03 0 1.41l1.06 1.06c.39.39 1.03.39 1.41 0 .39-.39.39-1.03 0-1.41l-1.06-1.06zm1.06-10.96c.39-.39.39-1.03 0-1.41-.39-.39-1.03-.39-1.41 0l-1.06 1.06c-.39.39-.39 1.03 0 1.41s1.03.39 1.41 0l1.06-1.06zM7.05 18.36c.39-.39.39-1.03 0-1.41-.39-.39-1.03-.39-1.41 0l-1.06 1.06c-.39.39-.39 1.03 0 1.41s1.03.39 1.41 0l1.06-1.06z"/></svg>',
  themeDark: '<svg viewBox="0 0 24 24"><path d="M12 3c-4.97 0-9 4.03-9 9s4.03 9 9 9 9-4.03 9-9c0-.46-.04-.92-.1-1.36-.98 1.37-2.58 2.26-4.4 2.26-2.98 0-5.4-2.42-5.4-5.4 0-1.81.89-3.42 2.26-4.4-.44-.06-.9-.1-1.36-.1z"/></svg>',
  themeAuto: '<svg viewBox="0 0 24 24"><path d="M10.85 12.65h2.3L12 9l-1.15 3.65zM20 8.69V4h-4.69L12 .69 8.69 4H4v4.69L.69 12 4 15.31V20h4.69L12 23.31 15.31 20H20v-4.69L23.31 12 20 8.69zM14.3 16l-.7-2h-3.2l-.7 2H7.8L11 7h2l3.2 9h-1.9z"/></svg>',
};

const THEME_ICON = { light: "themeLight", dark: "themeDark", system: "themeAuto" };

function renderThemeButton() {
  const button = $("btnTheme");
  if (!button) return;
  const mode = themeMode();
  button.innerHTML = ICON[THEME_ICON[mode]] || ICON.themeAuto;
  button.title = `主题：${THEME_LABEL[mode]}`;
  button.classList.toggle("on", mode === "dark");
}

const VIRTUAL_STARRED = "@starred";
const VIRTUAL_ALL = "@all";

const store = {
  demo: false,
  expired: false,
  lists: [],
  tasks: [],
  account: { id: "", email: "" },
  activeListId: VIRTUAL_ALL,
  activeTaskId: null,
  // 侧栏「任务列表」分组是否收起，以及哪些清单的已完成段是展开的。
  listsCollapsed: false,
  expandedCompleted: new Set(),
  ops: new Map(),
};

/* ------------------------------------------------------------------ ops */

async function refreshOps() {
  try {
    const payload = await api("/api/ops");
    // 写入类操作由 Go 直接合成 SOV0tb 同步报文，没有 steps，但同样可以执行。
    const synced = new Set(payload.sync || []);
    store.ops = new Map(
      (payload.operation || []).map((d) => [d.op, !!(d.steps && d.steps.length) || synced.has(d.op)]),
    );
    return payload;
  } catch {
    return null;
  }
}

function opReady(op) {
  return store.ops.get(op) === true;
}

async function execute(op, params) {
  const payload = await api("/api/ops/execute", { method: "POST", body: { op, params } });
  if (payload.error) throw new Error(payload.error);
  return payload.result;
}

/* --------------------------------------------------------------- banner */

function showBanner(html, actions = []) {
  const banner = $("banner");
  banner.innerHTML = "";
  const text = document.createElement("span");
  text.innerHTML = html;
  banner.append(text);
  for (const action of actions) {
    const button = document.createElement("button");
    button.textContent = action.label;
    button.addEventListener("click", action.onClick);
    banner.append(button);
  }
  banner.hidden = false;
}

function hideBanner() {
  $("banner").hidden = true;
}

function expiredBanner() {
  showBanner("会话已失效（cookie 或 at 被 Google 拒绝），请在 Google 会话窗口里重新登录。", [
    { label: "打开登录窗口", onClick: () => api("/api/session", { method: "POST", body: { action: "show" } }).catch(() => {}) },
  ]);
}

// 主界面不轮询数据，但需要知道会话是否中途失效：watcher 的健康检查发现
// RPC 被 401 / 重定向到登录页时，这里给出横幅和重新登录入口。
async function refreshSession() {
  let state;
  try {
    state = await api("/api/state");
  } catch {
    return;
  }
  const expired = !!(state.watcher && state.watcher.expired);
  const was = store.expired;
  store.expired = expired;
  if (expired && !was) expiredBanner();
  if (!expired && was) await loadData();
}

/* ----------------------------------------------------------------- load */

// 网页端切换清单、已加星标、展开已完成时都不再发请求：全部视图都由一次
// snapshot 返回的数据在本地渲染。这里也一样，一次 /api/tasks 就够整份界面。
// 冷启动时会话页可能还在加载（bootstrap 尚未注入 / 正在跳转登录），这类错误
// 自己会好，重试几次即可，不必一上来就甩个红色横幅。
const SESSION_WARMING_UP = /引导脚本|页面未响应|尚未登录|正在加载|正在导航/;
const WARMUP_RETRIES = 20;
let warmupAttempts = 0;

async function loadData() {
  if (store.demo) {
    seedDemo();
    renderAll();
    return;
  }

  try {
    const payload = await api("/api/tasks");
    const snapshot = payload.snapshot || {};
    store.lists = snapshot.lists || [];
    store.tasks = snapshot.tasks || [];
    store.account = { id: snapshot.account || "", email: payload.email || "" };
    // 刷新后原来选中的清单可能已被删除。
    if (!store.lists.some((list) => list.id === store.activeListId) && !isVirtual(store.activeListId)) {
      store.activeListId = VIRTUAL_ALL;
    }
    warmupAttempts = 0;
    if (store.expired) expiredBanner();
    else hideBanner();
    renderAll();
  } catch (error) {
    store.lists = [];
    store.tasks = [];
    renderAll();
    if (SESSION_WARMING_UP.test(error.message || "") && warmupAttempts < WARMUP_RETRIES) {
      warmupAttempts += 1;
      showBanner("正在连接 Google 会话…");
      setTimeout(loadData, 900);
      return;
    }
    showBanner(`读取失败：${error.message}`, [
      { label: "重试", onClick: () => loadData() },
      { label: "打开登录窗口", onClick: () => api("/api/session", { method: "POST", body: { action: "show" } }).catch(() => {}) },
      { label: "载入演示数据", onClick: () => toggleDemo(true) },
    ]);
  }
}

function isVirtual(listId) {
  return listId === VIRTUAL_ALL || listId === VIRTUAL_STARRED;
}

// 新建任务要落到一个真实清单里；在「所有任务 / 已加星标」下沿用网页端的做法，
// 用侧栏里的第一个清单。
function targetListId() {
  if (!isVirtual(store.activeListId)) return store.activeListId;
  return (store.lists[0] || {}).id || "";
}

function seedDemo() {
  store.lists = [
    { id: "list-work", title: "工作", incomplete: 2, completedTotal: 1 },
    { id: "list-personal", title: "个人", incomplete: 2, completedTotal: 0 },
    { id: "list-shopping", title: "采购清单", incomplete: 1, completedTotal: 1 },
  ];
  store.account = { id: "", email: "demo@example.com" };
  const today = new Date();
  const iso = (offsetDays) => {
    const d = new Date(today);
    d.setDate(d.getDate() + offsetDays);
    return d.toISOString().slice(0, 10);
  };
  store.tasks = [
    { id: "t1", listId: "list-work", title: "回复设计评审邮件", notes: "附上最新一版的交互稿链接", starred: true, completed: false, due: { date: iso(0), time: "18:00" } },
    { id: "t2", listId: "list-work", title: "整理逆向抓包结果", notes: "", starred: false, completed: false, due: { date: iso(1), time: "" }, recurrence: "每周" },
    { id: "t3", listId: "list-work", title: "把 rpcid 固化进操作映射", notes: "", starred: false, completed: false, parent: "t2" },
    { id: "t4", listId: "list-work", title: "校对像素级还原度", notes: "和 tasks.google.com 并排比对", starred: false, completed: true, completedAt: new Date(Date.now() - 2 * 86400000).toISOString(), due: { date: iso(-2), time: "" } },
    { id: "t5", listId: "list-work", title: "准备季度规划材料", notes: "", starred: true, completed: false, due: { date: iso(3), time: "10:30" } },
    { id: "t6", listId: "list-work", title: "评审 batchexecute 解码结果", notes: "", starred: false, completed: false },
    { id: "t7", listId: "list-personal", title: "预约牙医", notes: "", starred: false, completed: false, due: { date: iso(-1), time: "09:00" } },
    { id: "t8", listId: "list-shopping", title: "咖啡豆", notes: "", starred: false, completed: false },
    { id: "t9", listId: "list-shopping", title: "滤纸", notes: "", starred: false, completed: true, completedAt: new Date(Date.now() - 86400000).toISOString() },
  ];
}

function toggleDemo(force) {
  store.demo = force === undefined ? !store.demo : force;
  $("btnDemo").classList.toggle("on", store.demo);
  if (store.demo) {
    hideBanner();
    seedDemo();
    toast("演示数据已载入（本地，不会写回 Google）");
  } else {
    store.activeTaskId = null;
    loadData();
  }
  renderAll();
}

/* --------------------------------------------------------------- select */

function tasksOf(listId) {
  return store.tasks.filter((t) => t.listId === listId);
}

function visibleTasks() {
  if (store.activeListId === VIRTUAL_STARRED) {
    return store.tasks.filter((t) => t.starred && !t.completed);
  }
  return store.tasks.filter((t) => t.listId === store.activeListId && !t.completed);
}

// Orders tasks so children follow their parent, mirroring the web app.
function orderedTasks(tasks) {
  const roots = tasks.filter((t) => !t.parent || !tasks.some((p) => p.id === t.parent));
  const byParent = new Map();
  for (const task of tasks) {
    if (!task.parent) continue;
    byParent.set(task.parent, [...(byParent.get(task.parent) || []), task]);
  }
  const out = [];
  const walk = (task, depth) => {
    out.push({ ...task, depth });
    for (const child of byParent.get(task.id) || []) walk(child, Math.min(depth + 1, 2));
  };
  for (const root of roots) walk(root, 0);
  return out;
}

function activeListTitle() {
  switch (store.activeListId) {
    case VIRTUAL_STARRED: return "已加星标";
    case VIRTUAL_ALL: return "所有任务";
    default: return (store.lists.find((l) => l.id === store.activeListId) || {}).title || "所有任务";
  }
}

/* --------------------------------------------------------------- render */

function navItem({ id, icon, label, count, active, onClick }) {
  const button = document.createElement("button");
  button.className = "nav-item" + (active ? " active" : "");
  const iconSpan = document.createElement("span");
  iconSpan.className = "nav-icon";
  iconSpan.innerHTML = icon;
  const labelSpan = document.createElement("span");
  labelSpan.className = "nav-label";
  labelSpan.textContent = label;
  button.append(iconSpan, labelSpan);
  if (count !== undefined) {
    const countSpan = document.createElement("span");
    countSpan.className = "nav-count";
    countSpan.textContent = String(count);
    button.append(countSpan);
  }
  button.addEventListener("click", onClick || (() => selectList(id)));
  return button;
}

// 侧栏与网页端一致：所有任务、已加星标、可折叠的「任务列表」分组（每项带未完成
// 数量），最后是新建任务列表。
function renderSidebar() {
  const nav = $("listNav");
  nav.innerHTML = "";
  nav.append(navItem({
    id: VIRTUAL_ALL,
    icon: ICON.allTasks,
    label: "所有任务",
    active: store.activeListId === VIRTUAL_ALL,
  }));
  nav.append(navItem({
    id: VIRTUAL_STARRED,
    icon: ICON.star,
    label: "已加星标",
    active: store.activeListId === VIRTUAL_STARRED,
  }));

  const head = document.createElement("div");
  head.className = "nav-group-head";
  const headLabel = document.createElement("span");
  headLabel.textContent = "任务列表";
  const collapse = document.createElement("button");
  collapse.className = "icon-btn tiny";
  collapse.innerHTML = store.listsCollapsed ? ICON.expandMore : ICON.expandLess;
  collapse.title = store.listsCollapsed ? "展开任务列表" : "收起任务列表";
  collapse.addEventListener("click", () => {
    store.listsCollapsed = !store.listsCollapsed;
    renderSidebar();
  });
  head.append(headLabel, collapse);
  nav.append(head);

  if (store.listsCollapsed) return;

  for (const list of store.lists) {
    nav.append(navItem({
      id: list.id,
      icon: ICON.list,
      label: list.title,
      count: list.incomplete,
      active: store.activeListId === list.id,
    }));
  }
  nav.append(navItem({
    icon: ICON.add,
    label: "新建任务列表",
    onClick: () => $("btnNewList").click(),
  }));
}

function renderAccount() {
  const label = $("accountLabel");
  if (!label) return;
  const { email, id } = store.account;
  label.textContent = email || (store.demo ? "演示数据" : id || "未登录");
  label.title = email && id ? `${email}（${id}）` : label.textContent;
}

function renderTasks() {
  $("listTitle").textContent = activeListTitle();
  renderComposerHint();
  const container = $("taskList");
  container.innerHTML = "";

  if (store.activeListId === VIRTUAL_ALL) {
    const sections = store.lists
      .map((list) => ({ list, tasks: tasksOf(list.id) }))
      .filter((section) => section.tasks.length > 0);
    if (!sections.length) {
      container.append(emptyState());
      return;
    }
    for (const section of sections) container.append(renderSection(section.list, section.tasks));
    return;
  }

  if (store.activeListId === VIRTUAL_STARRED) {
    const starred = orderedTasks(visibleTasks());
    if (!starred.length) {
      container.append(emptyState("没有已加星标的任务"));
      return;
    }
    for (const task of starred) container.append(renderTask(task));
    return;
  }

  const list = store.lists.find((item) => item.id === store.activeListId);
  const tasks = tasksOf(store.activeListId);
  if (!tasks.length) {
    container.append(emptyState());
    return;
  }
  // 单个清单视图里标题已经在页头，不再重复一遍。
  container.append(renderSection(list, tasks, { bare: true }));
}

// 在「所有任务 / 已加星标」下，底部输入框要把新任务落到哪个清单并不显然，
// 所以把目标写进 placeholder。
function renderComposerHint() {
  const input = $("inputNewTask");
  if (!input) return;
  if (!isVirtual(store.activeListId)) {
    input.placeholder = "添加任务";
    return;
  }
  const target = store.lists.find((list) => list.id === targetListId());
  input.placeholder = target ? `添加到「${target.title}」` : "添加任务";
}

function emptyState(text) {
  const empty = document.createElement("div");
  empty.className = "empty";
  empty.innerHTML = ICON.empty;
  const line = document.createElement("div");
  line.textContent = text || (store.demo ? "太好了，没有待办任务！" : "暂无任务");
  empty.append(line);
  return empty;
}

// 一个清单的一段：清单名（仅「所有任务」里显示）、未完成任务、「已完成 (N)」折叠
// 头，展开后是已完成任务。与网页端同一段结构。
function renderSection(list, tasks, { bare = false } = {}) {
  const section = document.createElement("section");
  section.className = "task-section";

  if (!bare && list) {
    const head = document.createElement("div");
    head.className = "section-head";
    const name = document.createElement("button");
    name.className = "section-name";
    name.textContent = list.title;
    name.title = `打开「${list.title}」`;
    name.addEventListener("click", () => selectList(list.id));
    head.append(name);
    section.append(head);
  }

  for (const task of orderedTasks(tasks.filter((item) => !item.completed))) {
    section.append(renderTask(task));
  }

  const completed = tasks.filter((item) => item.completed);
  const total = list ? Math.max(list.completedTotal || 0, completed.length) : completed.length;
  if (total > 0) section.append(completedToggle(list, total, completed.length));
  if (list && store.expandedCompleted.has(list.id)) {
    for (const task of orderedTasks(completed)) section.append(renderTask(task));
    // 快照每个清单只带回前 10 条已完成任务，网页端靠「加载更多」翻页。
    if (completed.length < total) section.append(partialHint(total - completed.length));
  }
  return section;
}

function completedToggle(list, total, loaded) {
  const row = document.createElement("button");
  row.className = "completed-toggle";
  const open = !!list && store.expandedCompleted.has(list.id);
  const icon = document.createElement("span");
  icon.className = "nav-icon";
  icon.innerHTML = open ? ICON.expandMore : ICON.chevron;
  const label = document.createElement("span");
  label.textContent = `已完成 (${total})`;
  row.append(icon, label);
  if (list) {
    row.addEventListener("click", () => {
      if (open) store.expandedCompleted.delete(list.id);
      else store.expandedCompleted.add(list.id);
      renderTasks();
    });
  }
  return row;
}

function partialHint(missing) {
  const row = document.createElement("div");
  row.className = "partial-hint";
  row.textContent = `还有 ${missing} 条已完成任务未载入（快照每个清单只返回前 10 条，需要 listTasks 的 rpcid 翻页）`;
  return row;
}

// 网页端的写法是「完成日期：11月17日周二」，跨年时才带年份。
function formatCompletedDate(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const weekday = "日一二三四五六"[date.getDay()];
  const year = date.getFullYear() === new Date().getFullYear() ? "" : `${date.getFullYear()}年`;
  return `${year}${date.getMonth() + 1}月${date.getDate()}日周${weekday}`;
}

function renderTask(task) {
  const row = document.createElement("div");
  row.className = `task depth-${task.depth || 0}` + (task.completed ? " done" : "") + (task.id === store.activeTaskId ? " selected" : "");
  row.dataset.id = task.id;
  row.addEventListener("click", () => openDetail(task.id));

  const box = document.createElement("button");
  box.className = "checkbox";
  box.title = task.completed ? "标记为未完成" : "标记为已完成";
  box.setAttribute("aria-label", box.title);
  box.innerHTML = ICON.check;
  box.addEventListener("click", (event) => {
    event.stopPropagation();
    toggleComplete(task);
  });

  const main = document.createElement("div");
  main.className = "task-main";
  const title = document.createElement("div");
  title.className = "task-title";
  title.textContent = task.title;
  main.append(title);

  // 网页端在标题下面直接显示说明的前几行。
  if (!task.completed && task.notes) {
    const notes = document.createElement("div");
    notes.className = "task-notes";
    notes.textContent = task.notes;
    main.append(notes);
  }

  const chips = [];
  if (task.due && task.due.date) chips.push(dueChip(task));
  if (task.recurrence) chips.push({ icon: ICON.repeat, text: task.recurrence });
  if (task.starred) chips.push({ icon: ICON.starOn, text: "已加星标", cls: "starred" });
  if (chips.length) {
    const meta = document.createElement("div");
    meta.className = "task-meta";
    for (const chip of chips) {
      const node = document.createElement("span");
      node.className = "chip" + (chip.cls ? ` ${chip.cls}` : "");
      node.innerHTML = chip.icon;
      node.append(document.createTextNode(chip.text));
      meta.append(node);
    }
    main.append(meta);
  }

  if (task.completed && task.completedAt) {
    const line = document.createElement("div");
    line.className = "task-completed-date";
    line.textContent = `完成日期：${formatCompletedDate(task.completedAt)}`;
    main.append(line);
  }

  const actions = document.createElement("div");
  actions.className = "task-actions";
  if (task.completed) {
    // 网页端在已完成行上只放删除，星标和选项都收进详情页。
    const remove = document.createElement("button");
    remove.className = "icon-btn";
    remove.title = "删除任务";
    remove.setAttribute("aria-label", "删除任务");
    remove.innerHTML = ICON.trash;
    remove.addEventListener("click", (event) => {
      event.stopPropagation();
      deleteTask(task.id);
    });
    actions.append(remove);
  } else {
    const more = document.createElement("button");
    more.className = "icon-btn";
    more.title = "任务选项";
    more.setAttribute("aria-label", "任务选项");
    more.innerHTML = ICON.more;
    more.addEventListener("click", (event) => {
      event.stopPropagation();
      openDetail(task.id);
    });
    const star = document.createElement("button");
    star.className = "icon-btn star-btn" + (task.starred ? " on always" : "");
    star.title = task.starred ? "从“已加星标”中移除" : "添加到“已加星标”";
    star.setAttribute("aria-label", star.title);
    star.innerHTML = task.starred ? ICON.starOn : ICON.star;
    star.addEventListener("click", (event) => {
      event.stopPropagation();
      toggleStar(task);
    });
    actions.append(more, star);
  }
  const open = document.createElement("button");
  open.className = "icon-btn";
  open.title = "详细信息";
  open.setAttribute("aria-label", "详细信息");
  open.innerHTML = ICON.chevron;
  open.addEventListener("click", (event) => {
    event.stopPropagation();
    openDetail(task.id);
  });
  actions.append(open);

  row.append(box, main, actions);
  return row;
}

function dueChip(task) {
  const overdue = isOverdue(task);
  const date = new Date(task.due.date + "T00:00:00");
  const label = `${date.getMonth() + 1}月${date.getDate()}日` + (task.due.time ? ` ${task.due.time}` : "");
  return { icon: ICON.calendar, text: label, cls: overdue && !task.completed ? "overdue" : "" };
}

function isOverdue(task) {
  if (!task.due || !task.due.date || task.completed) return false;
  const due = new Date(task.due.date + "T" + (task.due.time || "23:59") + ":00");
  return due.getTime() < Date.now();
}

/* --------------------------------------------------------------- detail */

function openDetail(taskId) {
  store.activeTaskId = taskId;
  renderTasks();
  renderDetail();
  $("detailPanel").hidden = false;
}

function closeDetail() {
  store.activeTaskId = null;
  $("detailPanel").hidden = true;
  renderTasks();
}

function renderDetail() {
  const body = $("detailBody");
  body.innerHTML = "";
  const task = store.tasks.find((t) => t.id === store.activeTaskId);
  if (!task) return;

  const titleInput = document.createElement("textarea");
  titleInput.id = "detailTitle";
  titleInput.rows = 1;
  titleInput.value = task.title;
  titleInput.addEventListener("change", () => updateTask(task, { title: titleInput.value }));
  body.append(titleInput);

  // 星标
  const starRow = document.createElement("div");
  starRow.className = "detail-row";
  const starBtn = document.createElement("button");
  starBtn.className = "icon-btn" + (task.starred ? " on" : "");
  starBtn.innerHTML = task.starred ? ICON.starOn : ICON.star;
  starBtn.addEventListener("click", () => toggleStar(task));
  const starLabel = document.createElement("div");
  starLabel.className = "detail-field";
  starLabel.textContent = task.starred ? "已加星标" : "加星标";
  starRow.append(starBtn, starLabel);
  body.append(starRow);

  // 日期 / 时间
  const dueRow = document.createElement("div");
  dueRow.className = "detail-row";
  const dueIcon = document.createElement("button");
  dueIcon.className = "icon-btn";
  dueIcon.innerHTML = ICON.calendar;
  const dueFields = document.createElement("div");
  dueFields.className = "detail-field";
  dueFields.innerHTML = "<label>日期 / 时间</label>";
  const dueWrap = document.createElement("div");
  dueWrap.className = "due-wrap";
  const dateInput = document.createElement("input");
  dateInput.type = "date";
  dateInput.value = (task.due && task.due.date) || "";
  const timeInput = document.createElement("input");
  timeInput.type = "time";
  timeInput.value = (task.due && task.due.time) || "";
  // 与标题、备注一致：改完即提交，省掉一个挤不下的保存按钮。
  const commitDue = () => updateTask(task, { due: { date: dateInput.value, time: timeInput.value } });
  dateInput.addEventListener("change", commitDue);
  timeInput.addEventListener("change", commitDue);
  dueWrap.append(dateInput, timeInput);
  dueFields.append(dueWrap);
  dueRow.append(dueIcon, dueFields);
  body.append(dueRow);
  if (!opReady("setDueDateTime")) {
    body.append(warningLine("「时分」官方 API 不支持，需要 setDueDateTime 的 rpcid。"));
  }

  // 重复
  const repeatRow = document.createElement("div");
  repeatRow.className = "detail-row";
  const repeatIcon = document.createElement("button");
  repeatIcon.className = "icon-btn";
  repeatIcon.innerHTML = ICON.repeat;
  const repeatField = document.createElement("div");
  repeatField.className = "detail-field";
  repeatField.innerHTML = "<label>重复</label>";
  const repeatSelect = document.createElement("select");
  for (const option of ["不重复", "每天", "每周", "每月", "每年"]) {
    const node = document.createElement("option");
    node.value = option;
    node.textContent = option;
    if ((task.recurrence || "不重复") === option) node.selected = true;
    repeatSelect.append(node);
  }
  repeatSelect.addEventListener("change", () => {
    if (!requireOp("setRecurrence")) return;
    updateTask(task, { recurrence: repeatSelect.value === "不重复" ? "" : repeatSelect.value });
  });
  repeatField.append(repeatSelect);
  repeatRow.append(repeatIcon, repeatField);
  body.append(repeatRow);

  // 说明
  const notesRow = document.createElement("div");
  notesRow.className = "detail-row";
  const notesField = document.createElement("div");
  notesField.className = "detail-field";
  notesField.innerHTML = "<label>说明</label>";
  const notesArea = document.createElement("textarea");
  notesArea.value = task.notes || "";
  notesArea.placeholder = "添加说明";
  notesArea.addEventListener("change", () => updateTask(task, { notes: notesArea.value }));
  notesField.append(notesArea);
  notesRow.append(notesField);
  body.append(notesRow);

  // 子任务
  const children = store.tasks.filter((t) => t.parent === task.id);
  if (children.length) {
    const subRow = document.createElement("div");
    subRow.className = "detail-row";
    const subField = document.createElement("div");
    subField.className = "detail-field";
    subField.innerHTML = "<label>子任务</label>";
    for (const child of children) {
      const line = document.createElement("div");
      line.textContent = "· " + child.title;
      line.style.color = "var(--text-2)";
      subField.append(line);
    }
    subRow.append(subField);
    body.append(subRow);
  }

  const addSub = document.createElement("button");
  addSub.className = "btn";
  addSub.textContent = "添加子任务";
  addSub.addEventListener("click", () => {
    if (!requireOp("addSubtask")) return;
    const title = window.prompt("子任务标题");
    if (title) createTask(title, task.id);
  });
  body.append(addSub);
}

function warningLine(text) {
  const node = document.createElement("div");
  node.className = "chip";
  node.style.margin = "8px 0";
  node.textContent = "⚠ " + text;
  return node;
}

function requireOp(op) {
  if (opReady(op)) return true;
  toast(`操作 ${op} 还没发现 rpcid，请到工作台抓包后固化`, true);
  return false;
}

/* -------------------------------------------------------------- actions */

function findTask(id) {
  return store.tasks.find((t) => t.id === id);
}

async function toggleStar(task) {
  const next = !task.starred;
  if (store.demo) {
    task.starred = next;
    renderAll();
    return;
  }
  if (!requireOp("setStarred")) return;
  try {
    await execute("setStarred", { TaskID: task.id, Starred: next, TaskListID: task.listId });
    task.starred = next;
    renderAll();
  } catch (error) {
    toast(error.message, true);
  }
}

async function toggleComplete(task) {
  const next = !task.completed;
  if (store.demo) {
    task.completed = next;
    renderAll();
    return;
  }
  const op = next ? "completeTask" : "uncompleteTask";
  if (!requireOp(op)) return;
  try {
    await execute(op, { TaskID: task.id, TaskListID: task.listId, Completed: next });
    task.completed = next;
    renderAll();
  } catch (error) {
    toast(error.message, true);
  }
}

async function updateTask(task, patch) {
  if (store.demo) {
    Object.assign(task, patch);
    renderAll();
    return;
  }
  const opFor = {
    title: "updateTaskTitle",
    notes: "updateTaskNotes",
    due: "setDueDateTime",
    recurrence: "setRecurrence",
  };
  const key = Object.keys(patch)[0];
  const op = opFor[key];
  if (!op || !requireOp(op)) return;
  try {
    await execute(op, { TaskID: task.id, TaskListID: task.listId, ...patch });
    Object.assign(task, patch);
    renderAll();
  } catch (error) {
    toast(error.message, true);
  }
}

async function createTask(title, parentId) {
  if (store.demo) {
    store.tasks.push({
      id: "demo-" + Date.now(),
      listId: targetListId(),
      title,
      notes: "",
      starred: false,
      completed: false,
      parent: parentId || null,
    });
    renderAll();
    return;
  }
  const listId = targetListId();
  if (!listId) {
    toast("还没有清单，先新建一个任务列表", true);
    return;
  }
  if (!requireOp("createTask")) return;
  try {
    await execute("createTask", { Title: title, TaskListID: listId, ParentID: parentId || "" });
    await loadData();
  } catch (error) {
    toast(error.message, true);
  }
}

// 详情页的删除按钮和已完成行上的删除图标共用这条路径。
async function deleteTask(taskId) {
  const task = findTask(taskId || store.activeTaskId);
  if (!task) return;
  if (store.demo) {
    store.tasks = store.tasks.filter((t) => t.id !== task.id && t.parent !== task.id);
    if (store.activeTaskId === task.id) closeDetail();
    renderAll();
    return;
  }
  if (!requireOp("deleteTask")) return;
  try {
    await execute("deleteTask", { TaskID: task.id, TaskListID: task.listId });
    store.tasks = store.tasks.filter((t) => t.id !== task.id);
    if (store.activeTaskId === task.id) closeDetail();
    renderAll();
  } catch (error) {
    toast(error.message, true);
  }
}

function selectList(id) {
  store.activeListId = id;
  closeDetail();
  renderAll();
}

function renderAll() {
  renderSidebar();
  renderAccount();
  renderTasks();
  if (store.activeTaskId) renderDetail();
  else $("detailPanel").hidden = true;
}

/* ----------------------------------------------------------------- bind */

function bindUI() {
  $("btnDemo").addEventListener("click", () => toggleDemo());
  $("btnTheme").addEventListener("click", (event) => {
    openThemeMenu(event.currentTarget).catch((error) =>
      toast(`主题菜单打开失败：${error.message}`, true),
    );
  });
  // 菜单里选完由 Go 落盘并广播回来，这里只需跟着刷新图标。
  document.addEventListener("gtasks-theme", () => renderThemeButton());
  $("btnCloseDetail").addEventListener("click", closeDetail);
  $("btnDeleteTask").addEventListener("click", () => deleteTask());
  $("btnRefresh").addEventListener("click", () => {
    if (store.demo) {
      toast("演示模式下不联网，先关掉演示再刷新", true);
      return;
    }
    loadData();
  });

  const input = $("inputNewTask");
  const submit = () => {
    const title = input.value.trim();
    if (!title) return;
    input.value = "";
    createTask(title);
  };
  input.addEventListener("keydown", (event) => {
    if (event.key === "Enter") submit();
  });
  $("btnAddTask").addEventListener("click", submit);
  $("btnNewTaskDate").addEventListener("click", () => {
    toast("先在演示/真实数据里创建任务，再用详情面板设置日期与时分");
  });

  $("btnNewList").addEventListener("click", async () => {
    const title = window.prompt("新清单名称");
    if (!title) return;
    if (store.demo) {
      store.lists.push({ id: "demo-list-" + Date.now(), title });
      renderAll();
      return;
    }
    if (!requireOp("createTaskList")) return;
    try {
      await execute("createTaskList", { Title: title });
      await loadData();
    } catch (error) {
      toast(error.message, true);
    }
  });

  $("btnListMenu").addEventListener("click", () => {
    const ready = [...store.ops.entries()].filter(([, v]) => v).map(([k]) => k);
    toast(ready.length ? `已固化操作: ${ready.join(", ")}` : "还没有固化任何 rpcid", ready.length === 0);
  });
}

async function init() {
  initTheme();
  await loadServerTheme();
  renderThemeButton();
  bindUI();
  await refreshOps();
  await loadData();
  setInterval(refreshOps, 15000);
  setInterval(refreshSession, 8000);
}

init();
