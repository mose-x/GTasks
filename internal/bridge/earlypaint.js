// GTasks document-start 早遮罩（macOS 由 internal/appkit 以 WKUserScript 注入）。
//
// Wails 在 macOS 上要等 finish-navigation 才执行 options.JS（bootstrap.js），
// 于是新页面从首帧到加载完成之间，Google 的默认顶栏（含「Google 应用」九宫格）
// 会原样露出来约一秒。本脚本跑在真正的 document-start：先盖一层与主题同色的
// 遮罩、藏掉九宫格，等 bootstrap 把顶栏按钮摆好（#gtasks-theme-btn 出现）再
// 淡出。配色 /  id / 遮罩结构都与 bootstrap.js 的 installSplash 保持一致，
// bootstrap 看见遮罩已在就跳过自建，两段无缝衔接。
(function () {
  "use strict";
  if (location.hostname !== "tasks.google.com") return;
  if (document.getElementById("gtasks-splash")) return;

  var SPLASH_ID = "gtasks-splash";
  var TIMEOUT = 8000;
  var POLL = 90;
  var READY_SELECTOR = ".yVc39c, li[data-task-list-id], c-wiz";
  var THEME_KEY = "gtasks-theme";
  var I18N_KEY = "gtasks-i18n";
  var LOGO =
    '<svg viewBox="44 38 104 104" width="26" height="26" aria-hidden="true"><path fill="none" stroke="#fff" stroke-linecap="round" stroke-linejoin="round" stroke-width="15" d="m61 90 22.172 22.172a4 4 0 0 0 5.656 0L138 63"/></svg>';

  function dark() {
    var mode = "system";
    try {
      mode = localStorage.getItem(THEME_KEY) || "system";
    } catch (ignored) {}
    if (mode === "light") return false;
    if (mode === "dark") return true;
    try {
      return !!(window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches);
    } catch (ignored) {
      return false;
    }
  }

  function splashText() {
    try {
      var cached = JSON.parse(localStorage.getItem(I18N_KEY) || "null");
      var messages = (cached && cached.messages) || {};
      if (typeof messages["session.splash"] === "string" && messages["session.splash"]) {
        return messages["session.splash"];
      }
      return cached && cached.lang === "zh" ? "正在载入 Google Tasks…" : "Loading Google Tasks…";
    } catch (ignored) {
      return "Loading Google Tasks…";
    }
  }

  var palette = dark()
    ? { bg: "#1b1b1b", fg: "#9aa0a6", accent: "#8ab4f8" }
    : { bg: "#f8fafd", fg: "#5f6368", accent: "#1a73e8" };

  // 九宫格隐藏规则与 bootstrap 的 installUIStyles 第一条保持一致：遮罩万一提前
  // 退场，默认顶栏也不该露出桌面客户端用不到的入口。
  var style = document.createElement("style");
  style.id = SPLASH_ID + "-style";
  style.textContent =
    "@keyframes gtasks-splash-spin { to { transform: rotate(360deg); } }\n" +
    'a.gb_C[aria-label*="Google 应用"], a.gb_C[aria-label*="Google apps"], a.gb_C[href*="/about/products"] { display: none !important; }';

  var splash = document.createElement("div");
  splash.id = SPLASH_ID;
  splash.setAttribute(
    "style",
    [
      "position:fixed",
      "left:0",
      "top:0",
      "right:0",
      "bottom:0",
      "z-index:2147483000",
      "display:flex",
      "flex-direction:column",
      "align-items:center",
      "justify-content:center",
      "background:" + palette.bg,
      "color:" + palette.fg,
      "pointer-events:none",
      "font:13px/1.4 -apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'PingFang SC','Microsoft YaHei',sans-serif",
      "opacity:1",
      "transition:opacity .24s ease",
    ].join(";"),
  );
  splash.innerHTML =
    '<div style="width:54px;height:54px;border-radius:16px;background:' +
    palette.accent +
    ';display:flex;align-items:center;justify-content:center;margin-bottom:22px">' +
    LOGO +
    "</div>" +
    '<div style="display:flex;align-items:center;gap:10px">' +
    '<span style="width:15px;height:15px;border-radius:50%;border:2px solid ' +
    palette.accent +
    ';border-top-color:transparent;animation:gtasks-splash-spin .8s linear infinite"></span>' +
    "<span>" +
    splashText() +
    "</span></div>";

  (document.head || document.documentElement).appendChild(style);
  (document.body || document.documentElement).appendChild(splash);

  function hide() {
    var node = document.getElementById(SPLASH_ID);
    if (!node || node.getAttribute("data-gtasks-hiding")) return;
    node.setAttribute("data-gtasks-hiding", "1");
    node.style.opacity = "0";
    setTimeout(function () {
      if (node.parentNode) node.parentNode.removeChild(node);
      // 样式要等 bootstrap 的 #gtasks-ui-style 落地才能摘：藏九宫格那条规则两边
      // 都有，早摘会在超时 / 用户打断的退场路径上把默认顶栏放出来。
      var owned = document.getElementById(SPLASH_ID + "-style");
      if (owned && owned.parentNode && document.getElementById("gtasks-ui-style")) {
        owned.parentNode.removeChild(owned);
      }
    }, 280);
  }

  var started = Date.now();
  var tick = function () {
    if (!document.getElementById(SPLASH_ID)) return;
    var settled = false;
    try {
      // 首屏信号 + 顶栏按钮都在，才说明 bootstrap 的样式 / 按钮已经落地，
      // 此刻退场不会把默认顶栏闪给用户。
      settled =
        document.readyState !== "loading" &&
        !!document.querySelector(READY_SELECTOR) &&
        !!document.getElementById("gtasks-theme-btn");
    } catch (ignored) {}
    if (settled || Date.now() - started > TIMEOUT) {
      hide();
      return;
    }
    setTimeout(tick, POLL);
  };
  setTimeout(tick, POLL);

  // 用户已经在点页面了，说明遮罩该走了（也兜住首屏信号识别不出来的情况）。
  var events = ["pointerdown", "mousedown", "keydown", "wheel", "touchstart"];
  for (var index = 0; index < events.length; index++) {
    try {
      window.addEventListener(events[index], hide, true);
    } catch (ignored) {}
  }
})();
