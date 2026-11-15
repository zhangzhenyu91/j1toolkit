# -*- coding: utf-8 -*-
"""通用改名引擎 fileops.naming 单元测试。

覆盖：自然排序、非法名与 Windows 保留名、目标互撞、磁盘已有同名、循环改名、
仅大小写变化、连续序号、取消与部分失败。全部在临时目录内完成，不触碰真实数据。
"""

import os
import random
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
for _p in (str(ROOT), str(ROOT / "toolkit")):
    if _p not in sys.path:
        sys.path.insert(0, _p)

from fileops import naming  # noqa: E402
from jobs import Cancelled  # noqa: E402


class FakeJob:
    """最小 Job 桩：记录日志/进度，按次数触发协作取消。"""

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
            raise Cancelled()


class TempDirMixin:
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory(ignore_cleanup_errors=True)
        self.addCleanup(self._tmp.cleanup)
        self.dir = Path(self._tmp.name)

    def make(self, name, content=""):
        path = self.dir / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")
        return path

    def names(self):
        return sorted(os.listdir(self.dir))

    def read(self, name):
        return (self.dir / name).read_text(encoding="utf-8")


def _case_insensitive_fs(directory) -> bool:
    """探针：仅大小写不同的两个名字是否指向同一个文件（Windows 为 True，POSIX 为 False）。

    直接问文件系统（``os.path.samefile``），不借用产品里的身份判定，避免「被测函数
    自己判断错了，测试也跟着错」。探针文件当场删除，不留进用例的目录清单。
    """
    directory = Path(directory)
    probe = directory / "case-probe.tmp"
    probe.write_text("probe", encoding="utf-8")
    try:
        return os.path.samefile(str(probe), str(directory / "CASE-PROBE.TMP"))
    except OSError:
        return False
    finally:
        probe.unlink()


def _identity(path) -> tuple:
    """文件身份（设备号 + inode）：改名只是换个名字，文件本身不变。"""
    stat = os.stat(str(path))
    return (stat.st_dev, stat.st_ino)


class NaturalKeyTest(unittest.TestCase):
    def test_自然排序按数字大小而非字典序(self):
        names = ["图10.jpg", "图2.jpg", "图1.jpg", "a.jpg"]
        self.assertEqual(
            ["a.jpg", "图1.jpg", "图2.jpg", "图10.jpg"],
            sorted(names, key=naming.natural_key),
        )

    def test_自然排序忽略大小写且稳定(self):
        names = ["b2.txt", "B10.txt", "b1.txt"]
        self.assertEqual(
            ["b1.txt", "b2.txt", "B10.txt"],
            sorted(names, key=naming.natural_key),
        )


class InvalidNameTest(unittest.TestCase):
    def test_非法字符与空名被拒(self):
        bad = [
            "",
            "   ",
            "a/b",
            "a\\b",
            "a:b",
            "a*b",
            "a?b",
            'a"b',
            "a<b",
            "a>b",
            "a|b",
            "a\x00b",
            "a\x1fb",
            " trailing",
            "trailing ",
            "trailing.",
            ".",
            "..",
            "长" * 300,
        ]
        for name in bad:
            with self.subTest(name=name):
                self.assertTrue(naming.invalid_reason(name))

    def test_windows保留名不区分大小写且含扩展名(self):
        for name in ["CON", "con", "Con.txt", "prn.log", "AUX", "nul", "COM1", "lpt9"]:
            with self.subTest(name=name):
                self.assertTrue(naming.invalid_reason(name))

    def test_普通名称与相似名合法(self):
        for name in [
            "a.txt",
            "console.txt",
            "com0.txt",
            "con1",
            "照片_001.jpg",
            "a.b.c",
        ]:
            with self.subTest(name=name):
                self.assertEqual("", naming.invalid_reason(name))


