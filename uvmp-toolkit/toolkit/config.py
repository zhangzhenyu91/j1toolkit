# -*- coding: utf-8 -*-
"""配置加载与写回。

优先级（低到高）：内置默认值 < config.ini（只读引用，保持 CLI 版行为） < toolkit/config.json（GUI 可写）。
- config.json 路径可用环境变量 UVMP_TOOLKIT_CONFIG 覆盖（桌面用户定时器经 Environment 指定同一份配置）
- config.json 存放可调项与 SSO 凭据，写入后置 600 权限；不入仓（模板见 config.example.json）
- config.ini 仍是导出底层参数（网关/筛选/render/map）的来源，本模块只读不写
- 写回全程“读旧 → 校验 → 合并 → 唯一临时名 + fsync + 原子替换”，断电/并发都不会留半截配置；
  旧文件损坏时先另存备份再重建，读不出来则拒绝覆盖（不能让一次保存把用户配置清空）
"""

import configparser
import copy
import json
import os
import shutil
import sys
import tempfile
import time
from pathlib import Path

# 需要脱敏/空值不覆盖的字段名（密码、集群密钥、令牌）
SECRET_FIELDS = frozenset({"password", "secret", "token"})
# 前端回传的掩码值，语义等同“不修改”
MASKED_SECRET = "******"


class ConfigValidationError(ValueError):
    """更新项与内置默认值的类型不符（防止把字符串塞进 dict/bool 打坏配置）"""

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


def _merge_lists(cur: list, new: list) -> list:
    """列表合并：元素都是带 id 的字典时按 id 合并（保留未提交的密码等字段），
    其余情况整体替换——与历史行为一致，避免旧条目被意外保留。"""
    if not isinstance(cur, list) or not isinstance(new, list):
        return new
    if not cur or not new:
        return new
    if not all(isinstance(i, dict) and "id" in i for i in cur + new):
        return new
    old_by_id = {i["id"]: i for i in cur}
    merged = []
    for item in new:
        base = old_by_id.get(item["id"])
        merged.append(deep_merge(base, item) if isinstance(base, dict) else item)
    return merged


def deep_merge(base: dict, over: dict) -> dict:
    out = dict(base)
    for k, v in (over or {}).items():
        if isinstance(v, dict) and isinstance(out.get(k), dict):
            out[k] = deep_merge(out[k], v)
        elif isinstance(v, list) and isinstance(out.get(k), list):
            out[k] = _merge_lists(out[k], v)
        else:
            out[k] = v
    return out


def _is_secret_key(key: str) -> bool:
    text = str(key or "")
    return (
        text in SECRET_FIELDS
        or text.endswith("_password")
        or text.endswith("_secret")
        or text.endswith("_token")
    )


def _type_error(path: str, expected: str) -> ConfigValidationError:
    return ConfigValidationError("%s 类型应为%s" % (path or "<根>", expected))


def _validate_types(updates: dict, defaults: dict, prefix: str = ""):
    """递归类型校验：只校验内置默认值里出现的键，未知键（如 instances）放行。
    bool 与 int 不互换、数字不接受字符串，避免前端误传把配置写坏。"""
    for key, value in (updates or {}).items():
        path = "%s.%s" % (prefix, key) if prefix else str(key)
        if str(key).startswith("_"):
            continue  # 内部字段（_ini/_config_path）不校验，写回前统一剔除
        if key not in defaults:
            continue
        default = defaults[key]
        if isinstance(default, bool):
            if not isinstance(value, bool):
                raise _type_error(path, "布尔值")
        elif isinstance(default, int):
            if isinstance(value, bool) or not isinstance(value, int):
                raise _type_error(path, "整数")
        elif isinstance(default, str):
            if not isinstance(value, str):
                raise _type_error(path, "字符串")
        elif isinstance(default, dict):
            if not isinstance(value, dict):
                raise _type_error(path, "对象")
            _validate_types(value, default, path)
        elif isinstance(default, list):
            if not isinstance(value, list):
                raise _type_error(path, "数组")
            if not default and any(not isinstance(v, str) for v in value):
                raise _type_error(path, "字符串数组")
            elif default and any(type(v) is not type(default[0]) for v in value):
                raise _type_error(path, "与默认元素同类型的数组")


