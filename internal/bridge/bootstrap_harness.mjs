// Test harness for bootstrap.js.
//
// Runs the bridge script inside a sandbox that imitates a logged-in
// tasks.google.com page, feeds it the dispatch scripts produced by the Go side,
// and prints everything the script posted back plus every fetch it issued.
//
// Usage: node bootstrap_harness.mjs <bootstrap.js> <spec.json>
// spec.json: { scripts: string[], fetchBody: string, fetchStatus: number }

import { readFileSync } from "node:fs";
import vm from "node:vm";

const [, , bootstrapPath, specPath] = process.argv;
const bootstrap = readFileSync(bootstrapPath, "utf8");
const spec = JSON.parse(readFileSync(specPath, "utf8"));

const messages = [];
const requests = [];
const listeners = {};

function makeResponse(body, status) {
  const response = {
    ok: status >= 200 && status < 300,
    status,
    text: async () => body,
  };
  response.clone = () => makeResponse(body, status);
  return response;
}

const sandbox = {
  console,
  setTimeout,
  clearTimeout,
  navigator: { userAgent: "harness-ua" },
  addEventListener: (type, fn) => {
    (listeners[type] ||= []).push(fn);
  },
  location: {
    href: "https://tasks.google.com/",
    origin: "https://tasks.google.com",
    pathname: "/",
    hostname: "tasks.google.com",
    ...(spec.location || {}),
  },
  document: {
    title: "Google Tasks",
    cookie: "SID=abc; __Secure-1PSID=xyz",
    readyState: spec.readyState ?? "complete",
  },
  WIZ_global_data: {
    SNlM0e: "AT-token-123",
    cfb2h: "boq.tasks.ui.20261118.00",
    oxN3nb: [1, 2, 3],
  },
  fetch: async (url, init) => {
    requests.push({ url: String(url), init: init || null });
    return makeResponse(spec.fetchBody ?? "", spec.fetchStatus ?? 200);
  },
};

function FakeXHR() {}
FakeXHR.prototype.open = function (method, url) {
  this.__method = method;
  this.__url = url;
};
FakeXHR.prototype.send = function () {
  this.status = 200;
  this.responseText = "";
  (this.__listeners || []).forEach((fn) => fn());
};
FakeXHR.prototype.addEventListener = function (type, fn) {
  this.__listeners = this.__listeners || [];
  this.__listeners.push(fn);
};

sandbox.window = sandbox;
sandbox.XMLHttpRequest = FakeXHR;
sandbox.webkit = {
  messageHandlers: {
    external: { postMessage: (text) => messages.push(String(text)) },
  },
};

const context = vm.createContext(sandbox);

for (const script of spec.scripts || []) {
  vm.runInContext(script, context);
  await new Promise((resolve) => setTimeout(resolve, 5));
}

// The bootstrap defers a second announce to the load event when it is injected
// before the document finishes loading (the Windows document-start path).
for (const fn of listeners.load || []) fn();
await new Promise((resolve) => setTimeout(resolve, 5));

process.stdout.write(JSON.stringify({ messages, requests }, null, 2));