class PlanTest(TempDirMixin, unittest.TestCase):
    def test_目录只展开第一层并按自然序(self):
        self.make("图10.jpg")
        self.make("图2.jpg")
        self.make("图1.jpg")
        self.make("子目录/内层.jpg")
        built = naming.build_plan({"directory": str(self.dir), "prefix": "x_"})
        self.assertEqual(3, len(built["rows"]))
        self.assertEqual(
            ["图1.jpg", "图2.jpg", "图10.jpg"],
            [row["name"] for row in built["rows"]],
        )
        self.assertEqual(3, built["can"])

    def test_空参数给出提示且无行(self):
        built = naming.build_plan({})
        self.assertEqual([], built["rows"])
        self.assertEqual(0, built["can"])
        self.assertTrue(built["warnings"])

    def test_查找替换前后缀与保留扩展名(self):
        self.make("photo.txt")
        built = naming.build_plan(
            {
                "files": [str(self.dir / "photo.txt")],
                "replace": [{"find": "ph", "replace": "PH"}],
                "prefix": "P_",
                "suffix": "_S",
            }
        )
        self.assertEqual("P_PHoto_S.txt", built["rows"][0]["new_name"])

    def test_关闭保留扩展名后新名不含扩展名(self):
        self.make("a.txt")
        built = naming.build_plan(
            {
                "files": [str(self.dir / "a.txt")],
                "prefix": "x_",
                "keep_extension": False,
            }
        )
        self.assertEqual("x_a", built["rows"][0]["new_name"])

    def test_页面传来的字符串假值按假处理(self):
        self.make("a.txt")
        built = naming.build_plan(
            {
                "files": [str(self.dir / "a.txt")],
                "prefix": "x_",
                "keep_extension": "false",
                "sequence": {"enabled": "0", "base": "b"},
            }
        )
        self.assertEqual("x_a", built["rows"][0]["new_name"])

    def test_单组替换规则也接受(self):
        self.make("a.txt")
        built = naming.build_plan(
            {
                "files": [str(self.dir / "a.txt")],
                "replace": {"find": "a", "replace": "b"},
            }
        )
        self.assertEqual("b.txt", built["rows"][0]["new_name"])

    def test_替换区分大小写开关(self):
        self.make("Photo.txt")
        same_case = naming.build_plan(
            {
                "files": [str(self.dir / "Photo.txt")],
                "replace": [
                    {"find": "photo", "replace": "IMG", "case_sensitive": True}
                ],
            }
        )
        self.assertEqual("Photo.txt", same_case["rows"][0]["new_name"])
        self.assertEqual(1, same_case["same"])
        ignore_case = naming.build_plan(
            {
                "files": [str(self.dir / "Photo.txt")],
                "replace": [{"find": "photo", "replace": "IMG"}],
            }
        )
        self.assertEqual("IMG.txt", ignore_case["rows"][0]["new_name"])

    def test_替换规则按顺序依次作用(self):
        self.make("a.txt")
        built = naming.build_plan(
            {
                "files": [str(self.dir / "a.txt")],
                "replace": [
                    {"find": "a", "replace": "b"},
                    {"find": "b", "replace": "c"},
                ],
            }
        )
        self.assertEqual("c.txt", built["rows"][0]["new_name"])

    def test_目标为同批未变化文件时让位(self):
        self.make("a.txt", "A")
        self.make("b.txt", "B")
        built = naming.build_plan(
            {
                "files": [str(self.dir / "a.txt"), str(self.dir / "b.txt")],
                # b 保持不变，a 想去占 b 的名字
                "replace": [{"find": "a", "replace": "b"}],
            }
        )
        self.assertEqual(["skip", "same"], [r["status"] for r in built["rows"]])
        self.assertIn("已存在", built["rows"][0]["reason"])
        naming.apply_plan(built["rows"])
        self.assertEqual("A", self.read("a.txt"))
        self.assertEqual("B", self.read("b.txt"))

    def test_长链让位逐层收敛到稳定(self):
        """f0→f1→f2→f3→f4 五项链，f4 未变化：让位必须传到链首 f0。

        rows 形状与 build_plan 的输出一致（同一目录、同批文件、末项 same）；
        固定 3 轮的实现只让位 3 层，f0 仍为 ok，执行时撞上仍在磁盘的 f1 报「目标已存在」。
        """
        for i in range(5):
            self.make("f%d.txt" % i, "F%d" % i)
        rows = [
            {
                "path": str(self.dir / ("f%d.txt" % i)),
                "name": "f%d.txt" % i,
                "new_name": "f%d.txt" % (i + 1) if i < 4 else "f4.txt",
                "status": "same" if i == 4 else "ok",
                "reason": "",
                "flags": [],
            }
            for i in range(5)
        ]
        naming._settle_held_targets(rows, {"conflict": "skip"})
        statuses = [row["status"] for row in rows]
        # 预检不再给可执行项：链首 f0 也必须让位（f4 本身未变化）
        self.assertEqual(["skip", "skip", "skip", "skip", "same"], statuses)
        outcome = naming.apply_plan(rows)
        self.assertEqual(0, outcome["failed"], outcome["rows"])
        self.assertEqual(0, outcome["success"], outcome["rows"])
        for i in range(5):
            self.assertEqual("F%d" % i, self.read("f%d.txt" % i))

    def test_序号整体后移且磁盘已有尾号时全部让位(self):
        """文件_001..010 统一改为 start=2 的序号，磁盘另有 011：链尾占用必须一路传到链首。"""
        for i in range(1, 11):
            self.make("文件_%03d.jpg" % i, "I%03d" % i)
        self.make("文件_011.jpg", "I011")
        files = [str(self.dir / ("文件_%03d.jpg" % i)) for i in range(1, 11)]
        before = {name: (self.dir / name).read_bytes() for name in self.names()}
        built = naming.build_plan(
            {
                "files": files,
                "sequence": {
                    "enabled": True,
                    "base": "文件",
                    "start": 2,
                    "step": 1,
                    "digits": 3,
                },
            }
        )
        self.assertEqual(0, built["can"], [row["status"] for row in built["rows"]])
        outcome = naming.apply_plan(built["rows"])
        self.assertEqual(0, outcome["failed"], outcome["rows"])
        self.assertEqual(0, outcome["success"], outcome["rows"])
        after = {name: (self.dir / name).read_bytes() for name in self.names()}
        self.assertEqual(before, after)

    def test_让位跳过项不占序号且重算后无空号(self):
        """R2：B_001 未变化，后续项的目标都落在它身上，重算后不应再产出 B_004 之类的空号。

        设计稿 assignNames + markConflicts + buildPreview 的口径：可提交项连续编号，
        非法/未变化/冲突排除项不占号，排除后重算到不再新增排除项为止。
        """
        names = ["B_001.txt", "B_002.txt", "B_003.txt", "B_009.txt", "B_020.txt"]
        for name in names:
            self.make(name, name)
        before = {name: self.read(name) for name in self.names()}
        built = naming.build_plan(
            {
                "directory": str(self.dir),
                "sequence": {
                    "enabled": True,
                    "base": "B",
                    "start": 1,
                    "step": 1,
                    "digits": 3,
                },
            }
        )
        rows = {row["name"]: row for row in built["rows"]}
        self.assertEqual("same", rows["B_001.txt"]["status"])
        for name in names[1:]:
            with self.subTest(name=name):
                self.assertEqual("skip", rows[name]["status"])
                self.assertIn("已存在", rows[name]["reason"])
                # 让位项保持原名（不占号），尝试过的目标名只能是本批仍占用的名字
                self.assertIn(rows[name]["new_name"], names)
                # 旧口径的痕迹：唯一成品拿到 B_004，前三个号成空洞
                self.assertNotEqual("B_004.txt", rows[name]["new_name"])
        self.assertEqual(0, built["can"])
        self.assertEqual(4, built["skip"])
        self.assertEqual(1, built["same"])
        outcome = naming.apply_plan(built["rows"])
        self.assertEqual(0, outcome["success"], outcome["rows"])
        self.assertEqual(before, {name: self.read(name) for name in self.names()})

    def test_追加序号模式让位保留名(self):
        """a 想改名为 b，b 未变化：suffix 模式让位到 b (2)，绝不覆盖 b。"""
        self.make("a.txt", "A")
        self.make("b.txt", "B")
        built = naming.build_plan(
            {
                "files": [str(self.dir / "a.txt"), str(self.dir / "b.txt")],
                "replace": [{"find": "a", "replace": "b"}],
                "conflict": "suffix",
            }
        )
        self.assertEqual(["b (2).txt", "b.txt"], [r["new_name"] for r in built["rows"]])
        self.assertEqual(["ok", "same"], [r["status"] for r in built["rows"]])
        outcome = naming.apply_plan(built["rows"])
        self.assertEqual(1, outcome["success"], outcome["rows"])
        self.assertEqual(["b (2).txt", "b.txt"], self.names())
        self.assertEqual("A", self.read("b (2).txt"))
        self.assertEqual("B", self.read("b.txt"))

    def test_追加序号模式让位同时避开磁盘已有序号(self):
        """b 保留原名、磁盘已有 b (2)：a 必须继续后移到 b (3)。"""
        self.make("a.txt", "A")
        self.make("b.txt", "B")
        self.make("b (2).txt", "旧2")
        built = naming.build_plan(
            {
                "files": [str(self.dir / "a.txt"), str(self.dir / "b.txt")],
                "replace": [{"find": "a", "replace": "b"}],
                "conflict": "suffix",
            }
        )
        self.assertEqual("b (3).txt", built["rows"][0]["new_name"])
        self.assertEqual("ok", built["rows"][0]["status"])
        self.assertEqual("旧2", self.read("b (2).txt"))

    def test_跨目录让位后其他目录可提交项仍连续编号(self):
        """一/ 里的链让位全部跳过，二/ 的可提交项仍从 001 连续编号，不留空号。"""
        first = self.dir / "一"
        second = self.dir / "二"
        first.mkdir()
        second.mkdir()
        for name in ("X_001.txt", "X_002.txt", "X_003.txt"):
            (first / name).write_text(name, encoding="utf-8")
        for name in ("a.txt", "b.txt"):
            (second / name).write_text(name, encoding="utf-8")
        built = naming.build_plan(
            {
                "files": [
                    str(first / name)
                    for name in ("X_001.txt", "X_002.txt", "X_003.txt")
                ]
                + [str(second / "a.txt"), str(second / "b.txt")],
                "sequence": {
                    "enabled": True,
                    "base": "X",
                    "start": 1,
                    "step": 1,
                    "digits": 3,
                },
            }
        )
        rows = built["rows"]
        self.assertEqual(["same", "skip", "skip"], [r["status"] for r in rows[:3]])
        self.assertEqual(["ok", "ok"], [r["status"] for r in rows[3:]])
        self.assertEqual(["X_001.txt", "X_002.txt"], [r["new_name"] for r in rows[3:]])
        self.assertEqual(2, built["can"])
        outcome = naming.apply_plan(rows)
        self.assertEqual(2, outcome["success"], outcome["rows"])
        self.assertEqual(
            ["X_001.txt", "X_002.txt", "X_003.txt"], sorted(os.listdir(first))
        )
        self.assertEqual(["X_001.txt", "X_002.txt"], sorted(os.listdir(second)))
        self.assertEqual("a.txt", (second / "X_001.txt").read_text(encoding="utf-8"))
        self.assertEqual("b.txt", (second / "X_002.txt").read_text(encoding="utf-8"))

    def test_同目录混合扩展名让位后其他可提交项仍连续编号(self):
        """txt 链全部让位后，同目录的 jpg 仍从 001 起编，不继承让位项消耗过的号。"""
        for name in ("B_001.txt", "B_002.txt", "B_003.txt", "B_004.jpg"):
            self.make(name, name)
        built = naming.build_plan(
            {
                "files": [
                    str(self.dir / "B_001.txt"),
                    str(self.dir / "B_002.txt"),
                    str(self.dir / "B_003.txt"),
                    str(self.dir / "B_004.jpg"),
                ],
                "sequence": {
                    "enabled": True,
                    "base": "B",
                    "start": 1,
                    "step": 1,
                    "digits": 3,
                },
            }
        )
        rows = built["rows"]
        self.assertEqual(["same", "skip", "skip", "ok"], [r["status"] for r in rows])
        self.assertEqual(
            ["B_001.txt", "B_001.txt", "B_002.txt", "B_001.jpg"],
            [r["new_name"] for r in rows],
        )
        self.assertEqual(1, built["can"])
        outcome = naming.apply_plan(rows)
        self.assertEqual(1, outcome["success"], outcome["rows"])
        self.assertEqual("B_004.jpg", self.read("B_001.jpg"))
        self.assertEqual(
            ["B_001.jpg", "B_001.txt", "B_002.txt", "B_003.txt"], self.names()
        )

    def test_不同目录的同名目标不算互撞(self):
        first = self.dir / "一"
        second = self.dir / "二"
        first.mkdir()
        second.mkdir()
        (first / "a.txt").write_text("A", encoding="utf-8")
        (second / "b.txt").write_text("B", encoding="utf-8")
        built = naming.build_plan(
            {
                "files": [str(first / "a.txt"), str(second / "b.txt")],
                "replace": [
                    {"find": "a", "replace": "same"},
                    {"find": "b", "replace": "same"},
                ],
            }
        )
        self.assertEqual(2, built["can"])
        self.assertEqual(0, built["skip"])
        naming.apply_plan(built["rows"])
        self.assertEqual(["same.txt"], sorted(os.listdir(first)))
        self.assertEqual(["same.txt"], sorted(os.listdir(second)))
        self.assertEqual("A", (first / "same.txt").read_text(encoding="utf-8"))
        self.assertEqual("B", (second / "same.txt").read_text(encoding="utf-8"))

    def test_目标互撞跳过模式后者跳过(self):
        self.make("a.txt")
        self.make("b.txt")
        built = naming.build_plan(
            {
                "files": [str(self.dir / "a.txt"), str(self.dir / "b.txt")],
                "replace": [
                    {"find": "a", "replace": "same"},
                    {"find": "b", "replace": "same"},
                ],
            }
        )
        self.assertEqual(1, built["can"])
        self.assertEqual(1, built["skip"])
        self.assertEqual(
            ["same.txt", "same.txt"], [r["new_name"] for r in built["rows"]]
        )
        self.assertEqual(["ok", "skip"], [r["status"] for r in built["rows"]])
        self.assertIn("互撞", built["rows"][1]["reason"])

    def test_目标互撞追加序号模式后者让位(self):
        self.make("a.txt")
        self.make("b.txt")
        built = naming.build_plan(
            {
                "files": [str(self.dir / "a.txt"), str(self.dir / "b.txt")],
                "replace": [
                    {"find": "a", "replace": "same"},
                    {"find": "b", "replace": "same"},
                ],
                "conflict": "suffix",
            }
        )
        self.assertEqual(2, built["can"])
        self.assertEqual(
            ["same.txt", "same (2).txt"], [r["new_name"] for r in built["rows"]]
        )

    def test_磁盘已有同名目标跳过且绝不覆盖(self):
        self.make("a.txt", "新")
        self.make("b.txt", "旧")
        built = naming.build_plan(
            {
                "files": [str(self.dir / "a.txt")],
                "replace": [{"find": "a", "replace": "b"}],
            }
        )
        row = built["rows"][0]
        self.assertEqual("skip", row["status"])
        self.assertIn("已存在", row["reason"])
        self.assertEqual(0, built["can"])

    def test_磁盘同名追加序号并跳过已占用的序号(self):
        self.make("a.txt", "新")
        self.make("b.txt", "旧")
        self.make("b (2).txt", "旧2")
        built = naming.build_plan(
            {
                "files": [str(self.dir / "a.txt")],
                "replace": [{"find": "a", "replace": "b"}],
                "conflict": "suffix",
            }
        )
        self.assertEqual("b (3).txt", built["rows"][0]["new_name"])

    def test_循环互换标记且可提交(self):
        self.make("n_1.txt", "A")
        self.make("n_2.txt", "B")
        built = naming.build_plan(
            {
                "files": [str(self.dir / "n_1.txt"), str(self.dir / "n_2.txt")],
                "sequence": {
                    "enabled": True,
                    "base": "n",
                    "start": 2,
                    "step": -1,
                    "digits": 1,
                },
            }
        )
        rows = built["rows"]
        self.assertEqual(["n_2.txt", "n_1.txt"], [r["new_name"] for r in rows])
        self.assertEqual(2, built["can"])
        for row in rows:
            self.assertIn("cycle", row["flags"])

    def test_统一序号同时套前后缀(self):
        self.make("a.txt")
        self.make("b.txt")
        built = naming.build_plan(
            {
                "directory": str(self.dir),
                "prefix": "P_",
                "suffix": "_S",
                "sequence": {
                    "enabled": True,
                    "base": "巡检",
                    "start": 1,
                    "step": 1,
                    "digits": 3,
                },
            }
        )
        self.assertEqual(
            ["P_巡检_001_S.txt", "P_巡检_002_S.txt"],
            [r["new_name"] for r in built["rows"]],
        )
        self.assertEqual(2, built["can"])

    def test_统一主名为空时用文件兜底(self):
        self.make("a.txt")
        built = naming.build_plan(
            {
                "directory": str(self.dir),
                "sequence": {
                    "enabled": True,
                    "base": "",
                    "start": 1,
                    "step": 1,
                    "digits": 3,
                },
            }
        )
        self.assertEqual("文件_001.txt", built["rows"][0]["new_name"])

    def test_跳过项不占序号后续仍连续(self):
        self.make("a.txt", "A")
        self.make("b.txt", "B")
        self.make("c.txt", "C")
        # 磁盘上已有 n_2：b 的目标被判磁盘同名跳过，号码不被占用，
        # 后续项的尝试名仍是 n_1 / n_2，不出现 n_3 断层。
        self.make("n_2.txt", "旧")
        built = naming.build_plan(
            {
                "files": [
                    str(self.dir / "a.txt"),
                    str(self.dir / "b.txt"),
                    str(self.dir / "c.txt"),
                ],
                "sequence": {
                    "enabled": True,
                    "base": "n",
                    "start": 1,
                    "step": 1,
                    "digits": 1,
                },
            }
        )
        rows = {row["name"]: row for row in built["rows"]}
        self.assertEqual("n_1.txt", rows["a.txt"]["new_name"])
        self.assertEqual("ok", rows["a.txt"]["status"])
        self.assertEqual("n_2.txt", rows["b.txt"]["new_name"])
        self.assertEqual("skip", rows["b.txt"]["status"])
        self.assertEqual("n_2.txt", rows["c.txt"]["new_name"])
        self.assertEqual("skip", rows["c.txt"]["status"])
        self.assertEqual(1, built["can"])

    def test_仅大小写变化标记且可提交(self):
        self.make("Photo.JPG")
        built = naming.build_plan(
            {
                "files": [str(self.dir / "Photo.JPG")],
                "replace": [{"find": "Photo", "replace": "photo"}],
            }
        )
        row = built["rows"][0]
        self.assertEqual("ok", row["status"])
        self.assertIn("case_only", row["flags"])
        self.assertEqual("photo.JPG", row["new_name"])

    def test_连续序号且缺文件不占号(self):
        self.make("a.txt")
        self.make("b.txt")
        self.make("c.txt")
        missing = self.dir / "不存在.txt"
        built = naming.build_plan(
            {
                "files": [
                    str(self.dir / "a.txt"),
                    str(self.dir / "b.txt"),
                    str(missing),
                    str(self.dir / "c.txt"),
                ],
                "sequence": {
                    "enabled": True,
                    "base": "巡检",
                    "start": 1,
                    "step": 1,
                    "digits": 3,
                },
            }
        )
        self.assertEqual(
            ["巡检_001.txt", "巡检_002.txt", "巡检_003.txt"],
            [r["new_name"] for r in built["rows"] if r["status"] == "ok"],
        )
        skipped = [r for r in built["rows"] if r["status"] == "skip"]
        self.assertEqual(1, len(skipped))
        self.assertIn("不存在", skipped[0]["reason"])

    def test_未变化项不占序号(self):
        self.make("巡检_001.txt")
        self.make("b.txt")
        built = naming.build_plan(
            {
                # 显式顺序：未变化项在前，序号 001 不应因它被跳过
                "files": [str(self.dir / "巡检_001.txt"), str(self.dir / "b.txt")],
                "sequence": {
                    "enabled": True,
                    "base": "巡检",
                    "start": 1,
                    "step": 1,
                    "digits": 3,
                },
            }
        )
        rows = {row["name"]: row for row in built["rows"]}
        self.assertEqual("same", rows["巡检_001.txt"]["status"])
        # 序号 001 未被未变化项占用，但该号已落在磁盘上的同名文件，故随后判为磁盘同名
        self.assertEqual("skip", rows["b.txt"]["status"])
        self.assertIn("已存在", rows["b.txt"]["reason"])

    def test_非法规则名全部跳过(self):
        self.make("a.txt")
        built = naming.build_plan({"files": [str(self.dir / "a.txt")], "prefix": "a/b"})
        self.assertEqual("skip", built["rows"][0]["status"])
        self.assertEqual(0, built["can"])
        self.assertEqual(["a.txt"], self.names())


