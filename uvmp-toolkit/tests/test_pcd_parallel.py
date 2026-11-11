# -*- coding: utf-8 -*-
"""旧 Python 导出流程并发与生命周期测试（Task 2）。

全部使用 devmock 假数据、fake 渲染器、临时目录与 fake Job，不连接真实 SSO/派车系统。
覆盖：查询/渲染并发上限与真实重叠、乱序完成保序、单条失败隔离与重跑续传、取消停止
全部渲染器、渲染错误后 worker 重建、worker 目录隔离与清理、token 刷新串行、
12 单 dev.mock 全流程。
"""

import contextlib
import json
import os
import sys
import tempfile
import threading
import time
import unittest
from datetime import datetime
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
TOOLKIT = ROOT / "toolkit"
APP_DIR = TOOLKIT / "apps" / "pcd_export"
for _p in (str(ROOT), str(TOOLKIT), str(APP_DIR)):
    if _p not in sys.path:
        sys.path.insert(0, _p)

import backend  # noqa: E402
import devmock  # noqa: E402
import parallel  # noqa: E402
import uvmp  # noqa: E402
import vehicle_export as ve  # noqa: E402
from jobs import Cancelled  # noqa: E402

_REAL_MERGE = ve.merge_pdfs
_REAL_NOON = ve.write_noon_end_report


def make_items(count=6, day=None):
    day = day or datetime(2026, 10, 10, 9, 0)
    return [devmock._mk_order(i + 1, day, "2") for i in range(count)]


def codes_of(items):
    return [
        {"code": it["runCode"], "date_hint": str(it["planSendTime"])[:10]}
        for it in items
    ]


def seq_of_code(code):
    return int(str(code)[-2:])


class Probe:
    """记录同时在途数量"""

    def __init__(self):
        self.active = 0
        self.max_active = 0
        self.calls = 0
        self._lock = threading.Lock()

    @contextlib.contextmanager
    def track(self):
        with self._lock:
            self.active += 1
            self.calls += 1
            self.max_active = max(self.max_active, self.active)
        try:
            yield
        finally:
            with self._lock:
                self.active -= 1


class FakeJob:
    """与 jobs.Job 相同的业务侧接口；不落日志文件"""

    def __init__(self, job_id="job-test"):
        self.id = job_id
        self.logs = []
        self.progress = []
        self._cancel = threading.Event()
        self._hooks_lock = threading.Lock()
        self._commit_lock = threading.RLock()
        self.hooks = []

    def log(self, msg):
        self.logs.append(msg)

    def set_progress(self, current, total, label=""):
        self.progress.append((current, total, label))

    @property
    def cancelled(self):
        return self._cancel.is_set()

    def check_cancel(self):
        if self._cancel.is_set():
            raise Cancelled()

    def cancel(self):
        with self._commit_lock:
            with self._hooks_lock:
                if self._cancel.is_set():
                    return
                self._cancel.set()
                hooks = list(self.hooks)
        for fn in hooks:
            try:
                fn()
            except Exception:  # noqa: BLE001
                pass

    def add_cancel_hook(self, fn):
        with self._hooks_lock:
            self.hooks.append(fn)
            cancelled = self._cancel.is_set()
        if cancelled:
            try:
                fn()
            except Exception:  # noqa: BLE001
                pass

    def remove_cancel_hook(self, fn):
        with self._hooks_lock:
            try:
                self.hooks.remove(fn)
            except ValueError:
                pass

    @contextlib.contextmanager
    def commit(self):
        with self._commit_lock:
            self.check_cancel()
            yield


class RaceJob(FakeJob):
    """可在 commit 入口前插入一次强制交错的 FakeJob（用于证明取消先于提交）"""

    def __init__(self, job_id="job-test"):
        super().__init__(job_id)
        self.before_commit = None

    @contextlib.contextmanager
    def commit(self):
        cb, self.before_commit = self.before_commit, None
        if cb is not None:
            cb()
        with self._commit_lock:
            self.check_cancel()
            yield


class FakeRenderer:
    def __init__(self, work_dir):
        self.work_dir = Path(work_dir)
        self.work_dir.mkdir(parents=True, exist_ok=True)
        self.stop_calls = 0
        self.pcd_calls = []
        self.track_calls = []
        self.pre_shot_js = []

    def render_pcd_pdf_one(self, item, out_pdf, tag="", diag_dir=None):
        self.pcd_calls.append(item.get("runCode"))
        backend._write_stub_pdf(Path(out_pdf), "[fake] pcd %s" % item.get("runCode"))

    def render_route_png(self, route_id, out_png, pre_shot_js=""):
        self.track_calls.append(route_id)
        self.pre_shot_js.append(pre_shot_js)
        Path(out_png).write_bytes(b"\x89PNG\r\n\x1a\n")

    def render_pcd_png(self, order, out_png, pre_shot_js=""):
        self.track_calls.append(order.get("runCode"))
        self.pre_shot_js.append(pre_shot_js)
        Path(out_png).write_bytes(b"\x89PNG\r\n\x1a\n")

    def stop(self):
        self.stop_calls += 1


