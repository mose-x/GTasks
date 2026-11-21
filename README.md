# GTasks — a Google Tasks desktop client (Wails v3 + Go)

**Language**: English | [简体中文](README.ZH_CN.md)

A third-party desktop client for Google Tasks. Goals: **a UI identical to the web app**,
sign-in through **Google's own pages**, and data-plane coverage for the things the
official API cannot do (stars / due times / recurrence).

At this stage there are two parts:

1. **Main window** — a light-theme UI modelled on `tasks.google.com` (sidebar / task
   list / detail panel), with a demo-data mode so fidelity can be checked before a
   given rpcid has been discovered.
2. **Reverse-engineering workbench** — a separate window used to sign in, capture
   traffic, and freeze captured rpcids into reusable "operations".

> ⚠️ This project talks to Google through undocumented endpoints, which violates the
> [Google Terms of Service](https://policies.google.com). For personal study only —
> **the account risk is entirely yours**.

## Why this architecture

The official Tasks API v1 does not cover everything the web app can do. Taking
`tasks/v1/tasks-gen.go` from `google.golang.org/api@v0.316.0` as ground truth, the
`Task` resource has **no** `starred` / `recurrence` / `reminder` / `dueTime` fields at
all, and the only methods are `clear` `delete` `get` `insert` `list` `move` `patch`
`update`. Missing:

- ⭐ Stars — not readable or writable through the official API
- 🕐 The **time of day** on a due date — `due` keeps the date only
- 🔁 Recurrence rules, 🔔 reminders — do not exist in the official API

The internal `batchexecute` endpoint comes with two hard constraints: it does **not**
accept an OAuth bearer token, only the web session cookies (`__Secure-1PSID` and
friends, all httpOnly); and each request also needs `at` (`SNlM0e`) and `bl` (build
label).

The usual approach is "scrape the cookies into Go and issue the requests yourself",
which is cross-platform but brittle. This project instead does:

```
┌────────────────┐  ┌────────────────┐   ┌──────────────────────────────┐
│ Main window     │  │ Workbench      │   │ Session window (auto-hidden) │
│ /              │  │ /workbench.html│   │ https://tasks.google.com/    │
│ Tasks UI       │  │ capture/freeze │   │ Google sign-in + web app     │
│                │  │                │   │ ← bootstrap.js injected      │
└───────┬────────┘  └───────┬────────┘   └──────────┬────────▲──────────┘
        │ HTTP (asset server)                       │ ExecJS │ external
        ▼                                           ▼        │ postMessage
┌────────────────────────────────────────────────────────────┴──────────────┐
│ api.Handler → tasks.Service → tasks.Registry → batchexec.Client           │
│                                ↘ session.Watcher (login-state polling)     │
└───────────────────────────────────────────────────────────────────────────┘
```

**The key point: every Google request is issued by `fetch` inside the session
window's own page.**

- Cookies are attached by the browser; the Go side never touches a credential
- `at` / `bl` are read straight from `window.WIZ_global_data`, no HTML scraping
- UA, `X-Same-Domain`, same-origin policy — all genuine browser behaviour
- Sign-in is Google's own login page: **no OAuth client_id, no Google review**

### The trick that unlocks `ExecJS`

Wails v3's `WebviewWindow.ExecJS()` is gated on `runtimeLoaded`
(`pkg/application/webview_window.go:701`), which is only set once the page sends
`wails:runtime:ready` (same file, `:884`). A page from an external origin never loads
the Wails runtime bundle, so `ExecJS` silently queues scripts that never run.

`internal/bridge/bootstrap.js` is injected through `WebviewWindowOptions.JS`, and the
first thing it does is send that reserved message itself:

```js
send("wails:runtime:ready"); // → WebviewWindow.HandleMessage → runtimeLoaded = true
```

`ExecJS` then works normally — **no cgo, no reading the native cookie store, no Wails
fork** — on all three platforms. The return path uses the message channel Wails already
registers (macOS `webkit.messageHandlers.external`, `webview_window_darwin.go:193`;
Linux the same handler name, `register_script_message_handler(manager, "external")`;
Windows `chrome.webview.postMessage` → `TryGetWebMessageAsString`), received by
`application.Options.RawMessageHandler` — a handler that is independent of page origin.

### Injection timing differs per platform

| Platform | Injection point                  | When                                                                           |
| -------- | -------------------------------- | ------------------------------------------------------------------------------ |
| macOS    | `webview_window_darwin.go:1755`  | `execJS` after `WebViewDidFinishNavigation`                                    |
| Linux    | `webview_window_linux.go:527`    | `execJS` after `WindowLoadFinished`                                            |
| Windows  | `webview_window_windows.go:2835` | **HTML windows only**: `chromium.Init` → `AddScriptToExecuteOnDocumentCreated` |

That Windows line is the important trap: `navigateInitialPage` only handles `options.JS`
in the `options.HTML != ""` branch; for an external-URL window the `default:` branch just
calls `setURL`, so **the bootstrap would never be injected**. Therefore `main.go` uses
`bridge.RedirectHTML(googleTasksURL)` on Windows: load a stub document containing only
`location.replace(...)` so Wails takes the HTML branch and registers the bootstrap with
`AddScriptToExecuteOnDocumentCreated`. That registration lives for the whole webview
lifetime, so every subsequent document (including the `accounts.google.com` round trip)
is injected at **document start** — earlier than macOS/Linux, and early enough to capture
first-paint RPCs.

The cost is that the script runs in **every** document of that webview, so
`bootstrap.js` opens with a Google-domain guard (`isGoogleHost()`): the stub document and
any non-Google page return immediately, which both avoids bogus login state and avoids
unlocking `ExecJS` too early.

Another platform difference: `OriginInfo.IsMainFrame` is **only populated by the darwin
backend** (`application_darwin.go:409`); Windows (`webview_window_windows.go:2475`) and
WebKitGTK (`linux_cgo.go:1711`) always report false. `bridge.HandleRaw` therefore filters
subframes only on platforms where the field is trustworthy
(`reportsMainFrame = runtime.GOOS == "darwin"`), and elsewhere filters by protocol prefix
plus window name — otherwise the whole bridge would be dropped silently.

### Session resilience: detecting "looks signed in, actually dead"

After the cookies expire the SPA still holds the old `WIZ_global_data`: `SNlM0e` is still
on the page and a probe still reports "signed in", yet every RPC returns 401 or a 302 to
the login page. So every 8th poll (roughly half a minute) the watcher additionally runs a
**health check** (the `health` method in `bootstrap.js`): a minimal request with an rpcid
that cannot exist (`AAAAA`). A live session returns 200 plus one error frame; dead cookies
return 401, or `fetch` follows a redirect and lands on `accounts.google.com`
(`response.redirected`). The request carries a `__gtaskNoSniff` marker so it never pollutes
the capture log; if the previous round was abnormal, the check runs every round instead.

Once the verdict is `unauthorized` / `redirected`:

- the main window shows a "session expired, please sign in again" banner with an
  "Open sign-in window" button
- the workbench `Login` badge becomes `yes (session dead)`
- `main.go` pops the session window and reloads it once (rate limited to once per
  3 minutes), then hands over to Google's own login flow
- the window hides itself again once login completes or the session recovers

Rotation of cookies such as `__Secure-1PSIDTS` is done by the page's own JavaScript (the
session window loads the real tasks.google.com); this project does not rotate anything.
A failed rotation eventually surfaces as 401 / redirect, which the health check catches
all the same.