class ApplyTest(TempDirMixin, unittest.TestCase):
    def test_直接改名并保留原目录(self):
        self.make("a.txt", "A")
        self.make("b.txt", "B")
        built = naming.build_plan({"directory": str(self.dir), "prefix": "x_"})
        job = FakeJob()
        outcome = naming.apply_plan(built["rows"], job=job)
        self.assertEqual(2, outcome["success"])
        self.assertEqual(["x_a.txt", "x_b.txt"], self.names())
        self.assertEqual("A", self.read("x_a.txt"))
        self.assertTrue(job.lines)
        self.assertTrue(job.progress)

    def test_循环互换走临时名且内容正确(self):
        self.make("n_1.txt", "A")
        self.make("n_2.txt", "B")
        built = naming.build_plan(
            {
                "directory": str(self.dir),
                "sequence": {
                    "enabled": True,
                    "base": "n",
                    "start": 2,
                    "step": -1,
                    "digits": 1,
                },
            }
        )
        outcome = naming.apply_plan(built["rows"])
        self.assertEqual(2, outcome["success"])
        self.assertEqual("B", self.read("n_1.txt"))
        self.assertEqual("A", self.read("n_2.txt"))
        self.assertEqual(["n_1.txt", "n_2.txt"], self.names())

    def test_仅大小写变化实际落盘(self):
        self.make("Photo.JPG", "P")
        built = naming.build_plan(
            {
                "directory": str(self.dir),
                "replace": [{"find": "Photo", "replace": "photo"}],
            }
        )
        outcome = naming.apply_plan(built["rows"])
        self.assertEqual(1, outcome["success"])
        self.assertIn("photo.JPG", self.names())
        self.assertEqual("P", self.read("photo.JPG"))

    def test_部分失败不阻塞其他项且不覆盖(self):
        self.make("a.txt", "A")
        self.make("b.txt", "B")
        self.make("c.txt", "C")
        built = naming.build_plan({"directory": str(self.dir), "prefix": "x_"})
        # 预检后目标被外部占用：提交时必须失败且保留原文件
        (self.dir / "x_b.txt").write_text("占用", encoding="utf-8")
        outcome = naming.apply_plan(built["rows"])
        self.assertEqual(2, outcome["success"])
        self.assertEqual(1, outcome["failed"])
        # 失败项保持原名 b.txt；预先占位的 x_b.txt 未被覆盖
        self.assertEqual(["b.txt", "x_a.txt", "x_b.txt", "x_c.txt"], self.names())
        self.assertEqual("占用", self.read("x_b.txt"))
        self.assertIn("b.txt", self.names())
        failed = [r for r in outcome["rows"] if r["result"] == "failed"]
        self.assertEqual("b.txt", failed[0]["name"])

    def test_取消保留已完成项并标注未执行(self):
        self.make("a.txt", "A")
        self.make("b.txt", "B")
        self.make("c.txt", "C")
        built = naming.build_plan({"directory": str(self.dir), "prefix": "x_"})
        outcome = naming.apply_plan(built["rows"], job=FakeJob(cancel_after=1))
        self.assertTrue(outcome["cancelled"])
        self.assertEqual(1, outcome["success"])
        self.assertEqual(2, outcome["not_run"])
        # 已完成项保留新名，未处理项保持原名
        self.assertEqual(["b.txt", "c.txt", "x_a.txt"], self.names())
        self.assertEqual("A", self.read("x_a.txt"))
        self.assertEqual(
            ["success", "not_run", "not_run"],
            [r["result"] for r in outcome["rows"]],
        )

    def _fail_on_rename_call(self, nth, on_fail=None):
        """第 nth 次底层改名抛错，用于构造暂存/落位阶段的磁盘故障。"""
        real = naming._rename_no_replace
        calls = {"n": 0}

        def wrapper(src, dst):
            calls["n"] += 1
            if calls["n"] == nth:
                if on_fail is not None:
                    on_fail()
                raise OSError(13, "模拟磁盘故障")
            return real(src, dst)

        return mock.patch.object(naming, "_rename_no_replace", side_effect=wrapper)

    def _swap_rows(self, base: Path):
        built = naming.build_plan(
            {
                "directory": str(base),
                "sequence": {
                    "enabled": True,
                    "base": "n",
                    "start": 2,
                    "step": -1,
                    "digits": 1,
                },
            }
        )
        return built["rows"]

    def test_取消只在组间生效未开始的组不执行(self):
        first = self.dir / "一"
        second = self.dir / "二"
        for base, tag in ((first, "一"), (second, "二")):
            base.mkdir()
            (base / "n_1.txt").write_text(tag + "1", encoding="utf-8")
            (base / "n_2.txt").write_text(tag + "2", encoding="utf-8")
        rows = self._swap_rows(first) + self._swap_rows(second)
        job = FakeJob(cancel_after=1)  # 第二组边界才取消
        outcome = naming.apply_plan(rows, job=job)
        self.assertTrue(outcome["cancelled"])
        # 第一组已完成互换
        self.assertEqual("一2", (first / "n_1.txt").read_text(encoding="utf-8"))
        self.assertEqual("一1", (first / "n_2.txt").read_text(encoding="utf-8"))
        # 第二组一个字节都没动
        self.assertEqual("二1", (second / "n_1.txt").read_text(encoding="utf-8"))
        self.assertEqual("二2", (second / "n_2.txt").read_text(encoding="utf-8"))
        self.assertEqual(
            ["success", "success", "not_run", "not_run"],
            [r["result"] for r in outcome["rows"]],
        )
        self.assertNotIn(
            ".uvmp-rename",
            " ".join(p.name for p in list(first.iterdir()) + list(second.iterdir())),
        )

    def test_组内暂存失败回滚整组(self):
        self.make("n_1.txt", "A")
        self.make("n_2.txt", "B")
        rows = self._swap_rows(self.dir)
        job = FakeJob()
        with self._fail_on_rename_call(2):  # 第二项暂存失败
            outcome = naming.apply_plan(rows, job=job)
        self.assertEqual("A", self.read("n_1.txt"))
        self.assertEqual("B", self.read("n_2.txt"))
        self.assertEqual(["n_1.txt", "n_2.txt"], self.names())
        self.assertEqual(["not_run", "failed"], [r["result"] for r in outcome["rows"]])
        self.assertTrue(any("回滚" in line for line in job.lines))

    def test_组内落位失败回滚整组且不留占位名(self):
        self.make("n_1.txt", "A")
        self.make("n_2.txt", "B")
        rows = self._swap_rows(self.dir)
        job = FakeJob()
        with self._fail_on_rename_call(4):  # 两次暂存 + 第一次落位之后，第二次落位失败
            outcome = naming.apply_plan(rows, job=job)
        # 已落位项与暂存项都要回到原名，不能出现「原名 (2)」
        self.assertEqual("A", self.read("n_1.txt"))
        self.assertEqual("B", self.read("n_2.txt"))
        self.assertEqual(["n_1.txt", "n_2.txt"], self.names())
        self.assertEqual(["not_run", "failed"], [r["result"] for r in outcome["rows"]])
        self.assertEqual(0, outcome["success"])
        self.assertTrue(any("回滚" in line for line in job.lines))

    def test_回滚失败写日志且结果与磁盘一致(self):
        self.make("n_1.txt", "A")
        self.make("n_2.txt", "B")
        rows = self._swap_rows(self.dir)
        job = FakeJob()

        def occupy_original():
            # 落位失败的同时占住第一项的原名，逼出回滚失败
            (self.dir / "n_1.txt").write_text("外部占用", encoding="utf-8")

        with self._fail_on_rename_call(4, on_fail=occupy_original):
            outcome = naming.apply_plan(rows, job=job)
        self.assertTrue(any("回滚异常" in line for line in job.lines))
        self.assertEqual(["failed", "failed"], [r["result"] for r in outcome["rows"]])
        # 结果与磁盘一致：消息里「文件仍在 X」的每一项都真实存在，且没有「原名 (2)」占位名
        reported = set()
        for row in outcome["rows"]:
            for part in (row.get("message") or "").split("文件仍在"):
                tail = part.strip().split("；")[0]
                if tail and Path(tail).exists():
                    reported.add(Path(tail).name)
        # 我们操作过的文件（除外部新建的 n_1.txt）全部在消息里如实登记
        expected = {n for n in self.names() if n != "n_1.txt"}
        self.assertEqual(expected, reported)
        self.assertNotIn("n_1.txt (2)", " ".join(self.names()))

    def test_回滚目标仅大小写不同时按文件系统语义恢复原名(self):
        """回滚的期望随文件系统语义走，不能把 Windows 的大小写不敏感写死给 POSIX。

        - 大小写不敏感（Windows）：两个名字本就是同一文件，回滚无需再改名；
        - 大小写敏感（Linux/macOS）：`Photo.JPG` 这个路径并不存在，回滚必须把文件
          恢复成原大写名，否则「回滚到原名」不成立。
        两种情况下文件身份（设备号 + inode）都不变，也没有多出第二个文件。
        """
        source = self.make("photo.JPG", "P")
        original = self.dir / "Photo.JPG"
        identity = _identity(source)
        ok, detail = naming._rollback(source, original)
        self.assertTrue(ok, detail)
        self.assertEqual("", detail)
        if _case_insensitive_fs(self.dir):
            self.assertEqual(["photo.JPG"], self.names())
            self.assertEqual(identity, _identity(self.dir / "photo.JPG"))
            self.assertEqual("P", self.read("photo.JPG"))
        else:
            self.assertEqual(["Photo.JPG"], self.names())
            self.assertEqual(identity, _identity(self.dir / "Photo.JPG"))
            self.assertEqual("P", self.read("Photo.JPG"))

    def test_回滚绝不覆盖已存在的原名(self):
        """原名已被另一个文件占用时回滚必须失败并保留两份内容，绝不替换。"""
        original = self.make("a.txt", "原名")
        temp = self.make(naming.TEMP_PREFIX + "held-0", "待回滚")
        ok, detail = naming._rollback(temp, original)
        self.assertFalse(ok)
        self.assertIn("已被占用", detail)
        self.assertEqual("原名", self.read("a.txt"))
        self.assertTrue(Path(detail.split("文件仍在")[-1].strip()).exists(), detail)
        self.assertEqual("待回滚", (self.dir / temp.name).read_text(encoding="utf-8"))
        self.assertEqual(sorted(["a.txt", temp.name]), self.names())

    def test_暂存文件被外部删除时回滚不算成功(self):
        temp = self.dir / (naming.TEMP_PREFIX + "gone-0")
        ok, detail = naming._rollback(temp, self.dir / "a.txt")
        self.assertFalse(ok)
        self.assertIn("外部删除", detail)

    def test_仅大小写项随同组失败回滚恢复原名(self):
        """同组失败整组回滚后，仅大小写项必须回到原名，且绝不覆盖任何文件。

        - 大小写不敏感（Windows）：落位后的名字与原名指向同一文件，回滚无需再改名，
          名字停在落位时的小写形式（内容与文件身份不变）；
        - 大小写敏感（Linux/macOS）：`photo.JPG` 与 `Photo.JPG` 是两个不同路径，
          原大写名已被落位腾空，回滚必须改名恢复成 `Photo.JPG`。
        """
        source = self.make("Photo.JPG", "P")
        self.make("other.txt", "O")
        identity = _identity(source)
        rows = [
            {
                "path": str(self.dir / "Photo.JPG"),
                "name": "Photo.JPG",
                "new_name": "photo.JPG",
                "status": "ok",
                "reason": "",
                "flags": ["case_only", "needs_temp"],
            },
            {
                "path": str(self.dir / "other.txt"),
                "name": "other.txt",
                "new_name": "other2.txt",
                "status": "ok",
                "reason": "",
                "flags": ["needs_temp"],
            },
        ]
        job = FakeJob()
        # 仅大小写项与普通项同组（分组逻辑另有单测，这里强制并组以覆盖回滚路径）
        with (
            mock.patch.object(naming, "_group_staged", return_value=[[0, 1]]),
            self._fail_on_rename_call(4),  # 第二项落位失败，触发整组回滚
        ):
            outcome = naming.apply_plan(rows, job=job)
        results = [r["result"] for r in outcome["rows"]]
        self.assertEqual(["not_run", "failed"], results)
        # 回滚不是「占用」：消息就是干净的「已回滚」，不夹带任何失败细节
        self.assertEqual(
            "同组 other.txt 失败，已回滚", outcome["rows"][0].get("message")
        )
        self.assertEqual(
            "改名失败（模拟磁盘故障），整组已回滚",
            outcome["rows"][1].get("message"),
        )
        self.assertTrue(any("回滚" in line for line in job.lines))
        if _case_insensitive_fs(self.dir):
            self.assertEqual(["other.txt", "photo.JPG"], self.names())
            self.assertEqual(identity, _identity(self.dir / "photo.JPG"))
            self.assertEqual("P", self.read("photo.JPG"))
        else:
            self.assertEqual(["Photo.JPG", "other.txt"], self.names())
            self.assertEqual(identity, _identity(self.dir / "Photo.JPG"))
            self.assertEqual("P", self.read("Photo.JPG"))
        # 不覆盖：同组另一项回到原名、内容原样，也没有临时文件残留
        self.assertEqual("O", self.read("other.txt"))
        self.assertEqual(
            [], [n for n in self.names() if n.startswith(naming.TEMP_PREFIX)]
        )

    def test_回滚时暂存文件被外部删除标失败并记日志(self):
        self.make("1.txt", "A")
        self.make("2.txt", "B")
        self.make("3.txt", "C")
        rows = [
            {
                "path": str(self.dir / name),
                "name": name,
                "new_name": target,
                "status": "ok",
                "reason": "",
                "flags": ["needs_temp"],
            }
            for name, target in (
                ("1.txt", "2.txt"),
                ("2.txt", "3.txt"),
                ("3.txt", "4.txt"),
            )
        ]
        job = FakeJob()
        real = naming._rename_no_replace
        calls = []

        def wrapper(src, dst):
            calls.append((Path(src), Path(dst)))
            if len(calls) == 4:  # 第一次落位失败
                calls[2][1].unlink()  # 第三项的暂存文件被外部删除
                raise OSError(13, "模拟磁盘故障")
            return real(src, dst)

        with mock.patch.object(naming, "_rename_no_replace", side_effect=wrapper):
            outcome = naming.apply_plan(rows, job=job)
        # 落位失败项与「暂存文件被外部删除」项都标 failed，其余按未执行上报
        self.assertEqual(
            ["failed", "not_run", "failed"], [r["result"] for r in outcome["rows"]]
        )
        deleted = outcome["rows"][2]["message"]
        self.assertIn("外部删除", deleted)
        self.assertTrue(
            any("回滚异常" in line and "外部删除" in line for line in job.lines)
        )
        # 未被删除的两项已回滚原名，也不留临时文件
        self.assertEqual(["1.txt", "2.txt"], self.names())

    def test_三步循环落位失败整组回滚(self):
        self.make("1.txt", "A")
        self.make("2.txt", "B")
        self.make("3.txt", "C")
        rows = [
            {
                "path": str(self.dir / name),
                "name": name,
                "new_name": target,
                "status": "ok",
                "reason": "",
                "flags": ["needs_temp", "cycle"],
            }
            for name, target in (
                ("1.txt", "2.txt"),
                ("2.txt", "3.txt"),
                ("3.txt", "1.txt"),
            )
        ]
        job = FakeJob()
        with self._fail_on_rename_call(6):  # 3 次暂存 + 2 次落位后，最后一次落位失败
            outcome = naming.apply_plan(rows, job=job)
        self.assertEqual("A", self.read("1.txt"))
        self.assertEqual("B", self.read("2.txt"))
        self.assertEqual("C", self.read("3.txt"))
        self.assertEqual(["1.txt", "2.txt", "3.txt"], self.names())
        self.assertEqual(
            ["not_run", "not_run", "failed"], [r["result"] for r in outcome["rows"]]
        )
        self.assertTrue(any("整组 3 项已回滚" in line for line in job.lines))

    def test_组内暂存失败后续组不受影响(self):
        first = self.dir / "一"
        second = self.dir / "二"
        for base, tag in ((first, "一"), (second, "二")):
            base.mkdir()
            (base / "n_1.txt").write_text(tag + "1", encoding="utf-8")
            (base / "n_2.txt").write_text(tag + "2", encoding="utf-8")
        rows = self._swap_rows(first) + self._swap_rows(second)
        job = FakeJob()
        with self._fail_on_rename_call(2):  # 第一组第二项暂存失败
            outcome = naming.apply_plan(rows, job=job)
        self.assertEqual(
            ["not_run", "failed", "success", "success"],
            [r["result"] for r in outcome["rows"]],
        )
        self.assertEqual("一1", (first / "n_1.txt").read_text(encoding="utf-8"))
        self.assertEqual("一2", (first / "n_2.txt").read_text(encoding="utf-8"))
        self.assertEqual("二2", (second / "n_1.txt").read_text(encoding="utf-8"))
        self.assertEqual("二1", (second / "n_2.txt").read_text(encoding="utf-8"))

    def test_两个目录各自循环互换互不干扰(self):
        first = self.dir / "一"
        second = self.dir / "二"
        first.mkdir()
        second.mkdir()
        for base, tag in ((first, "一"), (second, "二")):
            (base / "n_1.txt").write_text(tag + "1", encoding="utf-8")
            (base / "n_2.txt").write_text(tag + "2", encoding="utf-8")
        rows = self._swap_rows(first) + self._swap_rows(second)
        outcome = naming.apply_plan(rows)
        self.assertEqual(4, outcome["success"])
        self.assertEqual("一2", (first / "n_1.txt").read_text(encoding="utf-8"))
        self.assertEqual("一1", (first / "n_2.txt").read_text(encoding="utf-8"))
        self.assertEqual("二2", (second / "n_1.txt").read_text(encoding="utf-8"))
        self.assertEqual("二1", (second / "n_2.txt").read_text(encoding="utf-8"))

    def test_三步循环经暂存组落位(self):
        self.make("1.txt", "A")
        self.make("2.txt", "B")
        self.make("3.txt", "C")
        rows = [
            {
                "path": str(self.dir / "1.txt"),
                "name": "1.txt",
                "new_name": "2.txt",
                "status": "ok",
                "reason": "",
                "flags": ["needs_temp", "cycle"],
            },
            {
                "path": str(self.dir / "2.txt"),
                "name": "2.txt",
                "new_name": "3.txt",
                "status": "ok",
                "reason": "",
                "flags": ["needs_temp", "cycle"],
            },
            {
                "path": str(self.dir / "3.txt"),
                "name": "3.txt",
                "new_name": "1.txt",
                "status": "ok",
                "reason": "",
                "flags": ["needs_temp", "cycle"],
            },
        ]
        outcome = naming.apply_plan(rows)
        self.assertEqual(3, outcome["success"])
        self.assertEqual("C", self.read("1.txt"))
        self.assertEqual("A", self.read("2.txt"))
        self.assertEqual("B", self.read("3.txt"))

    def test_从未变化的项不执行(self):
        self.make("a.txt")
        built = naming.build_plan({"directory": str(self.dir)})
        outcome = naming.apply_plan(built["rows"])
        self.assertEqual(0, outcome["success"])
        self.assertEqual(1, outcome["unchanged"])
        self.assertEqual(["a.txt"], self.names())