def _drop_unchanged_secrets(updates: dict) -> dict:
    """空值/掩码的密码字段表示“不修改”：直接从更新里摘掉，交由旧配置保留原值。"""
    if not isinstance(updates, dict):
        return updates
    out = {}
    for key, value in updates.items():
        if isinstance(value, dict):
            out[key] = _drop_unchanged_secrets(value)
        elif isinstance(value, list):
            out[key] = [
                _drop_unchanged_secrets(v) if isinstance(v, dict) else v
                for v in value
            ]
        elif _is_secret_key(key) and (
            value is None or str(value) in ("", MASKED_SECRET)
        ):
            continue
        else:
            out[key] = value
    return out


def _strip_derived_flags(updates: dict) -> dict:
    """脱敏视图派生的展示字段（has_password/password_source）不是配置项，
    前端把整份 cfg 回传时不能落盘。"""
    if not isinstance(updates, dict):
        return updates
    out = {}
    for key, value in updates.items():
        if key in ("has_password", "password_source"):
            continue
        if isinstance(value, dict):
            out[key] = _strip_derived_flags(value)
        elif isinstance(value, list):
            out[key] = [
                _strip_derived_flags(v) if isinstance(v, dict) else v for v in value
            ]
        else:
            out[key] = value
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
            # 走 stderr：rpc 入口的 stdout 是协议独占通道，一行噪音就会让对端解析失败
            print("[config] config.json 解析失败（%s），使用默认值" % e, file=sys.stderr)
    cfg["_config_path"] = str(p)
    return cfg


def _atomic_write_json(path: Path, data: dict):
    """唯一临时名 + fsync + 原子替换；临时文件与目标同目录（跨盘 replace 会失败）。
    config.json 落 600：内含 SSO/机巡凭据。"""
    path.parent.mkdir(parents=True, exist_ok=True)
    text = json.dumps(data, ensure_ascii=False, indent=2) + "\n"
    fd, tmp_name = tempfile.mkstemp(
        prefix=".%s-" % path.name, suffix=".tmp", dir=str(path.parent)
    )
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as fp:
            fp.write(text)
            fp.flush()
            os.fsync(fp.fileno())
        try:
            os.chmod(tmp_name, 0o600)
        except OSError:
            pass  # Windows 无 Unix 权限位
        os.replace(tmp_name, str(path))
    except BaseException:
        try:
            os.unlink(tmp_name)
        except OSError:
            pass
        raise


def _backup_corrupt(path: Path) -> Path:
    """损坏的 config.json 另存一份（保留原始字节，便于人工找回），返回备份路径"""
    stamp = time.strftime("%Y%m%d%H%M%S")
    target = path.with_name("%s.corrupt-%s" % (path.name, stamp))
    n = 1
    while target.exists():
        target = path.with_name("%s.corrupt-%s-%d" % (path.name, stamp, n))
        n += 1
    shutil.copy2(str(path), str(target))
    return target


