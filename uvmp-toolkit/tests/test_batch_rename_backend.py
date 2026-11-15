# -*- coding: utf-8 -*-
"""batch_rename 后端测试：app.json 声明、注册器加载、preview 预检摘要、run 逐项结果、
进度与日志、取消与不覆盖。全部使用临时目录与 Job 桩，不触碰真实用户数据。
"""

import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
TOOLKIT = ROOT / "toolkit"
for _p in (str(TOOLKIT), str(ROOT)):
    if _p not in sys.path:
        sys.path.insert(0, _p)

import apps  # noqa: E402
from fileops import naming  # noqa: E402


class FakeJob:
    def __init__(self, cancel_after=None):
        self.lines = []
        self.progress = []
        self.cancel_after = cancel_after
        self.calls = 0

    def log(self, msg):
        self.lines.append(str(msg))

    def set_progress(self, current, total, label=""):
        self.progress.append((current, total, label))

    def check_cancel(self):
        self.calls += 1
        if self.cancel_after is not None and self.calls > self.cancel_after:
            raise __import__("jobs").Cancelled()

    def text(self):
        return "\n".join(self.lines)


def load_backend():
    meta = next((a for a in apps.scan_apps() if a["id"] == "batch_rename"), None)
    if meta is None or "_backend" not in meta:
        raise AssertionError("批量重命名应用未注册")
    return meta["_backend"]


class BatchRenameAppTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory(ignore_cleanup_errors=True)
        self.addCleanup(self._tmp.cleanup)
        self.dir = Path(self._tmp.name)
        env = mock.patch.dict(
            os.environ,
            {"LOCALAPPDATA": self._tmp.name, "XDG_DATA_HOME": self._tmp.name},
        )
        env.start()
        self.addCleanup(env.stop)
        self.backend = load_backend()

    def make(self, name, content=""):
        path = self.dir / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")
        return path

    def names(self):
        return sorted(os.listdir(self.dir))

    def test_app_json声明完整且后端已加载(self):
        meta = next(a for a in apps.scan_apps() if a["id"] == "batch_rename")
        self.assertEqual("batch_rename", meta["id"])
        self.assertEqual("批量重命名", meta["name"])
        self.assertTrue(meta["badge"])
        self.assertTrue(meta["desc"])
        self.assertIsInstance(meta["order"], int)
        self.assertTrue(callable(meta["_backend"].preview))
        self.assertTrue(callable(meta["_backend"].run))

    def test_app_json是合法中文utf8(self):
        raw = (TOOLKIT / "apps" / "batch_rename" / "app.json").read_text(
            encoding="utf-8"
        )
        meta = json.loads(raw)
        self.assertEqual("batch_rename", meta["id"])

    def test_预览返回摘要且不改动磁盘(self):
        self.make("图10.jpg")
        self.make("图2.jpg")
        self.make("子目录/内层.jpg")
        built = self.backend.preview({"directory": str(self.dir), "prefix": "x_"})
        for key in ("rows", "can", "skip", "same", "warnings"):
            self.assertIn(key, built)
        self.assertEqual(2, built["can"])
        self.assertEqual(0, built["skip"])
        self.assertEqual(
            ["x_图2.jpg", "x_图10.jpg"], [r["new_name"] for r in built["rows"]]
        )
        # 预览只读：磁盘保持原样
        self.assertEqual(["图10.jpg", "图2.jpg", "子目录"], self.names())

    def test_预览汇总冲突与非法项(self):
        self.make("a.txt", "A")
        self.make("b.txt", "B")
        built = self.backend.preview(
            {
                "files": [str(self.dir / "a.txt"), str(self.dir / "b.txt")],
                "replace": [
                    {"find": "a", "replace": "same"},
                    {"find": "b", "replace": "same"},
                ],
            }
        )
        # 默认跳过模式：首项占据目标名，后者判互撞跳过
        self.assertEqual(1, built["can"])
        self.assertEqual(1, built["skip"])
        self.assertTrue(built["warnings"])

    def test_空参数预览不抛异常(self):
        built = self.backend.preview({})
        self.assertEqual([], built["rows"])
        self.assertTrue(built["warnings"])

    def test_执行逐项日志进度与计数(self):
        self.make("a.txt", "A")
        self.make("b.txt", "B")
        job = FakeJob()
        result = self.backend.run(job, {"directory": str(self.dir), "prefix": "x_"})
        self.assertEqual(2, result["success"])
        self.assertEqual(0, result["failed"])
        self.assertEqual(0, result["skipped"])
        self.assertEqual(0, result["unchanged"])
        self.assertEqual(str(self.dir), result["output_dir"])
        self.assertEqual(["x_a.txt", "x_b.txt"], self.names())
        self.assertEqual(
            2, len([r for r in result["rows"] if r["result"] == "success"])
        )
        self.assertTrue(any("x_a.txt" in line for line in job.lines))
        self.assertEqual(2, job.progress[-1][1])

    def test_执行跳过磁盘同名且不覆盖(self):
        self.make("a.txt", "新")
        self.make("b.txt", "旧")
        job = FakeJob()
        result = self.backend.run(
            job,
            {
                "files": [str(self.dir / "a.txt")],
                "replace": [{"find": "a", "replace": "b"}],
            },
        )
        self.assertEqual(0, result["success"])
        self.assertEqual(1, result["skipped"])
        self.assertEqual("旧", (self.dir / "b.txt").read_text(encoding="utf-8"))
        self.assertTrue(any("跳过" in line for line in job.lines))

    def test_冲突追加序号模式执行成功(self):
        self.make("a.txt", "新")
        self.make("b.txt", "旧")
        job = FakeJob()
        result = self.backend.run(
            job,
            {
                "files": [str(self.dir / "a.txt")],
                "replace": [{"find": "a", "replace": "b"}],
                "conflict": "suffix",
            },
        )
        self.assertEqual(1, result["success"])
        self.assertEqual(["b (2).txt", "b.txt"], sorted(os.listdir(self.dir)))

    def test_取消返回部分结果并标注未执行(self):
        self.make("a.txt", "A")
        self.make("b.txt", "B")
        self.make("c.txt", "C")
        job = FakeJob(cancel_after=1)
        result = self.backend.run(job, {"directory": str(self.dir), "prefix": "x_"})
        self.assertTrue(result["cancelled"])
        self.assertEqual(1, result["success"])
        self.assertEqual(2, result["not_run"])
        self.assertIn("任务已取消", job.text())

    def test_循环互换取消不覆盖且标注未执行(self):
        self.make("n_1.txt", "A")
        self.make("n_2.txt", "B")
        job = FakeJob(cancel_after=0)  # 第一个组边界就取消
        result = self.backend.run(
            job,
            {
                "files": [str(self.dir / "n_1.txt"), str(self.dir / "n_2.txt")],
                "sequence": {
                    "enabled": True,
                    "base": "n",
                    "start": 2,
                    "step": -1,
                    "digits": 1,
                },
            },
        )
        self.assertTrue(result["cancelled"])
        self.assertEqual(0, result["success"])
        self.assertEqual(2, result["not_run"])
        self.assertEqual("A", (self.dir / "n_1.txt").read_text(encoding="utf-8"))
        self.assertEqual("B", (self.dir / "n_2.txt").read_text(encoding="utf-8"))
        self.assertEqual(["n_1.txt", "n_2.txt"], self.names())
        self.assertTrue(any("未执行" in line for line in job.lines))

    def test_正常完成进度按总数拉满(self):
        self.make("a.txt", "A")
        self.make("b.txt", "旧")
        job = FakeJob()
        result = self.backend.run(
            job,
            {
                "files": [str(self.dir / "a.txt")],
                "replace": [{"find": "a", "replace": "b"}],
            },
        )
        self.assertEqual(0, result["success"])
        self.assertEqual(1, result["skipped"])
        current, total, label = job.progress[-1]
        self.assertEqual((1, 1), (current, total))
        self.assertEqual("完成", label)

    def test_取消时日志进度用实际已处理数(self):
        self.make("a.txt", "A")
        self.make("b.txt", "B")
        self.make("c.txt", "C")
        job = FakeJob(cancel_after=2)  # 第三个单元边界取消
        real = naming._rename_no_replace
        calls = {"n": 0}

        def wrapper(src, dst):
            calls["n"] += 1
            if calls["n"] == 2:
                raise OSError(13, "模拟失败")
            return real(src, dst)

        with mock.patch.object(naming, "_rename_no_replace", side_effect=wrapper):
            result = self.backend.run(job, {"directory": str(self.dir), "prefix": "x_"})
        self.assertTrue(result["cancelled"])
        self.assertEqual(1, result["success"])
        self.assertEqual(1, result["failed"])
        self.assertEqual(1, result["not_run"])
        # 已处理 1 成功 + 1 失败 = 2 项，不能只报 success
        self.assertTrue(
            any("进度停在 2/3" in line for line in job.lines),
            "\n".join(job.lines),
        )

    def test_取消后进度不拉满(self):
        self.make("a.txt", "A")
        self.make("b.txt", "B")
        self.make("c.txt", "C")
        job = FakeJob(cancel_after=1)
        self.backend.run(job, {"directory": str(self.dir), "prefix": "x_"})
        current, total, _label = job.progress[-1]
        self.assertEqual(3, total)
        self.assertEqual(1, current)
        self.assertLess(current, total)

    def test_多目录时输出目录留空(self):
        other = self.dir / "另一处"
        other.mkdir()
        self.make("a.txt")
        (other / "b.txt").write_text("B", encoding="utf-8")
        job = FakeJob()
        result = self.backend.run(
            job,
            {
                "files": [str(self.dir / "a.txt"), str(other / "b.txt")],
                "prefix": "x_",
            },
        )
        self.assertEqual(2, result["success"])
        self.assertEqual("", result["output_dir"])


if __name__ == "__main__":
    unittest.main()
