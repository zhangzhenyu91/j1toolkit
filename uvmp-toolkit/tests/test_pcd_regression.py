# -*- coding: utf-8 -*-
"""派车导出编排与 xlsx 工具回归测试（覆盖本轮审查问题）。

全部 fake：假渲染器、devmock 假客户端、临时目录与临时状态目录；不连真实 SSO/派车系统，
不读真实密钥。覆盖：
  - xlsx 读侧安全上限（成员数/解压总量/行数）与 DTD/实体炸弹拒绝
  - xlsx 与 清单/未找到/12 点前报表的原子写（失败保留旧件、不留临时文件）
  - 单号表头精确优先匹配
  - 渲染器启动失败整批快速中止、任务终态为失败
  - 断点重跑：历史 noon 记录保留、manifest 命中校验实际文件、合并只收真实文件
  - 集群汇总缺件：不写 manifest、标记失败可重跑
"""

import csv
import io
import json
import os
import sys
import tempfile
import unittest
import zipfile
from datetime import datetime
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
TOOLKIT = ROOT / "toolkit"
APP_DIR = TOOLKIT / "apps" / "pcd_export"
for _p in (str(ROOT), str(TOOLKIT), str(APP_DIR), str(Path(__file__).resolve().parent)):
    if _p not in sys.path:
        sys.path.insert(0, _p)

import backend  # noqa: E402
import jobs  # noqa: E402
import vehicle_export as ve  # noqa: E402
import xlsx_util  # noqa: E402
from test_pcd_parallel import (  # noqa: E402
    ExportFixture,
    FakeRenderer,
    make_items,
)

SHEET_HEAD = (
    '<?xml version="1.0" encoding="UTF-8"?>'
    '<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">'
)
SHEET_TAIL = "</worksheet>"


def _sheet_xml(rows_xml="", padding=""):
    return "%s<sheetData>%s</sheetData>%s%s" % (
        SHEET_HEAD,
        rows_xml,
        padding,
        SHEET_TAIL,
    )


def _write_zip(path, parts):
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as zf:
        for name, data in parts.items():
            zf.writestr(name, data)


def _row_dict(seq):
    return {
        "seq": seq,
        "code": "A-%d" % seq,
        "vehicle": "晋AD1000%d" % seq,
        "date": "2026-10-10",
        "pcd_file": "",
        "pcd_status": "未导出",
        "track_file": "",
        "track_status": "未导出",
        "note": "",
    }


def make_noon_items(count=3):
    """3 单里第 1、3 单结束于 12 点前（与既有 noon 用例口径一致）"""
    items = make_items(count)
    ends = ["2026-10-10 10:30:00", "2026-10-10 15:00:00", "2026-10-10 11:00:00"]
    for it, end in zip(items, ends):
        it["realSendTime"] = "2026-10-10 08:30:00.0"
        it["realBackTime"] = end + ".0"
        it["realBackTimeStr"] = end
    return items


def make_other_batch(day):
    """另一批次：单号与订单 id 都与上一批不同（避开上一批 manifest），
    只留第 2 单结束于 12 点前，用来比对跨批次记录是否被错误认领"""
    items = make_items(3, day=day)
    dates = day.strftime("%Y-%m-%d")
    ends = ["15:00:00", "11:20:00", "16:00:00"]
    for i, (it, end) in enumerate(zip(items, ends)):
        it["id"] = "OTHER%04d" % (i + 1)
        it["vehicleId"] = "OB%04d" % (i + 1)
        it["realSendTime"] = dates + " 08:30:00.0"
        it["realSendTimeStr"] = dates + " 08:30:00"
        it["realBackTime"] = dates + " " + end + ".0"
        it["realBackTimeStr"] = dates + " " + end
    return items