class ExportFixture(unittest.TestCase):
    """导出编排测试公共夹具：临时目录、devmock 客户端、fake 渲染器与临时状态目录。
    不含用例，供本文件与 test_pcd_regression.py 复用。"""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.tmpdir = Path(self._tmp.name)
        self.outdir = self.tmpdir / "out"
        self.outdir.mkdir()

        env = mock.patch.dict(
            os.environ,
            {
                "LOCALAPPDATA": str(self.tmpdir / "local"),
                "XDG_DATA_HOME": str(self.tmpdir / "xdg"),
            },
        )
        env.start()
        self.addCleanup(env.stop)
        self.state_root = backend.state.state_dir() / "browser-workers"

        self.clients = []
        self.created_renderers = []
        self.merge_calls = []
        self.noon_calls = []
        self.job = FakeJob()
        self.cfg = self.make_cfg()
        self.renderer_factory = self.default_renderer_factory
        self.use_items(make_items(6))

        patchers = [
            mock.patch.object(
                backend.uvmp, "make_clients", side_effect=self._make_clients
            ),
            mock.patch.object(
                backend.platform_utils, "electron_binary", return_value=""
            ),
            mock.patch.object(
                backend.platform_utils, "detect_chrome", return_value="fake-chrome"
            ),
            mock.patch.object(
                backend.ve, "html_to_pdf", side_effect=self._fake_html_to_pdf
            ),
            mock.patch.object(
                backend.uvmp, "start_renderer", side_effect=self._start_renderer
            ),
            mock.patch.object(backend.ve, "merge_pdfs", side_effect=self._record_merge),
            mock.patch.object(
                backend.ve, "write_noon_end_report", side_effect=self._record_noon
            ),
        ]
        for p in patchers:
            p.start()
            self.addCleanup(p.stop)

    # ---- 基础设施 ----
    def make_cfg(self, query_workers=4, render_workers=2, mock_on=False):
        return {
            "order_export": {
                "default_outdir": "",
                "lookback_days": 62,
                "batch_subdir": False,
                "query_workers": query_workers,
                "render_workers": render_workers,
            },
            "dev": {"mock": mock_on},
            "render": {"chrome": ""},
            "_ini": {"export": {"track_render": "official"}, "render": {}},
        }

    def use_items(self, items):
        self.items = items
        self.client = devmock.FakeClient(items)
        self.clients = [self.client] * 8

    def default_renderer_factory(self, work_dir, client):
        r = FakeRenderer(work_dir)
        self.created_renderers.append(r)
        return r

    def _make_clients(self, cfg, log=print, count=1):
        return (self.clients[: max(1, int(count))], self.client.auth)

    def _start_renderer(self, client, auth, cfg, work_dir, log=print):
        return self.renderer_factory(Path(work_dir), client)

    def _fake_html_to_pdf(
        self, html, pdf_path, chrome, budget="", electron="", job=None
    ):
        backend._write_stub_pdf(Path(pdf_path), "[fake] html2pdf")

    def _record_merge(self, files, out_path, log=print):
        self.merge_calls.append((Path(out_path).name, [Path(f).name for f in files]))
        return _REAL_MERGE(files, out_path, log=log)

    def _record_noon(self, rows, out_path, log=print):
        self.noon_calls.append([dict(r) for r in rows])
        return _REAL_NOON(rows, out_path, log=log)

    def params(self, items=None, outdir=None, **overrides):
        items = items if items is not None else self.items
        params = {
            "outdir": str(outdir or self.outdir),
            "batch_subdir": False,
            "export_pcd": True,
            "export_track": True,
            "calibrate_noon": False,
            "codes": codes_of(items),
        }
        params.update(overrides)
        return params

    def run_export(self, items=None, outdir=None, cfg=None, **overrides):
        if items is not None and items is not self.items:
            self.use_items(items)
        params = self.params(items=items, outdir=outdir, **overrides)
        cfg = cfg or self.cfg
        return backend._run_python(
            self.job, cfg, params, backend._resolve_codes(params)
        )

    def job_root(self):
        return self.state_root / self.job.id


