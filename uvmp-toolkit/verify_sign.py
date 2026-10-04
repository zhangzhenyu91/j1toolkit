#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""签名结构离线校验工具 v2（只用 Python 标准库）
用途：在同一个浏览器登录会话中取得 cURL 与 cube.mtk，自动扫描签名结构变体。
运行：python3 verify_sign.py
"""
import base64
import hashlib
import hmac
import json
import re
import sys
import urllib.parse


def js_quote(s):
    return urllib.parse.quote(s, safe="-_.!~*'()")


def b64url(data, pad=False):
    s = base64.urlsafe_b64encode(data).decode()
    return s if pad else s.rstrip("=")


def parse_curl(text):
    m = (re.search(r"--url\s+'([^']+)'", text)
         or re.search(r"curl\s+'(http[^']+)'", text)
         or re.search(r"(https?://\S+)", text))
    url = m.group(1).rstrip("'\"")

    def header(name):
        m = re.search(r"-H\s+'" + re.escape(name) + r":\s*([^']+)'", text, re.I)
        return m.group(1).strip() if m else None

    return url, header("Auth_Tk"), header("X-Content-Token")


def main():
    print("=" * 64)
    print("第 1 步：在浏览器完成一次【查询】后，Copy as cURL（带 Auth_Tk 的 GET 那条），")
    print("         整段粘贴到这里；结束后单独一行输入 EOF 并回车：")
    print("=" * 64)
    lines = []
    while True:
        try:
            l = input()
        except EOFError:
            break
        if l.strip() == "EOF":
            break
        lines.append(l)
    url, token, expected = parse_curl("\n".join(lines))
    if not (url and token and expected):
        sys.exit("!! 未能解析出 url / Auth_Tk / X-Content-Token，请检查粘贴内容后重跑")
    print("解析成功：X-Content-Token = %s" % expected)

    print()
    print("第 2 步：【同一个浏览器会话】的 Console 执行  copy(JSON.stringify(cube.mtk))")
    mtk_raw = input("         粘贴结果并回车：").strip()
    try:
        mtk = json.loads(mtk_raw)
    except Exception:  # noqa: BLE001
        mtk = mtk_raw.strip('"').strip()
    print("  mtk = %s（长度 %d）" % (mtk, len(mtk)))
    if expected.startswith("MAC "):
        expected = expected[4:]
    print()

    u = url.split("?")[0]
    i1 = u.index("/")
    i2 = u.index("/", i1 + 1)
    i3 = u.index("/", i2 + 1)
    temp_url = u[i3:]
    FILTER = {"rnd", "cl_u_id", "_", "T", "Auth_Token", "ticket"}

    q, qd = {}, {}
    for item in (url.split("?", 1)[1] if "?" in url else "").split("&"):
        if not item:
            continue
        k, _, v = item.partition("=")
        q[k] = v
        qd[k] = urllib.parse.unquote(v)
    tycl = {k: v for k, v in q.items() if k.startswith("tycl_")}
    base = {k: v for k, v in q.items() if k not in FILTER and not k.startswith("tycl_")}
    params_obj = None
    try:
        params_obj = json.loads(qd.get("params", ""))
    except Exception:  # noqa: BLE001
        pass

    nd_variants = {}
    nd_variants["params字符串+tycl"] = dict(sorted({**base, **tycl}.items()))
    nd_variants["params字符串(无tycl)"] = dict(sorted(base.items()))
    nd_variants["只有tycl"] = dict(sorted(tycl.items()))
    nd_variants["空对象"] = {}
    nd_variants["不排除rnd等"] = dict(sorted({k: v for k, v in q.items()}.items()))
    if params_obj is not None:
        d = dict(base); d["params"] = params_obj
        nd_variants["params对象+tycl"] = dict(sorted({**d, **tycl}.items()))
        nd_variants["params对象(无tycl)"] = dict(sorted(d.items()))
        flat = dict(base); flat.pop("params", None); flat.update(params_obj)
        nd_variants["平铺+tycl"] = dict(sorted({**flat, **tycl}.items()))
        nd_variants["平铺(无tycl)"] = dict(sorted(flat.items()))
        flat2 = dict(flat)
        if "filter" in flat2:
            flat2["filter"] = flat2["filter"].replace("+", " ")
        nd_variants["平铺空格+tycl"] = dict(sorted({**flat2, **tycl}.items()))
        nd_variants["平铺空格(无tycl)"] = dict(sorted(flat2.items()))
        d2 = dict(base); d2.update(params_obj)
        nd_variants["字符串+平铺+tycl"] = dict(sorted({**d2, **tycl}.items()))
        d3 = dict(base); d3["params"] = qd.get("params", "")
        nd_variants["params解码串+tycl"] = dict(sorted({**d3, **tycl}.items()))
        nd_variants["params解码串(无tycl)"] = dict(sorted(d3.items()))

    key_hex = None
    try:
        key_hex = bytes.fromhex(mtk)
    except ValueError:
        pass

    hits = []
    total = 0
    for nd_name, nd in nd_variants.items():
        for spaces in (False, True):
            js = json.dumps(nd, ensure_ascii=False, separators=None if spaces else (",", ":"))
            for pad in (False, True):
                np_ = b64url(js_quote(js).encode(), pad=pad)
                tu = b64url(temp_url.encode(), pad=pad)
                for order2 in (False, True):
                    content = (token + "." + tu + "." + np_) if order2 else (token + "." + np_ + "." + tu)
                    for keyname, key in (("hex", key_hex), ("raw", mtk.encode())):
                        if key is None:
                            continue
                        total += 1
                        sig = b64url(hmac.new(key, content.encode(), hashlib.sha256).digest(), pad=pad)
                        if sig == expected:
                            hits.append((nd_name, "JSON%s" % ("带空格" if spaces else "紧凑"),
                                         "b64%s" % ("有填充" if pad else "无填充"),
                                         "顺序%s" % ("token.url.params" if order2 else "token.params.url"),
                                         "key=%s" % keyname))
    print("扫描 %d 种组合：" % total)
    if hits:
        for h in hits:
            print("  <<<<<<<<<< 命中：%s | %s | %s | %s | %s" % h)
        print()
        print("请把命中行原样发给开发者。")
    else:
        print("  全部不一致。可能原因：cURL 与 mtk 不是同一会话，或 token 串接的不是 Auth_Tk。")
        print("  请把本屏幕输出和粘贴的 cURL、mtk 一起发给开发者。")


if __name__ == "__main__":
    main()