class XlsxSafetyTest(unittest.TestCase):
    """读侧安全上限与表头定位"""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.tmpdir = Path(self._tmp.name)

    def test_拒绝DOCTYPE与实体声明(self):
        bomb = (
            '<?xml version="1.0"?><!DOCTYPE lolz [<!ENTITY lol "lol">'
            '<!ENTITY lol2 "&lol;&lol;&lol;">]><worksheet><sheetData/></worksheet>'
        )
        path = self.tmpdir / "bomb.xlsx"
        _write_zip(path, {"xl/worksheets/sheet1.xml": bomb})

        with self.assertRaises(RuntimeError) as ctx:
            xlsx_util.read_xlsx_rows(path)

        self.assertIn("DTD", str(ctx.exception))

    def test_解压总量超限拒绝读取(self):
        path = self.tmpdir / "big.xlsx"
        _write_zip(
            path,
            {"xl/worksheets/sheet1.xml": _sheet_xml(padding="x" * 4000)},
        )
        with mock.patch.object(xlsx_util, "MAX_ZIP_TOTAL_BYTES", 1024):
            with self.assertRaises(RuntimeError) as ctx:
                xlsx_util.read_xlsx_rows(path)
        self.assertIn("总量", str(ctx.exception))

    def test_成员数超限拒绝读取(self):
        parts = {"xl/worksheets/sheet1.xml": _sheet_xml()}
        for i in range(10):
            parts["xl/pad%02d.xml" % i] = "<a/>"
        path = self.tmpdir / "many.xlsx"
        _write_zip(path, parts)
        with mock.patch.object(xlsx_util, "MAX_ZIP_MEMBERS", 5):
            with self.assertRaises(RuntimeError) as ctx:
                xlsx_util.read_xlsx_rows(path)
        self.assertIn("成员", str(ctx.exception))

    def test_成员解压后超限拒绝读取(self):
        """不信任 zip 头里声明的大小：实际解压超过单成员上限即拒绝"""
        path = self.tmpdir / "member-bomb.xlsx"
        _write_zip(
            path,
            {"xl/worksheets/sheet1.xml": _sheet_xml(padding="x" * 4000)},
        )
        with mock.patch.object(xlsx_util, "MAX_ZIP_MEMBER_BYTES", 1024):
            with self.assertRaises(RuntimeError) as ctx:
                xlsx_util.read_xlsx_rows(path)
        self.assertIn("上限", str(ctx.exception))

    def test_行数超限拒绝读取(self):
        rows_xml = "".join(
            '<row r="%d"><c r="A%d"><v>%d</v></c></row>' % (i, i, i)
            for i in range(1, 51)
        )
        path = self.tmpdir / "rows.xlsx"
        _write_zip(path, {"xl/worksheets/sheet1.xml": _sheet_xml(rows_xml)})
        with mock.patch.object(xlsx_util, "MAX_ROWS", 20):
            with self.assertRaises(RuntimeError) as ctx:
                xlsx_util.read_xlsx_rows(path)
        self.assertIn("行", str(ctx.exception))

    def test_写出失败保留旧件且不留临时文件(self):
        path = self.tmpdir / "out.xlsx"
        path.write_bytes(b"OLD-XLSX")

        with mock.patch.object(
            xlsx_util.os, "replace", side_effect=OSError("模拟落盘失败")
        ):
            with self.assertRaises(OSError):
                xlsx_util.write_table_xlsx(
                    path, "统计", ["甲"], [["乙"]], log=lambda *_: None
                )

        self.assertEqual(path.read_bytes(), b"OLD-XLSX")
        self.assertEqual(list(self.tmpdir.glob("*.tmp")), [])
        self.assertEqual([p.name for p in self.tmpdir.iterdir()], ["out.xlsx"])

    def test_写出成功替换旧件(self):
        path = self.tmpdir / "out.xlsx"
        path.write_bytes(b"OLD-XLSX")

        xlsx_util.write_table_xlsx(path, "统计", ["甲"], [["乙"]], log=lambda *_: None)

        self.assertNotEqual(path.read_bytes(), b"OLD-XLSX")
        self.assertEqual(list(self.tmpdir.glob("*.tmp")), [])
        self.assertEqual(xlsx_util.read_xlsx_rows(path), [["甲"], ["乙"]])

    def test_表头精确优先匹配派车单号列(self):
        rows = [
            ["原派车单号", "派车单号", "记录ID"],
            ["OLD-1", "A-PC-1", "x"],
            ["OLD-2", "A-PC-2", "y"],
        ]

        result = xlsx_util.extract_codes_from_rows(rows)

        self.assertEqual([item["code"] for item in result], ["A-PC-1", "A-PC-2"])

    def test_表头精确优先匹配预计用车时间而非小时数(self):
        rows = [
            ["派车单号", "预计用车时间(小时)", "预计用车时间"],
            ["A1", "2", "2026-10-02 08:00:00"],
        ]

        result = xlsx_util.extract_codes_from_rows(rows)

        self.assertEqual(len(result), 1)
        self.assertEqual(result[0]["date_hint"], "2026-10-02")

    def test_无精确命中时退化为包含匹配(self):
        rows = [
            ["派车单号（必填）", "用车日期"],
            ["A1", "2026-10-03"],
        ]

        result = xlsx_util.extract_codes_from_rows(rows)

        self.assertEqual([item["code"] for item in result], ["A1"])
        self.assertEqual(result[0]["date_hint"], "2026-10-03")


