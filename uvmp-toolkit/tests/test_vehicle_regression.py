# -*- coding: utf-8 -*-
"""车辆导出与轨迹底层回归测试。

覆盖：请求头凭证一致快照、刷新响应轮换 mtk/refresh_token、刷新与网络重试预算独立、
轨迹点 CSV 转义与公式注入、空轨迹/无法识别响应不写成功清单、合并 PDF 过滤目录与缺项、
12 点校准仅同日正时长并给跨天/脏数据备注、清单损坏告警留副本、渲染诊断 URL 脱敏、
CDP 启动参数与 profile 权限收紧、开发模拟凭证与空轨迹模拟。

全部使用假 Session、假渲染器、假浏览器与临时目录，不连接真实 SSO/派车系统/浏览器。
"""

import contextlib
import csv
import io
import json
import sys
import tempfile
import unittest
import zipfile
from datetime import datetime
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
TOOLKIT = ROOT / "toolkit"
for _p in (str(ROOT), str(TOOLKIT)):
    if _p not in sys.path:
        sys.path.insert(0, _p)

import devmock  # noqa: E402
import official_track as ot  # noqa: E402
import track_time_rewrite_trial as trial  # noqa: E402
import vehicle_export as ve  # noqa: E402


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


class HeaderSnapshotTest(unittest.TestCase):
    """_headers 的三个凭证必须来自同一次快照，签名与 Auth_Tk 不得错位"""

    def test_headers只取一次快照(self):
        auth = ve.Auth("t1", "r1", "ab" * 16)
        auth.snapshot = mock.Mock(return_value=("tk", "rf", "cd" * 16))
        auth.token, auth.refresh_token, auth.mtk = "live", "live", "ef" * 16
        client = ve.UvmpClient("http://fake/", auth)
        captured = {}

        def fake_token(token, mtk, temp_url, new_data):
            captured["token"] = token
            captured["mtk"] = mtk
            return "MAC sig"

        with mock.patch.object(ve, "make_x_content_token", side_effect=fake_token):
            headers = client._headers("/demo", {})

        self.assertEqual(auth.snapshot.call_count, 1)
        self.assertEqual(headers["Auth_Tk"], "tk")
        self.assertEqual(headers["Refresh_Tk"], "rf")
        self.assertEqual(captured["token"], headers["Auth_Tk"])
        self.assertEqual(captured["mtk"], "cd" * 16)

    def test_请求重试每次取新快照且单次内一致(self):
        auth = ve.Auth("t1", "r1", "ab" * 16)
        client = ve.UvmpClient("http://fake/", auth, retries=2)
        signed = []

        class Session:
            def __init__(self):
                self.calls = 0
                self.headers = []

            def get(self, url, headers=None, params=None, timeout=None):
                self.headers.append(dict(headers or {}))
                self.calls += 1
                if self.calls == 1:
                    raise RuntimeError("网络抖动")
                return FakeHttpResponse(
                    200, {}, {"successful": True, "resultValue": {"ok": 1}}
                )

        session = Session()
        client.session = session

        def fake_token(token, mtk, temp_url, new_data):
            signed.append((token, mtk))
            return "MAC sig"

        with mock.patch.object(ve, "make_x_content_token", side_effect=fake_token):
            with mock.patch.object(ve.time, "sleep"):
                data = client.get("demo")

        self.assertEqual(data["resultValue"], {"ok": 1})
        self.assertEqual(len(signed), 2)
        for (token, _mtk), headers in zip(signed, session.headers):
            self.assertEqual(headers["Auth_Tk"], token)
            self.assertEqual(headers["Refresh_Tk"], "r1")