## Read layer: one RPC returns everything

Switching lists, clicking "Starred", expanding "Completed" — the web app issues **no
further requests** for any of it; every view is rendered locally from the single
first-paint payload. So the read layer needs exactly one rpcid:

|          |                                                                                                                                         |
| -------- | --------------------------------------------------------------------------------------------------------------------------------------- |
| rpcid    | `Yz8iP` (the page's `window.AF_dataServiceRequests["ds:0"].id`)                                                                         |
| endpoint | `/_/TasksWebEmbedUi/data/batchexecute` (published by the page in `WIZ_global_data.Im6cmf`)                                              |
| args     | empty array                                                                                                                             |
| returns  | every list + open tasks (including the server-declared display order) + the first 10 completed tasks per list + star flags + list order |

The response is an array of **positional slots**, not keyed objects, and Google publishes
no schema, so the slot map in `internal/tasks/model.go` was established by comparing live
responses against the rendered DOM slot by slot:

- For a task, `inner[1][13]` is the starred boolean and `inner[1][14]` the starred
  timestamp — fields the official API does not have at all
- For a list, `d[2][0]` is the server-declared display order of open tasks, and **it is
  the only trustworthy sort key** (the task's own `inner[7]` sort key is not monotonic)
- For a list, `d[9]` is the real completed count, larger than what the snapshot actually
  carries (only the first 10 per list) — exactly the number in the web app's
  "Completed (44)"
- Completed tasks are rendered by `completedAt` descending, matching the web app

Slots whose meaning is still unknown are not discarded: they are collected verbatim into
`SlotBag` (`Extra` / `DetailExtra` / `MetaExtra`) and returned along with `/api/tasks`.
When Google changes the structure it shows up there first, instead of manifesting as "some
field quietly disappeared". `internal/tasks/model_test.go` pins every slot down with a
synthetic snapshot (containing no real task content).

The main window therefore has exactly one data endpoint: `GET /api/tasks`.

## Write layer: a single bidirectional sync channel, `SOV0tb`

The assumption was that each action (star, retitle, delete…) had its own rpcid. Capturing
traffic proved otherwise: **every** write in the web app goes through the same sync stream
`SOV0tb`. The request carries **field-level patch events**; the response carries the
server's own mutations plus a new sync timestamp. So the write layer is not "find N
rpcids" but "learn to synthesise this one message" — `internal/tasks/sync.go` is the
implementation of that protocol.

### Message shape

The request args (a single-element array):

```json
[[null, syncState], null, [null, ["account id"]], null,
 ["random 16 chars", [null, 4, "boq_...", "prod-00-us", 3, 1, 2, null, 1]], []]
```

- **Polling**: `syncState = [[sec, nsec]]`, meaning "give me everything after this point"
- **Writing**: `syncState = [[sec, nsec], null, eventGroup]`,
  `eventGroup = [[[event, ...], []]]`
- **Response**: `[entityMutations, null, [[newSec, newNsec]]]`; sending empty args is
  rejected (error code 13)

**Timestamps must be chained**: the next request has to carry the new value from the tail
of the previous response. A stale timestamp intermittently yields error code 3, and the
page itself polls the same channel on a timer, pushing the server time forward at any
moment. So the write layer does an empty poll before every commit to fetch the latest
value, and on error code 3 it polls again and retries (`SyncWriter.commit`).

### Event structure

An event is an array of 28 slots; only these carry meaning:

| Slot      | Meaning                                                                          |
| --------- | -------------------------------------------------------------------------------- |
| 0         | Client-generated event id (16 chars, base64url)                                  |
| 1         | Entity type: `2` account-level list order (singleton, no id), `3` task, `4` list |
| 2         | Entity id                                                                        |
| 3         | Path; `[1]` for a single-event write, `[1]`/`[1,1]`/`[1,2]` when creating a task |
| 5 / 6 / 7 | Patch, for entity types 2 / 3 / 4 respectively                                   |
| 8         | Insert position inside a list: `[taskId, null, null, null, null, null, 1]`       |
| 9         | Client timestamp `[sec, nsec]`                                                   |
| 10        | `[null, listId]`, the task's parent list                                         |
| 11        | `[[], []]`, order placeholder                                                    |
| 25        | Constant `1`                                                                     |
| 26        | Sequence number within the group (1, 2, 3… for multi-event writes)               |
| 27        | Tail, not validated by the server                                                |

### Patches and field numbers

```json
[[null, [fieldNumber...]], valueArray]
```

The value array is **dense**: index = field number − 1, trailing empty slots trimmed. So
"retitle" is `[[null,[2]],[null,"new title"]]`, and **clearing a field means sending an
empty value array** — that is how un-starring, un-completing and clearing a due date are
written.

Field numbers for a task's detail (the read side uses the same numbering: detail array
index = field number − 1):

| Field | Meaning      | How to write it                                                                                                                                  |
| ----- | ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| 1     | Completed    | `1`, written together with field 5                                                                                                               |
| 2     | Title        | string                                                                                                                                           |
| 3     | Notes        | string, may contain newlines                                                                                                                     |
| 5     | Completed at | `[sec, nsec]`                                                                                                                                    |
| 6     | Deleted      | `1` to delete, empty array to restore. The server removes the task from the list order by itself and records the deletion time (detail index 12) |
| 14    | Starred      | `1`, written together with field 15                                                                                                              |
| 15    | Starred at   | `[sec, nsec]`                                                                                                                                    |
| 18    | Due date     | payload `[[sec, nsec]]`, wrapped one level deeper than a plain timestamp                                                                         |

Lists: field `1` title, `3` kind (the server fills in `user` itself), `5` sort order. The
account-level order singleton: field `3` is a comma-separated list csv, and **the server
validates that it contains exactly all non-deleted lists**, so creating a list requires
re-reading the snapshot for the current order — a cached one will not do.

Timezone convention for due times: a date-only value is stored as **UTC midnight**
(Google's whole-day convention, so every timezone sees the same day); when a time of day
is supplied it is converted from local time to a real instant, which is what makes the
Google web / mobile clients show the clock time the user picked. The read side restores
the same rule: UTC midnight → date only, anything else → local date + time.

### Operations built out of this

| Operation                                                       | Events                                                                                                                                                                                |
| --------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Create task                                                     | 4: create entity (with title) → order placeholder (slot 11) → insert into list (type 4 + slot 8) → bind list (slot 10). The task id is generated client-side and the server adopts it |
| Retitle / edit notes / star / complete / due / delete / restore | 1 task patch event                                                                                                                                                                    |
| Create list                                                     | 2: create list (type 4, field 1) + rewrite the order csv (type 2, field 3)                                                                                                            |
| Rename list                                                     | 1 list patch event                                                                                                                                                                    |

These messages are computed by Go on the fly and **do not go through the
`operations.json` templates** — templates cannot express chained timestamps or
client-generated ids. `tasks.Service.Execute` dispatches them to `SyncWriter`, and the
workbench shows them as `SOV0tb (sync channel)`. The frontend contract is unchanged:
still `POST /api/ops/execute {op, params}`. `internal/tasks/sync_test.go` pins the exact
JSON of every message with a fake client, including timestamp chaining and the error-3
retry.

**Still unsolved**: deleting a list (setting field 2 to true → error 13 INTERNAL, probably
not a field-level patch), reordering tasks and moving them across lists (writing slot 10
alone is not enough; opcodes 0/2/3/4 in slot 8 all fail), subtasks, recurrence rules
(field 8 accepts a write but has no effect).

## Operation mapping: turning captures into capabilities

rpcids and argument structures are undocumented and can only be captured from the live web
app. To make a capture reusable there is an **operation registry**
(`internal/tasks/operations.json`): 23 logical operations, each mapping to one or more
batchexecute calls with `text/template` arguments. Today `snapshot` goes through a
template, 12 write operations go through the sync channel described above (the registry
only keeps their description, no steps), and the rest are still waiting on captures:

```json
{ "op": "setStarred", "steps": [{ "rpcid": "AbCdE", "args": "[null,{{json .TaskID}},[{{json .Starred}}]]" }] }
```

`tasks.Service.Execute(ctx, "snapshot", params)` renders the template → batches → sends
through the page → decodes the response. Every button in the main window calls one op, so
**each channel that gets unlocked lights up another batch of features in the UI**.

The registry is persisted in the user config directory, and captures are persisted as
JSONL:

| Platform | Directory                               |
| -------- | --------------------------------------- |
| macOS    | `~/Library/Application Support/gtasks/` |
| Linux    | `~/.config/gtasks/`                     |
| Windows  | `%AppData%\gtasks\`                     |

Inside: `operations.json` (the operation map) and `captures.jsonl` (append-only capture
history).

## Desktop adaptations: the injected layer in the session page

The session window is the real tasks.google.com you use every day. The UI layer of
`internal/bridge/bootstrap.js` makes a few desktop-friendly changes on top of it (DOM and
callbacks into Go only — Google's own logic is never replaced):

- **The "Google apps" waffle is removed from the top bar** (hidden with pure CSS).
- **Theme / language buttons in the top bar**: absolutely positioned into the gap left of
  the "?" (order: theme / language / ?, matching the main window's sidebar); clicking
  opens Go's native radio menu. There is a trap here: the right segment of Google's top
  bar (`.gb_Pe`) is an `overflow: hidden` clipping box only 96px wide, so injected
  buttons drawn outside it simply do not show; the `.gb_fe` on the left is a `flex: 1` gap.
  The fix widens the clipping box by 44px and nudges the "?" right to make room, leaving
  the avatar and Google's own layout untouched. Google re-lays out the top bar during
  first paint, so button positions are corrected at a few time points and re-arranged on
  window resize.
- **Theme** has three settings (light / dark / follow system), persisted by Go
  (`<data dir>/theme.txt`). On macOS it switches both `NSApp.appearance` and each window's
  `appearance`, so the title bar and the Google page's `prefers-color-scheme` change
  together. Google's colours are entirely media-query driven and recompute the moment the
  appearance changes, so switching theme **no longer reloads the session page**; a reload
  is only used as a fallback when the page reports that its media queries did not follow
  (certain WebView versions).
- **"Help" in the "Support" menu is repurposed into "GTasks Workbench"**: it reuses
  Google's own menu item (row height, hover highlight and keyboard navigation all stay
  consistent) and moves it to the end of the menu. A help page is meaningless in a desktop
  client, which frees the slot. Clicking it opens the reverse-engineering workbench.
- **Startup splash**: there is a 1–3 second gap between navigation and Google's first
  paint. At document-start the bootstrap covers the page with a splash in the current
  theme colour plus a spinner (`#gtasks-splash`) and fades it out as soon as the shell
  renders; the splash ignores mouse events and has an 8 second hard timeout. All three
  windows' `BackgroundColour` and the webview's `underPageBackgroundColor` are also
  pre-set from the theme, so dark mode no longer flashes white.
- **A "⋮" on every list row in the sidebar**: appears on hover and still clicks Google's
  own "List options" menu (sort / rename / delete / move…); if that list is not currently
  rendered in the content area it is selected first, then clicked.
- **The context menu is a native custom one**: back / reload / cut / copy / paste / select
  all / inspect element — "Forward" from WebKit's default menu is gone. The main window and
  the workbench use the Wails runtime's `--custom-contextmenu` CSS variables; the session
  page intercepts `contextmenu` in the bootstrap and calls back into Go.

The app icon is Google Tasks' own product mark (the `tasks_2026` SVG on gstatic, personal
use only): `build/macos-icon.sh` rasterises it with headless Chrome into an iconset and
packs it with `iconutil`; `build/macos-bundle.sh` assembles `GTasks.app`
(Info.plist + icns + binary). **`CFBundleIdentifier` is pinned to `gtasks`**: WebKit
isolates cookies / localStorage by that value, so changing it forces a re-login.

## Build & run

Repository: <https://github.com/mose-x/GTasks.git>

```bash
git clone https://github.com/mose-x/GTasks.git
cd GTasks
make build && ./gtasks      # or: go run .
make app                    # macOS: assemble /tmp/GTasks.app with its icon
make help                   # every target
```

Requires **Go 1.26+** (a Wails v3.1.0 requirement; if only 1.25 is installed the toolchain
downloads 1.26 automatically). The `Makefile` defaults `GOPROXY` to
`https://goproxy.cn,direct`; override it if you prefer. The app icon is committed at
`build/macos/GTasks.icns`; run `make icon` only after changing `build/icon.svg` (it
rasterises with headless Chrome, so a local Chrome is required).

| Target  | Command                                                            | Dependencies                                                                                                 |
| ------- | ------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------ |
| macOS   | `make build`                                                       | Xcode Command Line Tools (cgo)                                                                               |
| Windows | `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o gtasks.exe .` | Cross-compiles from macOS / Linux; needs the WebView2 Runtime at run time (usually preinstalled on Win10/11) |
| Linux   | `CGO_ENABLED=0 go build -tags purego -o gtasks .`                  | Runtime libraries only: `libgtk-3-0 libwebkit2gtk-4.1-0` (Arch: `gtk3 webkit2gtk-4.1`)                       |

All six targets (Windows amd64/arm64, Linux amd64/arm64 purego, macOS amd64/arm64) are
verified with `go build`; **on-device smoke tests for Windows and Linux are still
outstanding**.

### Release artifacts

`make package` (current platform) or `make dist` (three platforms × amd64/arm64) writes to
`build/bin/`, with names following `gtasks-<version>-<platform>-<arch>.<ext>` (Go's
`amd64` becomes `x64` in asset names):

| Platform | Artifacts                                                            | Script                     |
| -------- | -------------------------------------------------------------------- | -------------------------- |
| macOS    | `.dmg`                                                               | `scripts/build-macos.sh`   |
| Windows  | `.exe`                                                               | `scripts/build-windows.sh` |
| Linux    | bare binary + `.tar.gz` (plus `.deb` / `.rpm` when fpm is installed) | `scripts/build-linux.sh`   |

The version is stamped both into the binary (`./gtasks --version`) and into the macOS
`Info.plist`. Pushing a `v*` tag makes `.github/workflows/build.yml` build every target
and publish a Release with `sha256sums.txt`; tags carrying an `-rc` / `-beta` suffix are
published as pre-releases. Pushing a branch or PR runs
`.github/workflows/ci.yml` (gofmt + vet + tests on all three platforms). Details in
`scripts/README.md`.

macOS artifacts are **not code-signed**: under strict MDM an ad-hoc signature is rejected
by amfid with `-423`, which is worse than no signature at all, so packaging strips the
ad-hoc signature the Go linker applies and stamps the DMG with `com.apple.quarantine` —
first launch then goes through the Gatekeeper "right-click → Open" bypass. **Do not run
`xattr -cr`**: clearing the quarantine attribute routes the launch through the provenance
path, which SIGKILLs unsigned binaries (exit 137). If an install still refuses to launch,
run `./scripts/run-first-installed-local-mac.sh` once.

### Commit rules and branch flow

This repo adopts [code-hooks](https://github.com/mose-x/code-hooks): with
`core.hooksPath` pointed at it, `commit-msg` enforces conventional commits, pure
ASCII, subject <= 100 and total <= 200 characters; `pre-push` only allows pushing
`dev` (`main` is updated by merging PRs) and runs `go test` / `npm test` /
`pytest` before the push. PRs additionally run the `commit-lint` workflow, the one
layer `--no-verify` cannot bypass. The JS and Python toolchains the hooks need
(prettier / eslint / ruff / pytest) are configured in `package.json`,
`.prettierrc`, `.prettierignore`, `eslint.config.mjs` and `pyproject.toml`.

`main` is protected on GitHub: the required status checks are `fmt + vet`,
`test (ubuntu-latest)`, `test (windows-latest)`, `test (macos-latest)` and
`commit-lint / commit-lint`, and a branch must be up to date before merging.
Daily work happens on `dev` and reaches `main` through a pull request, so a
commit that skipped the local hooks still cannot land on `main`.

Two windows appear on launch: **Google Tasks** (the main UI) and **Google Session**
(loads `tasks.google.com`; it redirects to Google's login page when signed out). The
**workbench** does not pop up by default — open it from "GTasks Workbench" in the "?" menu
of the session page's top bar (it is rebuilt on demand after being closed).

Theme has three settings (light / dark / follow system) owned by Go, with a button in the
main window's sidebar and one in the session page's top bar; switching in either place
syncs everywhere. Dark mode follows Google's own web dark palette (background `#202124`,
body text `#e8eaed`, accent `#8ab4f8`) and sets `color-scheme: dark` so native controls
(date picker, dropdowns, scrollbars) darken too. Switching is instant: the native
appearance changes, then every window's CSS variables and the Google page's media queries
recompute — no page reload. Before the main window's first frame, an inline script in
`index.html` applies the cached localStorage colours so dark mode does not flash white.
The workbench is a reverse-engineering tool and stays dark regardless of theme.

Capture history is reloaded into the workbench after a restart (the last 3000 entries are
kept, the UI shows the newest 400 rows). Entries classified as "internal sync calls"
(the show/hide RPCs produced when the sidebar's list options temporarily show or hide an
unselected list) collapse into a single group row that expands on click — they are a side
effect of the desktop adaptations, not something you care about while reverse-engineering.

UI copy lives in `internal/i18n/locales/*.toml` (embedded with `go:embed`), currently
Simplified Chinese and English: the main window sidebar, the session page top bar and the
workbench top bar each have a language button (after the theme button) that opens Go's
native radio menu. The current language is persisted at `<data dir>/lang.txt`, and
switching updates all three windows plus the native context menu. The catalogues are flat
key/value files whose keys map one-to-one onto the fallback literals in the code's
`t("key", "中文兜底")` calls; a missing key falls back to Chinese, so the UI never shows a
hole. **Adding a language means dropping in a `jp.toml` and rebuilding** — the menu gains
an entry with zero code changes (`meta.label` is the name shown in the menu, written in
that language). The embedded Google page follows along: the session URL carries an `hl=`
UI-language parameter (`zh`→`zh-CN`, `jp`→`ja`, everything else passed through), and
switching language does one in-place `location.replace` reload so Google's own copy
matches the app language.

That reload is a full page load and the bridge has to pause for a few seconds, so
`langNavigator` (`main.go`) keeps it to a minimum: no reload when the language is
unchanged; when the session window is hidden (the normal state after login) the reload is
deferred and settled once the window actually surfaces, so the main window only swaps its
catalogue and the user feels nothing; only when the user is looking at the session page is
it reloaded on the spot, with the white flash covered by the bootstrap splash. While the
reload runs the `watcher` enters a quiet period (shortened to just cover the next poll
once the page announces ready) so that ready-state jitter cannot throw the session window
in your face. Over the same window, if `/api/tasks` hits an unavailable bridge it returns
the last successful snapshot flagged `stale`, so the main window neither flashes an empty
list nor raises a red banner, and the frontend quietly refreshes once the bridge is back.
Re-rendering (`renderAll`) preserves the scroll positions and focus of all three scroll
areas, so switching language no longer feels like "the UI was reset". All of this is
covered end to end by `scripts/verify-lang-switch.py` (see Testing).

`session.Watcher` probes the session page every 4 seconds: it hides the session window
after login and brings it back when the session dies; sniffing is started automatically as
soon as the bridge is ready. Both behaviours can be turned off in the workbench top bar.

Testing:

```bash
go test ./...        # or: make check (gofmt + vet + test)
```

- `internal/bridge` runs `bootstrap.js` in Node against a faked tasks.google.com page and
  asserts for real: `wails:runtime:ready` is sent, `at`/`bl`/`loggedIn` are read
  correctly, outgoing request URLs contain
  `rpcids`/`bl`/`rt=c`/`source-path`/`_reqid`, headers include `X-Same-Domain: 1` and
  `credentials: include`, and the double-encoded `f.req` structure is right. Skipped
  automatically when `node` is absent.
- `internal/batchexec` covers `f.req` encoding plus `)]}'` / chunked response decoding.
- `internal/tasks` covers registry persistence and reload, template parameter
  substitution, and errors for unconfigured operations.
- `internal/api` covers capture → Go code generation (including `go/format` validation of
  the emitted snippet).

After touching language- or theme-related code, also run the end-to-end regression (the app
must be running and signed in to Google):

```bash
python3 scripts/verify-lang-switch.py            # full run, ~70 s, 34 assertions
python3 scripts/verify-lang-switch.py --quick    # skips the light-theme round
make verify                                      # same as the full run
```

It drives the local control port and checks, in order: re-selecting the same language
triggers no reload; switching language while the session window is hidden only swaps the
catalogue (the session page stays put, the window is not thrown in your face, and the main
window's data stays fresh throughout); the deferred reload is settled automatically once
the window surfaces; switching while the window is visible reloads on the spot (the splash
must cover the whole gap and its background must match the page background, and the main
window keeps serving from the `/api/tasks` snapshot with no stuck requests), once in dark
and once in light. Language, theme and window state are restored afterwards; a clean run
exits 0.

## rpcid discovery workflow

1. Sign in to Google normally in the **session window**; the workbench top bar should
   show `Login: yes` with values for `at`/`bl`
2. Capturing is on by default (there is also a **Start** button)
3. Perform an action in the session window, e.g. star a task
4. A record appears in the workbench's "RPC captures"; expand it to see `f.req` and the
   response, then click **Decode response** to inspect the structure
5. Click **Generate code / Freeze as operation**: the right pane shows a compilable Go
   snippet and the steps JSON
6. In the "Freeze as operation" dropdown pick the matching op (e.g. `setStarred`), replace
   literals with template placeholders such as `{{json .TaskID}}` where needed, and click
   **Save as operation**
7. Switch to the "Operation mapping" page and replay it from the **Execute** panel with
   parameters to confirm the semantics
8. Back in the main window, the corresponding button is now live

Cover them one by one: create / delete / retitle / edit notes / due date with time /
star / recurrence / drag to reorder (within and across lists) / add subtask / restore
deleted / clear completed / list create-delete-rename-reorder / search.

**Known limitation**: on macOS / Linux `WebviewWindowOptions.JS` is injected **after**
navigation finishes, so the first-paint RPCs may be missed (Windows injects at document
start and captures them too). Anything triggered by **user interaction** is captured on
all three platforms, which covers everything the rpcid discovery workflow needs.

Also, Wails re-runs the bootstrap after every navigation, and a fresh copy's sniffing
switch defaults to off. The switch therefore lives on `window.__gtaskSniffing` rather than
in a closure, and `session.Watcher` re-issues `sniffStart` on every poll (idempotent) so
recording cannot silently stop after a login redirect.

## TODO

- [x] Read layer: `snapshot` (`Yz8iP`) parsing + `GET /api/tasks` + real data in the main
      window (sidebar "All tasks / Starred / lists with counts / New list", a collapsible
      "Completed (N)" section per list, and "Completed: M/D weekday" on completed rows)
- [x] Write layer: the `SOV0tb` sync channel works — create / retitle / edit notes / star
      / complete / uncomplete / due date with time / delete / restore / create list /
      rename list all usable and verified by reading them back
- [x] Due-date slot: task detail field 18 (index 17), payload `[[sec, nsec]]`, parsed on
      the read side into `due.date` / `due.time`
- [ ] Delete list: field-level patching does not work (error code 13); needs a capture of
      the web app's real "delete list" event group
- [ ] Task reordering and cross-list moves: slot 8's opcode is still wrong; needs one real
      drag captured
- [ ] Subtasks: expected to need a parent-child event on top of `createTask`'s four events
- [ ] Recurrence rules: detail field 8 accepts a write but has no effect; internal
      structure unknown
- [ ] Completed paging: the snapshot returns only the first 10 per list; needs the
      "load more" rpcid captured
- [ ] "All tasks" view: on wide screens the web app renders each list as a horizontal
      column; the client currently renders vertical sections (same content, better suited
      to a desktop window) — still to be decided whether to switch
- [ ] Side-by-side comparison with the real web page to close pixel-level differences
      (just drag the session window next to it)
- [x] Three-state theme globalised: persisted by Go + native macOS appearance (title bar
      and session page included), with a button in the session page top bar too
- [x] Session page desktop-ified: "Google apps" waffle removed, workbench entry added to
      the "Support" menu, "⋮" on sidebar list rows, custom context menu (Forward removed)
- [x] Startup experience: theme-coloured first paint + session page splash, theme switching
      no longer reloads the page
- [x] UI localisation: toml catalogues embedded (zh / en), language button next to the
      theme button, adding a toml adds a language
- [x] Seamless language switching: session page reload deferred on demand
      (`langNavigator`), bridge fails fast during navigation (`MarkNavigating`), main
      window serves the previous snapshot, plus the end-to-end regression
      `scripts/verify-lang-switch.py`
- [x] "Internal sync calls" noise collapsed in the capture list (a side effect of showing
      / hiding lists temporarily), expandable to the raw records
- [x] App icon and macOS packaging scripts (`build/macos-bundle.sh` /
      `build/macos-icon.sh`)
- [x] Multi-platform packaging and release pipeline (`Makefile` + `scripts/build-*.sh` +
      `.github/workflows/*.yml`)
- [x] Windows / Linux bridge parity (bootstrap injection path, `IsMainFrame` filtering) +
      cross-compilation verified
- [ ] Windows / Linux on-device smoke tests (need real Windows / Linux machines and a
      Google account)
- [x] Dead-session detection and automatic re-login (health check recognises 401 / login
      redirect → banner + reload popup)
- [ ] Local cache and offline view

## Risks

- **No app-store listing, and no exemption from the ToS.** Accessing private endpoints
  violates the terms by itself.
- The real risk lands on **the user's account**: rate limiting, forced re-login, and in
  extreme cases a ban — irreversible. Low-frequency personal use is unlikely to be
  detected, but the probability is not zero.
- The internal endpoints are undocumented and come with no compatibility promise. Any
  Google release can change `bl`, an rpcid, or the argument array structure, and the only
  remedy is to capture again. Treat that as the long-term maintenance cost.

## License

MIT — see [`LICENSE`](LICENSE). The code licence changes nothing in the Risks section
above: using this project to access Google services remains subject to the Google Terms of
Service.
