#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""缺陷识别任务自动审核试验脚本（客户端脚本测试可直接运行）。

依赖：Python 3.8+、Playwright（python3 -m pip install playwright）。
使用系统 Chrome/公司浏览器，无需执行 playwright install，也不会自动登录。

首次启动受控浏览器（只扫描）：
  python3 defect_review_trial.py --launch --start-url <系统网址> --scan-only
浏览器打开后，人工登录并停留在系统页面，再回到终端按 Enter。
后续在该浏览器中直接运行：
  python3 defect_review_trial.py --scan-only        # 只扫描候选
  python3 defect_review_trial.py                    # 逐条审核并提交

正式模式只审核图片分组里 auditStatus 为 unreviewed 的未打勾图片（按钮为
「审核完成」）；已打勾图片（auditStatus 非 unreviewed）按钮是「重新审核」，
脚本一律跳过，避免点到已审核图片卡住。缩略图按 fileGuid 精确定位并点击，
不按第几张/序号盲点。already_annotation 或 taggingDetailInfo 有数据都表示系统
已经画出缺陷框：未打勾的跳过并写入输出目录的「审核汇总.xlsx」之「缺陷」工作表，
供人工复核（列名使用系统的「线路杆塔」，并附带电压等级、线路名称、缺陷描述等）。
图片框的 picDefectFlg 可能仍是 unlabeled，不能只信这个字段。
图片加载以「审核完成」按钮亮起且可用为判定标准，按钮亮起立即点击，不再等下载响应
或画布像素。点击后系统会自动切到下一张未审核图片，脚本正常流程不点缩略图；
只有加载失败、按钮不可用、当前图片是已打勾/系统缺陷框时，才兜底切到下一张未打勾图片。
图片按钮等待默认 10 秒；单张失败不立即重试，先切到下一张未打勾图片。所有其他
未打勾图片处理完后，再给失败图片各 1 次补审机会，仍失败才记录到
「审核汇总.xlsx」之「异常」工作表并结束该任务，不会反复卡在同一张。
失败汇总表附带该任务当前未审核图片数量。任务可能已被别人审核过一部分，本次
成功数+失败数不要求等于总图片数；以界面「未审核 0」和「完成任务」按钮为准。
全部未打勾图片审核完后主动点「完成任务」再点「确认」。审核时以高亮缩略图对应的
图片为准，顶部文件名可能滞后；响应中的 picDetailGuid 决定实际记哪一张。加载失败
图片记录后不再重复点击，只留到末轮补审。
任务存在系统缺陷框或加载失败图片时，脚本审核其余未打勾图片后直接关闭审核弹窗，
不点「完成任务」、不发 submitAudit。没有这些情况的任务才提交并核对列表刷新。
图片已经显示但「审核完成」按钮额外等待 5 秒仍不可用时，不记图片加载失败，
改写「审核汇总.xlsx」之「异常」工作表，并关闭任务转人工。
新任务打开后若界面「未审核数」超过 --max-unreviewed（默认 400），直接跳过并记异常。
任务处理异常会刷新网页后重试同一任务一次;仍失败才跳过并继续,扫描异常刷新后继续,
不中止整轮;刷新失败每 60 秒重试,只有用户主动停止才退出。
进度条按任务总数统计,缺陷框/加载失败/阈值跳过均算完成;
完成任务会消失、后续页任务会前移(如第 3 页变第 2 页、第 4 页变第 3 页),
所以下一轮从上一任务所在页继续扫描;需要换页时直接跳转目标页码,
不逐页点「下一页」。
任务弹层以「任务名匹配当前任务且未审核文案已渲染」或「本任务缩略图已出现」为内容就绪；
顶部弹层加载蒙层（会包住首图遮罩或盖住「审核完成」按钮的那层）消失后才开始审核，
避免弹层还在加载就点首图。每次点击缩略图切换图片也会触发同一蒙层，脚本等它消失后
再识别切换后的图片或进入审核，避免把上一张图片当成刚点击的图片。首图自身的图片
加载遮罩不阻塞弹层就绪，单图加载仍按 --image-timeout 等待并在失败后补审/跳过。
弹层内容迟迟不渲染、关闭失败、写入结果不明等一律刷新网页回到任务列表继续，
失败每 60 秒重试，不中止整轮审核。
每次运行都会在输出目录写一份 run.log（与客户端任务日志内容一致，带日期时间）
和「跳过任务.txt」（仅缺陷框/加载失败/阈值跳过任务名，可直接作为下次的 --skip-tasks-file），
便于事后核对与续跑；汇总表与失败截图也在同一目录。
运行期间可请求宿主机关屏/休眠抑制（Windows：SetThreadExecutionState；Linux：
systemd-inhibit + xdg-screensaver/xset 定时重置），结束或停止后恢复。客户端
「机巡审核」启动卡片勾选「运行时防止关屏/休眠」后由客户端 Job 层启用；独立脚本
默认启用。系统不支持或设置失败时只记日志，不影响审核。
点停止后协作取消每 200ms 检查一次；浏览器点击/填表等动作本身最长等 5 秒，
不会因为一个卡住的动作让客户端一直停在“正在停止”。停止后不再关闭审核弹窗，
也不关闭受控浏览器。
任务名称跳过表默认读取当前目录的「跳过任务.txt」；也支持一列 xlsx/csv，首行标题，其后每行一个任务名称；
命中的任务直接跳过。本轮因图片加载失败或按钮未就绪转人工的任务，会自动追加到该表，
本次运行的后续扫描及下次运行都不再进入。
写请求结果不明或出现未知二次确认时停止运行，不自动重发、不盲点未知按钮。

客户端「开发者工具 → 脚本测试」可直接选择本文件并传参，例如：
  --task-status 待人工审核 --mission-type 仿线巡检 --voltage-level 交流220kV
  --line-name 吕汾 --start-date 2026-01-01 --end-date 2026-11-08 --launch
加 --launch 时脚本会启动受控浏览器，并自动等待人工登录（不需要终端按 Enter）；
登录后等待用户手动进入「智能分析 → 智能识别 → 缺陷识别任务」，再填筛选和审核。
脚本会自动读取客户端配置的浏览器路径和 CDP 地址。若 CDP 端口已有受控浏览器，
会直接复用；需要全新登录时先关闭它或换 --cdp-url。若已有受控浏览器且已登录，
可去掉 --launch 直接运行；加 --manual-task-page 也会等待手动进入任务列表。

脚本只连接本机 CDP 调试端口，不读取或保存账号密码，不导出 Cookie/token，
不直接 fetch 审核写接口，也不关闭用户浏览器。
"""

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import time
import traceback
import zipfile
import xml.etree.ElementTree as ET
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Callable, Iterable, Optional, Sequence, Tuple
from urllib import request as urllib_request
from urllib.parse import parse_qs, urlsplit

SCRIPT_DIR = Path(__file__).resolve().parent
# 脚本可单独运行，也可被客户端按文件加载；host_awake 在 toolkit/ 下。
_TOOLKIT_DIR = SCRIPT_DIR / "toolkit"
if _TOOLKIT_DIR.is_dir() and str(_TOOLKIT_DIR) not in sys.path:
    sys.path.insert(0, str(_TOOLKIT_DIR))
try:
    from host_awake import HostAwakeGuard
except ImportError:
    from toolkit.host_awake import HostAwakeGuard

TASK_PAGE_API = "/api/defect-server/code/recognitiontaskinfo/page"
LINE_CONDITION_API = "/api/basic-server/device/listByLineCondition"
PIC_GROUP_API = "/api/defect-server/code/picturedetail/getPicGroup"
PIC_DETAIL_API = "/api/defect-server/code/picturedetail/getPictureDetailList"
FILE_DOWNLOAD_API = "/api/allcore-file/publicFile/downLoadFile"
PICTURE_REVIEW_API = "/api/defect-server/code/recognitiontaskinfo/pictureReview"
SUBMIT_AUDIT_API = "/api/defect-server/code/recognitiontaskinfo/submitAudit"
TAGGING_DETAIL_API = "/api/defect-server/code/taggingDetailInfo/findByPictureDetailGuid"
DEFAULT_CDP_URL = "http://127.0.0.1:9222"
TASK_LIST_MENU_PATH = ("智能分析", "智能识别", "缺陷识别任务")
TASK_PAGE_PATH = "/intelligentAnalysis/identification/IdentifyTasks"
TASK_TABLE_NAME_HEADER = "任务名称"
TASK_ENTER_SELECTOR = '[data-pid="IdentifyTasks_enter"]'
QUERY_BUTTON_TEXT = "查询"
REVIEW_BUTTON_TEXT = "审核完成"
CONFIRM_BUTTON_TEXT = "确认"
FINAL_CONFIRM_PHRASES = (
    "审核完成",
    "完成审核",
    "提交审核",
    "确认提交",
    "任务完成",
    "完成任务",
    "确认完成任务",
)
FINAL_CONFIRM_NEGATIVE_WORDS = ("失败", "错误", "异常", "驳回", "终止")
DEFAULT_IMAGE_TIMEOUT = 10
DEFAULT_IMAGE_RETRIES = 0
DEFAULT_LOGIN_TIMEOUT = 300
DEFAULT_MAX_UNREVIEWED = 400
DEFAULT_UNKNOWN_TIMEOUT = 120
DEFAULT_TASK_RETRIES = 1
_CANCEL_WAIT_SLICE_MS = 200
# Playwright 点击/填表/悬停的等待上限。页面卡住时不能沿用 --timeout 的整步
# 超时（默认 120 秒），否则用户点停止后要等当前动作超时才响应。
_ACTION_TIMEOUT_MS = 5000
# 页面跳转/刷新的等待上限；正常内网 SPA 几秒内完成，卡住时不能拖住停止。
_NAVIGATION_TIMEOUT_MS = 15000
# 任何异常兜底：刷新网页回任务列表；多次刷新之间固定等待 60 秒。
_REFRESH_RETRY_SECONDS = 60

# 系统已识别的缺陷框：图片分组接口通过 picDefectFlg 标记，现场抓包值为
# already_annotation；这类图片必须交人工复核，不能由脚本点「审核完成」。
SYSTEM_DEFECT_FLAG = "already_annotation"
KNOWN_PIC_DEFECT_FLAGS = frozenset(("", "unlabeled", "no_defect", SYSTEM_DEFECT_FLAG))
SUMMARY_XLSX_NAME = "审核汇总.xlsx"
SKIP_TASKS_OUTPUT_NAME = "跳过任务.txt"
DEFECT_SHEET_NAME = "缺陷"
ANOMALY_SHEET_NAME = "异常"
SYSTEM_DEFECT_HEADERS = (
    "电压等级",
    "线路名称",
    "线路杆塔",
    "杆塔号",
    "任务名称",
    "工单编号",
    "图片名称",
    "拍摄时间",
    "缺陷描述",
    "缺陷等级",
    "缺陷坐标",
    "算法识别",
    "任务GUID",
    "图片GUID",
    "处理结果",
)
ANOMALY_HEADERS = (
    "时间",
    "异常类型",
    "异常原因",
    "任务名称",
    "任务GUID",
    "工单编号",
    "电压等级",
    "线路名称",
    "线路杆塔",
    "杆塔号",
    "图片名称",
    "拍摄时间",
    "图片GUID",
    "任务未审核数",
    "处理结果",
)
BUTTON_NOT_READY_GRACE_SECONDS = 5.0
DEFAULT_TAGGING_DETAIL_TIMEOUT = 2
TAGGING_DETAIL_UI_WAIT_MS = 100

TASK_STATUS_OPTIONS = (
    "图片未上传",
    "图片上传中",
    "算法识别中",
    "待人工审核",
    "任务完成",
    "任务已终止",
    "审核完成",
    "审核驳回",
    "工单终止",
    "图片转换中",
)
MISSION_TYPE_OPTIONS = (
    "线路交跨巡检",
    "精细化巡检",
    "应急巡检",
    "正射巡检",
    "红外巡检",
    "固定翼巡检",
    "通道巡检",
    "树障巡检",
    "特殊巡视",
    "仿线巡检",
    "联合巡视",
)
VOLTAGE_LEVEL_OPTIONS = (
    "交流35kV",
    "交流110kV",
    "交流220kV",
    "交流500kV",
    "交流1000kV",
    "±800kV地",
    "直流500kV",
    "直流660kV",
    "直流800kV",
    "直流1000kV",
)

DEFAULT_TASK_STATUS = "待人工审核"
DEFAULT_MISSION_TYPE = "精细化巡检"
DEFAULT_VOLTAGE_LEVEL = "交流220kV"
TASK_GUID_FIELD = "recognitionTaskGuid"
SCRIPT_VERSION = "2026-11-09.06"

TASK_STATUS_CODES = {
    "图片未上传": "picture_not_uploaded",
    "图片上传中": "picture_uploading",
    "算法识别中": "algorithm_recognition",
    "待人工审核": "manual_review",
    "任务完成": "task_completed",
    "任务已终止": "task_termination",
    "审核完成": "audit_completed",
    "审核驳回": "approval_rejection",
    "工单终止": "work_order_termination",
    "图片转换中": "picture_converted",
}
MISSION_TYPE_CODES = {
    "线路交跨巡检": "line_inspect",
    "精细化巡检": "fine_inspect",
    "应急巡检": "yj_inspect",
    "正射巡检": "dom_inspect",
    "红外巡检": "red_inspect",
    "固定翼巡检": "laser_inspect",
    "通道巡检": "way_inspect",
    "树障巡检": "tree_inspect",
    "特殊巡视": "special_inspect",
    "仿线巡检": "imitation_inspect",
    "联合巡视": "union_inspect",
}
VOLTAGE_LEVEL_CODES = {
    "交流35kV": "25",
    "交流110kV": "32",
    "交流220kV": "33",
    "交流500kV": "35",
    "交流1000kV": "37",
    "±800kV地": "50",
    "直流500kV": "83",
    "直流660kV": "84",
    "直流800kV": "85",
    "直流1000kV": "86",
}

_DATE_PATTERN = re.compile(r"^\d{4}-\d{2}-\d{2}$")
_URL_PATTERN = re.compile(r"https?://[^\s'\"<>]+", re.IGNORECASE)


def _normalize_text(value: object) -> str:
    return " ".join(str(value or "").split())


SKIP_TASK_HEADER = "任务名称"


_XLSX_NS = {"m": "http://schemas.openxmlformats.org/spreadsheetml/2006/main"}


def _xlsx_first_column(path) -> list:
    """读取 xlsx 第一列；首行标题由调用方忽略。"""
    try:
        with zipfile.ZipFile(path) as archive:
            names = archive.namelist()
            shared = []
            if "xl/sharedStrings.xml" in names:
                root = ET.fromstring(archive.read("xl/sharedStrings.xml"))
                for item in root.findall("m:si", _XLSX_NS):
                    shared.append(
                        "".join(
                            node.text or ""
                            for node in item.iter(
                                "{http://schemas.openxmlformats.org/"
                                "spreadsheetml/2006/main}t"
                            )
                        )
                    )
            sheet_name = next(
                (
                    name
                    for name in names
                    if name.startswith("xl/worksheets/sheet") and name.endswith(".xml")
                ),
                None,
            )
            if sheet_name is None:
                return []
            root = ET.fromstring(archive.read(sheet_name))
            values = []
            for row in root.findall(".//m:sheetData/m:row", _XLSX_NS):
                value = ""
                for cell in row.findall("m:c", _XLSX_NS):
                    ref = str(cell.get("r") or "")
                    if not re.match(r"^A\d+$", ref):
                        continue
                    cell_type = str(cell.get("t") or "")
                    if cell_type == "inlineStr":
                        value = "".join(
                            node.text or ""
                            for node in cell.iter(
                                "{http://schemas.openxmlformats.org/"
                                "spreadsheetml/2006/main}t"
                            )
                        )
                    else:
                        node = cell.find("m:v", _XLSX_NS)
                        raw = node.text if node is not None else ""
                        if cell_type == "s" and raw:
                            index = int(raw)
                            value = shared[index] if index < len(shared) else ""
                        else:
                            value = raw or ""
                    break
                values.append(value)
            return values
    except Exception:
        return []


def load_skip_task_name_rows(path) -> list:
    if path is None:
        return []
    file_path = Path(path)
    if not file_path.is_file():
        return []
    if file_path.suffix.casefold() == ".xlsx":
        lines = _xlsx_first_column(file_path)
    elif file_path.suffix.casefold() in (".xls", ".xlsm"):
        return []
    else:
        try:
            lines = file_path.read_text(encoding="utf-8-sig").splitlines()
        except Exception:
            return []
    names = []
    seen = set()
    for index, line in enumerate(lines):
        text = _normalize_text(str(line).split(",", 1)[0])
        if index == 0 or not text or text.startswith("#"):
            continue
        folded = text.casefold()
        if folded in seen:
            continue
        seen.add(folded)
        names.append(text)
    return names


def load_skip_task_names(path) -> set:
    """读取一列任务名称表：忽略首行标题，其余非空行按任务名称处理。"""
    return {name.casefold() for name in load_skip_task_name_rows(path)}


def append_skip_task_name(path, task_name: str) -> bool:
    """把任务名称追加到跳过表；表不存在时创建标题行。返回是否新增。"""
    if path is None:
        return False
    name = _normalize_text(task_name)
    if not name:
        return False
    if name.casefold() in load_skip_task_names(path):
        return False
    file_path = Path(path)
    suffix = file_path.suffix.casefold()
    if suffix == ".xlsx":
        rows = load_skip_task_name_rows(file_path)
        rows.append(name)
        write_table_xlsx(
            file_path,
            SKIP_TASK_HEADER,
            (SKIP_TASK_HEADER,),
            [[row] for row in rows],
            log=lambda _message: None,
        )
        return True
    if suffix in (".xls", ".xlsm"):
        return False
    try:
        file_path.parent.mkdir(parents=True, exist_ok=True)
        prefix = ""
        if not file_path.is_file() or file_path.stat().st_size == 0:
            prefix = SKIP_TASK_HEADER + "\n"
        with file_path.open("a", encoding="utf-8") as handle:
            handle.write(prefix + name + "\n")
    except Exception:
        return False
    return True


def _line_name_from_tower(value: object) -> str:
    text = _normalize_text(value)
    if not text:
        return ""
    return re.split(r"[:：]", text, maxsplit=1)[0].strip()


def _has_disabled_class(value: object) -> bool:
    for token in str(value or "").split():
        lowered = token.lower()
        if lowered in ("disabled", "is-disabled") or lowered.endswith("-disabled"):
            return True
    return False


def _is_api(url: str, path: str) -> bool:
    return urlsplit(url).path.endswith(path)



def _wait_for_timeout(page, timeout_ms: int, cancel_check=None) -> None:
    deadline = time.monotonic() + timeout_ms / 1000.0
    while True:
        if cancel_check:
            cancel_check()
        remaining_ms = (deadline - time.monotonic()) * 1000.0
        if remaining_ms <= 0:
            return
        page.wait_for_timeout(min(_CANCEL_WAIT_SLICE_MS, remaining_ms))


def validate_start_url(value: Optional[str]) -> str:
    text = "" if value is None else str(value).strip()
    if not text:
        return ""
    if any(character.isspace() for character in text):
        raise RuntimeError(
            "--start-url 必须是完整 http/https 网址，例如 https://example.test/app"
        )
    parsed = urlsplit(text)
    if (
        parsed.scheme not in ("http", "https")
        or not parsed.hostname
        or parsed.username
        or parsed.password
    ):
        raise RuntimeError(
            "--start-url 必须是完整 http/https 网址，例如 https://example.test/app"
        )
    try:
        parsed.port
    except ValueError:
        raise RuntimeError("--start-url 端口无效")
    return text


def validate_cdp_url(value: str):
    parsed = urlsplit(value)
    if (
        parsed.scheme != "http"
        or parsed.hostname not in ("127.0.0.1", "localhost", "::1")
        or parsed.username
        or parsed.password
        or parsed.path not in ("", "/")
        or parsed.query
        or parsed.fragment
    ):
        raise RuntimeError("--cdp-url 仅允许本机 HTTP 地址，例如 http://127.0.0.1:9222")
    try:
        port = parsed.port
    except ValueError:
        raise RuntimeError("--cdp-url 端口无效")
    if not port:
        raise RuntimeError("--cdp-url 必须明确指定端口")
    return value, port


def runtime_root() -> Path:
    source_output = SCRIPT_DIR / "output"
    writable_target = source_output if source_output.exists() else SCRIPT_DIR
    if os.access(writable_target, os.W_OK):
        return source_output
    if sys.platform.startswith("win"):
        base = Path(
            os.environ.get("LOCALAPPDATA", str(Path.home() / "AppData" / "Local"))
        )
    else:
        base = Path(
            os.environ.get("XDG_DATA_HOME", str(Path.home() / ".local" / "share"))
        )
    return base / "defect-review"


def _is_client_dev_script() -> bool:
    return os.environ.get("UVMP_DEV_SCRIPT") == "1"


def _stdin_is_tty() -> bool:
    try:
        return bool(sys.stdin and sys.stdin.isatty())
    except Exception:
        return False


def _client_input(prompt: str) -> str:
    """客户端脚本测试没有终端 stdin：枚举项取默认值，必填项直接报错。"""
    if prompt.startswith("请选择"):
        return ""
    raise EOFError("客户端脚本测试不等待交互输入")


def _client_config() -> dict:
    """客户端脚本测试会注入 UVMP_TOOLKIT_CONFIG，读取其浏览器配置。"""
    path = os.environ.get("UVMP_TOOLKIT_CONFIG", "").strip()
    if not path:
        return {}
    try:
        data = json.loads(Path(path).expanduser().read_text(encoding="utf-8"))
    except Exception:
        return {}
    return data if isinstance(data, dict) else {}


def _client_browser_path() -> str:
    render = _client_config().get("render") or {}
    if not isinstance(render, dict):
        return ""
    return str(render.get("chrome") or "").strip()


def _client_cdp_url() -> str:
    item = _client_config().get("browser_automation") or {}
    if not isinstance(item, dict):
        return ""
    return str(item.get("cdp_url") or "").strip()


def detect_browser(preferred: str = "") -> str:
    explicit = bool(preferred)
    if not preferred:
        preferred = _client_browser_path()
    if preferred:
        executable = Path(preferred).expanduser()
        if executable.exists():
            return str(executable)
        if explicit:
            raise RuntimeError("指定的浏览器不存在：%s" % executable)
        # 客户端配置里的路径可能已失效，继续走自动探测。
    names = (
        "google-chrome",
        "google-chrome-stable",
        "chromium",
        "chromium-browser",
        "qaxbrowser",
        "browser360",
        "browser360-cn",
        "360chrome",
        "microsoft-edge",
        "microsoft-edge-stable",
        "chrome",
    )
    for name in names:
        executable = shutil.which(name)
        if executable:
            return executable
    candidates = [
        "/usr/bin/qaxbrowser",
        "/opt/qaxbrowser/qaxbrowser",
        "/opt/apps/qaxbrowser/qaxbrowser",
        "/usr/bin/browser360",
        "/opt/apps/com.360.browser-stable/files/browser360",
        "/opt/google/chrome/chrome",
    ]
    if sys.platform.startswith("win"):
        candidates.extend(
            [
                r"C:\Program Files\Google\Chrome\Application\chrome.exe",
                r"C:\Program Files (x86)\Google\Chrome\Application\chrome.exe",
                r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe",
                r"C:\Program Files\Microsoft\Edge\Application\msedge.exe",
            ]
        )
        local_appdata = os.environ.get("LOCALAPPDATA", "")
        if local_appdata:
            candidates.append(
                str(Path(local_appdata) / r"Google\Chrome\Application\chrome.exe")
            )
    for candidate in candidates:
        if Path(candidate).exists():
            return candidate
    raise RuntimeError("未找到 Chrome/Chromium/公司浏览器，请用 --browser-path 指定")


def cdp_reachable(cdp_url: str) -> bool:
    try:
        with urllib_request.urlopen(
            cdp_url.rstrip("/") + "/json/list", timeout=1
        ) as response:
            return 200 <= response.status < 400
    except Exception:
        return False


def launch_browser(args, port: int, log: Callable[[str], None]) -> None:
    executable = detect_browser(args.browser_path)
    profile = Path(args.profile_dir).expanduser().resolve()
    profile.mkdir(parents=True, exist_ok=True)
    start_address = validate_start_url(getattr(args, "start_url", "")) or "about:blank"
    command = [
        executable,
        "--remote-debugging-port=%d" % port,
        "--user-data-dir=%s" % profile,
        "--no-first-run",
        "--no-default-browser-check",
        "--disable-dev-shm-usage",
        start_address,
    ]
    if hasattr(os, "geteuid") and os.geteuid() == 0:
        command.insert(1, "--no-sandbox")
    log("启动受控浏览器：%s" % executable)
    log("使用独立浏览器配置目录：%s" % profile)
    start_origin = _safe_origin(start_address)
    start_display = (
        start_origin + _page_path(start_address) if start_origin else "空白页"
    )
    log("浏览器起始地址：%s" % start_display)
    subprocess.Popen(command, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


@dataclass(frozen=True)
class ReviewSettings:
    task_status: str
    mission_type: str
    voltage_level: str
    line_name: str
    start_date: str
    end_date: str


@dataclass(frozen=True)
class ReviewImage:
    picture_guid: str
    file_guid: str
    index: int = 0
    audit_status: str = ""
    pic_defect_flag: str = ""
    line_name: str = ""
    tower_name: str = ""
    original_name: str = ""
    shoot_time: str = ""


class ReviewStopped(RuntimeError):
    """无法安全继续时中止整个运行。"""


class WriteResultUnknown(ReviewStopped):
    """审核或提交写请求已发出，但结果无法确认。"""


class WriteRejected(ReviewStopped):
    """审核或提交写请求返回明确的业务失败。"""


class UnknownTaskError(ReviewStopped):
    """任务中出现未分类异常，先给 120 秒容错，再刷新页面继续下一任务。"""


class ImageLoadTimeout(RuntimeError):
    """单次图片加载等待超时，可点击缩略图重试。"""


class ImageLoadFailure(RuntimeError):
    """图片首次加载加全部重试均失败，应关闭弹窗并跳过任务。"""


class ReviewButtonNotReady(RuntimeError):
    """图片已经显示，但「审核完成」按钮迟迟不可用，不能记作加载失败。"""


class ReviewButtonUnavailable(ReviewStopped):
    """图片已显示但「审核完成」按钮不可用，应按加载失败跳过该图片。"""


def _read_choice(
    args: argparse.Namespace,
    attr: str,
    label: str,
    options: Sequence[str],
    default: str,
    input_fn: Callable[[str], str],
) -> str:
    raw = getattr(args, attr, None)
    if raw is not None:
        value = str(raw).strip()
        if value:
            if value not in options:
                raise ValueError(
                    "%s必须是以下之一：%s；当前值：%s"
                    % (label, "、".join(options), value)
                )
            return value
    prompt = "请选择%s（可选：%s）[%s]：" % (label, "、".join(options), default)
    while True:
        try:
            answer = input_fn(prompt)
        except EOFError:
            raise RuntimeError("未提供%s，且当前终端无法交互输入" % label) from None
        value = (answer or "").strip()
        if not value:
            return default
        if value in options:
            return value
        print(
            "无效的%s：%s；可选值：%s" % (label, value, "、".join(options)),
            file=sys.stderr,
        )


def _date_error(value: str) -> str:
    if not _DATE_PATTERN.match(value):
        return "日期格式应为 YYYY-MM-DD：%s" % value
    try:
        datetime.strptime(value, "%Y-%m-%d")
    except ValueError:
        return "日期不存在：%s" % value
    return ""


def _read_required(
    args: argparse.Namespace,
    attr: str,
    label: str,
    input_fn: Callable[[str], str],
    hint: str,
    validator: Optional[Callable[[str], str]] = None,
) -> str:
    raw = getattr(args, attr, None)
    if raw is not None:
        value = str(raw).strip()
        if value:
            error = validator(value) if validator else ""
            if error:
                raise ValueError(error)
            return value
    prompt = "请输入%s（%s）：" % (label, hint)
    while True:
        try:
            answer = input_fn(prompt)
        except EOFError:
            raise RuntimeError("必须提供%s，且当前终端无法交互输入" % label) from None
        value = (answer or "").strip()
        if not value:
            print("%s不能为空，请重新输入" % label, file=sys.stderr)
            continue
        error = validator(value) if validator else ""
        if error:
            print("%s，请重新输入" % error, file=sys.stderr)
            continue
        return value


def parse_settings(
    args: argparse.Namespace, input_fn: Callable[[str], str] = input
) -> ReviewSettings:
    task_status = _read_choice(
        args,
        "task_status",
        "任务状态",
        TASK_STATUS_OPTIONS,
        DEFAULT_TASK_STATUS,
        input_fn,
    )
    mission_type = _read_choice(
        args,
        "mission_type",
        "作业性质",
        MISSION_TYPE_OPTIONS,
        DEFAULT_MISSION_TYPE,
        input_fn,
    )
    voltage_level = _read_choice(
        args,
        "voltage_level",
        "电压等级",
        VOLTAGE_LEVEL_OPTIONS,
        DEFAULT_VOLTAGE_LEVEL,
        input_fn,
    )
    line_name = _read_required(
        args,
        "line_name",
        "线路名称",
        input_fn,
        "必填，用于唯一匹配线路",
    )
    start_date = _read_required(
        args,
        "start_date",
        "开始日期",
        input_fn,
        "格式 YYYY-MM-DD，必填",
        _date_error,
    )
    end_date = _read_required(
        args,
        "end_date",
        "结束日期",
        input_fn,
        "格式 YYYY-MM-DD，必填",
        _date_error,
    )
    if start_date > end_date:
        raise ValueError("开始日期 %s 不能晚于结束日期 %s" % (start_date, end_date))
    return ReviewSettings(
        task_status=task_status,
        mission_type=mission_type,
        voltage_level=voltage_level,
        line_name=line_name,
        start_date=start_date,
        end_date=end_date,
    )


def parse_api_data(payload: object, description: str) -> object:
    if not isinstance(payload, dict):
        raise RuntimeError("%s响应不是 JSON 对象" % description)
    code = payload.get("code")
    success = payload.get("success")
    if code == 200 and success is True:
        return payload.get("data")
    message = payload.get("msg")
    if not isinstance(message, str) or not message.strip():
        message = "未知错误"
    raise RuntimeError(
        "%s返回失败（code=%r，success=%r）：%s"
        % (description, code, success, message.strip()[:200])
    )


def parse_task_page(payload: object) -> Tuple[list, int, int, int]:
    data = parse_api_data(payload, "缺陷识别任务列表")
    if not isinstance(data, dict):
        raise RuntimeError("缺陷识别任务列表响应缺少分页对象")
    records = data.get("records")
    if not isinstance(records, list):
        raise RuntimeError("缺陷识别任务列表响应 records 不是数组")
    numbers = []
    for field in ("total", "current", "pages"):
        value = data.get(field)
        if isinstance(value, bool) or not isinstance(value, int):
            raise RuntimeError("缺陷识别任务列表响应 %s 不是整数" % field)
        numbers.append(value)
    return records, numbers[0], numbers[1], numbers[2]


def _picture_group_items(data: dict) -> list:
    all_items = data.get("pictureListAll")
    if all_items is not None and not isinstance(all_items, list):
        raise RuntimeError("图片分组 pictureListAll 不是数组")
    if isinstance(all_items, list) and all_items:
        return list(all_items)
    combined = []
    seen = {}
    for field in ("examineList", "noTaggingList", "noExamineList", "taggingList"):
        value = data.get(field)
        if value is None:
            continue
        if not isinstance(value, list):
            raise RuntimeError("图片分组 %s 不是数组" % field)
        for item in value:
            if isinstance(item, dict):
                picture_guid = _normalize_text(item.get("pictureDetailGuid"))
                file_guid = _normalize_text(item.get("fileGuid"))
                if picture_guid:
                    if picture_guid in seen:
                        if seen[picture_guid] != file_guid:
                            raise RuntimeError(
                                "图片分组 %s 中图片 %s 的 fileGuid 冲突"
                                % (field, picture_guid)
                            )
                        continue
                    seen[picture_guid] = file_guid
            combined.append(item)
    return combined


def parse_task_images(group_payload: object, detail_payload: object = None) -> list:
    """解析图片分组与图片明细，按界面顺序生成待审核图片列表。"""
    data = parse_api_data(group_payload, "任务图片分组")
    if not isinstance(data, dict):
        raise RuntimeError("任务图片分组响应 data 不是对象")
    items = _picture_group_items(data)
    rows = []
    seen = {}
    for index, item in enumerate(items):
        if not isinstance(item, dict):
            raise RuntimeError("任务图片分组第 %d 项不是对象" % (index + 1))
        picture_guid = _normalize_text(item.get("pictureDetailGuid"))
        file_guid = _normalize_text(item.get("fileGuid"))
        if not picture_guid or not file_guid:
            raise RuntimeError(
                "任务图片第 %d 项缺少 pictureDetailGuid 或 fileGuid" % (index + 1)
            )
        if picture_guid in seen:
            raise RuntimeError(
                "任务图片分组存在重复 pictureDetailGuid：%s" % picture_guid
            )
        seen[picture_guid] = file_guid
        rows.append(
            {
                "picture_guid": picture_guid,
                "file_guid": file_guid,
                "index": index,
                "audit_status": _normalize_text(item.get("auditStatus")),
                "pic_defect_flag": _normalize_text(item.get("picDefectFlg")),
                "line_name": _normalize_text(item.get("lineName")),
                "tower_name": _normalize_text(item.get("deviceName")),
                "original_name": _normalize_text(item.get("originalName")),
                "shoot_time": _normalize_text(item.get("shootTime")),
            }
        )
    if not rows:
        raise RuntimeError("任务没有图片，无法审核")
    if detail_payload is None:
        return [ReviewImage(**row) for row in rows]

    detail_data = parse_api_data(detail_payload, "任务图片明细")
    if not isinstance(detail_data, list):
        raise RuntimeError("任务图片明细响应 data 不是数组")
    detail_files = {}
    detail_meta = {}
    for group_index, group in enumerate(detail_data):
        if not isinstance(group, dict):
            raise RuntimeError("任务图片明细第 %d 组不是对象" % (group_index + 1))
        pictures = group.get("pictureDetailVOS")
        if not isinstance(pictures, list):
            raise RuntimeError(
                "任务图片明细第 %d 组缺少 pictureDetailVOS 数组" % (group_index + 1)
            )
        group_line = _normalize_text(group.get("lineName"))
        group_tower = _normalize_text(group.get("towerName"))
        for picture in pictures:
            if not isinstance(picture, dict):
                raise RuntimeError("任务图片明细中存在非对象图片项")
            picture_guid = _normalize_text(picture.get("pictureDetailGuid"))
            file_guid = _normalize_text(picture.get("fileGuid"))
            if not picture_guid or not file_guid:
                raise RuntimeError(
                    "任务图片明细存在缺少 pictureDetailGuid 或 fileGuid 的项"
                )
            if picture_guid in detail_files and detail_files[picture_guid] != file_guid:
                raise RuntimeError(
                    "任务图片明细存在冲突的 pictureDetailGuid：%s" % picture_guid
                )
            detail_files[picture_guid] = file_guid
            meta = detail_meta.setdefault(
                picture_guid,
                {
                    "line_name": "",
                    "tower_name": "",
                    "original_name": "",
                    "shoot_time": "",
                },
            )
            for key, value in (
                ("line_name", _normalize_text(picture.get("lineName"))),
                ("tower_name", _normalize_text(picture.get("deviceName"))),
                ("original_name", _normalize_text(picture.get("originalName"))),
                ("shoot_time", _normalize_text(picture.get("shootTime"))),
            ):
                if value and not meta[key]:
                    meta[key] = value
            if group_line and not meta["line_name"]:
                meta["line_name"] = group_line
            if group_tower and not meta["tower_name"]:
                meta["tower_name"] = group_tower
    for row in rows:
        detail_file = detail_files.get(row["picture_guid"])
        if not detail_file:
            raise RuntimeError("任务图片明细缺少图片 %s" % row["picture_guid"])
        if detail_file != row["file_guid"]:
            raise RuntimeError(
                "图片 %s 的 fileGuid 在分组与明细中不一致：%s / %s"
                % (row["picture_guid"], row["file_guid"], detail_file)
            )
        meta = detail_meta.get(row["picture_guid"]) or {}
        for key in ("line_name", "tower_name", "original_name", "shoot_time"):
            if not row[key] and meta.get(key):
                row[key] = meta[key]
    return [ReviewImage(**row) for row in rows]


def choose_unique_line(options: Optional[Iterable[str]], typed: str) -> str:
    typed_text = (typed or "").strip()
    if not typed_text:
        raise ValueError("线路名称不能为空，无法匹配候选")
    candidates = []
    for item in options or ():
        text = str(item).strip() if item is not None else ""
        if text:
            candidates.append(text)
    if not candidates:
        raise RuntimeError(
            "线路「%s」没有匹配候选，请检查电压等级或补全线路名称" % typed_text
        )
    if len(candidates) > 1:
        shown = "、".join(candidates[:10])
        suffix = " 等" if len(candidates) > 10 else ""
        raise RuntimeError(
            "线路「%s」匹配到 %d 个候选（%s%s），请补全线路名称后重试"
            % (typed_text, len(candidates), shown, suffix)
        )
    candidate = candidates[0]
    if typed_text.casefold() not in candidate.casefold():
        raise RuntimeError(
            "线路「%s」的唯一候选「%s」与输入不匹配，请检查电压等级或重新输入"
            % (typed_text, candidate)
        )
    return candidate


def _id_set(values: Optional[Iterable[object]]) -> set:
    result = set()
    for value in values or ():
        text = "" if value is None else str(value).strip()
        if text:
            result.add(text)
    return result


def next_task_candidate(
    records: Optional[Iterable[object]],
    processed_ids: Optional[Iterable[object]],
    skipped_ids: Optional[Iterable[object]],
) -> Optional[dict]:
    processed = _id_set(processed_ids)
    skipped = _id_set(skipped_ids)
    for item in records or ():
        if not isinstance(item, dict):
            continue
        guid = str(item.get(TASK_GUID_FIELD) or "").strip()
        if not guid or guid in processed or guid in skipped:
            continue
        return item
    return None


def _flatten_params(payload: dict, result: dict) -> None:
    for key, value in payload.items():
        if isinstance(value, dict):
            _flatten_params(value, result)
        elif isinstance(value, (list, tuple)):
            for item in value:
                if isinstance(item, dict):
                    _flatten_params(item, result)
                elif item is not None:
                    result[str(key)] = str(item)
        elif value is not None:
            result[str(key)] = str(value)


def _request_query_params(response) -> Optional[dict]:
    request = getattr(response, "request", None)
    if request is None:
        return None
    result = {}
    payload = getattr(request, "post_data_json", None)
    if isinstance(payload, dict):
        _flatten_params(payload, result)
    if not result:
        raw = getattr(request, "post_data", None)
        if isinstance(raw, str) and raw.strip():
            try:
                parsed = json.loads(raw)
            except ValueError:
                parsed = parse_qs(raw)
            if isinstance(parsed, dict):
                _flatten_params(parsed, result)
    if not result:
        parsed = parse_qs(urlsplit(getattr(request, "url", "")).query)
        if parsed:
            _flatten_params(parsed, result)
    return result or None


def _normalize_datetime(value: object) -> str:
    text = str(value or "").strip().replace("T", " ")
    if "." in text:
        text = text.split(".", 1)[0]
    if text.endswith("Z"):
        text = text[:-1]
    return text


def _page_path(value: object) -> str:
    try:
        return urlsplit(str(value or "")).path or "/"
    except ValueError:
        return ""


def _safe_origin(value: object) -> str:
    """只保留协议、主机和端口，用于日志脱敏。"""
    try:
        parsed = urlsplit(str(value or ""))
        port = parsed.port
    except ValueError:
        return ""
    if not parsed.scheme or not parsed.hostname:
        return ""
    host = parsed.hostname
    if ":" in host and not host.startswith("["):
        host = "[%s]" % host
    return "%s://%s%s" % (
        parsed.scheme,
        host,
        ":%d" % port if port else "",
    )


def _redact_urls(text: object) -> str:
    """将文本中的 http/https URL 统一降为 scheme://host[:port]。"""

    def replace(match):
        safe = _safe_origin(match.group(0))
        return safe or "<url>"

    return _URL_PATTERN.sub(replace, str(text))


