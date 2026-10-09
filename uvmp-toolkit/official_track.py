#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""官方页面直出渲染器：用 CDP 驱动无头 Chrome 打开真实派车系统，
调用官方 cube.showDialog 打开"行程轨迹"弹窗（官方 dataform 面板 + 官方 SGMap
轨迹图 + 官方拖拽手柄），对弹窗内容区截图并生成 PDF。
不再手工仿制官方样式。"""
import base64
import json
import subprocess
import time
import urllib.request
from collections import deque
from pathlib import Path

import websocket  # websocket-client

INDEX_URL = "http://uvmp.sgcc.com.cn/uvmp-web/factoryLayout/index.html"

# 国密浏览器无头模式缺 window.crypto.random（页面 SM.js 的 SM2 加密依赖它取随机数，
# 缺失会连锁炸掉页面登录/初始化），用标准 getRandomValues 补齐；须在页面脚本之前注入
CRYPTO_RANDOM_POLYFILL_JS = r"""
(function(){
  try {
    var c = window.crypto;
    if (c && typeof c.random !== 'function') {
      c.random = function(len){
        var n = (typeof len === 'number' && len > 0 && len < 4096) ? len : 32;
        var b = new Uint8Array(n);
        c.getRandomValues(b);
        var s = '';
        for (var i = 0; i < n; i++) s += ('0' + b[i].toString(16)).slice(-2);
        return s;
      };
    }
  } catch (e) {}
})();
"""


class CdpBrowser:
    def __init__(self, chrome: str, width: int = 1500, height: int = 950, log_file: Path = None):
        self.prof = Path(log_file).parent / ".chrome-official-tmp" if log_file else Path(".chrome-official-tmp")
        self.prof.mkdir(parents=True, exist_ok=True)
        port_file = self.prof / "DevToolsActivePort"
        try:
            port_file.unlink()   # py3.7 无 missing_ok（麒麟核心包基线），用 try/except
        except OSError:
            pass
        cmd = [chrome, "--headless=new", "--no-sandbox", "--disable-dev-shm-usage",
               "--remote-debugging-port=0", "--remote-allow-origins=*",
               "--user-data-dir=" + str(self.prof),
               "--ignore-certificate-errors", "--disable-web-security",
               "--enable-unsafe-swiftshader", "--use-angle=swiftshader",
               "--window-size=%d,%d" % (width, height), "about:blank"]
        stderr = open(log_file, "wb") if log_file else subprocess.DEVNULL
        self.proc = subprocess.Popen(cmd, stdout=subprocess.DEVNULL,
                                     stderr=stderr if log_file else subprocess.DEVNULL)
        port = None
        for _ in range(150):
            if port_file.exists():
                try:
                    port = int(port_file.read_text().splitlines()[0].strip())
                    break
                except (ValueError, IndexError):
                    pass
            if self.proc.poll() is not None:
                raise RuntimeError("Chrome 提前退出")
            time.sleep(0.2)
        if not port:
            raise RuntimeError("未获取到 Chrome 调试端口")
        self.port = port
        ws_url = None
        for _ in range(100):
            try:
                tabs = json.loads(urllib.request.urlopen(
                    "http://127.0.0.1:%d/json/list" % port, timeout=2).read())
                for t in tabs:
                    if t.get("type") == "page":
                        ws_url = t.get("webSocketDebuggerUrl")
                        break
                if ws_url:
                    break
            except Exception:  # noqa: BLE001
                pass
            time.sleep(0.2)
        if not ws_url:
            raise RuntimeError("未找到页面调试目标")
        self.ws = websocket.create_connection(ws_url, timeout=120, suppress_origin=True)
        self._mid = [0]
        self.console_log = []   # 页面 console/异常环形缓冲（诊断用，见 call() 事件捕获）
        self._net_requests = {}  # requestId -> url（在途请求表：失败定位 + 静默检测）
        self._net_done = deque(maxlen=600)  # 已完成请求 URL（min_seen 判定用）
        self.call("Page.enable")
        self.call("Runtime.enable")   # 捕获 consoleAPICalled / exceptionThrown
        self.call("Log.enable")       # 捕获 Log.entryAdded（网络/资源错误也在这）
        self.call("Network.enable")   # 捕获资源加载失败/HTTP 错误（require/组件加载诊断）
        self.call("Page.addScriptToEvaluateOnNewDocument",
                  {"source": CRYPTO_RANDOM_POLYFILL_JS})

    def _capture_event(self, msg: dict):
        """call() 等待响应期间收到的事件消息：console/异常入环形缓冲"""
        method = msg.get("method", "")
        if method == "Runtime.consoleAPICalled":
            p = msg.get("params", {})
            args = " ".join(str(a.get("value", a.get("description", "")))[:300]
                            for a in p.get("args", []))
            self.console_log.append("[console.%s] %s" % (p.get("type"), args))
        elif method == "Runtime.exceptionThrown":
            d = msg.get("params", {}).get("exceptionDetails", {})
            exc = d.get("exception") or {}
            self.console_log.append("[异常] %s %s" % (d.get("text", ""),
                                                       exc.get("description", "")[:400]))
        elif method == "Log.entryAdded":
            e = msg.get("params", {}).get("entry", {})
            self.console_log.append("[log.%s] %s" % (e.get("level"), str(e.get("text", ""))[:300]))
        elif method == "Network.requestWillBeSent":
            p = msg.get("params", {})
            rid = p.get("requestId")
            if rid:
                if len(self._net_requests) > 2000:
                    self._net_requests.clear()
                self._net_requests[rid] = (p.get("request") or {}).get("url", "")
        elif method == "Network.responseReceived":
            p = msg.get("params", {})
            resp = p.get("response") or {}
            status = resp.get("status") or 0
            if status >= 400:
                url = resp.get("url") or self._net_requests.get(p.get("requestId"), "")
                self.console_log.append("[net.%d] %s" % (status, url[:220]))
        elif method == "Network.loadingFailed":
            p = msg.get("params", {})
            url = self._net_requests.pop(p.get("requestId"), "")
            if url:
                self._net_done.append(url)
            err = p.get("errorText", "")
            if "ERR_ABORTED" not in err:   # 导航中断属常态噪音，不记
                self.console_log.append("[net.fail] %s %s" % (err, url[:220]))
        elif method == "Network.loadingFinished":
            # 在途表出清（wait_network_idle 的"加载完成"信号源）
            url = self._net_requests.pop(msg.get("params", {}).get("requestId"), "")
            if url:
                self._net_done.append(url)
        if len(self.console_log) > 300:
            del self.console_log[:100]

    def dump_diag(self, out_dir: Path, tag: str):
        """失败现场打包：console 日志 + 整页截图，文件名带 tag 序号"""
        try:
            out_dir = Path(out_dir)
            out_dir.mkdir(parents=True, exist_ok=True)
            if self.console_log:
                (out_dir / ("diag_console_%s.log" % tag)).write_text(
                    "\n".join(self.console_log), encoding="utf-8")
            r = self.call("Page.captureScreenshot", {"format": "png"})
            data = (r.get("result") or {}).get("data")
            if data:
                (out_dir / ("diag_page_%s.png" % tag)).write_bytes(base64.b64decode(data))
            print("  [诊断] console 日志与截图已写入 %s（diag_*_%s.*）" % (out_dir, tag))
        except Exception:  # noqa: BLE001
            pass

    def call(self, method, params=None, timeout=120):
        self._mid[0] += 1
        self.ws.settimeout(timeout)
        self.ws.send(json.dumps({"id": self._mid[0], "method": method, "params": params or {}}))
        while True:
            msg = json.loads(self.ws.recv())
            if msg.get("id") == self._mid[0]:
                return msg
            self._capture_event(msg)   # 等待期间的异步事件入缓冲

    def _new_ws(self, ws_url):
        ws = websocket.create_connection(ws_url, timeout=120, suppress_origin=True)
        state = {"mid": 0}

        def call(method, params=None, timeout=120):
            state["mid"] += 1
            ws.settimeout(timeout)
            ws.send(json.dumps({"id": state["mid"], "method": method, "params": params or {}}))
            while True:
                msg = json.loads(ws.recv())
                if msg.get("id") == state["mid"]:
                    return msg

        return ws, call

    def new_tab(self):
        """打开新标签页，返回 {ws, call, id}"""
        info = None
        for method in ("PUT", "GET", None):
            try:
                req = urllib.request.Request("http://127.0.0.1:%d/json/new?about:blank" % self.port,
                                             method=method)
                info = json.loads(urllib.request.urlopen(req, timeout=5).read())
                break
            except Exception:  # noqa: BLE001
                continue
        if not info:
            raise RuntimeError("无法创建新标签页")
        ws, call = self._new_ws(info["webSocketDebuggerUrl"])
        call("Page.enable")
        return {"ws": ws, "call": call, "id": info["id"]}

    def close_tab(self, tab):
        try:
            tab["ws"].close()
        except Exception:  # noqa: BLE001
            pass
        try:
            urllib.request.urlopen("http://127.0.0.1:%d/json/close/%s" % (self.port, tab["id"]),
                                   timeout=5).read()
        except Exception:  # noqa: BLE001
            pass

    def eval(self, expression, timeout=30):
        r = self.call("Runtime.evaluate",
                      {"expression": expression, "returnByValue": True}, timeout=timeout)
        return ((r.get("result") or {}).get("result") or {}).get("value")

    def wait_expr(self, expression, timeout=60, interval=0.5):
        deadline = time.time() + timeout
        while time.time() < deadline:
            if self.eval(expression) is True:
                return True
            time.sleep(interval)
        return False

    def wait_network_idle(self, quiet_s=3.0, timeout=60, url_substr="", min_seen=0):
        """等网络请求静默（瓦片/接口"加载完成"的信号）：在途请求归零且持续 quiet_s。
        事件随每次 eval 往返泵入（见 call()），轮询即泵；url_substr 可只盯某域名
        （如地图瓦片域名），避开首页实时监控自身的周期轮询。
        min_seen>0：须先累计见到 N 个匹配请求才开始计静默（以等待开始时的已完成数为
        基线，防"本弹窗加载还没开始就误判完成"——内网实测 3s 裸静默曾截早了几条）。"""
        def _counts():
            if url_substr:
                done_n = sum(1 for u in self._net_done if url_substr in u)
                busy = [u for u in self._net_requests.values() if url_substr in u]
            else:
                done_n = len(self._net_done)
                busy = list(self._net_requests.values())
            return done_n, busy

        deadline = time.time() + timeout
        base_done, _ = _counts()
        idle_since = None
        while time.time() < deadline:
            self.eval("1")   # 泵事件 + 保活
            done_n, busy = _counts()
            seen = max(0, done_n - base_done) + len(busy)
            if busy or seen < min_seen:
                idle_since = None
            elif idle_since is None:
                idle_since = time.time()
            elif time.time() - idle_since >= quiet_s:
                return True
            time.sleep(0.4)
        return False

    def navigate(self, url):
        self.call("Page.navigate", {"url": url})

    def screenshot_clip(self, clip: dict, out_png: Path):
        r = self.call("Page.captureScreenshot", {"format": "png", "clip": clip})
        data = (r.get("result") or {}).get("data")
        if not data:
            raise RuntimeError("截图失败: %s" % str(r)[:200])
        out_png.write_bytes(base64.b64decode(data))

    def close(self):
        try:
            self.ws.close()
        except Exception:  # noqa: BLE001
            pass
        self.proc.terminate()
        try:
            self.proc.wait(timeout=5)
        except Exception:  # noqa: BLE001
            self.proc.kill()


class OfficialTrackRenderer:
    """用官方系统页面渲染行程轨迹弹窗并截图"""

    def __init__(self, chrome: str, log_file: Path, wait_dialog_s: int = 60, wait_tiles_s: int = 12,
                 wait_login_s: int = 180, idle_quiet_s: float = 5.0, min_wait_s: float = 0.0,
                 pcd_wait_s: float = 6.0):
        self.chrome = chrome
        self.log_file = Path(log_file)
        self.wait_dialog_s = wait_dialog_s
        self.wait_tiles_s = wait_tiles_s
        self.wait_login_s = wait_login_s
        self.idle_quiet_s = idle_quiet_s
        self.min_wait_s = min_wait_s   # 轨迹弹窗出现后的固定等待下限（秒），到点才启动静默检测
        self.pcd_wait_s = pcd_wait_s   # 派车单打印弹窗 Section0 出现后的固定等待（秒），实测 6s 足够
        self.browser = None

    INJECT_AUTH_JS = """
