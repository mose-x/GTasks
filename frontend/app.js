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
  myTasks: '<svg viewBox="0 0 24 24"><path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-2 15l-5-5 1.41-1.41L10 14.17l7.59-7.59L19 8l-9 9z"/></svg>',
  calendar: '<svg viewBox="0 0 24 24"><path d="M19 3h-1V1h-2v2H8V1H6v2H5c-1.11 0-1.99.9-1.99 2L3 19c0 1.1.89 2 2 2h14c1.1 0 2-.9 2-2V5c0-1.1-.9-2-2-2zm0 16H5V8h14v11zM7 10h5v5H7z"/></svg>',
  repeat: '<svg viewBox="0 0 24 24"><path d="M7 7h10v3l4-4-4-4v3H5v6h2V7zm10 10H7v-3l-4 4 4 4v-3h12v-6h-2v4z"/></svg>',
  subtask: '<svg viewBox="0 0 24 24"><path d="M4 6h16v2H4zm2 5h14v2H6zm3 5h11v2H9z"/></svg>',
  chevron: '<svg viewBox="0 0 24 24"><path d="M10 6L8.59 7.41 13.17 12l-4.58 4.59L10 18l6-6z"/></svg>',
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
  button.title = `主题：${THEME_LABEL[mode]}（点击切换）`;
  button.classList.toggle("on", mode === "dark");
}

const VIRTUAL_STARRED = "@starred";
const VIRTUAL_MY = "@my";
const VIRTUAL_COMPLETED = "@completed";

const store = {
  demo: false,
  expired: false,
  lists: [],
  tasks: [],
  activeListId: VIRTUAL_MY,
  activeTaskId: null,
  ops: new Map(),
};

/* ------------------------------------------------------------------ ops */