def _coerce_review_image(image: object) -> ReviewImage:
    if isinstance(image, ReviewImage):
        return image
    if isinstance(image, dict):
        picture_guid = _normalize_text(image.get("pictureDetailGuid"))
        file_guid = _normalize_text(image.get("fileGuid"))
        if not picture_guid or not file_guid:
            raise ReviewStopped("无法确定当前图片：缺少 pictureDetailGuid 或 fileGuid")
        index = image.get("index")
        if isinstance(index, bool) or not isinstance(index, int):
            index = 0
        return ReviewImage(
            picture_guid=picture_guid,
            file_guid=file_guid,
            index=index,
            audit_status=_normalize_text(image.get("auditStatus")),
            pic_defect_flag=_normalize_text(image.get("picDefectFlg")),
            line_name=_normalize_text(image.get("lineName")),
            tower_name=_normalize_text(
                image.get("deviceName") or image.get("towerName")
            ),
            original_name=_normalize_text(image.get("originalName")),
            shoot_time=_normalize_text(image.get("shootTime")),
        )
    raise ReviewStopped("无法确定当前图片")


def is_system_defect_image(image: object) -> bool:
    """图片分组里的系统已识别缺陷框标记。"""
    if isinstance(image, ReviewImage):
        flag = image.pic_defect_flag
    elif isinstance(image, dict):
        flag = _normalize_text(image.get("picDefectFlg"))
    else:
        flag = ""
    return flag.casefold() == SYSTEM_DEFECT_FLAG


def is_unreviewed_image(image: object) -> bool:
    """UI 对勾表示人工审核状态：只有 unreviewed 未打勾。"""
    if isinstance(image, ReviewImage):
        status = image.audit_status
    elif isinstance(image, dict):
        status = _normalize_text(image.get("auditStatus"))
    else:
        status = ""
    return status.casefold() in ("", "unreviewed")


def is_reviewable_image(image: object) -> bool:
    """未打勾且不是系统缺陷框的图片才有「审核完成」，可自动审核。"""
    return is_unreviewed_image(image) and not is_system_defect_image(image)


def _unique_join(values: Iterable[object], separator: str = "；") -> str:
    result = []
    seen = set()
    for value in values:
        text = _normalize_text(value)
        if not text or text in seen:
            continue
        seen.add(text)
        result.append(text)
    return separator.join(result)


def _unique_join_longest(values: Iterable[object], separator: str = "；") -> str:
    """同一缺陷名可能带不同详细说明，只保留最长的那个，避免重复行。"""
    result = []
    for value in values:
        text = _normalize_text(value)
        if not text:
            continue
        replaced = False
        for index, existing in enumerate(result):
            if text == existing:
                replaced = True
                break
            if text.startswith(existing):
                result[index] = text
                replaced = True
                break
            if existing.startswith(text):
                replaced = True
                break
        if not replaced:
            result.append(text)
    return separator.join(result)


def _format_coordinate(value: object) -> str:
    if isinstance(value, bool):
        return ""
    if isinstance(value, (int, float)):
        return "%g" % float(value)
    return _normalize_text(value)


def summarize_tagging_details(details: object) -> dict:
    """把系统缺陷框明细压成 xlsx 需要的几列。"""
    if not isinstance(details, list):
        return {"description": "", "level": "", "coordinate": "", "algorithm": ""}
    descriptions = []
    levels = []
    coordinates = []
    algorithms = []
    for item in details:
        if not isinstance(item, dict):
            continue
        name = _normalize_text(item.get("defectName"))
        if not name:
            name = "/".join(
                part
                for part in (
                    _normalize_text(item.get("componentName")),
                    _normalize_text(item.get("partName")),
                    _normalize_text(item.get("defectDescriptionName")),
                )
                if part
            )
        detailed = _normalize_text(item.get("detailedDescription"))
        if detailed and detailed != name:
            name = "%s（%s）" % (name, detailed) if name else detailed
        if name:
            descriptions.append(name)
        level = _normalize_text(item.get("defectLevelName"))
        if level:
            levels.append(level)
        coordinate = ",".join(
            _format_coordinate(item.get(key))
            for key in ("xmin", "ymin", "xmax", "ymax")
        )
        if coordinate.strip(","):
            coordinates.append(coordinate)
        algorithm = _normalize_text(item.get("shortName"))
        algorithm_type = _normalize_text(item.get("algorithmType"))
        if algorithm_type.casefold() == "yes":
            algorithms.append(algorithm or "系统算法")
        elif algorithm:
            algorithms.append(algorithm)
    return {
        "description": _unique_join_longest(descriptions),
        "level": _unique_join(levels),
        "coordinate": _unique_join(coordinates),
        "algorithm": _unique_join(algorithms),
    }


def _xlsx_escape(value: object) -> str:
    text = "" if value is None else str(value)
    text = re.sub(r"[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]", "", text)
    return (
        text.replace("&", "&amp;")
        .replace("<", "&lt;")
        .replace(">", "&gt;")
        .replace('"', "&quot;")
        .replace("'", "&apos;")
    )


def _xlsx_col_letter(index: int) -> str:
    result = ""
    while index > 0:
        index, remainder = divmod(index - 1, 26)
        result = chr(65 + remainder) + result
    return result


def _xlsx_sheet_name(value: object, index: int) -> str:
    text = re.sub(r"[\\/*?:\[\]]", "_", str(value or "")).strip()
    return (text or "Sheet%d" % (index + 1))[:31]


def _xlsx_sheet_xml(headers, rows):
    num_cols = len(headers)
    num_rows = len(rows)

    def display_width(value) -> int:
        text = "" if value is None else str(value)
        return sum(2 if ord(character) > 0x7F else 1 for character in text)

    widths = []
    for column in range(num_cols):
        width = display_width(headers[column])
        for row in rows:
            if column < len(row):
                width = max(width, display_width(row[column]))
        widths.append(max(10, min(30, width + 2)))

    sheet_rows = []
    header_cells = []
    for column, caption in enumerate(headers, 1):
        ref = "%s1" % _xlsx_col_letter(column)
        header_cells.append(
            '<c r="%s" s="0" t="inlineStr"><is><t>%s</t></is></c>'
            % (ref, _xlsx_escape(caption))
        )
    sheet_rows.append(
        '<row r="1" spans="1:%d">%s</row>' % (num_cols, "".join(header_cells))
    )

    for row_index, row in enumerate(rows, 2):
        cells = []
        for column in range(1, num_cols + 1):
            value = row[column - 1] if column - 1 < len(row) else None
            ref = "%s%d" % (_xlsx_col_letter(column), row_index)
            if value is None or value == "":
                cells.append('<c r="%s"/>' % ref)
            elif isinstance(value, (int, float)) and not isinstance(value, bool):
                cells.append('<c r="%s" s="1"><v>%s</v></c>' % (ref, value))
            else:
                cells.append(
                    '<c r="%s" s="1" t="inlineStr"><is><t>%s</t></is></c>'
                    % (ref, _xlsx_escape(value))
                )
        sheet_rows.append(
            '<row r="%d" spans="1:%d">%s</row>' % (row_index, num_cols, "".join(cells))
        )

    xml = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<dimension ref="A1:%s%d"/>
<sheetViews><sheetView workbookViewId="0" tabSelected="0"><selection activeCell="A2" sqref="A2"/></sheetView></sheetViews>
<sheetFormatPr baseColWidth="10" defaultColWidth="15" defaultRowHeight="13" x14ac:dyDescent="0.15" xmlns:x14ac="http://schemas.microsoft.com/office/spreadsheetml/2009/9/main"/>
<cols>
%s
</cols>
<sheetData>
%s
</sheetData>
<pageMargins left="0.7" right="0.7" top="1.08" footer="0.78" header="0.78"/>
</worksheet>""" % (
        _xlsx_col_letter(num_cols),
        num_rows + 1,
        "".join(
            '<col collapsed="0" hidden="0" max="%d" min="%d" style="0" width="%d"/>'
            % (column, column, widths[column - 1])
            for column in range(1, num_cols + 1)
        ),
        "".join(sheet_rows),
    )
    return xml


def write_workbook_xlsx(path, sheets, log=print) -> dict:
    """无第三方依赖的多工作表 xlsx 写出（内联字符串，适配 WPS/Excel）。"""
    Path(path).parent.mkdir(parents=True, exist_ok=True)
    normalized = []
    used_names = set()
    for index, item in enumerate(sheets):
        if not isinstance(item, (list, tuple)) or len(item) != 3:
            raise ValueError("工作表必须为 (名称, 表头, 行) 三元组")
        sheet_name, headers, rows = item
        headers = list(headers or [])
        if not headers:
            raise ValueError("工作表 %s 缺少表头" % (sheet_name or index + 1))
        name = _xlsx_sheet_name(sheet_name, index)
        if name.casefold() in used_names:
            name = ("%s_%d" % (name[:28], index + 1))[:31]
        used_names.add(name.casefold())
        normalized.append((name, headers, list(rows or [])))

    content_types = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
%s
<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>
</Types>""" % "".join(
        '<Override PartName="/xl/worksheets/sheet%d.xml" '
        'ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>'
        % (index + 1)
        for index in range(len(normalized))
    )

    rels = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>"""

    workbook = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets>
%s
</sheets>
</workbook>""" % "".join(
        '<sheet name="%s" sheetId="%d" r:id="rId%d"/>'
        % (_xlsx_escape(name), index + 1, index + 1)
        for index, (name, _headers, _rows) in enumerate(normalized)
    )

    workbook_rels = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