class RandomBatchTest(TempDirMixin, unittest.TestCase):
    """固定种子的随机小批次：预检到执行的结果必须与磁盘一致。"""

    STEMS = (
        "a",
        "b",
        "B",
        "c",
        "n_1",
        "n_2",
        "n_3",
        "文件_001",
        "文件_002",
        "x",
        "X_001",
    )
    EXTS = ("", ".txt", ".jpg", ".TXT")

    def _prepare(self, seed: int) -> dict:
        rnd = random.Random(seed)
        base = self.dir / ("批次%02d" % seed)
        files = []
        for d in range(rnd.randint(1, 2)):
            folder = base / ("目录%d" % d)
            folder.mkdir(parents=True)
            used = set()
            for _ in range(rnd.randint(2, 6)):
                name = rnd.choice(self.STEMS) + rnd.choice(self.EXTS)
                if name.casefold() in used:
                    continue
                used.add(name.casefold())
                (folder / name).write_text("%02d-%s" % (seed, name), encoding="utf-8")
                files.append(str(folder / name))
        params = {"files": files, "conflict": rnd.choice(["skip", "suffix"])}
        mode = rnd.choice(["sequence", "replace", "prefix", "mixed"])
        if mode in ("sequence", "mixed"):
            params["sequence"] = {
                "enabled": True,
                "base": rnd.choice(["n", "文件", "B", "x", ""]),
                "start": rnd.randint(1, 3),
                "step": rnd.choice([1, 1, 2, -1]),
                "digits": rnd.choice([1, 2, 3]),
            }
        if mode in ("replace", "mixed"):
            params["replace"] = [
                {
                    "find": rnd.choice(["a", "b", "x", "_001"]),
                    "replace": rnd.choice(["b", "X_001", "n_1"]),
                }
            ]
        if mode in ("prefix", "mixed"):
            params["prefix"] = rnd.choice(["", "x_", "P_"])
            params["suffix"] = rnd.choice(["", "_S"])
        if rnd.random() < 0.2:
            params["keep_extension"] = False
        return params

    def test_随机小批次结果与磁盘一致(self):
        for seed in range(24):
            with self.subTest(seed=seed):
                self._check_case(seed)

    def _check_case(self, seed: int):
        params = self._prepare(seed)
        built = naming.build_plan(params)
        rows = built["rows"]
        originals = {
            row["path"]: Path(row["path"]).read_text(encoding="utf-8") for row in rows
        }
        # 让位判定必须是不动点：再跑一次不再改判，否则执行时会撞上仍在磁盘的名字
        settled = [dict(row, flags=list(row["flags"])) for row in rows]
        naming._settle_held_targets(settled, {"conflict": params["conflict"]})
        self.assertEqual(
            [(row["status"], row["new_name"]) for row in rows],
            [(row["status"], row["new_name"]) for row in settled],
        )
        # 可提交项目标互不重复，且都不落在批内保留名（skip/same）上
        claimed = [
            naming._claim_key(Path(row["path"]).parent, row["new_name"])
            for row in rows
            if row["status"] == "ok"
        ]
        self.assertEqual(len(claimed), len(set(claimed)))
        held = {naming._path_key(row["path"]) for row in rows if row["status"] != "ok"}
        for row in rows:
            if row["status"] != "ok":
                continue
            target = naming._path_key(Path(row["path"]).with_name(row["new_name"]))
            self.assertNotIn(target, held)
        outcome = naming.apply_plan(rows)
        self.assertEqual(0, outcome["failed"], outcome["rows"])
        for row, result in zip(rows, outcome["rows"]):
            path = (
                Path(row["path"]).with_name(row["new_name"])
                if result["result"] == "success"
                else Path(row["path"])
            )
            self.assertEqual(originals[row["path"]], path.read_text(encoding="utf-8"))
        leftovers = [
            p.name for p in self.dir.rglob("*") if p.name.startswith(naming.TEMP_PREFIX)
        ]
        self.assertEqual([], leftovers)


