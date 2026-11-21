"""verify-lang-switch.py 的纯函数单测（code-hooks 的 python:test 阶段跑 pytest）。

回归脚本本体要连着正在运行的应用才能跑，这里只覆盖不需要控制端口的部分：
语言码到 Google hl 参数的映射，以及 Reporter 的计数与结论。
脚本文件名带连字符、不能直接 import，所以按路径加载。
"""

from __future__ import annotations

import importlib.util
import pathlib

SCRIPT_PATH = pathlib.Path(__file__).with_name("verify-lang-switch.py")


def _load_script():
    spec = importlib.util.spec_from_file_location("verify_lang_switch", SCRIPT_PATH)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    # 模块名不是 __main__，脚本尾部的 sys.exit(main()) 不会执行。
    spec.loader.exec_module(module)
    return module


script = _load_script()


def test_hl_for_maps_known_languages() -> None:
    assert script.hl_for("zh") == "zh-CN"
    assert script.hl_for("jp") == "ja"


def test_hl_for_passes_other_languages_through() -> None:
    # 与 main.go 的 googleHL 一致：没有专门映射的语言原样传给 hl。
    assert script.hl_for("en") == "en"
    assert script.hl_for("de") == "de"


def test_reporter_verdict_is_zero_only_when_everything_passed(capsys) -> None:
    clean = script.Reporter()
    assert clean.check(True, "通过项") is True
    assert clean.verdict() == 0
    assert clean.count == 1

    broken = script.Reporter()
    broken.check(True, "通过项")
    assert broken.check(False, "失败项", "细节") is False
    assert broken.verdict() == 1
    assert broken.count == 2
    assert broken.failures == ["失败项"]
    assert "失败项" in capsys.readouterr().out
