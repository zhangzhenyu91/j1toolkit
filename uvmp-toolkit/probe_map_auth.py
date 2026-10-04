#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""地图平台认证探测（只用 requests + vehicle_export 的 sso_login）
目的：用 SSO 自动登录后的会话探测 map.sgcc.com.cn 的资源是否可自动获得。
运行：python3 probe_map_auth.py
把全部输出原样发给开发者。"""
import configparser
import getpass
import sys

import requests
import urllib3

import vehicle_export as ve

urllib3.disable_warnings()

cfgp = configparser.ConfigParser()
cfgp.read("config.ini", encoding="utf-8")
auth_cfg = dict(cfgp.items("auth")) if cfgp.has_section("auth") else {}
sso_cfg = dict(cfgp.items("sso")) if cfgp.has_section("sso") else {}
username = auth_cfg.get("username", "").strip() or input("SSO 用户名: ").strip()
password = auth_cfg.get("password", "").strip() or getpass.getpass("SSO 密码: ")

s = requests.Session()
s.headers["User-Agent"] = ("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
                           "(KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36")

print("== 正在 SSO 登录 ==")
try:
    t = ve.sso_login(s, username, password, sso_cfg)
    print("登录成功，T =", t[:30], "...")
except Exception as e:  # noqa: BLE001
    sys.exit("SSO 登录失败: %s" % e)
print("SSO 域 cookies:", [(c.name, c.domain) for c in s.cookies])

urls = [
    "https://map.sgcc.com.cn/maps?v=3.0.0",
    "https://map.sgcc.com.cn/api/gl/styles/aegis/Streets",
    "https://map.sgcc.com.cn/styles/aegis/Streets",
    "https://map.sgcc.com.cn/aegis/styles/aegis/Streets",
    "https://map.sgcc.com.cn/tiles/aegis/Streets",
    "https://map.sgcc.com.cn/",
]
for u in urls:
    print("=" * 70)
    try:
        r = s.get(u, timeout=25, allow_redirects=True, verify=False)
        print(u)
        print("  => HTTP", r.status_code, "|", r.headers.get("Content-Type"))
        if r.history:
            print("  重定向链:")
            for h in r.history:
                print("     %s %s" % (h.status_code, h.url[:90]))
                print("        -> %s" % h.headers.get("Location", "")[:120])
        print("  最终URL:", r.url[:140])
        print("  响应前200字符:", r.text[:200].replace("\n", " "))
    except Exception as e:  # noqa: BLE001
        print(u, "请求异常:", e)

print("=" * 70)
print("最终所有 cookies:")
for c in s.cookies:
    print("   %s @ %s" % (c.name, c.domain))
print()
print("判读提示：")
print("  - 如果 styles/tiles 相关 URL 返回 200 且内容是 JSON/图片 -> SSO 会话可直接拿地图资源，可全自动")
print("  - 如果被重定向到 sso.tyqx.sgcc.com.cn 后又跳回并返回 200 -> CAS 自动登录成功，同样可全自动")
print("  - 如果最终落在登录页 HTML 或 401/403 -> 地图平台是独立认证，把输出发给开发者")
