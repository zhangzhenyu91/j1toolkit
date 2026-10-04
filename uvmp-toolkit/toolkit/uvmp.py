# -*- coding: utf-8 -*-
"""派车系统客户端获取：统一处理 mock 开关、SSO 登录、token 凭证与官方渲染器启动。

- dev.mock=true → devmock.FakeClient（离线开发）
- config.ini [auth] mode=sso（默认）→ SSO 账号密码自动登录（凭据：config.json 优先，config.ini 兜底）
- mode=token → 直接使用 config.ini [auth] 已填写的三件套（GUI/定时器环境不能交互输入）
"""
import sys
from pathlib import Path

TOOLKIT_DIR = Path(__file__).resolve().parent
PROJECT_DIR = TOOLKIT_DIR.parent
for _p in (str(TOOLKIT_DIR), str(PROJECT_DIR)):
    if _p not in sys.path:
        sys.path.insert(0, _p)

import config as _config  # noqa: E402


def make_client(cfg: dict, log=print):
    """返回（client, auth）。client 接口形状同 vehicle_export.UvmpClient"""
    if (cfg.get("dev") or {}).get("mock"):
        import devmock
        client = devmock.fake_client(cfg, log)
        return client, client.auth

    import vehicle_export as ve
    import requests

    gateway = _config.gateway(cfg)
    ini_auth = (cfg.get("_ini") or {}).get("auth") or {}
    mode = (ini_auth.get("mode") or "sso").strip()

    if mode == "token":
        token = (ini_auth.get("token") or "").strip()
        refresh = (ini_auth.get("refresh_token") or "").strip()
        mtk = (ini_auth.get("mtk") or "").strip()
        if not (token and refresh and mtk):
            raise RuntimeError("config.ini [auth] mode=token，但 token/refresh_token/mtk 未填齐；"
                               "GUI 环境不能交互粘贴凭证，请改用 sso 模式或先在 config.ini 填好")
        auth = ve.Auth(token, refresh, mtk)
        log("使用 config.ini 中的手动凭证（token 模式）")
        return ve.UvmpClient(gateway, auth), auth

    username, password = _config.sso_credentials(cfg)
    if not username or not password:
        raise RuntimeError("未配置 SSO 账号密码：请在工具箱设置页填写，或在 config.ini [auth] 填写")
    session = requests.Session()
    session.headers["User-Agent"] = ("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
                                     "(KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36")
    log("SSO 登录中（%s）..." % username)
    t_jwt = ve.sso_login(session, username, password, _config.sso_cfg(cfg))
    auth = ve.Auth.via_getuserauth(gateway, t_jwt, session)
    log("登录成功，凭证已就绪")
    return ve.UvmpClient(gateway, auth), auth


def start_renderer(client, auth, cfg: dict, work_dir: Path, log=print):
    """按配置启动官方页面直出渲染器；启动失败返回 None（调用方回退内置模板渲染）。
    mock 模式下直接返回 None。"""
    if (cfg.get("dev") or {}).get("mock"):
        return None
    vecfg = _config.ve_cfg(cfg)
    if vecfg.get("track_render", "official") != "official":
        return None
    try:
        from official_track import OfficialTrackRenderer
        import platform_utils
        chrome = platform_utils.detect_chrome(_config.render_cfg(cfg).get("chrome", ""))
        log("官方页面渲染器启动中（浏览器：%s）..." % chrome)
        renderer = OfficialTrackRenderer(
            chrome, Path(work_dir) / ".chrome-tmp" / "official_chrome.log",
            wait_tiles_s=max(3, int(vecfg.get("map_tiles_wait", 12000)) // 1000),
            idle_quiet_s=max(2, int(vecfg.get("map_idle_quiet", 5000)) // 1000),
            min_wait_s=max(0, int(vecfg.get("map_min_wait", 30000)) // 1000),
            pcd_wait_s=max(0, int(vecfg.get("pcd_render_wait", 6000)) // 1000))
        renderer.start(auth)
        log("官方页面渲染器就绪（登录态注入成功）")
        return renderer
    except Exception as e:  # noqa: BLE001
        log("官方页面渲染器启动失败：%s" % e)
        return None