(function(token, refresh, loginUser){
  try {
    if (typeof cube === 'undefined') return 'ERR:无cube';
    cube.token = token;
    if (refresh) cube.refreshToken = refresh;
    cube.loginUser = loginUser;
    return 'ok';
  } catch(e) { return 'ERR:' + (e && (e.message || e)); }
})(%s, %s, %s)
"""

    def start(self, auth):
        """auth 为 vehicle_export.Auth（兼容直接传 token 字符串）。
        流程：等 cube 框架加载 → 优先等页面自己走完 #home?T= 自动登录（页面完整初始化
        依赖它：弹窗消息包/地图 AccessToken 都是登录后才就位——凭证注入跳步曾致打印弹窗
        CUBE.self.msg 未初始化、轨迹地图空白）→ 超时再注入凭证兜底 → 失败给全量诊断。"""
        token = getattr(auth, 'token', auth)
        refresh = getattr(auth, 'refresh_token', '') or ''
        login_user = getattr(auth, 'login_user', None) or {}
        self.browser = CdpBrowser(self.chrome, log_file=self.log_file)
        self.browser.navigate(INDEX_URL + "#home?T=" + token)

        # 1) 等 cube 框架本身加载
        ok = self.browser.wait_expr("typeof cube!=='undefined'", timeout=60)
        if not ok:
            self._dump_failure("cube 框架未加载")
            raise RuntimeError("cube 框架未加载（页面未打开成功），诊断见 %s" % self._diag_path())

        # 2) 等页面自动登录完成（完整初始化；每 15s 打一次页面环境便于定位卡点）
        deadline = time.time() + self.wait_login_s
        last_log = 0.0
        while time.time() < deadline:
            if self.browser.eval("typeof cube!=='undefined' && !!cube.loginUser && !!cube.token") is True:
                print("官方页面渲染器：页面自动登录完成（完整初始化）")
                return
            if time.time() - last_log >= 15:
                last_log = time.time()
                print("  [渲染器] 等待页面自动登录… 环境 %s" % self.browser.eval(self.PROBE_JS))
            time.sleep(1.0)

        # 3) 兜底：注入凭证（页面初始化可能不完整，弹窗/地图渲染可能异常）
        print("  [渲染器] 页面自动登录 %ds 未完成，改为注入凭证兜底" % self.wait_login_s)
        r = self.browser.eval(self.INJECT_AUTH_JS % (
            json.dumps(token), json.dumps(refresh),
            json.dumps(login_user, ensure_ascii=False)), timeout=30)
        if r != "ok":
            self._dump_failure("凭证注入返回异常")
            raise RuntimeError("凭证注入失败: %s" % r)

        ready = self.browser.wait_expr("!!cube.token && !!cube.loginUser", timeout=30)
        if not ready:
            self._dump_failure("凭证注入后仍未就绪")
            raise RuntimeError("官方系统页面登录态注入后仍未就绪，诊断见 %s" % self._diag_path())

    def _diag_path(self) -> Path:
        return self.log_file.parent / "official_login_diag.png"

    def _dump_failure(self, why: str):
        """登录超时时的现场诊断：cube/token/loginUser 各自状态 + 整页截图"""
        try:
            info = {
                "why": why,
                "typeof_cube": self.browser.eval("typeof cube"),
                "has_token": self.browser.eval("typeof cube!=='undefined' ? !!cube.token : null"),
                "has_loginUser": self.browser.eval("typeof cube!=='undefined' ? !!cube.loginUser : null"),
                "readyState": self.browser.eval("document.readyState"),
                "title": self.browser.eval("document.title"),
                "url": (self.browser.eval("location.href") or "")[:160],
            }
            print("  [渲染器诊断] %s" % json.dumps(info, ensure_ascii=False))
        except Exception:  # noqa: BLE001
            pass
        try:
            self.browser.call("Page.captureScreenshot", {"format": "png"})
            r = self.browser.call("Page.captureScreenshot", {"format": "png"})
            data = (r.get("result") or {}).get("data")
            if data:
                self._diag_path().write_bytes(base64.b64decode(data))
        except Exception:  # noqa: BLE001
            pass

    OPEN_DIALOG_JS = """
