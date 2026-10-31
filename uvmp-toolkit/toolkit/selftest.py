# -*- coding: utf-8 -*-
"""环境自检任务（设置页「环境自检」卡片）：PDF 渲染自检 / SSO 登录测试 / CDP 渲染自检。
以普通任务形态运行（走 jobs 模型，日志/进度/结果与普通导出一致）。"""

import base64
import shutil
import time
from pathlib import Path

import config as _config
import jobs
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
    html = (
        "<!DOCTYPE html><html lang='zh-CN'><head><meta charset='utf-8'>"
        "<style>body{font-family:sans-serif;padding:40px;color:#22314E}"
        ".box{border:4px solid #F26D21;border-radius:12px;padding:30px;text-align:center}"
        "</style></head><body><div class='box'>"
        "<h2>内网工具箱 · PDF 渲染自检</h2></div></body></html>"
    )

    # 通道①：自带 Electron（包版打印用）
    eb = platform_utils.electron_binary()
    if eb:
        try:
            out = state.logs_dir() / "render-selftest-electron.pdf"
            ve.html_to_pdf(html, out, "", electron=eb, budget="5000")
            job.log(
                "① 自带 Electron：OK（%s，%.1f KB）" % (out, out.stat().st_size / 1024)
            )
            lanes.append({"lane": "electron", "ok": True, "path": eb})
        except Exception as e:  # noqa: BLE001
            job.log("① 自带 Electron：失败 %s" % e)
            lanes.append({"lane": "electron", "ok": False, "path": eb, "error": str(e)})
    else:
        lanes.append(
            {
                "lane": "electron",
                "ok": False,
                "path": "",
                "error": "源码模式无自带 Electron（打包版才有）",
            }
        )
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
            job.log(
                "② 浏览器（%s %s）：OK（%.1f KB）"
                % (src, chrome, out.stat().st_size / 1024)
            )
            lanes.append({"lane": "chrome", "ok": True, "path": chrome, "src": src})
        except Exception as e:  # noqa: BLE001
            job.log("② 浏览器（%s %s）：失败 %s" % (src, chrome, e))
            lanes.append(
                {
                    "lane": "chrome",
                    "ok": False,
                    "path": chrome,
                    "src": src,
                    "error": str(e),
                }
            )
    except Exception as e:  # noqa: BLE001
        job.log("② 浏览器：未找到（%s）" % e)
        lanes.append({"lane": "chrome", "ok": False, "path": "", "error": str(e)})

    ok_all = all(l["ok"] for l in lanes if l["lane"] == "chrome" or l["path"])
    if not any(l["ok"] for l in lanes):
        raise RuntimeError("两条渲染通道均不可用，请检查设置页浏览器选择")
    return {"lanes": lanes, "ok_all": ok_all}


_CDP_STEPS = ("启动浏览器", "导航与 eval", "新 tab", "截图 PNG", "printToPDF")


def _cdp_local_page(title: str, body: str) -> str:
    """纯本地 data:text/html 测试页（base64 编码，避免转义问题）"""
    html = "<html><head><title>%s</title></head><body>%s</body></html>" % (title, body)
    return "data:text/html;base64," + base64.b64encode(html.encode("utf-8")).decode(
        "ascii"
    )


def _cdp_check_response(r: dict, what: str):
    """CDP 顶层 error 响应（协议/上下文错误）必须进入错误消息，不能静默当空结果"""
    err = (r or {}).get("error")
    if err:
        raise RuntimeError(
            "CDP %s 返回错误（%s）：%s"
            % (what, err.get("code", "?"), err.get("message", ""))
        )
    return r


def _cdp_failure(job, prefix: str, exc: Exception) -> Exception:
    """取消期间浏览器被销毁引发的异常按取消收尾，不误报 failed"""
    if job.cancelled:
        return jobs.Cancelled()
    return RuntimeError("%s：%s" % (prefix, exc))


def _cdp_tab_eval(tab: dict, expression: str, timeout: int = 30):
    """在指定标签页上下文 eval（与 CdpBrowser.eval 同口径）"""
    r = tab["call"](
        "Runtime.evaluate",
        {"expression": expression, "returnByValue": True},
        timeout=timeout,
    )
    _cdp_check_response(r, "Runtime.evaluate")
    return ((r.get("result") or {}).get("result") or {}).get("value")


def _cdp_wait_tab_expr(
    tab: dict, expression: str, timeout: float = 30, interval: float = 0.2
) -> bool:
    deadline = time.time() + timeout
    while time.time() < deadline:
        if _cdp_tab_eval(tab, expression) is True:
            return True
        time.sleep(interval)
    return False


