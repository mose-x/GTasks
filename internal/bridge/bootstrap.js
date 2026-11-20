// GTasks bridge bootstrap.
//
// Injected into the Google session webview through WebviewWindowOptions.JS.
// Wails re-runs that script after every finished navigation, so this file must
// stay idempotent: it rebuilds window.__gtask from scratch on each page load.
//
// Two channels are used:
//   page -> Go : window.webkit.messageHandlers.external (macOS/Linux) or
//                window.chrome.webview (Windows), consumed by RawMessageHandler.
//   Go   -> page: WebviewWindow.ExecJS, which Wails gates on an internal
//                "runtimeLoaded" flag. An external origin never loads the Wails
//                runtime bundle, so we report readiness ourselves below.
//
// On Windows the script is registered with AddScriptToExecuteOnDocumentCreated,
// so it runs in every document the webview ever loads — including the redirect
// stub it starts from. The host guard below keeps it inert off Google origins.
(function () {
  "use strict";

  var PROTOCOL = "gtask1";
  var RESPONSE_LIMIT = 200000;
  // 首屏数据可能很大（整个账号的任务），单独放宽上限并按 key 分次取回。
  var INIT_DATA_LIMIT = 8000000;

  function pageHost() {
    try {
      if (location.hostname) return location.hostname;
    } catch (ignored) {}
    try {
      var match = /^[a-z][a-z0-9+.-]*:\/\/([^\/?#]*)/i.exec(location.origin || location.href || "");
      if (match) return match[1].split("@").pop().split(":")[0];
    } catch (ignored) {}
    return "";
  }

  function isGoogleHost() {
    var host = pageHost().toLowerCase();
    return host === "google.com" || host.slice(-11) === ".google.com";
  }

  if (!isGoogleHost()) return;

  function makeSender() {
    try {
      if (window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.external) {
        var webkitHandler = window.webkit.messageHandlers.external;
        return function (text) { webkitHandler.postMessage(text); };
      }
    } catch (ignored) {}
    try {
      if (window.chrome && window.chrome.webview && typeof window.chrome.webview.postMessage === "function") {
        var edgeHandler = window.chrome.webview;
        return function (text) { edgeHandler.postMessage(text); };
      }
    } catch (ignored) {}
    return null;
  }

  var send = makeSender();

  function post(payload) {
    if (!send) return;
    var envelope = { p: PROTOCOL };
    for (var key in payload) {
      if (Object.prototype.hasOwnProperty.call(payload, key)) envelope[key] = payload[key];
    }
    var text;
    try {
      text = JSON.stringify(envelope);
    } catch (ignored) {
      return;
    }
    try {
      send(text);
    } catch (ignored) {}
  }

  function reply(id, ok, data, error) {
    post({ kind: "result", id: id, ok: !!ok, data: data || null, error: error || "" });
  }

  function truncate(text) {
    if (typeof text !== "string") return text;
    if (text.length <= RESPONSE_LIMIT) return text;
    return text.slice(0, RESPONSE_LIMIT) + "\n…[truncated " + (text.length - RESPONSE_LIMIT) + " chars]";
  }

  function wizData() {
    try {
      return window.WIZ_global_data || {};
    } catch (ignored) {
      return {};
    }
  }

  function probeInfo() {
    var wiz = wizData();
    var keys = [];
    for (var key in wiz) {
      if (Object.prototype.hasOwnProperty.call(wiz, key)) keys.push(key);
    }
    var scalars = {};
    keys.forEach(function (name) {
      var value = wiz[name];
      var kind = typeof value;
      if (value === null || kind === "string" || kind === "number" || kind === "boolean") {
        scalars[name] = value;
      }
    });
    var cookieNames = [];
    try {
      cookieNames = (document.cookie || "")
        .split(";")
        .map(function (pair) { return pair.trim().split("=")[0]; })
        .filter(Boolean);
    } catch (ignored) {}

    return {
      url: location.href,
      origin: location.origin,
      pathname: location.pathname,
      title: document.title,
      userAgent: navigator.userAgent,
      readyState: document.readyState,
      // SNlM0e is the `at` CSRF token, cfb2h is the `bl` build label.
      at: wiz.SNlM0e || "",
      bl: wiz.cfb2h || "",
      loggedIn: !!wiz.SNlM0e,
      wizKeys: keys,
      wizScalars: scalars,
      visibleCookieNames: cookieNames,
      fetchPatched: !!window.__gtaskFetchPatched,
      xhrPatched: !!window.__gtaskXHRPatched
    };
  }

  // ---------------------------------------------------------------- sniffer
  //
  // The recorder state lives on window rather than in this closure: Wails
  // re-runs the bootstrap after every navigation, and a second copy running in
  // the same document must neither reset the flag nor orphan the fetch/XHR
  // wrappers the first copy installed.

  // 抓包开关记在 localStorage：页面一刷新，注入的脚本就要抢在 Google 自己的
  // 请求之前把 recorder 打开，否则最关键的初始化 RPC 全部丢失。
  var SNIFF_KEY = "gtasks.sniff";

  function isSniffing() {
    return window.__gtaskSniffing === true;
  }

  function setSniffing(on) {
    window.__gtaskSniffing = !!on;
    try {
      if (on) localStorage.setItem(SNIFF_KEY, "1");
      else localStorage.removeItem(SNIFF_KEY);
    } catch (ignored) {}
  }

  function storedSniffing() {
    try {
      return localStorage.getItem(SNIFF_KEY) === "1";
    } catch (ignored) {
      return false;
    }
  }

  function sniffStatus() {
    return { sniffing: isSniffing(), sniffed: window.__gtaskSniffed || 0 };
  }

  function classify(url, body) {
    if (typeof url !== "string" || url.indexOf("batchexecute") === -1) return null;
    var entry = { url: url, via: "" };
    var rpcids = /[?&]rpcids=([^&]*)/.exec(url);
    if (rpcids) {
      try {
        entry.rpcids = decodeURIComponent(rpcids[1]).split(",").filter(Boolean);
      } catch (ignored) {}
    }
    var bl = /[?&]bl=([^&]*)/.exec(url);
    if (bl) { try { entry.bl = decodeURIComponent(bl[1]); } catch (ignored) {} }
    if (typeof body === "string" && body) {
      var freq = /(?:^|&)f\.req=([^&]*)/.exec(body);
      if (freq) {
        try {
          entry.freq = JSON.parse(decodeURIComponent(freq[1]));
        } catch (ignored) {
          entry.freqRaw = truncate(decodeURIComponent(freq[1]));
        }
      }
      var at = /(?:^|&)at=([^&]*)/.exec(body);
      if (at) { try { entry.at = decodeURIComponent(at[1]); } catch (ignored) {} }
    }
    return entry;
  }

  // Google 的 fetch 调用经常把表单放在 URLSearchParams 里，只认字符串会丢掉
  // f.req 参数，逆向时就看不到真实入参。
  function bodyText(init) {
    if (!init) return null;
    var body = init.body;
    if (typeof body === "string") return body;
    if (!body || typeof body.forEach !== "function") return null;
    try {
      var pairs = [];
      body.forEach(function (value, key) {
        if (typeof value !== "string") return;
        pairs.push(encodeURIComponent(key) + "=" + encodeURIComponent(value));
      });
      return pairs.length ? pairs.join("&") : null;
    } catch (ignored) {
      return null;
    }
  }

  function record(entry) {
    if (!isSniffing()) return;
    window.__gtaskSniffed = (window.__gtaskSniffed || 0) + 1;
    entry.t = Date.now();
    entry.href = location.href;
    post({ kind: "sniff", entry: entry });
  }

  // 页面经常传 Request 对象而不是 (url, init)，此时 init 里没有 body，只能
  // clone 出来异步读取，否则抓包看不到 f.req 入参。
  function requestBodyPromise(input, init) {
    var direct = bodyText(init);
    if (direct !== null) return Promise.resolve(direct);
    try {
      if (input && typeof input.clone === "function" && (!input.method || String(input.method).toUpperCase() !== "GET")) {
        return input.clone().text();
      }
    } catch (ignored) {}
    return Promise.resolve(null);
  }

  function mergeRequestBody(entry, url, body) {
    if (!entry || typeof body !== "string" || !body) return;
    if (entry.freq || entry.freqRaw || entry.at) return;
    var parsed = classify(url, body);
    if (!parsed) return;
    if (parsed.freq) entry.freq = parsed.freq;
    if (parsed.freqRaw) entry.freqRaw = parsed.freqRaw;
    if (parsed.at) entry.at = parsed.at;
  }

  function installFetchSniffer() {
    if (window.__gtaskFetchPatched || typeof window.fetch !== "function") return;
    var original = window.fetch;
    var patched = function (input, init) {
      var url = typeof input === "string" ? input : (input && input.url) || "";
      var active = isSniffing() && !(init && init.__gtaskNoSniff);
      var entry = active ? classify(url, bodyText(init)) : null;
      if (entry) entry.via = "fetch";
      var pendingBody = active && entry ? requestBodyPromise(input, init) : null;
      var promise = original.apply(this, arguments);
      if (entry) {
        promise.then(function (response) {
          var texts = [Promise.resolve(null), Promise.resolve(null)];
          try {
            texts[0] = response.clone().text();
          } catch (ignored) {}
          if (pendingBody) texts[1] = pendingBody;
          Promise.all(texts).then(function (settled) {
            entry.status = response.status;
            if (typeof settled[0] === "string") entry.response = truncate(settled[0]);
            mergeRequestBody(entry, url, settled[1]);
            record(entry);
          }, function () {
            entry.status = response.status;
            record(entry);
          });
        }, function () {});
      }
      return promise;
    };
    window.fetch = patched;
    window.__gtaskFetchPatched = true;
  }

  function installXHRSniffer() {
    if (window.__gtaskXHRPatched || typeof window.XMLHttpRequest !== "function") return;
    var prototype = window.XMLHttpRequest.prototype;
    var originalOpen = prototype.open;
    var originalSend = prototype.send;
    prototype.open = function (method, url) {
      this.__gtaskURL = url;
      return originalOpen.apply(this, arguments);
    };
    prototype.send = function (body) {
      var request = this;
      var entry = isSniffing() ? classify(request.__gtaskURL, typeof body === "string" ? body : null) : null;
      if (entry) {
        entry.via = "xhr";
        request.addEventListener("loadend", function () {
          entry.status = request.status;
          try {
            entry.response = truncate(request.responseText || "");
          } catch (ignored) {}
          record(entry);
        });
      }
      return originalSend.apply(this, arguments);
    };
    window.__gtaskXHRPatched = true;
  }

  // ---------------------------------------------------------- initial data
  //
  // Tasks 首屏并不发 batchexecute：清单和任务由 HTML 内联脚本通过
  // AF_initDataCallback 注入。bootstrap 在文档开始时执行，因此可以抢在页面脚本
  // 之前接管这个回调，把与网页完全一致的初始数据留在 window 上，读路径就不必
  // 依赖尚未发现的 rpcid。
  //
  // 接管必须是无损的：页面自己赋值的实现要照常调用；若数据回调先于实现到达，
  // 就先排队，等实现出现再按原顺序补放。

  function resolveInitPayload(payload) {
    if (!payload || typeof payload !== "object") return null;
    var data = payload.data;
    if (typeof data === "function") {
      try {
        data = data();
      } catch (ignored) {
        return null;
      }
    }
    return { key: payload.key || "", hash: payload.hash || "", data: data };
  }

  function installInitDataRecorder() {
    if (window.__gtaskInitPatched) return;
    var store = {};
    var order = [];
    var implementation = null;
    var queued = [];

    var record = function (payload) {
      try {
        var resolved = resolveInitPayload(payload);
        if (!resolved) return;
        var key = resolved.key || resolved.hash;
        if (!key) return;
        if (!Object.prototype.hasOwnProperty.call(store, key)) order.push(key);
        store[key] = resolved;
      } catch (ignored) {}
    };

    var wrapped = function (payload) {
      record(payload);
      if (typeof implementation === "function") {
        try {
          return implementation.apply(this, arguments);
        } catch (ignored) {
          return undefined;
        }
      }
      queued.push(arguments);
      return undefined;
    };

    try {
      Object.defineProperty(window, "AF_initDataCallback", {
        configurable: true,
        enumerable: true,
        get: function () {
          return wrapped;
        },
        set: function (value) {
          implementation = value;
          if (typeof value !== "function") return;
          var pending = queued;
          queued = [];
          for (var index = 0; index < pending.length; index++) {
            try {
              value.apply(window, pending[index]);
            } catch (ignored) {}
          }
        }
      });
    } catch (ignored) {
      window.AF_initDataCallback = wrapped;
    }

    window.__gtaskInitData = {
      keys: function () {
        return order.map(function (key) {
          var entry = store[key];
          var size = 0;
          try {
            size = JSON.stringify(entry.data).length;
          } catch (ignored) {}
          return { key: entry.key, hash: entry.hash, size: size };
        });
      },
      get: function (key) {
        return Object.prototype.hasOwnProperty.call(store, key) ? store[key] : null;
      }
    };
    window.__gtaskInitPatched = true;
  }

  // ------------------------------------------------------------ batchexecute

  var requestCounter = Math.floor(Math.random() * 900000) + 100000;

  // health() 用一个必然不存在的 rpcid 发最小请求：会话活着时 Google 回 200 +
  // 一条错误帧；cookie 失效时回 401，或被 302 到 accounts.google.com 的登录页。
  // Im6cmf 是 Google 自己公布的 batchexecute 挂载点（当前为 /_/TasksWebEmbedUi）。
  // Tasks 早就从 /_/TasksUi 搬走了，写死路径会一路 404。
  var FALLBACK_MOUNT = "/_/TasksWebEmbedUi";
  // 探针 rpcid：SOV0tb 只做服务器时间同步，无副作用，会话正常时回 200。
  var HEALTH_RPC = "SOV0tb";
  var HEALTH_ARGS = "[]";

  function authState(response) {
    if (response.status === 401 || response.status === 403) return "unauthorized";
    if (response.redirected) return "redirected";
    try {
      if (response.url && location.origin && response.url.indexOf(location.origin) !== 0) return "redirected";
    } catch (ignored) {}
    if (!response.ok) return "http" + response.status;
    return "ok";
  }

  function batchPath(opts, wiz) {
    if (opts && opts.path) return opts.path;
    var mount = wiz && typeof wiz.Im6cmf === "string" && wiz.Im6cmf ? wiz.Im6cmf : FALLBACK_MOUNT;
    return mount.replace(/\/+$/, "") + "/data/batchexecute";
  }

  // 组装查询串，顺序与页面自身发出的请求保持一致，便于抓包对比。
  function batchQuery(rpcids, opts, bl) {
    var query = [
      "rpcids=" + encodeURIComponent(rpcids.join(",")),
      "source-path=" + encodeURIComponent(opts.sourcePath || location.pathname || "/")
    ];
    if (bl) query.push("bl=" + encodeURIComponent(bl));
    query.push("_reqid=" + requestCounter++);
    query.push("rt=" + encodeURIComponent(opts.rt || "c"));
    return query.join("&");
  }

  function runBatch(id, rpcids, freqs, opts) {
    opts = opts || {};
    if (!Array.isArray(rpcids) || rpcids.length === 0) {
      reply(id, false, null, "rpcids 不能为空");
      return;
    }
    var wiz = wizData();
    var at = opts.at || wiz.SNlM0e || "";
    var bl = opts.bl || wiz.cfb2h || "";
    if (!at && !opts.allowAnonymous) {
      reply(id, false, null, "拿不到 SNlM0e（at）：页面未登录或尚未初始化完成");
      return;
    }

    var calls = [];
    for (var index = 0; index < rpcids.length; index++) {
      var args = Array.isArray(freqs) ? freqs[index] : null;
      var encoded;
      try {
        encoded = JSON.stringify(args === undefined || args === null ? [] : args);
      } catch (error) {
        reply(id, false, null, "第 " + index + " 个参数无法序列化: " + String(error));
        return;
      }
      calls.push([rpcids[index], encoded, null, String(index + 1)]);
    }

    var body = "f.req=" + encodeURIComponent(JSON.stringify([calls])) + "&at=" + encodeURIComponent(at) + "&";
    var path = batchPath(opts, wiz);
    var url = path + "?" + batchQuery(rpcids, opts, bl);

    var headers = { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" };
    if (opts.sameDomainHeader !== false) headers["X-Same-Domain"] = "1";

    var startedAt = Date.now();
    fetch(url, {
      method: "POST",
      credentials: "include",
      headers: headers,
      body: body
    }).then(function (response) {
      return response.text().then(function (text) {
        // HTTP 4xx/5xx 也是有效结果：状态码和 auth 判定要交给 Go 去决策，
        // 这里一旦按失败回复，载荷就被丢掉，只能看到一句 "HTTP 404"。
        reply(id, true, {
          status: response.status,
          auth: authState(response),
          url: url,
          path: path,
          at: at,
          bl: bl,
          rpcids: rpcids,
          elapsedMs: Date.now() - startedAt,
          raw: truncate(text)
        }, "");
      });
    }, function (error) {
      reply(id, false, null, String(error));
    });
  }

  // --------------------------------------------------------------- dispatch

  var api = {
    installed: true,
    protocol: PROTOCOL,

    announce: function () {
      post({ kind: "ready", info: probeInfo() });
    },
    probe: function (id) {
      reply(id, true, probeInfo(), "");
    },
    batch: function (id, args) {
      args = args || {};
      runBatch(id, args.rpcids || [], args.freqs || [], args.opts || {});
    },
    sniffStart: function (id) {
      setSniffing(true);
      installFetchSniffer();
      installXHRSniffer();
      reply(id, true, sniffStatus(), "");
    },
    sniffStop: function (id) {
      setSniffing(false);
      reply(id, true, sniffStatus(), "");
    },
    sniffStatus: function (id) {
      reply(id, true, sniffStatus(), "");
    },
    initDataKeys: function (id) {
      var accessor = window.__gtaskInitData;
      reply(id, true, { keys: accessor ? accessor.keys() : [] }, "");
    },
    initData: function (id, args) {
      var accessor = window.__gtaskInitData;
      var key = args && args.key;
      if (!accessor || !key) {
        reply(id, true, { key: key || "", found: false, data: null }, "");
        return;
      }
      var entry = accessor.get(key);
      if (!entry) {
        reply(id, true, { key: key, found: false, data: null }, "");
        return;
      }
      var text;
      try {
        text = JSON.stringify(entry.data);
      } catch (error) {
        reply(id, false, null, "初始化数据无法序列化: " + String(error));
        return;
      }
      var truncated = text.length > INIT_DATA_LIMIT;
      reply(id, true, {
        key: entry.key,
        hash: entry.hash,
        found: true,
        truncated: truncated,
        size: text.length,
        data: truncated ? text.slice(0, INIT_DATA_LIMIT) : text
      }, "");
    },
    // 逆向用的通用逃生口：在页面上下文里执行一段脚本并取回 JSON 结果。只由
    // 本机回环端口（带自定义头，跨站请求会被预检拦掉）触发。
    evalScript: function (id, args) {
      var source = (args && args.js) || "";
      if (!source) {
        reply(id, false, null, "eval 需要 js 参数");
        return;
      }
      var finish = function (value) {
        if (value && typeof value.then === "function") {
          value.then(finish, function (error) {
            reply(id, false, null, String(error));
          });
          return;
        }
        var text;
        try {
          text = JSON.stringify(value === undefined ? null : value);
        } catch (error) {
          reply(id, false, null, "结果无法序列化: " + String(error));
          return;
        }
        if (text === undefined) text = "null";
        var oversized = text.length > INIT_DATA_LIMIT;
        reply(id, true, {
          truncated: oversized,
          size: text.length,
          result: oversized ? text.slice(0, INIT_DATA_LIMIT) : text
        }, "");
      };
      var value;
      try {
        value = new Function('"use strict";return (' + source + ');')();
      } catch (error) {
        try {
          value = new Function('"use strict";' + source)();
        } catch (statementError) {
          reply(id, false, null, String(statementError));
          return;
        }
      }
      try {
        finish(value);
      } catch (error) {
        reply(id, false, null, String(error));
      }
    },
    health: function (id) {
      var wiz = wizData();
      var at = wiz.SNlM0e || "";
      if (!at) {
        reply(id, true, { state: "anonymous", status: 0, at: "" }, "");
        return;
      }
      var bl = wiz.cfb2h || "";
      var url = batchPath({}, wiz) + "?" + batchQuery([HEALTH_RPC], {}, bl);
      fetch(url, {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8", "X-Same-Domain": "1" },
        body: "f.req=" + encodeURIComponent(JSON.stringify([[[HEALTH_RPC, HEALTH_ARGS, null, "1"]]])) + "&at=" + encodeURIComponent(at) + "&",
        __gtaskNoSniff: true
      }).then(function (response) {
        reply(id, true, { state: authState(response), status: response.status, at: at }, "");
      }, function (error) {
        reply(id, false, null, String(error));
      });
    },
    dispatch: function (raw) {
      var message;
      try {
        message = typeof raw === "string" ? JSON.parse(raw) : raw;
      } catch (ignored) {
        post({ kind: "error", error: "无法解析 dispatch 载荷" });
        return;
      }
      if (!message || typeof message !== "object") return;
      var handler = api[message.method];
      if (typeof handler !== "function") {
        reply(message.id, false, null, "未知方法: " + message.method);
        return;
      }
      try {
        handler(message.id, message.args);
      } catch (error) {
        reply(message.id, false, null, String(error));
      }
    }
  };

  window.__gtask = api;
  // ------------------------------------------------------------------ UI 层
  //
  // 会话窗口就是用户每天真正在用的 tasks.google.com 界面，这里做几处桌面化
  // 改造。全部走「注入 DOM + 回调 Go」的路线，不替换 Google 自己的逻辑：
  //   1. 顶栏去掉「Google 应用」九宫格（纯 CSS，见 installUIStyles）；
  //   2. 顶栏「支持」按钮左边加主题按钮，明亮 / 暗黑 / 跟随系统三档循环；
  //   3. 「支持」菜单末尾追加「GTasks 工作台」一项；
  //   4. 侧栏每个任务列表行加「列表选项」入口，点的还是 Google 自己的菜单；
  //   5. 右键统一交给 Go 弹原生菜单（WebKit 默认菜单带前进/后退，用不上）。
  //
  // 测试沙箱里的 document 只有几个字段，没有 DOM API，整段自动跳过。

  var UI_MODES = ["light", "dark", "system"];
  var UI_THEME_TITLE = { light: "主题：明亮", dark: "主题：暗黑", system: "主题：跟随系统" };
  var UI_THEME_ICON = {
    light: '<svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><path fill="currentColor" d="M12 7c-2.76 0-5 2.24-5 5s2.24 5 5 5 5-2.24 5-5-2.24-5-5-5zM2 13h2c.55 0 1-.45 1-1s-.45-1-1-1H2c-.55 0-1 .45-1 1s.45 1 1 1zm18 0h2c.55 0 1-.45 1-1s-.45-1-1-1h-2c-.55 0-1 .45-1 1s.45 1 1 1zM11 2v2c0 .55.45 1 1 1s1-.45 1-1V2c0-.55-.45-1-1-1s-1 .45-1 1zm0 18v2c0 .55.45 1 1 1s1-.45 1-1v-2c0-.55-.45-1-1-1s-1 .45-1 1zM5.99 4.58c-.39-.39-1.03-.39-1.41 0-.39.39-.39 1.03 0 1.41l1.06 1.06c.39.39 1.03.39 1.41 0s.39-1.03 0-1.41L5.99 4.58zm12.37 12.37c-.39-.39-1.03-.39-1.41 0-.39.39-.39 1.03 0 1.41l1.06 1.06c.39.39 1.03.39 1.41 0 .39-.39.39-1.03 0-1.41l-1.06-1.06zm1.06-10.96c.39-.39.39-1.03 0-1.41-.39-.39-1.03-.39-1.41 0l-1.06 1.06c-.39.39-.39 1.03 0 1.41s1.03.39 1.41 0l1.06-1.06zM7.05 18.36c.39.39.39 1.03 0 1.41-.39.39-1.03.39-1.41 0l-1.06-1.06c-.39-.39-.39-1.03 0-1.41.39-.39 1.03-.39 1.41 0l1.06 1.06z"/></svg>',
    dark: '<svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><path fill="currentColor" d="M12 3c-4.97 0-9 4.03-9 9s4.03 9 9 9 9-4.03 9-9c0-.46-.04-.92-.1-1.36-.98 1.37-2.58 2.26-4.4 2.26-2.98 0-5.4-2.42-5.4-5.4 0-1.81.89-3.42 2.26-4.4-.44-.06-.9-.1-1.36-.1z"/></svg>',
    system: '<svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><path fill="currentColor" d="M10.85 12.65h2.3L12 9l-1.15 3.65zM20 8.69V4h-4.69L12 .69 8.69 4H4v4.69L.69 12 4 15.31V20h4.69L12 23.31 15.31 20H20v-4.69L23.31 12 20 8.69zM14.3 16l-.7-2h-3.2l-.7 2H7.8L11 7h2l3.2 9h-1.9z"/></svg>'
  };
  var UI_MORE_ICON = '<svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true"><path fill="currentColor" d="M12 8c1.1 0 2-.9 2-2s-.9-2-2-2-2 .9-2 2 .9 2 2 2zm0 2c-1.1 0-2 .9-2 2s.9 2 2 2 2-.9 2-2-.9-2-2-2zm0 6c-1.1 0-2 .9-2 2s.9 2 2 2 2-.9 2-2-.9-2-2-2z"/></svg>';
  var SUPPORT_MENU_SELECTOR = 'ul[role="menu"][aria-label*="支持"], ul[role="menu"][aria-label*="Support"]';
  var LIST_ROW_SELECTOR = "li[data-task-list-id]";
  var LIST_OPTIONS_SELECTOR = '[aria-label="列表选项"], [aria-label="List options"]';

  function uiAvailable() {
    return typeof document !== "undefined" &&
      typeof document.createElement === "function" &&
      typeof document.querySelector === "function" &&
      typeof MutationObserver === "function";
  }

  function uiCommand(name, args) {
    post({ kind: "command", name: name, args: args || {} });
  }

  function installUIStyles() {
    if (document.getElementById("gtasks-ui-style")) return;
    var style = document.createElement("style");
    style.id = "gtasks-ui-style";
    style.textContent = [
      // 「Google 应用」九宫格对桌面客户端没有意义，直接藏掉。
      'a.gb_C[aria-label*="Google 应用"], a.gb_C[aria-label*="Google apps"], a.gb_C[href*="/about/products"] { display: none !important; }',
      ".gtasks-hbtn { width: 40px; height: 40px; border-radius: 50%; display: inline-flex; align-items: center; justify-content: center; cursor: pointer; color: inherit; flex: none; }",
      ".gtasks-hbtn:hover, .gtasks-hbtn:focus { background: rgba(128,134,139,.14); outline: none; }",
      "li[data-task-list-id] { position: relative; }",
      ".gtasks-rowmenu { position: absolute; right: 6px; top: 50%; transform: translateY(-50%); width: 28px; height: 28px; border-radius: 50%; display: none; align-items: center; justify-content: center; cursor: pointer; color: inherit; z-index: 2; }",
      "li[data-task-list-id]:hover .gtasks-rowmenu, .gtasks-rowmenu:focus { display: inline-flex; }",
      ".gtasks-rowmenu:hover { background: rgba(128,134,139,.18); }",
      // 悬停时把计数让位给「列表选项」，避免两个元素叠在一起。
      'li[data-task-list-id]:hover > span[jsname="orbTae"] { visibility: hidden; }'
    ].join("\n");
    (document.head || document.documentElement).appendChild(style);
  }

  function themeModeNow() {
    var mode = window.__gtaskThemeMode;
    return UI_MODES.indexOf(mode) >= 0 ? mode : "system";
  }

  function renderThemeButton() {
    var button = document.getElementById("gtasks-theme-btn");
    if (!button) return;
    var mode = themeModeNow();
    button.innerHTML = UI_THEME_ICON[mode] || UI_THEME_ICON.system;
    button.title = UI_THEME_TITLE[mode] + "（点击切换）";
    button.setAttribute("aria-label", UI_THEME_TITLE[mode]);
  }

  function installHeaderButtons() {
    var support = document.querySelector('button[aria-label*="支持"], button[aria-label*="Support"]');
    if (!support || !support.parentElement) return;
    if (!document.getElementById("gtasks-theme-btn")) {
      var button = document.createElement("div");
      button.id = "gtasks-theme-btn";
      button.className = "gtasks-hbtn";
      button.setAttribute("role", "button");
      button.setAttribute("tabindex", "0");
      button.addEventListener("click", function (event) {
        event.stopPropagation();
        event.preventDefault();
        var next = UI_MODES[(UI_MODES.indexOf(themeModeNow()) + 1) % UI_MODES.length];
        uiCommand("setTheme", { mode: next });
      });
      button.addEventListener("keydown", function (event) {
        if (event.key === "Enter" || event.key === " ") button.click();
      });
      support.parentElement.insertBefore(button, support);
    }
    renderThemeButton();
  }

  function closeMenu(menu) {
    try {
      menu.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", keyCode: 27, bubbles: true }));
    } catch (ignored) {}
    setTimeout(function () {
      try {
        if (menu.isConnected || document.body.contains(menu)) document.body.click();
      } catch (ignored) {}
    }, 60);
  }

  function decorateSupportMenu(menu) {
    if (!menu || menu.querySelector("[data-gtasks-workbench]")) return;
    var item = document.createElement("li");
    item.className = "aqdrmf-rymPhb-ibnC6b aqdrmf-rymPhb-ibnC6b-OWXEXe-hXIJHe aqdrmf-rymPhb-ibnC6b-OWXEXe-SfQLQb-Woal0c-RWgCYc O68mGe-xl07Ob-ibnC6b-OWXEXe-r08add";
    item.setAttribute("role", "menuitem");
    item.setAttribute("tabindex", "0");
    item.setAttribute("data-gtasks-workbench", "1");
    item.innerHTML =
      '<span class="UTNHae"></span><span class="dNKuRb aqdrmf-rymPhb-sNKcce"></span>' +
      '<span class="aqdrmf-rymPhb-KkROqb"></span>' +
      '<span class="aqdrmf-rymPhb-Gtdoyb"><span class="aqdrmf-rymPhb-fpDzbe-fmcmS">GTasks 工作台</span></span>' +
      '<span class="aqdrmf-rymPhb-JMEf7e"></span>';
    item.addEventListener("click", function (event) {
      event.stopPropagation();
      event.preventDefault();
      closeMenu(menu);
      uiCommand("openWorkbench", {});
    });
    menu.appendChild(item);
  }

  // 内容区的「列表选项」按钮和清单标题在同一个 section 里，标题 id 就是
  // "<清单id>_label"，侧栏行上的 data-task-list-id 与之对应。
  function listOptionsButton(listId) {
    if (!listId) return null;
    var heading = document.getElementById(listId + "_label");
    if (!heading) return null;
    var section = heading.parentElement;
    for (var depth = 0; section && depth < 8; depth++) {
      var button = section.querySelector(LIST_OPTIONS_SELECTOR);
      if (button) return button;
      section = section.parentElement;
    }
    return null;
  }

  function openListOptions(listId) {
    var button = listOptionsButton(listId);
    if (button) {
      button.click();
      return;
    }
    var row = document.querySelector(LIST_ROW_SELECTOR + '[data-task-list-id="' + listId + '"]');
    if (!row) return;
    // 该清单当前没在内容区渲染（多列视图只渲染选中的）：先切过去，等内容区
    // 出来再点它自己的「列表选项」，菜单内容和手工点完全一致。
    row.click();
    var tries = 0;
    var timer = setInterval(function () {
      tries += 1;
      var target = listOptionsButton(listId);
      if (target) {
        clearInterval(timer);
        target.click();
      } else if (tries > 25) {
        clearInterval(timer);
      }
    }, 80);
  }

  function decorateRow(row) {
    if (!row || row.querySelector("[data-gtasks-rowmenu]")) return;
    var button = document.createElement("span");
    button.className = "gtasks-rowmenu";
    button.setAttribute("data-gtasks-rowmenu", "1");
    button.setAttribute("role", "button");
    button.setAttribute("tabindex", "-1");
    button.title = "列表选项";
    button.innerHTML = UI_MORE_ICON;
    button.addEventListener("click", function (event) {
      event.stopPropagation();
      event.preventDefault();
      openListOptions(row.getAttribute("data-task-list-id"));
    });
    row.appendChild(button);
  }

  function decorateRows(root) {
    var scope = root || document;
    if (!scope.querySelectorAll) return;
    var rows = scope.querySelectorAll(LIST_ROW_SELECTOR);
    for (var index = 0; index < rows.length; index++) decorateRow(rows[index]);
  }

  function installContextMenu() {
    // 捕获阶段拦截，WebKit 的原生右键菜单（含前进/后退）就不会出现。
    document.addEventListener("contextmenu", function (event) {
      if (event.defaultPrevented) return;
      event.preventDefault();
      uiCommand("contextMenu", { x: event.clientX, y: event.clientY });
    }, true);
  }

  function observeUI() {
    var observer = new MutationObserver(function (records) {
      var needsHeader = !document.getElementById("gtasks-theme-btn");
      for (var index = 0; index < records.length; index++) {
        var added = records[index].addedNodes;
        for (var nodeIndex = 0; nodeIndex < added.length; nodeIndex++) {
          var node = added[nodeIndex];
          if (!node || node.nodeType !== 1) continue;
          if (!needsHeader && node.matches && node.matches(SUPPORT_MENU_SELECTOR)) {
            decorateSupportMenu(node);
          } else if (!needsHeader && node.querySelector) {
            var menu = node.querySelector(SUPPORT_MENU_SELECTOR);
            if (menu) decorateSupportMenu(menu);
          }
          if (node.matches && node.matches(LIST_ROW_SELECTOR)) decorateRow(node);
          else if (node.querySelector && node.querySelector(LIST_ROW_SELECTOR)) decorateRows(node);
        }
      }
      if (needsHeader) installHeaderButtons();
    });
    observer.observe(document.body, { childList: true, subtree: true });
  }

  function installUI() {
    if (!uiAvailable() || location.hostname !== "tasks.google.com") return;
    if (window.__gtaskUIInstalled) {
      renderThemeButton();
      decorateRows(document);
      return;
    }
    window.__gtaskUIInstalled = true;
    var boot = function () {
      installUIStyles();
      installContextMenu();
      installHeaderButtons();
      decorateRows(document);
      decorateSupportMenu(document.querySelector(SUPPORT_MENU_SELECTOR));
      observeUI();
    };
    if (document.body) boot();
    else document.addEventListener("DOMContentLoaded", boot);
  }

  // Go 侧在页面 ready 后把当前主题推下来，按钮图标随之刷新。
  api.setThemeMode = function (mode) {
    window.__gtaskThemeMode = UI_MODES.indexOf(mode) >= 0 ? mode : "system";
    renderThemeButton();
  };


  installInitDataRecorder();

  // 上一次开着抓包就立刻恢复，抢在页面自己的请求之前装好钩子。
  if (storedSniffing()) setSniffing(true);

  installFetchSniffer();
  installXHRSniffer();

  // Unlock WebviewWindow.ExecJS. Wails only flushes its pending-JS queue and
  // accepts ExecJS calls after it receives "wails:runtime:ready" from the page.
  // That message is normally sent by the Wails runtime bundle, which is never
  // loaded on an external origin, so the bridge reports it itself.
  if (send) {
    try { send("wails:runtime:ready"); } catch (ignored) {}
  }

  api.announce();

  installUI();

  // Document-start injection (Windows) announces before WIZ_global_data exists,
  // so re-announce once the page has finished loading. The Go watcher also polls,
  // this just shortens the window where the login state looks stale.
  if (document.readyState !== "complete" && typeof window.addEventListener === "function") {
    window.addEventListener("load", function () { api.announce(); });
  }
})();
