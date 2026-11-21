#!/usr/bin/env python3
"""语言 / 主题切换的端到端回归（单窗口内嵌 Google 原页架构）。

跑之前应用要在运行、并且已经登录 Google：

    ./build/macos-bundle.sh /tmp/GTasks.app && open /tmp/GTasks.app
    python3 scripts/verify-lang-switch.py            # 全量
    python3 scripts/verify-lang-switch.py --quick    # 跳过浅色主题那一轮

覆盖的场景（对应 main.go 的 langNavigator 与 bridge.MarkNavigating）：

  A 同语言重选         不该触发任何重载：启动时 OnChange 的首次回调走的就是这条
  B 切主题             不重载、即时生效：Google 页靠 prefers-color-scheme 媒体
                       查询换色，原生外观一变页面立刻重算，遮罩不该出现
  C 切语言             当场整页重载：遮罩盖住全程、遮罩底色与页面底色一致、
                       桥接 / 工作台数据通道靠存货不断供
  D 浅色主题重复 C     浅色下同样不许露白

全部通过退出码 0。无论成功失败，结束时都会把语言 / 主题还原成进来时的样子
（--no-restore 可关闭）。
"""

from __future__ import annotations

import argparse
import json
import sys
import threading
import time
import urllib.error
import urllib.request

DEFAULT_API = "http://127.0.0.1:17321"
CLIENT_HEADER = {"X-GTasks-Client": "1", "Content-Type": "application/json"}

# 主窗口（内嵌的 Google 页）里读的探针：遮罩在不在、什么底色、页面语言、
# 注入按钮文案、首屏出没出、媒体查询当前是深是浅。
PROBE_JS = """(function () {
  var splash = document.getElementById('gtasks-splash');
  var langButton = document.getElementById('gtasks-lang-btn');
  var bodyBg = '';
  var dark = null;
  try { bodyBg = getComputedStyle(document.body).backgroundColor; } catch (ignored) {}
  try { dark = !!(window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches); } catch (ignored) {}
  return {
    readyState: document.readyState,
    splash: !!splash,
    hiding: splash ? (splash.getAttribute('data-gtasks-hiding') || '') : '',
    splashBg: splash ? getComputedStyle(splash).backgroundColor : '',
    splashText: splash ? (splash.textContent || '').trim().slice(0, 48) : '',
    hl: (location.search.match(/hl=([^&]*)/) || [, ''])[1],
    htmlLang: document.documentElement.lang || '',
    langButton: langButton ? (langButton.title || '') : '',
    shell: !!document.querySelector('.yVc39c, li[data-task-list-id], c-wiz'),
    bodyBg: bodyBg,
    dark: dark
  };
})()"""

# 与 main.go 的 googleHL 保持一致：词表语言码 → Google 的 hl 取值。
GOOGLE_HL = {"zh": "zh-CN", "jp": "ja"}


def hl_for(lang: str) -> str:
    return GOOGLE_HL.get(lang, lang)


class Reporter:
    """收集断言结果，最后统一给结论。"""

    def __init__(self) -> None:
        self.failures: list[str] = []
        self.count = 0

    def check(self, ok: bool, label: str, detail: str = "") -> bool:
        self.count += 1
        mark = "✓" if ok else "✗"
        suffix = f"  （{detail}）" if detail else ""
        print(f"  {mark} {label}{suffix}")
        if not ok:
            self.failures.append(label)
        return ok

    def section(self, title: str) -> None:
        print(f"\n=== {title} ===")

    def verdict(self) -> int:
        print("\n" + "=" * 62)
        if self.failures:
            print(f"未通过 {len(self.failures)}/{self.count} 项：")
            for label in self.failures:
                print(f"  - {label}")
            return 1
        print(f"全部通过（{self.count} 项断言）")
        return 0


