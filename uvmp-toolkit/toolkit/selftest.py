# -*- coding: utf-8 -*-
"""环境自检任务（设置页「环境自检」卡片）：PDF 渲染自检 / SSO 登录测试。
以普通任务形态运行（走 jobs 模型，日志/进度/结果与普通导出一致）。"""
import config as _config
import platform_utils
import state
import uvmp
import vehicle_export as ve

APP_ID = "selftest"


def run_render(job, params: dict) -> dict:
    """双通道自检：①自带 Electron 包版打印 ②用户所选/自动探测浏览器 CLI 打印。
    官方弹窗截图（CDP）走的是通道②的真实浏览器——两条分开验。"""
    cfg = _config.load()
    lanes = []
    html = ("<!DOCTYPE html><html lang='zh-CN'><head><meta charset='utf-8'>"
            "<style>body{font-family:sans-serif;padding:40px;color:#22314E}"
            ".box{border:4px solid #F26D21;border-radius:12px;padding:30px;text-align:center}"
            "</style></head><body><div class='box'>"
            "<h2>内网工具箱 · PDF 渲染自检</h2></div></body></html>")

    # 通道①：自带 Electron（包版打印用）
    eb = platform_utils.electron_binary()
    if eb:
        try:
            out = state.logs_dir() / "render-selftest-electron.pdf"
            ve.html_to_pdf(html, out, "", electron=eb, budget="5000")
            job.log("① 自带 Electron：OK（%s，%.1f KB）" % (out, out.stat().st_size / 1024))
            lanes.append({"lane": "electron", "ok": True, "path": eb})
        except Exception as e:  # noqa: BLE001
            job.log("① 自带 Electron：失败 %s" % e)
            lanes.append({"lane": "electron", "ok": False, "path": eb, "error": str(e)})
    else:
        lanes.append({"lane": "electron", "ok": False, "path": "",
                      "error": "源码模式无自带 Electron（打包版才有）"})
        job.log("① 自带 Electron：源码模式无，跳过")

    # 通道②：真实浏览器（官方直出用；用户所选优先）
    rcfg = _config.render_cfg(cfg)
    chosen = (rcfg.get("chrome") or "").strip()
    try:
        chrome = chosen or platform_utils.detect_chrome("")
        src = "用户指定" if chosen else "自动探测"
        try:
            out = state.logs_dir() / "render-selftest-chrome.pdf"
            ve.html_to_pdf(html, out, chrome, budget="5000")
            job.log("② 浏览器（%s %s）：OK（%.1f KB）" % (src, chrome, out.stat().st_size / 1024))
            lanes.append({"lane": "chrome", "ok": True, "path": chrome, "src": src})
        except Exception as e:  # noqa: BLE001
            job.log("② 浏览器（%s %s）：失败 %s" % (src, chrome, e))
            lanes.append({"lane": "chrome", "ok": False, "path": chrome, "src": src,
                          "error": str(e)})
    except Exception as e:  # noqa: BLE001
        job.log("② 浏览器：未找到（%s）" % e)
        lanes.append({"lane": "chrome", "ok": False, "path": "", "error": str(e)})

    ok_all = all(l["ok"] for l in lanes if l["lane"] == "chrome" or l["path"])
    if not any(l["ok"] for l in lanes):
        raise RuntimeError("两条渲染通道均不可用，请检查设置页浏览器选择")
    return {"lanes": lanes, "ok_all": ok_all}


def run_sso(job, params: dict) -> dict:
    """只验证 SSO 登录与凭证换取，不导出（mock 模式下走模拟客户端）"""
    cfg = _config.load()
    client, auth = uvmp.make_client(cfg, job.log)
    token = getattr(auth, "token", "") or ""
    return {"ok": True, "token_len": len(token),
            "mock": bool((cfg.get("dev") or {}).get("mock"))}