%s
<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>""" % (
        "".join(
            '<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>'
            % (index + 1, index + 1)
            for index in range(len(normalized))
        ),
        len(normalized) + 1,
    )

    styles = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<fonts count="2">
<font><name val="微软雅黑"/><family val="2"/><size val="11"/></font>
<font b="1"><name val="微软雅黑"/><family val="2"/><size val="11"/><color rgb="FFFFFFFF"/></font>
</fonts>
<fills count="2">
<fill><patternFill patternType="none"/></fill>
<fill><patternFill patternType="solid"><fgColor rgb="FF4472C4"/></patternFill></fill>
</fills>
<borders count="1"><border><left/><right/><top/><bottom/></border></borders>
<cellStyleXfs count="1"><xf/></cellStyleXfs>
<cellXfs count="2">
<xf applyAlignment="1" applyFill="1" applyFont="1" borderId="0" fillId="1" fontId="1" numFmtId="0" xfId="0">
<alignment horizontal="center" vertical="center" wrapText="1"/>
</xf>
<xf applyAlignment="1" borderId="0" fillId="0" fontId="0" numFmtId="0" xfId="0">
<alignment vertical="center" wrapText="1"/>
</xf>
</cellXfs>
</styleSheet>"""

    counts = {}
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as archive:
        archive.writestr("[Content_Types].xml", content_types)
        archive.writestr("_rels/.rels", rels)
        archive.writestr("xl/workbook.xml", workbook)
        archive.writestr("xl/_rels/workbook.xml.rels", workbook_rels)
        archive.writestr("xl/styles.xml", styles)
        for index, (name, headers, rows) in enumerate(normalized, 1):
            xml = _xlsx_sheet_xml(headers, rows)
            if index == 1:
                xml = xml.replace('tabSelected="0"', 'tabSelected="1"', 1)
            archive.writestr("xl/worksheets/sheet%d.xml" % index, xml)
            counts[name] = len(rows)
    log(
        "  Excel 文件已保存: %s（%s）"
        % (path, "；".join("%s %d 条" % (name, count) for name, count in counts.items()))
    )
    return counts


def write_table_xlsx(path, sheet_name: str, headers, rows, log=print) -> int:
    """兼容单表调用；汇总表统一走 write_workbook_xlsx。"""
    counts = write_workbook_xlsx(path, [(sheet_name, headers, rows)], log=log)
    return next(iter(counts.values()), 0)


class ResponseMonitor(object):
    def __init__(self, page=None, cancel_check=None):
        self.page = page
        self.cancel_check = cancel_check
        self.responses = []

    def __call__(self, response) -> None:
        self.responses.append(response)

    def _matches_page(self, response) -> bool:
        if self.page is None:
            return True
        try:
            return response.request.frame.page == self.page
        except Exception:
            return False

    def wait_success(self, api_path, start, timeout_ms, description, accept=None):
        deadline = time.monotonic() + timeout_ms / 1000.0
        while True:
            if self.cancel_check:
                self.cancel_check()
            for response in self.responses[start:]:
                if not self._matches_page(response) or not _is_api(
                    response.url, api_path
                ):
                    continue
                if accept is not None and not accept(response):
                    continue
                if response.status != 200:
                    raise RuntimeError(
                        "%s HTTP 状态为 %s" % (description, response.status)
                    )
                try:
                    payload = response.json()
                except Exception as exc:
                    raise RuntimeError("%s响应读取失败：%s" % (description, exc))
                parse_api_data(payload, description)
                return payload, response
            if self.page is not None and self.page.is_closed():
                raise RuntimeError("等待%s时页面已关闭" % description)
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise RuntimeError(
                    "等待%s超时（%.0f 秒）" % (description, timeout_ms / 1000.0)
                )
            if self.page is None:
                time.sleep(min(0.2, remaining))
            else:
                self.page.wait_for_timeout(
                    min(_CANCEL_WAIT_SLICE_MS, remaining * 1000.0)
                )