class RefreshRotationTest(unittest.TestCase):
    """tokenRefresh 可能轮换 mtk/refresh_token；刷新失败预算与网络重试预算相互独立"""

    def test_刷新轮换mtk与refreshToken(self):
        auth = ve.Auth("old", "r1", "ab" * 16)
        client = ve.UvmpClient("http://fake/", auth)

        class Session:
            def get(self, url, params=None, timeout=None):
                return FakeHttpResponse(
                    200,
                    {},
                    {
                        "accessToken": "new",
                        "refreshToken": "r2",
                        "mtk": "cd" * 16,
                    },
                )

        client.session = Session()
        self.assertTrue(client._refresh("old"))
        self.assertEqual(auth.token, "new")
        self.assertEqual(auth.refresh_token, "r2")
        self.assertEqual(auth.mtk, "cd" * 16)

    def test_刷新响应下划线字段也识别且缺项不覆盖(self):
        auth = ve.Auth("old", "r1", "ab" * 16)
        client = ve.UvmpClient("http://fake/", auth)

        class Session:
            def get(self, url, params=None, timeout=None):
                return FakeHttpResponse(
                    200, {}, {"accessToken": "new", "mtk": "cd" * 16}
                )

        client.session = Session()
        self.assertTrue(client._refresh("old"))
        self.assertEqual(auth.refresh_token, "r1")
        self.assertEqual(auth.mtk, "cd" * 16)

        class Session2:
            def get(self, url, params=None, timeout=None):
                return FakeHttpResponse(
                    200, {}, {"accessToken": "new2", "refresh_token": "r3"}
                )

        client.session = Session2()
        self.assertTrue(client._refresh("new"))
        self.assertEqual(auth.refresh_token, "r3")
        self.assertEqual(auth.mtk, "cd" * 16)

    def test_刷新后网络重试预算不减少(self):
        auth = ve.Auth("old", "r1", "ab" * 16)
        client = ve.UvmpClient("http://fake/", auth, retries=2)

        class Session:
            def __init__(self):
                self.api_calls = 0
                self.refresh_calls = 0

            def get(self, url, headers=None, params=None, timeout=None):
                if "tokenRefresh" in url:
                    self.refresh_calls += 1
                    return FakeHttpResponse(200, {}, {"accessToken": "new"})
                self.api_calls += 1
                if self.api_calls <= 2:  # 先 401 刷新，再一次网络失败
                    if self.api_calls == 1:
                        return FakeHttpResponse(
                            401, {"Token-Refresh": "true"}, {"msg": "expired"}
                        )
                    raise RuntimeError("网络抖动")
                return FakeHttpResponse(
                    200, {}, {"successful": True, "resultValue": {"ok": 1}}
                )

        session = Session()
        client.session = session
        with mock.patch.object(ve.time, "sleep"):
            data = client.get("demo")

        self.assertEqual(data["resultValue"], {"ok": 1})
        self.assertEqual(session.refresh_calls, 1)
        self.assertEqual(session.api_calls, 3)

    def test_刷新次数上限后报凭证失败(self):
        auth = ve.Auth("old", "r1", "ab" * 16)
        client = ve.UvmpClient("http://fake/", auth, retries=1, max_refresh=2)

        class Session:
            def __init__(self):
                self.refresh_calls = 0

            def get(self, url, headers=None, params=None, timeout=None):
                if "tokenRefresh" in url:
                    self.refresh_calls += 1
                    return FakeHttpResponse(
                        200, {}, {"accessToken": "n%d" % self.refresh_calls}
                    )
                return FakeHttpResponse(
                    401, {"Token-Refresh": "true"}, {"msg": "expired"}
                )

        session = Session()
        client.session = session
        with self.assertRaises(ve.AuthError):
            client.get("demo")
        self.assertEqual(session.refresh_calls, 2)