class ReportAtomicityTest(ExportFixture):
    """清单 / 未找到 / 12 点前报表：原子写，失败保留旧件"""

    def _flaky_replace(self, suffix):
        real_replace = os.replace

        def flaky(src, dst):
            if str(dst).endswith(suffix):
                raise OSError("模拟写入失败：%s" % suffix)
            return real_replace(src, dst)

        return flaky

    def test_清单写入失败保留旧件且无临时残留(self):
        csv_path = self.outdir / "_清单.csv"
        csv_path.write_text("OLD-CSV\n", encoding="utf-8-sig")

        with mock.patch.object(
            backend.os, "replace", side_effect=self._flaky_replace("_清单.csv")
        ):
            with self.assertRaises(OSError):
                backend._write_reports(
                    self.outdir, {1: _row_dict(1)}, [], lambda *_: None
                )

        self.assertEqual(csv_path.read_text(encoding="utf-8-sig"), "OLD-CSV\n")
        self.assertEqual(list(self.outdir.glob("*.tmp")), [])

    def test_未找到写入失败保留旧件且无临时残留(self):
        txt_path = self.outdir / "_未找到.txt"
        txt_path.write_text("OLD-MISSING\n", encoding="utf-8")

        with mock.patch.object(
            backend.os, "replace", side_effect=self._flaky_replace("_未找到.txt")
        ):
            with self.assertRaises(OSError):
                backend._write_reports(
                    self.outdir,
                    {1: _row_dict(1)},
                    [{"code": "NF-1"}],
                    lambda *_: None,
                )

        self.assertEqual(txt_path.read_text(encoding="utf-8"), "OLD-MISSING\n")
        self.assertEqual(list(self.outdir.glob("*.tmp")), [])

    def test_报表写入成功且无临时残留(self):
        backend._write_reports(
            self.outdir, {1: _row_dict(1)}, [{"code": "NF-1"}], lambda *_: None
        )

        csv_text = (self.outdir / "_清单.csv").read_text(encoding="utf-8-sig")
        self.assertIn("A-1", csv_text)
        self.assertIn("NF-1", (self.outdir / "_未找到.txt").read_text(encoding="utf-8"))
        self.assertEqual(list(self.outdir.glob("*.tmp")), [])

    def test_十二点前报表写入失败保留旧件且可重跑补出(self):
        items = make_noon_items(3)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)
        self.run_export(items=items, export_pcd=False, export_track=True)
        report = self.outdir / "_12点前结束行程.xlsx"
        old_bytes = report.read_bytes()
        self.assertTrue(old_bytes)
        self.assertEqual([r["seq"] for r in self.noon_calls[-1]], [1, 3])

        with mock.patch.object(
            backend.os,
            "replace",
            side_effect=self._flaky_replace("_12点前结束行程.xlsx"),
        ):
            with self.assertRaises(OSError):
                self.run_export(items=items, export_pcd=False, export_track=True)

        self.assertEqual(report.read_bytes(), old_bytes, "报表写入失败毁掉了旧件")
        self.assertEqual(list(self.outdir.glob("*.tmp")), [])

        # 重跑：全部命中 manifest，历史 noon 记录仍应补出完整报表
        again = self.run_export(items=items, export_pcd=False, export_track=True)
        self.assertEqual(again["track_ok"], 3)
        self.assertEqual([r["seq"] for r in self.noon_calls[-1]], [1, 3])
        self.assertEqual([r["page"] for r in self.noon_calls[-1]], [1, 3])
        self.assertEqual(list(self.outdir.glob("*.tmp")), [])


