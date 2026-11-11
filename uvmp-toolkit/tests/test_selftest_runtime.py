# -*- coding: utf-8 -*-
"""环境自检 run_sso 的运行时测试。

口径：mock 模式必须完全离线（走 devmock，不构造 requests 会话、不发只读查询）；
真实模式（sso / token）在凭证就绪后，用同一个 UvmpClient 发一次最小只读查询
（派车单第 1 页 1 条）确认服务端接受凭证——「能构造客户端」不等于「凭证就绪」，
获取凭证或只读查询任何一步失败都必须以异常透传，结果里要带检查口径与模式。
全部使用 fake 客户端，不联网、不启动真实请求。
"""

import sys
import tempfile
import threading
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
TOOLKIT = ROOT / "toolkit"
for _p in (str(ROOT), str(TOOLKIT)):
    if _p not in sys.path:
        sys.path.insert(0, _p)

import jobs  # noqa: E402
import selftest  # noqa: E402
import uvmp  # noqa: E402

TODAY = "2026-11-11"
TOKEN = "REAL_TOKEN_ABCDEF"


class FakeJob:
    """与 jobs.Job 相同的业务侧接口；不落日志文件"""

    def __init__(self):
        self.id = "job-selftest-sso"
        self.logs = []
        self.progress = []
        self.hooks = []
        self._cancel = threading.Event()

    def log(self, msg):
        self.logs.append(msg)

    def set_progress(self, current, total, label=""):
        self.progress.append((current, total, label))

    @property
    def cancelled(self):
        return self._cancel.is_set()

    def check_cancel(self):
        if self._cancel.is_set():
            raise jobs.Cancelled()

    def cancel(self):
        self._cancel.set()
        for fn in list(self.hooks):
            fn()

    def add_cancel_hook(self, fn):
        self.hooks.append(fn)
        if self._cancel.is_set():
            fn()


class FakeClient:
    """接口形状同 vehicle_export.UvmpClient（只用到 get 与 auth），完全离线"""

    def __init__(self, response=None, error=None, token=TOKEN):
        self.calls = []
        self.error = error
        self.response = (
            response
            if response is not None
            else {"successful": True, "resultValue": {"itemCount": 0, "items": []}}
        )
        self.auth = SimpleNamespace(
            token=token, refresh_token="REAL_REFRESH", mtk="REAL_MTK"
        )

    def get(self, base_path, p_data=None, desc=""):
        self.calls.append((base_path, dict(p_data or {}), desc))
        if self.error is not None:
            raise self.error
        return self.response


def _parse_params(p_data: dict) -> dict:
    import json

    return json.loads(p_data.get("params", "{}"))


class RunSsoTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.job = FakeJob()

    def _cfg(self, mock_mode=False, mode=""):
        ini_auth = {"mode": mode} if mode else {}
        return {"_ini": {"auth": ini_auth}, "dev": {"mock": bool(mock_mode)}}

    def _run(self, cfg, client=None, make_error=None):
        client = client or FakeClient()
        factory = (
            mock.Mock(side_effect=make_error)
            if make_error is not None
            else mock.Mock(return_value=(client, client.auth))
        )
        with (
            mock.patch.object(selftest._config, "load", return_value=cfg),
            mock.patch.object(selftest.uvmp, "make_client", factory),
        ):
            result = selftest.run_sso(self.job, {})
        return result, client, factory

    # ---- mock 模式：离线 ----

    def test_mock模式离线且不发只读查询(self):
        cfg = self._cfg(mock_mode=True)
        with (
            mock.patch.object(selftest._config, "load", return_value=cfg),
            mock.patch.object(
                selftest.ve,
                "query_pcd_page_filter",
                side_effect=AssertionError("mock 模式禁止发只读查询"),
            ) as probe,
            # mock 分支不得构造 requests 会话（devmock 完全离线）
            mock.patch.object(
                uvmp, "make_client", wraps=uvmp.make_client
            ) as real_factory,
            mock.patch("requests.Session", side_effect=AssertionError("不应联网")) as s,
        ):
            result = selftest.run_sso(self.job, {})
        self.assertTrue(real_factory.called)
        s.assert_not_called()
        probe.assert_not_called()
        self.assertTrue(result["ok"])
        self.assertTrue(result["mock"])
        self.assertEqual(result["mode"], "mock")
        self.assertEqual(result["check"], "offline")
        self.assertGreater(result["token_len"], 0)
        log_text = "\n".join(self.job.logs)
        self.assertIn("mock", log_text)
        self.assertIn("未访问网络", log_text)

    # ---- 真实模式：最小只读鉴权 ----

    def test_真实sso模式用UvmpClient发一次只读查询并回传口径(self):
        result, client, factory = self._run(self._cfg())
        factory.assert_called_once()
        self.assertEqual(len(client.calls), 1, "真实模式必须且只发一次只读查询")
        base_path, p_data, desc = client.calls[0]
        self.assertIn("pcdmanager", base_path)
        params = _parse_params(p_data)
        self.assertEqual(params["pageIndex"], 1)
        self.assertEqual(params["pageSize"], 1)
        self.assertIn("planSendTime=", params["filter"])
        self.assertIn("planSendTime2=", params["filter"])
        # 查询走的是派车单菜单口径（tycl_* 三件套）
        self.assertEqual(p_data.get("tycl_sonMenuId"), "pcdmanager")
        self.assertTrue(desc)
        self.assertTrue(result["ok"])
        self.assertFalse(result["mock"])
        self.assertEqual(result["mode"], "sso")
        self.assertEqual(result["check"], "readonly")
        self.assertIn("只读", result["check_desc"])
        self.assertEqual(result["token_len"], len(TOKEN))
        self.assertEqual(result["probe"]["item_count"], 0)
        self.assertEqual(result["probe"]["page_size"], 1)
        log_text = "\n".join(self.job.logs)
        self.assertIn("只读", log_text)

    def test_真实token模式回传mode_token(self):
        result, client, _ = self._run(self._cfg(mode="token"))
        self.assertEqual(result["mode"], "token")
        self.assertEqual(len(client.calls), 1)

    def test_只读查询命中条数进入结果(self):
        client = FakeClient(
            response={
                "successful": True,
                "resultValue": {"itemCount": 37, "items": [{"id": "1"}]},
            }
        )
        result, _client, _ = self._run(self._cfg(), client=client)
        self.assertEqual(result["probe"]["item_count"], 37)

    # ---- 失败必须透传，不得“构造即就绪” ----

    def test_凭证获取失败透传(self):
        with self.assertRaises(RuntimeError) as ctx:
            self._run(
                self._cfg(),
                make_error=RuntimeError("未配置 SSO 账号密码：请在设置页填写"),
            )
        self.assertIn("凭证获取失败", str(ctx.exception))
        self.assertIn("未配置 SSO 账号密码", str(ctx.exception))

    def test_只读鉴权失败透传(self):
        client = FakeClient(error=RuntimeError("凭证校验失败（HTTP 401）"))
        with self.assertRaises(RuntimeError) as ctx:
            self._run(self._cfg(), client=client)
        self.assertEqual(len(client.calls), 1)
        self.assertIn("只读鉴权", str(ctx.exception))
        self.assertIn("HTTP 401", str(ctx.exception))

    def test_接口返回successful_false视为鉴权失败(self):
        client = FakeClient(
            response={"successful": False, "message": "登录超时，请重新登录"}
        )
        with self.assertRaises(RuntimeError) as ctx:
            self._run(self._cfg(), client=client)
        self.assertIn("只读鉴权", str(ctx.exception))
        self.assertIn("登录超时", str(ctx.exception))

    def test_响应不是对象时视为失败(self):
        client = FakeClient(response=["not-a-dict"])
        with self.assertRaises(RuntimeError) as ctx:
            self._run(self._cfg(), client=client)
        self.assertIn("只读鉴权", str(ctx.exception))

    def test_凭证token为空时报错(self):
        client = FakeClient(token="")
        with self.assertRaises(RuntimeError) as ctx:
            self._run(self._cfg(), client=client)
        self.assertIn("token", str(ctx.exception))
        self.assertEqual(client.calls, [])

    def test_token模式缺凭证由make_client报错并透传(self):
        with self.assertRaises(RuntimeError) as ctx:
            self._run(
                self._cfg(mode="token"),
                make_error=RuntimeError(
                    "config.ini [auth] mode=token，但 token/refresh_token/mtk 未填齐"
                ),
            )
        self.assertIn("凭证获取失败", str(ctx.exception))
        self.assertIn("token", str(ctx.exception))


if __name__ == "__main__":
    unittest.main()