class ReviewRunner(object):
    def __init__(self, args, outdir, log_handler=None):
        self.args = args
        self.outdir = Path(outdir)
        self.outdir.mkdir(parents=True, exist_ok=True)
        self.timeout_ms = max(1, int(float(args.timeout) * 1000))
        self.log_handler = log_handler
        # 无论是否接入客户端 Job 日志，每次运行都在输出目录留一份 run.log。
        # 逐行追加而不长期占用句柄，避免 Windows 下任务结束后日志文件仍被锁。
        self.log_path = self.outdir / "run.log"
        self._cancel_logged = False
        self.cancel_check = getattr(args, "cancel_check", None)
        self.sync_playwright = None
        self.playwright = None
        self.browser = None
        self.page = None
        self.monitor = None
        self.line_candidates = []
        self.selected_line_guid = ""
        self.selected_line_name = ""
        self.last_scan_summary = {}
        self.diagnostic_number = 0
        self.image_timeout_ms = (
            float(getattr(args, "image_timeout", DEFAULT_IMAGE_TIMEOUT)) * 1000.0
        )
        self.image_retries = max(
            0, int(getattr(args, "image_retries", DEFAULT_IMAGE_RETRIES))
        )
        self.max_unreviewed = max(
            1, int(getattr(args, "max_unreviewed", DEFAULT_MAX_UNREVIEWED))
        )
        self.unknown_timeout_ms = max(
            1.0,
            float(getattr(args, "unknown_timeout", DEFAULT_UNKNOWN_TIMEOUT)) * 1000.0,
        )
        self.task_retries = max(
            0, int(getattr(args, "task_retries", DEFAULT_TASK_RETRIES))
        )
        self.stop_requested = False
        self.awake_guard = None
        self.tagging_detail_timeout_ms = min(
            float(DEFAULT_TAGGING_DETAIL_TIMEOUT) * 1000.0, self.timeout_ms
        )
        self.diagnose_button = bool(getattr(args, "diagnose_button", False))
        self.button_diagnosed = False
        self.ignore_defect_boxes = bool(getattr(args, "ignore_defect_boxes", False))
        self.progress_handler = getattr(args, "progress_handler", None)
        self.progress_label = ""
        self.task_progress_total = 0
        self.task_progress_done = 0
        skip_path = _normalize_text(getattr(args, "skip_tasks_file", ""))
        self.skip_tasks_file = Path(skip_path).expanduser() if skip_path else None
        self.manual_task_page = bool(getattr(args, "manual_task_page", False)) or bool(
            getattr(args, "launch", False)
        )
        self.task_page_timeout_ms = max(
            1.0,
            float(getattr(args, "login_timeout", DEFAULT_LOGIN_TIMEOUT)) * 1000.0,
        )
        self.current_dialog = None
        self.current_task_name = ""
        self.reviewed_guids = set()
        self.reviewed_count = 0
        self.task_image_total = 0
        self.task_images = []
        self.last_run_summary = {}
        self.system_defect_rows = []
        self.system_defect_guids = set()
        self.system_defect_dirty = False
        self.anomaly_rows = []
        self.anomaly_keys = set()
        self.anomaly_dirty = False
        self.skip_task_names = set()
        self.summary_xlsx_path = self.outdir / SUMMARY_XLSX_NAME
        self.output_skip_path = self.outdir / SKIP_TASKS_OUTPUT_NAME
        self.output_skip_names = set()

    def log(self, message: object) -> None:
        line = "[%s] %s" % (datetime.now().strftime("%Y-%m-%d %H:%M:%S"), message)
        if self.log_handler:
            self.log_handler(str(message))
        else:
            print(line, flush=True)
        try:
            with self.log_path.open("a", encoding="utf-8") as handle:
                handle.write(line + "\n")
        except Exception:
            pass

    def check_cancel(self) -> None:
        if not self.cancel_check:
            return
        try:
            self.cancel_check()
        except Exception:
            self.stop_requested = True
            if not self._cancel_logged:
                self._cancel_logged = True
                self.log("收到停止请求，正在中止当前浏览器操作")
            raise

    def keep_awake(self) -> None:
        """运行期间阻止宿主机关屏/休眠；不支持时自动降级，不阻断审核。"""
        if self.awake_guard is None:
            self.awake_guard = HostAwakeGuard(self.log)
        self.awake_guard.start()

    def release_keep_awake(self) -> None:
        if self.awake_guard is not None:
            self.awake_guard.stop()
            self.awake_guard = None

    def _wait_for_timeout(self, page, timeout_ms: int) -> None:
        _wait_for_timeout(page, timeout_ms, self.check_cancel)

    def load_playwright(self) -> None:
        try:
            from playwright.sync_api import sync_playwright
        except ImportError:
            raise RuntimeError(
                "缺少 Playwright。请先为当前 Python 安装：python3 -m pip install playwright；"
                "内网离线环境请使用对应 Python 版本和 CPU 架构的 wheel 包"
            )
        self.sync_playwright = sync_playwright

    def _select_page(self):
        pages = []
        contexts = getattr(self.browser, "contexts", []) or []
        for context in contexts:
            pages.extend(context.pages)
        if not pages:
            if not contexts:
                raise RuntimeError("受控浏览器没有可用上下文")
            pages = [contexts[0].new_page()]

        def is_visible(page):
            try:
                return page.evaluate("() => document.visibilityState === 'visible'")
            except Exception:
                return False

        visible_pages = [page for page in pages if is_visible(page)]
        return (visible_pages or pages)[0]

    def connect(self, launched: bool = False) -> None:
        validate_cdp_url(self.args.cdp_url)
        if self.sync_playwright is None:
            self.load_playwright()
        self.playwright = self.sync_playwright().start()
        deadline = time.monotonic() + (self.args.timeout if launched else 0)
        attempt = 0
        while True:
            attempt += 1
            try:
                self.browser = self.playwright.chromium.connect_over_cdp(
                    self.args.cdp_url,
                    timeout=min(self.timeout_ms, 8000) if launched else self.timeout_ms,
                )
                self.log("已连接本机浏览器调试端口")
                break
            except Exception as exc:
                if not launched or time.monotonic() >= deadline:
                    raise RuntimeError(
                        "无法连接 %s。请先用 --launch 启动受控浏览器，"
                        "或确认浏览器已开启远程调试。原始错误：%s"
                        % (self.args.cdp_url, str(exc)[:300])
                    )
                if attempt == 1:
                    self.log("等待受控浏览器启动……")
                time.sleep(1)
        self.page = self._select_page()
        self.log("当前页面路径：%s" % _page_path(self.page.url))
        # 动作（click/fill/hover）用短超时，卡住时几秒内退出并响应停止；
        # 引擎自己的等待循环仍按 --timeout 控制，不受这里影响。
        self.page.set_default_timeout(min(self.timeout_ms, _ACTION_TIMEOUT_MS))
        self.page.set_default_navigation_timeout(
            min(self.timeout_ms, _NAVIGATION_TIMEOUT_MS)
        )
        try:
            self.page.bring_to_front()
        except Exception:
            pass
        start_url = validate_start_url(getattr(self.args, "start_url", ""))
        if start_url and self.page.url != start_url:
            self.page.goto(start_url, wait_until="domcontentloaded")
            self.log("已打开起始地址，当前页面路径：%s" % _page_path(self.page.url))
        self._ensure_monitor()

    def _ensure_monitor(self) -> ResponseMonitor:
        if self.page is None:
            raise RuntimeError("尚未连接受控浏览器")
        if self.monitor is None:
            self.monitor = ResponseMonitor(
                page=self.page, cancel_check=self.check_cancel
            )
            self.page.on("response", self.monitor)
        return self.monitor

    def _current_route(self) -> str:
        if self.page is None or self.page.is_closed():
            return ""
        try:
            parsed = urlsplit(str(self.page.url or ""))
        except ValueError:
            return ""
        return ("%s#%s" % (parsed.path, parsed.fragment)).casefold()

    def _is_login_url(self) -> bool:
        return "/login" in self._current_route()

    def _is_task_page_url(self) -> bool:
        return TASK_PAGE_PATH.casefold() in self._current_route()

    def _task_list_visible(self) -> bool:
        if self.page is None or self.page.is_closed():
            return False
        if not self._is_task_page_url():
            return False
        try:
            tables = self.page.locator(".el-table:visible")
            for index in range(tables.count()):
                table = tables.nth(index)
                header_locator = table.locator(".el-table__header").first
                if not header_locator.count():
                    continue
                header = _normalize_text(header_locator.inner_text())
                if TASK_TABLE_NAME_HEADER in header and (
                    "任务状态" in header or "操作" in header
                ):
                    return True
        except Exception:
            return False
        return False

    def _visible_text(self, text: str):
        locator = self.page.get_by_text(text, exact=True)
        for index in range(locator.count()):
            item = locator.nth(index)
            try:
                if item.is_visible():
                    return item
            except Exception:
                continue
        return None

    def _login_page_visible(self) -> bool:
        if self.page is None or self.page.is_closed():
            return True
        if self._is_login_url():
            return True
        if (
            self._find_visible_button(self.page, "登录", require_enabled=False)
            is not None
        ):
            return True
        for text in ("验证码", "* 验证码", "用户名", "* 用户名", "密码", "* 密码"):
            if self._visible_text(text) is not None:
                return True
        return False

    def _login_ready(self) -> bool:
        if self.page is None or self.page.is_closed():
            return False
        if self._login_page_visible():
            return False
        if self._task_list_visible():
            return True
        return any(
            self._visible_text(label) is not None for label in TASK_LIST_MENU_PATH
        )

    def is_on_login_page(self) -> bool:
        return self._login_page_visible()

    def wait_for_login(self, timeout_ms: float) -> None:
        seconds = max(1.0, float(timeout_ms)) / 1000.0
        self.log(
            "请在受控浏览器中登录系统并进入任意系统页面；脚本最多等待 %.0f 秒……"
            % seconds
        )
        self._wait_for_value(
            self._login_ready, max(1.0, float(timeout_ms)), "人工登录系统"
        )
        self.log("已检测到登录后的系统页面，继续")

    def wait_for_task_page(self, timeout_ms: float) -> None:
        if self._task_list_visible():
            self.log("当前页面已在缺陷识别任务列表")
            return
        seconds = max(1.0, float(timeout_ms)) / 1000.0
        self.log(
            "请手动进入「智能分析 → 智能识别 → 缺陷识别任务」；"
            "脚本最多等待 %.0f 秒……" % seconds
        )
        self._wait_for_value(
            self._task_list_visible,
            max(1.0, float(timeout_ms)),
            "手动进入缺陷识别任务列表",
        )
        self.log("已检测到缺陷识别任务列表，继续")

    def _open_menu(self, label: str) -> None:
        item = self._visible_text(label)
        if item is None:
            raise RuntimeError(
                "未找到菜单项「%s」。请确认已登录且页面菜单已展开" % label
            )
        item.hover()

    def _click_menu(self, label: str) -> None:
        item = self._visible_text(label)
        if item is None:
            raise RuntimeError(
                "未找到菜单项「%s」。请确认已登录且页面菜单已展开" % label
            )
        item.click()

    def _wait_for_task_list(self) -> None:
        deadline = time.monotonic() + self.timeout_ms / 1000.0
        while True:
            if self._task_list_visible():
                return
            if self.page.is_closed():
                raise RuntimeError("等待缺陷识别任务列表时页面已关闭")
            if time.monotonic() >= deadline:
                raise RuntimeError(
                    "未找到缺陷识别任务列表。请确认账号有权限，并在受控浏览器中"
                    "打开「智能分析 → 智能识别 → 缺陷识别任务」后重试"
                )
            self._wait_for_timeout(self.page, 200)

    def open_task_list(self) -> bool:
        if self.page is None:
            raise RuntimeError("尚未连接受控浏览器")
        if self.manual_task_page:
            self.wait_for_task_page(self.task_page_timeout_ms)
            return False
        if self._is_task_page_url():
            self._wait_for_task_list()
            self.log("当前页面已在缺陷识别任务列表，跳过菜单导航")
            return False
        start_url = validate_start_url(getattr(self.args, "start_url", ""))
        if start_url and self.page.url in ("", "about:blank"):
            self.page.goto(start_url, wait_until="domcontentloaded")
        for label in TASK_LIST_MENU_PATH[:-1]:
            self._open_menu(label)
            self._wait_for_timeout(self.page, 300)
        self._click_menu(TASK_LIST_MENU_PATH[-1])
        self._wait_for_task_list()
        self.log("已进入缺陷识别任务列表")
        return True

    def _find_form_item(
        self,
        labels: Sequence[str],
        description: str,
        wait_seconds: float = 10.0,
    ):
        deadline = time.monotonic() + min(
            self.timeout_ms / 1000.0, max(0.1, float(wait_seconds))
        )
        while True:
            self.check_cancel()
            items = self.page.locator(".el-form-item:visible")
            for index in range(items.count()):
                item = items.nth(index)
                try:
                    label_locator = item.locator(".el-form-item__label").first
                    text = (
                        label_locator.inner_text()
                        if label_locator.count()
                        else item.inner_text()
                    )
                except Exception:
                    text = item.inner_text()
                cleaned = _normalize_text(text).rstrip("：:")
                for label in labels:
                    if label in cleaned:
                        return item
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise RuntimeError("未找到筛选表单项「%s」" % description)
            self._wait_for_timeout(
                self.page, int(min(_CANCEL_WAIT_SLICE_MS, remaining * 1000.0))
            )

    def _visible_dropdown(self):
        dropdowns = self.page.locator(".el-select-dropdown:visible")
        if not dropdowns.count():
            raise RuntimeError("筛选下拉未展开")
        return dropdowns.last

    def _selected_text(self, item) -> str:
        texts = []
        select = item.locator(".el-select:visible").first
        if select.count():
            texts.append(select.inner_text())
            input_locator = select.locator("input").first
            if input_locator.count():
                texts.append(input_locator.input_value())
        texts.append(item.inner_text())
        return _normalize_text(" ".join(str(text or "") for text in texts))

    def _find_dropdown_option(self, dropdown, value: str):
        options = dropdown.locator(".el-select-dropdown__item:visible")
        for index in range(options.count()):
            option = options.nth(index)
            if _normalize_text(option.inner_text()) == value:
                return option
        raise RuntimeError("下拉中没有找到选项「%s」" % value)

    def _choose_select_option(
        self,
        item,
        value: str,
        description: str,
        before_click=None,
    ) -> None:
        select = item.locator(".el-select:visible").first
        if not select.count():
            select = item.locator(".el-select").first
        if not select.count():
            raise RuntimeError("筛选项「%s」没有下拉控件" % description)
        select.click()
        self._wait_for_timeout(self.page, 150)
        option = self._find_dropdown_option(self._visible_dropdown(), value)
        if before_click is not None:
            before_click()
        option.click()
        self._wait_for_timeout(self.page, 100)
        actual = self._selected_text(item)
        if value not in actual:
            raise RuntimeError(
                "筛选项「%s」选择「%s」后实际显示「%s」" % (description, value, actual)
            )

    def _select_form_option(
        self, labels: Sequence[str], value: str, description: str
    ) -> None:
        item = self._find_form_item(labels, description)
        self._choose_select_option(item, value, description)

    def _line_response_voltage_code(self, response) -> str:
        params = _request_query_params(response) or {}
        code = params.get("voltageLevel")
        if code is not None:
            return _normalize_text(code)
        return ""

    def _select_voltage(self, target: str) -> None:
        item = self._find_form_item(("电压等级",), "电压等级")
        target_index = VOLTAGE_LEVEL_OPTIONS.index(target)
        neighbor_index = target_index - 1 if target_index > 0 else 1
        neighbor = VOLTAGE_LEVEL_OPTIONS[neighbor_index]
        self._choose_select_option(item, neighbor, "电压等级")
        self._wait_for_timeout(self.page, 200)
        mark = [0]

        def reset_mark():
            mark[0] = len(self.monitor.responses)

        self._choose_select_option(item, target, "电压等级", before_click=reset_mark)
        expected_code = VOLTAGE_LEVEL_CODES[target]
        payload, _response = self.monitor.wait_success(
            LINE_CONDITION_API,
            mark[0],
            self.timeout_ms,
            "目标电压「%s」的线路候选" % target,
            accept=lambda response: (
                self._line_response_voltage_code(response) == expected_code
            ),
        )
        data = parse_api_data(payload, "目标电压线路候选")
        if not isinstance(data, list):
            raise RuntimeError("目标电压线路候选响应 data 不是数组")
        if not data:
            raise RuntimeError("目标电压「%s」没有线路候选，已停止筛选" % target)
        for index, candidate in enumerate(data):
            if not isinstance(candidate, dict):
                raise RuntimeError("目标电压线路候选第 %d 项不是对象" % (index + 1))
            name = _normalize_text(candidate.get("lineName"))
            code = _normalize_text(candidate.get("voltageLevel"))
            voltage_name = _normalize_text(candidate.get("voltageName"))
            if not name:
                raise RuntimeError(
                    "目标电压线路候选第 %d 项缺少 lineName" % (index + 1)
                )
            if not code and not voltage_name:
                raise RuntimeError(
                    "目标电压线路候选「%s」缺少电压字段，无法核对" % name
                )
            if code and code != expected_code:
                raise RuntimeError(
                    "线路候选「%s」电压码为 %s，与目标电压「%s」不一致"
                    % (name, code, target)
                )
            if voltage_name and voltage_name != target:
                raise RuntimeError(
                    "线路候选「%s」电压为「%s」，与目标电压「%s」不一致"
                    % (name, voltage_name, target)
                )
        self.line_candidates = data
        self.selected_line_guid = ""
        self.selected_line_name = ""

    def _select_line(self, typed: str) -> str:
        item = self._find_form_item(("线路名称",), "线路名称")
        select = item.locator(".el-select:visible").first
        if not select.count():
            select = item.locator(".el-select").first
        if not select.count():
            raise RuntimeError("线路名称没有下拉控件")
        select.click()
        self._wait_for_timeout(self.page, 150)
        input_locator = select.locator("input").first
        if not input_locator.count():
            input_locator = item.locator("input").first
        if not input_locator.count():
            raise RuntimeError("线路名称没有可输入的输入框")
        input_locator.fill(typed)
        self._wait_for_timeout(self.page, 250)
        dropdown = self._visible_dropdown()
        options = dropdown.locator(".el-select-dropdown__item:visible")
        texts = []
        for index in range(options.count()):
            text = _normalize_text(options.nth(index).inner_text())
            if text:
                texts.append(text)
        chosen = choose_unique_line(texts, typed)
        candidate = None
        for line in self.line_candidates:
            if not isinstance(line, dict):
                continue
            if _normalize_text(line.get("lineName")) == chosen:
                candidate = line
                break
        if candidate is None:
            raise RuntimeError(
                "线路下拉「%s」不在目标电压接口候选内，无法核对" % chosen
            )
        self._find_dropdown_option(dropdown, chosen).click()
        self._wait_for_timeout(self.page, 100)
        actual = self._selected_text(item)
        if chosen not in actual:
            raise RuntimeError(
                "线路选择核对失败：预期「%s」，页面显示「%s」" % (chosen, actual)
            )
        self.selected_line_name = chosen
        self.selected_line_guid = _normalize_text(candidate.get("lineGuid"))
        return chosen

    def _fill_date(self, labels: Sequence[str], value: str, description: str) -> None:
        item = self._find_form_item(labels, description)
        input_locator = item.locator("input:visible").first
        if not input_locator.count():
            input_locator = item.locator("input").first
        if not input_locator.count():
            raise RuntimeError("%s没有可填写的输入框" % description)
        input_locator.click()
        input_locator.fill(value)
        input_locator.press("Enter")
        self._wait_for_timeout(self.page, 100)
        actual = str(input_locator.input_value() or "").strip()
        if not actual.startswith(value):
            raise RuntimeError(
                "%s填写失败：输入「%s」，页面显示「%s」" % (description, value, actual)
            )

    def _set_input_value(self, input_locator, value: str, description: str) -> None:
        try:
            input_locator.evaluate(
                "(el) => { el.removeAttribute('readonly');"
                " el.removeAttribute('disabled'); }"
            )
        except Exception:
            pass
        try:
            input_locator.fill(value)
        except Exception:
            pass
        actual = str(input_locator.input_value() or "").strip()
        if not actual.startswith(value):
            try:
                input_locator.evaluate(
                    """(el, value) => {
  const proto = window.HTMLInputElement && window.HTMLInputElement.prototype;
  const desc = proto && Object.getOwnPropertyDescriptor(proto, 'value');
  if (desc && desc.set) desc.set.call(el, value); else el.value = value;
  el.dispatchEvent(new Event('input', { bubbles: true }));
  el.dispatchEvent(new Event('change', { bubbles: true }));
}""",
                    value,
                )
            except Exception:
                pass
            actual = str(input_locator.input_value() or "").strip()
        if not actual.startswith(value):
            raise RuntimeError(
                "%s填写失败：输入「%s」，页面显示「%s」" % (description, value, actual)
            )

    def _fill_date_range(self, start_date: str, end_date: str) -> None:
        item = None
        try:
            item = self._find_form_item(
                ("创建日期", "创建时间", "日期范围", "时间范围"),
                "日期范围",
                wait_seconds=0.2,
            )
        except RuntimeError:
            item = None
        if item is not None:

            def inputs_ready():
                return (
                    item.locator("input:visible").count() >= 2
                    or item.locator("input").count() >= 2
                )

            self._wait_for_value(
                inputs_ready,
                min(self.timeout_ms, 3000.0),
                "日期范围输入框",
            )
            inputs = item.locator("input:visible")
            if inputs.count() < 2:
                inputs = item.locator("input")
            start_input = inputs.nth(0)
            end_input = inputs.nth(1)
            self._set_input_value(start_input, start_date, "开始日期")
            self._set_input_value(end_input, end_date, "结束日期")
            try:
                end_input.press("Enter")
            except Exception:
                pass
            self._wait_for_timeout(self.page, 100)
            return
        self._fill_date(("开始日期", "开始时间"), start_date, "开始日期")
        self._fill_date(("结束日期", "结束时间"), end_date, "结束日期")

    def apply_filters(self, settings: ReviewSettings) -> str:
        if not self._task_list_visible():
            raise RuntimeError("当前页面不是缺陷识别任务列表，无法应用筛选")
        self._ensure_monitor()
        self.check_cancel()
        self._select_form_option(("任务状态",), settings.task_status, "任务状态")
        self.check_cancel()
        self._select_form_option(("作业性质",), settings.mission_type, "作业性质")
        self.check_cancel()
        self._select_voltage(settings.voltage_level)
        self.check_cancel()
        line_name = self._select_line(settings.line_name)
        self.check_cancel()
        self._fill_date_range(settings.start_date, settings.end_date)
        self.log(
            "已设置筛选：任务状态=%s；作业性质=%s；电压等级=%s；线路=%s；"
            "开始=%s；结束=%s"
            % (
                settings.task_status,
                settings.mission_type,
                settings.voltage_level,
                line_name,
                settings.start_date,
                settings.end_date,
            )
        )
        return line_name

    def _find_query_button(self):
        buttons = self.page.locator("button:visible")
        for index in range(buttons.count()):
            button = buttons.nth(index)
            if _normalize_text(button.inner_text()) == QUERY_BUTTON_TEXT:
                if not button.is_enabled():
                    raise RuntimeError("「查询」按钮不可用")
                return button
        raise RuntimeError("未找到「查询」按钮")

    def _verify_query_conditions(self, response, settings: ReviewSettings) -> dict:
        params = _request_query_params(response)
        if not params:
            raise RuntimeError("无法读取任务查询请求参数，不能确认筛选条件已生效")
        start_key = end_key = None
        for candidate_start, candidate_end in (
            ("startTime", "endTime"),
            ("beginTime", "endTime"),
            ("startDate", "endDate"),
        ):
            if candidate_start in params and candidate_end in params:
                start_key, end_key = candidate_start, candidate_end
                break
        if start_key is None:
            raise RuntimeError(
                "任务查询请求缺少起止时间字段（startTime/endTime），"
                "不能确认日期筛选已生效"
            )
        expected_start = settings.start_date + " 00:00:00"
        expected_end = settings.end_date + " 23:59:59"
        actual_start = _normalize_datetime(params[start_key])
        actual_end = _normalize_datetime(params[end_key])
        if not actual_start.startswith(expected_start):
            raise RuntimeError(
                "查询开始时间与筛选不一致：预期「%s」，实际「%s」"
                % (expected_start, actual_start)
            )
        if not actual_end.startswith(expected_end):
            raise RuntimeError(
                "查询结束时间与筛选不一致：预期「%s」，实际「%s」"
                % (expected_end, actual_end)
            )
        flags = {
            "task_status": False,
            "mission_type": False,
            "line": False,
            "voltage": False,
        }
        expected_status = TASK_STATUS_CODES[settings.task_status]
        if "taskStatus" in params:
            actual_status = _normalize_text(params["taskStatus"])
            if actual_status != expected_status:
                raise RuntimeError(
                    "查询请求 taskStatus=%s，与筛选任务状态「%s」应有的 %s 不一致"
                    % (actual_status, settings.task_status, expected_status)
                )
            flags["task_status"] = True
        expected_mission = MISSION_TYPE_CODES[settings.mission_type]
        if "missionType" in params:
            actual_mission = _normalize_text(params["missionType"])
            if actual_mission != expected_mission:
                raise RuntimeError(
                    "查询请求 missionType=%s，与筛选作业性质「%s」应有的 %s 不一致"
                    % (
                        actual_mission,
                        settings.mission_type,
                        expected_mission,
                    )
                )
            flags["mission_type"] = True
        expected_line = self.selected_line_name or _normalize_text(settings.line_name)
        for line_key in ("lineName", "lineTowerName"):
            if line_key not in params:
                continue
            raw_line = params[line_key]
            actual_line = (
                _line_name_from_tower(raw_line)
                if line_key == "lineTowerName"
                else _normalize_text(raw_line)
            )
            if actual_line != expected_line:
                raise RuntimeError(
                    "查询请求 %s=%s，与筛选线路「%s」不一致"
                    % (line_key, actual_line, expected_line)
                )
            flags["line"] = True
        if "voltageLevel" in params:
            actual_voltage = _normalize_text(params["voltageLevel"])
            expected_code = VOLTAGE_LEVEL_CODES[settings.voltage_level]
            if actual_voltage not in (expected_code, settings.voltage_level):
                raise RuntimeError(
                    "查询请求 voltageLevel=%s，与筛选电压等级「%s」不一致"
                    % (actual_voltage, settings.voltage_level)
                )
            flags["voltage"] = True
        return flags

    def _verify_records_conditions(
        self, records: Sequence[object], settings: ReviewSettings, flags: dict
    ) -> None:
        expected_status = TASK_STATUS_CODES[settings.task_status]
        expected_mission = MISSION_TYPE_CODES[settings.mission_type]
        expected_line = self.selected_line_name or _normalize_text(settings.line_name)
        for index, item in enumerate(records):
            if not isinstance(item, dict):
                raise RuntimeError("任务记录第 %d 条不是对象" % (index + 1))
            actual_status = item.get("recognitionTaskStatus")
            if actual_status is not None:
                if _normalize_text(actual_status) != expected_status:
                    raise RuntimeError(
                        "任务记录第 %d 条 recognitionTaskStatus=%s，"
                        "与筛选任务状态「%s」不一致"
                        % (index + 1, actual_status, settings.task_status)
                    )
            elif not flags["task_status"]:
                raise RuntimeError(
                    "任务记录第 %d 条缺少 recognitionTaskStatus，"
                    "且查询请求未暴露任务状态，无法核对" % (index + 1)
                )
            actual_mission = item.get("missionType")
            if actual_mission is not None:
                if _normalize_text(actual_mission) != expected_mission:
                    raise RuntimeError(
                        "任务记录第 %d 条 missionType=%s，"
                        "与筛选作业性质「%s」不一致"
                        % (index + 1, actual_mission, settings.mission_type)
                    )
            elif not flags["mission_type"]:
                raise RuntimeError(
                    "任务记录第 %d 条缺少 missionType，"
                    "且查询请求未暴露作业性质，无法核对" % (index + 1)
                )
            raw_lines = item.get("lineTowerName")
            if isinstance(raw_lines, str):
                raw_lines = [raw_lines]
            if not isinstance(raw_lines, list) or not raw_lines:
                if not flags["line"]:
                    raise RuntimeError(
                        "任务记录第 %d 条缺少 lineTowerName，"
                        "且查询请求未暴露线路，无法核对" % (index + 1)
                    )
            else:
                for raw_line in raw_lines:
                    actual_line = _line_name_from_tower(raw_line)
                    if not actual_line:
                        if not flags["line"]:
                            raise RuntimeError(
                                "任务记录第 %d 条 lineTowerName 为空，"
                                "无法核对线路" % (index + 1)
                            )
                        continue
                    if actual_line != expected_line:
                        raise RuntimeError(
                            "任务记录第 %d 条线路「%s」与筛选线路「%s」不一致"
                            % (index + 1, actual_line, expected_line)
                        )
            record_guid = _normalize_text(item.get("lineGuid"))
            if (
                self.selected_line_guid
                and record_guid
                and record_guid != self.selected_line_guid
            ):
                raise RuntimeError(
                    "任务记录第 %d 条 lineGuid=%s，与所选线路不一致"
                    % (index + 1, record_guid)
                )

    def query_tasks(self, settings: ReviewSettings):
        self._ensure_monitor()
        button = self._find_query_button()
        mark = len(self.monitor.responses)
        self._dom_click(button)
        payload, response = self.monitor.wait_success(
            TASK_PAGE_API, mark, self.timeout_ms, "缺陷识别任务列表"
        )
        flags = self._verify_query_conditions(response, settings)
        records, total, current, pages = parse_task_page(payload)
        self._verify_records_conditions(records, settings, flags)
        self.log(
            "查询完成：第 %d/%d 页，本页 %d 条，总数 %d"
            % (current, pages, len(records), total)
        )
        return records, total, current, pages

    def _goto_page(self, settings: ReviewSettings, target_page: int):
        pager = self.page.locator(".el-pagination:visible").first
        if not pager.count():
            raise RuntimeError("未找到分页器，无法跳到第 %d 页" % target_page)
        jump_container = pager.locator(".el-pagination__jump").first
        if not jump_container.count():
            raise RuntimeError(
                "未找到分页跳转输入框，无法直接跳到第 %d 页" % target_page
            )
        jump_input = jump_container.locator("input").first
        if not jump_input.count():
            raise RuntimeError(
                "未找到分页跳转输入框，无法直接跳到第 %d 页" % target_page
            )
        mark = len(self.monitor.responses)
        jump_input.fill(str(target_page))
        jump_input.press("Enter")
        payload, response = self.monitor.wait_success(
            TASK_PAGE_API,
            mark,
            self.timeout_ms,
            "任务列表第 %d 页" % target_page,
        )
        flags = self._verify_query_conditions(response, settings)
        records, total, current, pages = parse_task_page(payload)
        if current != target_page:
            raise RuntimeError(
                "尝试跳到第 %d 页后响应仍为第 %d 页，停止扫描" % (target_page, current)
            )
        self._verify_records_conditions(records, settings, flags)
        self.log("已直接跳到任务列表第 %d 页" % current)
        return records, total, current, pages

    def _goto_first_page(self, settings: ReviewSettings):
        records, total, current, pages = self._goto_page(settings, 1)
        return records, total, current, pages

    def _next_page(self, settings: ReviewSettings, expected_current: int):
        # 用分页跳转输入框直接到目标页；逐页点「下一页」在任务消失、页码前移时容易漏任务。
        records, total, current, pages = self._goto_page(settings, expected_current)
        if current != expected_current:
            raise RuntimeError(
                "翻页后页码不一致：预期第 %d 页，响应为第 %d 页"
                % (expected_current, current)
            )
        return records, total, current, pages

    def _visible_task_table(self):
        tables = self.page.locator(".el-table:visible")
        for index in range(tables.count()):
            table = tables.nth(index)
            header_locator = table.locator(".el-table__header").first
            if not header_locator.count():
                continue
            header = _normalize_text(header_locator.inner_text())
            if TASK_TABLE_NAME_HEADER in header:
                return table
        raise RuntimeError("未找到包含「任务名称」的缺陷识别任务表格")

    def _task_rows(self, table):
        body = table.locator(".el-table__body-wrapper").first
        if not body.count():
            body = table
        return body.locator("tbody tr.el-table__row")

    def _rows_for_records(self, records: Sequence[object]) -> list:
        table = self._visible_task_table()
        rows = self._task_rows(table)
        if rows.count() != len(records):
            raise RuntimeError(
                "任务表格行数与接口不一致：DOM %d，接口 %d"
                % (rows.count(), len(records))
            )
        result = []
        for index, item in enumerate(records):
            name = ""
            if isinstance(item, dict):
                name = _normalize_text(item.get("taskName"))
            if not name:
                result.append(None)
                continue
            actual = _normalize_text(rows.nth(index).inner_text())
            if name not in actual:
                raise RuntimeError(
                    "任务行校验失败：第 %d 行预期「%s」，实际「%s」"
                    % (index + 1, name, actual)
                )
            result.append(rows.nth(index))
        return result

    def _wait_rows_for_records(self, records: Sequence[object]) -> list:
        last_error = None
        for attempt in range(5):
            try:
                return self._rows_for_records(records)
            except RuntimeError as exc:
                last_error = exc
                if attempt == 4:
                    break
                self.page.wait_for_timeout(200)
        raise last_error

    def _is_effectively_enabled(self, element, check_ancestors: bool = True) -> bool:
        node = element
        for _depth in range(8 if check_ancestors else 1):
            try:
                if not node.count():
                    return False
                if node.get_attribute("disabled") is not None:
                    return False
                aria_disabled = node.get_attribute("aria-disabled")
                if (
                    aria_disabled is not None
                    and str(aria_disabled).strip().lower() == "true"
                ):
                    return False
                if _has_disabled_class(node.get_attribute("class")):
                    return False
            except Exception:
                return False
            parent = node.locator("xpath=..").first
            if not parent.count():
                break
            node = parent
        return True

    def _enter_button(self, row):
        if row is None:
            return None
        candidates = []
        for selector in ("button", '[role="button"]'):
            locator = row.locator(selector)
            for index in range(locator.count()):
                element = locator.nth(index)
                if _normalize_text(element.inner_text()) == "进入":
                    candidates.append(element)
        locator = row.locator(TASK_ENTER_SELECTOR)
        for index in range(locator.count()):
            candidates.append(locator.nth(index))
        locator = row.get_by_text("进入", exact=True)
        for index in range(locator.count()):
            candidates.append(locator.nth(index))
        for button in candidates:
            try:
                if (
                    button.is_visible()
                    and button.is_enabled()
                    and self._is_effectively_enabled(button)
                ):
                    return button
            except Exception:
                continue
        return None

    def enter_task(self, entry: dict) -> None:
        item = entry.get("item") or {}
        row = entry.get("row")
        if not isinstance(item, dict) or row is None:
            raise RuntimeError("任务候选缺少可核对的行信息，已阻止点击进入")
        name = _normalize_text(item.get("taskName"))
        actual = _normalize_text(row.inner_text())
        if not name or name not in actual:
            raise RuntimeError("任务行校验失败，已阻止误点其他任务")
        button = self._enter_button(row)
        if button is None:
            raise RuntimeError("任务「%s」没有可见且可用的「进入」按钮" % name)
        self._dom_click(button)
        self.log("已进入任务《%s》" % name)

    def scan_task_list(
        self,
        settings: ReviewSettings,
        excluded_ids: Optional[Iterable[object]] = None,
        stop_at_first: bool = False,
        start_page: Optional[int] = None,
    ):
        excluded = _id_set(excluded_ids)
        records, total, current, pages = self.query_tasks(settings)
        target_page = 1 if start_page is None else max(1, int(start_page))
        target_page = min(target_page, max(1, pages))
        if current != target_page:
            if target_page == 1:
                self.log("查询返回第 %d 页，回到第 1 页后扫描" % current)
                records, total, current, pages = self._goto_first_page(settings)
            else:
                self.log(
                    "查询返回第 %d 页，直接跳到第 %d 页继续扫描"
                    % (current, target_page)
                )
                records, total, current, pages = self._goto_page(settings, target_page)
        page_number = current
        candidates = []
        summary = {
            "total": total,
            "pages": pages,
            "scanned": 0,
            "candidates": 0,
            "skipped": 0,
        }
        seen = 0
        while True:
            self.check_cancel()
            rows = self._wait_rows_for_records(records)
            for index, item in enumerate(records):
                seen += 1
                if not isinstance(item, dict):
                    summary["skipped"] += 1
                    continue
                candidate_item = next_task_candidate([item], excluded, excluded)
                if candidate_item is None:
                    continue
                guid = str(candidate_item.get(TASK_GUID_FIELD) or "").strip()
                row = rows[index]
                if row is None:
                    summary["skipped"] += 1
                    continue
                button = self._enter_button(row)
                if button is None:
                    summary["skipped"] += 1
                    continue
                candidates.append(
                    {
                        "item": item,
                        "guid": guid,
                        "page": page_number,
                        "index": index,
                        "row": row,
                        "button": button,
                    }
                )
                summary["candidates"] += 1
                if stop_at_first:
                    summary["scanned"] = seen
                    return candidates, summary
            self.log(
                "扫描第 %d 页：本页 %d 条，累计 %d/%d"
                % (page_number, len(records), seen, total)
            )
            if page_number >= pages or seen >= total:
                break
            if not records:
                raise RuntimeError("任务列表未到总数就出现空页，停止以避免漏任务")
            records, new_total, new_current, new_pages = self._next_page(
                settings, page_number + 1
            )
            if new_total != total:
                raise RuntimeError("翻页期间任务总数发生变化，请停止人工操作后重试")
            total = new_total
            pages = new_pages
            page_number = new_current
        summary["scanned"] = seen
        summary["total"] = total
        summary["pages"] = pages
        return candidates, summary

    def iter_task_candidates(
        self,
        settings: ReviewSettings,
        processed_ids: Optional[Iterable[object]] = None,
        skipped_ids: Optional[Iterable[object]] = None,
        rescan: bool = True,
    ):
        excluded = _id_set(processed_ids) | _id_set(skipped_ids)
        if not rescan:
            candidates, summary = self.scan_task_list(settings, excluded)
            self.last_scan_summary = summary
            for entry in candidates:
                yield entry
            return summary
        handled = set()
        start_page = None
        while True:
            self.check_cancel()
            candidates, summary = self.scan_task_list(
                settings,
                excluded | handled,
                stop_at_first=True,
                start_page=start_page,
            )
            self.last_scan_summary = summary
            if not candidates:
                return summary
            entry = candidates[0]
            handled.add(entry["guid"])
            # 下一轮从刚处理过的页继续；完成任务后后续页会前移
            #（如第 3 页变第 2 页、第 4 页变第 3 页），直接跳回该页即可。
            start_page = entry.get("page")
            yield entry

    def _iter_resilient_candidates(self, settings: ReviewSettings):
        """扫描任务列表；任何扫描异常都刷新网页后继续，不终止整轮。"""
        handled = set()
        while True:
            try:
                for entry in self.iter_task_candidates(
                    settings, processed_ids=handled
                ):
                    guid = _normalize_text((entry or {}).get("guid"))
                    if guid:
                        handled.add(guid)
                    yield entry
                return
            except Exception as exc:
                if self.stop_requested:
                    raise
                self.log(
                    "扫描任务列表异常：%s；刷新网页后继续"
                    % _redact_urls(exc)[:300]
                )
                self._refresh_and_resume(settings, "任务列表异常后")

    def _find_task_entry(
        self, settings: ReviewSettings, task_guid: str, page_hint=None
    ):
        target = _normalize_text(task_guid)
        if not target:
            return None
        hints = []
        try:
            hint = int(page_hint or 1)
        except (TypeError, ValueError):
            hint = 1
        hints.append(max(1, hint))
        if 1 not in hints:
            hints.append(1)
        for start_page in hints:
            candidates, _summary = self.scan_task_list(
                settings, start_page=start_page
            )
            for candidate in candidates:
                if _normalize_text(candidate.get("guid")) == target:
                    return candidate
        return None

    def _drop_task_abort_anomalies(self, task_guid: str) -> None:
        target = _normalize_text(task_guid)
        if not target:
            return
        kept = []
        removed = False
        for row in self.anomaly_rows:
            if (
                len(row) > 4
                and _normalize_text(row[4]) == target
                and row[1] in ("审核中止", "未知异常")
            ):
                removed = True
                continue
            kept.append(row)
        if not removed:
            return
        self.anomaly_rows = kept
        self.anomaly_keys = {
            (
                _normalize_text(row[4]),
                row[1],
                _normalize_text(row[12]) if len(row) > 12 else "",
                row[2],
            )
            for row in self.anomaly_rows
        }
        self.anomaly_dirty = True
        self.log("重试成功，已移除该任务的审核中止异常记录")

    def _wait_for_value(
        self,
        predicate,
        timeout_ms: float,
        description: str,
        timeout_exception=ReviewStopped,
        interval_ms: float = None,
    ):
        deadline = time.monotonic() + max(1.0, float(timeout_ms)) / 1000.0
        slice_ms = (
            _CANCEL_WAIT_SLICE_MS if interval_ms is None else max(1.0, interval_ms)
        )
        while True:
            self.check_cancel()
            value = predicate()
            if value:
                return value
            if self.page is not None and self.page.is_closed():
                raise ReviewStopped("等待%s时页面已关闭" % description)
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise timeout_exception(
                    "等待%s超时（%.1f 秒）"
                    % (description, max(1.0, float(timeout_ms)) / 1000.0)
                )
            if self.page is None:
                time.sleep(min(slice_ms / 1000.0, remaining))
            else:
                self._wait_for_timeout(
                    self.page, int(min(slice_ms, remaining * 1000.0))
                )

    def _reset_review_state(self) -> None:
        self.current_dialog = None
        self.reviewed_guids = set()
        self.reviewed_count = 0
        self.task_image_total = 0
        self.task_images = []

    def _collect_tagging_details(
        self, picture_guid: str, start_mark: int, timeout_ms: float = None
    ) -> list:
        """读取当前图片的系统缺陷框明细；取不到时返回空列表，不阻断审核。"""
        if self.monitor is None:
            return []
        wanted = picture_guid.casefold()
        start = max(0, int(start_mark))

        def lookup():
            # 最新响应优先，避免长任务里每张图都从头扫描全部响应。
            for response in reversed(self.monitor.responses):
                if not self.monitor._matches_page(response):
                    continue
                if not _is_api(response.url, TAGGING_DETAIL_API):
                    continue
                try:
                    params = parse_qs(urlsplit(response.url).query)
                except ValueError:
                    continue
                guid = _normalize_text(params.get("pictureDetailGuid", [""])[0])
                if guid.casefold() != wanted:
                    continue
                if response.status != 200:
                    return True, []
                try:
                    payload = response.json()
                except Exception:
                    return True, []
                try:
                    data = parse_api_data(payload, "系统缺陷框明细")
                except RuntimeError:
                    return True, []
                return True, data if isinstance(data, list) else []
            return None

        found = lookup()
        if found is not None:
            return found[1]
        wait_ms = (
            self.tagging_detail_timeout_ms
            if timeout_ms is None
            else max(0.0, float(timeout_ms))
        )
        if wait_ms <= 0:
            return []
        try:
            found = self._wait_for_value(
                lookup,
                wait_ms,
                "图片 %s 的系统缺陷框明细" % picture_guid,
                RuntimeError,
            )
        except RuntimeError:
            return []
        return found[1] if found else []

    def _unreviewed_count_from_dialog(self, dialog) -> int:
        try:
            text = _normalize_text(dialog.inner_text())
        except Exception:
            text = ""
        match = re.search(r"未审核\s*(\d+)", text)
        if match:
            return int(match.group(1))
        return len([image for image in self.task_images if is_unreviewed_image(image)])

    def _task_line_info(self, item: dict, images: Sequence[ReviewImage]):
        raw_lines = item.get("lineTowerName")
        if isinstance(raw_lines, str):
            raw_lines = [raw_lines]
        if not isinstance(raw_lines, list):
            raw_lines = []
        line_towers = [
            _normalize_text(value) for value in raw_lines if _normalize_text(value)
        ]
        line_name = ""
        tower_name = ""
        for image in images:
            if not line_name and image.line_name:
                line_name = image.line_name
            if not tower_name and image.tower_name:
                tower_name = image.tower_name
        for value in line_towers:
            parts = re.split(r"[:：]", value, maxsplit=1)
            if not line_name:
                line_name = parts[0].strip()
            if not tower_name and len(parts) > 1:
                tower_name = parts[1].strip()
        line_tower = "、".join(line_towers)
        if not line_tower:
            if line_name and tower_name:
                line_tower = "%s：%s" % (line_name, tower_name)
            else:
                line_tower = line_name or tower_name
        return line_name, line_tower, tower_name

    def _record_system_defect(
        self,
        task_info: dict,
        settings: ReviewSettings,
        image: ReviewImage,
        details: list,
    ) -> None:
        if image.picture_guid in self.system_defect_guids:
            return
        self.system_defect_guids.add(image.picture_guid)
        summary = summarize_tagging_details(details)
        row = [
            settings.voltage_level,
            task_info.get("line_name", ""),
            task_info.get("line_tower", ""),
            task_info.get("tower_name", ""),
            task_info.get("task_name", ""),
            task_info.get("work_order_no", ""),
            image.original_name,
            image.shoot_time,
            summary["description"],
            summary["level"],
            summary["coordinate"],
            summary["algorithm"],
            task_info.get("task_guid", ""),
            image.picture_guid,
            "已跳过，待人工审核",
        ]
        self.system_defect_rows.append(row)
        self.system_defect_dirty = True
        self.log(
            "已登记系统缺陷框图片：%s（%s %s）"
            % (
                image.original_name or image.picture_guid,
                task_info.get("line_tower", ""),
                summary["description"],
            )
        )

    def _write_summary_xlsx(self, force: bool = False) -> None:
        if not force and not self.system_defect_dirty and not self.anomaly_dirty:
            return
        try:
            self.outdir.mkdir(parents=True, exist_ok=True)
            counts = write_workbook_xlsx(
                self.summary_xlsx_path,
                (
                    (DEFECT_SHEET_NAME, SYSTEM_DEFECT_HEADERS, self.system_defect_rows),
                    (ANOMALY_SHEET_NAME, ANOMALY_HEADERS, self.anomaly_rows),
                ),
                log=self.log,
            )
            self.system_defect_dirty = False
            self.anomaly_dirty = False
            if self.last_run_summary is not None:
                self.last_run_summary["summary_xlsx"] = str(self.summary_xlsx_path)
                self.last_run_summary["defect_rows"] = counts.get(DEFECT_SHEET_NAME, 0)
                self.last_run_summary["anomaly_rows"] = counts.get(
                    ANOMALY_SHEET_NAME, 0
                )
            self.log(
                "审核汇总已写入：%s（缺陷 %d 条，异常 %d 条）"
                % (
                    self.summary_xlsx_path,
                    counts.get(DEFECT_SHEET_NAME, 0),
                    counts.get(ANOMALY_SHEET_NAME, 0),
                )
            )
        except Exception as exc:
            self.log("审核汇总写入失败：%s" % str(exc)[:200])

    def _init_output_skip_file(self) -> None:
        """在输出目录生成可被 --skip-tasks-file 直接复用的跳过任务表。"""
        try:
            self.outdir.mkdir(parents=True, exist_ok=True)
            if (
                not self.output_skip_path.exists()
                or self.output_skip_path.stat().st_size == 0
            ):
                self.output_skip_path.write_text(
                    SKIP_TASK_HEADER + "\n", encoding="utf-8"
                )
        except Exception as exc:
            self.log("输出目录跳过任务表初始化失败：%s" % str(exc)[:200])

    def _record_output_skip_task(self, task_name: str) -> None:
        """把缺陷/异常任务名追加到输出目录的「跳过任务.txt」，逐条落盘。"""
        name = _normalize_text(task_name)
        if not name:
            return
        folded = name.casefold()
        if folded in self.output_skip_names:
            return
        if folded in load_skip_task_names(self.output_skip_path):
            self.output_skip_names.add(folded)
            return
        self.output_skip_names.add(folded)
        try:
            self._init_output_skip_file()
            with self.output_skip_path.open("a", encoding="utf-8") as handle:
                handle.write(name + "\n")
            self.log("已写入输出目录跳过任务表：%s" % name)
        except Exception as exc:
            self.log("跳过任务表写入失败：%s" % str(exc)[:200])

    def _record_anomaly(
        self,
        task_info: dict,
        settings: ReviewSettings,
        kind: str,
        reason: str,
        image: Optional[ReviewImage] = None,
        result: str = "待人工处理",
        unreviewed: Optional[int] = None,
    ) -> None:
        task_info = task_info if isinstance(task_info, dict) else {}
        task_guid = _normalize_text(task_info.get("task_guid"))
        picture_guid = image.picture_guid if image is not None else ""
        key = (task_guid, kind, picture_guid, reason)
        if key in self.anomaly_keys:
            return
        self.anomaly_keys.add(key)
        count = (
            int(unreviewed)
            if unreviewed is not None
            else int(task_info.get("unreviewed_count", 0) or 0)
        )
        row = [
            datetime.now().strftime("%Y-%m-%d %H:%M:%S"),
            kind,
            reason,
            task_info.get("task_name", ""),
            task_guid,
            task_info.get("work_order_no", ""),
            settings.voltage_level if settings is not None else "",
            task_info.get("line_name", ""),
            task_info.get("line_tower", ""),
            task_info.get("tower_name", ""),
            image.original_name if image is not None else "",
            image.shoot_time if image is not None else "",
            picture_guid,
            count,
            result,
        ]
        self.anomaly_rows.append(row)
        self.anomaly_dirty = True
        self.log("已登记异常：%s（%s）" % (kind, reason))

    def _record_image_failure(
        self,
        task_info: dict,
        settings: ReviewSettings,
        image: ReviewImage,
        reason: str,
    ) -> None:
        self._record_anomaly(
            task_info,
            settings,
            "图片加载失败",
            reason,
            image=image,
            result="已跳过，待人工审核",
        )

    def _record_button_not_ready(
        self,
        task_info: dict,
        settings: ReviewSettings,
        image: ReviewImage,
        reason: str,
    ) -> None:
        self._record_anomaly(
            task_info,
            settings,
            "按钮未就绪",
            reason,
            image=image,
            result="已跳过，待人工审核",
        )

    def _has_review_complete_button(self, dialog) -> bool:
        return (
            self._find_visible_button(dialog, REVIEW_BUTTON_TEXT, require_enabled=False)
            is not None
        )

    # 弹层顶部加载蒙层探测。Element UI 的 v-loading 会把 .el-loading-mask
    # 插进被加载容器：首图遮罩只包住主图，弹层级遮罩会包住首图遮罩、或与
    # 「审核完成」按钮同容器/盖住按钮。只等后者消失，避免把首图加载当成
    # 弹层未加载（曾因此卡满 120 秒）。
    def _button_blocked_by_loading(self, button) -> bool:
        script = """(el) => {
  // shade-review-loading-probe
  const dialog = el.closest('.el-dialog') || el.ownerDocument;
  const shown = (node) => {
    const rect = node.getBoundingClientRect();
    if (!rect.width || !rect.height) return false;
    const style = getComputedStyle(node);
    return style.display !== 'none' && style.visibility !== 'hidden';
  };
  const masks = Array.from(
    dialog.querySelectorAll('.el-loading-mask')
  ).filter(shown);
  for (const outer of masks) {
    const parent = outer.parentElement;
    if (!parent) continue;
    if (masks.some((other) => other !== outer && parent.contains(other))) {
      return true;
    }
  }
  for (const mask of masks) {
    const parent = mask.parentElement;
    if (parent && parent.contains(el)) return true;
  }
  const rect = el.getBoundingClientRect();
  if (rect.width && rect.height) {
    const hit = document.elementFromPoint(
      rect.left + rect.width / 2, rect.top + rect.height / 2
    );
    if (
      hit && hit.closest &&
      hit.closest('.el-loading-mask, .el-loading-spinner')
    ) {
      return true;
    }
  }
  return false;
}"""
        try:
            return bool(button.evaluate(script))
        except Exception:
            return False

    def _dialog_loading_visible(self, dialog) -> bool:
        """弹层顶部加载蒙层是否仍遮挡审核区域；复用审核按钮遮挡探针。"""
        for text in (REVIEW_BUTTON_TEXT, "完成任务"):
            button = self._find_visible_button(dialog, text, require_enabled=False)
            if button is not None:
                return self._button_blocked_by_loading(button)
        return False

    def _wait_dialog_loading_gone(self, dialog) -> None:
        """缩略图切换会触发弹层加载蒙层；等它消失后再识别当前图片/进入审核。"""
        timeout_ms = max(5000.0, min(float(self.timeout_ms), 30000.0))
        self._wait_for_value(
            lambda: not self._dialog_loading_visible(dialog),
            timeout_ms,
            "缩略图切换后的弹层加载蒙层",
            interval_ms=100,
        )

    def _find_ready_review_button(self, dialog):
        """返回可用且未被弹层加载蒙层遮挡的「审核完成」按钮。"""
        button = self._find_visible_button(
            dialog,
            REVIEW_BUTTON_TEXT,
            require_enabled=True,
            check_ancestors=False,
        )
        if button is None:
            return None
        if self._button_blocked_by_loading(button):
            return None
        return button

    def _review_button_ready(self, dialog) -> bool:
        return self._find_ready_review_button(dialog) is not None

    def _find_visible_button(
        self,
        container,
        text: str,
        require_enabled: bool = True,
        check_ancestors: bool = True,
    ):
        try:
            buttons = container.locator("button")
        except Exception:
            return None
        for index in range(buttons.count()):
            button = buttons.nth(index)
            try:
                if text not in _normalize_text(button.inner_text()):
                    continue
                if not button.is_visible():
                    continue
                if require_enabled:
                    if not button.is_enabled():
                        continue
                    if not self._is_effectively_enabled(button, check_ancestors):
                        continue
                return button
            except Exception:
                continue
        return None

    def _review_dialog(self):
        if self.page is None or self.page.is_closed():
            return None
        try:
            dialogs = self.page.locator(".el-dialog:visible")
        except Exception:
            return None
        for index in range(dialogs.count() - 1, -1, -1):
            dialog = dialogs.nth(index)
            if (
                self._find_visible_button(
                    dialog, REVIEW_BUTTON_TEXT, require_enabled=False
                )
                is not None
                or self._find_visible_button(dialog, "完成任务", require_enabled=False)
                is not None
            ):
                return dialog
        return None

    def _wait_review_dialog(self):
        return self._wait_for_value(
            self._review_dialog, self.timeout_ms, "任务审核弹窗"
        )

    def _dialog_load_state(self, dialog) -> str:
        """加载超时时输出判定依据，便于区分遮罩未消失、弹窗关闭或内容缺失。"""
        parts = []
        try:
            parts.append("可见=%s" % dialog.is_visible())
        except Exception:
            parts.append("可见=读取失败")
        try:
            parts.append("加载遮罩=%d" % dialog.locator(".el-loading-mask:visible").count())
        except Exception:
            parts.append("加载遮罩=读取失败")
        text = self._container_text(dialog)
        match = re.search(r"未审核\s*\d+", text)
        parts.append("未审核文案=%s" % (match.group(0) if match else "无"))
        try:
            parts.append(
                "任务名匹配=%s"
                % (
                    "有"
                    if self._dialog_matches_task(
                        dialog, self.current_task_name, text
                    )
                    else "无"
                )
            )
        except Exception:
            parts.append("任务名匹配=读取失败")
        try:
            parts.append("图片=%d" % dialog.locator("img").count())
        except Exception:
            parts.append("图片=读取失败")
        try:
            parts.append(
                "本任务缩略图=%s"
                % ("有" if self._dialog_shows_task_images(dialog) else "无")
            )
        except Exception:
            parts.append("本任务缩略图=读取失败")
        for text in ("完成任务", REVIEW_BUTTON_TEXT):
            try:
                present = (
                    "有"
                    if self._find_visible_button(
                        dialog, text, require_enabled=False
                    )
                    is not None
                    else "无"
                )
            except Exception:
                present = "读取失败"
            parts.append("%s按钮=%s" % (text, present))
        try:
            probe_button = self._find_visible_button(
                dialog, REVIEW_BUTTON_TEXT, require_enabled=False
            )
            blocked = probe_button is not None and self._button_blocked_by_loading(
                probe_button
            )
            parts.append("审核按钮被顶部蒙层遮挡=%s" % ("是" if blocked else "否"))
        except Exception:
            parts.append("审核按钮被顶部蒙层遮挡=读取失败")
        return "；".join(parts)

    def _dialog_shows_task_images(self, dialog) -> bool:
        """弹层是否已出现当前任务的缩略图（按 fileGuid 核对，避免读到上一任务残留）。

        现场缩略图由 XHR 拉取二进制后渲染，img.src 可能是 blob/data 而不含
        fileGuid；因此这里只作辅助信号，主判据是任务名 + 未审核文案。
        """
        wanted = {
            _normalize_text(image.file_guid).casefold()
            for image in self.task_images
            if _normalize_text(image.file_guid)
        }
        if not wanted:
            return False
        try:
            values = dialog.evaluate(
                """(el) => Array.from(el.querySelectorAll('img')).flatMap((img) => [
  img.currentSrc || img.src || '',
  img.getAttribute('data-file-guid') || '',
  img.getAttribute('data-fileguid') || '',
  img.getAttribute('data-guid') || '',
])"""
            )
        except Exception:
            values = None
        if isinstance(values, list):
            for value in values:
                folded = str(value or "").casefold()
                if folded and any(guid in folded for guid in wanted):
                    return True
            return False
        # 测试桩不支持批量读取时逐个属性兜底。
        try:
            images = dialog.locator("img")
        except Exception:
            return False
        for index in range(images.count()):
            image = images.nth(index)
            source = self._element_source(image).casefold()
            if source and any(guid in source for guid in wanted):
                return True
            for attribute in ("data-file-guid", "data-fileguid", "data-guid"):
                try:
                    value = _normalize_text(image.get_attribute(attribute)).casefold()
                except Exception:
                    value = ""
                if value and value in wanted:
                    return True
        return False

    def _dialog_matches_task(self, dialog, task_name: str, text: str = None) -> bool:
        """弹层是否属于当前任务：核对任务名称（文本或 input/textarea 值）。"""
        wanted = _normalize_text(task_name)
        if not wanted:
            return False
        try:
            values = dialog.evaluate(
                """(el) => Array.from(el.querySelectorAll('input, textarea')).map(
  (node) => node.value || ''
)"""
            )
        except Exception:
            values = None
        if isinstance(values, list):
            for value in values:
                if wanted in _normalize_text(value):
                    return True
        if text is None:
            text = self._container_text(dialog)
        return wanted in text

    def _wait_task_dialog_loaded(self, dialog, task_name: str = ""):
        """点击进入任务后，等内容渲染且顶部弹层加载蒙层消失再开始审核。"""
        started = time.monotonic()
        last_notice = [0.0]
        mask_notice = [False]

        def loaded():
            elapsed = time.monotonic() - started
            if elapsed - last_notice[0] >= 30.0:
                last_notice[0] = elapsed
                self.log(
                    "仍在等待任务弹层就绪，已 %.0f 秒：%s"
                    % (elapsed, self._dialog_load_state(dialog))
                )
            if not dialog.is_visible():
                return False
            text = self._container_text(dialog)
            has_count = re.search(r"未审核\s*\d+", text) is not None
            # 弹层已显示当前任务名称且渲染出未审核数，或出现本任务（按 API
            # fileGuid 核对）的缩略图，即说明数据已渲染。
            content_ready = False
            if has_count:
                try:
                    content_ready = self._dialog_matches_task(dialog, task_name, text)
                except Exception:
                    content_ready = False
            if not content_ready:
                try:
                    content_ready = self._dialog_shows_task_images(dialog)
                except Exception:
                    content_ready = False
            if not content_ready:
                # 拿不到任务名称时，遮罩才作为“弹层数据仍是上一任务残留”的兜底。
                try:
                    if dialog.locator(".el-loading-mask:visible").count():
                        return False
                except Exception:
                    pass
                content_ready = has_count or (
                    self._find_visible_button(dialog, "完成任务", require_enabled=False)
                    is not None
                    or self._find_visible_button(
                        dialog, REVIEW_BUTTON_TEXT, require_enabled=False
                    )
                    is not None
                )
            if not content_ready:
                return False
            # 内容已渲染，但顶部弹层加载蒙层可能仍在：它包住首图遮罩，或与
            # 「审核完成」/「完成任务」按钮同容器、盖住按钮。首图自身的图片
            # 加载遮罩不满足该条件，交后续 image_timeout/补审处理。
            probe_button = self._find_visible_button(
                dialog, REVIEW_BUTTON_TEXT, require_enabled=False
            ) or self._find_visible_button(dialog, "完成任务", require_enabled=False)
            if probe_button is not None and self._button_blocked_by_loading(
                probe_button
            ):
                if not mask_notice[0]:
                    mask_notice[0] = True
                    self.log("任务弹层内容已渲染，等待顶部加载蒙层消失")
                return False
            return True

        try:
            self._wait_for_value(loaded, self.timeout_ms, "任务弹层加载完成")
        except ReviewStopped as exc:
            if self.page is not None and not self.page.is_closed():
                exc.shade_dialog_unresponsive = True
                self.log("任务弹层加载超时诊断：%s" % self._dialog_load_state(dialog))
            raise
        try:
            if dialog.locator(".el-loading-mask:visible").count():
                self.log("顶部弹层加载已结束，首图加载遮罩仍在（交单图流程处理）")
        except Exception:
            pass
        self.log("任务弹层加载完成")
        return dialog

    def _wait_review_button(self, dialog):
        timeout_ms = max(5000.0, min(float(self.image_timeout_ms), 30000.0))
        return self._wait_for_value(
            lambda: self._find_ready_review_button(dialog),
            timeout_ms,
            "「审核完成」按钮（未被弹层加载蒙层遮挡）",
            ReviewButtonUnavailable,
            interval_ms=50,
        )

    def _visible_message_box(self):
        if self.page is None or self.page.is_closed():
            return None
        try:
            boxes = self.page.locator(".el-message-box:visible")
        except Exception:
            return None
        if not boxes.count():
            return None
        return boxes.last

    def _element_source(self, element) -> str:
        try:
            return str(element.get_attribute("src") or "")
        except Exception:
            return ""

    def _is_thumbnail_image(self, element, source: str) -> bool:
        lowered_source = source.casefold()
        if "isthumb=yes" in lowered_source:
            return True
        try:
            classes = str(element.get_attribute("class") or "").casefold()
        except Exception:
            classes = ""
        return "thumb" in classes

    def _visible_main_images(self, dialog) -> list:
        result = []
        try:
            images = dialog.locator("img")
        except Exception:
            return result
        for index in range(images.count()):
            image = images.nth(index)
            try:
                if not image.is_visible():
                    continue
            except Exception:
                continue
            source = self._element_source(image)
            if not source or self._is_thumbnail_image(image, source):
                continue
            result.append((image, source))
        return result

    def _element_rendered(self, element) -> bool:
        try:
            return bool(
                element.evaluate("(el) => Boolean(el.complete && el.naturalWidth > 0)")
            )
        except Exception:
            return False

    def _has_active_evidence(self, element) -> bool:
        node = element
        for _depth in range(6):
            try:
                classes = str(node.get_attribute("class") or "")
                tokens = [token.casefold() for token in classes.split()]
                if any(
                    token
                    in (
                        "active",
                        "selected",
                        "current",
                        "is-active",
                        "is-selected",
                        "is-current",
                    )
                    or token.endswith("-active")
                    or token.endswith("-selected")
                    for token in tokens
                ):
                    return True
                if (
                    str(node.get_attribute("aria-selected") or "").strip().casefold()
                    == "true"
                ):
                    return True
                aria_current = node.get_attribute("aria-current")
                if aria_current is not None and str(
                    aria_current
                ).strip().casefold() not in ("", "false"):
                    return True
                if (
                    str(node.get_attribute("data-active") or "").strip().casefold()
                    == "true"
                ):
                    return True
            except Exception:
                pass
            try:
                parent = node.locator("xpath=..").first
            except Exception:
                break
            if not parent.count():
                break
            node = parent
        return False

    def _thumbnail_active(self, dialog, file_guid: str) -> bool:
        lowered_guid = file_guid.casefold()
        try:
            images = dialog.locator("img")
        except Exception:
            return False
        for index in range(images.count()):
            image = images.nth(index)
            try:
                if not image.is_visible():
                    continue
            except Exception:
                continue
            source = self._element_source(image)
            if not self._is_thumbnail_image(image, source):
                continue
            matched = lowered_guid in source.casefold()
            if not matched:
                for attribute in (
                    "data-file-guid",
                    "data-fileguid",
                    "data-guid",
                ):
                    try:
                        value = _normalize_text(image.get_attribute(attribute))
                    except Exception:
                        value = ""
                    if value and value.casefold() == lowered_guid:
                        matched = True
                        break
            if matched and self._has_active_evidence(image):
                return True
        return False

    def _image_list_item(self, dialog, image: ReviewImage):
        """按图片在 pictureListAll 中的顺序定位缩略图 li（真实页面兜底）。"""
        for selector in (
            ".manual-audit .img-list-wrap li",
            ".img-list-wrap li",
        ):
            try:
                items = dialog.locator(selector)
                if items.count():
                    return items.nth(image.index)
            except Exception:
                continue
        return None

    def _image_list_item_active(self, dialog, image: ReviewImage) -> bool:
        item = self._image_list_item(dialog, image)
        if item is None:
            return False
        try:
            target = item.locator(".box").first
            if not target.count():
                target = item
            return bool(target.is_visible() and self._has_active_evidence(target))
        except Exception:
            return False

    def _thumbnail_items(self, dialog):
        for selector in (
            ".manual-audit .img-list-wrap li",
            ".img-list-wrap li",
            "li",
        ):
            try:
                items = dialog.locator(selector)
                if items.count():
                    return items
            except Exception:
                continue
        return None

    def _thumbnail_items_available(self, dialog) -> bool:
        items = self._thumbnail_items(dialog)
        return bool(items is not None and items.count())

    def _thumbnail_has_checkmark(self, item) -> bool:
        try:
            if item.locator(".check").count():
                return True
        except Exception:
            pass
        try:
            node_classes = [str(item.get_attribute("class") or "")]
        except Exception:
            node_classes = []
        for selector in ("i", "svg", "span", "div", ".check", ".el-icon"):
            try:
                nodes = item.locator(selector)
            except Exception:
                continue
            for index in range(nodes.count()):
                try:
                    node_classes.append(
                        str(nodes.nth(index).get_attribute("class") or "")
                    )
                except Exception:
                    continue
        for classes in node_classes:
            for token in classes.casefold().split():
                if "check" in token and "uncheck" not in token:
                    return True
                if any(
                    word in token
                    for word in (
                        "success",
                        "complete",
                        "finished",
                        "audit-pass",
                    )
                ):
                    return True
        return False

    def _thumbnail_image(self, item, images: Sequence[ReviewImage]):
        containers = [item]
        try:
            box = item.locator(".box").first
            if box.count():
                containers.append(box)
        except Exception:
            pass
        for container in containers:
            try:
                imgs = container.locator("img")
            except Exception:
                continue
            for index in range(imgs.count()):
                img = imgs.nth(index)
                try:
                    source = str(img.get_attribute("src") or "")
                except Exception:
                    source = ""
                guid = ""
                try:
                    params = parse_qs(urlsplit(source).query)
                    guid = _normalize_text(params.get("fileGuid", [""])[0])
                except ValueError:
                    guid = ""
                if not guid:
                    for attribute in (
                        "data-file-guid",
                        "data-fileguid",
                        "data-guid",
                    ):
                        try:
                            guid = _normalize_text(img.get_attribute(attribute))
                        except Exception:
                            guid = ""
                        if guid:
                            break
                if not guid:
                    continue
                for image in images:
                    if image.file_guid.casefold() == guid.casefold():
                        return image
        return None

    def _current_image_from_dialog(self, dialog, images: Sequence[ReviewImage]):
        active_index = self._active_thumbnail_index(dialog)
        items = self._thumbnail_items(dialog)
        if items is not None and 0 <= active_index < items.count():
            active_image = self._thumbnail_image(items.nth(active_index), images)
            if active_image is not None:
                return active_image
        try:
            text = _normalize_text(dialog.inner_text())
        except Exception:
            text = ""
        for image in images:
            if image.original_name and image.original_name in text:
                return image
        for _image, source in reversed(self._visible_main_images(dialog)):
            try:
                params = parse_qs(urlsplit(source).query)
            except ValueError:
                continue
            guid = _normalize_text(params.get("fileGuid", [""])[0])
            if not guid:
                continue
            for image in images:
                if image.file_guid.casefold() == guid.casefold():
                    return image
        return None

    def _show_unreviewed_only(self, dialog) -> bool:
        for selector in (".el-checkbox", ".el-radio", ".el-checkbox-button"):
            try:
                controls = dialog.locator(selector)
            except Exception:
                continue
            for index in range(controls.count()):
                control = controls.nth(index)
                try:
                    if "已审核" not in _normalize_text(control.inner_text()):
                        continue
                    classes = str(control.get_attribute("class") or "").casefold()
                    checked = "is-checked" in classes or "checked" in classes
                    if not checked:
                        input_locator = control.locator("input").first
                        if input_locator.count():
                            try:
                                checked = bool(
                                    input_locator.evaluate(
                                        "(el) => Boolean(el.checked)"
                                    )
                                )
                            except Exception:
                                pass
                    if checked:
                        self._dom_click(control)
                        self._wait_for_timeout(self.page, 300)
                        self.log("已取消「已审核」筛选，仅显示未打勾图片")
                    return True
                except Exception:
                    continue
        self.log("未找到「已审核」筛选控件，按缩略图对勾逐个跳过")
        return False

    def _dom_click(self, target) -> None:
        """直接触发 DOM click，不移动鼠标、不滚动页面。"""
        try:
            target.evaluate("(el) => el.click()")
        except Exception:
            target.click()

    def _click_thumbnail_box(self, dialog, item) -> None:
        target = item
        try:
            box = item.locator(".box").first
            if box.count():
                target = box
            # 缩略图的 click 监听通常在 img 上；DOM click 不会像鼠标
            # 那样命中子元素，因此显式点 img。
            for container in (target, item):
                images = container.locator("img")
                if images.count():
                    target = images.first
                    break
        except Exception:
            pass
        self._dom_click(target)
        # 点击缩略图切换图片会触发弹层顶部加载蒙层：先给渲染一帧，
        # 等蒙层消失后再由调用方识别切换后的图片或进入审核。
        self._wait_for_timeout(self.page, 200)
        self._wait_dialog_loading_gone(dialog)

    def _active_thumbnail_index(self, dialog) -> int:
        items = self._thumbnail_items(dialog)
        if items is None:
            return -1
        for index in range(items.count()):
            item = items.nth(index)
            target = item
            try:
                box = item.locator(".box").first
                if box.count():
                    target = box
            except Exception:
                pass
            try:
                if self._has_active_evidence(target):
                    return index
            except Exception:
                continue
        return -1

    def _select_unreviewed_image(
        self,
        dialog,
        images: Sequence[ReviewImage],
        attempted_guids: set,
        check_marks: bool = True,
        start_index: int = 0,
        allowed_guids=None,
    ):
        items = self._thumbnail_items(dialog)
        if items is None:
            return None, 0
        for index in range(max(0, int(start_index)), items.count()):
            item = items.nth(index)
            try:
                if not item.is_visible():
                    continue
            except Exception:
                continue
            if check_marks and self._thumbnail_has_checkmark(item):
                self.log("缩略图第 %d 张已打勾，跳过选图" % (index + 1))
                continue
            image = self._thumbnail_image(item, images)
            if image is not None:
                if image.picture_guid in self.reviewed_guids:
                    self.log(
                        "图片 %s 本轮已审核通过，跳过选图"
                        % (image.original_name or image.picture_guid)
                    )
                    continue
                if not self.ignore_defect_boxes and is_system_defect_image(image):
                    self.log(
                        "图片 %s 属于系统缺陷框，跳过选图"
                        % (image.original_name or image.picture_guid)
                    )
                    continue
                if image.picture_guid in attempted_guids:
                    self.log(
                        "图片 %s 本轮已失败/跳过，跳过选图"
                        % (image.original_name or image.picture_guid)
                    )
                    continue
                if (
                    allowed_guids is not None
                    and image.picture_guid not in allowed_guids
                ):
                    continue
                if check_marks and not is_unreviewed_image(image):
                    self.log(
                        "图片 %s 不是未打勾图片，跳过选图"
                        % (image.original_name or image.picture_guid)
                    )
                    continue
                mark = len(self.monitor.responses) if self.monitor else 0
                self.log("点击缩略图：%s" % (image.original_name or image.picture_guid))
                try:
                    self._click_thumbnail_box(dialog, item)
                except ReviewStopped:
                    raise
                except Exception as exc:
                    if self.stop_requested:
                        raise
                    raise ReviewStopped(
                        "点击图片 %s 的缩略图失败：%s"
                        % (image.picture_guid, str(exc)[:200])
                    ) from exc
                self.log(
                    "已切换到未打勾图片：%s"
                    % (image.original_name or image.picture_guid)
                )
                return image, mark
            # 缩略图没有 fileGuid 时不能按 DOM 序号直接映射到 images：页面
            # 可能已按「未审核」筛选或虚拟滚动，序号与接口顺序不再一致。
            # 这类缩略图一律先点击，再按切换后的真实图片身份判断是否跳过。
            if self._active_thumbnail_index(dialog) == index:
                # 已是当前图片，外层循环已判定它不可审核，重复点击不会切换。
                continue
            self.log("点击缩略图第 %d 张（无 fileGuid，尝试切换）" % (index + 1))
            mark = len(self.monitor.responses) if self.monitor else 0
            # 点击后标题/主图会滞后于缩略图高亮：先把点击前的当前图片记下来，
            # 只接受识别结果发生变化，避免把上一张已跳过图片当成这一张。
            before = self._current_image_from_dialog(dialog, images)
            try:
                self._click_thumbnail_box(dialog, item)
            except ReviewStopped:
                raise
            except Exception:
                if self.stop_requested:
                    raise
                continue

            def switched_image():
                current = self._current_image_from_dialog(dialog, images)
                if current is None:
                    return None
                if before is None:
                    return current
                if current.picture_guid == before.picture_guid:
                    return None
                return current

            try:
                mapped = self._wait_for_value(
                    switched_image,
                    min(self.timeout_ms, 5000.0),
                    "识别当前图片",
                )
            except ReviewStopped:
                if self.page is None or self.page.is_closed():
                    raise
                self.log("缩略图第 %d 张点击后未切换，继续下一张" % (index + 1))
                continue
            if mapped is None:
                self.log("缩略图第 %d 张点击后无法识别图片，跳过选图" % (index + 1))
                continue
            if mapped.picture_guid in self.reviewed_guids:
                self.log(
                    "点击后识别为本轮已审核图片 %s，跳过选图"
                    % (mapped.original_name or mapped.picture_guid)
                )
                continue
            if not self.ignore_defect_boxes and is_system_defect_image(mapped):
                self.log(
                    "点击后识别为系统缺陷图片 %s，跳过选图"
                    % (mapped.original_name or mapped.picture_guid)
                )
                continue
            if mapped.picture_guid in attempted_guids:
                self.log(
                    "点击后识别为已失败/跳过图片 %s，跳过选图"
                    % (mapped.original_name or mapped.picture_guid)
                )
                continue
            if allowed_guids is not None and mapped.picture_guid not in allowed_guids:
                continue
            if check_marks and not is_unreviewed_image(mapped):
                self.log(
                    "点击后识别为已审图片 %s，跳过选图"
                    % (mapped.original_name or mapped.picture_guid)
                )
                continue
            self._wait_for_timeout(self.page, 100)
            if not self._has_review_complete_button(dialog):
                attempted_guids.add(mapped.picture_guid)
                self.log(
                    "图片 %s 是已打勾状态（按钮不是审核完成），跳过"
                    % (mapped.original_name or mapped.picture_guid)
                )
                continue
            self.log(
                "已切换到未打勾图片：%s" % (mapped.original_name or mapped.picture_guid)
            )
            return mapped, mark
        return None, 0

    def _click_thumbnail(
        self, dialog, image: ReviewImage, purpose: str = "重新加载"
    ) -> None:
        try:
            images = dialog.locator("img")
        except Exception:
            images = None
        if images is not None:
            lowered_guid = image.file_guid.casefold()
            for index in range(images.count()):
                thumbnail = images.nth(index)
                try:
                    source = str(thumbnail.get_attribute("src") or "").casefold()
                except Exception:
                    continue
                if lowered_guid not in source or "isthumb=yes" not in source:
                    continue
                try:
                    if not thumbnail.is_visible():
                        continue
                except Exception:
                    continue
                try:
                    if not thumbnail.is_enabled():
                        continue
                except Exception:
                    pass
                try:
                    self._dom_click(thumbnail)
                except Exception as exc:
                    raise ReviewStopped(
                        "点击图片 %s 的缩略图失败：%s"
                        % (image.picture_guid, str(exc)[:200])
                    ) from exc
                self._wait_for_timeout(self.page, 200)
                self._wait_dialog_loading_gone(dialog)
                self.log("已点击图片 %s 的缩略图%s" % (image.picture_guid, purpose))
                return
        raise ReviewStopped(
            "无法通过 fileGuid 定位图片 %s 的缩略图，不能点击其他图片"
            % image.picture_guid
        )

    def _select_image(self, dialog, image: ReviewImage) -> int:
        """点击目标缩略图切换图片，返回点击前的响应位置。"""
        if self._thumbnail_active(dialog, image.file_guid):
            return 0
        mark = len(self.monitor.responses)
        self._click_thumbnail(dialog, image, "切换")
        try:
            self._wait_for_value(
                lambda: self._thumbnail_active(dialog, image.file_guid),
                min(self.timeout_ms, 2000.0),
                "切换到图片 %s" % image.picture_guid,
            )
        except ReviewStopped:
            if self.page is None or self.page.is_closed():
                raise
            self.log(
                "未能确认图片 %s 的缩略图选中态，继续由图片身份校验兜底"
                % image.picture_guid
            )
        return mark

    def _canvas_rendered(self, dialog) -> bool:
        script = """(el) => {
  const w = Number(el.width || el.clientWidth || 0);
  const h = Number(el.height || el.clientHeight || 0);
  if (!w || !h) return false;
  const ctx = el.getContext && el.getContext('2d');
  if (!ctx) return true;
  const points = [
    [w * 0.5, h * 0.5],
    [w * 0.25, h * 0.25],
    [w * 0.75, h * 0.25],
    [w * 0.25, h * 0.75],
    [w * 0.75, h * 0.75],
  ];
  try {
    for (const [x, y] of points) {
      const data = ctx.getImageData(
        Math.max(0, Math.floor(x)), Math.max(0, Math.floor(y)), 1, 1
      ).data;
      if (data[3] > 0 && !(data[0] > 245 && data[1] > 245 && data[2] > 245)) {
        return true;
      }
    }
    return false;
  } catch (err) {
    return true;
  }
}"""
        try:
            canvases = dialog.locator("canvas:visible")
        except Exception:
            return False
        for index in range(canvases.count()):
            canvas = canvases.nth(index)
            try:
                if canvas.evaluate(script):
                    return True
            except Exception:
                continue
        return False

    def _loading_visible(self, dialog) -> bool:
        for selector in (".el-loading-mask:visible", ".el-loading-spinner:visible"):
            try:
                if dialog.locator(selector).count():
                    return True
            except Exception:
                continue
        return False

    def _image_name_matches(self, dialog, original_name: str) -> bool:
        if not original_name:
            return False
        try:
            text = _normalize_text(dialog.inner_text())
        except Exception:
            return False
        return original_name in text

    def _image_selected(self, dialog, image: ReviewImage) -> bool:
        return self._thumbnail_active(
            dialog, image.file_guid
        ) or self._image_name_matches(dialog, image.original_name)

    def _image_rendered(self, dialog, file_guid: str, original_name: str = "") -> bool:
        lowered_guid = file_guid.casefold()
        name_matches = self._image_name_matches(dialog, original_name)
        selected = self._thumbnail_active(dialog, file_guid) or name_matches
        for image, source in self._visible_main_images(dialog):
            lowered_source = source.casefold()
            if lowered_guid in lowered_source:
                if self._element_rendered(image):
                    return True
                continue
            if lowered_source.startswith(("blob:", "data:")):
                if selected and self._element_rendered(image):
                    return True
        return self._canvas_rendered(dialog) and selected

    def _image_is_ready(self, dialog, image: ReviewImage) -> bool:
        if self._loading_visible(dialog):
            return False
        if not self._image_selected(dialog, image):
            return False
        return self._image_rendered(dialog, image.file_guid, image.original_name)

    def _image_identity_confirmed(self, dialog, image: ReviewImage) -> bool:
        if self._review_button_ready(dialog) and self._image_selected(dialog, image):
            return True
        if self._image_is_ready(dialog, image):
            return True
        main_images = self._visible_main_images(dialog)
        if not main_images:
            return False
        if not any(self._element_rendered(item) for item, _source in main_images):
            return False
        lowered_guid = image.file_guid.casefold()
        if len(main_images) == 1:
            source = main_images[0][1].casefold()
            if lowered_guid in source:
                return True
            if source.startswith(("blob:", "data:")):
                return self._image_selected(dialog, image)
            return False
        return self._image_selected(dialog, image)

    def _require_current_image_identity(self, dialog, image: ReviewImage) -> None:
        if self._image_identity_confirmed(dialog, image):
            return
        raise ReviewStopped(
            "无法确认当前选中图片（fileGuid=%s），停止以避免审核错图" % image.file_guid
        )

    def _button_snapshot(self, button) -> str:
        parts = []
        try:
            parts.append("text=%r" % _normalize_text(button.inner_text()))
        except Exception:
            parts.append("text=?")
        for attribute in ("class", "disabled", "aria-disabled"):
            try:
                parts.append("%s=%r" % (attribute, button.get_attribute(attribute)))
            except Exception:
                parts.append("%s=?" % attribute)
        try:
            parts.append("is_enabled=%r" % button.is_enabled())
        except Exception:
            parts.append("is_enabled=?")
        try:
            parts.append(
                "computed=%r"
                % button.evaluate(
                    """(el) => {
  const s = getComputedStyle(el);
  return {
    opacity: s.opacity,
    pointerEvents: s.pointerEvents,
    display: s.display,
    visibility: s.visibility,
  };
}"""
                )
            )
        except Exception:
            parts.append("computed=?")
        try:
            box = button.bounding_box()
        except Exception:
            box = None
        if box and self.page is not None:
            try:
                hit = self.page.evaluate(
                    """([x, y]) => {
  const el = document.elementFromPoint(x, y);
  if (!el) return null;
  return {
    tag: el.tagName,
    class: String(el.className || ''),
    text: String(el.innerText || '').trim().slice(0, 40),
  };
}""",
                    [box["x"] + box["width"] / 2, box["y"] + box["height"] / 2],
                )
                parts.append("hit=%r" % hit)
            except Exception:
                parts.append("hit=?")
        return " | ".join(parts)

    def _diagnose_review_button(self, dialog, button) -> None:
        self.log("诊断：hover 前 %s" % self._button_snapshot(button))
        try:
            button.hover()
            self._wait_for_timeout(self.page, 300)
            self.log("诊断：hover 后 %s" % self._button_snapshot(button))
        except Exception as exc:
            self.log("诊断：hover 失败 %s" % str(exc)[:200])
        try:
            mouse = getattr(self.page, "mouse", None)
            if mouse is not None:
                mouse.move(0, 0)
                self._wait_for_timeout(self.page, 200)
                self.log("诊断：移开鼠标后 %s" % self._button_snapshot(button))
        except Exception as exc:
            self.log("诊断：移开鼠标失败 %s" % str(exc)[:200])

    def wait_current_image(self, image, dialog=None, mark=None) -> None:
        """以「审核完成」按钮亮起且可用作为图片加载成功判定。"""
        review_image = _coerce_review_image(image)
        dialog = dialog or self.current_dialog or self._review_dialog()
        if dialog is None:
            raise ReviewStopped("未找到审核弹窗，无法确定当前图片")
        timeout_ms = max(1.0, self.image_timeout_ms)
        started = time.monotonic()
        self._wait_for_value(
            lambda: self._review_button_ready(dialog),
            timeout_ms,
            "图片 %s 的「审核完成」按钮亮起" % review_image.file_guid,
            ImageLoadTimeout,
            interval_ms=50,
        )
        elapsed_ms = (time.monotonic() - started) * 1000.0
        if self.diagnose_button:
            self.log(
                "诊断：按钮就绪耗时 %.0f ms（%s）"
                % (elapsed_ms, review_image.picture_guid)
            )
        elif elapsed_ms > 1000.0:
            self.log(
                "图片 %s 「审核完成」可用等待 %.0f ms"
                % (review_image.picture_guid, elapsed_ms)
            )

    def _click_current_thumbnail(self, dialog, image: ReviewImage) -> None:
        self._click_thumbnail(dialog, image, "重新加载")

    def _load_image_with_retries(
        self, dialog, image: ReviewImage, search_mark: int
    ) -> None:
        attempts = 1 + max(0, self.image_retries)
        last_error = None
        for attempt in range(1, attempts + 1):
            if attempt > 1:
                self.log(
                    "图片 %s 第 %d/%d 次加载，点击缩略图重新触发"
                    % (image.picture_guid, attempt, attempts)
                )
                try:
                    self._click_current_thumbnail(dialog, image)
                except ReviewStopped as exc:
                    last_error = exc
                    self.log(
                        "无法点击图片 %s 的缩略图重试，转为跳过该图片：%s"
                        % (image.picture_guid, _redact_urls(exc))
                    )
                    break
            try:
                self.wait_current_image(image, dialog, mark=search_mark)
                self.log(
                    "图片 %s 已加载（第 %d/%d 次尝试）"
                    % (image.picture_guid, attempt, attempts)
                )
                return
            except ImageLoadTimeout as exc:
                if self._image_is_ready(dialog, image):
                    grace_ms = int(
                        min(
                            BUTTON_NOT_READY_GRACE_SECONDS * 1000,
                            max(500.0, self.image_timeout_ms),
                        )
                    )
                    self.log(
                        "图片 %s 已显示但「审核完成」按钮未就绪，额外等待 %.1f 秒"
                        % (image.picture_guid, grace_ms / 1000.0)
                    )
                    try:
                        self._wait_for_value(
                            lambda: self._review_button_ready(dialog),
                            grace_ms,
                            "图片 %s 的「审核完成」按钮（图片已显示）"
                            % image.picture_guid,
                            ImageLoadTimeout,
                            interval_ms=50,
                        )
                    except ImageLoadTimeout as grace_exc:
                        raise ReviewButtonNotReady(
                            "图片已显示，但「审核完成」按钮等待 %.1f 秒仍不可用"
                            % BUTTON_NOT_READY_GRACE_SECONDS
                        ) from grace_exc
                    self.log(
                        "图片 %s 已加载（第 %d/%d 次尝试）"
                        % (image.picture_guid, attempt, attempts)
                    )
                    return
                last_error = exc
                self.log(
                    "图片 %s 第 %d/%d 次加载失败：%s"
                    % (image.picture_guid, attempt, attempts, exc)
                )
        raise ImageLoadFailure(
            "图片 %s 连续 %d 次加载失败：%s"
            % (image.picture_guid, attempts, last_error)
        )

    def _load_and_review_selected(
        self,
        dialog,
        image: ReviewImage,
        search_mark: int,
        task_info: dict,
        settings: ReviewSettings,
        failed_images: list,
        retry_pass: bool = False,
        task_mark: int = 0,
        system_defect_images: list = None,
        button_not_ready_images: list = None,
    ) -> bool:
        try:
            self._load_image_with_retries(dialog, image, search_mark)
        except ReviewButtonNotReady as exc:
            reason = _redact_urls(exc)
            self.log(
                "图片 %s 已显示但按钮未就绪，记录并跳过（不记加载失败）"
                % image.picture_guid
            )
            self._record_button_not_ready(task_info, settings, image, reason)
            if (
                button_not_ready_images is not None
                and image not in button_not_ready_images
            ):
                button_not_ready_images.append(image)
            self.save_diagnostic(self.page, "button_not_ready")
            return False
        except ImageLoadFailure as exc:
            reason = _redact_urls(exc)
            if retry_pass:
                self.log(
                    "图片 %s 补审仍失败，记录并结束该任务：%s"
                    % (image.picture_guid, reason)
                )
                self._record_image_failure(task_info, settings, image, reason)
            else:
                self.log(
                    "图片 %s 首轮加载失败，暂记失败稍后补审：%s"
                    % (image.picture_guid, reason)
                )
            self.save_diagnostic(self.page, "image_failure")
            failed_images.append(image)
            return False
        details = (
            []
            if self.ignore_defect_boxes
            else self._collect_tagging_details(
                image.picture_guid,
                task_mark,
                timeout_ms=TAGGING_DETAIL_UI_WAIT_MS,
            )
        )
        if details:
            self._record_system_defect(task_info, settings, image, details)
            if system_defect_images is not None and image not in system_defect_images:
                system_defect_images.append(image)
            self.log(
                "图片 %s 存在系统缺陷框（%d 条），跳过审核"
                % (image.original_name or image.picture_guid, len(details))
            )
            return False
        try:
            self.review_current_image(image, dialog)
        except ReviewButtonUnavailable as exc:
            reason = _redact_urls(exc)
            if retry_pass:
                self.log(
                    "图片 %s 补审时审核按钮仍不可用，记录并结束该任务：%s"
                    % (image.picture_guid, reason)
                )
                self._record_image_failure(task_info, settings, image, reason)
            else:
                self.log(
                    "图片 %s 审核按钮不可用，暂记失败稍后补审：%s"
                    % (image.picture_guid, reason)
                )
            self.save_diagnostic(self.page, "image_failure")
            failed_images.append(image)
            return False
        return True

    def _mark_reviewed(self, picture_guid: object) -> bool:
        guid = _normalize_text(picture_guid)
        if not guid or guid in self.reviewed_guids:
            return False
        self.reviewed_guids.add(guid)
        self.reviewed_count += 1
        return True

    def _review_response_guid(self, response) -> str:
        params = _request_query_params(response) or {}
        return _normalize_text(params.get("picDetailGuid"))

    def _wait_write_result(
        self, api_path: str, mark: int, description: str, accept=None
    ):
        deadline = time.monotonic() + self.timeout_ms / 1000.0
        while True:
            self.check_cancel()
            for response in self.monitor.responses[mark:]:
                if not self.monitor._matches_page(response):
                    continue
                if not _is_api(response.url, api_path):
                    continue
                if accept is not None and not accept(response):
                    continue
                if response.status != 200:
                    raise WriteResultUnknown(
                        "%s HTTP 状态为 %s，写请求结果不明"
                        % (description, response.status)
                    )
                try:
                    payload = response.json()
                except Exception as exc:
                    raise WriteResultUnknown(
                        "%s响应读取失败：%s" % (description, str(exc)[:200])
                    ) from exc
                if not isinstance(payload, dict):
                    raise WriteResultUnknown("%s响应不是 JSON 对象" % description)
                code = payload.get("code")
                success = payload.get("success")
                if code == 200 and success is True:
                    return payload, response
                message = payload.get("msg")
                if not isinstance(message, str) or not message.strip():
                    message = "未知错误"
                raise WriteRejected(
                    "%s业务失败（code=%r，success=%r）：%s"
                    % (description, code, success, message.strip()[:200])
                )
            if self.page is not None and self.page.is_closed():
                raise WriteResultUnknown(
                    "等待%s时页面已关闭，写请求结果不明" % description
                )
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise WriteResultUnknown(
                    "等待%s超时（%.1f 秒），写请求结果不明，停止运行"
                    % (description, self.timeout_ms / 1000.0)
                )
            self._wait_for_timeout(
                self.page, int(min(_CANCEL_WAIT_SLICE_MS, remaining * 1000.0))
            )

    def review_current_image(self, image, dialog=None) -> int:
        """以界面当前图片为准审核；只要「审核完成」按钮亮起就点击。"""
        dialog = dialog or self.current_dialog or self._review_dialog()
        if dialog is None:
            raise ReviewStopped("未找到审核弹窗，无法审核当前图片")
        review_image = _coerce_review_image(image)
        current = None
        if not self._image_selected(dialog, review_image):
            current = (
                self._current_image_from_dialog(dialog, self.task_images)
                if self.task_images
                else None
            )
        if (
            current is not None
            and is_unreviewed_image(current)
            and not is_system_defect_image(current)
        ):
            if current.picture_guid != review_image.picture_guid:
                self.log(
                    "界面当前图片为 %s，以界面为准审核"
                    % (current.original_name or current.picture_guid)
                )
            review_image = current
        if review_image.picture_guid in self.reviewed_guids:
            self.log("图片 %s 本轮已审核成功，跳过重复审核" % review_image.picture_guid)
            return len(self.monitor.responses) if self.monitor is not None else 0
        button = self._wait_review_button(dialog)
        if self.diagnose_button and not self.button_diagnosed:
            self._diagnose_review_button(dialog, button)
            self.button_diagnosed = True
        self._ensure_monitor()
        mark = len(self.monitor.responses)
        position = review_image.index + 1
        total = max(self.task_image_total, position)
        self.log("审核图片 %d/%d：%s" % (position, total, review_image.picture_guid))
        click_started = time.monotonic()
        try:
            # 直接点 DOM 节点，不让鼠标移到按钮上触发 hover/遮罩/置灰；
            # Actionability 已在 _wait_review_button 中检查过。
            button.evaluate("(el) => el.click()")
        except Exception:
            try:
                button.click(force=True)
            except Exception as exc:
                raise WriteResultUnknown(
                    "点击「审核完成」失败，写请求结果不明：%s" % str(exc)[:200]
                ) from exc
        if self.diagnose_button:
            self.log(
                "诊断：DOM click 调用耗时 %.0f ms"
                % ((time.monotonic() - click_started) * 1000.0)
            )
        payload, response = self._wait_write_result(
            PICTURE_REVIEW_API,
            mark,
            "图片审核",
        )
        if self.diagnose_button:
            self.log(
                "诊断：点击到图片审核响应耗时 %.0f ms"
                % ((time.monotonic() - click_started) * 1000.0)
            )
        if payload.get("data") is not True:
            raise WriteRejected(
                "图片审核响应 data=%r，未确认成功" % payload.get("data")
            )
        response_guid = self._review_response_guid(response)
        if not response_guid:
            raise WriteResultUnknown("图片审核响应缺少 picDetailGuid，结果不明")
        review_image = ReviewImage(
            picture_guid=response_guid,
            file_guid=review_image.file_guid,
            index=review_image.index,
            audit_status=review_image.audit_status,
            pic_defect_flag=review_image.pic_defect_flag,
            line_name=review_image.line_name,
            tower_name=review_image.tower_name,
            original_name=review_image.original_name,
            shoot_time=review_image.shoot_time,
        )
        self._mark_reviewed(review_image.picture_guid)
        self.log("图片审核成功：%s" % review_image.picture_guid)
        return mark

    def _mark_current_dialog(self, dialog) -> None:
        try:
            dialog.evaluate("(el) => el.setAttribute('data-shade-review-dialog', '1')")
        except Exception:
            pass

    def _is_current_review_dialog(self, container) -> bool:
        if self.current_dialog is not None and container is self.current_dialog:
            return True
        try:
            marker = container.get_attribute("data-shade-review-dialog")
        except Exception:
            marker = None
        if str(marker or "").strip() == "1":
            return True
        return (
            self._find_visible_button(
                container, REVIEW_BUTTON_TEXT, require_enabled=False
            )
            is not None
        )

    def _is_completion_confirm_text(self, text: object) -> bool:
        normalized = _normalize_text(text)
        if any(word in normalized for word in FINAL_CONFIRM_NEGATIVE_WORDS):
            return False
        return any(phrase in normalized for phrase in FINAL_CONFIRM_PHRASES)

    def _container_text(self, container) -> str:
        try:
            return _normalize_text(container.inner_text())
        except Exception:
            return ""

    def _wait_final_confirm(self):
        def find():
            try:
                boxes = self.page.locator(".el-message-box:visible")
            except Exception:
                boxes = None
            if boxes is not None and boxes.count():
                box = boxes.last
                if not self._is_completion_confirm_text(self._container_text(box)):
                    raise ReviewStopped(
                        "出现未知确认框，文案不是明确的完成确认，已停止运行"
                    )
                button = self._find_visible_button(box, CONFIRM_BUTTON_TEXT)
                if button is None:
                    raise ReviewStopped("完成确认框缺少可用的「确认」按钮，已停止运行")
                return button
            try:
                dialogs = self.page.locator(".el-dialog:visible")
            except Exception:
                dialogs = None
            if dialogs is not None:
                for index in range(dialogs.count() - 1, -1, -1):
                    dialog = dialogs.nth(index)
                    if self._is_current_review_dialog(dialog):
                        continue
                    if not self._is_completion_confirm_text(
                        self._container_text(dialog)
                    ):
                        continue
                    button = self._find_visible_button(dialog, CONFIRM_BUTTON_TEXT)
                    if button is not None:
                        return button
            return None

        return self._wait_for_value(find, self.timeout_ms, "明确的完成确认")

    def _wait_list_refresh(
        self,
        task_guid: str,
        mark: int,
        page_number: int,
        settings: ReviewSettings,
    ):
        deadline = time.monotonic() + self.timeout_ms / 1000.0
        while True:
            self.check_cancel()
            for response in self.monitor.responses[mark:]:
                if not self.monitor._matches_page(response):
                    continue
                if not _is_api(response.url, TASK_PAGE_API):
                    continue
                if response.status != 200:
                    raise WriteResultUnknown(
                        "任务列表刷新 HTTP 状态为 %s，未记任务完成" % response.status
                    )
                try:
                    payload = response.json()
                except Exception as exc:
                    raise WriteResultUnknown(
                        "任务列表刷新响应读取失败：%s" % str(exc)[:200]
                    ) from exc
                try:
                    records, _total, current, _pages = parse_task_page(payload)
                except RuntimeError as exc:
                    raise WriteResultUnknown("任务列表刷新响应无效：%s" % exc) from exc
                try:
                    flags = self._verify_query_conditions(response, settings)
                    self._verify_records_conditions(records, settings, flags)
                except RuntimeError as exc:
                    raise WriteResultUnknown(
                        "任务列表刷新响应未通过筛选核验：%s" % exc
                    ) from exc
                # 任务消失后末页可能被回收，列表会从原页码前移到上一页；
                # 允许原页码或前移一页，任务 GUID 消失仍作为完成依据。
                if current not in (page_number, page_number - 1):
                    raise WriteResultUnknown(
                        "任务列表刷新页码为 %d，任务原页码为 %d，"
                        "不能确认真实列表已刷新" % (current, page_number)
                    )
                if current == page_number - 1:
                    self.log(
                        "任务列表由第 %d 页前移为第 %d 页，任务已消失"
                        % (page_number, current)
                    )
                still_present = False
                for item in records:
                    if not isinstance(item, dict):
                        continue
                    if _normalize_text(item.get(TASK_GUID_FIELD)) == task_guid:
                        still_present = True
                        break
                if still_present:
                    continue
                self.log("任务已从刷新后的列表消失")
                return records
            if self.page is not None and self.page.is_closed():
                raise WriteResultUnknown("等待任务列表刷新时页面已关闭，未记任务完成")
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise WriteResultUnknown(
                    "submitAudit 成功但列表刷新超时（%.1f 秒），未记任务完成"
                    % (self.timeout_ms / 1000.0)
                )
            self._wait_for_timeout(
                self.page, int(min(_CANCEL_WAIT_SLICE_MS, remaining * 1000.0))
            )

    def _confirm_and_submit(
        self,
        task_guid: str,
        page_number: int,
        settings: ReviewSettings,
    ) -> None:
        dialog = self.current_dialog or self._review_dialog()
        if dialog is not None and self._visible_message_box() is None:
            complete = self._wait_for_value(
                lambda: self._find_visible_button(
                    dialog, "完成任务", require_enabled=True
                ),
                min(self.timeout_ms, 10000.0),
                "「完成任务」按钮",
            )
            try:
                self._dom_click(complete)
            except Exception as exc:
                raise ReviewStopped(
                    "点击「完成任务」失败：%s" % str(exc)[:200]
                ) from exc
            self.log("已点击「完成任务」")
            self._wait_for_timeout(self.page, 300)
        confirm = self._wait_final_confirm()
        mark = len(self.monitor.responses)
        try:
            self._dom_click(confirm)
        except Exception as exc:
            raise WriteResultUnknown(
                "点击「确认」失败，提交结果不明：%s" % str(exc)[:200]
            ) from exc
        _payload, _response = self._wait_write_result(
            SUBMIT_AUDIT_API,
            mark,
            "任务提交",
            accept=lambda response: self._submit_response_matches(response, task_guid),
        )
        self.log("任务提交审核成功，等待列表刷新")
        self._wait_list_refresh(task_guid, mark, page_number, settings)

    def _submit_response_matches(self, response, task_guid: str) -> bool:
        try:
            params = parse_qs(urlsplit(response.url).query)
        except ValueError:
            return False
        response_guid = _normalize_text(params.get("recognitionTaskGuid", [""])[0])
        return not response_guid or response_guid == task_guid

    def _find_close_button(self, dialog):
        try:
            buttons = dialog.locator(".el-dialog__headerbtn")
        except Exception:
            return None
        for index in range(buttons.count()):
            button = buttons.nth(index)
            try:
                if button.is_visible():
                    return button
            except Exception:
                continue
        return None

    def _press_escape(self) -> None:
        try:
            keyboard = getattr(self.page, "keyboard", None)
            if keyboard is not None:
                keyboard.press("Escape")
            else:
                self.page.press("Escape")
        except Exception as exc:
            self.log("Escape 兜底失败：%s" % str(exc)[:200])

    def _wait_dialog_closed(self, dialog, timeout_ms: float = None) -> None:
        def closed():
            if self._visible_message_box() is not None:
                raise ReviewStopped("关闭审核弹窗时出现未知二次确认，已停止运行")
            return not dialog.is_visible()

        self._wait_for_value(
            closed,
            self.timeout_ms if timeout_ms is None else timeout_ms,
            "关闭审核弹窗",
        )

    def close_task(self, dialog=None, timeout_ms: float = None) -> bool:
        """关闭当前审核弹窗；出现未知二次确认时停止，不盲点。"""
        dialog = dialog or self.current_dialog or self._review_dialog()
        if dialog is None:
            if self._visible_message_box() is not None:
                raise ReviewStopped("存在未识别的确认框，无法安全关闭")
            return True
        if self._visible_message_box() is not None:
            raise ReviewStopped("关闭审核弹窗前出现未知二次确认，已停止运行")
        if not dialog.is_visible():
            return True
        close_button = self._find_close_button(dialog)
        if close_button is not None:
            try:
                self._dom_click(close_button)
            except Exception:
                close_button = None
        if close_button is None:
            self.log("未找到可用的弹窗关闭按钮，尝试 Escape 兜底")
            self._press_escape()
        self._wait_dialog_closed(dialog, timeout_ms)
        if self._visible_message_box() is not None:
            raise ReviewStopped("关闭审核弹窗时出现未知二次确认，已停止运行")
        self.current_dialog = None
        self.log("已关闭审核弹窗")
        return True

    def _response_task_guid(self, response) -> str:
        try:
            params = parse_qs(urlsplit(response.url).query)
        except ValueError:
            return ""
        return _normalize_text(params.get("recognitionTaskGuid", [""])[0])

    def _wait_task_images(self, mark: int, task_guid: str) -> list:
        def belongs_to_task(response) -> bool:
            response_guid = self._response_task_guid(response)
            return not response_guid or response_guid == task_guid

        try:
            group_payload, _group_response = self.monitor.wait_success(
                PIC_GROUP_API,
                mark,
                self.timeout_ms,
                "任务图片分组",
                accept=belongs_to_task,
            )
            detail_payload, _detail_response = self.monitor.wait_success(
                PIC_DETAIL_API,
                mark,
                self.timeout_ms,
                "任务图片明细",
                accept=belongs_to_task,
            )
        except RuntimeError as exc:
            raise ReviewStopped("读取任务图片失败：%s" % exc) from exc
        try:
            return parse_task_images(group_payload, detail_payload)
        except RuntimeError as exc:
            raise ReviewStopped("解析任务图片失败：%s" % exc) from exc

    def process_task(self, entry: dict, settings: ReviewSettings = None) -> dict:
        """处理单个任务：逐张加载并审核，全部成功后提交并核对列表刷新。"""
        if not isinstance(entry, dict):
            raise ReviewStopped("任务候选不是对象，无法处理")
        task_guid = _normalize_text(entry.get("guid"))
        if not task_guid:
            raise ReviewStopped("任务候选缺少 recognitionTaskGuid")
        page_number = entry.get("page")
        if (
            isinstance(page_number, bool)
            or not isinstance(page_number, int)
            or page_number < 1
        ):
            raise ReviewStopped("任务候选缺少有效页码，无法核对任务列表刷新")
        settings = settings or getattr(self, "settings", None)
        if settings is None:
            raise ReviewStopped("缺少筛选条件，无法核对任务列表刷新")
        item = entry.get("item") or {}
        task_name = _normalize_text(item.get("taskName")) or "未命名任务"
        self._reset_review_state()
        self._ensure_monitor()
        self.current_task_name = task_name
        task_mark = len(self.monitor.responses)
        self.log("开始处理任务《%s》GUID=%s" % (task_name, task_guid))
        system_defect_images = []
        failed_images = []
        button_not_ready_images = []
        task_info = {
            "task_guid": task_guid,
            "task_name": task_name,
            "work_order_no": _normalize_text(item.get("workOrderNos")),
            "line_name": "",
            "line_tower": "",
            "tower_name": "",
            "unreviewed_count": 0,
        }
        try:
            self.enter_task(entry)
            images = self._wait_task_images(task_mark, task_guid)
            self.task_image_total = len(images)
            self.task_images = images
            self.progress_label = task_name
            if self.progress_handler:
                self.progress_handler(0, 1, task_name)
            self.log("任务《%s》共 %d 张图片" % (task_name, len(images)))
            try:
                dialog = self._wait_review_dialog()
            except ReviewStopped as exc:
                if self.page is not None and not self.page.is_closed():
                    exc.shade_dialog_unresponsive = True
                    self.log("未找到任务审核弹窗，按弹层无响应处理")
                raise
            # 先登记弹窗再等内容加载：加载超时时 run 层才能安全关闭/清理它。
            self.current_dialog = dialog
            self._mark_current_dialog(dialog)
            dialog = self._wait_task_dialog_loaded(dialog, task_name)
            line_name, line_tower, tower_name = self._task_line_info(item, images)
            task_info = {
                "task_guid": task_guid,
                "task_name": task_name,
                "work_order_no": _normalize_text(item.get("workOrderNos")),
                "line_name": line_name,
                "line_tower": line_tower,
                "tower_name": tower_name,
                "unreviewed_count": self._unreviewed_count_from_dialog(dialog),
            }
            if task_info.get("unreviewed_count") == 0:
                self.log("任务《%s》界面未审核 0 张，直接完成任务" % task_name)
                if self.progress_handler:
                    self.progress_handler(1, 1, task_name)
                self._confirm_and_submit(task_guid, page_number, settings)
                self.current_dialog = None
                self.log("任务《%s》审核完成，共审核 0 张未打勾图片" % task_name)
                return {
                    "status": "completed",
                    "images": 0,
                    "reason": "",
                }
            if task_info["unreviewed_count"] > self.max_unreviewed:
                reason = (
                    "新任务未审核 %d 张，超过阈值 %d 张，跳过本任务"
                    % (task_info["unreviewed_count"], self.max_unreviewed)
                )
                self._record_anomaly(
                    task_info,
                    settings,
                    "未审核数超阈值",
                    reason,
                    result="已跳过，待人工处理",
                )
                self.log("跳过任务《%s》：%s" % (task_name, reason))
                if self.progress_handler:
                    self.progress_handler(1, 1, task_name)
                try:
                    self.close_task(dialog)
                except ReviewStopped as close_exc:
                    self.save_diagnostic(self.page, "close_failure")
                    close_exc.shade_close_failed = True
                    raise
                return {
                    "status": "skipped",
                    "images": 0,
                    "reason": reason,
                    "threshold_skipped": True,
                }
            system_defect_images = (
                []
                if self.ignore_defect_boxes
                else [
                    image
                    for image in images
                    if is_system_defect_image(image) and is_unreviewed_image(image)
                ]
            )
            reviewable_images = [
                image
                for image in images
                if is_unreviewed_image(image)
                and (self.ignore_defect_boxes or not is_system_defect_image(image))
            ]
            checked_images = [
                image for image in images if not is_unreviewed_image(image)
            ]
            unknown_flags = (
                []
                if self.ignore_defect_boxes
                else [
                    image
                    for image in images
                    if is_unreviewed_image(image)
                    and image.pic_defect_flag.casefold() not in KNOWN_PIC_DEFECT_FLAGS
                ]
            )
            if unknown_flags:
                first_unknown = unknown_flags[0]
                reason = (
                    "存在未知图片缺陷标识（pictureDetailGuid=%s，"
                    "picDefectFlg=%r），需人工核对"
                    % (first_unknown.picture_guid, first_unknown.pic_defect_flag)
                )
                for image in system_defect_images:
                    self._record_system_defect(task_info, settings, image, [])
                self._record_anomaly(
                    task_info,
                    settings,
                    "未知缺陷标识",
                    reason,
                    image=first_unknown,
                    result="已跳过，待人工核对",
                )
                self.log("跳过任务《%s》：%s" % (task_name, reason))
                if self.progress_handler:
                    self.progress_handler(1, 1, task_name)
                self.save_diagnostic(self.page, "partial_review")
                try:
                    self.close_task(dialog)
                except ReviewStopped as close_exc:
                    self.save_diagnostic(self.page, "close_failure")
                    close_exc.shade_close_failed = True
                    raise
                return {
                    "status": "skipped",
                    "images": self.reviewed_count,
                    "reason": reason,
                    "system_defects": [
                        image.picture_guid for image in system_defect_images
                    ],
                }
            if system_defect_images:
                self.log(
                    "任务《%s》发现 %d 张系统已识别缺陷框图片，将跳过这些图片，"
                    "审核其余未打勾图片后关闭弹窗、不提交任务"
                    % (task_name, len(system_defect_images))
                )
            if checked_images:
                self.log(
                    "任务《%s》发现 %d 张已打勾/已标记图片，仅审核 %d 张未打勾图片"
                    % (task_name, len(checked_images), len(reviewable_images))
                )
            # 进度按“本任务实际要处理的未打勾图片”推进：系统缺陷框图片已确定跳过，
            # 先计入已处理；其余每审核/跳过一张再 +1。只按审核成功数推进会在
            # 缺陷框/失败较多的任务上长期停在 0。
            eligible_guids = {
                image.picture_guid for image in reviewable_images + system_defect_images
            }
            progress = {
                "total": len(eligible_guids),
                "done": len(system_defect_images),
                "counted": {image.picture_guid for image in system_defect_images},
            }

            def report_progress():
                if self.progress_handler:
                    self.progress_handler(
                        progress["done"], max(1, progress["total"]), task_name
                    )

            def mark_processed(image):
                guid = image.picture_guid
                if guid not in eligible_guids or guid in progress["counted"]:
                    return
                progress["counted"].add(guid)
                progress["done"] += 1
                report_progress()

            report_progress()
            attempted_guids = {image.picture_guid for image in system_defect_images}
            ui_driven = self._thumbnail_items_available(dialog)
            if ui_driven:
                filtered = self._show_unreviewed_only(dialog)
                while True:
                    self.check_cancel()
                    current = self._current_image_from_dialog(dialog, images)
                    if (
                        current is not None
                        and current.picture_guid not in attempted_guids
                        and current.picture_guid not in self.reviewed_guids
                        and (filtered or is_unreviewed_image(current))
                        and (
                            self.ignore_defect_boxes
                            or not is_system_defect_image(current)
                        )
                        and self._find_visible_button(
                            dialog, "重新审核", require_enabled=True
                        )
                        is None
                    ):
                        self.log(
                            "当前已是未打勾图片，直接审核：%s"
                            % (current.original_name or current.picture_guid)
                        )
                        ok = self._load_and_review_selected(
                            dialog,
                            current,
                            0,
                            task_info,
                            settings,
                            failed_images,
                            task_mark=task_mark,
                            system_defect_images=system_defect_images,
                            button_not_ready_images=button_not_ready_images,
                        )
                        mark_processed(current)
                        if not ok:
                            attempted_guids.add(current.picture_guid)
                            continue
                        has_failures = bool(failed_images or button_not_ready_images)
                        if has_failures:
                            # 有失败：不再等系统切图，直接进入下一轮，
                            # 由兜底选图点击第一张未审核且未失败的图片。
                            continue

                        # 无失败：不点缩略图、不单独等切换，直接等下一张的
                        # 「审核完成」按钮；系统切到哪张就审哪张。
                        def next_ready():
                            if (
                                self._find_visible_button(
                                    dialog, "完成任务", require_enabled=True
                                )
                                is not None
                            ):
                                return True
                            # 系统切图后按钮会重新亮起；只等按钮，
                            # 不再等标题/高亮缩略图变化，避免多等 3 秒。
                            return self._review_button_ready(dialog)

                        switch_started = time.monotonic()
                        try:
                            self._wait_for_value(
                                next_ready,
                                self.image_timeout_ms,
                                "下一张图片的「审核完成」按钮",
                                interval_ms=50,
                            )
                        except ReviewStopped:
                            pass
                        switch_ms = (time.monotonic() - switch_started) * 1000.0
                        if switch_ms > 1000.0:
                            self.log("下一张「审核完成」等待 %.0f ms" % switch_ms)
                        continue
                    has_failures = bool(failed_images or button_not_ready_images)
                    start_index = 0
                    if (
                        not has_failures
                        and current is not None
                        and current.picture_guid in attempted_guids
                    ):
                        active_index = self._active_thumbnail_index(dialog)
                        if active_index >= 0:
                            start_index = active_index + 1
                    image, search_mark = self._select_unreviewed_image(
                        dialog,
                        images,
                        attempted_guids,
                        check_marks=not filtered,
                        start_index=start_index,
                    )
                    if image is None:
                        break
                    if (
                        not self.ignore_defect_boxes and is_system_defect_image(image)
                    ) or (not filtered and not is_unreviewed_image(image)):
                        mark_processed(image)
                        attempted_guids.add(image.picture_guid)
                        continue
                    processed = self._load_and_review_selected(
                        dialog,
                        image,
                        search_mark,
                        task_info,
                        settings,
                        failed_images,
                        task_mark=task_mark,
                        system_defect_images=system_defect_images,
                        button_not_ready_images=button_not_ready_images,
                    )
                    mark_processed(image)
                    if not processed:
                        attempted_guids.add(image.picture_guid)
                if failed_images:
                    self.log(
                        "首轮有 %d 张图片失败，开始各补审 1 次" % len(failed_images)
                    )
                    allowed = {image.picture_guid for image in failed_images}
                    final_failed = []
                    while allowed:
                        image, search_mark = self._select_unreviewed_image(
                            dialog,
                            images,
                            set(),
                            check_marks=not filtered,
                            allowed_guids=set(allowed),
                        )
                        if image is None:
                            for failed in failed_images:
                                if failed.picture_guid in allowed:
                                    reason = "补审时无法定位该图片"
                                    self._record_image_failure(
                                        task_info,
                                        settings,
                                        failed,
                                        reason,
                                    )
                                    final_failed.append(failed)
                            break
                        allowed.discard(image.picture_guid)
                        if not self._load_and_review_selected(
                            dialog,
                            image,
                            search_mark,
                            task_info,
                            settings,
                            final_failed,
                            retry_pass=True,
                            task_mark=task_mark,
                            system_defect_images=system_defect_images,
                            button_not_ready_images=button_not_ready_images,
                        ):
                            continue
                    failed_images = final_failed
            else:
                for image in reviewable_images:
                    if image.picture_guid in self.reviewed_guids:
                        continue
                    search_mark = self._select_image(dialog, image)
                    if not self._has_review_complete_button(dialog):
                        self.log(
                            "图片 %s 的按钮不是审核完成，跳过硬点"
                            % (image.original_name or image.picture_guid)
                        )
                        mark_processed(image)
                        continue
                    self._load_and_review_selected(
                        dialog,
                        image,
                        search_mark,
                        task_info,
                        settings,
                        failed_images,
                        retry_pass=True,
                        task_mark=task_mark,
                        system_defect_images=system_defect_images,
                        button_not_ready_images=button_not_ready_images,
                    )
                    mark_processed(image)
            for image in system_defect_images:
                details = self._collect_tagging_details(image.picture_guid, task_mark)
                self._record_system_defect(task_info, settings, image, details)
            if failed_images or system_defect_images or button_not_ready_images:
                reasons = []
                if system_defect_images:
                    reasons.append("%d 张系统已识别缺陷框" % len(system_defect_images))
                if failed_images:
                    reasons.append("%d 张图片加载失败" % len(failed_images))
                if button_not_ready_images:
                    reasons.append(
                        "%d 张图片已显示但按钮未就绪" % len(button_not_ready_images)
                    )
                reason = (
                    "发现 %s，已跳过并审核其余 %d 张未打勾图片，"
                    "手动关闭审核弹窗、未提交任务，需人工复核"
                    % ("、".join(reasons), self.reviewed_count)
                )
                self.log("任务《%s》%s" % (task_name, reason))
                try:
                    self.close_task(dialog)
                except ReviewStopped as close_exc:
                    self.save_diagnostic(self.page, "close_failure")
                    close_exc.shade_close_failed = True
                    raise
                return {
                    "status": "deferred",
                    "images": self.reviewed_count,
                    "reason": reason,
                    "system_defects": [
                        image.picture_guid for image in system_defect_images
                    ],
                    "load_failures": [image.picture_guid for image in failed_images],
                    "button_not_ready": [
                        image.picture_guid for image in button_not_ready_images
                    ],
                }
            if not ui_driven and not reviewable_images:
                reason = "任务没有未打勾的 unlabeled 图片，已关闭审核弹窗、未提交任务"
                self.log("任务《%s》：%s" % (task_name, reason))
                try:
                    self.close_task(dialog)
                except ReviewStopped as close_exc:
                    self.save_diagnostic(self.page, "close_failure")
                    close_exc.shade_close_failed = True
                    raise
                return {
                    "status": "deferred",
                    "images": self.reviewed_count,
                    "reason": reason,
                    "system_defects": [],
                    "load_failures": [],
                }
            if not ui_driven and len(self.reviewed_guids) != len(reviewable_images):
                raise ReviewStopped(
                    "图片审核数量不一致：成功 %d 张，可审核 %d 张"
                    % (len(self.reviewed_guids), len(reviewable_images))
                )
            self._confirm_and_submit(task_guid, page_number, settings)
            self.current_dialog = None
            self.log(
                "任务《%s》审核完成，共审核 %d 张未打勾图片"
                % (task_name, self.reviewed_count)
            )
            return {
                "status": "completed",
                "images": self.reviewed_count,
                "reason": "",
            }
        except ImageLoadFailure as exc:
            reason = _redact_urls(exc)
            self.log("跳过任务《%s》：%s" % (task_name, reason))
            self.save_diagnostic(self.page, "image_failure")
            try:
                self.close_task(self.current_dialog)
            except ReviewStopped as close_exc:
                self.save_diagnostic(self.page, "close_failure")
                close_exc.shade_close_failed = True
                raise
            self.log("已关闭任务《%s》的审核弹窗，继续下一条" % task_name)
            return {
                "status": "skipped",
                "images": self.reviewed_count,
                "reason": reason,
                "system_defects": [
                    image.picture_guid for image in system_defect_images
                ],
                "load_failures": [image.picture_guid for image in failed_images],
            }
        except ReviewStopped as exc:
            if not getattr(exc, "shade_anomaly_recorded", False):
                if getattr(exc, "shade_dialog_unresponsive", False):
                    kind = "任务弹层加载超时"
                elif getattr(exc, "shade_close_failed", False):
                    kind = "关闭审核弹窗失败"
                else:
                    kind = "审核中止"
                self._record_anomaly(
                    task_info,
                    settings,
                    kind,
                    _redact_urls(exc),
                    result="已中止，待人工处理",
                )
                exc.shade_anomaly_recorded = True
            self.save_diagnostic(self.page, "review_stop")
            raise
        except Exception as exc:
            if self.stop_requested:
                raise
            reason = "任务《%s》处理异常：%s" % (task_name, str(exc)[:300])
            self._record_anomaly(
                task_info,
                settings,
                "未知异常",
                reason,
                result="等待 120 秒容错",
            )
            self.save_diagnostic(self.page, "unexpected")
            raise UnknownTaskError(reason) from exc
        finally:
            if failed_images or button_not_ready_images:
                if append_skip_task_name(self.skip_tasks_file, task_name):
                    self.skip_task_names.add(task_name.casefold())
                    self.log("已写入任务名称跳过表：%s" % task_name)
                elif task_name.casefold() in load_skip_task_names(self.skip_tasks_file):
                    self.skip_task_names.add(task_name.casefold())
                else:
                    self.log(
                        "警告：任务《%s》未能写入任务名称跳过表，"
                        "请检查文件是否被占用或不可写" % task_name
                    )
            self._write_summary_xlsx()

    def _refresh_and_resume(
        self, settings: ReviewSettings, prefix: str = "异常后"
    ) -> bool:
        """刷新网页回任务列表并继续；失败每 60 秒重试，直到成功或用户停止。"""
        self.current_dialog = None
        attempt = 0
        while True:
            self.check_cancel()
            attempt += 1
            try:
                self.log("%s：刷新网页（第 %d 次）" % (prefix, attempt))
                self.page.reload(wait_until="domcontentloaded")
                manual = self.manual_task_page
                self.manual_task_page = False
                try:
                    self.open_task_list()
                finally:
                    self.manual_task_page = manual
                self.apply_filters(settings)
                self.log("已刷新网页回到任务列表，继续")
                return True
            except Exception as exc:
                if self.stop_requested:
                    raise
                self.log(
                    "刷新网页失败：%s；%d 秒后重试"
                    % (_redact_urls(exc)[:300], _REFRESH_RETRY_SECONDS)
                )
                try:
                    self._wait_for_timeout(
                        self.page, int(_REFRESH_RETRY_SECONDS * 1000)
                    )
                except Exception:
                    if self.stop_requested:
                        raise
                    for _ in range(_REFRESH_RETRY_SECONDS):
                        self.check_cancel()
                        time.sleep(1)

    def _write_summary(self, summary: dict) -> None:
        try:
            self.outdir.mkdir(parents=True, exist_ok=True)
            path = self.outdir / "summary.json"
            path.write_text(
                json.dumps(summary, ensure_ascii=False, indent=2),
                encoding="utf-8",
            )
            self.log("汇总已写入：%s" % path)
        except Exception as exc:
            self.log("汇总写入失败：%s" % str(exc)[:200])

    def _log_run_summary(self, summary: dict) -> None:
        if not summary.get("tasks_processed"):
            if summary.get("skip_list_tasks"):
                self.log(
                    "运行结束：跳过表任务 %d 个，没有可审核任务。"
                    % summary.get("skip_list_tasks", 0)
                )
            else:
                self.log("运行结束：没有可审核任务。")
            return
        parts = [
            "完成任务 %d 个" % summary.get("completed", 0),
            "审核图片 %d 张" % summary.get("images", 0),
        ]
        if summary.get("system_defect_tasks"):
            parts.append(
                "系统缺陷框转人工任务 %d 个（%d 张图片）"
                % (
                    summary.get("system_defect_tasks", 0),
                    summary.get("system_defect_images", 0),
                )
            )
        if summary.get("image_load_failure_tasks"):
            parts.append(
                "图片加载失败转人工任务 %d 个（%d 张图片）"
                % (
                    summary.get("image_load_failure_tasks", 0),
                    summary.get("image_load_failures", 0),
                )
            )
        if summary.get("button_not_ready_tasks"):
            parts.append(
                "按钮未就绪转人工任务 %d 个（%d 张图片）"
                % (
                    summary.get("button_not_ready_tasks", 0),
                    summary.get("button_not_ready_images", 0),
                )
            )
        if summary.get("skip_list_tasks"):
            parts.append("跳过表任务 %d 个" % summary.get("skip_list_tasks", 0))
        if summary.get("threshold_skipped"):
            parts.append("超阈值跳过任务 %d 个" % summary.get("threshold_skipped", 0))
        if summary.get("unknown_errors"):
            parts.append("未知异常任务 %d 个" % summary.get("unknown_errors", 0))
        if summary.get("skipped"):
            parts.append("失败跳过任务 %d 个" % summary.get("skipped", 0))
        message = "运行结束：" + "，".join(parts)
        if (
            summary.get("deferred")
            or summary.get("skipped")
            or summary.get("system_defect_tasks")
            or summary.get("image_load_failure_tasks")
            or summary.get("button_not_ready_tasks")
            or summary.get("threshold_skipped")
            or summary.get("unknown_errors")
        ):
            message += "；存在未自动完成的任务，不能宣称全部审核完成。"
        else:
            message += "，全部可审核任务已完成。"
        self.log(message)
        if summary.get("summary_xlsx"):
            self.log("审核汇总表：%s" % summary["summary_xlsx"])

    def _report_task_progress(self) -> None:
        if not self.progress_handler:
            return
        total = max(1, int(self.task_progress_total or 0))
        done = max(0, min(int(self.task_progress_done), total))
        self.progress_handler(done, total, "任务进度")

    def run(self, settings: ReviewSettings) -> dict:
        """正式模式：逐条处理可审核任务，跳过失败任务并汇总。"""
        self.log("脚本版本 %s" % SCRIPT_VERSION)
        summary = {
            "completed": 0,
            "skipped": 0,
            "deferred": 0,
            "images": 0,
            "tasks_processed": 0,
            "skip_reasons": [],
            "system_defect_images": 0,
            "system_defect_tasks": 0,
            "image_load_failures": 0,
            "image_load_failure_tasks": 0,
            "button_not_ready_images": 0,
            "button_not_ready_tasks": 0,
            "skip_list_tasks": 0,
            "threshold_skipped": 0,
            "unknown_errors": 0,
        }
        skip_names = load_skip_task_names(self.skip_tasks_file)
        self.skip_task_names = skip_names
        self.log(
            "任务名称跳过表：%s（%d 个）"
            % (self.skip_tasks_file or "未设置", len(skip_names))
        )
        self.last_run_summary = summary
        self.settings = settings
        self.task_progress_total = 0
        self.task_progress_done = 0
        self.system_defect_rows = []
        self.system_defect_guids = set()
        self.system_defect_dirty = False
        self.anomaly_rows = []
        self.anomaly_keys = set()
        self.anomaly_dirty = False
        self._init_output_skip_file()
        try:
            while True:
                try:
                    self.open_task_list()
                    self.apply_filters(settings)
                    break
                except Exception as exc:
                    if self.stop_requested:
                        raise
                    self.log(
                        "进入任务列表失败：%s；刷新网页后重试"
                        % _redact_urls(exc)[:300]
                    )
                    self._refresh_and_resume(settings, "进入任务列表失败后")
            for entry in self._iter_resilient_candidates(settings):
                self.check_cancel()
                if not self.task_progress_total:
                    scan_total = int((self.last_scan_summary or {}).get("total") or 0)
                    if scan_total:
                        self.task_progress_total = scan_total
                        self._report_task_progress()
                item = entry.get("item") or {}
                task_name = _normalize_text(item.get("taskName")) or "未命名任务"
                task_guid = _normalize_text(entry.get("guid"))
                if task_name.casefold() in skip_names:
                    summary["skip_list_tasks"] += 1
                    summary["skip_reasons"].append(
                        {
                            "guid": entry.get("guid"),
                            "task": task_name,
                            "type": "skip_list",
                            "reason": "命中任务名称跳过表",
                        }
                    )
                    self.log(
                        "跳过任务《%s》GUID=%s：命中任务名称跳过表"
                        % (task_name, entry.get("guid"))
                    )
                    self.task_progress_done += 1
                    self._report_task_progress()
                    continue
                attempts = 0
                while True:
                    try:
                        saved_progress_handler = self.progress_handler
                        self.progress_handler = None
                        try:
                            result = self.process_task(entry, settings)
                        finally:
                            self.progress_handler = saved_progress_handler
                        if attempts:
                            self.log(
                                "任务《%s》刷新后重试成功（第 %d 次）"
                                % (task_name, attempts)
                            )
                        break
                    except Exception as exc:
                        if self.stop_requested:
                            raise
                        reason = _redact_urls(exc)
                        retryable = not isinstance(exc, WriteResultUnknown)
                        if attempts < self.task_retries and retryable:
                            self.log(
                                "任务《%s》异常，刷新网页后重试本条"
                                "（第 %d/%d 次）：%s"
                                % (
                                    task_name,
                                    attempts + 1,
                                    self.task_retries,
                                    reason[:300],
                                )
                            )
                            self._refresh_and_resume(settings, "任务异常后重试")
                            try:
                                fresh_entry = self._find_task_entry(
                                    settings, task_guid, entry.get("page")
                                )
                            except Exception as find_exc:
                                fresh_entry = None
                                self.log(
                                    "刷新后查找任务《%s》失败：%s"
                                    % (task_name, _redact_urls(find_exc)[:300])
                                )
                            if fresh_entry is not None:
                                self._drop_task_abort_anomalies(task_guid)
                                entry = fresh_entry
                                item = entry.get("item") or item
                                attempts += 1
                                continue
                            self.log("刷新后任务《%s》已不在列表，跳过重试" % task_name)
                        else:
                            self.log(
                                "任务《%s》异常，刷新网页后跳过：%s"
                                % (task_name, reason[:300])
                            )
                        self.last_run_summary["unknown_errors"] += 1
                        if isinstance(exc, WriteResultUnknown) and append_skip_task_name(
                            self.skip_tasks_file, task_name
                        ):
                            self.skip_task_names.add(task_name.casefold())
                            self.log("已写入任务名称跳过表：%s" % task_name)
                        self._refresh_and_resume(settings, "任务异常后")
                        result = {
                            "status": "skipped",
                            "images": self.reviewed_count,
                            "reason": reason,
                            "unknown_error": True,
                        }
                        break
                self.last_run_summary["tasks_processed"] += 1
                self.last_run_summary["images"] += int(result.get("images") or 0)
                self.task_progress_done += 1
                self._report_task_progress()
                status = result.get("status")
                system_defects = result.get("system_defects") or []
                load_failures = result.get("load_failures") or []
                button_not_ready = result.get("button_not_ready") or []
                # 输出目录跳过表只收集三类：系统缺陷框、图片加载失败、未审核数超阈值。
                if result.get("threshold_skipped") or system_defects or load_failures:
                    self._record_output_skip_task(task_name)
                if system_defects:
                    self.last_run_summary["system_defect_images"] += len(system_defects)
                    self.last_run_summary["system_defect_tasks"] += 1
                if load_failures:
                    self.last_run_summary["image_load_failures"] += len(load_failures)
                    self.last_run_summary["image_load_failure_tasks"] += 1
                if button_not_ready:
                    self.last_run_summary["button_not_ready_images"] += len(
                        button_not_ready
                    )
                    self.last_run_summary["button_not_ready_tasks"] += 1
                if result.get("threshold_skipped"):
                    self.last_run_summary["threshold_skipped"] += 1
                if status == "deferred" and (load_failures or button_not_ready):
                    if append_skip_task_name(self.skip_tasks_file, task_name):
                        self.skip_task_names.add(task_name.casefold())
                        self.log("已写入任务名称跳过表：%s" % task_name)
                    elif task_name.casefold() not in self.skip_task_names:
                        self.log(
                            "警告：任务《%s》未能写入任务名称跳过表，"
                            "请检查文件是否被占用或不可写" % task_name
                        )
                if status == "completed":
                    self.last_run_summary["completed"] += 1
                    continue
                reason = str(result.get("reason") or "未知原因")
                if result.get("threshold_skipped"):
                    item_type = "unreviewed_threshold"
                elif result.get("unknown_error"):
                    item_type = "unknown_error"
                elif load_failures and not system_defects:
                    item_type = "image_failure"
                elif button_not_ready and not load_failures and not system_defects:
                    item_type = "button_not_ready"
                elif status == "deferred":
                    item_type = "system_defect"
                else:
                    item_type = "skipped"
                self.last_run_summary["skip_reasons"].append(
                    {
                        "guid": entry.get("guid"),
                        "task": task_name,
                        "type": item_type,
                        "reason": reason,
                    }
                )
                if status == "deferred":
                    self.last_run_summary["deferred"] += 1
                    self.log(
                        "转人工任务《%s》GUID=%s：%s"
                        % (task_name, entry.get("guid"), reason)
                    )
                else:
                    self.last_run_summary["skipped"] += 1
                    self.log(
                        "跳过任务《%s》GUID=%s：%s"
                        % (task_name, entry.get("guid"), reason)
                    )
            if not self.task_progress_total:
                self.task_progress_total = int(
                    (self.last_scan_summary or {}).get("total") or 0
                )
            if self.task_progress_total:
                self.task_progress_done = self.task_progress_total
                self._report_task_progress()
        finally:
            self.last_run_summary = summary
            self._write_summary_xlsx(force=True)
            self._write_summary(summary)
        self._log_run_summary(summary)
        return summary

    def save_diagnostic(self, page, prefix: str) -> None:
        if page is None or page.is_closed():
            return
        self.diagnostic_number += 1
        path = self.outdir / (
            "%s_%02d_%s.png" % ("failure", self.diagnostic_number, prefix)
        )
        try:
            page.screenshot(path=str(path), full_page=False, timeout=10000)
            self.log("失败现场截图：%s" % path)
        except Exception as exc:
            self.log("失败截图保存失败：%s" % str(exc)[:200])

    def _cleanup_dialog(self) -> None:
        """取消/退出时只关闭 current_dialog 或带本脚本标记的弹窗。"""
        dialog = self.current_dialog
        if dialog is None:
            if self.page is None or self.page.is_closed():
                return
            try:
                dialogs = self.page.locator(".el-dialog:visible")
            except Exception:
                return
            for index in range(dialogs.count() - 1, -1, -1):
                candidate = dialogs.nth(index)
                try:
                    marker = candidate.get_attribute("data-shade-review-dialog")
                except Exception:
                    marker = None
                if str(marker or "").strip() == "1":
                    dialog = candidate
                    break
            if dialog is None:
                return
        if self.page is None or self.page.is_closed():
            return
        try:
            if not dialog.is_visible():
                return
            if self._visible_message_box() is not None:
                return
            button = self._find_close_button(dialog)
            if button is not None:
                self._dom_click(button)
            else:
                keyboard = getattr(self.page, "keyboard", None)
                if keyboard is not None:
                    keyboard.press("Escape")
                else:
                    self.page.press("Escape")
        except Exception:
            pass
        finally:
            self.current_dialog = None

    def close(self) -> None:
        # 先恢复宿主机关屏/休眠，避免后面断连或清理卡住时仍占着防休眠。
        self.release_keep_awake()
        # 用户点停止后必须立刻断开，不再花时间关闭审核弹窗，更不关闭浏览器。
        if not self.stop_requested:
            self._cleanup_dialog()
        if self.monitor is not None and self.page is not None:
            try:
                self.page.remove_listener("response", self.monitor)
            except Exception:
                pass
            self.monitor = None
        if self.stop_requested:
            self.log("已停止审核，正在断开受控浏览器连接（浏览器保持打开）")
        if self.playwright is not None:
            try:
                self.playwright.stop()
            except Exception:
                pass
            self.playwright = None
        if self.stop_requested:
            self.log("已断开受控浏览器连接")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="按筛选条件批量审核缺陷识别任务（独立试验脚本，需人工登录）"
    )
    parser.add_argument(
        "--task-status",
        choices=TASK_STATUS_OPTIONS,
        default=None,
        help="任务状态，默认待人工审核（未提供时交互选择）",
    )
    parser.add_argument(
        "--mission-type",
        choices=MISSION_TYPE_OPTIONS,
        default=None,
        help="作业性质，默认精细化巡检（未提供时交互选择）",
    )
    parser.add_argument(
        "--voltage-level",
        choices=VOLTAGE_LEVEL_OPTIONS,
        default=None,
        help="电压等级，默认交流220kV（未提供时交互选择）",
    )
    parser.add_argument(
        "--line-name",
        default=None,
        help="线路名称（必填，未提供时交互输入）",
    )
    parser.add_argument(
        "--start-date",
        default=None,
        help="开始日期 YYYY-MM-DD（必填，未提供时交互输入）",
    )
    parser.add_argument(
        "--end-date",
        default=None,
        help="结束日期 YYYY-MM-DD（必填，未提供时交互输入）",
    )
    parser.add_argument(
        "--scan-only", action="store_true", help="只扫描筛选结果，不进入任何任务"
    )
    parser.add_argument(
        "--launch", action="store_true", help="启动独立受控浏览器并等待人工登录"
    )
    parser.add_argument(
        "--manual-task-page",
        action="store_true",
        help="登录后等待用户手动进入缺陷识别任务列表（--launch 默认启用）",
    )
    parser.add_argument(
        "--start-url", default="", help="受控浏览器启动或复用时打开的完整网址"
    )
    parser.add_argument(
        "--cdp-url",
        default=_client_cdp_url() or DEFAULT_CDP_URL,
        help="本机浏览器 CDP 地址",
    )
    parser.add_argument(
        "--browser-path", default="", help="浏览器可执行文件路径（仅 --launch）"
    )
    parser.add_argument(
        "--profile-dir",
        default=str(runtime_root() / "defect_review_browser_profile"),
        help="受控浏览器独立配置目录（仅 --launch）",
    )
    parser.add_argument("--out", default="", help="日志和截图输出目录")
    parser.add_argument(
        "--ignore-defect-boxes",
        action="store_true",
        help="无视系统缺陷框，所有能加载的图片都点审核完成",
    )
    parser.add_argument(
        "--skip-tasks-file",
        default="跳过任务.txt",
        help="任务名称跳过表（xlsx/txt/csv，一列，首行标题），不存在时自动创建",
    )
    parser.add_argument(
        "--timeout", type=int, default=120, help="单步等待超时秒数，默认 120"
    )
    parser.add_argument(
        "--image-timeout",
        type=int,
        default=DEFAULT_IMAGE_TIMEOUT,
        help="单张图片审核按钮等待秒数，默认 10",
    )
    parser.add_argument(
        "--image-retries",
        type=int,
        default=DEFAULT_IMAGE_RETRIES,
        help="图片首次加载失败后的立即重试次数，默认 0",
    )
    parser.add_argument(
        "--max-unreviewed",
        type=int,
        default=DEFAULT_MAX_UNREVIEWED,
        help="新任务未审核数超过此值时跳过，默认 400",
    )
    parser.add_argument(
        "--unknown-timeout",
        type=int,
        default=DEFAULT_UNKNOWN_TIMEOUT,
        help="已弃用：异常统一刷新网页并固定 60 秒重试；保留参数仅为兼容旧命令",
    )
    parser.add_argument(
        "--login-timeout",
        type=int,
        default=DEFAULT_LOGIN_TIMEOUT,
        help="启动浏览器后等待人工登录的秒数，默认 300",
    )
    parser.add_argument(
        "--diagnose-button",
        action="store_true",
        help="对第一张图片做「审核完成」按钮 hover/遮罩/点击耗时诊断日志",
    )
    return parser