class Client:
    def __init__(self, base: str) -> None:
        self.base = base.rstrip("/")

    def request(self, path: str, body: dict | None = None, timeout: float = 25.0):
        data = json.dumps(body).encode() if body is not None else None
        request = urllib.request.Request(
            self.base + path,
            data=data,
            headers=CLIENT_HEADER,
            method="POST" if data is not None else "GET",
        )
        started = time.monotonic()
        try:
            with urllib.request.urlopen(request, timeout=timeout) as response:
                raw, code = response.read(), response.status
        except urllib.error.HTTPError as error:
            raw, code = error.read(), error.code
        # 连接被拒 / 超时 / 对端断开都算「够不着」，统一是 OSError 家族
        # （HTTPError 也是 OSError 子类，但上面已经先接住了）。
        except OSError as error:
            return 0, {"error": str(error)}, (time.monotonic() - started) * 1000
        try:
            payload = json.loads(raw) if raw else {}
        except ValueError:
            payload = {"error": raw[:120].decode("utf-8", "replace")}
        return code, payload, (time.monotonic() - started) * 1000

    # ------------------------------------------------------------ 便捷封装
    def state(self) -> dict:
        return self.request("/api/state")[1]

    def reachable(self) -> bool:
        """控制端口通不通。连不上时 request 也会返回一个带 error 的字典，
        不能拿它当「应用活着但没登录」，否则报错会把人带偏。"""
        code, _payload, _ms = self.request("/api/state", timeout=5)
        return code == 200

    def lang(self) -> str:
        return self.request("/api/i18n")[1].get("lang", "")

    def languages(self) -> list[str]:
        items = self.request("/api/i18n")[1].get("items") or []
        return [item.get("code", "") for item in items if item.get("code")]

    def theme(self) -> str:
        return self.request("/api/theme")[1].get("mode", "")

    def set_lang(self, code: str):
        return self.request("/api/i18n", {"lang": code})

    def set_theme(self, mode: str):
        return self.request("/api/theme", {"mode": mode})

    def tasks(self):
        code, payload, ms = self.request("/api/tasks")
        count = -1
        if code == 200:
            snapshot = payload.get("snapshot") or {}
            count = len(snapshot.get("tasks") or [])
        return code, payload.get("stale"), count, ms, payload.get("error", "")

    def probe(self) -> dict:
        """读主窗口（Google 页）DOM。导航期间桥接不可用，返回 {'down': 原因}。"""
        code, payload, _ = self.request("/api/eval", {"window": "session", "js": PROBE_JS}, timeout=12)
        if code == 200 and "result" in payload:
            return payload["result"]
        return {"down": (payload.get("error") or f"HTTP {code}")[:48]}

    def hl(self) -> str:
        url = (self.state().get("session") or {}).get("url") or ""
        return url.split("hl=")[-1] if "hl=" in url else ""


def wait_ready(client: Client, timeout: float = 60.0) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        state = client.state()
        watcher = state.get("watcher") or {}
        if state.get("ready") and watcher.get("loggedIn"):
            return True
        time.sleep(0.5)
    return False


def record_switch(client: Client, target: str, seconds: float):
    """切一次语言，同时高频采页面与数据通道，返回 (时间线, 请求记录, 切换耗时, HTTP 码)。"""
    timeline: list[tuple[float, dict]] = []
    requests: list[tuple[float, int, object, int, float]] = []
    stop = threading.Event()
    origin = [time.monotonic()]

    def sampler() -> None:
        while not stop.is_set():
            timeline.append((time.monotonic() - origin[0], client.probe()))
            time.sleep(0.04)

    def hammer() -> None:
        while not stop.is_set():
            started = time.monotonic()
            code, stale, count, ms, _error = client.tasks()
            requests.append((started - origin[0], code, stale, count, ms))
            time.sleep(0.1)

    threads = [
        threading.Thread(target=sampler, daemon=True),
        threading.Thread(target=hammer, daemon=True),
    ]
    for thread in threads:
        thread.start()
    time.sleep(0.8)

    origin[0] = time.monotonic()
    code, _payload, switch_ms = client.set_lang(target)
    time.sleep(seconds)
    stop.set()
    for thread in threads:
        thread.join(timeout=2)
    return timeline, requests, switch_ms, code