def run_cdp(job, params: dict) -> dict:
    """CDP 渲染自检：以 strict=True 的 CdpBrowser 驱动用户所选/自动探测的浏览器，
    只打开 data: 本地页（零网络），验证导航 / eval / 新 tab / 截图 PNG / printToPDF；
    浏览器 profile/日志写在按 job.id 安全后缀隔离的工作目录（成功/取消只清本 job profile），
    PNG/PDF 写日志目录（cdp-selftest.png / cdp-selftest.pdf）。无浏览器或链路失败明确报错。"""
    from official_track import CdpBrowser

    cfg = _config.load()
    rcfg = _config.render_cfg(cfg)
    chosen = (rcfg.get("chrome") or "").strip()
    try:
        chrome = chosen or platform_utils.detect_chrome("")
    except Exception as e:  # noqa: BLE001
        raise RuntimeError(
            "CDP 渲染自检：未找到可用浏览器（%s）；请到「设置」页选择公司指定浏览器。"
            % e
        )
    out = state.logs_dir()
    job_key = ve.safe_name(getattr(job, "id", "")) or "job"
    work = out / ("cdp-selftest-work-" + job_key)
    work.mkdir(parents=True, exist_ok=True)
    job.log(
        "CDP 渲染自检：浏览器 %s（%s）" % (chrome, "用户指定" if chosen else "自动探测")
    )
    job.log("CDP 渲染自检：产物目录 %s" % out)

    checks = []

    def step(name: str):
        job.check_cancel()
        checks.append(name)
        job.log("  [自检] %s：OK" % name)
        job.set_progress(len(checks), len(_CDP_STEPS), name)

    try:
        br = CdpBrowser(chrome, log_file=work / "browser.log", strict=True)
    except Exception as e:  # noqa: BLE001
        raise _cdp_failure(job, "CDP 渲染自检：启动浏览器失败", e) from e
    closed = [False]

    def _close():
        if not closed[0]:
            closed[0] = True
            try:
                br.close()
            except Exception:  # noqa: BLE001
                pass

    job.add_cancel_hook(_close)
    tab = None
    ok = False
    try:
        step("启动浏览器")
        job.check_cancel()

        try:
            br.navigate(_cdp_local_page("main-page", "hello"))
            if not br.wait_expr("document.title === 'main-page'", timeout=30):
                raise RuntimeError("等待主 target 导航就绪超时")
            title = br.eval("document.title")
            if title != "main-page":
                raise RuntimeError(
                    "eval document.title = %r；want main-page" % (title,)
                )
            value = br.eval("1+2")
            if value != 3:
                raise RuntimeError("eval 1+2 = %r；want 3" % (value,))
        except Exception as e:  # noqa: BLE001
            raise _cdp_failure(job, "CDP 渲染自检：导航与 eval 失败", e) from e
        step("导航与 eval")

        try:
            tab = br.new_tab()
            if _cdp_tab_eval(tab, "1+1") != 2:
                raise RuntimeError("新 tab eval 1+1 != 2")
            _cdp_check_response(
                tab["call"](
                    "Page.navigate", {"url": _cdp_local_page("tab-page", "pdf body")}
                ),
                "Page.navigate",
            )
            if not _cdp_wait_tab_expr(tab, "document.title === 'tab-page'"):
                raise RuntimeError("等待新 tab 导航就绪超时")
            tab_title = _cdp_tab_eval(tab, "document.title")
            if tab_title != "tab-page":
                raise RuntimeError(
                    "新 tab eval document.title = %r；want tab-page" % (tab_title,)
                )
        except Exception as e:  # noqa: BLE001
            raise _cdp_failure(job, "CDP 渲染自检：新 tab 失败", e) from e
        step("新 tab")

        png_path = out / "cdp-selftest.png"
        try:
            br.screenshot_clip(
                {"x": 0, "y": 0, "width": 800, "height": 600, "scale": 1}, png_path
            )
            png_data = png_path.read_bytes()
            if not png_data.startswith(b"\x89PNG"):
                raise RuntimeError("截图不是 PNG（%d 字节）" % len(png_data))
        except Exception as e:  # noqa: BLE001
            raise _cdp_failure(job, "CDP 渲染自检：截图 PNG 失败", e) from e
        step("截图 PNG")

        try:
            r = tab["call"]("Page.printToPDF", {"printBackground": True}, timeout=60)
            _cdp_check_response(r, "Page.printToPDF")
            pdf_data = base64.b64decode(((r.get("result") or {}).get("data")) or "")
            if not pdf_data.startswith(b"%PDF"):
                raise RuntimeError("printToPDF 产物不是 PDF（%d 字节）" % len(pdf_data))
            pdf_path = out / "cdp-selftest.pdf"
            pdf_path.write_bytes(pdf_data)
        except Exception as e:  # noqa: BLE001
            raise _cdp_failure(job, "CDP 渲染自检：printToPDF 失败", e) from e
        step("printToPDF")

        ok = True
        return {
            "ok": True,
            "png": str(png_path),
            "pdf": str(pdf_path),
            "png_bytes": len(png_data),
            "pdf_bytes": len(pdf_data),
            "browser": chrome,
            "work_dir": str(work),
            "checks": checks,
        }
    finally:
        if tab is not None:
            try:
                br.close_tab(tab)
            except Exception:  # noqa: BLE001
                pass
        _close()
        if ok or job.cancelled:
            # 成功/取消后不留临时 Chrome profile；失败保留现场供诊断（保留浏览器日志与产物）
            prof = getattr(br, "prof", None) or (work / ".chrome-official-tmp")
            shutil.rmtree(prof, ignore_errors=True)
            if Path(prof).exists():
                job.log(
                    "CDP 渲染自检：临时 profile 未能清理（可能仍被占用）：%s" % prof
                )


def run_sso(job, params: dict) -> dict:
    """只验证 SSO 登录与凭证换取，不导出（mock 模式下走模拟客户端）"""
    cfg = _config.load()
    client, auth = uvmp.make_client(cfg, job.log)
    token = getattr(auth, "token", "") or ""
    return {
        "ok": True,
        "token_len": len(token),
        "mock": bool((cfg.get("dev") or {}).get("mock")),
    }