(function(templateName, params){
  try {
    cube.showDialog({
      isShowDialog: true, title: "行程轨迹", eleId: "trackPicture",
      submitFormOnConfirm: true,
      templateOptions: { name: templateName, params: params },
      width: (window.innerWidth-100)+"px",
      height: (window.innerHeight-100)+"px",
      top: "20px", bottom: "20px",
      isShowMaxBtn: false, isShowMinBtn: false,
      isShowConfirmBtn: false, isShowCloseBtn: false
    });
    return "ok";
  } catch(e) { return "ERR:" + (e && (e.message || e)); }
})(%s, %s)
"""

    # 官方两种轨迹弹窗：行程详情（routeId，字段最全，理想样式）、按派车单（runData）
    TEMPLATE_ROUTE = "factoryLayout.monitor.realMonitor.track_picture"
    TEMPLATE_PCD = "factoryLayout.monitor.trackQuery.pcd_track_picture"
    # 官方派车单批量打印弹窗
    TEMPLATE_PCD_PRINT = "factoryLayout.running.pcdmanager.sx.sxpcdlistprint"

    OPEN_PCD_PRINT_JS = """
(function(items){
  try {
    cube.showDialog({
      isShowDialog: true, title: "派车单批量打印",
      submitFormOnConfirm: false, isClickEnterCloseDialog: false,
      templateOptions: {
        name: 'factoryLayout.running.pcdmanager.sx.sxpcdlistprint',
        type: "viewModel",
        params: { data: items, print: true }
      },
      width: (window.innerWidth-100)+"px",
      isWidthHeightAuto: true,
      isShowConfirmBtn: false, isShowMaxBtn: false, isShowMinBtn: false
    });
    return "ok";
  } catch(e) { return "ERR:" + (e && (e.message || e)); }
})(%s)
"""

    EXTRACT_PRINT_DOM_JS = """