def print_timeline(timeline: list[tuple[float, dict]]) -> None:
    """只打印状态发生变化的采样点，方便肉眼看节奏。"""
    last = None
    for offset, sample in timeline:
        if "down" in sample:
            key = ("down", sample["down"])
        else:
            key = (
                sample["splash"],
                sample["hiding"],
                sample["hl"],
                sample["shell"],
                sample["langButton"],
            )
        if key == last:
            continue
        last = key
        if "down" in sample:
            print(f"    +{offset:5.2f}s  桥接不可用: {sample['down']}")
            continue
        splash = "无" if not sample["splash"] else ("淡出中" if sample["hiding"] else "有")
        print(
            f"    +{offset:5.2f}s  遮罩={splash:4s} 遮罩底色={sample['splashBg'] or '-':20s}"
            f" body底色={sample['bodyBg']:20s} hl={sample['hl']:6s}"
            f" 首屏={'出' if sample['shell'] else '未'} 按钮={sample['langButton']!r}"
        )


def check_data_channel(rep: Reporter, requests: list, label: str, baseline: int) -> None:
    """重载期间数据通道（/api/tasks，工作台与脚本共用）的表现：不许卡住。

    baseline 是跑之前的任务条数：整页重载期间桥接不可用，允许走存货（stale），
    但不允许非 200、不允许卡超过 3 秒。
    """
    bad = [row for row in requests if row[1] != 200]
    slow = [row for row in requests if row[4] > 3000]
    stale = [row for row in requests if row[2]]
    rep.check(
        not bad,
        f"{label}: 数据通道全程有响应（{len(requests)} 次请求，非 200 {len(bad)} 次）",
        str(bad[0]) if bad else f"其中 stale 垫了 {len(stale)} 次",
    )
    rep.check(
        not slow,
        f"{label}: 没有卡住的请求（>3s {len(slow)} 次）",
        f"最慢 {max(row[4] for row in requests):.0f}ms" if requests else "",
    )


def phase_same_lang(client: Client, rep: Reporter, lang: str) -> None:
    rep.section(f"A 同语言重选（{lang} → {lang}）：不该有任何重载")
    before_hl = client.hl()
    code, _payload, ms = client.set_lang(lang)
    rep.check(code == 200 and ms < 500, "切换请求立刻返回", f"HTTP {code}，{ms:.0f}ms")
    down = 0
    for _ in range(10):
        time.sleep(0.4)
        if "down" in client.probe():
            down += 1
    rep.check(down == 0, "页面没有被重载（桥接全程可用）", f"不可用采样 {down} 次")
    rep.check(client.hl() == before_hl, "hl 参数没变", f"{before_hl} → {client.hl()}")


def phase_theme(client: Client, rep: Reporter, mode: str, label: str) -> None:
    rep.section(f"{label} 切主题到 {mode}：不重载、即时生效")
    want_dark = mode == "dark"
    before_hl = client.hl()
    code, _payload, ms = client.set_theme(mode)
    rep.check(code == 200 and ms < 500, "切换请求立刻返回", f"HTTP {code}，{ms:.0f}ms")

    applied = None
    down = 0
    splash = 0
    started = time.monotonic()
    while time.monotonic() - started < 5:
        time.sleep(0.15)
        sample = client.probe()
        if "down" in sample:
            down += 1
            continue
        if sample["splash"]:
            splash += 1
        if applied is None and sample["dark"] is want_dark:
            applied = time.monotonic() - started
    rep.check(down == 0, "页面没有被重载（桥接全程可用）", f"不可用采样 {down} 次")
    rep.check(splash == 0, "切主题不该出现启动遮罩", f"遮罩采样 {splash} 次")
    rep.check(
        applied is not None and applied < 2.0,
        "页面配色 2 秒内跟上（prefers-color-scheme 即时重算）",
        f"+{applied:.2f}s" if applied is not None else "5 秒内没跟上",
    )
    rep.check(client.hl() == before_hl, "hl 参数没变", f"{before_hl} → {client.hl()}")


