# -*- coding: utf-8 -*-
"""集群配置脱敏/保存单测：密钥不回传，空值或掩码值不覆盖已有密钥。"""

import os
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "toolkit"))

import config  # noqa: E402


class ClusterConfigTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.path = Path(self.tmp.name) / "config.json"
        self.old = os.environ.get("UVMP_TOOLKIT_CONFIG")
        os.environ["UVMP_TOOLKIT_CONFIG"] = str(self.path)

    def tearDown(self):
        if self.old is None:
            os.environ.pop("UVMP_TOOLKIT_CONFIG", None)
        else:
            os.environ["UVMP_TOOLKIT_CONFIG"] = self.old
        self.tmp.cleanup()

    def test_secret_masked_and_preserved(self):
        config.save(
            {"cluster": {"enabled": True, "cluster_id": "c1", "secret": "s" * 20}}
        )
        cfg = config.load()
        self.assertEqual(cfg["cluster"]["secret"], "s" * 20)
        view = config.masked_view(cfg)
        self.assertEqual(view["cluster"]["secret"], "")
        self.assertTrue(view["cluster"]["secret_set"])

    def test_empty_or_masked_secret_keeps_existing(self):
        config.save({"cluster": {"secret": "s" * 20}})
        config.save({"cluster": {"enabled": False, "secret": ""}})
        self.assertEqual(config.load()["cluster"]["secret"], "s" * 20)
        config.save(
            {"cluster": {"enabled": True, "secret": "******", "secret_set": True}}
        )
        cfg = config.load()
        self.assertEqual(cfg["cluster"]["secret"], "s" * 20)
        self.assertNotIn("secret_set", cfg["cluster"])

    def test_cluster_defaults_present(self):
        cfg = config.load()
        self.assertFalse(cfg["cluster"]["enabled"])
        self.assertEqual(cfg["cluster"]["listen_port"], 17890)
        self.assertTrue(cfg["cluster"]["require_approval"])


if __name__ == "__main__":
    unittest.main()
