#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
派车单 / 行驶轨迹 批量导出 PDF
直接调用派车系统（UVMP）后台接口，不操作浏览器界面。
接口细节见《接口分析.md》。

用法示例：
  python3 vehicle_export.py --from 2026-09-01 --to 2026-09-30            # 导出派车单+轨迹
  python3 vehicle_export.py --from 2026-09-01 --to 2026-09-30 --type pcd # 只导出派车单
  python3 vehicle_export.py --auth token                                 # 手动粘贴凭证模式
依赖：requests（必需）；gmssl（仅 --auth sso 自动登录时需要）
"""

import argparse
import base64
import binascii
import configparser
import getpass
import hashlib
import hmac as hmac_mod
import html as html_mod
import json
import os
import re
import secrets
import shutil
import subprocess
import sys
import time
import urllib.parse
from datetime import datetime, timedelta
from pathlib import Path

# 免 pip 运行：如果脚本旁存在 pylib 目录（wheel 解压出的依赖），直接加入搜索路径
_PYLIB = Path(__file__).resolve().parent / "pylib"
if _PYLIB.is_dir():
    sys.path.insert(0, str(_PYLIB))

try:
    import requests
except ImportError:
    sys.exit("缺少 requests 库，请先安装：pip3 install requests（离线安装见《部署说明.md》）")

GATEWAY = "http://20.1.59.23:18080/"
WEB_ORIGIN = "http://uvmp.sgcc.com.cn"
# getUserAuth 的 SM2 公钥（前端硬编码，见接口分析.md）
GDKEY = ("04a1b7b1dfef3b21ef0523fe9496624a"
         "4b99eb83ceb037e75bbbdcf61dab5"
         "aced689064385366b46456f4d782699873ed3e4"
         "3695dc64baa107e6d8a167eb570475")

SCRIPT_DIR = Path(__file__).resolve().parent


# ---------------------------------------------------------------- 工具

def js_quote(s: str) -> str:
    """等价于 JS encodeURIComponent"""
    return urllib.parse.quote(s, safe="-_.!~*'()")


def b64url_nopad(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).decode().rstrip("=")


def safe_name(s: str) -> str:
    return re.sub(r'[\\/:*?"<>|\s]+', "_", str(s or ""))[:80]


def esc(v) -> str:
    return html_mod.escape("" if v is None else str(v))


def fmt_epoch_ms(v) -> str:
    """epoch 毫秒 -> 'YYYY-MM-DD HH:MM:SS'，非法输入原样返回"""
    try:
        return datetime.fromtimestamp(int(v) / 1000).strftime("%Y-%m-%d %H:%M:%S")
    except (TypeError, ValueError, OSError, OverflowError):
        return str(v or "")


def clean_time(v) -> str:
    """'2026-09-01 08:57:06.0' -> '2026-09-01 08:57:06'"""
    s = str(v or "").strip()
    return re.sub(r"(\d{2}:\d{2}:\d{2})\.\d+$", r"\1", s)


# ---------------------------------------------------------------- SM4（移植自系统前端 sm4.js，标准 SM4-CBC/PKCS7）

SM4_SBOX = [
    0xd6, 0x90, 0xe9, 0xfe, 0xcc, 0xe1, 0x3d, 0xb7, 0x16, 0xb6, 0x14, 0xc2, 0x28, 0xfb, 0x2c, 0x05,
    0x2b, 0x67, 0x9a, 0x76, 0x2a, 0xbe, 0x04, 0xc3, 0xaa, 0x44, 0x13, 0x26, 0x49, 0x86, 0x06, 0x99,
    0x9c, 0x42, 0x50, 0xf4, 0x91, 0xef, 0x98, 0x7a, 0x33, 0x54, 0x0b, 0x43, 0xed, 0xcf, 0xac, 0x62,
    0xe4, 0xb3, 0x1c, 0xa9, 0xc9, 0x08, 0xe8, 0x95, 0x80, 0xdf, 0x94, 0xfa, 0x75, 0x8f, 0x3f, 0xa6,
    0x47, 0x07, 0xa7, 0xfc, 0xf3, 0x73, 0x17, 0xba, 0x83, 0x59, 0x3c, 0x19, 0xe6, 0x85, 0x4f, 0xa8,
    0x68, 0x6b, 0x81, 0xb2, 0x71, 0x64, 0xda, 0x8b, 0xf8, 0xeb, 0x0f, 0x4b, 0x70, 0x56, 0x9d, 0x35,
    0x1e, 0x24, 0x0e, 0x5e, 0x63, 0x58, 0xd1, 0xa2, 0x25, 0x22, 0x7c, 0x3b, 0x01, 0x21, 0x78, 0x87,
    0xd4, 0x00, 0x46, 0x57, 0x9f, 0xd3, 0x27, 0x52, 0x4c, 0x36, 0x02, 0xe7, 0xa0, 0xc4, 0xc8, 0x9e,
    0xea, 0xbf, 0x8a, 0xd2, 0x40, 0xc7, 0x38, 0xb5, 0xa3, 0xf7, 0xf2, 0xce, 0xf9, 0x61, 0x15, 0xa1,
    0xe0, 0xae, 0x5d, 0xa4, 0x9b, 0x34, 0x1a, 0x55, 0xad, 0x93, 0x32, 0x30, 0xf5, 0x8c, 0xb1, 0xe3,
    0x1d, 0xf6, 0xe2, 0x2e, 0x82, 0x66, 0xca, 0x60, 0xc0, 0x29, 0x23, 0xab, 0x0d, 0x53, 0x4e, 0x6f,
    0xd5, 0xdb, 0x37, 0x45, 0xde, 0xfd, 0x8e, 0x2f, 0x03, 0xff, 0x6a, 0x72, 0x6d, 0x6c, 0x5b, 0x51,
    0x8d, 0x1b, 0xaf, 0x92, 0xbb, 0xdd, 0xbc, 0x7f, 0x11, 0xd9, 0x5c, 0x41, 0x1f, 0x10, 0x5a, 0xd8,
    0x0a, 0xc1, 0x31, 0x88, 0xa5, 0xcd, 0x7b, 0xbd, 0x2d, 0x74, 0xd0, 0x12, 0xb8, 0xe5, 0xb4, 0xb0,
    0x89, 0x69, 0x97, 0x4a, 0x0c, 0x96, 0x77, 0x7e, 0x65, 0xb9, 0xf1, 0x09, 0xc5, 0x6e, 0xc6, 0x84,
    0x18, 0xf0, 0x7d, 0xec, 0x3a, 0xdc, 0x4d, 0x20, 0x79, 0xee, 0x5f, 0x3e, 0xd7, 0xcb, 0x39, 0x48,
]
SM4_CK = [
    0x00070e15, 0x1c232a31, 0x383f464d, 0x545b6269,
    0x70777e85, 0x8c939aa1, 0xa8afb6bd, 0xc4cbd2d9,
    0xe0e7eef5, 0xfc030a11, 0x181f262d, 0x343b4249,
    0x50575e65, 0x6c737a81, 0x888f969d, 0xa4abb2b9,
    0xc0c7ced5, 0xdce3eaf1, 0xf8ff060d, 0x141b2229,
    0x30373e45, 0x4c535a61, 0x686f767d, 0x848b9299,
    0xa0a7aeb5, 0xbcc3cad1, 0xd8dfe6ed, 0xf4fb0209,
    0x10171e25, 0x2c333a41, 0x484f565d, 0x646b7279,
]
SM4_FK = (0xa3b1bac6, 0x56aa3350, 0x677d9197, 0xb27022dc)


def _rotl(x, n):
    return ((x << n) | (x >> (32 - n))) & 0xFFFFFFFF


def _tau(a):
    return ((SM4_SBOX[(a >> 24) & 0xFF] << 24) | (SM4_SBOX[(a >> 16) & 0xFF] << 16)
            | (SM4_SBOX[(a >> 8) & 0xFF] << 8) | SM4_SBOX[a & 0xFF])


def _l1(b):
    return (b ^ _rotl(b, 2) ^ _rotl(b, 10) ^ _rotl(b, 18) ^ _rotl(b, 24)) & 0xFFFFFFFF


def _l2(b):
    return (b ^ _rotl(b, 13) ^ _rotl(b, 23)) & 0xFFFFFFFF


def _sm4_round_keys(key: bytes, decrypt: bool):
    x = [int.from_bytes(key[4 * i:4 * i + 4], "big") ^ SM4_FK[i] for i in range(4)]
    rk = [0] * 32
    for r in range(0, 32, 4):
        x[0] = (x[0] ^ _l2(_tau(x[1] ^ x[2] ^ x[3] ^ SM4_CK[r]))) & 0xFFFFFFFF
        rk[r] = x[0]
        x[1] = (x[1] ^ _l2(_tau(x[2] ^ x[3] ^ x[0] ^ SM4_CK[r + 1]))) & 0xFFFFFFFF
        rk[r + 1] = x[1]
        x[2] = (x[2] ^ _l2(_tau(x[3] ^ x[0] ^ x[1] ^ SM4_CK[r + 2]))) & 0xFFFFFFFF
        rk[r + 2] = x[2]
        x[3] = (x[3] ^ _l2(_tau(x[0] ^ x[1] ^ x[2] ^ SM4_CK[r + 3]))) & 0xFFFFFFFF
        rk[r + 3] = x[3]
    if decrypt:
        rk.reverse()
    return rk


def _sm4_block(block: bytes, rk) -> bytes:
    x = [int.from_bytes(block[4 * i:4 * i + 4], "big") for i in range(4)]
    for r in range(0, 32, 4):
        x[0] = (x[0] ^ _l1(_tau(x[1] ^ x[2] ^ x[3] ^ rk[r]))) & 0xFFFFFFFF
        x[1] = (x[1] ^ _l1(_tau(x[2] ^ x[3] ^ x[0] ^ rk[r + 1]))) & 0xFFFFFFFF
        x[2] = (x[2] ^ _l1(_tau(x[3] ^ x[0] ^ x[1] ^ rk[r + 2]))) & 0xFFFFFFFF
        x[3] = (x[3] ^ _l1(_tau(x[0] ^ x[1] ^ x[2] ^ rk[r + 3]))) & 0xFFFFFFFF
    out = bytearray()
    for j in range(0, 16, 4):
        w = x[3 - j // 4]
        out += bytes([(w >> 24) & 0xFF, (w >> 16) & 0xFF, (w >> 8) & 0xFF, w & 0xFF])
    return bytes(out)


def sm4_cbc_decrypt_hex(cipher_hex: str, key: bytes, iv: bytes) -> bytes:
    """对应前端 sm4_decrypt(p, K, {mode:'cbc', iv:V})，输入输出均为未解码字节（自动去 PKCS7 填充）"""
    data = bytes.fromhex(cipher_hex)
    rk = _sm4_round_keys(key, decrypt=True)
    out = bytearray()
    last = iv
    for off in range(0, len(data), 16):
        block = data[off:off + 16]
        dec = _sm4_block(block, rk)
        out += bytes(a ^ b for a, b in zip(dec, last))
        last = block
    pad = out[-1]
    if 1 <= pad <= 16:
        del out[-pad:]
    return bytes(out)


# ---------------------------------------------------------------- SM2/SM3（依赖 gmssl，仅自动登录用）

def sm2_encrypt_hex(plaintext: bytes, pubkey130: str) -> str:
    """等价于前端 SG_sm2Encrypt：SM2 C1C3C2，输出 '04'+C1+C3+C2 的 hex
    （gmssl 的 encrypt 输出不带 04 前缀的 C1，需自行补上）"""
    try:
        from gmssl import sm2
    except ImportError:
        raise RuntimeError("缺少 gmssl 库（自动登录需要）。安装：pip3 install gmssl，离线安装见《部署说明.md》；"
                           "或改用 --auth token 手动凭证模式（不需要 gmssl）。")
    crypt = sm2.CryptSM2(public_key=pubkey130[2:], private_key="", mode=1)
    for _ in range(10):
        res = crypt.encrypt(plaintext)
        if res:
            return "04" + (res.hex() if isinstance(res, (bytes, bytearray)) else res)
    raise RuntimeError("SM2 加密失败")


def _der_len(n: int) -> bytes:
    if n < 0x80:
        return bytes([n])
    s = n.to_bytes((n.bit_length() + 7) // 8, "big")
    return bytes([0x80 | len(s)]) + s


def _der_int(b: bytes) -> bytes:
    b = b.lstrip(b"\x00") or b"\x00"
    if b[0] & 0x80:
        b = b"\x00" + b
    return b"\x02" + _der_len(len(b)) + b


def _der_octet(b: bytes) -> bytes:
    return b"\x04" + _der_len(len(b)) + b


def sm2_encrypt_asn1(plaintext: bytes, pubkey130: str) -> str:
    """SSO 登录密码加密：SM2 C1C3C2 的 ASN.1 DER 编码 hex。
    结构已由抓包验证：SEQUENCE{INTEGER x, INTEGER y, OCTET STRING c3(32B), OCTET STRING c2}
    （gmssl 3.2.2 的 asn1 参数实际未生效，故手工 DER 编码）"""
    try:
        from gmssl import sm2
    except ImportError:
        raise RuntimeError("缺少 gmssl 库（自动登录需要）。安装：pip3 install gmssl，离线安装见《部署说明.md》。")
    crypt = sm2.CryptSM2(public_key=pubkey130[2:], private_key="", mode=1)
    raw = None
    for _ in range(10):
        raw = crypt.encrypt(plaintext)
        if raw:
            break
    if not raw:
        raise RuntimeError("SM2 加密失败")
    x, y, c3, c2 = raw[:32], raw[32:64], raw[64:96], raw[96:]
    seq = _der_int(x) + _der_int(y) + _der_octet(c3) + _der_octet(c2)
    return (b"\x30" + _der_len(len(seq)) + seq).hex()


def sm3_hex(data: bytes) -> str:
    try:
        from gmssl import sm3
    except ImportError:
        raise RuntimeError("缺少 gmssl 库（自动登录需要）。安装：pip3 install gmssl，离线安装见《部署说明.md》。")
    return sm3.sm3_hash(list(data))


# ---------------------------------------------------------------- 认证

class AuthError(Exception):
    pass


class Auth:
    def __init__(self, token: str, refresh_token: str, mtk: str, login_user: dict = None):
        self.token = token
        self.refresh_token = refresh_token
        self.mtk = mtk
        self.login_user = login_user or {}   # 官方渲染器凭证注入用（loginUser 原样透传）

    @classmethod
    def manual(cls, cfg):
        token = cfg.get("token", "").strip() or input("粘贴 Auth_Tk（cube.token）: ").strip()
        refresh = cfg.get("refresh_token", "").strip() or input("粘贴 Refresh_Tk（cube.refreshToken）: ").strip()
        mtk = cfg.get("mtk", "").strip() or input("粘贴 mtk（cube.mtk）: ").strip()
        if not (token and refresh and mtk):
            raise AuthError("token / refresh_token / mtk 不能为空")
        return cls(token, refresh, mtk)

    @classmethod
    def via_getuserauth(cls, gateway: str, t_jwt: str, session: "requests.Session"):
        """用 authLogin 颁发的 T，经 getUserAuth 换取 token/mtk/refreshToken"""
        k = secrets.token_hex(16)
        v = secrets.token_hex(16)
        token_data = "T=" + t_jwt + "&K=" + k + "&V=" + v
        p = sm2_encrypt_hex(js_quote(token_data).encode("utf-8"), GDKEY)
        r = session.get(gateway + "getUserAuth", params={"p": p}, timeout=30)
        r.raise_for_status()
        blob = r.json().get("p")
        if not blob:
            raise AuthError("getUserAuth 返回异常: " + r.text[:200])
        plain = sm4_cbc_decrypt_hex(blob, bytes.fromhex(k), bytes.fromhex(v))
        data = json.loads(urllib.parse.unquote(plain.decode("utf-8")))
        # 诊断输出：打印响应顶层字段（值脱敏，只显示长度），便于核对 token/mtk 字段名
        brief = ", ".join("%s(len=%d)" % (kk, len(str(vv))) for kk, vv in data.items()
                          if not isinstance(vv, (dict, list)))
        print("getUserAuth 响应字段: " + brief)
        if not data.get("mtk"):
            print("!! 警告：getUserAuth 响应中 mtk 字段为空或不存在，签名必然失败，"
                  "请把上面字段列表发给开发者")
        if not data.get("isLogin"):
            raise AuthError("getUserAuth: isLogin=false，T 已失效，请重新登录获取")
        user = data.get("loginUser") or {}
        print("登录成功：%s（%s）" % (user.get("loginName", "?"), user.get("defaultOrgCode", "?")))
        return cls(data["token"], user.get("refreshToken", ""), data.get("mtk", ""),
                   login_user=user)


SSO_BASE = "http://sso.tyqx.sgcc.com.cn/isc_sso"
SSO_APPID = "8af894fa6a6b03e3016a6b68580100cd"
REDIRECT_URI = ("http://20.1.59.23:18080/authLogin?redirectUrl="
                "http://uvmp.sgcc.com.cn/uvmp-web/factoryLayout/index.html"
                "&tycl_parentMenuId=denglu&tycl_sonMenuId=denglu"
                "&tycl_sonMenuTitle=%E7%99%BB%E5%BD%95")


def _getuid() -> str:
    """与前端 getUID 一致：'xxxxxxxxxxxx4xxxyxxxxxxxxxxxxxxx' 形式的 32 位 hex"""
    out = []
    for c in "xxxxxxxxxxxx4xxxyxxxxxxxxxxxxxxx":
        if c == "x":
            out.append(format(secrets.randbelow(16), "x"))
        elif c == "y":
            out.append(format(secrets.randbelow(16) & 0x3 | 0x8, "x"))
        else:
            out.append(c)
    return "".join(out)


def _sign_triple():
    """preLogin/login 的防重放三元组：sign = SM3("requestTime,nonce")（已由 4 组抓包样本验证）"""
    rt = str(int(time.time() * 1000))
    nonce = _getuid()
    return rt, nonce, sm3_hex((rt + "," + nonce).encode("utf-8"))


def _random_string(n: int) -> str:
    """与前端 getRandomString 相同字符集（去掉易混淆字符）"""
    chars = "ABCDEFGHJKMNPQRSTWXYZabcdefhijkmnprstwxyz2345678"
    return "".join(secrets.choice(chars) for _ in range(n))


def _normalize_pubkey(pk: str) -> str:
    pk = pk.strip()
    if len(pk) == 128:
        pk = "04" + pk
    if len(pk) != 130 or not pk.startswith("04"):
        raise AuthError("SM2 公钥格式不正确（应为 128 或 04 开头的 130 位十六进制）")
    return pk


def encrypt_sso_password(password: str, pubkey130: str) -> str:
    """与 aostaritEncrypt.js 一致：SM3(密码)(64hex) + 8位随机串 + 密码，再 SM2/ASN.1 加密
    （明文长度 64+8+len(密码) 已与抓包密文长度验证吻合）"""
    wrapped = sm3_hex(password.encode("utf-8")) + _random_string(8) + password
    return sm2_encrypt_asn1(wrapped.encode("utf-8"), pubkey130)


def _extract_login_page(html: str):
    """从登录页 HTML 提取 execution 和 aostaritEncryptUtils.init 的 encryptKey"""
    m = re.search(r'name="execution"[^>]*value="([^"]+)"', html)
    execution = m.group(1) if m else "e1s1"
    pubkey = None
    m = re.search(r"""encryptKey["']?\s*[:=]\s*["']([0-9a-fA-F]{128,130})["']""", html)
    if m:
        pubkey = m.group(1)
    return execution, pubkey


def sso_login(session: "requests.Session", username: str, password: str, cfg: dict) -> str:
    """SSO 账号密码登录，返回 authLogin 颁发的 T(JWT)"""
    # 内网 SSO/网关间歇性把 deflate 响应体发成裸流（错标 zlib 封装），requests 解压即炸
    # （Error -3 incorrect header check，已踩坑）——声明只收未压缩内容，根除这一类问题
    session.headers["Accept-Encoding"] = "identity"
    service = REDIRECT_URI
    # 1. 打开登录页（种 SESSION cookie，提取 execution 和页面内的密码加密公钥 encryptKey）
    r = session.get(SSO_BASE + "/login", params={"service": service}, timeout=30)
    r.raise_for_status()
    execution, page_pubkey = _extract_login_page(r.text)
    pubkey = (cfg.get("sm2_pubkey") or "").strip() or page_pubkey
    if not pubkey:
        raise AuthError("登录页中未找到密码加密公钥(encryptKey)，请在 config.ini [sso] sm2_pubkey= 手动配置")
    pubkey = _normalize_pubkey(pubkey)
    # 2. 密码混淆+SM2(ASN.1) 加密；构造签名体
    body = {
        "appId": SSO_APPID, "authMode": "ACCOUNT_PASSWORD_SGCC",
        "provinceId": cfg.get("province_id", "sx"), "wangsheng": cfg.get("wangsheng", "sx"),
        "username": username, "password": encrypt_sso_password(password, pubkey),
        "execution": execution, "_eventId": "submit",
    }
    rt, nonce, sign = _sign_triple()
    body.update({"requestTime": rt, "nonce": nonce, "sign": sign, "redirectUri": REDIRECT_URI})
    # 3. preLogin/check（URL query 用独立的一组三元组）
    rt2, nonce2, sign2 = _sign_triple()
    pr = session.post(SSO_BASE + "/preLogin/check",
                      params={"requestTime": rt2, "nonce": nonce2, "sign": sign2},
                      data=json.dumps(body, ensure_ascii=False, separators=(",", ":")).encode("utf-8"),
                      headers={"Content-Type": "application/json;charset=UTF-8"}, timeout=30)
    user_ticket = None
    try:
        pj = json.loads(pr.text)
        candidates = [pj]
        if isinstance(pj, dict):
            candidates += [pj.get(k) for k in ("data", "result", "resultValue", "dataMap") if isinstance(pj.get(k), dict)]
        for c in candidates:
            if isinstance(c, str) and c.startswith("UT-"):
                user_ticket = c
                break
            if isinstance(c, dict):
                for k in ("userTicket", "ticket", "ut", "user_ticket"):
                    if isinstance(c.get(k), str) and c[k].startswith("UT-"):
                        user_ticket = c[k]
                        break
            if user_ticket:
                break
    except ValueError:
        pass
    if not user_ticket:
        raise AuthError("preLogin/check 未返回 userTicket（HTTP %s），响应原文：%s"
                        "——请把该响应发给开发者调整提取逻辑" % (pr.status_code, pr.text[:300]))
    # 4. 正式登录（表单），跟随 302 链：login→authLogin?ticket→authLogin→index#home?T=
    form = dict(body)
    form["userTicket"] = user_ticket
    lr = session.post(SSO_BASE + "/login", params={"service": service}, data=form, timeout=30)
    for resp in list(lr.history) + [lr]:
        m = re.search(r"[?&]T=([A-Za-z0-9._-]+)", resp.headers.get("Location", ""))
        if m:
            return m.group(1)
    m = re.search(r"[?&]T=([A-Za-z0-9._-]+)", lr.url)
    if m:
        return m.group(1)
    raise AuthError("SSO 登录未获得 T：请检查账号密码是否正确（或响应: %s）" % lr.text[:200])


# ---------------------------------------------------------------- 签名 & 请求客户端

SIGN_FILTERED = {"rnd", "cl_u_id", "_", "T", "Auth_Token", "ticket"}


def make_x_content_token(token: str, mtk_hex: str, temp_url: str, new_data: dict) -> str:
    """对应前端签名（已用浏览器真实样本双样本验证命中）：
    new_data = 发送前的原始 p_data（未编码原文，filter 中用空格）合并 url 自有 query（解码后），
    按 key 排序后 JSON 紧凑序列化 -> encodeURIComponent -> base64url(无填充)，
    与 temp_url 的 base64url 一起接在 token 后做 HMAC-SHA256（key=unhex(mtk)），base64url(无填充)"""
    nd = {k: (str(v) if v else v) for k, v in sorted(new_data.items())}
    js = json.dumps(nd, ensure_ascii=False, separators=(",", ":"))
    content = (token + "." + b64url_nopad(js_quote(js).encode("utf-8"))
               + "." + b64url_nopad(temp_url.encode("utf-8")))
    sign = hmac_mod.new(bytes.fromhex(mtk_hex), content.encode("utf-8"), hashlib.sha256).digest()
    return "MAC " + b64url_nopad(sign)


class UvmpClient:
    def __init__(self, gateway: str, auth: Auth, retries: int = 3):
        self.gateway = gateway
        self.auth = auth
        self.retries = retries
        self.session = requests.Session()
        self.session.headers.update({
            "User-Agent": "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36",
            "Origin": WEB_ORIGIN,
            "Referer": WEB_ORIGIN + "/",
            # 同 sso_login：网关 deflate 错标间歇出现，统一只收未压缩内容
            "Accept-Encoding": "identity",
        })

    def _build(self, base_path: str, p_data: dict):
        """构造最终 URL 与签名用 new_data。
        base_path 可自带 query（如 getPrintDetails?id=a,b,c——原样拼接不编码，与浏览器一致）；
        p_data 为原始值（中文、空格不编码），URL 序列化用 quote_plus（空格->+，与抓包一致）。"""
        temp_url = "/" + base_path.split("?")[0]
        url = self.gateway + base_path
        url += ("&" if "?" in url else "?") + "rnd=" + rnd16() + "&cl_u_id=null"
        if p_data:
            url += "&" + "&".join(urllib.parse.quote_plus(str(k)) + "=" + urllib.parse.quote_plus(str(v))
                                  for k, v in p_data.items())
        url += "&_=" + ts_ms()
        new_data = {}
        if "?" in base_path:
            for item in base_path.split("?", 1)[1].split("&"):
                k, _, v = item.partition("=")
                if k and k not in SIGN_FILTERED:
                    new_data[k] = urllib.parse.unquote_plus(v)
        new_data.update(p_data or {})
        return url, temp_url, new_data

    def _headers(self, temp_url, new_data):
        return {
            "Content-Type": "application/json",
            "Auth_Tk": self.auth.token,
            "Refresh_Tk": self.auth.refresh_token,
            "X-Content-Token": make_x_content_token(self.auth.token, self.auth.mtk, temp_url, new_data),
        }

    def get(self, base_path: str, p_data=None, desc: str = "") -> dict:
        url, temp_url, new_data = self._build(base_path, p_data)
        return self._request("GET", url, temp_url, new_data, None, desc)

    def post(self, base_path: str, body: dict, desc: str = "") -> dict:
        url, temp_url, new_data = self._build(base_path, None)
        return self._request("POST", url, temp_url, body, body, desc)

    def _refresh(self) -> bool:
        """401 且 Token-Refresh:true 时，用 Refresh_Tk 换新 token（对应前端 tokenRefresh 逻辑）"""
        try:
            r = self.session.get(self.gateway + "tokenRefresh",
                                 params={"T": self.auth.token, "R": self.auth.refresh_token}, timeout=30)
            data = r.json()
            if data.get("tokenRefreshExpired"):
                print("  [token 刷新失败：refresh 凭证也已过期]")
                return False
            new_token = data.get("accessToken")
            if not new_token:
                print("  [token 刷新失败：%s]" % str(data)[:150])
                return False
            self.auth.token = new_token
            print("  [token 已自动刷新]")
            return True
        except Exception as e:  # noqa: BLE001
            print("  [token 刷新异常: %s]" % e)
            return False

    def _request(self, method, url, temp_url, new_data, body, desc):
        last_err = None
        refreshed = False
        for attempt in range(self.retries):
            try:
                headers = self._headers(temp_url, new_data)
                if method == "GET":
                    r = self.session.get(url, headers=headers, timeout=60)
                else:
                    payload = json.dumps(body, ensure_ascii=False, separators=(",", ":"))
                    r = self.session.post(url, headers=headers, data=payload.encode("utf-8"), timeout=60)
                if r.status_code in (401, 403):
                    # token 过期时服务器会带 Token-Refresh: true 头，可自动换新 token 重试
                    if not refreshed and r.headers.get("Token-Refresh", "").lower() == "true":
                        refreshed = True
                        if self._refresh():
                            continue
                    raise AuthError(
                        "凭证校验失败（HTTP %s）。Token-Refresh=%s；响应体：%s；"
                        "请求URL：%s；计算的X-Content-Token：%s"
                        % (r.status_code, r.headers.get("Token-Refresh"), r.text[:200],
                           url[:160], headers["X-Content-Token"][:60]))
                r.raise_for_status()
                data = r.json()
                if isinstance(data, dict) and data.get("successful") is False:
                    raise RuntimeError("接口返回失败 %s: %s" % (desc or url, str(data)[:300]))
                return data
            except AuthError:
                raise
            except Exception as e:  # noqa: BLE001
                last_err = e
                wait = 2 * (attempt + 1)
                print("  [重试 %d/%d] %s 失败: %s，%ds 后重试" % (attempt + 1, self.retries, desc or url, e, wait))
                time.sleep(wait)
        raise RuntimeError("请求失败（已重试 %d 次）%s: %s" % (self.retries, desc or url, last_err))


# ---------------------------------------------------------------- 业务接口

def rnd16() -> str:
    return "".join(str(secrets.randbelow(10)) for _ in range(16))


def ts_ms() -> str:
    return str(int(time.time() * 1000))


MENU_PCD = ("running", "pcdmanager", "派车单管理")
MENU_TRACK = ("monitor", "montrack", "行驶轨迹")


def tycl_params(menu: tuple) -> dict:
    """tycl_* 参数以原文（中文不编码）放入 p_data，URL 序列化时再 quote_plus"""
    parent, son, title = menu
    return {"tycl_parentMenuId": parent, "tycl_sonMenuId": son, "tycl_sonMenuTitle": title}


def query_pcd_page(client: UvmpClient, cfg, date_from: str, date_to: str, page_index: int) -> dict:
    # filter 中的日期时间用空格（签名用原始值；发送时 quote_plus 编码为 +，与浏览器一致）
    flt = ("planSendTime=%s 00:00&planSendTime2=%s 23:59:00&runState=%s&vehicleState=%s&keepTag=%s"
           % (date_from, date_to, cfg["run_state"], cfg["vehicle_state"], cfg["keep_tag"]))
    params_str = json.dumps({"pageIndex": page_index, "pageSize": cfg["page_size"], "filter": flt},
                            ensure_ascii=False, separators=(",", ":"))
    p_data = {"params": params_str}
    p_data.update(tycl_params(MENU_PCD))
    return client.get("running-service-provider/pcdmanager/", p_data, "派车单查询(第%d页)" % page_index)


def query_all_pcd(client: UvmpClient, cfg, date_from: str, date_to: str) -> list:
    first = query_pcd_page(client, cfg, date_from, date_to, 1)
    rv = first.get("resultValue") or {}
    total = rv.get("itemCount", 0)
    items = list(rv.get("items") or [])
    pages = (total + cfg["page_size"] - 1) // cfg["page_size"]
    print("派车单共 %d 条（%d 页）" % (total, pages))
    for p in range(2, pages + 1):
        data = query_pcd_page(client, cfg, date_from, date_to, p)
        items.extend((data.get("resultValue") or {}).get("items") or [])
        time.sleep(0.3)
    return items


def query_pcd_page_filter(client: UvmpClient, filter_str: str, page_index: int, page_size: int) -> dict:
    """自由 filter 的派车单分页查询：不附加 runState/vehicleState/keepTag，
    供按单号查找、当日全量导出等需要全部状态单据的场景（内网工具箱使用）"""
    params_str = json.dumps({"pageIndex": page_index, "pageSize": page_size, "filter": filter_str},
                            ensure_ascii=False, separators=(",", ":"))
    p_data = {"params": params_str}
    p_data.update(tycl_params(MENU_PCD))
    return client.get("running-service-provider/pcdmanager/", p_data, "派车单查询(第%d页)" % page_index)


def query_pcd_all_filter(client: UvmpClient, filter_str: str, page_size: int = 100) -> list:
    """按自由 filter 拉取全部页并合并且返回"""
    first = query_pcd_page_filter(client, filter_str, 1, page_size)
    rv = first.get("resultValue") or {}
    total = rv.get("itemCount", 0)
    items = list(rv.get("items") or [])
    pages = (total + page_size - 1) // page_size
    for p in range(2, pages + 1):
        data = query_pcd_page_filter(client, filter_str, p, page_size)
        items.extend((data.get("resultValue") or {}).get("items") or [])
        time.sleep(0.3)
    return items


def query_pcd_by_run_code(client: UvmpClient, code: str, date_hint: str = "",
                          lookback_days: int = 62, log=print):
    """按派车单号精确查找单据（不限 runState，任何状态的单都要能打印）。

    先按 runCode 过滤直查（该过滤项未经抓包验证，失败/为空自动兜底）；
    兜底按日期范围扫描：date_hint(YYYY-MM-DD)±3 天，无提示则回看 lookback_days 天
    （按 30 天切块）。无论哪条路径都在客户端按 runCode 精确匹配，防服务端模糊匹配。
    找不到返回 None。"""
    code = (code or "").strip()
    if not code:
        return None

    def pick(items):
        cands = [it for it in items if str(it.get("runCode", "")).strip() == code]
        if not cands:
            return None
        # 同号多单（理论上不会）时取预计用车时间最晚的一单
        return max(cands, key=lambda it: str(it.get("planSendTime") or ""))

    # 1) 直查
    try:
        hit = pick(query_pcd_all_filter(client, "runCode=" + code))
        if hit:
            return hit
    except Exception as e:  # noqa: BLE001
        log("  [提示] 按单号直查失败（%s），改用日期范围兜底" % e)

    # 2) 日期范围兜底
    from datetime import timedelta
    windows = []
    if date_hint:
        try:
            d = datetime.strptime(date_hint[:10], "%Y-%m-%d")
            windows.append(((d - timedelta(days=3)).strftime("%Y-%m-%d"),
                            (d + timedelta(days=3)).strftime("%Y-%m-%d")))
        except ValueError:
            pass
    today = datetime.now()
    for i in range((max(1, lookback_days) + 29) // 30):
        end = today - timedelta(days=30 * i)
        start = end - timedelta(days=29)
        windows.append((start.strftime("%Y-%m-%d"), end.strftime("%Y-%m-%d")))
    for date_from, date_to in windows:
        try:
            flt = "planSendTime=%s 00:00&planSendTime2=%s 23:59:00" % (date_from, date_to)
            hit = pick(query_pcd_all_filter(client, flt))
        except Exception as e:  # noqa: BLE001
            log("  [提示] 日期兜底查询失败（%s~%s：%s）" % (date_from, date_to, e))
            continue
        if hit:
            return hit
    return None


def get_print_details(client: UvmpClient, ids: list, chunk: int) -> list:
    result = []
    for i in range(0, len(ids), chunk):
        part = ids[i:i + chunk]
        data = client.get("running-service-provider/pcdmanager/getPrintDetails?id=" + ",".join(part),
                          tycl_params(MENU_PCD), "打印详情(%d-%d)" % (i + 1, i + len(part)))
        result.extend((data.get("resultValue") or {}).get("items") or [])
        time.sleep(0.3)
    return result


def query_track_points(client: UvmpClient, vehicle_id: str, begin: str, end: str) -> list:
    # 时间字符串带空格（签名用原始值；发送时编码为 +）
    p_data = {"beginTimeStr": begin, "vehicleId": vehicle_id, "endTimeStr": end}
    p_data.update(tycl_params(MENU_TRACK))
    data = client.get("monitor-service-provider/tyclTrack/queryTrackHisByVehicle", p_data, "轨迹点查询")
    if isinstance(data, dict):
        d = data.get("data")
        if isinstance(d, list):
            return d
        d = data.get("resultValue")
        if isinstance(d, list):
            return d
        if isinstance(d, dict) and isinstance(d.get("items"), list):
            return d["items"]
    return []


def query_track_segment(client: UvmpClient, vehicle_id: str, begin: str, end: str):
    """按派车单实际出车/归队时间查对应行程段（含 id/routeNo/起止时间/里程/时速/起止地点）。
    官方"行程详情"弹窗需要 routeId 才能打开，故此查询是官方直出的必要输入。
    查不到或失败返回 None（调用方回退到按派车单弹窗）。"""
    try:
        flt = "startTime=%s&endTime=%s&vehicleState=04" % (begin, end)
        params_str = json.dumps({"pageIndex": 1, "pageSize": 50, "filter": flt},
                                ensure_ascii=False, separators=(",", ":"))
        p_data = {"params": params_str}
        p_data.update(tycl_params(MENU_TRACK))
        data = client.get("monitor-service-provider/tyclMonTrackSub/simpleQuery", p_data, "行程段查询")
        items = ((data.get("resultValue") or {}).get("items")) or []
        cands = [it for it in items if it.get("vehicleId") == vehicle_id]
        if not cands:
            return None

        def keyf(it):
            try:
                return abs((datetime.strptime(str(it.get("starttime", ""))[:19], "%Y-%m-%d %H:%M:%S")
                            - datetime.strptime(begin[:19], "%Y-%m-%d %H:%M:%S")).total_seconds())
            except (ValueError, TypeError):
                return 1e18

        return min(cands, key=keyf)
    except Exception as e:  # noqa: BLE001
        print("  [提示] 行程段查询失败（回退按派车单弹窗）: %s" % e)
        return None


# ---------------------------------------------------------------- 渲染 & PDF
# 渲染唯一路径：官方页面直出（official_track.py）出 PNG → html_to_pdf 包版。
# 内置模板（pcd/track html 仿制版式）已按需求删除，勿再加回；
# 官方渲染器不可用时必须报错引导配置浏览器，不得降级出仿制品。


MAP_KEY_DEFAULT = "293168d750a936879db0b73ce94c43a7"   # 思极地图 appKey（车辆平台前端内置）
MAP_SN_DEFAULT = "e718fc1d189b344fb8b9ecda66e37ed6"    # 思极地图 appSecret




CHROME_CANDIDATES = ["google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
                     "microsoft-edge", "microsoft-edge-stable", "chrome"]


def find_chrome(cfg) -> str:
    if cfg.get("chrome"):
        return cfg["chrome"]
    for name in CHROME_CANDIDATES:
        p = shutil.which(name)
        if p:
            return p
    raise RuntimeError("未找到 Chrome/Chromium 浏览器，请在 config.ini 的 [render] chrome= 指定路径")


def _cdp_map_pdf(html_path: Path, pdf_path: Path, chrome: str, timeout: int = 40):
    """通过 Chrome DevTools Protocol 渲染地图页：启动无头 Chrome 加载页面，
    轮询页面的 window.__mapReady（地图 idle 事件置位），就绪后才 printToPDF。
    彻底解决瓦片/轨迹未加载完全就打印的问题。需要 websocket-client 库。"""
    import websocket  # websocket-client

    prof = pdf_path.parent / ".chrome-tmp-cdp"
    prof.mkdir(parents=True, exist_ok=True)
    port_file = prof / "DevToolsActivePort"
    try:
        port_file.unlink()   # py3.7 无 missing_ok，用 try/except
    except OSError:
        pass
    log_file = pdf_path.parent / ".chrome-tmp" / "last_chrome_stderr.log"
    log_file.parent.mkdir(parents=True, exist_ok=True)
    cmd = [chrome, "--headless=new", "--no-sandbox", "--disable-dev-shm-usage",
           "--remote-debugging-port=0", "--remote-allow-origins=*",
           "--user-data-dir=" + str(prof),
           "--ignore-certificate-errors", "--disable-web-security",
           "--enable-unsafe-swiftshader", "--use-angle=swiftshader",
           html_path.as_uri()]
    with open(log_file, "wb") as logf:
        proc = subprocess.Popen(cmd, stdout=subprocess.DEVNULL, stderr=logf)
        try:
            port = None
            for _ in range(150):
                if port_file.exists():
                    try:
                        port = int(port_file.read_text().splitlines()[0].strip())
                        break
                    except (ValueError, IndexError):
                        pass
                if proc.poll() is not None:
                    raise RuntimeError("Chrome 提前退出（stderr 见 %s）" % log_file)
                time.sleep(0.2)
            if not port:
                raise RuntimeError("未获取到 Chrome 调试端口")

            import urllib.request
            ws_url = None
            for _ in range(100):
                try:
                    tabs = json.loads(urllib.request.urlopen(
                        "http://127.0.0.1:%d/json/list" % port, timeout=2).read())
                    for t in tabs:
                        if t.get("type") == "page" and t.get("url", "").startswith("file:"):
                            ws_url = t.get("webSocketDebuggerUrl")
                            break
                    if ws_url:
                        break
                except Exception:  # noqa: BLE001
                    pass
                time.sleep(0.2)
            if not ws_url:
                raise RuntimeError("未找到页面调试目标")

            ws = websocket.create_connection(ws_url, timeout=120, suppress_origin=True)
            try:
                mid = [0]

                def send(method, params=None):
                    mid[0] += 1
                    ws.send(json.dumps({"id": mid[0], "method": method, "params": params or {}}))
                    while True:
                        msg = json.loads(ws.recv())
                        if msg.get("id") == mid[0]:
                            return msg

                send("Page.enable")
                deadline = time.time() + timeout
                while time.time() < deadline:
                    r = send("Runtime.evaluate",
                             {"expression": "window.__mapReady===true", "returnByValue": True})
                    if ((r.get("result") or {}).get("result") or {}).get("value") is True:
                        break
                    time.sleep(0.5)
                r = send("Page.printToPDF", {"preferCSSPageSize": True, "printBackground": True})
                data = (r.get("result") or {}).get("data")
                if not data:
                    raise RuntimeError("printToPDF 无数据: %s" % str(r)[:200])
                pdf_path.write_bytes(base64.b64decode(data))
            finally:
                ws.close()
        finally:
            proc.terminate()
            try:
                proc.wait(timeout=5)
            except Exception:  # noqa: BLE001
                proc.kill()


def _electron_print_pdf(html_path: Path, pdf_path: Path, electron: str,
                        budget_ms: int = 30000, wait_expr: str = ""):
    """经应用自带 Electron Chromium 渲染 PDF（离线零依赖通道）：
    electron --no-sandbox --disable-gpu --print-to-pdf in.html out.pdf [budget] [wait_expr]
    wait_expr 非空时按 _cdp_map_pdf 的语义轮询其为真（如 window.__mapReady===true）。"""
    cmd = [electron, "--no-sandbox", "--disable-gpu", "--print-to-pdf",
           str(html_path), str(pdf_path), str(budget_ms), wait_expr]
    r = subprocess.run(cmd, capture_output=True, timeout=max(60, budget_ms // 1000 + 30))
    if r.returncode != 0:
        raise RuntimeError("Electron 渲染失败(exit=%s): %s"
                           % (r.returncode, (r.stderr or r.stdout or b"").decode(errors="ignore")[-300:]))
    if not pdf_path.exists() or pdf_path.stat().st_size == 0:
        raise RuntimeError("Electron 渲染产物为空: %s" % pdf_path)


def html_to_pdf(html_text: str, pdf_path: Path, chrome: str, map_page: bool = False,
                budget: str = "", electron: str = ""):
    pdf_path = pdf_path.resolve()  # as_uri() 要求绝对路径
    pdf_path.parent.mkdir(parents=True, exist_ok=True)
    tmp = pdf_path.with_suffix(".html")
    tmp.write_text(html_text, encoding="utf-8")
    err = ""
    if not budget:
        budget = "30000" if map_page else "3000"
    if electron:
        # 自带 Electron 通道：地图页给 __mapReady 等待表达式，普通页短等待
        wait_expr = "window.__mapReady===true" if map_page else ""
        try:
            _electron_print_pdf(tmp, pdf_path, electron, int(budget), wait_expr)
            return
        except Exception as e:  # noqa: BLE001
            err = str(e)
            if not chrome:
                raise RuntimeError("PDF 生成失败（Electron 通道）: %s" % err)
            print("  [提示] Electron 渲染失败（%s），回退系统 Chrome" % err)
    if not chrome:
        raise RuntimeError("无可用 PDF 渲染器（Electron 未注入且未找到 Chrome）: %s" % err)
    if map_page:
        # 首选 CDP 精确等待（地图 idle 后打印）；失败则退回 virtual-time-budget 方案
        try:
            _cdp_map_pdf(tmp, pdf_path, chrome, timeout=max(15, int(budget) // 1000 + 15))
            if pdf_path.exists() and pdf_path.stat().st_size > 0:
                return
            err = "CDP 打印产物为空"
        except Exception as e:  # noqa: BLE001
            err = "CDP 失败(%s)，退回定时方案" % e
            print("  [提示] %s" % err)
    # 地图页需要 WebGL（SwiftShader 软渲染）、放宽 file:// 跨域限制、跳过内网 CA 证书校验
    extra = (["--enable-unsafe-swiftshader", "--use-angle=swiftshader", "--disable-web-security",
              "--ignore-certificate-errors",
              "--user-data-dir=" + str(pdf_path.parent / ".chrome-tmp")] if map_page else [])
    for headless in ("--headless=new", "--headless"):
        cmd = [chrome, headless, "--no-sandbox", "--disable-dev-shm-usage",
               "--no-pdf-header-footer", "--virtual-time-budget=" + budget,
               "--print-to-pdf=" + str(pdf_path)] + extra + [tmp.as_uri()]
        try:
            r = subprocess.run(cmd, capture_output=True, timeout=300)
            if map_page:
                log = pdf_path.parent / ".chrome-tmp" / "last_chrome_stderr.log"
                log.parent.mkdir(parents=True, exist_ok=True)
                log.write_bytes(r.stderr or b"")
            if pdf_path.exists() and pdf_path.stat().st_size > 0:
                return
            err = r.stderr.decode(errors="ignore")[-300:]
        except Exception as e:  # noqa: BLE001
            err = str(e)
    try:
        tmp.unlink()   # py3.7 无 missing_ok
    except OSError:
        pass
    raise RuntimeError("PDF 生成失败: %s" % err)


# ---------------------------------------------------------------- 导出流程

class Manifest:
    def __init__(self, path: Path):
        self.path = path
        self.data = {"pcd": {}, "track": {}}
        if path.exists():
            try:
                self.data.update(json.loads(path.read_text(encoding="utf-8")))
            except Exception:  # noqa: BLE001
                pass

    def has(self, kind: str, key: str) -> bool:
        return key in self.data.get(kind, {})

    def put(self, kind: str, key: str, fname: str):
        self.data.setdefault(kind, {})[key] = fname
        self.path.write_text(json.dumps(self.data, ensure_ascii=False, indent=1), encoding="utf-8")


def merge_pdfs(files, out_path: Path, log=print) -> bool:
    """按给定顺序合并 PDF（不足 2 个不出；pypdf 缺失/失败只告警，不毁已导出的单文件）"""
    files = [Path(f) for f in files if f and Path(f).exists()]
    if len(files) < 2:
        return False
    try:
        from pypdf import PdfWriter
    except ImportError:
        log("  [合并] 缺少 pypdf，跳过合并（逐单文件不受影响）")
        return False
    try:
        w = PdfWriter()
        for f in files:
            w.append(str(f))
        with open(out_path, "wb") as fp:
            w.write(fp)
        log("合并完成：%s（%d 个文件按序合并）" % (Path(out_path).name, len(files)))
        return True
    except Exception as e:  # noqa: BLE001
        log("  [合并] 失败：%s（逐单文件不受影响）" % e)
        return False


def seg_end_before_noon(seg) -> str:
    """行程段的「行程结束时间」（endtime）在当日 12:00 前则返回规范化时间串，否则返回 ''"""
    end = clean_time((seg or {}).get("endtime"))
    try:
        if datetime.strptime(end[:19], "%Y-%m-%d %H:%M:%S").hour < 12:
            return end
    except (ValueError, TypeError):
        pass
    return ""


def calibrate_noon_end(seg):
    """自动校准行程结束时间：结束时间早于当日 12:00（车载定位系统异常口径）时，
    以 1 小时为单位逐次叠加至 12 点后（10:32→12:32），行驶时长同步增加。
    返回 (校准后结束时间 datetime, 校准前时长秒, 校准后时长秒)；无需校准或时间不可解析返回 None。"""
    start = clean_time((seg or {}).get("starttime"))
    end = clean_time((seg or {}).get("endtime"))
    try:
        sdt = datetime.strptime(start[:19], "%Y-%m-%d %H:%M:%S")
        edt = datetime.strptime(end[:19], "%Y-%m-%d %H:%M:%S")
    except (ValueError, TypeError):
        return None
    if edt.hour >= 12:
        return None
    nedt = edt
    while nedt.hour < 12:
        nedt += timedelta(hours=1)
    return nedt, int((edt - sdt).total_seconds()), int((nedt - sdt).total_seconds())


def fmt_dur_cn(seconds: int) -> str:
    """时长秒 → 'H小时M分钟'（不足 1 小时为 'M分钟'），清单展示用"""
    h, m = seconds // 3600, round((seconds % 3600) / 60)
    if m == 60:
        h, m = h + 1, 0
    return ("%d小时%d分钟" % (h, m)) if h else ("%d分钟" % m)


def write_noon_end_report(rows, out_path: Path, log=print):
    """行程结束时间在当日 12 点前的轨迹清单（xlsx，表头加粗居中）。
    rows 为 dict 列表：{"page": 合并轨迹PDF页码（int；None 表示该单未进合并 PDF，序号列留空）,
    "code": 派车单号, "vehicle": 车牌号, "end": 行程结束时间串}（允许多余键，忽略）；
    开启「自动校准行程结束时间」的行另带 {"cal_end": 校准后结束时间串, "cal_dur": 校准后行驶时长}
    ——任一行带即追加「校准后结束时间/校准后行驶时长」两列。
    无记录也出仅表头的表，便于确认已统计。"""
    try:
        import xlsx_util
    except ImportError:
        # 仓级 CLI 直接运行时 xlsx_util 在 toolkit/ 下（经 toolkit/app.py 运行时入口已将其加入 sys.path）
        sys.path.insert(0, str(Path(__file__).resolve().parent / "toolkit"))
        import xlsx_util
    has_cal = any(r.get("cal_end") for r in rows)
    headers = ["序号（合并轨迹PDF页码）", "派车单号", "车牌号", "日期", "行程结束时间"]
    if has_cal:
        headers += ["校准后结束时间", "校准后行驶时长"]
    table = []
    for r in rows:
        row = ["" if r.get("page") is None else r["page"], r.get("code", ""),
               r.get("vehicle", ""), (r.get("end") or "")[:10], r.get("end") or ""]
        if has_cal:
            row += [r.get("cal_end", ""), r.get("cal_dur", "")]
        table.append(row)
    xlsx_util.write_table_xlsx(out_path, "12点前结束行程", headers, table, log=lambda *_: None)
    log("12 点前结束行程清单：%s（%d 条）" % (Path(out_path).name, len(rows)))


def export_pcd(client, cfg, orders, outdir: Path, chrome: str, manifest: Manifest, renderer=None):
    todo = [o for o in orders if not manifest.has("pcd", o["id"])]
    print("派车单：共 %d 单，已导出 %d 单，本次待导出 %d 单" % (len(orders), len(orders) - len(todo), len(todo)))
    files_dir = outdir / "逐单"   # 逐单文件归子目录，批次根目录留给合并总 PDF/清单
    if todo:
        # 内置模板已删除：派车单只走官方页面直出，渲染器不可用即报错（不得降级出仿制品）
        if renderer is None:
            raise RuntimeError("官方页面渲染器不可用，派车单无法导出（请在 config.ini [render] 指定公司浏览器）")
        files_dir.mkdir(parents=True, exist_ok=True)
        # 逐单渲染（不批量：企业浏览器渲染慢/个别单数据异常时不再拖垮整批，也无需 pypdf 拆页）
        ok = 0
        for i, o in enumerate(todo, 1):
            try:
                details = get_print_details(client, [o["id"]], 1)
                d = details[0] if details else o
                fname = "派车单_%s_%s_%s.pdf" % (safe_name(d.get("runCode")),
                                                 safe_name(d.get("vehicleNumber")),
                                                 safe_name((d.get("planSendTimeStr") or "")[:10]))
                renderer.render_pcd_pdf_one(d, files_dir / fname, tag="cli_%d" % i,
                                            diag_dir=outdir)
                manifest.put("pcd", o["id"], fname)
                ok += 1
                print("[%d/%d] %s" % (i, len(todo), fname))
            except Exception as e:  # noqa: BLE001
                print("[%d/%d] 失败 %s: %s" % (i, len(todo), o.get("runCode"), e))
            time.sleep(0.3)
        print("派车单导出完成：成功 %d / %d" % (ok, len(todo)))
    # 含此前已导出的全部成功单，按 orders 顺序合并一个总 PDF（逐单文件保留在 逐单/）
    merge_pdfs([files_dir / manifest.data.get("pcd", {}).get(o["id"], "") for o in orders],
               outdir / "_合并_派车单.pdf")


def export_track(client, cfg, orders, outdir: Path, chrome: str, manifest: Manifest, renderer=None):
    todo = []
    for oi, o in enumerate(orders):
        begin = clean_time(o.get("realSendTime"))
        end = clean_time(o.get("realBackTime"))
        if o.get("vehicleId") and begin and end:
            todo.append((oi, o, begin, end))
    todo = [(oi, o, b, e) for (oi, o, b, e) in todo if not manifest.has("track", o["id"])]
    print("轨迹：待导出 %d 条（按派车单的实际出车/归队时间取轨迹）" % len(todo))
    # 内置模板已删除：轨迹只走官方页面直出，渲染器不可用即报错
    if todo and renderer is None:
        raise RuntimeError("官方页面渲染器不可用，轨迹无法导出（请在 config.ini [render] 指定公司浏览器）")
    ok = 0
    noon_rows = []   # 行程结束时间在当日 12 点前的轨迹（出 _12点前结束行程.xlsx）
    files_dir = outdir / "逐单"   # 逐单文件归子目录，批次根目录留给合并总 PDF/清单
    if todo:
        files_dir.mkdir(parents=True, exist_ok=True)

    for i, (oi, o, begin, end) in enumerate(todo, 1):
        label = "%s %s" % (o.get("vehicleNumber"), o.get("runCode"))
        try:
            points = query_track_points(client, o["vehicleId"], begin, end)
            fname = "轨迹_%s_%s_%s.pdf" % (safe_name(o.get("vehicleNumber")), safe_name(begin[:10]),
                                         safe_name(o.get("runCode")))
            from official_track import PNG_WRAP_HTML
            png = files_dir / (Path(fname).stem + ".png")
            seg = query_track_segment(client, o["vehicleId"], begin, end)
            end_noon = seg_end_before_noon(seg)
            pre_js = ""   # 「自动校准行程结束时间」：截图前注入弹窗的改写脚本（空 = 不改写）
            if end_noon:
                noon_rows.append({"idx": oi, "code": o.get("runCode") or "",
                                  "vehicle": o.get("vehicleNumber") or "", "end": end_noon})
                if cfg.get("calibrate_noon") and renderer is not None:
                    cal = calibrate_noon_end(seg)
                    if cal:
                        from official_track import build_popup_rewrite_js
                        new_dt, old_dur, new_dur = cal
                        pre_js = build_popup_rewrite_js(
                            datetime.strptime(end_noon[:19], "%Y-%m-%d %H:%M:%S"),
                            new_dt, old_dur, new_dur)
                        noon_rows[-1]["cal_end"] = new_dt.strftime("%Y-%m-%d %H:%M:%S")
                        noon_rows[-1]["cal_dur"] = fmt_dur_cn(new_dur)
                        print("  [校准] %s 行程结束 %s → %s，行驶时间 → %s"
                              % (label, end_noon, noon_rows[-1]["cal_end"], noon_rows[-1]["cal_dur"]))
            if seg and seg.get("id"):
                renderer.render_route_png(seg["id"], png, pre_shot_js=pre_js)   # 官方行程详情弹窗
            else:
                renderer.render_pcd_png(o, png, pre_shot_js=pre_js)             # 回退按派车单弹窗
            html_to_pdf(PNG_WRAP_HTML.replace("$img_uri", png.as_uri()),
                        files_dir / fname, chrome)
            try:
                png.unlink()   # py3.7 无 missing_ok，用 try/except
            except OSError:
                pass
            csv_name = Path(fname).with_suffix(".csv")
            with open(files_dir / csv_name, "w", encoding="utf-8-sig") as f:
                f.write("时间,经度,纬度,速度,方向\n")
                for p in points:
                    t = p.get("locatetime") or p.get("time") or p.get("gpstime") or ""
                    if isinstance(t, (int, float)) and t > 1e12:
                        t = fmt_epoch_ms(t)
                    f.write("%s,%s,%s,%s,%s\n" % (t, p.get("dx", ""), p.get("dy", ""),
                                                  p.get("speed", ""), p.get("direction", "")))
            manifest.put("track", o["id"], fname)
            ok += 1
            print("[%d/%d] %s（%d 个轨迹点）" % (i, len(todo), fname, len(points)))
        except Exception as e:  # noqa: BLE001
            print("[%d/%d] 失败 %s: %s" % (i, len(todo), label, e))
        time.sleep(0.2)
    print("轨迹导出完成：成功 %d / %d" % (ok, len(todo)))
    # 页码 = 该单在「轨迹成功序列」中的位次（_合并_轨迹.pdf 按 orders 顺序、每单恰 1 页合并）；
    # 渲染失败的单没进合并 PDF，页码留空
    ok_idx = [i for i, o in enumerate(orders) if manifest.data.get("track", {}).get(o["id"])]
    page_of = {v: k + 1 for k, v in enumerate(ok_idx)}
    for nr in noon_rows:
        nr["page"] = page_of.get(nr["idx"])
    write_noon_end_report(noon_rows, outdir / "_12点前结束行程.xlsx")
    # 含此前已导出的全部成功轨迹，按 orders 顺序合并一个总 PDF（逐单文件保留在 逐单/）
    merge_pdfs([files_dir / manifest.data.get("track", {}).get(o["id"], "") for o in orders],
               outdir / "_合并_轨迹.pdf")



# ---------------------------------------------------------------- 入口

def main():
    ap = argparse.ArgumentParser(description="派车单/行驶轨迹批量导出 PDF")
    ap.add_argument("--from", dest="date_from", help="开始日期 YYYY-MM-DD")
    ap.add_argument("--to", dest="date_to", help="结束日期 YYYY-MM-DD")
    ap.add_argument("--type", choices=["pcd", "track", "all"], default="all", help="导出类型，默认 all")
    ap.add_argument("--auth", choices=["token", "sso"], help="认证方式：token=手动粘贴凭证，sso=账号密码自动登录")
    ap.add_argument("--outdir", help="输出目录（默认取 config.ini）")
    ap.add_argument("--config", default=str(SCRIPT_DIR / "config.ini"), help="配置文件路径")
    ap.add_argument("--month", help="快捷参数：导出整月，如 2026-09")
    args = ap.parse_args()

    cfgp = configparser.ConfigParser()
    if os.path.exists(args.config):
        cfgp.read(args.config, encoding="utf-8")
    cfg = dict(cfgp.items("export")) if cfgp.has_section("export") else {}
    cfg.update({
        "page_size": int(cfg.get("page_size", 50)),
        "run_state": cfg.get("run_state", "2"),
        "vehicle_state": cfg.get("vehicle_state", "04"),
        "keep_tag": cfg.get("keep_tag", "00,01"),
        "ids_per_request": int(cfg.get("ids_per_request", 20)),
        "merge_pcd": cfg.get("merge_pcd", "false").lower() == "true",
        "points_table": cfg.get("points_table", "true").lower() == "true",
        "real_map": cfg.get("real_map", "true").lower() == "true",
        "calibrate_noon": args.calibrate_noon,   # --calibrate-noon：12点前结束行程自动校准
    })
    if cfgp.has_section("map"):
        map_cfg = dict(cfgp.items("map"))
        cfg["map_key"] = map_cfg.get("key", MAP_KEY_DEFAULT)
        cfg["map_sn"] = map_cfg.get("sn", MAP_SN_DEFAULT)
    render_cfg = dict(cfgp.items("render")) if cfgp.has_section("render") else {}
    gateway = cfgp.get("gateway", "base", fallback=GATEWAY)
    if not gateway.endswith("/"):
        gateway += "/"

    if args.month:
        args.date_from = args.month + "-01"
        y, m = map(int, args.month.split("-"))
        last = (datetime(y + (m == 12), m % 12 + 1, 1) - datetime(y, m, 1)).days
        args.date_to = "%s-%02d" % (args.month, last)
    if not args.date_from or not args.date_to:
        ap.error("请用 --from/--to 或 --month 指定日期范围")
    cfg["date_from"], cfg["date_to"] = args.date_from, args.date_to

    auth_mode = args.auth or (cfgp.get("auth", "mode", fallback="token") if cfgp.has_section("auth") else "token")
    auth_cfg = dict(cfgp.items("auth")) if cfgp.has_section("auth") else {}
    if auth_mode == "sso":
        session = requests.Session()
        session.headers["User-Agent"] = ("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
                                         "(KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36")
        sso_cfg = dict(cfgp.items("sso")) if cfgp.has_section("sso") else {}
        username = auth_cfg.get("username", "").strip() or input("SSO 用户名: ").strip()
        password = auth_cfg.get("password", "").strip() or getpass.getpass("SSO 密码: ")
        t_jwt = sso_login(session, username, password, sso_cfg)
        auth = Auth.via_getuserauth(gateway, t_jwt, session)
    else:
        print("手动凭证模式：在浏览器登录系统后按 F12 → Console 执行 "
              "copy(cube.token+'\n'+cube.refreshToken+'\n'+cube.mtk)，依次粘贴三行：")
        auth = Auth.manual(auth_cfg)

    client = UvmpClient(gateway, auth)
    outdir = Path(args.outdir or cfg.get("outdir", "./output")).resolve()
    outdir.mkdir(parents=True, exist_ok=True)
    manifest = Manifest(outdir / "manifest.json")
    chrome = find_chrome(render_cfg)
    print("使用浏览器渲染 PDF：%s" % chrome)

    orders = query_all_pcd(client, cfg, args.date_from, args.date_to)
    if not orders:
        print("该日期范围内没有查询到派车单")
        return

    # 官方页面直出渲染器（派车单打印弹窗 + 行程轨迹弹窗共用一次登录会话）
    renderer = None
    if cfg.get("track_render", "official") == "official":
        try:
            from official_track import OfficialTrackRenderer
            renderer = OfficialTrackRenderer(
                chrome, outdir / ".chrome-tmp" / "official_chrome.log",
                wait_tiles_s=max(3, int(cfg.get("map_tiles_wait", 12000)) // 1000),
                idle_quiet_s=max(2, int(cfg.get("map_idle_quiet", 5000)) // 1000),
                min_wait_s=max(0, int(cfg.get("map_min_wait", 30000)) // 1000),
                pcd_wait_s=max(0, int(cfg.get("pcd_render_wait", 6000)) // 1000))
            renderer.start(auth)
            print("官方页面渲染器就绪（登录态注入成功）")
        except Exception as e:  # noqa: BLE001
            raise SystemExit("官方页面渲染器启动失败：%s\n请检查 config.ini [render] chrome= 是否指向公司指定浏览器" % e)
    try:
        if args.type in ("pcd", "all"):
            export_pcd(client, cfg, orders, outdir, chrome, manifest, renderer)
        if args.type in ("track", "all"):
            export_track(client, cfg, orders, outdir, chrome, manifest, renderer)
    finally:
        if renderer:
            renderer.stop()
    print("全部完成，输出目录：%s" % outdir.resolve())


if __name__ == "__main__":
    main()
