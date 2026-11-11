# -*- coding: utf-8 -*-
"""学堂刷课回归测试：断点并发合并、口径准确、页签清理与本机地址校验。

全部使用 fake 浏览器与临时状态目录，不连接任何真实浏览器或内网服务。
"""

import importlib.util
import json
import os
import sys
import tempfile
import threading
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
TOOLKIT = ROOT / "toolkit"
for path in (str(TOOLKIT), str(ROOT)):
    if path not in sys.path:
        sys.path.insert(0, path)

import jobs as _jobs  # noqa: E402
import state  # noqa: E402


def load_backend():
    path = TOOLKIT / "apps" / "course_autoplay" / "backend.py"
    spec = importlib.util.spec_from_file_location("course_autoplay_backend_test", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def load_engine():
    path = ROOT / "course_autoplay_trial.py"
    spec = importlib.util.spec_from_file_location("course_autoplay_engine_test", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class FakeJob:
    def __init__(self, job_id="job-1"):
        self.id = job_id
        self.cancelled = False
        self.logs = []
        self.progress = []

    def log(self, message):
        self.logs.append(str(message))

    def set_progress(self, current, total, label=""):
        self.progress.append((current, total, label))

    def check_cancel(self):
        if self.cancelled:
            raise _jobs.Cancelled()


class FakePage:
    def __init__(self, url, visible=True, context=None):
        self.url = url
        self.visible = visible
        self.context = context
        self.closed = False

    def evaluate(self, expression):
        return self.visible

    def locator(self, selector):
        raise RuntimeError("fake 页面没有 DOM")

    def title(self):
        return "假页面"

    def set_default_timeout(self, value):
        pass

    def set_default_navigation_timeout(self, value):
        pass

    def bring_to_front(self):
        pass

    def is_closed(self):
        return self.closed

    def close(self):
        self.closed = True


class FakeContext:
    def __init__(self, pages):
        self._pages = pages
        self.listeners = []

    @property
    def pages(self):
        pages = self._pages() if callable(self._pages) else self._pages
        return list(pages)

    def on(self, event, handler):
        self.listeners.append((event, handler))

    def remove_listener(self, event, handler):
        if (event, handler) in self.listeners:
            self.listeners.remove((event, handler))


class FakeRunner:
    """替代 engine.CourseRunner，只保留 backend.run 会调用的接口。"""

    def __init__(self, summary=None, error=None):
        self.summary = summary or {
            "next": None,
            "pending": [],
            "total": 2,
            "seen": 2,
            "studiable": 2,
            "completed": 2,
        }
        self.error = error
        self.calls = []
        self.list_page = SimpleNamespace(
            url="http://example.test/train/trainDetail?trainid=8"
        )
        self.on_run = None

    def load_playwright(self):
        self.calls.append("load_playwright")

    def connect(self, launched=False):
        self.calls.append("connect")

    def find_list_page(self):
        self.calls.append("find_list_page")

    def reset_filters(self):
        self.calls.append("reset_filters")
        return [], 0

    def run(self, first_page, checkpoint=None):
        self.calls.append("run")
        if self.on_run:
            self.on_run(self)
        if self.error is not None:
            raise self.error
        return self.summary

    def scan_courses(self, stop_first=False, **kwargs):
        self.calls.append("scan_courses")
        return self.summary

    def report_scan(self, summary, skip_reasons=None):
        self.calls.append("report_scan")

    def close(self):
        self.calls.append("close")


def empty_summary(total):
    """列表里有 total 门课，但没有一门可学习（或列表为空）"""
    return {
        "next": None,
        "pending": [],
        "total": total,
        "seen": total,
        "studiable": 0,
        "completed": 0,
    }


def make_args(**overrides):
    values = {
        "cdp_url": "http://127.0.0.1:9222",
        "timeout": 1,
        "max_attempts": 3,
        "cancel_check": None,
        "progress_handler": None,
        "interrupt_exceptions": (),
        "diagnostic_prefix": "test",
    }
    values.update(overrides)
    return SimpleNamespace(**values)


class CourseAutoplayBackendTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.data = Path(self.tmp.name) / "data"
        env = {
            "XDG_DATA_HOME": str(self.data),
            "LOCALAPPDATA": str(self.data),
            "APPDATA": str(self.data),
            "UVMP_TOOLKIT_MIRROR_STATE": "",
        }
        patcher = mock.patch.dict(os.environ, env)
        patcher.start()
        self.addCleanup(patcher.stop)
        self.backend = load_backend()
        self.engine = load_engine()

    def fake_engine(self, runner):
        return SimpleNamespace(
            CourseRunner=lambda *a, **k: runner,
            validate_cdp_url=self.engine.validate_cdp_url,
            course_list_key=self.engine.course_list_key,
            course_progress=self.engine.course_progress,
        )

    def run_job(self, runner, params=None, cfg=None):
        job = FakeJob()
        params = params or {"instance_id": "default", "instance": {"id": "default"}}
        patcher = mock.patch.object(
            self.backend, "_engine", return_value=self.fake_engine(runner)
        )
        with patcher:
            with mock.patch.object(
                self.backend._config, "load", return_value=cfg or {}
            ):
                return self.backend.run(job, params), job

    def checkpoints(self):
        path = self.backend._checkpoint_path()
        return json.loads(path.read_text(encoding="utf-8"))

    def profile_dir(self, instance):
        return self.backend._browser_values({}, {"instance": instance})["profile_dir"]

    def test_每个实例默认浏览器目录独立(self):
        default_dir = self.profile_dir({"id": "default"})
        a_dir = self.profile_dir({"id": "a"})
        b_dir = self.profile_dir({"id": "b"})
        self.assertNotEqual(default_dir, a_dir)
        self.assertNotEqual(a_dir, b_dir)
        shared = self.backend.browser_automation.settings({})["profile_dir"]
        self.assertNotEqual(Path(default_dir), Path(shared))
        for value in (default_dir, a_dir, b_dir):
            self.assertIn("course_autoplay", value)

    def test_显式配置的浏览器目录优先(self):
        profile = self.profile_dir({"id": "a", "profile_dir": "/tmp/自定义"})
        self.assertEqual("/tmp/自定义", profile)

    def test_实例标识不会逃出状态目录(self):
        profile = self.profile_dir({"id": "../../evil"})
        self.assertNotIn("..", Path(profile).parts)
        self.assertTrue(profile.startswith(str(state.state_dir())))

    def persist(self, key, processed):
        checkpoint = self.backend._clean_checkpoint({"processed": processed})
        self.backend._persist(key, checkpoint)

    def test_断点落盘保留其他实例条目(self):
        self.persist("a::train:1", 1)
        self.persist("b::train:2", 2)
        data = self.checkpoints()
        self.assertEqual(1, data["a::train:1"]["processed"])
        self.assertEqual(2, data["b::train:2"]["processed"])
        self.assertEqual({"a::train:1", "b::train:2"}, set(data))

    def test_运行期间其他实例落盘不被覆盖(self):
        runner = FakeRunner()
        runner.on_run = lambda _r: self.backend._persist(
            "other::train:9", self.backend._clean_checkpoint({"processed": 9})
        )
        self.run_job(runner)
        data = self.checkpoints()
        self.assertIn("other::train:9", data)
        self.assertIn("default::train:8", data)

    def test_并发落盘不丢条目(self):
        errors = []

        def writer(index):
            try:
                for _ in range(20):
                    self.backend._persist(
                        "inst%d::train:1" % index,
                        self.backend._clean_checkpoint({"processed": index}),
                    )
            except Exception as exc:  # pragma: no cover - 失败时给出线索
                errors.append(exc)

        threads = [threading.Thread(target=writer, args=(i,)) for i in range(4)]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join()
        self.assertEqual([], errors)
        self.assertEqual(
            {"inst%d::train:1" % i for i in range(4)}, set(self.checkpoints())
        )

    def test_落盘失败不中断学习(self):
        runner = FakeRunner()
        with mock.patch.object(
            self.backend, "_write_checkpoints", side_effect=OSError("磁盘已满")
        ):
            result, job = self.run_job(runner)
        self.assertFalse(result.get("paused"))
        self.assertEqual(2, result["completed"])
        self.assertIn("run", runner.calls)
        self.assertTrue(any("断点" in line for line in job.logs))

    def test_暂停保留active并记录断点(self):
        runner = FakeRunner(error=_jobs.Cancelled())
        result, job = self.run_job(runner)
        self.assertTrue(result["paused"])
        checkpoint = self.checkpoints()["default::train:8"]
        self.assertTrue(checkpoint["active"])
        self.assertFalse(checkpoint.get("last_error"))
        self.assertTrue(any("暂停" in line for line in job.logs))

    def test_失败不保留active并记录最后错误(self):
        runner = FakeRunner(error=RuntimeError("页面已被关闭"))
        with self.assertRaises(RuntimeError):
            self.run_job(runner)
        checkpoint = self.checkpoints()["default::train:8"]
        self.assertFalse(checkpoint["active"])
        self.assertIn("页面已被关闭", checkpoint["last_error"])

    def test_空列表不报百分之百(self):
        runner = FakeRunner(summary=empty_summary(total=0))
        with self.assertRaises(RuntimeError) as ctx:
            self.run_job(runner)
        self.assertIn("可学习", str(ctx.exception))
        checkpoint = self.checkpoints()["default::train:8"]
        self.assertFalse(checkpoint["active"])
        self.assertIn("可学习", checkpoint["last_error"])

    def test_全部不可学习不报百分之百(self):
        runner = FakeRunner(summary=empty_summary(total=3))
        with self.assertRaises(RuntimeError) as ctx:
            self.run_job(runner)
        self.assertIn("可学习", str(ctx.exception))

    def test_汇总口径包含可学习门数(self):
        entry = {
            "item": {"resourceid": "r1", "coursename": "课程一", "studyRate": 40},
            "page": 2,
            "index": 0,
            "page_size": 5,
            "reason": "",
        }
        summary = {"total": 7, "studiable": 5, "completed": 4, "pending": [entry]}
        result = self.backend._summary(
            summary, self.backend._clean_checkpoint({}), "k::train:1"
        )
        self.assertEqual(7, result["total"])
        self.assertEqual(5, result["studiable"])
        self.assertEqual(4, result["completed"])
        self.assertEqual(1, result["pending"])
        self.assertEqual(40, result["pending_courses"][0]["progress"])

    def test_非本机cdp地址在连接前被拒绝(self):
        runner = FakeRunner()
        with self.assertRaises(RuntimeError) as ctx:
            self.run_job(
                runner,
                params={
                    "instance_id": "default",
                    "instance": {"id": "default", "cdp_url": "http://10.1.2.3:9222"},
                },
            )
        self.assertIn("本机", str(ctx.exception))
        self.assertNotIn("connect", runner.calls)

    def test_检查点字段类型清洗(self):
        clean = self.backend._clean_checkpoint(
            {
                "version": "2",
                "active": "yes",
                "resume_page": "4",
                "processed": "-3",
                "attempts": {"a": "3", "b": "x", 5: 2},
                "last_progress": {"a": "50", "b": "不是数字"},
                "last_errors": {"a": 1, "b": "超时"},
                "skip_reasons": {7: "已下线"},
                "skipped_ids": ["r1", 9, ""],
                "last_error": {"detail": "x"},
            }
        )
        self.assertEqual(1, clean["version"])
        self.assertTrue(clean["active"])
        self.assertEqual(4, clean["resume_page"])
        self.assertEqual(0, clean["processed"])
        self.assertEqual({"a": 3}, clean["attempts"])
        self.assertEqual({"a": 50.0}, clean["last_progress"])
        self.assertEqual({"b": "超时"}, clean["last_errors"])
        self.assertEqual({"7": "已下线"}, clean["skip_reasons"])
        self.assertEqual(["r1"], clean["skipped_ids"])
        self.assertEqual("", clean["last_error"])

    def test_恢复学习时跳过已处理课程(self):
        runner = FakeRunner()
        self.backend._persist(
            "default::train:8",
            self.backend._clean_checkpoint(
                {
                    "active": True,
                    "resume_page": 3,
                    "processed": 5,
                    "skipped_ids": ["r9"],
                }
            ),
        )
        result, job = self.run_job(runner)
        self.assertFalse(result.get("paused"))
        self.assertTrue(any("恢复当前培训班" in line for line in job.logs))
        self.assertTrue(any("第 3 页" in line for line in job.logs))


class CourseAutoplayEngineTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.engine = load_engine()

    def make_runner(self, **overrides):
        logs = []
        runner = self.engine.CourseRunner(
            make_args(**overrides), Path(self.tmp.name), log_handler=logs.append
        )
        return runner, logs

    def test_识别课程列表时每轮刷新页签(self):
        runner, logs = self.make_runner()
        context = FakeContext([])
        access = {"count": 0}
        target = FakePage(
            "http://example.test/learning-c/train/trainDetail?trainid=8", True, context
        )

        def pages():
            access["count"] += 1
            return [target] if access["count"] > 1 else []

        # 第一次读取时列表页还没打开，之后由人工打开
        context._pages = pages
        runner.browser = SimpleNamespace(contexts=[context])
        runner.find_list_page()
        self.assertIs(target, runner.list_page)
        self.assertIs(context, runner.context)
        self.assertTrue(logs)

    def test_连接前拒绝非本机调试地址(self):
        runner, _logs = self.make_runner(cdp_url="http://192.168.1.9:9222")
        runner.sync_playwright = lambda: self.fail("不应启动 Playwright")
        with self.assertRaises(RuntimeError):
            runner.connect(launched=False)
        self.assertIsNone(runner.playwright)

    def test_学习页超时后关闭新增页签(self):
        runner, logs = self.make_runner()
        context = FakeContext([])
        list_page = FakePage(
            "http://example.test/train/trainDetail?trainid=8", True, context
        )
        context._pages = [list_page]
        extra = FakePage("http://example.test/study/1", True, context)
        runner.browser = SimpleNamespace(contexts=[context])
        runner.context = context
        runner.list_page = list_page

        class Button:
            def __init__(self):
                self.first = self

            def count(self):
                return 1

            def is_visible(self):
                return True

            def click(self):
                return None

        class Row:
            def get_by_text(self, text, exact=True):
                return Button()

        runner._course_row = lambda entry: Row()

        def open_popup(page, action):
            context._pages = [list_page, extra]
            raise RuntimeError("等待学习页超时")

        runner._expect_popup = open_popup
        entry = {
            "item": {"resourceid": "r1", "coursename": "课程一"},
            "page": 1,
            "index": 0,
            "page_size": 1,
            "reason": "",
        }
        with self.assertRaises(RuntimeError):
            runner._process_course(entry)
        self.assertTrue(extra.closed)
        self.assertFalse(list_page.closed)
        self.assertTrue(any("关闭" in line for line in logs))

    def test_没有可学习课程时不宣布百分之百(self):
        runner, logs = self.make_runner()
        runner.scan_courses = lambda **kwargs: {
            "next": None,
            "pending": [],
            "total": 3,
            "seen": 3,
            "studiable": 0,
            "completed": 0,
        }
        summary = runner.run(([], 3))
        self.assertEqual(0, summary["studiable"])
        self.assertFalse(any("100%" in line for line in logs))
        self.assertTrue(any("可学习" in line for line in logs))

    def test_全部完成时仍宣布完成(self):
        runner, logs = self.make_runner()
        runner.scan_courses = lambda **kwargs: {
            "next": None,
            "pending": [],
            "total": 3,
            "seen": 3,
            "studiable": 3,
            "completed": 3,
        }
        runner.run(([], 3))
        self.assertTrue(any("100%" in line for line in logs))

    def test_检查点脏数据不会中断学习(self):
        runner, logs = self.make_runner()
        runner.scan_courses = lambda **kwargs: {
            "next": None,
            "pending": [],
            "total": 2,
            "seen": 2,
            "studiable": 2,
            "completed": 2,
        }
        summary = runner.run(
            ([], 2),
            checkpoint={
                "attempts": {"r1": "2"},
                "last_progress": {"r1": "八十"},
                "skipped_ids": ["r2", 7],
            },
        )
        self.assertEqual(2, summary["completed"])


if __name__ == "__main__":
    unittest.main()
