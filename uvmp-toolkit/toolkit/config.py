# -*- coding: utf-8 -*-
"""配置加载与写回。

优先级（低到高）：内置默认值 < config.ini（只读引用，保持 CLI 版行为） < toolkit/config.json（GUI 可写）。
- config.json 路径可用环境变量 UVMP_TOOLKIT_CONFIG 覆盖（桌面用户定时器经 Environment 指定同一份配置）
- config.json 存放可调项与 SSO 凭据，写入后置 600 权限；不入仓（模板见 config.example.json）
- config.ini 仍是导出底层参数（网关/筛选/render/map）的来源，本模块只读不写
"""

import configparser
import copy
import json
import os
import shutil
import sys
from pathlib import Path

TOOLKIT_DIR = Path(__file__).resolve().parent
PROJECT_DIR = TOOLKIT_DIR.parent

DEFAULTS = {
    "sso": {"username": "", "password": ""},  # 留空则回退读 config.ini [auth]
    "ui": {
        "close_to_tray": True,  # 关窗最小化到托盘（后台任务仍可继续）
        "autostart": False,
    },  # 开机自启（建议开启；U 盘 agent 由服务/计划任务常驻）
    "usb": {
        "label": "GLKVM",
        "fallback_dir": "",
        # usb-agent 挂载/轮询参数：轮询只做发现/消费，不触发 udisks；挂载失败按
        # 30s→300s 退避重试，挂载成功或挂载点被服务端取件断开时复位。
        # 轮询取 15s 是为了缩短“服务端重新共享后多久挂上”的延迟（KVM 的共享切换
        # 不保证 udev 事件；60s 轮询会白吃掉单次 10 分钟等待窗口里的第一分钟）。
        "agent": {
            "poll_sec": 15,
            "mounted_poll_sec": 15,
            "debounce_sec": 4,
            "auto_mount_grace_sec": 10,
            "result_retention_days": 7,
            "log_retention_days": 30,
        },
    },  # fallback_dir 为空时找不到 U 盘即失败
    "cluster": {
        # 节点集群 Phase 1：身份/入网审批/心跳；指令转发与更新分发后续接入。
        # role=hub 负责局域网协调，GLKVM 可选；role=worker 经任一 Hub 转发，
        # 无本班组 Hub 时可填其他班组的 Hub 地址（同一 cluster_id/secret）。
        "enabled": False,
        "role": "worker",
        "cluster_id": "",
        "node_name": "",
        "team_id": 0,
        "team_name": "",
        "listen_host": "0.0.0.0",
        "listen_port": 17890,
        "advertise_host": "",
        "secret": "",
        "hubs": [],
        "hub_peers": [],
        "require_approval": True,
        "heartbeat_sec": 10,
        "offline_sec": 30,
        "request_timeout_sec": 5,
        "export": {  # 集群导出分片：每个分片的单号数 / 重试次数 / 单分片硬超时
            "chunk_size": 6,
            "max_attempts": 2,
            "request_timeout_sec": 1800,
        },
    },
    "daily_export": {"temp_dir": ""},  # 空=~/临时下载（保持旧脚本习惯）
    "tree_sync": {  # 树障统计表同步（服务端 U 盘指令 snapshot/replace）
        "local_path": "",  # 内网树障统计表 xlsx 完整路径（必配）
        "file_name": "检修一班树木统计表.xlsx",  # snapshot/本地备份上盘文件名
    },
    "crossing_sync": {  # 交跨统计表同步（服务端 U 盘指令 snapshot/replace）
        "local_path": "",  # 内网交跨统计表 xlsx 完整路径（必配）
        "file_name": "检修一班交跨统计表.xlsx",  # snapshot/本地备份上盘文件名
    },
    "order_export": {
        "default_outdir": "",  # 空=项目目录/output
        "lookback_days": 62,  # 无日期提示时按单号兜底查询的回看天数
        "batch_subdir": True,  # 在输出目录下建批次子目录
        "query_workers": 8,  # Python 导出引擎查询并发（钳 1..16）
        "render_workers": 4,  # Python 导出引擎渲染并发（钳 1..16，每路独占一个浏览器）
    },
    "dev": {"mock": False},  # 开发用：假数据替代真实接口
    "render": {
        "chrome": "",  # 浏览器路径（用户手选的公司浏览器；空=自动探测）
        "builtin": True,  # 内置浏览器优先（安装包自带，生产不依赖宿主 Chrome）
        "ignore_certificate_errors": False,  # 忽略 TLS 证书错误（内网自签证书临时排障用）
        "certificate_spki_pins": [],  # 证书 SPKI 固定清单（sha256/SPKI-base64）
    },
    "browser_automation": {
        "cdp_url": "http://127.0.0.1:9222",  # 仅允许本机回环地址
        "profile_dir": "",  # 空=用户状态目录/browser-automation/profile
        "start_url": "",  # 打开受控浏览器后自动访问；空=空白页
        "step_timeout": 120,
    },
    "course_autoplay": {"max_attempts": 3},  # 达到单课尝试上限后跳过并继续
    "defect_review": {
        "start_url": "",
        "outdir": "",
        "skip_tasks_file": "",
        "task_status": "待人工审核",
        "mission_type": "仿线巡检",
        "voltage_level": "交流110kV",
        "line_name": "",
        "start_date": "",
        "end_date": "",
        "step_timeout": 120,
        "action_timeout": 5,
        "navigation_timeout": 15,
        "login_result_timeout": 20,
        "captcha_ocr_timeout": 30,
        "image_timeout": 10,
        "image_retries": 0,
        "ignore_defect_boxes": False,
        "keep_awake": False,
        "username": "",
        "password": "",
        "reload_after_tasks": 10,
        "reload_after_images": 200,
        "stall_timeout": 120,
        "slow_image_seconds": 15,
        "slow_image_count": 3,
        "slow_restart_unreviewed": 50,
    },
}