def save(updates: dict):
    """把 GUI 可写项校验后合并写回 config.json（原子写 + 600 权限），返回写后内容。

    - 类型不符直接抛 ConfigValidationError（不落盘）
    - 空值/掩码密码不覆盖原值（含机巡与实例 params 里的密码）
    - 旧文件解析失败：先备份再重建；旧文件存在但读不出来：拒绝覆盖
    """
    updates = copy.deepcopy(updates or {})
    if not isinstance(updates, dict):
        raise ConfigValidationError("更新内容必须是对象")
    _validate_types(updates, DEFAULTS)
    if isinstance(updates.get("cluster"), dict):
        # 脱敏视图里的 secret_set 不是配置项；掩码/空值由 _drop_unchanged_secrets 处理
        updates["cluster"].pop("secret_set", None)
    updates = _drop_unchanged_secrets(updates)
    updates = _strip_derived_flags(updates)
    p = config_path()
    cur = {}
    if p.exists():
        try:
            cur = json.loads(p.read_text(encoding="utf-8"))
        except ValueError as e:  # JSON 损坏：备份后按空配置重建
            try:
                backup = _backup_corrupt(p)
                print(
                    "[config] config.json 已损坏（%s），备份到 %s" % (e, backup),
                    file=sys.stderr,
                )
            except OSError:
                pass
            cur = {}
        except OSError as e:  # 权限/占用：读不到旧值就不能写，否则等于清空配置
            raise RuntimeError("配置文件无法读取，已取消保存：%s" % e) from e
        if not isinstance(cur, dict):
            cur = {}
    cur = deep_merge(cur, updates)
    cur.pop("_ini", None)
    cur.pop("_config_path", None)
    _atomic_write_json(p, cur)
    return cur


def _mask_field(container: dict, key: str = "password") -> None:
    """把 container[key] 清空并置 has_<key>，只用于返回给前端"""
    if not isinstance(container, dict):
        return
    raw = container.get(key) or ""
    container[key] = ""
    container["has_" + key] = bool(raw)


def _secret_set(container, key: str = "password") -> bool:
    """容器里是否已存了真实密钥（掩码不算密钥，空值不算）"""
    if not isinstance(container, dict):
        return False
    value = container.get(key)
    return isinstance(value, str) and value not in ("", MASKED_SECRET)


def masked_view(cfg: dict) -> dict:
    """供 API 返回的脱敏视图：密码只告知是否已配置，绝不回传明文。
    覆盖 SSO、机巡审核（含多实例 params）与集群密钥。

    实例的 has_password 按运行期取值顺序回退：实例自身密码（params 优先，
    其次实例对象顶层）→ 应用级 defect_review.password。实例没填密码时任务实际
    会用应用级密码登录，视图若只报实例自身状态，前端会把可启动的实例判成缺密码。"""
    view = {k: v for k, v in cfg.items() if not k.startswith("_")}
    view = copy.deepcopy(view)
    pw = (view.get("sso") or {}).get("password") or ""
    ini_pw = ((cfg.get("_ini") or {}).get("auth") or {}).get("password") or ""
    view.setdefault("sso", {})["password"] = ""
    view["sso"]["has_password"] = bool(pw or ini_pw)
    view["sso"]["password_source"] = (
        "config.json" if pw else ("config.ini" if ini_pw else "")
    )
    review = view.get("defect_review")
    if isinstance(review, dict):
        # 机巡登录密码属于实例凭据：只回传是否已填
        raw_review = cfg.get("defect_review")
        app_level_pw = _secret_set(raw_review)
        raw_instances = (
            raw_review.get("instances") if isinstance(raw_review, dict) else None
        )
        raw_instances = raw_instances if isinstance(raw_instances, list) else []
        _mask_field(review)
        review["has_password"] = app_level_pw
        for index, instance in enumerate(review.get("instances") or []):
            if not isinstance(instance, dict):
                continue
            # view 是 cfg 的深拷贝，下标与原始实例一一对应
            raw_instance = (
                raw_instances[index] if index < len(raw_instances) else None
            )
            raw_params = (
                raw_instance.get("params") if isinstance(raw_instance, dict) else None
            )
            instance_pw = _secret_set(raw_params) or _secret_set(raw_instance)
            _mask_field(instance)
            instance["has_password"] = instance_pw or app_level_pw
            if isinstance(instance.get("params"), dict):
                _mask_field(instance["params"])
                instance["params"]["has_password"] = instance_pw or app_level_pw
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