class RendererFailFastTest(ExportFixture):
    """渲染器启动失败：整批快速中止，任务不记成功"""

    def test_派车单渲染器启动异常整批快速中止(self):
        items = make_items(8)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=2, render_workers=2)
        calls = []

        def factory(work_dir, client):
            calls.append(Path(work_dir))
            raise RuntimeError("浏览器启动失败")

        self.renderer_factory = factory
        with self.assertRaises(RuntimeError) as ctx:
            self.run_export(items=items, export_pcd=True, export_track=False)

        message = str(ctx.exception)
        self.assertIn("官方页面渲染器未启动", message)
        self.assertIn("设置", message)
        # 每个渲染槽只尝试启动一次，其余工作项立即中止（不再逐单重试启动）
        self.assertGreaterEqual(len(calls), 1)
        self.assertLessEqual(len(calls), 2)
        self.assertFalse((self.outdir / "manifest.json").exists())
        self.assertFalse((self.outdir / "_清单.csv").exists())
        self.assertEqual(list((self.outdir / "逐单").glob("*.pdf")), [])
        self.assertFalse(
            any(label.startswith("派车单 ") for _c, _t, label in self.job.progress)
        )

    def test_轨迹渲染器启动异常整批快速中止(self):
        items = make_noon_items(6)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=2, render_workers=2)
        calls = []

        def factory(work_dir, client):
            calls.append(Path(work_dir))
            raise RuntimeError("浏览器启动失败")

        self.renderer_factory = factory
        with self.assertRaises(RuntimeError) as ctx:
            self.run_export(items=items, export_pcd=False, export_track=True)

        self.assertIn("官方页面渲染器未启动", str(ctx.exception))
        self.assertLessEqual(len(calls), 2)
        self.assertFalse((self.outdir / "manifest.json").exists())
        self.assertFalse((self.outdir / "_12点前结束行程.xlsx").exists())

    def test_渲染器启动失败任务终态为失败(self):
        items = make_items(2)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=2)
        self.renderer_factory = lambda work_dir, client: (_ for _ in ()).throw(
            RuntimeError("浏览器启动失败")
        )
        manager = jobs.JobManager()
        job = manager.create("pcd_export", "渲染器启动失败")
        params = self.params(items=items, export_pcd=True, export_track=False)

        with mock.patch.object(backend._config, "load", return_value=self.cfg):
            manager.run_sync(job, lambda j: backend.run(j, params), echo=False)

        self.assertEqual(job.status, "failed")
        self.assertIn("官方页面渲染器未启动", job.error)
        self.assertEqual(job.result, {})