def config_path() -> Path:
    """config.json 路径解析（优先级从高到低）：
    1. 环境变量 UVMP_TOOLKIT_CONFIG（桌面用户定时器经 unit 的 Environment 指定）
    2. 冻结版：用户配置目录（linux ~/.config/uvmp-toolkit/，Windows %APPDATA%\\uvmp-toolkit\\）
       ——安装包升级会清空安装目录（NSIS 卸载器 RMDir /r $INSTDIR，已踩坑），
       exe 旁的 config.json 只作旧版遗留处理：读到即迁移到用户目录
    3. 源码运行：toolkit/config.json
    """
    env = os.environ.get("UVMP_TOOLKIT_CONFIG")
    if env:
        return Path(env)
    if getattr(sys, "frozen", False):
        if sys.platform.startswith("win"):
            base = Path(
                os.environ.get("APPDATA") or (Path.home() / "AppData" / "Roaming")
            )
        else:
            base = Path(os.environ.get("XDG_CONFIG_HOME") or (Path.home() / ".config"))
        user_cfg = base / "uvmp-toolkit" / "config.json"
        if user_cfg.exists():
            return user_cfg
        beside = Path(sys.executable).resolve().parent / "config.json"
        if beside.exists():
            try:
                user_cfg.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(str(beside), str(user_cfg))
                try:
                    os.chmod(user_cfg, 0o600)
                except OSError:
                    pass
            except OSError:
                pass
        return user_cfg
    return TOOLKIT_DIR / "config.json"


def _ini_path() -> Path:
    """config.ini 路径：冻结版优先读 exe 旁的（用户可改），否则用内置默认"""
    if getattr(sys, "frozen", False):
        beside = Path(sys.executable).resolve().parent / "config.ini"
        if beside.exists():
            return beside
    return PROJECT_DIR / "config.ini"


def deep_merge(base: dict, over: dict) -> dict:
    out = dict(base)
    for k, v in (over or {}).items():
        if isinstance(v, dict) and isinstance(out.get(k), dict):
            out[k] = deep_merge(out[k], v)
        else:
            out[k] = v
    return out


def _read_ini() -> configparser.ConfigParser:
    cp = configparser.ConfigParser()
    ini = _ini_path()
    if ini.exists():
        cp.read(ini, encoding="utf-8")
    return cp


def load() -> dict:
    """加载合并后的完整配置"""
    cfg = copy.deepcopy(DEFAULTS)
    cp = _read_ini()
    ini = {sec: dict(cp.items(sec)) for sec in cp.sections()}
    cfg["_ini"] = ini  # 原始 ini 段，供构造 vehicle_export 风格的 cfg 使用
    p = config_path()
    if p.exists():
        try:
            cfg = deep_merge(cfg, json.loads(p.read_text(encoding="utf-8")))
        except Exception as e:
            print("[config] config.json 解析失败（%s），使用默认值" % e)
    cfg["_config_path"] = str(p)
    return cfg


