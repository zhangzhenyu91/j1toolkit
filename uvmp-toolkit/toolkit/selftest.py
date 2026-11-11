# -*- coding: utf-8 -*-
"""环境自检任务（设置页「环境自检」卡片）：PDF 渲染自检 / SSO 登录测试 / CDP 渲染自检。
以普通任务形态运行（走 jobs 模型，日志/进度/结果与普通导出一致）。"""

import base64
import shutil
import time
from datetime import datetime, timedelta, timezone
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
_WORK_PREFIX = "cdp-selftest-work-"


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


def _remove_work_dir(job, out: Path, work: Path):
    """删掉本 job 的自检工作目录。

    只允许删日志目录下直系、名字以自检前缀开头的真实目录：job.id 异常（含 ../、软链）
    时宁可不删并记日志，也不能越界清理其他 job 或用户文件。"""
    work = Path(work)
    try:
        if work.is_symlink():
            job.log("CDP 渲染自检：工作目录是软链，跳过清理：%s" % work)
            return
        if not work.is_dir():
            return
        if work.parent.resolve() != Path(out).resolve():
            job.log("CDP 渲染自检：工作目录不在日志目录内，跳过清理：%s" % work)
            return
        if not work.name.startswith(_WORK_PREFIX):
            job.log("CDP 渲染自检：工作目录名不是自检目录，跳过清理：%s" % work)
            return
        shutil.rmtree(work)
    except Exception as e:  # noqa: BLE001
        job.log("CDP 渲染自检：工作目录清理失败（可能仍被占用）：%s（%s）" % (work, e))
        return
    if work.exists():
        job.log("CDP 渲染自检：工作目录未能清理（可能仍被占用）：%s" % work)


def run_cdp(job, params: dict) -> dict:
    """CDP 渲染自检：以 strict=True 的 CdpBrowser 驱动用户所选/自动探测的浏览器，
    只打开 data: 本地页（零网络），验证导航 / eval / 新 tab / 截图 PNG / printToPDF；
    浏览器 profile/日志写在按 job.id 安全后缀隔离的工作目录（成功/取消删掉本 job
    整个工作目录，失败保留现场供诊断），PNG/PDF 写日志目录
    （cdp-selftest.png / cdp-selftest.pdf）。无浏览器或链路失败明确报错。"""
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
    work = out / (_WORK_PREFIX + job_key)
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
            # 成功/取消后不留自检痕迹：只删本 job 的整个工作目录（profile + 浏览器日志），
            # 产物 PNG/PDF 在日志目录，不受影响。失败保留现场供诊断。
            _remove_work_dir(job, out, work)


_CST = timezone(timedelta(hours=8))


def _sso_probe_filter() -> str:
    """只读鉴权探针的 filter：当日窗口（北京时间），不附加状态限制"""
    day = datetime.now(_CST).strftime("%Y-%m-%d")
    return "planSendTime=%s 00:00&planSendTime2=%s 23:59:00" % (day, day)


def _sso_probe(client) -> dict:
    """最小只读鉴权：派车单查询第 1 页 1 条，确认服务端接受当前凭证。
    能构造客户端不等于凭证可用（token 过期/签名错只在真正请求时才暴露）。"""
    data = ve.query_pcd_page_filter(client, _sso_probe_filter(), 1, 1)
    if not isinstance(data, dict):
        raise RuntimeError("响应不是 JSON 对象：%s" % str(data)[:200])
    if data.get("successful") is False:
        raise RuntimeError("接口返回失败：%s" % str(data)[:300])
    rv = data.get("resultValue") or {}
    if not isinstance(rv, dict):
        raise RuntimeError("响应 resultValue 不是对象：%s" % str(rv)[:200])
    total = rv.get("itemCount")
    return {
        "item_count": int(total) if isinstance(total, (int, float)) else 0,
        "page_size": 1,
    }


def run_sso(job, params: dict) -> dict:
    """验证凭证真正可用：先换取登录凭证，再用同一个客户端发一次最小只读查询。
    mock 模式完全离线（devmock 假客户端，不发任何请求）；真实模式（sso / token）
    凭证换取或只读查询任一步失败都以异常透传，不把「构造出客户端」当成凭证就绪。"""
    cfg = _config.load()
    mock = bool((cfg.get("dev") or {}).get("mock"))
    mode = "mock"
    if not mock:
        ini_auth = (cfg.get("_ini") or {}).get("auth") or {}
        mode = str(ini_auth.get("mode") or "sso").strip()
    job.log("环境自检·凭证：模式 %s" % mode)

    try:
        client, auth = uvmp.make_client(cfg, job.log)
    except Exception as e:  # noqa: BLE001
        raise RuntimeError("凭证获取失败：%s" % e) from e
    token = getattr(auth, "token", "") or ""
    if not token:
        raise RuntimeError("凭证获取失败：登录后 token 为空，请检查 [auth] 配置或重新登录")

    if mock:
        job.log("mock 模式：使用离线模拟客户端，未访问网络")
        return {
            "ok": True,
            "mode": mode,
            "mock": True,
            "check": "offline",
            "check_desc": "mock 离线：仅校验模拟客户端可构造，未访问网络",
            "token_len": len(token),
            "probe": None,
        }

    job.log("凭证已换取，开始只读鉴权校验（派车单查询第 1 页 1 条）...")
    job.check_cancel()
    try:
        probe = _sso_probe(client)
    except Exception as e:  # noqa: BLE001
        raise RuntimeError("只读鉴权校验失败：%s" % e) from e
    job.log("只读鉴权校验通过：服务端接受当前凭证（命中 %d 条）" % probe["item_count"])
    return {
        "ok": True,
        "mode": mode,
        "mock": False,
        "check": "readonly",
        "check_desc": "只读查询：派车单第 1 页 1 条（服务端已接受当前凭证）",
        "token_len": len(token),
        "probe": probe,
    }