class SettlePerformanceTest(unittest.TestCase):
    """让位预检的性能上限：长链只在内存里推演，不建任何文件。"""

    def test_两千项链让位预检在宽松上限内完成(self):
        total = 2000
        base = Path(tempfile.gettempdir()) / "uvmp-rename-chain-bench"
        rows = [
            {
                "path": str(base / ("f%04d.txt" % i)),
                "name": "f%04d.txt" % i,
                "new_name": "f%04d.txt" % (i + 1 if i < total - 1 else i),
                "status": "same" if i == total - 1 else "ok",
                "reason": "",
                "flags": [],
            }
            for i in range(total)
        ]
        started = time.perf_counter()
        naming._settle_held_targets(rows, {"conflict": "skip"})
        elapsed = time.perf_counter() - started
        self.assertEqual(
            ["skip"] * (total - 1) + ["same"], [row["status"] for row in rows]
        )
        # 旧实现每轮重建 owners、每行重建 claimed：本机 2000 项约 14 秒；上限留足余量
        self.assertLess(elapsed, 3.0, "2000 项链让位预检耗时 %.2f 秒" % elapsed)


class RenameOneTest(TempDirMixin, unittest.TestCase):
    def test_单图改名保留扩展名(self):
        path = self.make("IMG_0001.JPG")
        new_path = naming.rename_one(path, "封面")
        self.assertEqual("封面.JPG", new_path.name)
        self.assertTrue(new_path.exists())

    def test_重复输入扩展名不叠加(self):
        path = self.make("a.png")
        new_path = naming.rename_one(path, "b.png")
        self.assertEqual("b.png", new_path.name)

    def test_同名原样返回(self):
        path = self.make("a.txt")
        self.assertEqual(path, naming.rename_one(path, "a"))

    def test_拒绝非法名与已存在目标(self):
        path = self.make("a.txt")
        self.make("b.txt", "占用")
        with self.assertRaises(ValueError):
            naming.rename_one(path, "a/b")
        with self.assertRaises(ValueError):
            naming.rename_one(path, "con")
        with self.assertRaises(FileExistsError):
            naming.rename_one(path, "b")
        self.assertEqual("占用", self.read("b.txt"))

    def test_仅大小写改名(self):
        path = self.make("Photo.JPG", "P")
        new_path = naming.rename_one(path, "photo.JPG")
        self.assertIn("photo.JPG", self.names())
        self.assertTrue(new_path.exists())


if __name__ == "__main__":
    unittest.main()