def save(updates: dict):
    """把 GUI 可写项合并写回 config.json（600 权限），返回写后的文件内容"""
    updates = copy.deepcopy(updates or {})
    cluster = updates.get("cluster")
    if isinstance(cluster, dict):
        # 脱敏视图里的 secret_set 不是配置项；空/掩码值表示“不修改”。
        cluster.pop("secret_set", None)
        secret = cluster.get("secret")
        if secret is None or str(secret) in ("", "******"):
            cluster.pop("secret", None)
    p = config_path()
    cur = {}
    if p.exists():
        try:
            cur = json.loads(p.read_text(encoding="utf-8"))
        except Exception:
            cur = {}
    cur = deep_merge(cur, updates)
    cur.pop("_ini", None)
    cur.pop("_config_path", None)
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(json.dumps(cur, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    try:
        os.chmod(p, 0o600)
    except OSError:
        pass  # Windows 无 Unix 权限位
    return cur


def masked_view(cfg: dict) -> dict:
    """供 API 返回的脱敏视图：密码不回传，只告知是否已配置"""
    view = {k: v for k, v in cfg.items() if not k.startswith("_")}
    view = copy.deepcopy(view)
    pw = (view.get("sso") or {}).get("password") or ""
    ini_pw = ((cfg.get("_ini") or {}).get("auth") or {}).get("password") or ""
    view.setdefault("sso", {})["password"] = ""
    view["sso"]["has_password"] = bool(pw or ini_pw)
    view["sso"]["password_source"] = (
        "config.json" if pw else ("config.ini" if ini_pw else "")
    )
    raw_cluster = cfg.get("cluster")
    cluster_secret = (
        raw_cluster.get("secret") if isinstance(raw_cluster, dict) else ""
    ) or ""
    cluster = view.get("cluster")
    if not isinstance(cluster, dict):
        cluster = {}
        view["cluster"] = cluster
    cluster["secret"] = ""
    cluster["secret_set"] = bool(cluster_secret)
    return view


def sso_credentials(cfg: dict):
    """有效 SSO 凭据：config.json 优先，config.ini [auth] 兜底。返回 (username, password)"""
    sso = cfg.get("sso") or {}
    ini_auth = (cfg.get("_ini") or {}).get("auth") or {}
    username = (sso.get("username") or "").strip() or (
        ini_auth.get("username") or ""
    ).strip()
    password = (sso.get("password") or "") or (ini_auth.get("password") or "")
    return username, password


def ve_cfg(cfg: dict) -> dict:
    """构造 vehicle_export.py 业务函数期望的 cfg 字典（与 ve.main 的解析口径一致）"""
    ini = cfg.get("_ini") or {}
    exp = dict(ini.get("export") or {})
    out = dict(exp)
    out.update(
        {
            "page_size": int(exp.get("page_size", 50)),
            "run_state": exp.get("run_state", "2"),
            "vehicle_state": exp.get("vehicle_state", "04"),
            "keep_tag": exp.get("keep_tag", "00,01"),
            "map_tiles_wait": exp.get("map_tiles_wait", "12000"),
            "map_idle_quiet": exp.get("map_idle_quiet", "5000"),
            "map_min_wait": exp.get("map_min_wait", "30000"),
            "pcd_render_wait": exp.get("pcd_render_wait", "6000"),
            "track_render": exp.get("track_render", "official"),
        }
    )
    return out


def gateway(cfg: dict) -> str:
    g = ((cfg.get("_ini") or {}).get("gateway") or {}).get("base", "").strip()
    if not g:
        import vehicle_export as ve

        g = ve.GATEWAY
    return g if g.endswith("/") else g + "/"


def render_cfg(cfg: dict) -> dict:
    """render 配置：config.ini [render] 为底，config.json render.chrome（用户手选）覆盖"""
    rc = dict((cfg.get("_ini") or {}).get("render") or {})
    chosen = ((cfg.get("render") or {}).get("chrome") or "").strip()
    if chosen:
        rc["chrome"] = chosen
    return rc


def sso_cfg(cfg: dict) -> dict:
    return dict((cfg.get("_ini") or {}).get("sso") or {})