class TrackCsvTest(unittest.TestCase):
    def test_csv转义逗号引号与换行(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "轨迹点.csv"
            points = [
                {
                    "locatetime": '2026-09-01 08:00:00,"x"\n2',
                    "dx": "112.5,1",
                    "dy": '37.8"5',
                    "speed": 30,
                    "direction": "45",
                }
            ]
            ve.write_track_points_csv(path, points)
            rows = list(csv.reader(io.StringIO(path.read_text(encoding="utf-8-sig"))))
        self.assertEqual(rows[0], ["时间", "经度", "纬度", "速度", "方向"])
        self.assertEqual(rows[1][0], '2026-09-01 08:00:00,"x"\n2')
        self.assertEqual(rows[1][1], "112.5,1")
        self.assertEqual(rows[1][2], '37.8"5')
        self.assertEqual(rows[1][3], "30")

    def test_csv防公式注入(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "轨迹点.csv"
            points = [
                {
                    "locatetime": "=1+1",
                    "dx": "+112.5",
                    "dy": "@SUM(A1)",
                    "speed": "-30",
                    "direction": "45",
                },
                {"locatetime": "\tTAB", "dx": "\rCR", "dy": 37.8, "speed": 0},
            ]
            ve.write_track_points_csv(path, points)
            rows = list(csv.reader(io.StringIO(path.read_text(encoding="utf-8-sig"))))
        self.assertEqual(rows[1][:5], ["'=1+1", "'+112.5", "'@SUM(A1)", "-30", "45"])
        self.assertTrue(rows[2][0].startswith("'"))
        self.assertTrue(rows[2][1].startswith("'"))
        self.assertEqual(rows[2][2], "37.8")
        self.assertEqual(rows[2][3], "0")

    def test_csv纯数值不加前缀(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "轨迹点.csv"
            ve.write_track_points_csv(
                path, [{"locatetime": "2026-09-01 08:00:00", "dx": -0.5, "dy": "-37.8"}]
            )
            rows = list(csv.reader(io.StringIO(path.read_text(encoding="utf-8-sig"))))
        self.assertEqual(rows[1][1], "-0.5")
        self.assertEqual(rows[1][2], "-37.8")


class FakeRenderer:
    def __init__(self):
        self.route_calls = []
        self.pcd_calls = []

    def render_route_png(self, route_id, out_png, pre_shot_js=""):
        self.route_calls.append(route_id)
        Path(out_png).write_bytes(b"\x89PNG\r\n\x1a\n")

    def render_pcd_png(self, order, out_png, pre_shot_js=""):
        self.pcd_calls.append(order.get("runCode"))
        Path(out_png).write_bytes(b"\x89PNG\r\n\x1a\n")


class ExportTrackTest(unittest.TestCase):
    """CLI 轨迹导出：只有真正拿到轨迹点并出图才算成功"""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.outdir = Path(self._tmp.name)
        self.out = io.StringIO()
        self.addCleanup(self.out.close)

    def _order(self, begin="2026-09-01 08:30:00", end="2026-09-01 10:30:00"):
        order = devmock._mk_order(1, datetime(2026, 9, 1, 8, 0), "2")
        order["realSendTime"] = begin
        order["realBackTime"] = end
        return order

    def _run(self, order, points, cfg=None, seg=None, merge=None, noon=None):
        manifest = ve.Manifest(self.outdir / "manifest.json")
        patchers = [
            mock.patch.object(ve, "html_to_pdf", self._fake_html_to_pdf),
            mock.patch.object(ve, "query_track_points", side_effect=points),
            mock.patch.object(ve, "query_track_segment", return_value=seg),
            mock.patch.object(
                ve, "merge_pdfs", side_effect=merge or (lambda *a, **k: False)
            ),
            mock.patch.object(
                ve, "write_noon_end_report", side_effect=noon or (lambda *a, **k: None)
            ),
        ]
        for p in patchers:
            p.start()
            self.addCleanup(p.stop)
        with contextlib.redirect_stdout(self.out):
            ve.export_track(
                client=object(),
                cfg=cfg or {"calibrate_noon": False},
                orders=[order],
                outdir=self.outdir,
                chrome="fake-chrome",
                manifest=manifest,
                renderer=FakeRenderer(),
            )
        return manifest

    @staticmethod
    def _fake_html_to_pdf(html, pdf_path, chrome, *a, **k):
        Path(pdf_path).write_bytes(b"%PDF-1.4\nfake")

    def test_空轨迹不写成功清单与CSV(self):
        order = self._order()
        manifest = self._run(order, points=lambda *a, **k: [])
        self.assertFalse(manifest.has("track", order["id"]))
        self.assertEqual(list((self.outdir / "逐单").glob("*.csv")), [])
        self.assertIn("轨迹点为空", self.out.getvalue())

    def test_无法识别响应不写成功清单(self):
        order = self._order()

        class WeirdClient:
            def get(self, base_path, p_data=None, desc=""):
                return {"successful": True, "rows": 3, "data": None}

        manifest = ve.Manifest(self.outdir / "manifest.json")
        with contextlib.ExitStack() as stack:
            stack.enter_context(
                mock.patch.object(ve, "html_to_pdf", self._fake_html_to_pdf)
            )
            stack.enter_context(
                mock.patch.object(ve, "query_track_segment", return_value=None)
            )
            stack.enter_context(mock.patch.object(ve, "merge_pdfs", return_value=False))
            stack.enter_context(mock.patch.object(ve, "write_noon_end_report"))
            with contextlib.redirect_stdout(self.out):
                ve.export_track(
                    client=WeirdClient(),
                    cfg={"calibrate_noon": False},
                    orders=[order],
                    outdir=self.outdir,
                    chrome="fake-chrome",
                    manifest=manifest,
                    renderer=FakeRenderer(),
                )
        self.assertFalse(manifest.has("track", order["id"]))
        self.assertEqual(list((self.outdir / "逐单").glob("*.csv")), [])
        self.assertIn("无法识别", self.out.getvalue())

    def test_成功导出写入CSV与清单(self):
        order = self._order()
        points = [
            {
                "locatetime": "2026-09-01 08:31:00",
                "dx": 112.5,
                "dy": 37.8,
                "speed": 30,
                "direction": 45,
            }
        ]
        manifest = self._run(order, points=lambda *a, **k: list(points))
        self.assertTrue(manifest.has("track", order["id"]))
        csv_files = list((self.outdir / "逐单").glob("*.csv"))
        self.assertEqual(len(csv_files), 1)
        self.assertIn(
            "2026-09-01 08:31:00", csv_files[0].read_text(encoding="utf-8-sig")
        )

    def test_跨天行程不校准并备注(self):
        order = self._order("2026-09-01 23:00:00", "2026-09-02 00:30:00")
        rows = []
        seg = {
            "id": "SEG-1",
            "starttime": "2026-09-01 23:00:00",
            "endtime": "2026-09-02 00:30:00",
        }
        points = [{"locatetime": "2026-09-01 23:10:00", "dx": 1, "dy": 2}]
        self._run(
            order,
            points=lambda *a, **k: list(points),
            cfg={"calibrate_noon": True},
            seg=seg,
            noon=lambda r, p, log=print: rows.extend(r),
        )
        self.assertEqual(len(rows), 1)
        self.assertIn("跨天", rows[0].get("note", ""))
        self.assertNotIn("cal_end", rows[0])

    def test_同日12点前正常校准不备注(self):
        order = self._order("2026-09-01 08:30:00", "2026-09-01 10:32:00")
        rows = []
        seg = {
            "id": "SEG-1",
            "starttime": "2026-09-01 08:30:00",
            "endtime": "2026-09-01 10:32:00",
        }
        points = [{"locatetime": "2026-09-01 08:31:00", "dx": 1, "dy": 2}]
        self._run(
            order,
            points=lambda *a, **k: list(points),
            cfg={"calibrate_noon": True},
            seg=seg,
            noon=lambda r, p, log=print: rows.extend(r),
        )
        self.assertEqual(rows[0]["cal_end"], "2026-09-01 12:32:00")
        self.assertNotIn("note", rows[0])


class MergePdfTest(unittest.TestCase):
    def _pdf(self, path):
        from pypdf import PdfWriter

        w = PdfWriter()
        w.add_blank_page(width=100, height=100)
        with open(path, "wb") as fp:
            w.write(fp)
        return path

    def test_合并过滤目录与缺项(self):
        with tempfile.TemporaryDirectory() as tmp:
            tmpdir = Path(tmp)
            a = self._pdf(tmpdir / "a.pdf")
            b = self._pdf(tmpdir / "b.pdf")
            folder = tmpdir / "逐单"
            folder.mkdir()
            missing = tmpdir / "missing.pdf"
            out = tmpdir / "_合并.pdf"
            logs = []
            ok = ve.merge_pdfs([folder, a, missing, b, ""], out, log=logs.append)
            self.assertTrue(ok)
            self.assertTrue(out.exists())
            self.assertIn("跳过", "\n".join(logs))
            from pypdf import PdfReader

            self.assertEqual(len(PdfReader(str(out)).pages), 2)

    def test_有效文件不足两个不合并(self):
        with tempfile.TemporaryDirectory() as tmp:
            tmpdir = Path(tmp)
            a = self._pdf(tmpdir / "a.pdf")
            folder = tmpdir / "逐单"
            folder.mkdir()
            out = tmpdir / "_合并.pdf"
            self.assertFalse(ve.merge_pdfs([a, folder], out, log=lambda *_: None))
            self.assertFalse(out.exists())


class NoonCalibrationTest(unittest.TestCase):
    def test_同日正时长校准到12点后(self):
        cal = ve.calibrate_noon_end(
            {"starttime": "2026-09-01 08:30:00", "endtime": "2026-09-01 10:32:00"}
        )
        self.assertIsNotNone(cal)
        new_dt, old_dur, new_dur = cal
        self.assertEqual(new_dt, datetime(2026, 9, 1, 12, 32, 0))
        self.assertEqual(old_dur, 2 * 3600 + 2 * 60)
        self.assertEqual(new_dur, 4 * 3600 + 2 * 60)

    def test_跨天返回None并给原因(self):
        seg = {
            "starttime": "2026-09-01 23:00:00",
            "endtime": "2026-09-02 00:30:00",
        }
        self.assertIsNone(ve.calibrate_noon_end(seg))
        self.assertIn("跨天", ve.noon_skip_reason(seg))

    def test_非正时长返回None并给原因(self):
        seg = {
            "starttime": "2026-09-01 09:00:00",
            "endtime": "2026-09-01 08:00:00",
        }
        self.assertIsNone(ve.calibrate_noon_end(seg))
        self.assertIn("时长", ve.noon_skip_reason(seg))

    def test_无法解析返回None并给原因(self):
        seg = {"starttime": "脏数据", "endtime": "2026-09-01 08:00:00"}
        self.assertIsNone(ve.calibrate_noon_end(seg))
        self.assertIn("无法解析", ve.noon_skip_reason(seg))

    def test_12点后无需校准且无跳过原因(self):
        seg = {
            "starttime": "2026-09-01 08:30:00",
            "endtime": "2026-09-01 13:00:00",
        }
        self.assertIsNone(ve.calibrate_noon_end(seg))
        self.assertEqual(ve.noon_skip_reason(seg), "")

    def test_报告带备注列(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "_12点前结束行程.xlsx"
            ve.write_noon_end_report(
                [
                    {
                        "page": 1,
                        "code": "PCD1",
                        "vehicle": "晋AD10000",
                        "end": "2026-09-02 00:30:00",
                        "note": "跨天，未校准",
                    }
                ],
                out,
                log=lambda *_: None,
            )
            with zipfile.ZipFile(out) as zf:
                sheet = zf.read("xl/worksheets/sheet1.xml").decode("utf-8")
        self.assertIn("备注", sheet)
        self.assertIn("跨天，未校准", sheet)

    def test_报告无备注时不加列(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "_12点前结束行程.xlsx"
            ve.write_noon_end_report(
                [
                    {
                        "page": 1,
                        "code": "PCD1",
                        "vehicle": "晋AD10000",
                        "end": "2026-09-01 10:32:00",
                    }
                ],
                out,
                log=lambda *_: None,
            )
            with zipfile.ZipFile(out) as zf:
                sheet = zf.read("xl/worksheets/sheet1.xml").decode("utf-8")
        self.assertNotIn("备注", sheet)


class ManifestCorruptTest(unittest.TestCase):
    def test_损坏清单告警并留corrupt副本(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "manifest.json"
            raw = '{"pcd": {"A": "x.pdf"},'
            path.write_text(raw, encoding="utf-8")
            buf = io.StringIO()
            with contextlib.redirect_stdout(buf):
                m = ve.Manifest(path)
            corrupt = path.with_name(path.name + ".corrupt")
            self.assertTrue(corrupt.exists())
            self.assertEqual(corrupt.read_text(encoding="utf-8"), raw)
            self.assertEqual(m.data, {"pcd": {}, "track": {}})
            self.assertIn("损坏", buf.getvalue())
            m.put("pcd", "B", "b.pdf")
            self.assertTrue(m.has("pcd", "B"))


class OfficialTrackSecurityTest(unittest.TestCase):
    def test_诊断URL脱敏(self):
        text = (
            "http://uvmp.example/uvmp-web/factoryLayout/index.html"
            "#home?T=eyJhbGciOiJIUzI1NiJ9.SECRETPAYLOAD.sig&x=1"
        )
        red = ot.redact_secrets(text)
        self.assertNotIn("SECRETPAYLOAD", red)
        self.assertIn("uvmp.example", red)

    def test_console日志落盘脱敏(self):
        with tempfile.TemporaryDirectory() as tmp:
            browser = object.__new__(ot.CdpBrowser)
            browser.console_log = [
                "[net.401] http://uvmp.example/api?token=SECRETTOKEN&a=1",
                "[net.fail] ERR http://uvmp.example/api?T=SECRETJWT",
            ]
            browser.call = mock.Mock(return_value={})
            out = Path(tmp)
            browser.dump_diag(out, "t1")
            written = (out / "diag_console_t1.log").read_text(encoding="utf-8")
        self.assertNotIn("SECRETTOKEN", written)
        self.assertNotIn("SECRETJWT", written)

    def test_登录失败诊断不打印原始token(self):
        with tempfile.TemporaryDirectory() as tmp:
            renderer = ot.OfficialTrackRenderer(
                "chrome", Path(tmp) / "official_chrome.log"
            )

            class FakeBrowser:
                def eval(self, expr, timeout=None):
                    if expr == "location.href":
                        return "http://uvmp.example/uvmp-web/#home?T=SECRETJWT"
                    return None

                def call(self, *a, **k):
                    return {}

            renderer.browser = FakeBrowser()
            buf = io.StringIO()
            with contextlib.redirect_stdout(buf):
                renderer._dump_failure("登录超时")
        self.assertIn("uvmp.example", buf.getvalue())
        self.assertNotIn("SECRETJWT", buf.getvalue())

    def test_启动参数收紧且不破坏渲染(self):
        with tempfile.TemporaryDirectory() as tmp:
            log_file = Path(tmp) / "browser.log"

            class FakeProc:
                def poll(self):
                    return None

                def terminate(self):
                    pass

                def wait(self, timeout=None):
                    return 0

                def kill(self):
                    pass

            class FakeWs:
                def __init__(self):
                    self.sent = []

                def settimeout(self, t):
                    pass

                def send(self, data):
                    self.sent.append(json.loads(data))

                def recv(self):
                    return json.dumps({"id": self.sent[-1]["id"], "result": {}})

                def close(self):
                    pass

            def popen(cmd, **kw):
                prof = Path(log_file).parent / ".chrome-official-tmp"
                (prof / "DevToolsActivePort").write_text(
                    "9222\nfake\n", encoding="utf-8"
                )
                return FakeProc()

            captured = {}

            def create_ws(url, **kw):
                captured["url"] = url
                captured["kw"] = kw
                return FakeWs()

            def urlopen(url, timeout=None):
                class Resp:
                    def read(self):
                        return json.dumps(
                            [
                                {
                                    "type": "page",
                                    "webSocketDebuggerUrl": "ws://127.0.0.1:9222/devtools/page/1",
                                }
                            ]
                        ).encode("utf-8")

                return Resp()

            with mock.patch.object(
                ot.subprocess, "Popen", side_effect=popen
            ) as popen_mock:
                with mock.patch.object(ot.time, "sleep"):
                    with mock.patch.object(ot.urllib.request, "urlopen", urlopen):
                        with mock.patch.object(
                            ot.websocket, "create_connection", side_effect=create_ws
                        ):
                            browser = ot.CdpBrowser("chrome", log_file=log_file)
        cmd = popen_mock.call_args[0][0]
        self.assertFalse(
            [a for a in cmd if a.startswith("--remote-allow-origins") and "*" in a]
        )
        self.assertTrue(captured["kw"].get("suppress_origin"))
        self.assertEqual(browser.port, 9222)
        browser.close()

    @unittest.skipIf(sys.platform.startswith("win"), "Windows 不校验 POSIX 权限位")
    def test_profile目录权限收紧(self):
        with tempfile.TemporaryDirectory() as tmp:
            log_file = Path(tmp) / "browser.log"
            with mock.patch.object(
                ot.subprocess, "Popen", side_effect=OSError("no chrome")
            ):
                with self.assertRaises(Exception):
                    ot.CdpBrowser("chrome", log_file=log_file)
            prof = Path(tmp) / ".chrome-official-tmp"
            self.assertEqual(prof.stat().st_mode & 0o777, 0o700)


class DevMockTest(unittest.TestCase):
    def test_假凭证可签名并可轮换(self):
        client = devmock.fake_client({"dev": {}}, log=lambda *_: None)
        sig = ve.make_x_content_token(client.auth.token, client.auth.mtk, "/x", {})
        self.assertTrue(sig.startswith("MAC "))
        before = (client.auth.token, client.auth.refresh_token, client.auth.mtk)
        rotated = client.token_refresh()
        self.assertNotEqual(rotated["accessToken"], before[0])
        self.assertNotEqual(rotated["refreshToken"], before[1])
        self.assertNotEqual(rotated["mtk"], before[2])
        bytes.fromhex(rotated["mtk"])

    def test_空轨迹与无法识别响应模拟(self):
        client = devmock.fake_client({"dev": {}}, log=lambda *_: None)
        client.points_mode = "empty"
        data = client.get(
            "queryTrackHisByVehicle", {"beginTimeStr": "2026-09-01 08:00:00"}
        )
        self.assertEqual(data["data"], [])
        client.points_mode = "unknown"
        data = client.get(
            "queryTrackHisByVehicle", {"beginTimeStr": "2026-09-01 08:00:00"}
        )
        self.assertNotIn("data", data)
        with self.assertRaises(RuntimeError):
            ve.query_track_points(
                client, "V1", "2026-09-01 08:00:00", "2026-09-01 10:00:00", strict=True
            )


class TrialCalibrationTest(unittest.TestCase):
    def test_同日正时长校准(self):
        new_dt = trial.calibrate_end("2026-09-01 10:32:00", "2026-09-01 08:00:00")
        self.assertEqual(new_dt, datetime(2026, 9, 1, 12, 32, 0))

    def test_跨天不校准(self):
        self.assertIsNone(
            trial.calibrate_end("2026-09-02 00:30:00", "2026-09-01 23:00:00")
        )

    def test_非正时长不校准(self):
        self.assertIsNone(
            trial.calibrate_end("2026-09-01 08:00:00", "2026-09-01 09:00:00")
        )


if __name__ == "__main__":
    unittest.main()