class ExportParallelBase(ExportFixture):
    # ---- 用例 ----
    def test_并发配置钳制(self):
        self.assertEqual(parallel.clamp_workers(None, 4, 1, 8), 4)
        self.assertEqual(parallel.clamp_workers(0, 4, 1, 8), 1)
        self.assertEqual(parallel.clamp_workers(-3, 2, 1, 4), 1)
        self.assertEqual(parallel.clamp_workers(99, 2, 1, 4), 4)
        self.assertEqual(parallel.clamp_workers("3", 2, 1, 4), 3)
        self.assertEqual(parallel.clamp_workers("坏值", 2, 1, 4), 2)
        # 导出引擎新上限：查询/渲染均 1..16，默认 8/4
        self.assertEqual(
            backend._worker_counts(
                {"order_export": {"query_workers": 99, "render_workers": 99}}
            ),
            (16, 16),
        )
        self.assertEqual(backend._worker_counts({}), (8, 4))

    def test_查询并发不超过配置且确实重叠(self):
        items = make_items(8)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=3, render_workers=1)
        probe = Probe()
        real_query = ve.query_pcd_by_run_code

        def slow_query(client, code, date_hint="", lookback_days=62, log=print):
            with probe.track():
                time.sleep(0.12)
                return real_query(client, code, date_hint, lookback_days, log)

        with mock.patch.object(ve, "query_pcd_by_run_code", side_effect=slow_query):
            result = self.run_export(items=items, export_track=False)

        self.assertGreaterEqual(probe.max_active, 2)
        self.assertLessEqual(probe.max_active, 3)
        self.assertEqual(result["found"], 8)
        self.assertEqual(result["pcd_ok"], 8)

    def test_渲染并发不超过配置且确实重叠(self):
        items = make_items(4)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=2)
        probe = Probe()

        def factory(work_dir, client):
            r = FakeRenderer(work_dir)
            self.created_renderers.append(r)
            real_render = r.render_pcd_pdf_one

            def slow_render(item, out_pdf, tag="", diag_dir=None):
                with probe.track():
                    time.sleep(0.12)
                    return real_render(item, out_pdf, tag, diag_dir)

            r.render_pcd_pdf_one = slow_render
            return r

        self.renderer_factory = factory
        result = self.run_export(items=items, export_track=False)

        self.assertEqual(probe.max_active, 2)
        self.assertEqual(result["pcd_ok"], 4)
        self.assertEqual(len(self.created_renderers), 2)
        for r in self.created_renderers:
            self.assertGreaterEqual(r.stop_calls, 1)

    def test_乱序完成仍按seq更新进度清单与合并输入(self):
        items = make_items(4)
        self.use_items(items)
        completion = []

        def factory(work_dir, client):
            r = FakeRenderer(work_dir)
            self.created_renderers.append(r)
            real_render = r.render_pcd_pdf_one

            def delayed(item, out_pdf, tag="", diag_dir=None):
                seq = seq_of_code(item.get("runCode"))
                time.sleep(0.5 - 0.1 * seq)
                completion.append(seq)
                return real_render(item, out_pdf, tag, diag_dir)

            r.render_pcd_pdf_one = delayed
            return r

        self.renderer_factory = factory
        self.cfg = self.make_cfg(query_workers=2, render_workers=2)
        result = self.run_export(items=items, export_track=False)

        # 实际完成顺序确实乱序（输入顺序 1,2,3,4）
        self.assertEqual(sorted(completion), [1, 2, 3, 4])
        self.assertNotEqual(completion, [1, 2, 3, 4])
        self.assertEqual([r["seq"] for r in result["rows"]], [1, 2, 3, 4])
        # 进度按输入顺序（而不是完成顺序）
        done_seqs = []
        for _cur, _total, label in self.job.progress:
            if label.startswith("派车单 "):
                done_seqs.append(
                    int(Path(label.split(" ", 1)[1]).name.split("_", 1)[0])
                )
        self.assertEqual(done_seqs, [1, 2, 3, 4])
        # 清单顺序
        lines = (
            (self.outdir / "_清单.csv")
            .read_text(encoding="utf-8-sig")
            .strip()
            .splitlines()
        )
        self.assertEqual([int(line.split(",")[0]) for line in lines[1:]], [1, 2, 3, 4])
        # 合并输入顺序
        merged = dict(self.merge_calls)
        self.assertEqual(
            [int(n.split("_", 1)[0]) for n in merged["_合并_派车单.pdf"]], [1, 2, 3, 4]
        )

    def test_轨迹乱序完成_noon页码与合并仍按seq(self):
        items = make_items(3)
        noon_ends = [
            "2026-10-10 10:30:00",
            "2026-10-10 15:00:00",
            "2026-10-10 11:00:00",
        ]
        for it, end in zip(items, noon_ends):
            it["realSendTime"] = "2026-10-10 08:30:00.0"
            it["realSendTimeStr"] = "2026-10-10 08:30:00"
            it["realBackTime"] = end + ".0"
            it["realBackTimeStr"] = end
        self.use_items(items)

        def factory(work_dir, client):
            r = FakeRenderer(work_dir)
            self.created_renderers.append(r)
            real_route = r.render_route_png
            real_pcd = r.render_pcd_png

            def delay(route):
                idx = int(str(route).split("-")[-1].replace("MOCK", "") or 0)
                time.sleep(0.4 - 0.09 * idx)

            def route(route_id, out_png, pre_shot_js=""):
                delay(route_id)
                return real_route(route_id, out_png, pre_shot_js)

            def pcd(order, out_png, pre_shot_js=""):
                delay(order.get("id"))
                return real_pcd(order, out_png, pre_shot_js)

            r.render_route_png = route
            r.render_pcd_png = pcd
            return r

        self.renderer_factory = factory
        self.cfg = self.make_cfg(query_workers=2, render_workers=2)
        result = self.run_export(
            items=items, export_pcd=False, export_track=True, calibrate_noon=True
        )

        self.assertEqual(result["track_ok"], 3)
        noon = self.noon_calls[-1]
        self.assertEqual([seq_of_code(r["code"]) for r in noon], [1, 3])
        self.assertEqual([r["page"] for r in noon], [1, 3])
        self.assertIn("cal_end", noon[0])
        self.assertIn("cal_dur", noon[0])
        self.assertIn("[校准]", "\n".join(self.job.logs))
        merged = dict(self.merge_calls)
        self.assertEqual(
            [int(n.split("_", 1)[0]) for n in merged["_合并_轨迹.pdf"]], [1, 2, 3]
        )

    def test_单条失败继续其他单且重跑跳过成功项(self):
        items = make_items(4)
        self.use_items(items)
        fail_code = items[1]["runCode"]
        state = {"failed": False}

        def factory(work_dir, client):
            r = FakeRenderer(work_dir)
            self.created_renderers.append(r)
            real_render = r.render_pcd_pdf_one

            def maybe_fail(item, out_pdf, tag="", diag_dir=None):
                if item.get("runCode") == fail_code and not state["failed"]:
                    state["failed"] = True
                    raise RuntimeError("模拟单条渲染失败")
                return real_render(item, out_pdf, tag, diag_dir)

            r.render_pcd_pdf_one = maybe_fail
            return r

        self.renderer_factory = factory
        self.cfg = self.make_cfg(query_workers=2, render_workers=2)
        first = self.run_export(items=items, export_track=False)

        self.assertEqual(first["pcd_ok"], 3)
        self.assertEqual(first["pcd_fail"], 1)
        failed = [r for r in first["rows"] if r["code"] == fail_code][0]
        self.assertEqual(failed["pcd_status"], "失败")
        self.assertIn("模拟单条渲染失败", "\n".join(self.job.logs))
        manifest = ve.Manifest(self.outdir / "manifest.json")
        self.assertFalse(manifest.has("pcd", items[1]["id"]))
        self.assertEqual(len(manifest.data["pcd"]), 3)

        # 重跑：只渲染失败单，成功项从清单续传
        self.job = FakeJob("job-rerun")
        before = len(self.created_renderers)
        second = self.run_export(items=items, export_track=False)
        self.assertEqual(second["pcd_ok"], 4)
        self.assertEqual(second["pcd_fail"], 0)
        logs = "\n".join(self.job.logs)
        self.assertIn("已导出 3 单", logs)
        self.assertIn("本次待导出 1 单", logs)
        new_calls = [
            call for r in self.created_renderers[before:] for call in r.pcd_calls
        ]
        self.assertEqual(new_calls, [fail_code])

    def test_渲染错误后下一项使用新worker(self):
        items = make_items(2)
        self.use_items(items)
        fail_code = items[0]["runCode"]
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)

        def factory(work_dir, client):
            r = FakeRenderer(work_dir)
            self.created_renderers.append(r)
            real_render = r.render_pcd_pdf_one

            def maybe_fail(item, out_pdf, tag="", diag_dir=None):
                if item.get("runCode") == fail_code:
                    raise RuntimeError("渲染器崩溃")
                return real_render(item, out_pdf, tag, diag_dir)

            r.render_pcd_pdf_one = maybe_fail
            return r

        self.renderer_factory = factory
        result = self.run_export(items=items, export_track=False)

        self.assertEqual(result["pcd_ok"], 1)
        self.assertEqual(result["pcd_fail"], 1)
        self.assertEqual(len(self.created_renderers), 2)
        self.assertEqual(
            self.created_renderers[0].work_dir, self.created_renderers[1].work_dir
        )
        self.assertGreaterEqual(self.created_renderers[0].stop_calls, 1)
        self.assertEqual(self.created_renderers[1].pcd_calls, [items[1]["runCode"]])

    def test_每worker独立目录且成功清理(self):
        items = make_items(4)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=2)
        result = self.run_export(items=items, export_track=False)

        self.assertEqual(result["pcd_ok"], 4)
        dirs = {r.work_dir for r in self.created_renderers}
        self.assertEqual(len(dirs), 2)
        for d in dirs:
            self.assertEqual(d.parent, self.job_root())
            self.assertRegex(d.name, r"^worker-\d$")
        self.assertFalse(self.job_root().exists())

    def test_失败保留worker诊断目录(self):
        items = make_items(1)
        self.use_items(items)

        def factory(work_dir, client):
            r = FakeRenderer(work_dir)
            (r.work_dir / "diag.txt").write_text("诊断现场", encoding="utf-8")
            r.render_pcd_pdf_one = mock.Mock(side_effect=RuntimeError("渲染失败"))
            self.created_renderers.append(r)
            return r

        self.renderer_factory = factory
        result = self.run_export(items=items, export_track=False)

        self.assertEqual(result["pcd_fail"], 1)
        self.assertTrue((self.job_root() / "worker-0" / "diag.txt").exists())

    def test_渲染器启动失败按旧口径报错(self):
        items = make_items(1)
        self.use_items(items)
        self.renderer_factory = lambda work_dir, client: None

        with self.assertRaises(RuntimeError) as ctx:
            self.run_export(items=items, export_track=False)

        message = str(ctx.exception)
        self.assertIn("官方页面渲染器未启动", message)
        self.assertIn("设置", message)

    def test_取消停止全部渲染器且整批中止(self):
        items = make_items(4)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=2)
        entered = 0
        cv = threading.Condition()
        cancel_event = threading.Event()
        barrier = threading.Barrier(2)

        def factory(work_dir, client):
            r = FakeRenderer(work_dir)
            self.created_renderers.append(r)

            def render(item, out_pdf, tag="", diag_dir=None):
                nonlocal entered
                with cv:
                    entered += 1
                    cv.notify_all()
                try:
                    barrier.wait(timeout=10)
                except threading.BrokenBarrierError:
                    pass
                if cancel_event.is_set():
                    raise RuntimeError("浏览器已关闭（取消）")
                backend._write_stub_pdf(Path(out_pdf), "late")

            r.render_pcd_pdf_one = render
            return r

        self.renderer_factory = factory

        def canceller():
            with cv:
                cv.wait_for(lambda: entered >= 2, timeout=10)
            cancel_event.set()
            self.job.cancel()

        t = threading.Thread(target=canceller, name="canceller")
        t.start()
        try:
            with self.assertRaises(Cancelled):
                self.run_export(items=items, export_track=False)
        finally:
            t.join(timeout=10)

        self.assertTrue(self.job.cancelled)
        self.assertTrue(self.created_renderers)
        for r in self.created_renderers:
            self.assertGreaterEqual(r.stop_calls, 1)
        self.assertEqual(len(self.job.hooks), 1)

    def test_取消后不写轨迹CSV与Manifest且不记当前单成功(self):
        items = make_items(2)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)

        def pdf_then_cancel(html, pdf_path, chrome, budget="", electron="", job=None):
            # 打印成功返回，但用户已在打印期间取消：worker 落盘前必须再次检查
            backend._write_stub_pdf(Path(pdf_path), "[fake] html2pdf")
            self.job.cancel()

        with mock.patch.object(backend.ve, "html_to_pdf", side_effect=pdf_then_cancel):
            with self.assertRaises(Cancelled):
                self.run_export(items=items, export_pcd=False)

        self.assertFalse((self.outdir / "manifest.json").exists())
        self.assertEqual(list((self.outdir / "逐单").glob("*_轨迹点_*.csv")), [])
        self.assertFalse(
            any(label.startswith("轨迹 ") for _cur, _total, label in self.job.progress)
        )
        self.assertFalse((self.outdir / "_清单.csv").exists())

    def test_轨迹CSV写入中取消_无临时或最终CSV与成功回调(self):
        items = make_items(1)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)
        self.job = RaceJob()
        started = threading.Event()
        release = threading.Event()

        class SlowRows(list):
            def __iter__(self):
                started.set()
                release.wait(5)  # 真实写入循环中途暂停，强制取消落在 CSV 写入期间
                return super().__iter__()

        points = SlowRows(
            [
                {
                    "locatetime": "2026-10-10 09:00:00",
                    "dx": 1,
                    "dy": 2,
                    "speed": 3,
                    "direction": 4,
                }
            ]
        )

        def canceller():
            self.assertTrue(started.wait(5), "CSV 写入未开始")
            self.job.cancel()
            release.set()

        t = threading.Thread(target=canceller, name="cancel-during-csv")
        t.start()
        try:
            with mock.patch.object(
                backend.ve, "query_track_points", return_value=points
            ):
                with self.assertRaises(Cancelled):
                    self.run_export(items=items, export_pcd=False)
        finally:
            release.set()
            t.join(timeout=5)

        self.assertTrue(self.job.cancelled)
        self.assertFalse((self.outdir / "manifest.json").exists())
        self.assertEqual(list((self.outdir / "逐单").glob("*.tmp")), [])
        self.assertEqual(list((self.outdir / "逐单").glob("*_轨迹点_*.csv")), [])
        self.assertFalse(
            any(label.startswith("轨迹 ") for _cur, _total, label in self.job.progress)
        )
        self.assertFalse(any("个轨迹点" in line for line in self.job.logs))

    def test_轨迹清单与成功回调提交前取消_不写最终CSV与Manifest(self):
        items = make_items(1)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)
        self.job = RaceJob()

        def cancel_then_commit():
            t = threading.Thread(target=self.job.cancel, name="cancel-before-commit")
            t.start()
            t.join(timeout=5)
            self.assertFalse(t.is_alive(), "取消线程未结束")

        self.job.before_commit = cancel_then_commit
        with self.assertRaises(Cancelled):
            self.run_export(items=items, export_pcd=False)

        self.assertTrue(self.job.cancelled)
        self.assertFalse((self.outdir / "manifest.json").exists())
        self.assertEqual(list((self.outdir / "逐单").glob("*.tmp")), [])
        self.assertEqual(list((self.outdir / "逐单").glob("*_轨迹点_*.csv")), [])
        self.assertFalse(
            any(label.startswith("轨迹 ") for _cur, _total, label in self.job.progress)
        )
        self.assertFalse(any("个轨迹点" in line for line in self.job.logs))

    def test_PCD成功回调提交前取消_不写Manifest且无成功回调(self):
        items = make_items(1)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)
        self.job = RaceJob()

        def cancel_then_commit():
            t = threading.Thread(target=self.job.cancel, name="cancel-before-pcd")
            t.start()
            t.join(timeout=5)
            self.assertFalse(t.is_alive(), "取消线程未结束")

        self.job.before_commit = cancel_then_commit
        with self.assertRaises(Cancelled):
            self.run_export(items=items, export_pcd=True, export_track=False)

        self.assertTrue(self.job.cancelled)
        self.assertFalse((self.outdir / "manifest.json").exists())
        self.assertFalse(
            any(
                label.startswith("派车单 ") for _cur, _total, label in self.job.progress
            )
        )
        self.assertFalse(any("_派车单_" in line for line in self.job.logs))

    def _track_csv_path(self, items, index=0):
        seq = seq_of_code(items[index]["runCode"])
        csv_name = "%s_轨迹点_%s.csv" % (
            str(seq).zfill(len(str(len(items)))),
            items[index]["runCode"],
        )
        return self.outdir / "逐单" / csv_name

    def test_轨迹清单失败时恢复已有最终CSV且不留新内容或残留(self):
        items = make_items(1)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)
        final = self._track_csv_path(items)
        final.parent.mkdir(parents=True, exist_ok=True)
        old_bytes = b"\xef\xbb\xbfOLD-CSV-BYTES\n"
        final.write_bytes(old_bytes)
        real_put = ve.Manifest.put

        def failing_put(manifest, kind, key, fname):
            if kind == "track":
                raise OSError("manifest 写入失败")
            return real_put(manifest, kind, key, fname)

        with mock.patch.object(ve.Manifest, "put", failing_put):
            result = self.run_export(items=items, export_pcd=False)

        self.assertEqual(result["track_fail"], 1)
        self.assertTrue(final.exists(), "原最终 CSV 被误删")
        self.assertEqual(final.read_bytes(), old_bytes, "旧 CSV 未完整恢复")
        self.assertEqual(list((self.outdir / "逐单").glob("*.tmp")), [])
        self.assertEqual(list((self.outdir / "逐单").glob("*.bak*")), [])
        self.assertFalse((self.outdir / "manifest.json").exists())

    def test_轨迹替换失败时保留已有最终CSV并清理临时文件(self):
        items = make_items(1)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)
        final = self._track_csv_path(items)
        final.parent.mkdir(parents=True, exist_ok=True)
        old_bytes = b"\xef\xbb\xbfOLD-CSV-BYTES\n"
        final.write_bytes(old_bytes)
        real_replace = os.replace

        def flaky_replace(src, dst):
            if "_轨迹点_" in str(dst):
                raise OSError("模拟替换失败")
            return real_replace(src, dst)

        with mock.patch.object(backend.os, "replace", side_effect=flaky_replace):
            result = self.run_export(items=items, export_pcd=False)

        self.assertEqual(result["track_fail"], 1)
        self.assertTrue(final.exists(), "替换失败误删了既有最终 CSV")
        self.assertEqual(final.read_bytes(), old_bytes)
        self.assertEqual(list((self.outdir / "逐单").glob("*.tmp")), [])
        self.assertEqual(list((self.outdir / "逐单").glob("*.bak*")), [])
        self.assertFalse((self.outdir / "manifest.json").exists())
        self.assertIn("模拟替换失败", "\n".join(self.job.logs))

    def test_轨迹清单失败且原先无最终CSV时不留下新文件(self):
        items = make_items(1)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)
        final = self._track_csv_path(items)
        real_put = ve.Manifest.put

        def failing_put(manifest, kind, key, fname):
            if kind == "track":
                raise OSError("manifest 写入失败")
            return real_put(manifest, kind, key, fname)

        with mock.patch.object(ve.Manifest, "put", failing_put):
            result = self.run_export(items=items, export_pcd=False)

        self.assertEqual(result["track_fail"], 1)
        self.assertFalse(final.exists(), "原先无 final 时 manifest 失败仍留下新 CSV")
        self.assertEqual(list((self.outdir / "逐单").glob("*.tmp")), [])
        self.assertEqual(list((self.outdir / "逐单").glob("*.bak*")), [])
        self.assertFalse((self.outdir / "manifest.json").exists())

    def test_旧CSV恢复失败时保留备份现场且报清晰错误(self):
        items = make_items(1)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)
        final = self._track_csv_path(items)
        final.parent.mkdir(parents=True, exist_ok=True)
        old_bytes = b"\xef\xbb\xbfOLD-CSV-BYTES\n"
        final.write_bytes(old_bytes)
        real_put = ve.Manifest.put
        real_replace = os.replace

        def failing_put(manifest, kind, key, fname):
            if kind == "track":
                raise OSError("manifest 写入失败")
            return real_put(manifest, kind, key, fname)

        def flaky_replace(src, dst):
            if str(src).endswith(".bak"):
                raise OSError("模拟恢复失败")
            return real_replace(src, dst)

        with mock.patch.object(ve.Manifest, "put", failing_put):
            with mock.patch.object(backend.os, "replace", side_effect=flaky_replace):
                result = self.run_export(items=items, export_pcd=False)

        self.assertEqual(result["track_fail"], 1)
        backups = list((self.outdir / "逐单").glob("*.bak*"))
        self.assertEqual(len(backups), 1, "恢复失败必须保留旧文件备份")
        self.assertEqual(backups[0].read_bytes(), old_bytes)
        self.assertTrue(final.exists())
        self.assertNotEqual(final.read_bytes(), old_bytes)  # 新文件未覆盖备份
        self.assertEqual(list((self.outdir / "逐单").glob("*.tmp")), [])
        logs = "\n".join(self.job.logs)
        self.assertIn("旧 CSV 恢复失败", logs)
        self.assertIn(str(backups[0]), logs)

    def test_旧CSV暂存失败时原文件不动且无备份残留(self):
        items = make_items(1)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)
        final = self._track_csv_path(items)
        final.parent.mkdir(parents=True, exist_ok=True)
        old_bytes = b"\xef\xbb\xbfOLD-CSV-BYTES\n"
        final.write_bytes(old_bytes)
        real_replace = os.replace

        def flaky_replace(src, dst):
            if str(dst).endswith(".bak"):
                raise OSError("模拟暂存失败")
            return real_replace(src, dst)

        with mock.patch.object(backend.os, "replace", side_effect=flaky_replace):
            result = self.run_export(items=items, export_pcd=False)

        self.assertEqual(result["track_fail"], 1)
        self.assertTrue(final.exists())
        self.assertEqual(final.read_bytes(), old_bytes)
        self.assertEqual(list((self.outdir / "逐单").glob("*.tmp")), [])
        self.assertEqual(list((self.outdir / "逐单").glob("*.bak*")), [])
        self.assertIn("旧 CSV 暂存失败", "\n".join(self.job.logs))

    def test_取消不被慢启动阻塞且启动完成后自行关闭(self):
        started = threading.Event()
        release = threading.Event()
        created = []

        def factory(client, work_dir):
            started.set()
            release.wait(5)  # 旧实现持锁执行工厂，取消会被阻塞到此处超时
            r = FakeRenderer(work_dir)
            created.append(r)
            return r

        pool = parallel.RendererPool(1, [object()], factory, self.tmpdir / "workers")
        job = self.job
        job.add_cancel_hook(pool.stop)
        outcome = {}

        def starter():
            try:
                pool.worker(0).renderer
            except parallel.RendererPoolClosed:
                outcome["closed"] = True

        t = threading.Thread(target=starter, name="slow-start")
        t.start()
        self.assertTrue(started.wait(2))
        t0 = time.monotonic()
        try:
            job.cancel()
            elapsed = time.monotonic() - t0
        finally:
            release.set()
        t.join(timeout=5)

        self.assertLess(elapsed, 0.5)
        self.assertFalse(t.is_alive())
        self.assertTrue(outcome.get("closed"))
        self.assertEqual(len(created), 1)
        self.assertGreaterEqual(created[0].stop_calls, 1)
        self.assertIsNone(pool.worker(0).current_renderer)

    def test_数据准备与清单失败保留健康worker且不中止批次(self):
        items = make_items(4)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)
        bad_detail = items[1]["id"]
        bad_manifest = items[3]["id"]
        state = {"manifest_failed": False}
        real_details = ve.get_print_details
        real_put = ve.Manifest.put

        def flawed_details(client, ids, chunk):
            if bad_detail in ids:
                raise RuntimeError("打印详情查询失败")
            return real_details(client, ids, chunk)

        def flaky_put(manifest, kind, key, fname):
            if key == bad_manifest and not state["manifest_failed"]:
                state["manifest_failed"] = True
                raise OSError("清单写入失败")
            return real_put(manifest, kind, key, fname)

        with mock.patch.object(ve, "get_print_details", side_effect=flawed_details):
            with mock.patch.object(ve.Manifest, "put", flaky_put):
                result = self.run_export(items=items, export_track=False)

        self.assertEqual(result["pcd_ok"], 2)
        self.assertEqual(result["pcd_fail"], 2)
        self.assertEqual(len(self.created_renderers), 1)  # 未因数据/清单失败重建
        self.assertEqual(self.created_renderers[0].stop_calls, 1)  # 仅收尾 stop
        failed = [r["code"] for r in result["rows"] if r["pcd_status"] == "失败"]
        self.assertEqual(
            sorted(failed), sorted([items[1]["runCode"], items[3]["runCode"]])
        )
        logs = "\n".join(self.job.logs)
        self.assertIn("打印详情查询失败", logs)
        self.assertIn("清单写入失败", logs)

    def test_轨迹数据准备失败保留worker(self):
        items = make_items(2)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)
        real_points = ve.query_track_points
        state = {"failed": False}

        def flaky_points(client, vehicle_id, begin, end, strict=False):
            if not state["failed"]:
                state["failed"] = True
                raise RuntimeError("轨迹点查询失败")
            return real_points(client, vehicle_id, begin, end, strict=strict)

        with mock.patch.object(ve, "query_track_points", side_effect=flaky_points):
            result = self.run_export(items=items, export_pcd=False, export_track=True)

        self.assertEqual(result["track_ok"], 1)
        self.assertEqual(result["track_fail"], 1)
        self.assertEqual(len(self.created_renderers), 1)
        self.assertEqual(self.created_renderers[0].stop_calls, 1)
        self.assertIn("轨迹点查询失败", "\n".join(self.job.logs))

    def test_run直接执行Python编排且忽略导出引擎变量(self):
        items = make_items(2)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=1, mock_on=True)
        with mock.patch.dict(os.environ, {"UVMP_EXPORT_ENGINE": "go"}):
            with mock.patch.object(backend._config, "load", return_value=self.cfg):
                with mock.patch.object(
                    backend.uvmp,
                    "start_renderer",
                    side_effect=AssertionError("mock 不得启动浏览器"),
                ):
                    result = backend.run(self.job, self.params(items=items))
        self.assertEqual(result["pcd_ok"], 2)
        self.assertNotIn("goexport", sys.modules)
        self.assertEqual(self.job.hooks, [])

    def test_dev_mock十二单导出_命名顺序报告与断点续传(self):
        items = make_items(12)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=4, render_workers=2, mock_on=True)
        params = self.params(items=items)
        probe = Probe()
        real_stub = backend._write_stub_pdf

        def slow_stub(path, text):
            with probe.track():
                time.sleep(0.15)
                return real_stub(path, text)

        with mock.patch.object(backend, "_write_stub_pdf", side_effect=slow_stub):
            with mock.patch.object(backend._config, "load", return_value=self.cfg):
                with mock.patch.object(
                    backend.uvmp,
                    "start_renderer",
                    side_effect=AssertionError("mock 不得启动浏览器"),
                ):
                    result = backend.run(self.job, params)

        self.assertEqual(result["total"], 12)
        self.assertEqual(result["found"], 12)
        self.assertEqual(result["pcd_ok"], 12)
        self.assertEqual(result["track_ok"], 12)
        self.assertEqual(result["not_found"], [])
        self.assertGreaterEqual(probe.max_active, 2)
        self.assertLessEqual(probe.max_active, 2)
        self.assertEqual(probe.calls, 24)
        self.assertEqual(self.job.hooks, [])

        files = sorted(p.name for p in (self.outdir / "逐单").iterdir())
        self.assertEqual(len([n for n in files if "_派车单_" in n]), 12)
        self.assertEqual(len([n for n in files if "_轨迹_" in n]), 12)
        self.assertEqual(len([n for n in files if "_轨迹点_" in n]), 12)
        for prefix in ("01", "09", "10", "11", "12"):
            self.assertTrue(
                any(n.startswith(prefix + "_派车单_") for n in files), prefix
            )

        lines = (
            (self.outdir / "_清单.csv")
            .read_text(encoding="utf-8-sig")
            .strip()
            .splitlines()
        )
        self.assertEqual(
            [int(line.split(",")[0]) for line in lines[1:]], list(range(1, 13))
        )
        self.assertTrue((self.outdir / "_12点前结束行程.xlsx").exists())
        self.assertFalse((self.outdir / "_未找到.txt").exists())

        from pypdf import PdfReader

        self.assertEqual(
            len(PdfReader(str(self.outdir / "_合并_派车单.pdf")).pages), 12
        )
        self.assertEqual(len(PdfReader(str(self.outdir / "_合并_轨迹.pdf")).pages), 12)

        # 断点续传：同一批次目录重跑全部跳过
        self.job = FakeJob("job-rerun")
        with mock.patch.object(backend._config, "load", return_value=self.cfg):
            with mock.patch.object(
                backend.uvmp,
                "start_renderer",
                side_effect=AssertionError("mock 不得启动浏览器"),
            ):
                again = backend.run(self.job, params)
        self.assertEqual(again["pcd_ok"], 12)
        self.assertEqual(again["track_ok"], 12)
        logs = "\n".join(self.job.logs)
        self.assertIn("已导出 12 单", logs)
        self.assertIn("已导出 12 单，本次待导出 0 单", logs)
        self.assertIn("轨迹：待导出 0 条", logs)