def main(argv=None, runner_factory=None) -> int:
    args = build_parser().parse_args(argv)
    if args.timeout <= 0:
        print("配置错误：--timeout 必须大于 0", file=sys.stderr)
        return 2
    if args.image_timeout <= 0:
        print("配置错误：--image-timeout 必须大于 0", file=sys.stderr)
        return 2
    if args.image_retries < 0:
        print("配置错误：--image-retries 不能小于 0", file=sys.stderr)
        return 2
    if args.max_unreviewed <= 0:
        print("配置错误：--max-unreviewed 必须大于 0", file=sys.stderr)
        return 2
    if args.unknown_timeout <= 0:
        print("配置错误：--unknown-timeout 必须大于 0", file=sys.stderr)
        return 2
    if args.login_timeout <= 0:
        print("配置错误：--login-timeout 必须大于 0", file=sys.stderr)
        return 2
    client_mode = _is_client_dev_script()
    input_fn = _client_input if client_mode else input
    try:
        settings = parse_settings(args, input_fn=input_fn)
        args.start_url = validate_start_url(args.start_url)
        _, port = validate_cdp_url(args.cdp_url)
    except (ValueError, RuntimeError) as exc:
        print("配置错误：%s" % exc, file=sys.stderr)
        return 2
    outdir = (
        Path(args.out).expanduser()
        if args.out
        else runtime_root()
        / ("自动审核试验_%s" % datetime.now().strftime("%Y%m%d_%H%M%S"))
    )
    outdir.mkdir(parents=True, exist_ok=True)
    factory = runner_factory or ReviewRunner
    runner = factory(args, outdir)
    try:
        runner.log("缺陷识别自动审核试验脚本启动；输出目录：%s" % outdir)
        if hasattr(runner, "keep_awake"):
            runner.keep_awake()
        start_origin = _safe_origin(args.start_url)
        runner.log(
            "系统地址：%s"
            % (
                start_origin + _page_path(args.start_url)
                if start_origin
                else "当前已打开标签页"
            )
        )
        runner.log(
            "筛选条件：任务状态=%s；作业性质=%s；电压等级=%s；线路名称=%s；"
            "开始日期=%s；结束日期=%s"
            % (
                settings.task_status,
                settings.mission_type,
                settings.voltage_level,
                settings.line_name,
                settings.start_date,
                settings.end_date,
            )
        )
        runner.load_playwright()
        if args.launch:
            if cdp_reachable(args.cdp_url):
                runner.log(
                    "检测到已有受控浏览器，直接复用；"
                    "如需全新登录请先关闭它或指定其他 --cdp-url"
                )
            else:
                launch_browser(args, port, runner.log)
        runner.connect(launched=args.launch)
        page = getattr(runner, "page", None)
        if args.launch:
            if client_mode or not _stdin_is_tty():
                runner.wait_for_login(args.login_timeout * 1000.0)
            else:
                try:
                    input("请在浏览器中登录并进入系统页面，然后按 Enter 继续……")
                except EOFError:
                    runner.wait_for_login(args.login_timeout * 1000.0)
        elif page is not None:
            if getattr(runner, "is_on_login_page", lambda: False)():
                runner.wait_for_login(args.login_timeout * 1000.0)
            elif client_mode and page.url in ("", "about:blank"):
                raise RuntimeError(
                    "受控浏览器当前是空白页；请加 --launch 让脚本打开浏览器并等待登录，"
                    "或先在客户端打开受控浏览器进入系统"
                )
        if not args.scan_only:
            runner.run(settings)
            return 0
        runner.open_task_list()
        runner.apply_filters(settings)
        count = 0
        for entry in runner.iter_task_candidates(settings, rescan=False):
            item = entry.get("item") or {}
            runner.log(
                "可进入任务：第 %s 页《%s》GUID=%s"
                % (
                    entry.get("page", "?"),
                    item.get("taskName", "未命名任务"),
                    entry.get("guid", ""),
                )
            )
            count += 1
        summary = getattr(runner, "last_scan_summary", {}) or {}
        runner.log(
            "只扫描模式结束：扫描 %d 条，可进入 %d 条，跳过 %d 条，"
            "未进入任何任务"
            % (
                summary.get("scanned", 0),
                count,
                summary.get("skipped", 0),
            )
        )
        return 0
    except KeyboardInterrupt:
        runner.stop_requested = True
        runner.log("用户中止，已停止运行，未再发送审核或提交请求")
        return 130
    except Exception as exc:
        runner.log("执行失败：%s" % _redact_urls(exc))
        runner.log(_redact_urls(traceback.format_exc()))
        try:
            runner.save_diagnostic(getattr(runner, "page", None), "error")
        except Exception:
            pass
        return 1
    finally:
        runner.close()


if __name__ == "__main__":
    sys.exit(main())