class ClientTrackTest(ExportFixture):
    """客户端轨迹链路：CSV 转义/防公式注入、未知响应与空轨迹一律失败"""

    def _track_pdfs(self):
        return sorted((self.outdir / "逐单").glob("*_轨迹_*.pdf"))

    def _track_csvs(self):
        return sorted((self.outdir / "逐单").glob("*_轨迹点_*.csv"))

    def test_轨迹点CSV走csvwriter转义并防公式注入(self):
        items = make_items(1)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)
        points = [
            {
                "locatetime": '2026-10-10 09:00:00,"引号"\n换行',
                "dx": "=1+1",
                "dy": "-0.5",
                "speed": "+2",
                "direction": "@SUM(A1)",
            }
        ]

        with mock.patch.object(ve, "query_track_points", return_value=points) as query:
            result = self.run_export(items=items, export_pcd=False, export_track=True)

        self.assertEqual(result["track_ok"], 1)
        # 必须按 strict 口径查询，否则响应形状变化会被当成「没有轨迹点」
        self.assertTrue(query.call_args.kwargs.get("strict"), query.call_args)
        csvs = self._track_csvs()
        self.assertEqual(len(csvs), 1)
        rows = list(csv.reader(io.StringIO(csvs[0].read_text(encoding="utf-8-sig"))))
        self.assertEqual(rows[0], ["时间", "经度", "纬度", "速度", "方向"])
        self.assertEqual(len(rows), 2, "含逗号/换行的时间字段被拆成了多行")
        self.assertEqual(rows[1][0], '2026-10-10 09:00:00,"引号"\n换行')
        self.assertEqual(
            rows[1][1:],
            ["'=1+1", "-0.5", "'+2", "'@SUM(A1)"],
            "公式前导字符未转文本，或负数被误加前缀",
        )

    def test_未知轨迹响应记失败且不写manifest与最终CSV(self):
        items = make_items(2)
        self.use_items(items)
        self.client.points_mode = "unknown"
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)

        result = self.run_export(items=items, export_pcd=False, export_track=True)

        self.assertEqual(result["track_ok"], 0)
        self.assertEqual(result["track_fail"], 2)
        for row in result["rows"]:
            self.assertEqual(row["track_status"], "失败")
            self.assertEqual(row["track_file"], "")
        self.assertFalse((self.outdir / "manifest.json").exists())
        self.assertEqual(self._track_csvs(), [])
        self.assertEqual(self._track_pdfs(), [])
        logs = "\n".join(self.job.logs)
        self.assertIn("轨迹点响应无法识别", logs)

    def test_空轨迹记失败且不写manifest与最终CSV(self):
        items = make_items(2)
        self.use_items(items)
        self.client.points_mode = "empty"
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)

        result = self.run_export(items=items, export_pcd=False, export_track=True)

        self.assertEqual(result["track_ok"], 0)
        self.assertEqual(result["track_fail"], 2)
        self.assertFalse((self.outdir / "manifest.json").exists())
        self.assertEqual(self._track_csvs(), [])
        self.assertEqual(self._track_pdfs(), [])
        self.assertIn("轨迹点为空", "\n".join(self.job.logs))
        # 失败不阻断其他单：同一批里的派车单阶段照样完成
        self.assertEqual(result["pcd_ok"], 0)  # 本用例未勾选派车单

    def test_空轨迹不影响同批其他单的轨迹导出(self):
        items = make_items(3)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)
        real_points = ve.query_track_points
        bad_vehicle = items[1]["vehicleId"]

        def flaky(client, vehicle_id, begin, end, strict=False):
            if vehicle_id == bad_vehicle:
                return []
            return real_points(client, vehicle_id, begin, end, strict=strict)

        with mock.patch.object(ve, "query_track_points", side_effect=flaky):
            result = self.run_export(items=items, export_pcd=False, export_track=True)

        self.assertEqual(result["track_ok"], 2)
        self.assertEqual(result["track_fail"], 1)
        manifest = ve.Manifest(self.outdir / "manifest.json")
        self.assertFalse(manifest.has("track", items[1]["id"]))
        self.assertTrue(manifest.has("track", items[0]["id"]))
        self.assertEqual(len(self._track_csvs()), 2)
        self.assertEqual(len(self._track_pdfs()), 2)


