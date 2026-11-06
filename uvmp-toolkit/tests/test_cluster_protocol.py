# -*- coding: utf-8 -*-
"""集群协议单测：签名、时间窗、重放、节点信息校验。"""

import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "toolkit"))

from cluster import protocol as p  # noqa: E402

SECRET = "unit-test-secret-0123456789"
CLUSTER = "team-a"
NODE = "node-0001"


def headers_for(body=b"{}", path="/cluster/join", ts=None, nonce=None):
    return p.build_headers(
        SECRET, CLUSTER, NODE, "POST", path, body, timestamp=ts, nonce=nonce
    )


def verify(headers, body=b"{}", path="/cluster/join", now=None, cache=None):
    return p.verify_headers(
        SECRET,
        CLUSTER,
        "POST",
        path,
        headers,
        body,
        cache or p.NonceCache(),
        now=now,
    )


class SignatureTest(unittest.TestCase):
    def test_roundtrip(self):
        self.assertEqual(verify(headers_for()), NODE)

    def test_wrong_secret(self):
        headers = headers_for()
        with self.assertRaises(p.ClusterAuthError):
            p.verify_headers(
                "another-secret-0123456789",
                CLUSTER,
                "POST",
                "/cluster/join",
                headers,
                b"{}",
                p.NonceCache(),
            )

    def test_tampered_body(self):
        headers = headers_for(b'{"a":1}')
        with self.assertRaises(p.ClusterAuthError):
            verify(headers, b'{"a":2}')

    def test_wrong_path(self):
        headers = headers_for(path="/cluster/join")
        with self.assertRaises(p.ClusterAuthError):
            verify(headers, path="/cluster/heartbeat")

    def test_clock_skew(self):
        headers = headers_for(ts=1000)
        with self.assertRaises(p.ClusterAuthError):
            verify(headers, now=1000 + p.CLOCK_SKEW_SEC + 1)

    def test_replay(self):
        headers = headers_for(nonce="nonce-12345678")
        cache = p.NonceCache()
        verify(headers, cache=cache)
        with self.assertRaises(p.ClusterAuthError):
            verify(headers, cache=cache)

    def test_cluster_mismatch(self):
        headers = headers_for()
        with self.assertRaises(p.ClusterAuthError):
            p.verify_headers(
                SECRET,
                "team-b",
                "POST",
                "/cluster/join",
                headers,
                b"{}",
                p.NonceCache(),
            )


class NodeInfoTest(unittest.TestCase):
    def info(self, **over):
        base = {
            "node_id": NODE,
            "name": "一号机",
            "role": "worker",
            "team_id": 3,
            "team_name": "检修一班",
            "platform": "linux",
            "arch": "amd64",
            "version": "2.6.0",
            "protocol": 1,
            "port": 17890,
        }
        base.update(over)
        return base

    def test_accepts_valid(self):
        out = p.validate_node_info(self.info())
        self.assertEqual(out["node_id"], NODE)
        self.assertEqual(out["team_id"], 3)

    def test_rejects_bad_values(self):
        for over in (
            {"node_id": ""},
            {"node_id": "../x"},
            {"role": "master"},
            {"team_id": -1},
            {"port": 0},
            {"port": 70000},
            {"protocol": 2},
            {"name": "x" * 65},
        ):
            with self.subTest(over=over):
                with self.assertRaises(p.ClusterProtocolError):
                    p.validate_node_info(self.info(**over))

    def test_defaults_name_to_id(self):
        out = p.validate_node_info(self.info(name=""))
        self.assertEqual(out["name"], NODE)


class BodyTest(unittest.TestCase):
    def test_empty_body(self):
        self.assertEqual(p.parse_json_body(b""), {})

    def test_rejects_non_object(self):
        with self.assertRaises(p.ClusterProtocolError):
            p.parse_json_body(b"[]")

    def test_rejects_oversize(self):
        with self.assertRaises(p.ClusterProtocolError):
            p.parse_json_body(b'{"x":"' + b"a" * p.MAX_BODY + b'"}')


if __name__ == "__main__":
    unittest.main()