def phase_visible(
    client: Client,
    rep: Reporter,
    target: str,
    label: str,
    baseline: int,
    seconds: float = 10.0,
) -> None:
    rep.section(f"{label} 切语言（→ {target}）：当场重载，遮罩盖住全程")
    timeline, requests, switch_ms, code = record_switch(client, target, seconds)
    rep.check(
        code == 200 and switch_ms < 500,
        "切换请求立刻返回",
        f"HTTP {code}，{switch_ms:.0f}ms",
    )
    print_timeline(timeline)

    samples = [sample for _offset, sample in timeline if "down" not in sample]
    splash_samples = [sample for sample in samples if sample["splash"]]
    rep.check(
        any("down" in sample for _o, sample in timeline),
        "确实发生了整页重载（桥接短暂不可用）",
    )
    rep.check(
        bool(splash_samples),
        "启动遮罩接管了重载空档",
        f"命中 {len(splash_samples)} 个采样点",
    )
    if splash_samples:
        mismatched = [s for s in splash_samples if s["splashBg"] and s["splashBg"] != s["bodyBg"]]
        rep.check(
            not mismatched,
            "遮罩底色与页面底色一致（不闪白）",
            f"{mismatched[0]['splashBg']} vs {mismatched[0]['bodyBg']}"
            if mismatched
            else splash_samples[0]["splashBg"],
        )
        print(f"      遮罩文案: {splash_samples[0]['splashText']!r}")
    if samples:
        final = samples[-1]
        rep.check(not final["splash"], "遮罩最终自己退场了")
        rep.check(final["hl"] == hl_for(target), "页面落到目标语言", f"hl={final['hl']}")
        rep.check(final["shell"], "Google 首屏已经渲染出来")
        rep.check(bool(final["langButton"]), "注入的语言按钮还在且有文案", final["langButton"])
    check_data_channel(rep, requests, label, baseline)


def pick_other(codes: list[str], current: str) -> str:
    for preferred in ("en", "zh", "jp"):
        if preferred in codes and preferred != current:
            return preferred
    for code in codes:
        if code != current:
            return code
    return ""


def main() -> int:
    parser = argparse.ArgumentParser(description="语言 / 主题切换回归")
    parser.add_argument("--api", default=DEFAULT_API, help=f"控制端口地址（默认 {DEFAULT_API}）")
    parser.add_argument("--quick", action="store_true", help="跳过浅色主题那一轮")
    parser.add_argument("--no-restore", action="store_true", help="跑完不还原语言 / 主题")
    args = parser.parse_args()

    client = Client(args.api)
    rep = Reporter()

    print(f"控制端口: {args.api}")
    if not wait_ready(client, timeout=20):
        if not client.reachable():
            print(f"连不上 {args.api}：应用没在跑。先启动再跑本脚本：")
            print("  ./build/macos-bundle.sh /tmp/GTasks.app && open /tmp/GTasks.app")
            return 2
        watcher = client.state().get("watcher") or {}
        print(
            "应用活着，但页面还没就绪 / 未登录：先在窗口里登录 Google 再跑。",
            json.dumps(watcher, ensure_ascii=False),
        )
        return 2

    codes = client.languages()
    start_lang = client.lang()
    start_theme = client.theme()
    baseline = client.tasks()[2]
    other = pick_other(codes, start_lang)
    if not other:
        print("词表里只有一门语言，没法切。往 internal/i18n/locales/ 再加一个 toml。")
        return 2
    print(f"已登录，词表 {codes}，当前 lang={start_lang} theme={start_theme}，任务 {baseline} 条；用 {other} 做对切")

    try:
        phase_same_lang(client, rep, start_lang)
        phase_theme(client, rep, "light" if start_theme == "dark" else "dark", "B")
        phase_visible(client, rep, other, "C", baseline, seconds=10)
        if not args.quick:
            client.set_theme("light")
            time.sleep(2)
            phase_visible(client, rep, start_lang, "D（浅色主题）", baseline, seconds=10)
    finally:
        if not args.no_restore:
            rep.section("还原现场")
            client.set_theme(start_theme)
            time.sleep(1.5)
            client.set_lang(start_lang)
            want_hl = hl_for(start_lang)
            deadline = time.monotonic() + 20
            settled = False
            while time.monotonic() < deadline:
                if client.hl() == want_hl:
                    settled = True
                    break
                time.sleep(0.5)
            print(f"  theme={client.theme()} lang={client.lang()} hl={client.hl()} tasks={client.tasks()[0]}")
            if not settled:
                print(f"  注：20 秒内 hl 没落到 {want_hl}，请人工看一眼窗口")

    return rep.verdict()


if __name__ == "__main__":
    sys.exit(main())