class ResumeTest(ExportFixture):
    """断点重跑：noon 记录保留、manifest 命中校验文件、合并只收真实文件"""

    def test_断点重跑保留历史noon清单(self):
        items = make_noon_items(3)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)
        self.run_export(items=items, export_pcd=False, export_track=True)
        self.assertEqual([r["seq"] for r in self.noon_calls[-1]], [1, 3])

        again = self.run_export(items=items, export_pcd=False, export_track=True)

        self.assertEqual(again["track_ok"], 3)
        self.assertIn("轨迹：待导出 0 条", "\n".join(self.job.logs))
        noon = self.noon_calls[-1]
        self.assertEqual([r["seq"] for r in noon], [1, 3])
        self.assertEqual([r["page"] for r in noon], [1, 3])
        self.assertEqual(
            [r["end"] for r in noon],
            ["2026-10-10 10:30:00", "2026-10-10 11:00:00"],
        )
        rows = xlsx_util.read_xlsx_rows(self.outdir / "_12点前结束行程.xlsx")
        self.assertEqual(
            [row[1] for row in rows[1:]],
            [items[0]["runCode"], items[2]["runCode"]],
        )

    def test_同批续跑仅重渲一单仍保留同序号历史noon(self):
        items = make_noon_items(3)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)
        self.run_export(items=items, export_pcd=False, export_track=True)
        index_path = self.outdir / "_12点前结束行程索引.json"
        index = json.loads(index_path.read_text(encoding="utf-8"))
        self.assertEqual(
            {k: v["code"] for k, v in index.items()},
            {"1": items[0]["runCode"], "3": items[2]["runCode"]},
        )

        # 序号 1 的轨迹文件丢失 → 本次重渲；序号 3 仍命中 manifest，其历史只能来自索引
        manifest = ve.Manifest(self.outdir / "manifest.json")
        (self.outdir / "逐单" / manifest.get("track", items[0]["id"])).unlink()
        before = len(self.created_renderers)

        again = self.run_export(items=items, export_pcd=False, export_track=True)

        self.assertEqual(again["track_ok"], 3)
        rerendered = [c for r in self.created_renderers[before:] for c in r.track_calls]
        self.assertEqual(len(rerendered), 1, rerendered)
        noon = self.noon_calls[-1]
        self.assertEqual([r["seq"] for r in noon], [1, 3])
        self.assertEqual(
            [r["code"] for r in noon], [items[0]["runCode"], items[2]["runCode"]]
        )
        self.assertEqual([r["page"] for r in noon], [1, 3])
        rows = xlsx_util.read_xlsx_rows(self.outdir / "_12点前结束行程.xlsx")
        self.assertEqual(
            [row[1] for row in rows[1:]],
            [items[0]["runCode"], items[2]["runCode"]],
        )

    def test_跨批次同序号不同单号不继承历史noon(self):
        first = make_noon_items(3)  # 上一批：序号 1、3 结束于 12 点前
        self.use_items(first)
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)
        self.run_export(items=first, export_pcd=False, export_track=True)
        self.assertEqual([r["seq"] for r in self.noon_calls[-1]], [1, 3])

        # 固定输出目录（batch_subdir=false）导第二批：序号 1/3 与上一批相同但单号不同，
        # 本批只有序号 2 结束于 12 点前 → 只能认领本批 2，上一批 1/3 不得被写进本批
        second = make_other_batch(datetime(2026, 10, 11, 9, 0))
        self.assertNotEqual(
            [it["runCode"] for it in first], [it["runCode"] for it in second]
        )
        again = self.run_export(items=second, export_pcd=False, export_track=True)

        self.assertEqual(again["track_ok"], 3)
        noon = self.noon_calls[-1]
        self.assertEqual([r["seq"] for r in noon], [2])
        self.assertEqual([r["code"] for r in noon], [second[1]["runCode"]])
        self.assertEqual([r["page"] for r in noon], [2])
        rows = xlsx_util.read_xlsx_rows(self.outdir / "_12点前结束行程.xlsx")
        self.assertEqual(
            [row[1] for row in rows[1:]],
            [second[1]["runCode"]],
            "跨批次历史被写进了本批报表",
        )
        index = json.loads(
            (self.outdir / "_12点前结束行程索引.json").read_text(encoding="utf-8")
        )
        self.assertEqual(
            {k: v["code"] for k, v in index.items()}, {"2": second[1]["runCode"]}
        )

    def test_派车单续传命中但文件缺失则重新导出(self):
        items = make_items(3)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)
        self.run_export(items=items, export_pcd=True, export_track=False)
        manifest = ve.Manifest(self.outdir / "manifest.json")
        fname = manifest.get("pcd", items[1]["id"])
        self.assertTrue(fname)
        (self.outdir / "逐单" / fname).unlink()
        before = len(self.created_renderers)

        again = self.run_export(items=items, export_pcd=True, export_track=False)

        self.assertEqual(again["pcd_ok"], 3)
        rerendered = [c for r in self.created_renderers[before:] for c in r.pcd_calls]
        self.assertEqual(rerendered, [items[1]["runCode"]])
        self.assertTrue((self.outdir / "逐单" / fname).is_file())
        self.assertIn("文件缺失", "\n".join(self.job.logs))

    def test_轨迹续传命中但文件缺失则重新导出(self):
        items = make_noon_items(3)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)
        self.run_export(items=items, export_pcd=False, export_track=True)
        manifest = ve.Manifest(self.outdir / "manifest.json")
        fname = manifest.get("track", items[1]["id"])
        self.assertTrue(fname)
        (self.outdir / "逐单" / fname).unlink()
        before = len(self.created_renderers)

        again = self.run_export(items=items, export_pcd=False, export_track=True)

        self.assertEqual(again["track_ok"], 3)
        rerendered = [c for r in self.created_renderers[before:] for c in r.track_calls]
        self.assertEqual(len(rerendered), 1, rerendered)
        self.assertTrue(str(rerendered[0]).endswith(items[1]["id"]))
        self.assertTrue((self.outdir / "逐单" / fname).is_file())

    def test_渲染成功但产出文件缺失时记失败且不进合并(self):
        items = make_items(3)
        self.use_items(items)
        self.cfg = self.make_cfg(query_workers=1, render_workers=1)
        self.run_export(items=items, export_pcd=True, export_track=False)
        missing_seq = items[2]["seq"] if "seq" in items[2] else 3
        missing_code = items[2]["runCode"]
        manifest = ve.Manifest(self.outdir / "manifest.json")
        (self.outdir / "逐单" / manifest.get("pcd", items[2]["id"])).unlink()

        def factory(work_dir, client):
            r = FakeRenderer(work_dir)
            real_render = r.render_pcd_pdf_one

            def render_then_lose(item, out_pdf, tag="", diag_dir=None):
                real_render(item, out_pdf, tag, diag_dir)
                Path(out_pdf).unlink()  # 渲染返回成功但产出随后丢失

            if True:
                r.render_pcd_pdf_one = render_then_lose
            self.created_renderers.append(r)
            return r

        self.renderer_factory = factory
        again = self.run_export(items=items, export_pcd=True, export_track=False)

        self.assertEqual(again["pcd_ok"], 2)
        self.assertEqual(again["pcd_fail"], 1)
        failed = [r for r in again["rows"] if r["code"] == missing_code][0]
        self.assertEqual(failed["pcd_status"], "失败")
        manifest = ve.Manifest(self.outdir / "manifest.json")
        # 记录即便仍在，也不得被当成已导出（每次续传都重新核实文件）
        self.assertEqual(
            backend._resume_hit(
                manifest, "pcd", items[2]["id"], self.outdir / "逐单", self.job.log
            ),
            "",
        )
        merged = dict(self.merge_calls)["_合并_派车单.pdf"]
        self.assertEqual([int(n.split("_", 1)[0]) for n in merged], [1, 2])
        self.assertNotIn(missing_seq, [int(n.split("_", 1)[0]) for n in merged])
        csv_text = (self.outdir / "_清单.csv").read_text(encoding="utf-8-sig")
        self.assertIn("失败", csv_text)


