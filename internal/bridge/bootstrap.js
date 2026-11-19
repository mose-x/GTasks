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

  function isSniffing() {
    return window.__gtaskSniffing === true;
  }

  function setSniffing(on) {
    window.__gtaskSniffing = !!on;
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

  function record(entry) {
    if (!isSniffing()) return;
    window.__gtaskSniffed = (window.__gtaskSniffed || 0) + 1;
    entry.t = Date.now();
    entry.href = location.href;
    post({ kind: "sniff", entry: entry });
  }

  function installFetchSniffer() {
    if (window.__gtaskFetchPatched || typeof window.fetch !== "function") return;
    var original = window.fetch;
    var patched = function (input, init) {
      var url = typeof input === "string" ? input : (input && input.url) || "";
      var body = init && typeof init.body === "string" ? init.body : null;
      var entry = isSniffing() && !(init && init.__gtaskNoSniff) ? classify(url, body) : null;
      if (entry) entry.via = "fetch";
      var promise = original.apply(this, arguments);
      if (entry) {
        promise.then(function (response) {
          try {
            response.clone().text().then(function (text) {
              entry.status = response.status;
              entry.response = truncate(text);
              record(entry);
            }, function () {
              entry.status = response.status;
              record(entry);
            });
          } catch (ignored) {
            record(entry);
          }
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

  // ------------------------------------------------------------ batchexecute

  var requestCounter = Math.floor(Math.random() * 900000) + 100000;

  // health() 用一个必然不存在的 rpcid 发最小请求：会话活着时 Google 回 200 +
  // 一条错误帧；cookie 失效时回 401，或被 302 到 accounts.google.com 的登录页。
  var HEALTH_RPC = "AAAAA";

  function authState(response) {
    if (response.status === 401 || response.status === 403) return "unauthorized";
    if (response.redirected) return "redirected";
    try {
      if (response.url && location.origin && response.url.indexOf(location.origin) !== 0) return "redirected";
    } catch (ignored) {}
    if (!response.ok) return "http" + response.status;
    return "ok";
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
    var query = [
      "rpcids=" + encodeURIComponent(rpcids.join(",")),
      "source-path=" + encodeURIComponent(opts.sourcePath || location.pathname || "/"),
      "_reqid=" + requestCounter++,
      "rt=" + encodeURIComponent(opts.rt || "c")
    ];
    if (bl) query.splice(2, 0, "bl=" + encodeURIComponent(bl));
    var path = opts.path || "/_/TasksUi/data/batchexecute";
    var url = path + "?" + query.join("&");

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
        reply(id, response.ok, {
          status: response.status,
          auth: authState(response),
          url: url,
          path: path,
          at: at,
          bl: bl,
          rpcids: rpcids,
          elapsedMs: Date.now() - startedAt,
          raw: truncate(text)
        }, response.ok ? "" : "HTTP " + response.status);
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
    health: function (id) {
      var wiz = wizData();
      var at = wiz.SNlM0e || "";
      if (!at) {
        reply(id, true, { state: "anonymous", status: 0, at: "" }, "");
        return;
      }
      var url = "/_/TasksUi/data/batchexecute?" + [
        "rpcids=" + HEALTH_RPC,
        "source-path=" + encodeURIComponent(location.pathname || "/"),
        "_reqid=" + requestCounter++,
        "rt=c"
      ].join("&");
      fetch(url, {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8", "X-Same-Domain": "1" },
        body: "f.req=" + encodeURIComponent(JSON.stringify([[[HEALTH_RPC, "[]", null, "1"]]])) + "&at=" + encodeURIComponent(at) + "&",
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

  // Document-start injection (Windows) announces before WIZ_global_data exists,
  // so re-announce once the page has finished loading. The Go watcher also polls,
  // this just shortens the window where the login state looks stale.
  if (document.readyState !== "complete" && typeof window.addEventListener === "function") {
    window.addEventListener("load", function () { api.announce(); });
  }
})();