async function refreshOps() {
  try {
    const payload = await api("/api/ops");
    store.ops = new Map((payload.operation || []).map((d) => [d.op, !!(d.steps && d.steps.length)]));
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

async function loadData() {
  if (store.demo) {
    seedDemo();
    renderAll();
    return;
  }

  if (!opReady("listTaskLists") || !opReady("listTasks")) {
    store.lists = [];
    store.tasks = [];
    renderAll();
    showBanner(
      "尚未发现 rpcid，无法读取真实数据。先在会话窗口登录 Google，然后到工作台抓包并把 rpcid 固化到操作映射里。",
      [
        { label: "载入演示数据", onClick: () => toggleDemo(true) },
      ]
    );
    return;
  }

  try {
    if (store.expired) expiredBanner();
    else hideBanner();
    // rpcid 已知，但响应结构仍需按抓包结果实现解析。
    await execute("listTaskLists", {});
    toast("listTaskLists 已调用，响应解析尚未实现（见工作台抓包结果）");
    store.lists = [];
    store.tasks = [];
    renderAll();
  } catch (error) {
    store.lists = [];
    store.tasks = [];
    renderAll();
    showBanner(`读取失败：${error.message}`, [{ label: "载入演示数据", onClick: () => toggleDemo(true) }]);
  }
}

function seedDemo() {
  store.lists = [
    { id: "list-work", title: "工作" },
    { id: "list-personal", title: "个人" },
    { id: "list-shopping", title: "采购清单" },
  ];
  const today = new Date();
  const iso = (offsetDays) => {
    const d = new Date(today);
    d.setDate(d.getDate() + offsetDays);
    return d.toISOString().slice(0, 10);
  };
  store.tasks = [
    { id: "t1", listId: VIRTUAL_MY, title: "回复设计评审邮件", notes: "附上最新一版的交互稿链接", starred: true, completed: false, due: { date: iso(0), time: "18:00" } },
    { id: "t2", listId: VIRTUAL_MY, title: "整理逆向抓包结果", notes: "", starred: false, completed: false, due: { date: iso(1), time: "" }, recurrence: "每周" },
    { id: "t3", listId: VIRTUAL_MY, title: "把 rpcid 固化进操作映射", notes: "", starred: false, completed: false, parent: "t2" },
    { id: "t4", listId: VIRTUAL_MY, title: "校对像素级还原度", notes: "和 tasks.google.com 并排比对", starred: false, completed: true, due: { date: iso(-2), time: "" } },
    { id: "t5", listId: "list-work", title: "准备季度规划材料", notes: "", starred: true, completed: false, due: { date: iso(3), time: "10:30" } },
    { id: "t6", listId: "list-work", title: "评审 batchexecute 解码结果", notes: "", starred: false, completed: false },
    { id: "t7", listId: "list-personal", title: "预约牙医", notes: "", starred: false, completed: false, due: { date: iso(-1), time: "09:00" } },
    { id: "t8", listId: "list-shopping", title: "咖啡豆", notes: "", starred: false, completed: false },
    { id: "t9", listId: "list-shopping", title: "滤纸", notes: "", starred: false, completed: true },
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

function visibleTasks() {
  const listId = store.activeListId;
  if (listId === VIRTUAL_STARRED) {
    return store.tasks.filter((t) => t.starred && !t.completed);
  }
  if (listId === VIRTUAL_COMPLETED) {
    return store.tasks.filter((t) => t.completed);
  }
  const scope = listId === VIRTUAL_MY ? [VIRTUAL_MY] : [listId];
  return store.tasks.filter((t) => scope.includes(t.listId) && !t.completed);
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
    case VIRTUAL_COMPLETED: return "已完成的任务";
    case VIRTUAL_MY: return "我的任务";
    default: return (store.lists.find((l) => l.id === store.activeListId) || {}).title || "我的任务";
  }
}

/* --------------------------------------------------------------- render */

function navItem({ id, icon, label, count, active }) {
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
  button.addEventListener("click", () => selectList(id));
  return button;
}

function renderSidebar() {
  const nav = $("listNav");
  nav.innerHTML = "";
  nav.append(navItem({
    id: VIRTUAL_STARRED,
    icon: ICON.star,
    label: "已加星标",
    active: store.activeListId === VIRTUAL_STARRED,
  }));
  nav.append(navItem({
    id: VIRTUAL_MY,
    icon: ICON.myTasks,
    label: "我的任务",
    active: store.activeListId === VIRTUAL_MY,
  }));

  if (store.lists.length) {
    const sep = document.createElement("div");
    sep.className = "nav-sep";
    nav.append(sep);
    for (const list of store.lists) {
      nav.append(navItem({
        id: list.id,
        icon: ICON.list,
        label: list.title,
        active: store.activeListId === list.id,
      }));
    }
  }

  const completedActive = store.activeListId === VIRTUAL_COMPLETED;
  $("navCompleted").classList.toggle("active", completedActive);
}

function renderTasks() {
  $("listTitle").textContent = activeListTitle();
  const container = $("taskList");
  container.innerHTML = "";

  const tasks = orderedTasks(visibleTasks());
  if (tasks.length === 0) {
    const empty = document.createElement("div");
    empty.className = "empty";
    empty.innerHTML = ICON.empty;
    const line = document.createElement("div");
    line.textContent = store.demo ? "太好了，没有待办任务！" : "暂无任务";
    empty.append(line);
    container.append(empty);
    return;
  }

  for (const task of tasks) container.append(renderTask(task));
}

function renderTask(task) {
  const row = document.createElement("div");
  row.className = `task depth-${task.depth || 0}` + (task.completed ? " done" : "") + (task.id === store.activeTaskId ? " selected" : "");
  row.addEventListener("click", () => openDetail(task.id));

  const box = document.createElement("button");
  box.className = "checkbox";
  box.title = task.completed ? "标记为未完成" : "标记为完成";
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

  const actions = document.createElement("div");
  actions.className = "task-actions";
  const star = document.createElement("button");
  star.className = "icon-btn star-btn" + (task.starred ? " on always" : "");
  star.title = task.starred ? "取消星标" : "加星标";
  star.innerHTML = task.starred ? ICON.starOn : ICON.star;
  star.addEventListener("click", (event) => {
    event.stopPropagation();
    toggleStar(task);
  });
  const open = document.createElement("button");
  open.className = "icon-btn";
  open.title = "打开详情";
  open.innerHTML = ICON.chevron;
  open.addEventListener("click", (event) => {
    event.stopPropagation();
    openDetail(task.id);
  });
  actions.append(star, open);

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
      listId: store.activeListId === VIRTUAL_STARRED || store.activeListId === VIRTUAL_COMPLETED ? VIRTUAL_MY : store.activeListId,
      title,
      notes: "",
      starred: false,
      completed: false,
      parent: parentId || null,
    });
    renderAll();
    return;
  }
  if (!requireOp("createTask")) return;
  try {
    await execute("createTask", { Title: title, TaskListID: store.activeListId, ParentID: parentId || "" });
    await loadData();
  } catch (error) {
    toast(error.message, true);
  }
}

async function deleteTask() {
  const task = findTask(store.activeTaskId);
  if (!task) return;
  if (store.demo) {
    store.tasks = store.tasks.filter((t) => t.id !== task.id && t.parent !== task.id);
    closeDetail();
    renderAll();
    return;
  }
  if (!requireOp("deleteTask")) return;
  try {
    await execute("deleteTask", { TaskID: task.id, TaskListID: task.listId });
    store.tasks = store.tasks.filter((t) => t.id !== task.id);
    closeDetail();
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
  renderTasks();
  if (store.activeTaskId) renderDetail();
  else $("detailPanel").hidden = true;
}

/* ----------------------------------------------------------------- bind */

function bindUI() {
  $("btnDemo").addEventListener("click", () => toggleDemo());
  $("btnTheme").addEventListener("click", () => {
    const { mode } = cycleTheme();
    renderThemeButton();
    toast(`主题：${THEME_LABEL[mode]}`);
  });
  $("btnCloseDetail").addEventListener("click", closeDetail);
  $("btnDeleteTask").addEventListener("click", deleteTask);
  $("navCompleted").addEventListener("click", () => selectList(VIRTUAL_COMPLETED));

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
  renderThemeButton();
  bindUI();
  await refreshOps();
  await loadData();
  setInterval(refreshOps, 15000);
  setInterval(refreshSession, 8000);
}

init();