class FakeHttpResponse:
    def __init__(self, status=200, headers=None, data=None):
        self.status_code = status
        self.headers = headers or {}
        self._data = data or {}
        self.text = json.dumps(self._data)

    def json(self):
        return self._data

    def raise_for_status(self):
        if self.status_code >= 400:
            raise RuntimeError("HTTP %d" % self.status_code)


class TokenSession:
    """首次 API 请求返回 401 + Token-Refresh，tokenRefresh 返回新 token，重试成功"""

    def __init__(self, auth, new_token="new", before_401=None):
        self.auth = auth
        self.new_token = new_token
        self.before_401 = before_401
        self.api_headers = []
        self.refresh_calls = 0

    def get(self, url, headers=None, params=None, timeout=None):
        if "tokenRefresh" in url:
            self.refresh_calls += 1
            return FakeHttpResponse(200, {}, {"accessToken": self.new_token})
        self.api_headers.append(dict(headers or {}))
        if len(self.api_headers) == 1:
            if self.before_401 is not None:
                self.before_401()
            return FakeHttpResponse(401, {"Token-Refresh": "true"}, {"msg": "expired"})
        return FakeHttpResponse(200, {}, {"successful": True, "resultValue": {"ok": 1}})


class ClientPoolTest(unittest.TestCase):
    def test_登录一次多客户端各自Session共享Auth(self):
        auth = ve.Auth("t", "r", "m")

        class Base:
            gateway = "http://fake/"

        base = Base()
        base.auth = auth
        with mock.patch.object(uvmp, "make_client", return_value=(base, auth)):
            clients, got = uvmp.make_clients({"dev": {}}, print, 3)

        self.assertIs(got, auth)
        self.assertEqual(len(clients), 3)
        self.assertIs(clients[0], base)
        self.assertIsNot(clients[1], clients[2])
        self.assertIsNot(clients[1].session, clients[2].session)
        self.assertIs(clients[1].auth, auth)
        self.assertIs(clients[2].auth, auth)

    def test_mock分支复用同一FakeClient(self):
        fake = devmock.FakeClient(devmock.make_items())
        with mock.patch.object(uvmp, "make_client", return_value=(fake, fake.auth)):
            clients, auth = uvmp.make_clients({"dev": {"mock": True}}, print, 3)

        self.assertEqual(len(clients), 3)
        self.assertTrue(all(c is fake for c in clients))
        self.assertIs(auth, fake.auth)