class ClusterFinalizeTest(unittest.TestCase):
    """集群汇总：复制/manifest 前核实文件存在，缺件可重跑"""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.tmpdir = Path(self._tmp.name)
        self.logs = []
        self.shard = self.tmpdir / "shard"
        self.shard.mkdir()

    def _stub(self, name):
        path = self.shard / name
        backend._write_stub_pdf(path, "[stub] %s" % name)
        return path

    def _row(self, seq, code, pcd_file="", pcd_status=""):
        return {
            "seq": seq,
            "code": code,
            "oid": code,
            "vehicle": "晋AD1000%d" % seq,
            "date": "2026-10-10",
            "pcd_file": pcd_file,
            "pcd_status": pcd_status,
            "track_file": "",
            "track_status": "",
            "note": "",
        }

    def _finalize(self, rows, files):
        return backend.finalize_cluster(
            self.tmpdir,
            "测试批次",
            True,
            "20261010_090000",
            rows,
            [],
            [],
            files,
            export_track=False,
            log=self.logs.append,
        )

    def test_缺件不写manifest且标记失败可重跑(self):
        rows = [
            self._row(1, "A1", "001_派车单_A1.pdf", "成功"),
            self._row(2, "A2", "002_派车单_A2.pdf", "成功"),
            self._row(3, "A3", "003_派车单_A3.pdf", "成功"),
        ]
        files = [
            self._stub("001_派车单_A1.pdf"),
            # 002 的分片产物缺失（节点上传遗漏/磁盘丢失）
            self._stub("003_派车单_A3.pdf"),
        ]

        result = self._finalize(rows, files)

        final = Path(result["dir"])
        manifest = ve.Manifest(final / "manifest.json")
        self.assertTrue(manifest.has("pcd", "A1"))
        self.assertFalse(manifest.has("pcd", "A2"), "缺件不得写入 manifest")
        self.assertTrue(manifest.has("pcd", "A3"))
        failed = [r for r in result["rows"] if r["code"] == "A2"][0]
        self.assertEqual(failed["pcd_status"], "失败")
        self.assertIn("文件缺失", failed["note"])
        self.assertEqual((result["pcd_ok"], result["pcd_fail"]), (2, 1))
        self.assertIn("缺件", "\n".join(self.logs))
        merged = final / "_合并_派车单.pdf"
        self.assertTrue(merged.is_file())
        from pypdf import PdfReader

        self.assertEqual(len(PdfReader(str(merged)).pages), 2)

    def test_缺件补齐后重跑写入manifest(self):
        rows = [
            self._row(1, "A1", "001_派车单_A1.pdf", "成功"),
            self._row(2, "A2", "002_派车单_A2.pdf", "成功"),
        ]
        self._finalize(rows, [self._stub("001_派车单_A1.pdf")])
        manifest_path = None
        for path in self.tmpdir.glob("*_导出_20261010_090000/manifest.json"):
            manifest_path = path
        self.assertIsNotNone(manifest_path)
        self.assertFalse(ve.Manifest(manifest_path).has("pcd", "A2"))

        # 补齐缺件后重跑：协调器重新下发全新的行结果，同一批次目录补上 A2
        fresh_rows = [
            self._row(1, "A1", "001_派车单_A1.pdf", "成功"),
            self._row(2, "A2", "002_派车单_A2.pdf", "成功"),
        ]
        files = [self._stub("001_派车单_A1.pdf"), self._stub("002_派车单_A2.pdf")]
        result = self._finalize(fresh_rows, files)

        manifest = ve.Manifest(Path(result["dir"]) / "manifest.json")
        self.assertTrue(manifest.has("pcd", "A1"))
        self.assertTrue(manifest.has("pcd", "A2"))
        self.assertEqual((result["pcd_ok"], result["pcd_fail"]), (2, 0))
        merged = Path(result["dir"]) / "_合并_派车单.pdf"
        from pypdf import PdfReader

        self.assertEqual(len(PdfReader(str(merged)).pages), 2)


if __name__ == "__main__":
    unittest.main()