(function(){
  var area = document.getElementById('sxpcdmanageprint-area');
  if (!area) return null;
  var head = Array.from(document.head.querySelectorAll('style,link[rel=stylesheet]'))
                  .map(function(e){ return e.outerHTML; }).join('');
  return JSON.stringify({head: head, body: area.outerHTML});
})()
"""

    PROBE_JS = """
(function(){
  var o = {cube: typeof cube, ko: typeof ko, require_: typeof require,
           win_require: typeof window.require, readyState: document.readyState,
           crypto_random: (window.crypto ? typeof window.crypto.random : 'no-crypto'),
           has_token: (typeof cube!=='undefined' ? !!cube.token : null),
           has_loginUser: (typeof cube!=='undefined' ? !!cube.loginUser : null)};
  try {
    if (typeof CUBE !== 'undefined' && CUBE.self) o.cube_msg = typeof CUBE.self.msg;
    if (typeof ko !== 'undefined' && ko.components)
      o.print_tpl_registered = ko.components.isRegistered(
        'factoryLayout.running.pcdmanager.sx.sxpcdlistprint');
  } catch(e) { o.probe_err = String(e); }
  return JSON.stringify(o);
})()
"""

    STYLES_READY_JS = """
(function(){
  if (document.readyState !== 'complete') return false;
  var links = document.querySelectorAll('link[rel=stylesheet]');
  for (var i = 0; i < links.length; i++) {
    try { if (!links[i].sheet) return false; } catch (e) {}
  }
  return true;
})()
"""

    def _tab_wait_expr(self, tab, expr, timeout=30, interval=0.3):
        """新标签页（独立 ws）里的条件等待，替代固定 sleep"""
        deadline = time.time() + timeout
        while time.time() < deadline:
            r = tab["call"]("Runtime.evaluate",
                            {"expression": expr, "returnByValue": True})
            if (((r.get("result") or {}).get("result") or {}).get("value")) is True:
                return True
            time.sleep(interval)
        return False

    def render_pcd_pdf_one(self, item: dict, out_pdf: Path, tag: str = "",
                           diag_dir: Path = None):
        """逐单渲染（按需求不批量）：每单独立开官方打印弹窗，失败隔离到单。
        超时/失败自动落诊断包（console 日志+整页截图）到 diag_dir（默认随 PDF）。"""
        diag_dir = diag_dir or out_pdf.parent
        # 打印模板经 ko component 加载：已注册则直用，未注册走 requirejs 兜底——
        # 企业浏览器上该兜底曾报 "(require || window.require) is not a function"，
        # 先等"模板已注册或加载器就绪"再开弹窗，并探测页面环境写入日志
        tpl_ready = self.browser.wait_expr(
            "(typeof ko!=='undefined' && ko.components && "
            "ko.components.isRegistered('factoryLayout.running.pcdmanager.sx.sxpcdlistprint'))"
            " || typeof window.require==='function' || typeof require==='function'",
            timeout=60)
        print("  [渲染诊断] 打印组件/加载器就绪=%s；页面环境 %s"
              % (tpl_ready, self.browser.eval(self.PROBE_JS)))
        r = self.browser.eval(self.OPEN_PCD_PRINT_JS % json.dumps([item], ensure_ascii=False),
                              timeout=60)
        if r != "ok":
            time.sleep(3)   # 加载竞态兜底：重试一次
            r = self.browser.eval(self.OPEN_PCD_PRINT_JS % json.dumps([item], ensure_ascii=False),
                                  timeout=60)
        if r != "ok":
            self.browser.dump_diag(diag_dir, tag or "openfail")
            raise RuntimeError("打开官方派车单打印弹窗失败: %s" % r)
        ok = self.browser.wait_expr(
            "document.querySelectorAll('#sxpcdmanageprint-area .Section0').length >= 1",
            timeout=120)
        if not ok:
            state = self.browser.eval(
                "(function(){return JSON.stringify({"
                "modal: document.querySelectorAll('.cube.modal.fade').length,"
                "area: !!document.getElementById('sxpcdmanageprint-area'),"
                "s0: document.querySelectorAll('#sxpcdmanageprint-area .Section0').length})})()")
            print("  [渲染诊断] 弹窗状态 %s" % state)
            self.browser.dump_diag(diag_dir, tag or "section0")
            self.browser.eval(self.CLOSE_DIALOG_JS)
            raise RuntimeError("派车单打印区渲染超时（Section0 未出）")
        # 打印区数据填充/样式就位的固定等待：实测 6s 足够（轨迹弹窗仍需 30s，见 min_wait_s；
        # config.ini [export] pcd_render_wait 可调/置 0 关闭）
        if self.pcd_wait_s > 0:
            time.sleep(self.pcd_wait_s)
        dom = self.browser.eval(self.EXTRACT_PRINT_DOM_JS)
        if not dom:
            self.browser.dump_diag(diag_dir, tag or "extract")
            raise RuntimeError("派车单打印区提取失败")
        dom = json.loads(dom)
        full_html = ("<!DOCTYPE html><html><head><meta charset='utf-8'>"
                     + dom["head"] + "</head><body>" + dom["body"] + "</body></html>")
        tab = self.browser.new_tab()
        try:
            tab["call"]("Runtime.evaluate",
                        {"expression": "document.write(%s); document.close(); 'ok'"
                                       % json.dumps(full_html), "returnByValue": True})
            # 样式表/文档就绪检测，替代固定 sleep(2.0)；上限与派车单固定等待同档（6s 封顶）
            self._tab_wait_expr(tab, self.STYLES_READY_JS, timeout=max(1, int(self.pcd_wait_s)))
            r = tab["call"]("Page.printToPDF",
                            {"preferCSSPageSize": True, "printBackground": True})
            data = (r.get("result") or {}).get("data")
            if not data:
                raise RuntimeError("printToPDF 无数据: %s" % str(r)[:200])
            out_pdf.write_bytes(base64.b64decode(data))
        finally:
            self.browser.close_tab(tab)
        self.browser.eval(self.CLOSE_DIALOG_JS)
        time.sleep(0.5)

    RECT_JS = """