class AuthRefreshTest(unittest.TestCase):
    def _client(self, response_token, calls, probe):
        auth = ve.Auth("old", "refresh", "mtk")
        client = ve.UvmpClient("http://fake/", auth)

        class Resp:
            def json(self):
                return {"accessToken": response_token}

        class Session:
            def get(self, url, params=None, timeout=None):
                with probe.track():
                    calls.append(params.get("T"))
                    time.sleep(0.08)
                    return Resp()

        client.session = Session()
        return client, auth

    def test_共享Auth刷新串行且他线程已换新token不重复刷新(self):
        calls = []
        probe = Probe()
        client, auth = self._client("new", calls, probe)
        threads = [
            threading.Thread(
                target=client._refresh, args=("old",), name="refresh-%d" % i
            )
            for i in range(4)
        ]
        for t in threads:
            t.start()
        for t in threads:
            t.join()

        self.assertEqual(auth.token, "new")
        self.assertEqual(len(calls), 1)
        self.assertEqual(probe.max_active, 1)

    def test_刷新本身串行(self):
        calls = []
        probe = Probe()
        client, auth = self._client("old", calls, probe)
        threads = [
            threading.Thread(
                target=client._refresh, args=("old",), name="refresh-%d" % i
            )
            for i in range(2)
        ]
        for t in threads:
            t.start()
        for t in threads:
            t.join()

        self.assertEqual(len(calls), 2)
        self.assertEqual(probe.max_active, 1)

    def test_request_401刷新后重试使用新token(self):
        auth = ve.Auth("old", "refresh", "ab" * 16)
        client = ve.UvmpClient("http://fake/", auth)
        session = TokenSession(auth, "new")
        client.session = session

        data = client.get("demo")

        self.assertEqual(data["resultValue"], {"ok": 1})
        self.assertEqual(auth.token, "new")
        self.assertEqual(session.refresh_calls, 1)
        self.assertEqual(session.api_headers[0]["Auth_Tk"], "old")
        self.assertEqual(session.api_headers[1]["Auth_Tk"], "new")

    def test_request_401时他线程已换新token不重复刷新(self):
        auth = ve.Auth("old", "refresh", "ab" * 16)
        client = ve.UvmpClient("http://fake/", auth)
        session = TokenSession(
            auth, "unused", before_401=lambda: setattr(auth, "token", "newer")
        )
        client.session = session

        data = client.get("demo")

        self.assertEqual(data["resultValue"], {"ok": 1})
        self.assertEqual(auth.token, "newer")
        self.assertEqual(session.refresh_calls, 0)
        self.assertEqual(session.api_headers[1]["Auth_Tk"], "newer")


if __name__ == "__main__":
    unittest.main()