(function(){
  var el = document.getElementById('trackPictureBox');
  if (!el) el = document.querySelector('.cube.modal.fade .modal-body');
  if (!el) return null;
  return JSON.stringify(el.getBoundingClientRect());
})()
"""

    CLOSE_DIALOG_JS = """
(function(){
  var closed = 0;
  document.querySelectorAll('.cube.modal.fade .close').forEach(function(b){
    b.dispatchEvent(new MouseEvent('mousedown', {bubbles:true})); closed++;
  });
  if (!closed) document.querySelectorAll('.cube.modal.fade').forEach(function(e){ e.remove(); });
  document.querySelectorAll('.modal-backdrop, .cube-modal-backdrop').forEach(function(e){ e.remove(); });
  return closed;
})()
"""

    def _render(self, template_name: str, params, out_png: Path, pre_shot_js: str = ""):
        r = self.browser.eval(self.OPEN_DIALOG_JS % (json.dumps(template_name),
                                                     json.dumps(params, ensure_ascii=False)),
                              timeout=60)
        if r != "ok":
            raise RuntimeError("打开官方轨迹弹窗失败: %s" % r)
        if not self.browser.wait_expr("!!document.querySelector('.cube.modal.fade .modal-body *')",
                                      timeout=self.wait_dialog_s):
            raise RuntimeError("轨迹弹窗未出现")
        # 等地图 canvas 出现（无轨迹点时可能一直没有，容忍）
        self.browser.wait_expr("!!document.querySelector('.cube.modal.fade canvas')",
                               timeout=min(40, self.wait_dialog_s))
        # 固定等待下限：实测多数轨迹完整渲染需 30s+，下限之内不启动静默检测（防截早；
        # config.ini [export] map_min_wait 可调/置 0 关闭）
        if self.min_wait_s > 0:
            time.sleep(self.min_wait_s)
        # 瓦片/轨迹渲染完成检测：盯地图域名在途请求，静默即完成；须先见到本弹窗自己的
        # 瓦片请求（min_seen=2，防"还没开始加载就误判静默"——实测裸 3s 静默截早了几条）；
        # wait_tiles_s 为兜底上限，idle_quiet_s 可调（config.ini [export] map_idle_quiet）
        t0 = time.time()
        idle_ok = self.browser.wait_network_idle(
            quiet_s=self.idle_quiet_s, timeout=max(15, self.wait_tiles_s),
            url_substr="map.sgcc.com.cn", min_seen=2)
        print("  [渲染] 地图加载检测：%s（%.1fs）" % ("静默完成" if idle_ok else "兜底超时",
                                                       time.time() - t0))
        time.sleep(1.0)   # 静默后给最后一帧渲染留一瞬
        # 「自动校准行程结束时间」：截图前注入改写 JS（build_popup_rewrite_js 生成），
        # 命中数随日志透出；未命中不视为失败（保留官方原始时间，页面结构变化时降级安全）
        rewrite_hits = None
        if pre_shot_js:
            try:
                raw = self.browser.eval(pre_shot_js, timeout=30)
                info = json.loads(raw) if raw and not str(raw).startswith("ERR") else {}
                rewrite_hits = info.get("replaced", 0)
            except Exception:  # noqa: BLE001
                rewrite_hits = 0
            print("  [校准] 弹窗时间改写命中 %d 处%s"
                  % (rewrite_hits, "" if rewrite_hits else "（保留官方原始时间）"))
        rect = self.browser.eval(self.RECT_JS)
        if not rect:
            raise RuntimeError("弹窗内容区未找到")
        box = json.loads(rect)
        clip = {"x": max(0, box["x"]), "y": max(0, box["y"]),
                "width": box["width"], "height": box["height"], "scale": 1}
        self.browser.screenshot_clip(clip, out_png)
        # 空白告警：内容弹窗截图通常 ≥30KB，过小多半没渲完就拍了
        try:
            size = out_png.stat().st_size
            if size < 30 * 1024:
                print("  [渲染告警] 轨迹截图仅 %.1f KB，疑似空白（企业浏览器偏慢，"
                      "可调大 config.ini [export] map_tiles_wait 后重试）" % (size / 1024))
                self.browser.dump_diag(out_png.parent, "blank_" + out_png.stem[:24])
        except OSError:
            pass
        self.browser.eval(self.CLOSE_DIALOG_JS)
        time.sleep(0.5)
        return rewrite_hits   # 未启用校准时为 None；启用时为命中处数（0 = 未命中）

    def render_route_png(self, route_id: str, out_png: Path, pre_shot_js: str = ""):
        """行程详情弹窗（理想样式：行程编号/里程/时速/绑定的派车单等）。
        pre_shot_js：截图前注入执行的改写脚本（自动校准行程结束时间用，空串不注入）"""
        return self._render(self.TEMPLATE_ROUTE, {"routeId": route_id}, out_png, pre_shot_js)

    def render_pcd_png(self, order: dict, out_png: Path, pre_shot_js: str = ""):
        """按派车单弹窗（找不到行程段时的回退）；pre_shot_js 口径同 render_route_png"""
        return self._render(self.TEMPLATE_PCD, {"runData": order}, out_png, pre_shot_js)

    def stop(self):
        if self.browser:
            self.browser.close()
            self.browser = None


PNG_WRAP_HTML = """<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><style>
@page { size: A4 landscape; margin: 5mm 5mm 5mm 20mm; }
body { margin: 0; -webkit-print-color-adjust: exact; }
img { display: block; width: 272mm; height: 200mm; }
</style></head><body><img src="$img_uri"></body></html>"""

# 派车单：A4 纵向，页边距与官方打印一致（上/下 10mm，左/右 31.5mm），
# 图片宽 147mm（官方 shrink-to-fit 0.866 的等效版心）
PNG_WRAP_PCD_HTML = """<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><style>
@page { size: A4 portrait; margin: 10mm 31.5mm 10mm 31.5mm; }
body { margin: 0; -webkit-print-color-adjust: exact; }
img { display: block; width: 147mm; margin: 0 auto; }
</style></head><body><img src="$img_uri"></body></html>"""


# ---------------------------------------------------------------- 弹窗时间改写（自动校准行程结束时间用）
# 已在 track_time_rewrite_trial.py 经内网实测验证（2026-10：结束时间/行驶时间精确命中改写）。
# 匹配按「值」而非死字符串：兼容 &nbsp;、- 与 / 分隔、有无秒、文本节点拆分；
# 时长兼容「X小时X分钟(X秒)」与小数小时（0.63 小时，按原小数位写回），±90s 容差吸收页面取整。
# 占位符 __OLDC__/__NEWC__（[Y,M,D,h,m,s] JSON 数组）与 __OLDDUR__/__NEWDUR__（秒）由
# build_popup_rewrite_js 注入（不用 % 格式化：JS 里有取模运算，百分号转义易错）。
POPUP_REWRITE_JS = r"""
(function(){
  var root = document.getElementById('trackPictureBox');
  if (!root) { var m = document.querySelectorAll('.cube.modal.fade'); root = m.length ? m[m.length-1] : null; }
  if (!root) return 'ERR:无弹窗';
  var oldC = __OLDC__, newC = __NEWC__, oldDur = __OLDDUR__, newDur = __NEWDUR__;
  var timeRe = /(\d{4})([-\/])(\d{1,2})\2(\d{1,2})([\s\u00a0T]+)(\d{1,2}):(\d{2})(?::(\d{2}))?/g;
  var durRe = /(\d+)\s*小时\s*(\d+)\s*分钟\s*(?:(\d+)\s*秒)?|(\d+)\s*分钟\s*(?:(\d+)\s*秒)?|(\d+(?:\.\d+)?)(\s*)小时/g;
  function pad(v, w){ v = String(v); while (v.length < w) v = '0' + v; return v; }
  function eqTime(y, mo, d, h, mi, s, hasSec){
    if (y !== oldC[0] || mo !== oldC[1] || d !== oldC[2]) return false;
    if (h !== oldC[3] || mi !== oldC[4]) return false;
    return hasSec ? (s === oldC[5]) : true;   // 页面不显示秒时按分精度判定
  }
  function fmtDur(sec, withSec){
    var h = Math.floor(sec / 3600), m, tail = '';
    if (withSec) { m = Math.floor((sec % 3600) / 60); tail = (sec % 60) + '秒'; }
    else { m = Math.round((sec % 3600) / 60); if (m === 60) { h += 1; m = 0; } }
    return (h > 0 ? (h + '小时' + m + '分钟') : (m + '分钟')) + tail;
  }
  var total = 0, details = [], seen = [];
  function procText(s, kind){
    var out = s.replace(timeRe, function(m, y, sep, mo, d, gap, h, mi, ss){
      if (seen.length < 50 && seen.indexOf(m) < 0) seen.push(m);
      if (!eqTime(+y, +mo, +d, +h, +mi, +(ss || 0), ss !== undefined)) return m;
      var r = pad(newC[0], y.length) + sep + pad(newC[1], mo.length) + sep
            + pad(newC[2], d.length) + gap + pad(newC[3], h.length) + ':' + pad(newC[4], mi.length)
            + (ss !== undefined ? ':' + pad(newC[5], ss.length) : '');
      total++;
      if (details.length < 20) details.push({kind: kind, before: m, after: r});
      return r;
    });
    out = out.replace(durRe, function(m, h1, m1, s1, m2, s2, hd, gap){
      if (seen.length < 50 && seen.indexOf(m) < 0) seen.push(m);
      var sec, r;
      if (hd !== undefined) {
        // 小数小时形态（如「0.63 小时」）：按原小数位数与原有间隔写回，容差 = 半位精度 + 30s
        var dec = (hd.split('.')[1] || '').length;
        sec = Math.round(parseFloat(hd) * 3600);
        if (Math.abs(sec - oldDur) > 0.5 * Math.pow(10, -dec) * 3600 + 30) return m;
        r = (newDur / 3600).toFixed(dec) + gap + '小时';
      } else {
        sec = h1 !== undefined
          ? (+h1) * 3600 + (+m1) * 60 + (+(s1 || 0))
          : (+m2) * 60 + (+(s2 || 0));
        if (Math.abs(sec - oldDur) > 90) return m;
        r = fmtDur(newDur, (s1 !== undefined || s2 !== undefined));
      }
      total++;
      if (details.length < 20) details.push({kind: kind + ':dur', before: m, after: r});
      return r;
    });
    return out;
  }
  // 叶子元素（无子元素）：整段 textContent 处理，兼容时间被拆成多个文本节点的情况
  root.querySelectorAll('*').forEach(function(el){
    if (el.children.length === 0 && el.tagName !== 'SCRIPT' && el.tagName !== 'STYLE') {
      var s = el.textContent;
      timeRe.lastIndex = 0; durRe.lastIndex = 0;
      if (s && (timeRe.test(s) || durRe.test(s))) {
        timeRe.lastIndex = 0; durRe.lastIndex = 0;
        var out = procText(s, 'text');
        if (out !== s) el.textContent = out;
      }
    }
  });
  root.querySelectorAll('input,textarea').forEach(function(el){
    var v = el.value || '';
    timeRe.lastIndex = 0; durRe.lastIndex = 0;
    if (v && (timeRe.test(v) || durRe.test(v))) {
      timeRe.lastIndex = 0; durRe.lastIndex = 0;
      var out = procText(v, 'input');
      if (out !== v) el.value = out;
    }
  });
  return JSON.stringify({replaced: total, details: details, seen: seen});
})()
"""


def build_popup_rewrite_js(old_dt: "object", new_dt: "object",
                           old_dur_s: int, new_dur_s: int) -> str:
    """生成弹窗时间改写 JS。old_dt/new_dt 为 datetime；时长为秒（行程开始→结束）。"""
    def c(dt):
        return [dt.year, dt.month, dt.day, dt.hour, dt.minute, dt.second]
    return (POPUP_REWRITE_JS
            .replace("__OLDC__", json.dumps(c(old_dt)))
            .replace("__NEWC__", json.dumps(c(new_dt)))
            .replace("__OLDDUR__", str(int(old_dur_s)))
            .replace("__NEWDUR__", str(int(new_dur_s))))
