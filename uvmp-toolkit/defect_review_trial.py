#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""缺陷识别任务自动审核试验脚本（客户端脚本测试可直接运行）。

依赖：Python 3.8+、Playwright（python3 -m pip install playwright）。
使用系统 Chrome/公司浏览器，无需执行 playwright install。自动登录为唯一模式：
账号密码由客户端实例传入（独立运行用 UVMP_DEFECT_USERNAME / UVMP_DEFECT_PASSWORD），
使用本机离线 OCR 识别验证码；启动网址可只填站点根地址，由应用自行跳转 `/login`，
脚本只被动识别登录页并登录，登录后自动进入「智能分析 → 智能识别 → 缺陷识别任务」。

首次启动受控浏览器（只扫描）：
  python3 defect_review_trial.py --launch --start-url <系统网址> --scan-only
脚本会启动浏览器并自动登录，然后只扫描候选；后续在该浏览器中直接运行：
  python3 defect_review_trial.py --scan-only        # 只扫描候选
  python3 defect_review_trial.py                    # 逐条审核并提交

正式模式只审核图片分组里 auditStatus 为 unreviewed 的未打勾图片（按钮为
「审核完成」）；已打勾图片（auditStatus 非 unreviewed）按钮是「重新审核」，
脚本一律跳过，避免点到已审核图片卡住。缩略图按 fileGuid 精确定位并点击，
不按第几张/序号盲点；只有补审没有 fileGuid 的 blob 失败图时，才按失败队列位置点击（见下）。already_annotation 或 taggingDetailInfo 有数据都表示系统
已经画出缺陷框：未打勾的跳过并写入输出目录的「审核汇总.xlsx」之「缺陷」工作表，
供人工复核（列名使用系统的「线路杆塔」，并附带电压等级、线路名称、缺陷描述等）。
图片框的 picDefectFlg 可能仍是 unlabeled，不能只信这个字段。
图片加载以「审核完成」按钮亮起且可用为判定标准，按钮亮起立即点击，不再等下载响应
或画布像素。点击后系统会自动切到下一张未审核图片，脚本正常流程不点缩略图；
只有加载失败、按钮不可用、当前图片是已打勾/系统缺陷框时，才兜底切到下一张未打勾图片。
图片按钮等待默认 10 秒；单张失败不立即重试，先切到下一张未打勾图片。所有其他
未打勾图片处理完后，再给失败图片各 1 次补审机会，仍失败才记录到
「审核汇总.xlsx」之「异常」工作表并结束该任务，不会反复卡在同一张。
首轮与补审共用同一套「完整未审核队列」语义：队列把接口里未审核、
未审核成功的图片按顺序与页面上可见且无对勾的缩略图直接配对，选中目标后
只点它自己配对到的那张缩略图，不按接口序号或可见项位置换算；缺陷框图、
按钮未就绪图夹在中间，或缩略图栏前面多出隐藏占位项，都不会错位。
补审按首轮失败顺序逐张处理，每次都重新计算队列；不要求高亮、文本或主图
fileGuid 身份。补审成功后目标 pictureGuid 必须真的进入本轮成功集合，
实际审到别的图按目标补审失败记录，不提交任务。
首轮出现失败后，下一张从队列里第一张尚未尝试的图开始，不回点已经失败的第一/前几张。
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
不中止整轮;刷新失败每 60 秒重试,只有用户主动停止才退出。刷新后重试重定位失败或目标已消失时，
按普通跳过结束（不记未知异常、不写跳过表）；只有真正进入重试后再次失败或不宜重试的最终异常
才记为未知异常任务。
进度条按任务总数统计,缺陷框/加载失败/阈值跳过均算完成;
完成任务会消失、后续页任务会前移(如第 3 页变第 2 页、第 4 页变第 3 页),
所以下一轮从上一任务所在页继续扫描;需要换页时直接跳转目标页码,
不逐页点「下一页」。刷新后重找同一任务时按目标 GUID 扫描，扫到该任务即返回，
浏览器停在它所在页，不会先扫到末页再回头点惰性行。
任务弹层以「任务名匹配当前任务且未审核文案已渲染」或「本任务缩略图已出现」为内容就绪；
顶部弹层加载蒙层（会包住首图遮罩或盖住「审核完成」按钮的那层）消失后才开始审核，
避免弹层还在加载就点首图。每次点击缩略图切换图片也会触发同一蒙层，脚本等它消失后
再识别切换后的图片或进入审核，避免把上一张图片当成刚点击的图片。首图自身的图片
加载遮罩不阻塞弹层就绪，单图加载仍按 --image-timeout 等待并在失败后补审/跳过。
弹层内容迟迟不渲染、关闭失败、写入结果不明等一律刷新网页回到任务列表继续，
失败每 60 秒重试，不中止整轮审核。
每次运行都会在输出目录写一份 run.log（与客户端任务日志内容一致，带日期时间）
和「跳过任务.txt」（缺陷框/加载失败/按钮未就绪/阈值跳过/最终未知异常等转人工任务名，同时就是默认跳过表，
可直接作为下次的 --skip-tasks-file），
便于事后核对与续跑；失败截图也在同一目录。审核结果逐条追加到「审核汇总.jsonl」
（进程中断也不丢已完成记录），「审核汇总.xlsx」每 20 个任务或运行结束时刷新一次，
避免长跑时反复重写整张表；配置为 xlsx 的任务名称跳过表同样批量刷新，txt/csv 即时追加。
运行汇总的「审核图片」按张次统计：同一任务刷新/健康重试后再次审核成功的张次也计入。
同一输出目录再次启动时先从「审核汇总.jsonl」逐行恢复历史（坏行/半截行跳过并告警、同时重写干净文件），
JSONL 没有可用记录时用旧「审核汇总.xlsx」恢复并补写 JSONL，重跑不重复登记已记录的缺陷与异常。
缺陷记录自动把照片原图保存到「输出目录/线路名称/杆塔号/原文件名」，并按系统缺陷框坐标
另存「文件名_标注」图片；不提供关闭开关，未勾选无视缺陷框时始终归档。归档前先查目标目录
同名原图与标注图，并以输出目录的 .照片归属索引.json（相对路径 -> fileGuid + 大小 + sha256）
确认是不是同一张照片：同一 fileGuid 且校验一致、两份齐全就直接跳过（不重复下载也不新增
(2)），只缺标注时读本地原图补标，原图缺失才下载；索引缺失且无法用当前缓存字节验证时宁可
另存 (2)，不把旧原图配新框。下载/标注失败的任务本轮不写跳过表（同一任务跨重试累计），
同目录重跑能补齐。原图下载头只由本站点文件下载接口的 GET 请求整体刷新，普通图片请求只补缺，
不会抹掉已捕获的鉴权/Cookie。下载拒绝 206/部分内容与超过 80MB 的响应，页面已加载的
blob/data 图只在确认属于本轮加载成功的那张照片时才采用。
系统缺陷框明细读不到（非 200、解析失败、等待超时）按异常中止该任务并走重试，不再用空列表
冒充「没有缺陷框」；只有接口明确返回空数组才算无框。
运行期间可请求宿主机关屏/休眠抑制（Windows：SetThreadExecutionState；Linux：
systemd-inhibit + xdg-screensaver/xset 定时重置），结束或停止后恢复。客户端
「机巡审核」启动卡片勾选「运行时防止关屏/休眠」后由客户端 Job 层启用；独立脚本
默认启用。系统不支持或设置失败时只记日志，不影响审核。
点停止后协作取消每 200ms 检查一次；浏览器点击/填表等动作本身最长等 5 秒，
不会因为一个卡住的动作让客户端一直停在“正在停止”。停止后不再关闭审核弹窗，
也不关闭受控浏览器。
任务名称跳过表未显式指定时用输出目录的「跳过任务.txt」；也支持 xlsx/csv，首行标题，其后每行一个任务名称；
表头多于一个字段的旧表（任务名称 + 备注/负责人）按首列取任务名称，单列表里任务名自带逗号也整行保留；
命中的任务直接跳过。同一输出目录多次启停继续往同一张表追加（用户手删才清空），
输出目录原本没有该文件时首轮没有跳过项，不读别处的旧全局表。本轮因图片加载失败、按钮未就绪
或最终仍未知异常转人工的任务，会自动追加到该表，本次运行的后续扫描及下次运行都不再进入；
照片下载/标注失败的任务例外，先留待下次补存；追加前会补齐缺失的换行，
xlsx 表只在 ZIP/XML 层给目标工作表首列补单元格，其他工作表、样式、公式、合并单元格与数字格式一律保留，
读取失败时不写入。写请求结果不明或出现未知二次确认时停止运行，不自动重发、不盲点未知按钮。

客户端「开发者工具 → 脚本测试」可直接选择本文件并传参，例如：
  --task-status 待人工审核 --mission-type 仿线巡检 --voltage-level 交流220kV
  --line-name 吕汾 --start-date 2026-01-01 --end-date 2026-11-08 --launch
加 --launch 时脚本会启动受控浏览器并自动登录（账号密码由客户端实例传入；
独立运行用环境变量 UVMP_DEFECT_USERNAME / UVMP_DEFECT_PASSWORD）。
启动网址可只填站点根地址，应用会自己跳到 /login；脚本只被动识别登录页，
不重复导航，登录成功后自动进入「智能分析 → 智能识别 → 缺陷识别任务」。
脚本会自动读取客户端配置的浏览器路径和 CDP 地址。若 CDP 端口已有受控浏览器，
会直接复用；需要全新登录时先关闭它或换 --cdp-url。

脚本只连接本机 CDP 调试端口，不导出 Cookie/token，不直接 fetch 审核写接口。
自动登录为唯一模式：账号密码只从客户端实例或上述环境变量读取，验证码使用
随包离线 OCR，不访问互联网。掉登录、页面 OOM/崩溃或浏览器重启后自动登录并
回到缺陷识别任务；看门狗长时间无活动会重启该实例浏览器。
恢复过程不关闭其他实例，也不主动关闭用户浏览器（只有看门狗/OOM 恢复会重启当前实例）。
"""

import argparse
import base64
import csv
import hashlib
import json
import math
import os
import re
import shutil
import struct
import subprocess
import sys
import threading
import time
import traceback
import weakref
import zipfile
import zlib
import xml.etree.ElementTree as ET
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Callable, Iterable, Optional, Sequence, Tuple
from urllib import request as urllib_request
from urllib.parse import parse_qs, urlencode, urlsplit

SCRIPT_DIR = Path(__file__).resolve().parent
# 脚本可单独运行，也可被客户端按文件加载；host_awake 在 toolkit/ 下。
_TOOLKIT_DIR = SCRIPT_DIR / "toolkit"
if _TOOLKIT_DIR.is_dir() and str(_TOOLKIT_DIR) not in sys.path:
    sys.path.insert(0, str(_TOOLKIT_DIR))
try:
    from host_awake import HostAwakeGuard
except ImportError:
    try:
        from toolkit.host_awake import HostAwakeGuard
    except ImportError:
        # 独立脚本/抓取包可能只带本文件，没有客户端 toolkit 目录；
        # 防休眠只是辅助能力，缺失时降级为不阻止休眠，不影响审核。
        HostAwakeGuard = None

try:
    import platform_utils
except ImportError:
    try:
        from toolkit import platform_utils
    except ImportError:
        platform_utils = None


def _run_hidden(args, **kwargs):
    """运行控制台子进程并隐藏窗口。

    Windows 上由 GUI 拉起的核心没有控制台，powershell/taskkill 子进程会各新分配一个
    可见控制台窗口（2026-11 实机反馈：审核过程中反复闪黑框）。统一走
    platform_utils.run_hidden（CREATE_NO_WINDOW + SW_HIDE，creationflags 按位合并）。
    独立脚本/抓取包只带本文件时没有 toolkit/，退化为本地等价实现。
    """
    if platform_utils is not None:
        return platform_utils.run_hidden(args, **kwargs)
    if os.name == "nt":
        kwargs["creationflags"] = int(kwargs.get("creationflags") or 0) | 0x08000000
        si = kwargs.get("startupinfo") or subprocess.STARTUPINFO()
        si.dwFlags |= subprocess.STARTF_USESHOWWINDOW
        si.wShowWindow = subprocess.SW_HIDE
        kwargs["startupinfo"] = si
    return subprocess.run(args, **kwargs)


def _taskkill_tree(pid, timeout: float = 10) -> None:
    """Windows 强杀指定进程及其子树（隐藏控制台）；其他平台不处理。"""
    if platform_utils is not None:
        platform_utils.run_taskkill(pid, timeout=timeout)
        return
    if os.name != "nt":
        return
    try:
        _run_hidden(
            ["taskkill", "/PID", str(pid), "/T", "/F"],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            timeout=timeout,
        )
    except Exception:  # noqa: BLE001
        pass

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
HOME_PAGE_PATH = "/home"
# 登录成功后应用先跳 /home；给它留出路由跳转和菜单渲染时间，避免过早直开任务页。
_HOME_SETTLE_TIMEOUT_MS = 15000
# 登录页与任务页不是同一个路由；脚本只被动识别应用跳转到的 /login，
# 不主动拼登录地址（启动网址可只填站点根地址）。
LOGIN_PAGE_PATH = "/login"
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
# 登录后准备任务列表/筛选连续失败上限；避免下拉/遮罩持续异常时无限重试刷日志。
_PREPARE_MAX_FAILURES = 3
# 自动登录失败后的重试间隔（无上限）：验证码错误快速换图，账号/服务端错误逐步退避。
_LOGIN_RETRY_SECONDS = 3
_LOGIN_RETRY_MAX_SECONDS = 60
# 浏览器刚启动/路由跳转时，先等应用外壳或登录表单渲染出来，避免误判掉登录。
_LOGIN_SETTLE_TIMEOUT_MS = 15000
# Playwright 驱动进程退出后的错误特征（Playwright 1.62 实测文本）。
# 命中后同一线程 stop() 旧驱动并 start() 新驱动即可恢复；每个重连周期最多重建一次，
# 避免驱动持续崩溃时无限重建。连接成功后计数清零，下一次运行期故障仍可再兜底。
_DRIVER_CLOSED_MARKERS = (
    "connection closed while reading from the driver",
    "driver closed",
)
_DRIVER_RESET_LIMIT = 1
# 看门狗：任务线程超过这么久没有任何日志/进度，判定页面/CDP 静默卡死。
_DEFAULT_STALL_TIMEOUT = 120
# 页面崩溃后看门狗强制关浏览器的重试间隔；主线程若卡在 Playwright 调用里，
# 关闭浏览器才能让挂起的调用抛错，从而进入自动重启链路。
_CRASH_RESTART_RETRY_SECONDS = 10
# 单张图片从上一张成功到下一张成功的墙钟时间超过阈值，连续 N 次判定实例不健康。
_DEFAULT_SLOW_IMAGE_SECONDS = 15
_DEFAULT_SLOW_IMAGE_COUNT = 3
# 健康重启冷却，避免外部服务持续慢时每张图都重启浏览器。
_HEALTH_RESTART_COOLDOWN = 300
# 自动登录点击“登录”后等待跳转/错误提示的时长。
_LOGIN_RESULT_TIMEOUT_MS = 20000

# 系统已识别的缺陷框：图片分组接口通过 picDefectFlg 标记，现场抓包值为
# already_annotation；这类图片必须交人工复核，不能由脚本点「审核完成」。
SYSTEM_DEFECT_FLAG = "already_annotation"
KNOWN_PIC_DEFECT_FLAGS = frozenset(("", "unlabeled", "no_defect", SYSTEM_DEFECT_FLAG))
SUMMARY_XLSX_NAME = "审核汇总.xlsx"
SUMMARY_JSONL_NAME = "审核汇总.jsonl"
SKIP_TASKS_OUTPUT_NAME = "跳过任务.txt"
# 缺陷照片归档：输出目录/线路名称/杆塔号/原文件名；系统缺陷框另存一份
# 同名 + 后缀的标注图，原图保持字节不变。
PHOTO_MARKED_SUFFIX = "_标注"
PHOTO_UNKNOWN_LINE = "未识别线路"
PHOTO_UNKNOWN_TOWER = "未识别杆塔"
PHOTO_DOWNLOAD_TIMEOUT_MS = 30000
PHOTO_PART_MAX_LENGTH = 60
PHOTO_NAME_MAX_LENGTH = 120
# 照片归属索引：输出目录下的隐藏 JSON，记录「相对路径 -> fileGuid + 大小 + sha256」。
# 跨运行重跑时据此确认同名文件是不是同一张照片，避免把旧原图配新框。
PHOTO_INDEX_NAME = ".照片归属索引.json"
PHOTO_INDEX_VERSION = 1
# 审核弹层已加载的原图响应直接复用字节：页面自身请求已通过鉴权，
# 避免归档时再发一次 downLoadFile 触发 401，也避免拿到缩略图。
PHOTO_RESPONSE_CACHE_LIMIT = 12
PHOTO_RESPONSE_MAX_BYTES = 80 * 1024 * 1024
# 审核页缩略图为 200×150（含竖版 150×200）；页面只加载到这种小图时不能当原图归档。
PHOTO_THUMBNAIL_MAX_LONG_SIDE = 200
PHOTO_THUMBNAIL_MAX_SHORT_SIDE = 150
_UNSAFE_PATH_CHARS = re.compile(r'[<>:"/\\|?*\x00-\x1f\x7f]')
_WINDOWS_RESERVED_NAMES = frozenset(
    ["con", "prn", "aux", "nul"]
    + ["com%d" % index for index in range(1, 10)]
    + ["lpt%d" % index for index in range(1, 10)]
)
# 汇总 xlsx 与配置的 xlsx 跳过表都按批刷新，避免长跑时每个任务重写整张表。
SUMMARY_WRITE_INTERVAL_TASKS = 20
SUMMARY_WRITE_ROW_THRESHOLD = 2000
SKIP_TABLE_FLUSH_INTERVAL_NAMES = 20
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
SCRIPT_VERSION = "2026-11-12.01"

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
# 多实例可能共用同一张跳过表：串行化“读表-判重-追加”，避免并发丢条目。
_SKIP_TABLE_LOCK = threading.Lock()

# 在页面内一次性找出当前高亮缩略图的下标：语义与 _has_active_evidence 一致，
# 但把每张缩略图 6 层祖先、多个属性的读取合并成一次 evaluate，避免长任务里
# 每审一张图就把整条缩略图栏重扫一遍（原来第 k 张约 30k 次 CDP 往返）。
_ACTIVE_THUMBNAIL_SCRIPT = """(el, startIndex) => {
  const selectors = [
    '.manual-audit .img-list-wrap li',
    '.img-list-wrap li',
    'li',
  ];
  let items = null;
  for (const selector of selectors) {
    const nodes = el.querySelectorAll(selector);
    if (nodes.length) {
      items = nodes;
      break;
    }
  }
  if (!items) return -1;
  const active = (node) => {
    let current = node;
    for (let depth = 0; depth < 6 && current; depth += 1) {
      const classes = String(current.getAttribute('class') || '');
      for (const token of classes.toLowerCase().split(/\\s+/)) {
        if (!token) continue;
        if (
          ['active', 'selected', 'current', 'is-active', 'is-selected',
           'is-current'].includes(token)
        ) {
          return true;
        }
        if (token.endsWith('-active') || token.endsWith('-selected')) {
          return true;
        }
      }
      if (
        String(current.getAttribute('aria-selected') || '')
          .trim().toLowerCase() === 'true'
      ) {
        return true;
      }
      const ariaCurrent = current.getAttribute('aria-current');
      if (ariaCurrent !== null) {
        const value = String(ariaCurrent).trim().toLowerCase();
        if (value !== '' && value !== 'false') return true;
      }
      if (
        String(current.getAttribute('data-active') || '')
          .trim().toLowerCase() === 'true'
      ) {
        return true;
      }
      current = current.parentElement;
    }
    return false;
  };
  const start = Math.max(0, Number(startIndex) || 0);
  for (let index = start; index < items.length; index += 1) {
    let target = items[index];
    const box = target.querySelector('.box');
    if (box) target = box;
    if (active(target)) return index;
  }
  return -1;
}"""


def _normalize_text(value: object) -> str:
    return " ".join(str(value or "").split())


def _safe_path_part(
    value: object, fallback: str, limit: int = PHOTO_PART_MAX_LENGTH
) -> str:
    """把线路名/杆塔号/文件名压成单个安全的 Windows/POSIX 路径段。"""
    text = _UNSAFE_PATH_CHARS.sub("_", _normalize_text(value)).strip(" .")
    if text in (".", ".."):
        text = ""
    if not text:
        return fallback
    if text.casefold() in _WINDOWS_RESERVED_NAMES:
        text = "_" + text
    if len(text) > limit:
        text = text[:limit].rstrip(" .")
    return text or fallback


def _photo_extension(mime: object, fallback: str = ".jpg") -> str:
    text = str(mime or "").split(";")[0].strip().casefold()
    return {
        "image/jpeg": ".jpg",
        "image/jpg": ".jpg",
        "image/png": ".png",
        "image/gif": ".gif",
        "image/webp": ".webp",
        "image/bmp": ".bmp",
        "image/tiff": ".tif",
        "image/svg+xml": ".svg",
        "image/heic": ".heic",
        "image/heif": ".heif",
    }.get(text, fallback)


def _safe_photo_extension(suffix: object, mime: object) -> str:
    text = str(suffix or "")
    if re.fullmatch(r"\.[A-Za-z0-9]{1,10}", text):
        return text
    return _photo_extension(mime)


def _photo_file_name(image: object, mime: object) -> str:
    """保留系统原文件名（含大小写扩展名）；缺扩展名时按实际图片类型补齐。"""
    original = _normalize_text(getattr(image, "original_name", ""))
    fallback_stem = (
        _normalize_text(
            getattr(image, "file_guid", "") or getattr(image, "picture_guid", "")
        )
        or "照片"
    )
    if original:
        base = re.split(r"[\\/]", original)[-1]
        stem, suffix = os.path.splitext(base)
        name = _safe_path_part(stem, fallback_stem) + _safe_photo_extension(
            suffix, mime
        )
    else:
        name = _safe_path_part(fallback_stem, "照片") + _photo_extension(mime)
    if len(name) > PHOTO_NAME_MAX_LENGTH:
        stem, suffix = os.path.splitext(name)
        name = stem[: PHOTO_NAME_MAX_LENGTH - len(suffix)].rstrip(" .") + suffix
    return name


def _annotated_photo_name(photo_name: str, output_mime: object) -> str:
    stem, suffix = os.path.splitext(photo_name)
    wanted = _photo_extension(output_mime)
    same_mime_suffixes = {
        "image/jpeg": (".jpg", ".jpeg", ".jpe"),
        "image/png": (".png",),
        "image/webp": (".webp",),
        "image/gif": (".gif",),
        "image/bmp": (".bmp",),
    }.get(str(output_mime or "").split(";")[0].strip().casefold(), ())
    extension = suffix if suffix.casefold() in same_mime_suffixes else wanted
    return "%s%s%s" % (stem, PHOTO_MARKED_SUFFIX, extension)


def _photo_label(item: dict) -> str:
    label = _normalize_text(item.get("defectName"))
    if not label:
        label = "/".join(
            part
            for part in (
                _normalize_text(item.get("componentName")),
                _normalize_text(item.get("partName")),
                _normalize_text(item.get("defectDescriptionName")),
            )
            if part
        )
    detailed = _normalize_text(item.get("detailedDescription"))
    if detailed and detailed not in label:
        label = "%s（%s）" % (label, detailed) if label else detailed
    if len(label) > 24:
        label = label[:23] + "…"
    return label


def _photo_boxes(details: object) -> list:
    """把系统缺陷框明细转成画布标注参数；坐标无效的项直接忽略。"""
    boxes = []
    if not isinstance(details, list):
        return boxes
    for index, item in enumerate(details):
        if not isinstance(item, dict):
            continue
        try:
            coords = [
                float(item.get(key)) for key in ("xmin", "ymin", "xmax", "ymax")
            ]
        except (TypeError, ValueError):
            continue
        if not all(math.isfinite(value) for value in coords):
            continue
        xmin, ymin, xmax, ymax = coords
        if xmax < xmin:
            xmin, xmax = xmax, xmin
        if ymax < ymin:
            ymin, ymax = ymax, ymin
        normalized = max(abs(value) for value in coords) <= 1.0
        minimum = 0.001 if normalized else 1.0
        if xmax - xmin < minimum or ymax - ymin < minimum:
            continue
        boxes.append(
            {
                "xmin": xmin,
                "ymin": ymin,
                "xmax": xmax,
                "ymax": ymax,
                "label": _photo_label(item) or ("缺陷%d" % (index + 1)),
                "normalized": normalized,
            }
        )
    return boxes


def _detect_image_mime(data: bytes) -> str:
    """按文件头识别图片类型；未知类型返回空字符串。"""
    if not data:
        return ""
    if data.startswith(b"\x89PNG\r\n\x1a\n"):
        return "image/png"
    if data.startswith(b"\xff\xd8\xff"):
        return "image/jpeg"
    if data.startswith(b"GIF8"):
        return "image/gif"
    if data[:4] == b"RIFF" and data[8:12] == b"WEBP":
        return "image/webp"
    if data[:2] == b"BM":
        return "image/bmp"
    if data[:4] in (b"II*\x00", b"MM\x00*"):
        return "image/tiff"
    head = data.lstrip()[:200].lower()
    if head.startswith(b"<svg"):
        return "image/svg+xml"
    if head.startswith(b"<?xml"):
        # XML 声明本身不是图片：必须确有 <svg 根元素才算 SVG，
        # 否则 <Error><Code>AccessDenied</Code></Error> 会被误当照片。
        _, _, root = head.partition(b"?>")
        if root.lstrip().startswith(b"<svg"):
            return "image/svg+xml"
    return ""


def _photo_output_mime(mime: object) -> str:
    text = str(mime or "").split(";")[0].strip().casefold()
    if text in ("image/jpeg", "image/jpg"):
        return "image/jpeg"
    if text in ("image/png", "image/webp"):
        return text
    return "image/png"


def _is_thumbnail_photo_url(value: object) -> bool:
    """判断图片地址是否缩略图；归档只接受原图地址。"""
    return bool(
        re.search(
            r"(?i)(?:[?&]isThumb=(?:yes|1|true)\b|/thumb\b)",
            str(value or ""),
        )
    )


def _photo_guid_from_url(value: object) -> str:
    """从图片下载地址提取 fileGuid，用于和当前审核图片匹配。"""
    try:
        params = parse_qs(urlsplit(str(value or "")).query)
    except ValueError:
        return ""
    for key, values in params.items():
        if key.casefold() == "fileguid" and values:
            return _normalize_text(values[0]).casefold()
    return ""


def _is_file_download_request(url: object) -> bool:
    """是否精确命中文件下载接口路径；query 里带同名路径的代理不算。"""
    try:
        return urlsplit(str(url or "")).path == FILE_DOWNLOAD_API
    except ValueError:
        return False


def _photo_index_key(path: Path, root: Path) -> str:
    """索引键：相对输出目录的 POSIX 路径；绝对路径退化为文件名。"""
    try:
        return path.relative_to(root).as_posix()
    except ValueError:
        return path.name


def _file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    try:
        with path.open("rb") as handle:
            while True:
                chunk = handle.read(1024 * 1024)
                if not chunk:
                    break
                digest.update(chunk)
    except OSError:
        return ""
    return digest.hexdigest()


def _response_length(response) -> int:
    """读取响应声明的 Content-Length；缺失或非法时返回 0（未知）。"""
    try:
        value = (getattr(response, "headers", None) or {}).get("content-length")
        return max(0, int(value or 0))
    except (TypeError, ValueError):
        return 0


# 页面内下载原图兜底：context.request 不带页面脚本注入的鉴权头时使用。
_PHOTO_FETCH_SCRIPT = r"""
async (payload) => {
  const { url, headers, maxBytes } = payload || {};
  const response = await fetch(url, {
    credentials: 'include',
    headers: headers || {},
  });
  if (!response.ok) return { error: `HTTP ${response.status}` };
  // 206/Content-Range 只是原图的一段；超过上限的响应也不归档。
  if (response.status === 206 || response.headers.get('content-range')) {
    return { error: 'partial content' };
  }
  const declared = Number(response.headers.get('content-length') || 0);
  if (maxBytes && declared > maxBytes) return { error: 'too large' };
  const buffer = await response.arrayBuffer();
  if (maxBytes && buffer.byteLength > maxBytes) return { error: 'too large' };
  const bytes = new Uint8Array(buffer);
  let binary = '';
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode.apply(null, bytes.subarray(i, i + chunk));
  }
  return { b64: btoa(binary), type: response.headers.get('content-type') || '' };
}
"""

# 页面内取已加载图片：只接受当前 fileGuid 的主图，不拿页面其他大图顶替。
_PHOTO_LOADED_SCRIPT = r"""
async (payload) => {
  const guid = String((payload && payload.guid) || '').toLowerCase();
  // 没有当前 fileGuid 就没有可归档的依据：宁可丢这一张，也不存错照片。
  if (!guid) return null;
  const matches = (src) => String(src || '').toLowerCase().includes(guid);
  // blob:/data: 地址无法带上 fileGuid，但它们是页面正在显示的这张图；
  // 其余 http 地址必须带当前 fileGuid，否则可能是别的照片。
  const acceptable = (src) =>
    matches(src) || /^(blob:|data:)/i.test(String(src || ''));
  const toResult = async (blob, width, height, src = '') => {
    if (!blob || !String(blob.type || '').startsWith('image/')) return null;
    const buffer = await blob.arrayBuffer();
    const bytes = new Uint8Array(buffer);
    let binary = '';
    const chunk = 0x8000;
    for (let i = 0; i < bytes.length; i += chunk) {
      binary += String.fromCharCode.apply(null, bytes.subarray(i, i + chunk));
    }
    return {
      b64: btoa(binary),
      type: blob.type || '',
      width: Number(width) || 0,
      height: Number(height) || 0,
      src: String(src || ''),
      thumbnail: /(?:[?&]isThumb=yes\b|\/thumb\b)/i.test(String(src || '')),
    };
  };
    const images = Array.from(document.images || [])
    .map((img) => {
      const src = String(img.currentSrc || img.src || '');
      const width = Number(img.naturalWidth || img.width) || 0;
      const height = Number(img.naturalHeight || img.height) || 0;
      const rect = img.getBoundingClientRect ? img.getBoundingClientRect() : null;
      const area = width && height ? width * height : rect ? rect.width * rect.height : 0;
      return { img, src, width, height, area };
    })
    .filter((item) => item.src && item.area >= 120 * 120 && acceptable(item.src))
    .sort((a, b) => (matches(b.src) ? 1 : 0) - (matches(a.src) ? 1 : 0) || b.area - a.area);
  for (const item of images) {
    try {
      const response = await fetch(item.src, { credentials: 'include' });
      if (response.ok) {
        const result = await toResult(
          await response.blob(),
          item.width,
          item.height,
          item.src
        );
        if (result) return result;
      }
    } catch (_err) {}
    // fetch 被 401/CORS 拦截时，直接复用浏览器已经解码好的位图。
    try {
      const canvas = document.createElement('canvas');
      canvas.width = item.width;
      canvas.height = item.height;
      const ctx = canvas.getContext('2d');
      if (!ctx) continue;
      ctx.drawImage(item.img, 0, 0, item.width, item.height);
      const blob = await new Promise((resolve) =>
        canvas.toBlob((value) => resolve(value), 'image/png')
      );
      const result = await toResult(blob, item.width, item.height, item.src);
      if (result) return result;
    } catch (_err) {}
  }
  return null;
}
"""

# 只读当前页主图地址，不下载字节：优先复用页面实际使用的原图 URL。
_PHOTO_SOURCE_SCRIPT = r"""
() => {
  return Array.from(document.images || [])
    .map((img) => {
      const src = String(img.currentSrc || img.src || '');
      const width = Number(img.naturalWidth || img.width) || 0;
      const height = Number(img.naturalHeight || img.height) || 0;
      const rect = img.getBoundingClientRect ? img.getBoundingClientRect() : null;
      const area = width && height ? width * height : rect ? rect.width * rect.height : 0;
      return { src, area };
    })
    .filter((item) => item.src && item.area >= 120 * 120)
    .sort((a, b) => b.area - a.area)
    .slice(0, 8)
    .map((item) => item.src);
}
"""

# 页面内标注：在独立空白页解码原图，按系统坐标画红框，输出同格式图片。
_PHOTO_ANNOTATE_SCRIPT = r"""
async (payload) => {
  const { dataUrl, boxes, outputMime } = payload;
  const image = new Image();
  await new Promise((resolve, reject) => {
    image.onload = () => resolve(true);
    image.onerror = () => reject(new Error('图片解码失败'));
    image.src = dataUrl;
  });
  const width = image.naturalWidth || image.width;
  const height = image.naturalHeight || image.height;
  if (!width || !height) return null;
  const canvas = document.createElement('canvas');
  canvas.width = width;
  canvas.height = height;
  const ctx = canvas.getContext('2d');
  if (!ctx) return null;
  ctx.drawImage(image, 0, 0, width, height);
  const clamp = (value, minimum, maximum) =>
    Math.min(maximum, Math.max(minimum, value));
  const lineWidth = Math.max(3, Math.round(Math.min(width, height) / 300));
  const fontPx = Math.max(18, Math.round(Math.min(width, height) / 90));
  ctx.lineWidth = lineWidth;
  ctx.strokeStyle = '#FF2D2D';
  ctx.font = 'bold ' + fontPx + 'px sans-serif';
  ctx.textBaseline = 'top';
  let drawn = 0;
  let skipped = 0;
  for (const box of boxes || []) {
    let x1 = Number(box.xmin);
    let y1 = Number(box.ymin);
    let x2 = Number(box.xmax);
    let y2 = Number(box.ymax);
    if (![x1, y1, x2, y2].every(Number.isFinite)) { skipped += 1; continue; }
    if (box.normalized) {
      x1 *= width; x2 *= width; y1 *= height; y2 *= height;
      // 归一化坐标是相对比例，夹到画布范围内不会错位。
      x1 = clamp(x1, 0, width);
      x2 = clamp(x2, 0, width);
      y1 = clamp(y1, 0, height);
      y2 = clamp(y2, 0, height);
    } else if (x1 < 0 || y1 < 0 || x2 > width || y2 > height) {
      // 原图像素坐标落出画布说明框与图片尺寸不匹配：
      // 夹取会把框画到错误位置，直接丢弃并计数，由调用方记录。
      skipped += 1;
      continue;
    }
    if (x2 - x1 < 1 || y2 - y1 < 1) { skipped += 1; continue; }
    ctx.strokeRect(x1, y1, x2 - x1, y2 - y1);
    drawn += 1;
    const label = String(box.label || '');
    if (!label) continue;
    const pad = Math.max(4, Math.round(fontPx * 0.25));
    const textWidth = ctx.measureText(label).width;
    let top = y1 - fontPx - pad * 2;
    if (top < 0) top = Math.min(height - fontPx - pad * 2, y1 + lineWidth);
    ctx.fillStyle = 'rgba(245,63,63,0.92)';
    ctx.fillRect(x1, top, Math.min(width - x1, textWidth + pad * 2), fontPx + pad * 2);
    ctx.fillStyle = '#FFFFFF';
    ctx.fillText(label, x1 + pad, top + pad);
  }
  const url = canvas.toDataURL(outputMime || 'image/jpeg', 0.95);
  const comma = url.indexOf(',');
  if (comma < 0) return null;
  return {
    b64: url.slice(comma + 1),
    type: outputMime || 'image/jpeg',
    drawn: drawn,
    skipped: skipped,
    width: width,
    height: height,
  };
}
"""

SKIP_TASK_HEADER = "任务名称"


_XLSX_NS = {"m": "http://schemas.openxmlformats.org/spreadsheetml/2006/main"}


_XLSX_TEXT_PATH = "{http://schemas.openxmlformats.org/spreadsheetml/2006/main}t"
_XLSX_REL_ID = (
    "{http://schemas.openxmlformats.org/officeDocument/2006/relationships}id"
)
_XLSX_CELL_REF = re.compile(r"^([A-Z]+)(\d+)$")


def _xlsx_shared_strings(archive, names) -> list:
    if "xl/sharedStrings.xml" not in names:
        return []
    root = ET.fromstring(archive.read("xl/sharedStrings.xml"))
    return [
        "".join(node.text or "" for node in item.iter(_XLSX_TEXT_PATH))
        for item in root.findall("m:si", _XLSX_NS)
    ]


def _xlsx_sheet_entries(archive, names) -> list:
    """按 workbook.xml 顺序返回 (工作表名, 包内路径)，用于保留用户原有表名。"""
    fallback = sorted(
        name
        for name in names
        if name.startswith("xl/worksheets/sheet") and name.endswith(".xml")
    )
    if "xl/workbook.xml" not in names:
        return [("Sheet%d" % (index + 1), name) for index, name in enumerate(fallback)]
    targets = {}
    if "xl/_rels/workbook.xml.rels" in names:
        root = ET.fromstring(archive.read("xl/_rels/workbook.xml.rels"))
        for rel in root:
            rel_id = rel.get("Id")
            target = rel.get("Target")
            if rel_id and target:
                targets[rel_id] = target
    root = ET.fromstring(archive.read("xl/workbook.xml"))
    entries = []
    for index, sheet in enumerate(root.findall(".//m:sheets/m:sheet", _XLSX_NS)):
        target = targets.get(sheet.get(_XLSX_REL_ID) or "", "")
        path = target.lstrip("/")
        if path and not path.startswith("xl/"):
            path = "xl/" + path
        if path not in names:
            path = fallback[index] if index < len(fallback) else ""
        if not path or path not in names:
            continue
        entries.append((str(sheet.get("name") or "Sheet%d" % (index + 1)), path))
    return entries or [
        ("Sheet%d" % (index + 1), name) for index, name in enumerate(fallback)
    ]


def _xlsx_col_index(ref: str) -> int:
    match = _XLSX_CELL_REF.match(str(ref or ""))
    if not match:
        return 0
    index = 0
    for character in match.group(1):
        index = index * 26 + (ord(character) - 64)
    return index


def _xlsx_cell_value(cell, shared) -> object:
    cell_type = str(cell.get("t") or "")
    if cell_type == "inlineStr":
        return "".join(node.text or "" for node in cell.iter(_XLSX_TEXT_PATH))
    node = cell.find("m:v", _XLSX_NS)
    raw = node.text if node is not None else ""
    if cell_type == "s" and raw:
        try:
            index = int(raw)
        except ValueError:
            return ""
        return shared[index] if 0 <= index < len(shared) else ""
    if cell_type == "b":
        return "TRUE" if raw == "1" else "FALSE"
    if cell_type in ("str", "e"):
        return raw or ""
    if cell_type == "" and raw:
        # 数值单元格保留数字类型，避免重写后整列数字变成文本。
        try:
            number = float(raw)
        except ValueError:
            return raw
        if number.is_integer():
            return int(number)
        return number
    return raw or ""


def _xlsx_sheet_rows(root, shared) -> list:
    rows = []
    for row in root.findall(".//m:sheetData/m:row", _XLSX_NS):
        values = []
        for cell in row.findall("m:c", _XLSX_NS):
            column = _xlsx_col_index(str(cell.get("r") or ""))
            if column <= 0:
                values.append("")
                continue
            while len(values) < column - 1:
                values.append("")
            values.append(_xlsx_cell_value(cell, shared))
        rows.append(values)
    return rows


def read_xlsx_workbook(path) -> list:
    """读取 xlsx 全部工作表的值（不保留样式）；读失败抛异常由调用方判断。"""
    with zipfile.ZipFile(path) as archive:
        names = archive.namelist()
        shared = _xlsx_shared_strings(archive, names)
        return [
            (sheet_name, _xlsx_sheet_rows(ET.fromstring(archive.read(sheet)), shared))
            for sheet_name, sheet in _xlsx_sheet_entries(archive, names)
        ]


_SUMMARY_JSONL_KINDS = ("defect", "anomaly")


def _summary_jsonl_records(path) -> tuple:
    """逐行解析审核汇总 JSONL，返回 (记录列表, 损坏行数)。

    记录保持文件顺序；进程中断留下的半截行、损坏 JSON、类型不认识的行走不算，
    计入损坏行数由调用方告警。末行解析成功但没有换行符也按损坏计：不修就会让
    下一条追加和它粘成一行。文件读不出来时按「没有可恢复记录」返回，
    调用方据此不清空原文件（读不了不等于内容坏了）。
    """
    records = []
    try:
        text = path.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return records, 0
    if not text:
        return records, 0
    damaged = 0
    last_parsed = False
    for line in text.splitlines():
        if not line.strip():
            last_parsed = False
            continue
        try:
            item = json.loads(line)
        except ValueError:
            damaged += 1
            last_parsed = False
            continue
        kind = item.get("type") if isinstance(item, dict) else ""
        row = item.get("row") if isinstance(item, dict) else None
        if kind not in _SUMMARY_JSONL_KINDS or not isinstance(row, list) or not row:
            damaged += 1
            last_parsed = False
            continue
        records.append((kind, row))
        last_parsed = True
    if last_parsed and not text.endswith(("\n", "\r")):
        damaged += 1
    return records, damaged


def _summary_xlsx_records(path) -> list:
    """用现有 xlsx 读取器恢复汇总行；读失败或表头不匹配时返回已读到的部分。

    按表头名称映射回标准列序：列被用户拖动过、少了末尾空列也能还原。
    """
    try:
        sheets = read_xlsx_workbook(path)
    except Exception:
        return []
    records = []
    for sheet_name, rows in sheets:
        if sheet_name == DEFECT_SHEET_NAME:
            kind, headers = "defect", SYSTEM_DEFECT_HEADERS
        elif sheet_name == ANOMALY_SHEET_NAME:
            kind, headers = "anomaly", ANOMALY_HEADERS
        else:
            continue
        if not rows:
            continue
        positions = {}
        for index, caption in enumerate(rows[0]):
            positions.setdefault(_normalize_text(caption), index)
        if any(caption not in positions for caption in headers):
            continue
        for values in rows[1:]:
            if not any(_normalize_text(value) for value in values):
                continue
            records.append(
                (
                    kind,
                    [
                        values[positions[caption]]
                        if positions[caption] < len(values)
                        else ""
                        for caption in headers
                    ],
                )
            )
    return records


def _defect_row_key(row: list) -> tuple:
    """系统缺陷框行的去重键：优先图片 GUID，缺 GUID 时退化为整行内容。"""
    picture_guid = _normalize_text(row[13]) if len(row) > 13 else ""
    return picture_guid or tuple(_normalize_text(value) for value in row)


def _anomaly_row_key(row: list) -> tuple:
    """异常行去重键，与 _record_anomaly 登记时使用的键保持一致。"""
    return (
        _normalize_text(row[4]) if len(row) > 4 else "",
        row[1] if len(row) > 1 else "",
        _normalize_text(row[12]) if len(row) > 12 else "",
        row[2] if len(row) > 2 else "",
    )


def _xlsx_first_column(path, strict: bool = False) -> list:
    """读取第一个工作表的第一列；strict 时读失败抛异常。"""
    try:
        sheets = read_xlsx_workbook(path)
    except Exception:
        if strict:
            raise
        return []
    if not sheets:
        return []
    return [row[0] if row else "" for row in sheets[0][1]]


_SKIP_TABLE_READ_ERROR = "跳过表读取失败"


def _skip_table_lines(path: Path, suffix: str) -> list:
    """读取跳过表每行的任务名称文本。

    表头多于一个字段说明是带备注/负责人的旧多列表，数据行只取首列；
    表头只有一列时整行都属于任务名（任务名本身可能含逗号）。
    txt 的旧多列表可能用制表符或逗号分隔，两者都按表头字段数判定。
    """
    if suffix == ".csv":
        with path.open("r", encoding="utf-8-sig", newline="") as handle:
            rows = list(csv.reader(handle))
        width = len(rows[0]) if rows else 0
        if width <= 1:
            # 单列 csv 被逗号拆开时按逗号拼回，任务名里的逗号不能丢。
            return [",".join(row) for row in rows]
        return [row[0] if row else "" for row in rows]
    lines = path.read_text(encoding="utf-8-sig").splitlines()
    if not lines:
        return []
    if len(lines[0].split("\t")) > 1:
        return [line.split("\t")[0] for line in lines]
    if "," in lines[0]:
        # 表头本身带逗号：按 csv 口径取首列（数据行里的逗号属于备注列）
        return [row[0] if row else "" for row in csv.reader(lines)]
    return lines


def _read_skip_table_names(path, strict: bool = False):
    """读取跳过表任务名称，返回 (名称列表, 是否读取成功)。

    读取失败与空表必须区分：失败时按空表重写会把整张表清空。
    """
    if path is None:
        return [], True
    file_path = Path(path)
    if not file_path.is_file():
        return [], True
    suffix = file_path.suffix.casefold()
    try:
        if suffix == ".xlsx":
            lines = _xlsx_first_column(file_path, strict=True)
        elif suffix in (".xls", ".xlsm"):
            raise RuntimeError("不支持读取 %s 格式的跳过表" % suffix)
        else:
            lines = _skip_table_lines(file_path, suffix)
    except Exception as exc:
        if strict:
            raise RuntimeError("%s：%s" % (_SKIP_TABLE_READ_ERROR, str(exc)[:200]))
        return [], False
    names = []
    seen = set()
    for index, line in enumerate(lines):
        # 首行是标题；其余行只取任务名称字段，不再按逗号截断。
        text = _normalize_text(str(line))
        if index == 0 or not text or text.startswith("#"):
            continue
        folded = text.casefold()
        if folded in seen:
            continue
        seen.add(folded)
        names.append(text)
    return names, True


def load_skip_task_name_rows(path, strict: bool = False) -> list:
    return _read_skip_table_names(path, strict=strict)[0]


def load_skip_task_names(path) -> set:
    """读取一列任务名称表：忽略首行标题，其余非空行按任务名称处理。"""
    return {name.casefold() for name in load_skip_task_name_rows(path)}


def _xlsx_cell_xml(ref: str, text: str) -> str:
    return '<c r="%s" t="inlineStr"><is><t>%s</t></is></c>' % (ref, _xlsx_escape(text))


_XLSX_DIMENSION = re.compile(
    r'(<dimension\b[^>]*?\bref="[A-Z]+\d+:[A-Z]+)(\d+)(")'
)


def _xlsx_touch_dimension(raw: str, last_row: int) -> str:
    """只把 dimension 的结束行号扩到新行；其余属性原样保留。"""

    def replace(match):
        return "%s%d%s" % (
            match.group(1),
            max(int(match.group(2)), last_row),
            match.group(3),
        )

    return _XLSX_DIMENSION.sub(replace, raw, count=1)


def _xlsx_row_tag(raw: str, number: int):
    """按 r 属性定位目标行；返回 (起点, 开标签结束位置, 是否自闭合)。"""
    match = re.search(r'<row\b[^>]*?\br="%d"(?=[\s/>])[^>]*?(/?)>' % number, raw)
    if match is None:
        return None
    return match.start(), match.end(), bool(match.group(1))


def _xlsx_append_row(raw: str, number: int, cell_xml: str) -> str:
    """在 sheetData 末尾补一行；没有 sheetData 时新建。"""
    row_xml = '<row r="%d">%s</row>' % (number, cell_xml)
    open_match = re.search(r"<sheetData\b[^>]*?(/?)>", raw)
    if open_match is None:
        close = raw.rfind("</worksheet>")
        if close < 0:
            raise RuntimeError("工作表结构无法识别")
        return raw[:close] + "<sheetData>%s</sheetData>" % row_xml + raw[close:]
    if open_match.group(1):
        return (
            raw[: open_match.start()]
            + "<sheetData>%s</sheetData>" % row_xml
            + raw[open_match.end() :]
        )
    close = raw.find("</sheetData>", open_match.end())
    if close < 0:
        raise RuntimeError("工作表 sheetData 未闭合")
    return raw[:close] + row_xml + raw[close:]


def _xlsx_put_header_cell(raw: str, number: int, text: str) -> str:
    """把标题写进指定行首列：已有 A 单元格就替换，没有就补在最前。

    某些写入器不写 row/c 的 r 属性，此时按 sheetData 里的顺序定位：
    第一行的第一个单元格就是 A 列，直接替换它，绝不新增重复单元格。
    """
    cell_xml = _xlsx_cell_xml("A%d" % number, text)
    tag = _xlsx_row_tag(raw, number)
    if tag is None:
        # 行缺少 r 属性：按 sheetData 里第一个 <row> 处理
        match = re.search(r"<row\b[^>]*?(/?)>", raw)
        if match is None:
            return _xlsx_append_row(raw, number, cell_xml)
        start, end, self_closing = match.start(), match.end(), bool(match.group(1))
    else:
        start, end, self_closing = tag
    if self_closing:
        opened = raw[start:end][:-2] + ">"
        return raw[:start] + opened + cell_xml + "</row>" + raw[end:]
    row_end = raw.find("</row>", end)
    segment_end = row_end if row_end >= 0 else len(raw)
    segment = raw[end:segment_end]
    first_cell = re.search(r"<c\b[^>]*?(?:/>|>.*?</c>)", segment, re.DOTALL)
    if first_cell is not None:
        cell_tag = re.match(r"<c\b[^>]*?/?>", first_cell.group(0))
        ref = re.search(r'\br="([A-Z]+)\d+"', cell_tag.group(0)) if cell_tag else None
        # 无 r 属性的首个单元格按规范就是 A 列
        if ref is None or ref.group(1) == "A":
            cell_start = end + first_cell.start()
            cell_stop = end + first_cell.end()
            return raw[:cell_start] + cell_xml + raw[cell_stop:]
    return raw[:end] + cell_xml + raw[end:]


def _xlsx_put_a_cell(raw: str, number: int, text: str) -> str:
    """在工作表 XML 里写入 A{number} 单元格，只动这个单元格或补一行。"""
    cell_xml = _xlsx_cell_xml("A%d" % number, text)
    tag = _xlsx_row_tag(raw, number)
    if tag is not None:
        start, end, self_closing = tag
        if self_closing:
            # 自闭合 <row .../>：展开成带单元格的行
            opened = raw[start:end][:-2] + ">"
            return raw[:start] + opened + cell_xml + "</row>" + raw[end:]
        row_end = raw.find("</row>", end)
        segment_end = row_end if row_end >= 0 else len(raw)
        existing = re.search(
            r'<c\b[^>]*?\br="A%d"(?=[\s/>])[^>]*?(?:/>|>.*?</c>)' % number,
            raw[end:segment_end],
            re.DOTALL,
        )
        if existing is not None:
            cell_start = end + existing.start()
            cell_stop = end + existing.end()
            return raw[:cell_start] + cell_xml + raw[cell_stop:]
        return raw[:end] + cell_xml + raw[end:]
    return _xlsx_append_row(raw, number, cell_xml)


def _write_zip_entries(path: Path, payloads: dict, infos: dict) -> None:
    """按原条目顺序写回 ZIP；先写临时文件再 os.replace，避免截断原表。"""
    temp_path = path.with_name(
        "%s.%d.%d.tmp" % (path.name, os.getpid(), threading.get_ident())
    )
    try:
        with zipfile.ZipFile(temp_path, "w", zipfile.ZIP_DEFLATED) as archive:
            for name, data in payloads.items():
                info = infos.get(name)
                item = zipfile.ZipInfo(
                    name,
                    date_time=info.date_time if info else time.localtime()[:6],
                )
                if info is not None:
                    # 保留原压缩方式与权限位，尽量少改动用户文件
                    item.compress_type = info.compress_type
                    item.external_attr = info.external_attr
                archive.writestr(item, data)
        os.replace(temp_path, path)
    finally:
        if temp_path.exists():
            try:
                temp_path.unlink()
            except OSError:
                pass


def _append_skip_names_xlsx(file_path: Path, names: Sequence[str]) -> int:
    """把任务名称追加到 xlsx 跳过表首列；其余内容一律不动。

    只在 ZIP/XML 层给目标工作表补 A 列单元格：整簿重建会丢样式、公式、
    合并单元格、数字格式、空表与用户自建的其他列。读失败直接抛出，
    调用方据此放弃写入，绝不按空表重写。
    """
    if not file_path.is_file() or file_path.stat().st_size == 0:
        rows = [
            [_normalize_text(name)] for name in names if _normalize_text(name)
        ]
        write_workbook_xlsx(
            file_path,
            [(SKIP_TASK_HEADER, (SKIP_TASK_HEADER,), rows)],
            log=lambda _message: None,
        )
        return len(rows)
    with zipfile.ZipFile(file_path) as archive:
        zip_names = archive.namelist()
        infos = {info.filename: info for info in archive.infolist()}
        payloads = {name: archive.read(name) for name in zip_names}
        shared = _xlsx_shared_strings(archive, zip_names)
        entries = _xlsx_sheet_entries(archive, zip_names)
        if not entries:
            raise RuntimeError("跳过表没有可写工作表")
        sheet_path = entries[0][1]
    raw = payloads[sheet_path].decode("utf-8")
    root = ET.fromstring(raw)
    row_numbers = []
    a_values = {}
    for index, row in enumerate(root.findall("m:sheetData/m:row", _XLSX_NS), 1):
        try:
            number = int(row.get("r"))
        except (TypeError, ValueError):
            number = row_numbers[-1] + 1 if row_numbers else index
        row_numbers.append(number)
        for cell in row.findall("m:c", _XLSX_NS):
            if _xlsx_col_index(str(cell.get("r") or "")) != 1:
                continue
            a_values[number] = _normalize_text(_xlsx_cell_value(cell, shared))
    last_row = max(row_numbers) if row_numbers else 0
    header_row = row_numbers[0] if row_numbers else 1
    existing = {text.casefold() for text in a_values.values() if text}
    header_written = False
    if not a_values.get(header_row):
        raw = _xlsx_put_header_cell(raw, header_row, SKIP_TASK_HEADER)
        header_written = True
    # 空表（sheetData 无行）时标题写在 header_row，数据行必须从它下面开始
    next_row = max(last_row, header_row)
    added = 0
    for name in names:
        text = _normalize_text(name)
        if not text or text.casefold() in existing:
            continue
        next_row += 1
        raw = _xlsx_put_a_cell(raw, next_row, text)
        existing.add(text.casefold())
        added += 1
    if not added and not header_written:
        return 0
    payloads[sheet_path] = _xlsx_touch_dimension(raw, next_row).encode("utf-8")
    _write_zip_entries(file_path, payloads, infos)
    return added


def append_skip_task_name(path, task_name: str) -> bool:
    """把任务名称追加到跳过表；表不存在时创建标题行。返回是否新增。"""
    with _SKIP_TABLE_LOCK:
        return _append_skip_task_name_locked(path, task_name)


def _same_path(left, right) -> bool:
    """两个路径是否指向同一文件；默认跳过表就是输出目录表。"""
    try:
        return os.path.normcase(os.path.abspath(str(left))) == os.path.normcase(
            os.path.abspath(str(right))
        )
    except (OSError, ValueError):
        return False


def _needs_leading_newline(file_path: Path) -> bool:
    """追加前判断末行是否缺换行符；缺了就直接追加会把两条任务名粘成一条。"""
    try:
        with file_path.open("rb") as handle:
            handle.seek(0, os.SEEK_END)
            if handle.tell() == 0:
                return False
            handle.seek(-1, os.SEEK_END)
            return handle.read(1) not in (b"\n", b"\r")
    except OSError:
        return False


def _append_skip_task_name_locked(path, task_name: str) -> bool:
    if path is None:
        return False
    name = _normalize_text(task_name)
    if not name:
        return False
    file_path = Path(path)
    suffix = file_path.suffix.casefold()
    try:
        # 读失败时绝不重写：按空表重写会把已有条目全部清空。
        existing, _ok = _read_skip_table_names(file_path, strict=True)
    except RuntimeError:
        return False
    if name.casefold() in {item.casefold() for item in existing}:
        return False
    if suffix == ".xlsx":
        try:
            return bool(_append_skip_names_xlsx(file_path, [name]))
        except Exception:
            return False
    if suffix in (".xls", ".xlsm"):
        return False
    try:
        is_new = not file_path.is_file() or file_path.stat().st_size == 0
        file_path.parent.mkdir(parents=True, exist_ok=True)
        prefix = "\n" if (not is_new and _needs_leading_newline(file_path)) else ""
        if suffix == ".csv":
            with file_path.open("a", encoding="utf-8", newline="") as handle:
                if prefix:
                    handle.write(prefix)
                writer = csv.writer(handle)
                if is_new:
                    writer.writerow([SKIP_TASK_HEADER])
                # 任务名称含逗号/引号时由 csv 负责加引号，避免拆成多列。
                writer.writerow([name])
            return True
        if is_new:
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


def _summary_line_values(task_info: dict, image: Optional[object]) -> tuple:
    """汇总行的（线路, 线路：杆塔, 杆塔），图片级信息优先、逐字段回退。

    一个任务可能跨多基杆塔：图片自身带了线路/杆塔就用图片的，缺哪个字段就
    回退任务级的该字段（只带杆塔的图片不能把线路留空、也不能拼出「：杆塔」），
    避免整任务都记成同一基杆塔或拼错组合。
    """
    info = task_info if isinstance(task_info, dict) else {}
    line = _normalize_text(getattr(image, "line_name", ""))
    tower = _normalize_text(getattr(image, "tower_name", ""))
    if not line and not tower:
        return (
            _normalize_text(info.get("line_name")),
            _normalize_text(info.get("line_tower")),
            _normalize_text(info.get("tower_name")),
        )
    line = line or _normalize_text(info.get("line_name"))
    tower = tower or _normalize_text(info.get("tower_name"))
    line_tower = "%s：%s" % (line, tower) if line and tower else (line or tower)
    return line, line_tower, tower


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
        "--disable-backgrounding-occluded-windows",
        "--disable-renderer-backgrounding",
        "--disable-background-timer-throttling",
        "--disable-features=CalculateNativeWinOcclusion",
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


def _cdp_port(cdp_url: str):
    try:
        return urlsplit(str(cdp_url)).port
    except ValueError:
        return None


def _browser_pids_for_port(port: int) -> list:
    """按调试端口定位受控浏览器进程；只用于无响应恢复，不影响其他实例。"""
    if not port:
        return []
    marker = "--remote-debugging-port=%d" % port
    pids = []
    if os.name == "nt":
        script = (
            "Get-CimInstance Win32_Process | Where-Object { "
            "$_.CommandLine -like '*%s*' } | "
            "Select-Object -ExpandProperty ProcessId" % marker
        )
        try:
            result = _run_hidden(
                ["powershell", "-NoProfile", "-Command", script],
                capture_output=True,
                text=True,
                timeout=15,
            )
            for line in result.stdout.splitlines():
                line = line.strip()
                if line.isdigit():
                    pids.append(int(line))
        except Exception:
            return []
        return pids
    proc_root = Path("/proc")
    if not proc_root.is_dir():
        return []
    for item in proc_root.iterdir():
        if not item.name.isdigit():
            continue
        try:
            raw = (item / "cmdline").read_bytes().replace(b"\x00", b" ").decode(
                "utf-8", "ignore"
            )
        except Exception:
            continue
        if marker in raw and any(
            name in raw.lower() for name in ("chrome", "chromium", "edge", "browser")
        ):
            pids.append(int(item.name))
    return pids


def _terminate_browser_pids(pids: list, log: Callable[[str], None]) -> None:
    for pid in pids:
        try:
            if os.name == "nt":
                _taskkill_tree(pid, timeout=10)
            else:
                os.kill(pid, 15)
        except Exception:
            pass
    if pids:
        log("已结束无响应的受控浏览器进程 %d 个" % len(pids))


def close_browser_for_restart(cdp_url: str, log: Callable[[str], None]) -> None:
    """先走 CDP Browser.close；无法连通时按调试端口杀进程。"""
    try:
        version_url = cdp_url.rstrip("/") + "/json/version"
        with urllib_request.urlopen(version_url, timeout=2) as response:
            info = json.loads(response.read().decode("utf-8", "replace") or "{}")
        ws_url = str(info.get("webSocketDebuggerUrl") or "")
        if ws_url:
            import websocket  # type: ignore

            ws = websocket.create_connection(ws_url, timeout=3)
            try:
                ws.send(json.dumps({"id": 1, "method": "Browser.close"}))
                try:
                    ws.recv()
                except Exception:
                    pass
            finally:
                ws.close()
            log("已通过 CDP 请求受控浏览器退出")
    except Exception:
        pass
    port = _cdp_port(cdp_url)
    if port and cdp_reachable(cdp_url):
        _terminate_browser_pids(_browser_pids_for_port(port), log)


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


class BrowserUnhealthy(ReviewStopped):
    """实例持续过慢，需要立即重启浏览器并从任务列表重入。"""


class WriteResultUnknown(ReviewStopped):
    """审核或提交写请求已发出，但结果无法确认。"""


class WriteRejected(ReviewStopped):
    """审核或提交写请求返回明确的业务失败。"""


class UnknownTaskError(ReviewStopped):
    """任务中出现未分类异常，先给 120 秒容错，再刷新页面继续下一任务。"""


class TaggingDetailError(ReviewStopped):
    """系统缺陷框明细读不到或读坏了：不能当成「没有缺陷框」继续审核。"""


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
    allow_all: bool = False,
) -> str:
    raw = getattr(args, attr, None)
    if raw is not None:
        value = str(raw).strip()
        if allow_all and value in ("", "全部"):
            return ""
        if value:
            if value not in options:
                raise ValueError(
                    "%s必须是以下之一：%s；当前值：%s"
                    % (label, "、".join(options), value)
                )
            return value
    choices = ("全部、" if allow_all else "") + "、".join(options)
    prompt = "请选择%s（可选：%s）[%s]：" % (label, choices, default)
    while True:
        try:
            answer = input_fn(prompt)
        except EOFError:
            raise RuntimeError("未提供%s，且当前终端无法交互输入" % label) from None
        value = (answer or "").strip()
        if not value:
            return default
        if allow_all and value == "全部":
            return ""
        if value in options:
            return value
        print(
            "无效的%s：%s；可选值：%s" % (label, value, choices),
            file=sys.stderr,
        )


def _read_optional(
    args: argparse.Namespace, attr: str, label: str, input_fn: Callable[[str], str]
) -> str:
    raw = getattr(args, attr, None)
    if raw is not None:
        return str(raw).strip()
    try:
        return (input_fn("请输入%s（留空=全部）：" % label) or "").strip()
    except EOFError:
        return ""


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
        allow_all=True,
    )
    voltage_level = _read_choice(
        args,
        "voltage_level",
        "电压等级",
        VOLTAGE_LEVEL_OPTIONS,
        DEFAULT_VOLTAGE_LEVEL,
        input_fn,
        allow_all=True,
    )
    line_name = _read_optional(args, "line_name", "线路名称", input_fn)
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


def _origin_path(base: object, path: str) -> str:
    """把基地址收敛为同源 + 指定路径，用于登录后直开任务页。"""
    try:
        parsed = urlsplit(str(base or ""))
    except ValueError:
        return ""
    if not parsed.scheme or not parsed.netloc:
        return ""
    return "%s://%s%s" % (parsed.scheme, parsed.netloc, path)


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


def _box_distance(a: dict, b: dict) -> float:
    """两个 Playwright bounding_box 的最小间距（像素）；相交为 0。"""
    ax = float(a.get("x") or 0)
    ay = float(a.get("y") or 0)
    aw = float(a.get("width") or 0)
    ah = float(a.get("height") or 0)
    bx = float(b.get("x") or 0)
    by = float(b.get("y") or 0)
    bw = float(b.get("width") or 0)
    bh = float(b.get("height") or 0)
    dx = max(bx - (ax + aw), ax - (bx + bw), 0.0)
    dy = max(by - (ay + ah), ay - (by + bh), 0.0)
    return (dx * dx + dy * dy) ** 0.5


def _png_to_gray(data: bytes) -> Optional[dict]:
    """把 PNG 图片字节转成验证码 OCR 的灰度字典；仅支持 8 位非隔行 PNG。

    用于验证码原图（src 下载/data URL/canvas.toDataURL），保持原始分辨率；
    不引入 Pillow，直接用标准库解压和反滤波。
    """
    if not data or not data.startswith(b"\x89PNG\r\n\x1a\n"):
        return None
    try:
        pos = 8
        width = height = bit_depth = color_type = interlace = 0
        palette = b""
        alpha = b""
        idat = bytearray()
        while pos + 8 <= len(data):
            length = struct.unpack(">I", data[pos : pos + 4])[0]
            kind = data[pos + 4 : pos + 8]
            chunk = data[pos + 8 : pos + 8 + length]
            if len(chunk) != length:
                return None
            pos += 12 + length
            if kind == b"IHDR":
                if length < 13:
                    return None
                (
                    width,
                    height,
                    bit_depth,
                    color_type,
                    _compression,
                    _filter,
                    interlace,
                ) = struct.unpack(">IIBBBBB", chunk[:13])
            elif kind == b"PLTE":
                palette = chunk
            elif kind == b"tRNS":
                alpha = chunk
            elif kind == b"IDAT":
                idat.extend(chunk)
            elif kind == b"IEND":
                break
        if width <= 0 or height <= 0 or interlace != 0 or bit_depth != 8:
            return None
        channels = {0: 1, 2: 3, 3: 1, 4: 2, 6: 4}.get(color_type)
        if channels is None:
            return None
        if color_type == 3 and len(palette) < 3:
            return None
        raw = zlib.decompress(bytes(idat))
        stride = width * channels
        bpp = channels
        rows = []
        prev = bytearray(stride)
        offset = 0
        for _ in range(height):
            if offset + 1 + stride > len(raw):
                return None
            filter_type = raw[offset]
            offset += 1
            row = bytearray(raw[offset : offset + stride])
            offset += stride
            if filter_type == 1:
                for i in range(bpp, stride):
                    row[i] = (row[i] + row[i - bpp]) & 0xFF
            elif filter_type == 2:
                for i in range(stride):
                    row[i] = (row[i] + prev[i]) & 0xFF
            elif filter_type == 3:
                for i in range(stride):
                    left = row[i - bpp] if i >= bpp else 0
                    row[i] = (row[i] + ((left + prev[i]) >> 1)) & 0xFF
            elif filter_type == 4:
                for i in range(stride):
                    left = row[i - bpp] if i >= bpp else 0
                    up = prev[i]
                    up_left = prev[i - bpp] if i >= bpp else 0
                    p = left + up - up_left
                    pa = abs(p - left)
                    pb = abs(p - up)
                    pc = abs(p - up_left)
                    if pa <= pb and pa <= pc:
                        predictor = left
                    elif pb <= pc:
                        predictor = up
                    else:
                        predictor = up_left
                    row[i] = (row[i] + predictor) & 0xFF
            elif filter_type != 0:
                return None
            rows.append(bytes(row))
            prev = row
    except (ValueError, struct.error, zlib.error):
        return None
    gray = bytearray(width * height)
    for y, row in enumerate(rows):
        for x in range(width):
            base = x * channels
            if color_type == 0:
                r = g = b = row[base]
                a = 255
            elif color_type == 2:
                r, g, b = row[base], row[base + 1], row[base + 2]
                a = 255
            elif color_type == 3:
                index = row[base]
                if index * 3 + 2 >= len(palette):
                    return None
                r, g, b = palette[index * 3 : index * 3 + 3]
                a = alpha[index] if index < len(alpha) else 255
            elif color_type == 4:
                r = g = b = row[base]
                a = row[base + 1]
            else:  # 6: RGBA
                r, g, b = row[base], row[base + 1], row[base + 2]
                a = row[base + 3]
            af = a / 255.0
            value = (0.2126 * r + 0.7152 * g + 0.0722 * b) * af + 255.0 * (1 - af)
            gray[y * width + x] = max(0, min(255, int(round(value))))
    return {
        "width": width,
        "height": height,
        "gray": base64.b64encode(bytes(gray)).decode("ascii"),
    }


def _image_mime(data: bytes) -> str:
    """按文件头判断图片类型；验证码原图常见 PNG/JPEG/GIF/WebP。"""
    if data.startswith(b"\x89PNG\r\n\x1a\n"):
        return "image/png"
    if data.startswith(b"\xff\xd8\xff"):
        return "image/jpeg"
    if data.startswith(b"GIF8"):
        return "image/gif"
    if data[:4] == b"RIFF" and data[8:12] == b"WEBP":
        return "image/webp"
    head = data.lstrip()[:200].lower()
    if head.startswith(b"<svg") or head.startswith(b"<?xml"):
        return "image/svg+xml"
    return "image/png"


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
    """无第三方依赖的多工作表 xlsx 写出（内联字符串，适配 WPS/Excel）。

    先写同目录临时文件再 os.replace，避免停止/崩溃时把已有汇总表截断。
    """
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    temp_path = path.with_name(
        "%s.%d.%d.tmp" % (path.name, os.getpid(), threading.get_ident())
    )
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
    try:
        with zipfile.ZipFile(temp_path, "w", zipfile.ZIP_DEFLATED) as archive:
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
        os.replace(temp_path, path)
    finally:
        try:
            if temp_path.exists():
                temp_path.unlink()
        except OSError:
            pass
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
        try:
            url = str(response.url or "")
        except Exception:
            return
        # 只保留审核匹配用到的接口响应；图片下载与静态资源不参与匹配，
        # 长任务下可显著减少浏览器 Response 对象的累积。
        if "/api/" not in url or FILE_DOWNLOAD_API in url:
            return
        self.responses.append(response)

    def reset(self) -> None:
        """任务边界清空响应引用，避免长时间运行累积浏览器响应对象。"""
        self.responses = []

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
        self.action_timeout_ms = max(
            1, int(float(getattr(args, "action_timeout", _ACTION_TIMEOUT_MS / 1000.0)) * 1000)
        )
        self.navigation_timeout_ms = max(
            1, int(float(getattr(args, "navigation_timeout", _NAVIGATION_TIMEOUT_MS / 1000.0)) * 1000)
        )
        self.login_result_timeout_ms = max(
            1, int(float(getattr(args, "login_result_timeout", _LOGIN_RESULT_TIMEOUT_MS / 1000.0)) * 1000)
        )
        self.captcha_ocr_timeout = max(
            1.0, float(getattr(args, "captcha_ocr_timeout", 30))
        )
        self.log_handler = log_handler
        # 无论是否接入客户端 Job 日志，每次运行都在输出目录留一份 run.log。
        # 逐行追加而不长期占用句柄，避免 Windows 下任务结束后日志文件仍被锁。
        self.log_path = self.outdir / "run.log"
        self._cancel_logged = False
        self.cancel_check = getattr(args, "cancel_check", None)
        self.sync_playwright = None
        self.playwright = None
        self._driver_reset_count = 0
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
        self.connection_lost = False
        self.awake_guard = None
        self.tagging_detail_timeout_ms = min(
            float(DEFAULT_TAGGING_DETAIL_TIMEOUT) * 1000.0, self.timeout_ms
        )
        self.diagnose_button = bool(getattr(args, "diagnose_button", False))
        self.button_diagnosed = False
        self.ignore_defect_boxes = bool(getattr(args, "ignore_defect_boxes", False))
        # 缺陷照片归档：按线路/杆塔保存原图，并为系统缺陷框另存标注图。
        # 非测试路径始终归档；引擎内部保留开关仅供单测关闭网络下载。
        self.save_photos = bool(getattr(args, "save_photos", True))
        self.photos_saved = 0
        self.photos_marked = 0
        # photo_failures 是兼容旧汇总口径的总数；下载与标注分开计数，
        # 汇总日志才能说清是没下到原图还是没画成标注。
        self.photo_failures = 0
        self.photo_download_failures = 0
        self.photo_mark_failures = 0
        self.photo_save_failures = 0
        # 当前任务的照片失败数：本轮有照片失败的任务不写跳过表，
        # 否则下一轮永远不再进这个任务，漏下载的照片再也没有机会补。
        self._task_photo_failures = 0
        self._task_photo_download_failures = 0
        self._task_photos_saved = 0
        # 照片归属索引：相对路径 -> fileGuid/大小/sha256/标注文件名，
        # 首次用到时从输出目录的隐藏索引文件加载，跨运行重跑据此确认
        # 同名文件是不是同一张照片（避免把旧原图配新框）。
        self.photo_index_path = self.outdir / PHOTO_INDEX_NAME
        self._photo_index = {}
        self._photo_index_loaded = False
        self._photo_index_dirty = False
        # 本轮页面刚加载成功的那张图的 fileGuid：blob:/data: 地址没有标识，
        # 只有记下身份才能把回退字节认作当前照片，避免串图。
        self._loaded_photo_guid = ""
        self._photo_user_agent = None
        self._file_auth_header = ""
        self._file_request_headers = {}
        self._photo_auth_warned = False
        # 页面原图响应缓存：URL -> (fileGuid, Response)，只保留最近若干张。
        self._photo_responses = {}
        self.progress_handler = getattr(args, "progress_handler", None)
        self.progress_label = ""
        self.task_progress_total = 0
        self.task_progress_done = 0
        skip_path = _normalize_text(getattr(args, "skip_tasks_file", ""))
        # 未显式指定跳过表时用它和输出目录绑定的「跳过任务.txt」：同一输出目录
        # 多次启停继续往同一张表追加；输出目录没有该文件时首轮就是空表，
        # 绝不回落到别处（历史全局表）的旧任务。
        self.skip_tasks_file = (
            Path(skip_path).expanduser()
            if skip_path
            else self.outdir / SKIP_TASKS_OUTPUT_NAME
        )
        # 自动登录是唯一模式：账号密码由客户端实例传入；独立运行用环境变量。
        self.username = str(
            getattr(args, "username", "")
            or os.environ.get("UVMP_DEFECT_USERNAME", "")
            or ""
        )
        self.password = str(
            getattr(args, "password", "")
            or os.environ.get("UVMP_DEFECT_PASSWORD", "")
            or ""
        )
        self._login_credential_failed = False
        self._login_diagnostic_saved = False
        self._captcha_diagnostic_logged = False
        # 登录页图片响应缓存：验证码 OCR 优先用页面已加载的原图，避免二次请求
        # 生成新验证码（会话码被覆盖）或需要页面内鉴权头时取不到。
        self._image_responses = {}
        self._last_text_probe = 0.0
        self.reload_after_tasks = max(
            0, int(getattr(args, "reload_after_tasks", 10))
        )
        self.reload_after_images = max(
            0, int(getattr(args, "reload_after_images", 200))
        )
        self._tasks_since_refresh = 0
        self._images_since_refresh = 0
        self.stall_timeout_ms = max(
            0.0,
            float(getattr(args, "stall_timeout", _DEFAULT_STALL_TIMEOUT)) * 1000.0,
        )
        self.last_activity = time.monotonic()
        self.last_known_url = ""
        self._last_image_success_at = None
        self.slow_image_seconds = max(
            1.0,
            float(
                getattr(
                    args, "slow_image_seconds", _DEFAULT_SLOW_IMAGE_SECONDS
                )
            ),
        )
        self.slow_image_count = max(
            2, int(getattr(args, "slow_image_count", _DEFAULT_SLOW_IMAGE_COUNT))
        )
        self.window_bounds = getattr(args, "window_bounds", None)
        self._slow_image_streak = 0
        self.health_unhealthy = False
        self.current_unreviewed_count = 0
        self.slow_restart_unreviewed = max(
            0, int(getattr(args, "slow_restart_unreviewed", 50))
        )
        self.force_browser_restart = False
        self._last_health_restart_at = 0.0
        self._watchdog_stop = None
        self._watchdog_thread = None
        self._recovering = False
        self.recovery_mode = False
        self.page_crashed = False
        self._page_recovered = False
        self._crash_restart_requested_at = 0.0
        self.current_dialog = None
        self.current_task_name = ""
        self.reviewed_guids = set()
        self.reviewed_count = 0
        # 同一任务跨刷新/健康重试累计的审核成功张次（含重试）。
        self.task_reviewed_total = 0
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
        self.summary_jsonl_path = self.outdir / SUMMARY_JSONL_NAME
        self._summary_jsonl_error_logged = False
        self._tasks_since_summary_write = 0
        self._rows_since_summary_write = 0
        self._skip_table_pending = []
        self._skip_table_flush_counter = 0
        self._skip_table_error_logged = False
        self._skip_names_loaded = False
        self.output_skip_path = self.outdir / SKIP_TASKS_OUTPUT_NAME
        self.output_skip_names = set()
        self._output_skip_loaded = False
        self._image_index_source = None
        self._image_index = {}
        self._last_active_thumbnail_index = -1
        self._thumbnail_selector = ""

    def log(self, message: object) -> None:
        self.last_activity = time.monotonic()
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
        if HostAwakeGuard is None:
            return
        if self.awake_guard is None:
            self.awake_guard = HostAwakeGuard(self.log)
        self.awake_guard.start()

    def release_keep_awake(self) -> None:
        if self.awake_guard is not None:
            self.awake_guard.stop()
            self.awake_guard = None

    def _wait_for_timeout(self, page, timeout_ms: int) -> None:
        self.last_activity = time.monotonic()
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
        # Playwright 同步 API 的 loop 在实例存活期间一直“运行”在本线程，
        # 同一线程再次 start() 会被判定为 asyncio loop 内调用；重连必须复用实例。
        if self.playwright is None:
            self.playwright = self.sync_playwright().start()
        deadline = time.monotonic() + (self.args.timeout if launched else 0)
        attempt = 0
        while True:
            attempt += 1
            try:
                self.browser = self.playwright.chromium.connect_over_cdp(
                    self.args.cdp_url,
                    timeout=(
                        min(self.timeout_ms, 8000)
                        if (launched or self.recovery_mode)
                        else self.timeout_ms
                    ),
                )
                self.log("已连接本机浏览器调试端口")
                self._driver_reset_count = 0
                break
            except Exception as exc:
                if (
                    self._driver_reset_count < _DRIVER_RESET_LIMIT
                    and self._is_driver_closed_error(exc)
                ):
                    self._driver_reset_count += 1
                    self.log("Playwright 驱动进程已退出，重建驱动后重试")
                    try:
                        self._reset_playwright()
                        self.playwright = self.sync_playwright().start()
                    except Exception as start_exc:
                        raise RuntimeError(
                            "重建 Playwright 驱动失败：%s"
                            % _redact_urls(start_exc)[:300]
                        ) from start_exc
                    continue
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
        self._remember_url()
        self._attach_browser_events()
        self.log("当前页面路径：%s" % _page_path(self.page.url))
        # 动作（click/fill/hover）用短超时，卡住时几秒内退出并响应停止；
        # 引擎自己的等待循环仍按 --timeout 控制，不受这里影响。
        self.page.set_default_timeout(min(self.timeout_ms, self.action_timeout_ms))
        self.page.set_default_navigation_timeout(
            min(self.timeout_ms, self.navigation_timeout_ms)
        )
        # 多实例并排时客户端会把窗口压到 600~900px，Element 筛选栏/下拉
        # 会被压得只剩省略号。用 CDP 页面视口固定到最小宽度，布局按大窗
        # 渲染，DOM 点击不依赖窗口物理大小。
        try:
            original = self.page.evaluate(
                "() => ({w: window.innerWidth, h: window.innerHeight})"
            )
            width = int(original.get("w") or 0)
            height = int(original.get("h") or 0)
            min_width = int(getattr(self.args, "min_viewport_width", 1280) or 0)
            min_height = int(getattr(self.args, "min_viewport_height", 900) or 0)
            if width and (width < min_width or height < min_height):
                self.page.set_viewport_size(
                    {
                        "width": max(width, min_width),
                        "height": max(height, min_height),
                    }
                )
                self.log(
                    "窗口过窄，页面视口调整为 %dx%d（原 %dx%d）"
                    % (max(width, min_width), max(height, min_height), width, height)
                )
        except Exception as exc:
            self.log("调整页面视口失败（继续使用当前窗口）：%s" % str(exc)[:200])
        try:
            self.page.bring_to_front()
        except Exception:
            pass
        # 只被动识别：浏览器启动时已打开启动网址，应用会自己跳转；
        # connect 不再重复导航，避免 domcontentloaded 超时阻断登录。
        self._apply_window_bounds()
        if bool(getattr(self.args, "maximize_window", False)):
            self._maximize_review_window()
        self._ensure_monitor()

    @staticmethod
    def _is_driver_closed_error(exc) -> bool:
        text = str(exc or "").casefold()
        return any(marker in text for marker in _DRIVER_CLOSED_MARKERS)

    def _reset_playwright(self) -> None:
        """驱动进程退出后同线程停掉旧驱动，并清空其浏览器/页面引用。"""
        old = self.playwright
        self.playwright = None
        self.browser = None
        self.page = None
        self.monitor = None
        self.current_dialog = None
        if old is not None:
            self._stop_playwright_safely(old)

    def _maximize_review_window(self) -> None:
        """审核时把浏览器最大化；全屏屏保覆盖在上面，不再移动/缩放窗口。"""
        if self.page is None or self.page.is_closed():
            return
        try:
            cdp = self.page.context.new_cdp_session(self.page)
            try:
                window = cdp.send("Browser.getWindowForTarget")
                window_id = int(window.get("windowId"))
                cdp.send(
                    "Browser.setWindowBounds",
                    {"windowId": window_id, "bounds": {"windowState": "normal"}},
                )
                cdp.send(
                    "Browser.setWindowBounds",
                    {"windowId": window_id, "bounds": {"windowState": "maximized"}},
                )
            finally:
                cdp.detach()
            self.log("审核浏览器已最大化")
        except Exception as exc:
            self.log("最大化浏览器失败（继续当前窗口）：%s" % str(exc)[:200])

    def _apply_window_bounds(self) -> None:
        bounds = self.window_bounds
        if not isinstance(bounds, dict) or self.page is None:
            return
        try:
            left = int(bounds.get("left"))
            top = int(bounds.get("top"))
            width = max(320, int(bounds.get("width")))
            height = max(240, int(bounds.get("height")))
        except (TypeError, ValueError):
            self.log("窗口排布参数无效，跳过")
            return
        try:
            cdp = self.page.context.new_cdp_session(self.page)
            try:
                window = cdp.send("Browser.getWindowForTarget")
                window_id = int(window.get("windowId"))
                cdp.send(
                    "Browser.setWindowBounds",
                    {"windowId": window_id, "bounds": {"windowState": "normal"}},
                )
                cdp.send(
                    "Browser.setWindowBounds",
                    {
                        "windowId": window_id,
                        "bounds": {
                            "left": left,
                            "top": top,
                            "width": width,
                            "height": height,
                        },
                    },
                )
            finally:
                cdp.detach()
            self.log(
                "浏览器窗口已排布：left=%d top=%d width=%d height=%d"
                % (left, top, width, height)
            )
        except Exception as exc:
            self.log("浏览器窗口排布失败（不影响审核）：%s" % str(exc)[:200])

    def _attach_browser_events(self) -> None:
        page = self.page
        browser = self.browser
        if page is not None:
            self._image_responses = {}
            self._photo_responses.clear()
            try:
                page.on("crash", self._on_page_crash)
            except Exception:
                pass
            try:
                page.on("response", self._on_image_response)
            except Exception:
                pass
            try:
                page.on("request", self._on_auth_request)
            except Exception:
                pass
        if browser is not None:
            try:
                browser.on("disconnected", self._on_browser_disconnected)
            except Exception:
                pass

    def _on_auth_request(self, request) -> None:
        self._capture_auth_header(request)

    def _capture_auth_header(self, request) -> None:
        """捕获请求头供原图下载复用；不硬编码密钥，也不打印敏感头。

        只有当前站点文件下载接口中「非图片通道、非缩略图的 GET 原图请求」
        能整体刷新文件头：它是页面通过 XHR/fetch 取原图的可信请求。其余请求
        （普通图片、缩略图、静态资源、文件接口的 POST 等）只补缺：
        Cookie 固然好，但绝不能把已经捕获到的 Allcore-Auth/Cookie 抹掉——
        页面只要加载过一张无鉴权头的图，原图下载就会 401。
        """
        if request is None:
            return
        try:
            url = str(getattr(request, "url", "") or "")
            site = _safe_origin(self.page.url) if self.page is not None else ""
            if not site:
                site = _safe_origin(getattr(self.args, "start_url", ""))
            req_origin = _safe_origin(url)
            is_file_api = FILE_DOWNLOAD_API in url
            if site and req_origin and site != req_origin and not is_file_api:
                return
            headers = {}
            all_headers = getattr(request, "all_headers", None)
            if callable(all_headers):
                headers = all_headers() or {}
            else:
                headers = getattr(request, "headers", {}) or {}
            resource_type = str(getattr(request, "resource_type", "") or "")
            method = str(getattr(request, "method", "") or "GET").upper()
            if is_file_api or resource_type == "image":
                skip = {
                    "host",
                    "content-length",
                    "connection",
                    "accept-encoding",
                    "origin",
                    "range",
                    "if-range",
                    "if-none-match",
                    "if-modified-since",
                    "if-match",
                    "if-unmodified-since",
                }
                captured = {}
                for key, value in headers.items():
                    if str(key).casefold() in skip:
                        continue
                    captured[str(key)] = str(value)
                if captured:
                    if (
                        _is_file_download_request(url)
                        and method == "GET"
                        and resource_type != "image"
                        and not _is_thumbnail_photo_url(url)
                    ):
                        # 仅 XHR/fetch 原图请求整体刷新；同接口的 <img> 缩略图
                        # 不带自定义鉴权头，不能把已捕获的会话头清掉。
                        self._file_request_headers = captured
                    else:
                        # 普通图片/其它请求：只补缺，不移除已有鉴权与 Cookie。
                        for key, value in captured.items():
                            self._file_request_headers.setdefault(key, value)
            if not self._file_auth_header:
                auth = ""
                for key, value in headers.items():
                    if str(key).lower() == "authorization":
                        auth = str(value or "")
                        break
                if auth.lower().startswith("basic "):
                    self._file_auth_header = auth
                    self.log("已捕获接口 Basic 鉴权头，用于缺陷照片下载")
        except Exception:
            pass

    def _on_image_response(self, response) -> None:
        """缓存图片响应对象；验证码 OCR 与缺陷原图归档优先复用页面字节。

        只存 Response 不读 body，避免事件线程被大图阻塞；真正取图时再读。
        """
        self._capture_auth_header(getattr(response, "request", None))
        self._cache_photo_response(response)
        try:
            headers = response.headers or {}
            if not str(headers.get("content-type") or "").startswith("image/"):
                return
            length = int(headers.get("content-length") or 0)
            if length > 2 * 1024 * 1024:
                return
            url = str(getattr(response, "url", "") or "")
            if not url:
                return
            self._image_responses[url] = response
            while len(self._image_responses) > 20:
                self._image_responses.pop(next(iter(self._image_responses)))
        except Exception:
            pass

    def _cache_photo_response(self, response) -> None:
        """缓存审核弹层已加载的原图响应；归档时直接读字节，避免二次请求。"""
        try:
            headers = response.headers or {}
            status = getattr(response, "status", 0)
            if isinstance(status, int) and status not in (0, 200):
                return
            if headers.get("content-range"):
                return
            content_type = (
                str(headers.get("content-type") or "")
                .split(";")[0]
                .strip()
                .casefold()
            )
            url = str(getattr(response, "url", "") or "")
            if not url or url.startswith(("blob:", "data:")):
                return
            if _is_thumbnail_photo_url(url):
                return
            # 文件下载接口现场会把 JPEG 错标成 application/json；只放宽这一入口，
            # 且按 URL 路径精确匹配（现场无前缀），避免 query 里带同名路径的接口混入；
            # 字节真伪仍由 _cached_photo_bytes 按文件头验证。
            if not content_type.startswith("image/") and (
                urlsplit(url).path != FILE_DOWNLOAD_API
            ):
                return
            length = int(headers.get("content-length") or 0)
            if length > PHOTO_RESPONSE_MAX_BYTES:
                return
            guid = _photo_guid_from_url(url)
            if not guid:
                return
            self._photo_responses.pop(url, None)
            self._photo_responses[url] = (guid, response)
            while len(self._photo_responses) > PHOTO_RESPONSE_CACHE_LIMIT:
                self._photo_responses.pop(next(iter(self._photo_responses)))
        except Exception:
            pass

    def _on_page_crash(self, *_args) -> None:
        self.page_crashed = True
        self.connection_lost = True
        self._crash_restart_requested_at = 0.0
        self.log("检测到浏览器页面崩溃（常见原因：内存耗尽），准备重启受控浏览器")

    def _on_browser_disconnected(self, *_args) -> None:
        self.connection_lost = True
        self.log("检测到受控浏览器进程已断开")

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
        return LOGIN_PAGE_PATH in self._current_route()

    def _is_task_page_url(self) -> bool:
        return TASK_PAGE_PATH.casefold() in self._current_route()

    def _task_list_visible(self) -> bool:
        if self.page is None or self.page.is_closed():
            return False
        if not self._is_task_page_url():
            return False
        frames = [self.page]
        try:
            frames.extend(
                [item for item in self.page.frames if item is not self.page]
            )
        except Exception:
            pass
        for frame in frames:
            try:
                tables = frame.locator(".el-table:visible")
                for index in range(tables.count()):
                    table = tables.nth(index)
                    header_locator = table.locator(".el-table__header").first
                    if not header_locator.count():
                        continue
                    header = _normalize_text(header_locator.inner_text(timeout=1000))
                    if TASK_TABLE_NAME_HEADER in header and (
                        "任务状态" in header or "操作" in header
                    ):
                        return True
            except Exception:
                continue
        # Avue/Element 版本差异或表格结构变化时，用页面文案兜底：任务页同时有
        # 「任务名称」「任务状态」和查询/重置控件，首页仪表盘不会同时具备。
        # body.inner_text 较重，轮询时最多每秒探测一次。
        now = time.monotonic()
        if now - self._last_text_probe >= 1.0:
            self._last_text_probe = now
            for frame in frames:
                try:
                    text = _normalize_text(frame.locator("body").inner_text(timeout=1000))
                except Exception:
                    continue
                if (
                    TASK_TABLE_NAME_HEADER in text
                    and "任务状态" in text
                    and ("查询" in text or "重置" in text)
                ):
                    return True
                # 首次进入任务页时列表要等点击「查询」才渲染；筛选栏+查询按钮
                # 已出现即视为页面就绪，避免把“尚未查询”误判为页面未加载。
                if "任务状态" in text and ("查询" in text or "重置" in text):
                    return True
        return False

    def _task_page_diagnostic(self) -> str:
        """任务页未识别时记录 URL/标题/表格数/正文片段，便于现场核对。"""
        if self.page is None or self.page.is_closed():
            return ""
        try:
            url = _page_path(self.page.url)
            title = self.page.title()
            tables = self.page.locator(".el-table").count()
            text = _normalize_text(self.page.locator("body").inner_text())[:400]
            return "url=%s title=%s tables=%d text=%s" % (
                url,
                title,
                tables,
                text,
            )
        except Exception as exc:
            return "诊断失败：%s" % str(exc)[:200]

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
        try:
            if self.page.locator('input[type="password"]:visible').count():
                return True
        except Exception:
            pass
        if (
            self._find_visible_button(self.page, "登录", require_enabled=False)
            is not None
        ):
            return True
        for text in ("验证码", "* 验证码", "用户名", "* 用户名", "密码", "* 密码"):
            if self._visible_text(text) is not None:
                return True
        return False

    def _login_form_ready(self) -> bool:
        """登录页已出现且输入框已渲染，避免抢在路由跳转中填表。"""
        if not self._login_page_visible():
            return False
        try:
            if not self.page.locator('input[type="password"]:visible').count():
                return False
            return self.page.locator("input:visible").count() >= 2
        except Exception:
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

    def _session_ready(self) -> bool:
        """是否已登录：必须看到任务列表或系统菜单，不能只看 URL。

        SPA 启动时先停在 `/` 再跳 `/login`，若把“非登录 URL”当成已登录，
        自动登录会被直接跳过，随后直达任务页失败。
        """
        if self.page is None or self.page.is_closed():
            return False
        if self._is_login_url():
            return False
        if self._login_page_visible():
            return False
        return self._login_ready()

    def _login_succeeded(self) -> bool:
        """点击登录后的成功判定：已离开 /login 且登录表单消失。

        与 `_session_ready` 分开：登录提交后应用可能先跳首页、菜单稍后才渲染，
        此时不能再等菜单，否则会把已成功的登录当成失败反复重提。
        """
        if self.page is None or self.page.is_closed():
            return False
        if self._is_login_url():
            return False
        return not self._login_page_visible()

    def _remember_url(self) -> None:
        if self.page is None or self.page.is_closed():
            return
        try:
            url = str(self.page.url or "")
        except Exception:
            return
        if url and url != "about:blank":
            self.last_known_url = url

    def _captcha_pixels(self, near=None) -> Optional[dict]:
        """从登录页读取验证码灰度像素；DOM 取不到时取验证码原图。

        DOM 路径覆盖 img 与 canvas；跨域图片/画布会让 canvas 污染，
        getImageData 直接抛错；验证码在 shadow DOM 里时 document.images
        也扫不到。此时按 locator 找到验证码元素，直接取它的原图
        （src/data/blob/canvas.toDataURL），保持图片原始分辨率，不做
        屏幕截图，避免 CSS 缩小后 OCR 失真。
        """
        if near is not None:
            try:
                near.evaluate(
                    "(el) => el.setAttribute('data-shade-captcha-input', '1')"
                )
            except Exception:
                pass
        pixels = self._captcha_pixels_dom()
        if pixels is not None:
            return pixels
        return self._captcha_pixels_from_source(near)

    def _wait_captcha_pixels(self, near=None, timeout_ms: float = None):
        """验证码可能晚于登录表单渲染；在超时内轮询，避免过早刷新丢图。"""
        wait_ms = min(float(self.timeout_ms), 8000.0)
        if timeout_ms is not None:
            wait_ms = max(0.0, float(timeout_ms))
        deadline = time.monotonic() + wait_ms / 1000.0
        while True:
            self.check_cancel()
            pixels = self._captcha_pixels(near)
            if pixels is not None:
                return pixels
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return None
            self._wait_for_timeout(
                self.page, int(min(200, max(1, remaining * 1000.0)))
            )

    def _captcha_pixels_dom(self) -> Optional[dict]:
        if self.page is None or self.page.is_closed():
            return None
        script = r"""
        () => {
          const visible = (el) => {
            const r = el.getBoundingClientRect();
            return r.width >= 2 && r.height >= 2 && r.bottom > 0 && r.right > 0;
          };
          const grayFromRgba = (rgba, w, h) => {
            const gray = new Uint8Array(w * h);
            for (let i = 0; i < gray.length; i += 1) {
              const p = i * 4;
              const a = rgba[p + 3] / 255;
              const r = rgba[p] * a + 255 * (1 - a);
              const g = rgba[p + 1] * a + 255 * (1 - a);
              const b = rgba[p + 2] * a + 255 * (1 - a);
              gray[i] = Math.round(0.2126 * r + 0.7152 * g + 0.0722 * b);
            }
            let binary = '';
            const chunk = 0x8000;
            for (let i = 0; i < gray.length; i += chunk) {
              binary += String.fromCharCode.apply(null, gray.subarray(i, i + chunk));
            }
            return { width: w, height: h, gray: btoa(binary) };
          };
          const extractImage = (img) => {
            try {
              if (!img || !img.complete || !img.naturalWidth || !img.naturalHeight) return null;
              if (!visible(img)) return null;
              const w = img.naturalWidth, h = img.naturalHeight;
              // 验证码可能被 CSS 缩到 23x9，像素必须按原图 130x48 取。
              if (w < 40 || h < 18 || w > 400 || h > 200) return null;
              const canvas = document.createElement('canvas');
              canvas.width = w; canvas.height = h;
              const ctx = canvas.getContext('2d');
              ctx.fillStyle = '#fff';
              ctx.fillRect(0, 0, w, h);
              ctx.drawImage(img, 0, 0, w, h);
              return grayFromRgba(ctx.getImageData(0, 0, w, h).data, w, h);
            } catch (_err) {
              return null;
            }
          };
          const extractCanvas = (el) => {
            try {
              if (!el || !visible(el)) return null;
              const w = el.width, h = el.height;
              if (w < 40 || h < 18 || w > 400 || h > 200) return null;
              const ctx = el.getContext('2d');
              if (!ctx) return null;
              return grayFromRgba(ctx.getImageData(0, 0, w, h).data, w, h);
            } catch (_err) {
              return null;
            }
          };
          const extract = (el) => {
            if (!el) return null;
            const tag = String(el.tagName || '').toUpperCase();
            return tag === 'CANVAS' ? extractCanvas(el) : extractImage(el);
          };
          const inputs = Array.from(document.querySelectorAll('input'));
          const captchaInput = inputs.find((el) => {
            if (el.getAttribute('data-shade-captcha-input') === '1') return true;
            const labels = el.labels ? Array.from(el.labels).map((item) => item.innerText || '').join(' ') : '';
            const text = [
              el.placeholder || '',
              el.getAttribute('aria-label') || '',
              el.name || '',
              el.id || '',
              labels,
            ].join(' ');
            return /验证码|captcha|checkcode|yzm/i.test(text);
          });
          if (captchaInput) {
            let root = captchaInput;
            for (let depth = 0; root && depth < 6; depth += 1) {
              for (const el of root.querySelectorAll('img, canvas')) {
                const got = extract(el);
                if (got) return got;
              }
              root = root.parentElement;
            }
          }
          const candidates = [];
          const seen = new Set();
          for (const img of Array.from(document.images)) {
            if (seen.has(img)) continue;
            seen.add(img);
            const src = String(img.currentSrc || img.src || '');
            if (!src || src === 'about:blank') continue;
            if (!visible(img)) continue;
            const w = img.naturalWidth || 0, h = img.naturalHeight || 0;
            if (w < 40 || h < 18 || w > 400 || h > 200) continue;
            const ratio = w / Math.max(1, h);
            let score = 0;
            if (src.startsWith('data:image/')) score += 100;
            if (ratio >= 1.6 && ratio <= 4.5) score += 20;
            if (h >= 24 && h <= 100) score += 10;
            score += Math.min(20, Math.round(w / 20));
            candidates.push({ el: img, score });
          }
          for (const canvas of Array.from(document.querySelectorAll('canvas'))) {
            if (seen.has(canvas)) continue;
            seen.add(canvas);
            if (!visible(canvas)) continue;
            const w = canvas.width || 0, h = canvas.height || 0;
            if (w < 40 || h < 18 || w > 400 || h > 200) continue;
            const ratio = w / Math.max(1, h);
            let score = 10;
            if (ratio >= 1.6 && ratio <= 4.5) score += 20;
            if (h >= 24 && h <= 100) score += 10;
            score += Math.min(20, Math.round(w / 20));
            candidates.push({ el: canvas, score });
          }
          candidates.sort((a, b) => b.score - a.score);
          for (const item of candidates) {
            const got = extract(item.el);
            if (got) return got;
          }
          return null;
        }
        """
        try:
            value = self.page.evaluate(script)
        except Exception:
            return None
        return self._pixels_value(value)

    def _pixels_value(self, value) -> Optional[dict]:
        if not isinstance(value, dict):
            return None
        try:
            width = int(value.get("width"))
            height = int(value.get("height"))
            gray = value.get("gray")
            if width <= 0 or height <= 0 or not gray:
                return None
            raw = base64.b64decode(str(gray))
            if len(raw) != width * height:
                return None
            return {"width": width, "height": height, "gray": str(gray)}
        except Exception:
            return None

    def _captcha_pixels_from_source(self, near=None) -> Optional[dict]:
        """DOM 取不到像素时，按验证码元素的原图取像素，不做屏幕截图。

        原图优先级：data:/blob:/页面 fetch（带会话与 Referer）→
        context.request（带 Cookie/Referer）→ canvas.toDataURL。均为图片
        原始分辨率，避免截图被 CSS 缩小后 OCR 失真。
        """
        element = self._captcha_candidate(near)
        if element is None:
            return None
        try:
            tag = str(element.evaluate("el => el.tagName") or "").upper()
        except Exception:
            tag = ""
        if tag == "CANVAS":
            try:
                data_url = element.evaluate("el => el.toDataURL('image/png')")
            except Exception:
                data_url = ""
            pixels = self._pixels_from_data_url(str(data_url or ""))
            if pixels is not None:
                self.log(
                    "验证码像素改用原图（%dx%d，canvas）"
                    % (pixels["width"], pixels["height"])
                )
            return pixels
        try:
            src = str(
                element.evaluate(
                    "el => el.currentSrc || el.src || el.getAttribute('src') || ''"
                )
                or ""
            ).strip()
        except Exception:
            src = ""
        if not src:
            return None
        data, mime = self._fetch_captcha_source(element, src)
        if not data:
            return None
        pixels = self._pixels_from_image_bytes(data, mime)
        if pixels is not None:
            if src.startswith("data:"):
                source = "data"
            elif src.startswith("blob:"):
                source = "blob"
            else:
                source = _safe_origin(src)
            self.log(
                "验证码像素改用原图（%dx%d，%s）"
                % (pixels["width"], pixels["height"], source or tag or "图片")
            )
        return pixels

    def _fetch_captcha_source(self, element, src: str):
        """取验证码原图字节；先页面 fetch（带会话），跨域再走请求上下文。"""
        if src.startswith("data:"):
            header, _, payload = src.partition(",")
            if ";base64" not in header:
                return b"", ""
            try:
                data = base64.b64decode(payload)
            except Exception:
                return b"", ""
            mime = header[5:].split(";")[0].strip() or _image_mime(data)
            return data, mime
        cached = getattr(self, "_image_responses", {}).get(src)
        if cached is not None:
            try:
                data = cached.body()
                mime = str(
                    (cached.headers or {}).get("content-type") or ""
                ).split(";")[0].strip()
                if data:
                    return data, mime or _image_mime(data)
            except Exception:
                pass
        try:
            value = element.evaluate(
                r"""
                async (el) => {
                  const url = el.currentSrc || el.src || el.getAttribute('src') || '';
                  if (!url) return null;
                  const response = await fetch(url);
                  if (!response.ok) return null;
                  const buffer = await response.arrayBuffer();
                  const bytes = new Uint8Array(buffer);
                  let binary = '';
                  const chunk = 0x8000;
                  for (let i = 0; i < bytes.length; i += chunk) {
                    binary += String.fromCharCode.apply(null, bytes.subarray(i, i + chunk));
                  }
                  return {
                    b64: btoa(binary),
                    type: response.headers.get('content-type') || '',
                  };
                }
                """
            )
            if isinstance(value, dict) and value.get("b64"):
                data = base64.b64decode(str(value.get("b64")))
                mime = str(value.get("type") or "").split(";")[0].strip()
                return data, mime or _image_mime(data)
        except Exception:
            pass
        try:
            headers = {"Referer": self.page.url}
            try:
                user_agent = str(self.page.evaluate("navigator.userAgent") or "")
            except Exception:
                user_agent = ""
            if user_agent:
                headers["User-Agent"] = user_agent
            response = self.page.context.request.get(
                src,
                headers=headers,
                timeout=min(self.timeout_ms, 8000),
            )
            if response.ok:
                data = response.body()
                mime = str(
                    (response.headers or {}).get("content-type") or ""
                ).split(";")[0].strip()
                return data, mime or _image_mime(data)
        except Exception:
            pass
        return b"", ""

    def _pixels_from_data_url(self, data_url: str) -> Optional[dict]:
        if not data_url.startswith("data:") or "," not in data_url:
            return None
        header, _, payload = data_url.partition(",")
        if ";base64" not in header:
            return None
        try:
            data = base64.b64decode(payload)
        except Exception:
            return None
        mime = header[5:].split(";")[0].strip() or _image_mime(data)
        return self._pixels_from_image_bytes(data, mime)

    def _pixels_from_image_bytes(self, data: bytes, mime: str) -> Optional[dict]:
        """原图字节转灰度：PNG 直接解，其余格式用空白页按原分辨率渲染。"""
        if not data:
            return None
        mime = str(mime or "").split(";")[0].strip().lower()
        if not mime.startswith("image/"):
            mime = _image_mime(data)
        if mime == "image/png":
            pixels = _png_to_gray(data)
            if pixels is not None:
                return pixels
        b64 = base64.b64encode(data).decode("ascii")
        data_url = "data:%s;base64,%s" % (mime, b64)
        script = r"""
        async (dataUrl) => {
          const img = new Image();
          const loaded = new Promise((resolve, reject) => {
            img.onload = () => resolve(true);
            img.onerror = () => reject(new Error('图片加载失败'));
          });
          img.src = dataUrl;
          await loaded;
          const w = img.naturalWidth, h = img.naturalHeight;
          if (!w || !h) return null;
          const canvas = document.createElement('canvas');
          canvas.width = w; canvas.height = h;
          const ctx = canvas.getContext('2d');
          ctx.fillStyle = '#fff';
          ctx.fillRect(0, 0, w, h);
          ctx.drawImage(img, 0, 0, w, h);
          const rgba = ctx.getImageData(0, 0, w, h).data;
          const gray = new Uint8Array(w * h);
          for (let i = 0; i < gray.length; i += 1) {
            const p = i * 4;
            const a = rgba[p + 3] / 255;
            const r = rgba[p] * a + 255 * (1 - a);
            const g = rgba[p + 1] * a + 255 * (1 - a);
            const b = rgba[p + 2] * a + 255 * (1 - a);
            gray[i] = Math.round(0.2126 * r + 0.7152 * g + 0.0722 * b);
          }
          let binary = '';
          const chunk = 0x8000;
          for (let i = 0; i < gray.length; i += chunk) {
            binary += String.fromCharCode.apply(null, gray.subarray(i, i + chunk));
          }
          return { width: w, height: h, gray: btoa(binary) };
        }
        """
        helper = None
        try:
            helper = self.page.context.new_page()
            value = helper.evaluate(script, data_url)
        except Exception:
            return None
        finally:
            if helper is not None:
                try:
                    helper.close()
                except Exception:
                    pass
                try:
                    self.page.bring_to_front()
                except Exception:
                    pass
        return self._pixels_value(value)

    def _captcha_candidate(self, near=None):
        """在验证码输入框附近找 img/canvas/input[type=image]，取最像验证码的一个。"""
        if self.page is None or self.page.is_closed():
            return None
        near_box = None
        if near is not None:
            try:
                near_box = near.bounding_box()
            except Exception:
                near_box = None
        frames = [self.page]
        try:
            frames.extend([item for item in self.page.frames if item is not self.page])
        except Exception:
            pass
        best = None
        best_key = None
        for frame in frames:
            for selector in ("img", "canvas", 'input[type="image"]'):
                try:
                    locator = frame.locator(selector)
                    count = locator.count()
                except Exception:
                    continue
                for index in range(count):
                    item = locator.nth(index)
                    try:
                        if not item.is_visible():
                            continue
                        box = item.bounding_box()
                        if not box:
                            continue
                        css_width = float(box.get("width") or 0)
                        css_height = float(box.get("height") or 0)
                        if css_width < 8 or css_height < 4:
                            continue
                        width, height = css_width, css_height
                        try:
                            natural = item.evaluate(
                                "el => [el.naturalWidth || el.width || 0,"
                                " el.naturalHeight || el.height || 0]"
                            )
                        except Exception:
                            natural = None
                        if isinstance(natural, (list, tuple)) and len(natural) >= 2:
                            try:
                                nw = float(natural[0])
                                nh = float(natural[1])
                            except (TypeError, ValueError):
                                nw = nh = 0.0
                            if 40 <= nw <= 400 and 18 <= nh <= 200:
                                width, height = nw, nh
                        if width < 8 or height < 4 or width > 400 or height > 200:
                            continue
                        src = str(item.get_attribute("src") or "")
                    except Exception:
                        continue
                    ratio = width / max(1.0, height)
                    score = 10 if selector == "canvas" else 0
                    if 1.6 <= ratio <= 4.5:
                        score += 20
                    if 40 <= width <= 220:
                        score += 10
                    if 20 <= height <= 80:
                        score += 10
                    if src.startswith("data:image/"):
                        score += 10
                    distance = _box_distance(box, near_box) if near_box else 0.0
                    near_rank = 0 if (near_box is None or distance <= 240) else 1
                    key = (near_rank, -score, distance, -width * height)
                    if best_key is None or key < best_key:
                        best_key = key
                        best = item
        return best

    def _ocr_captcha(self, pixels: dict) -> str:
        helper = os.environ.get("UVMP_OCR_HELPER", "").strip()
        if not helper:
            candidate = (
                SCRIPT_DIR
                / "desktop"
                / "electron"
                / "ocr"
                / "captcha_ocr.cjs"
            )
            if candidate.is_file():
                helper = str(candidate)
        if not helper or not Path(helper).is_file():
            raise RuntimeError(
                "自动登录缺少离线 OCR 助手（UVMP_OCR_HELPER）；"
                "请使用完整客户端运行"
            )
        node = os.environ.get("UVMP_ELECTRON", "").strip()
        if not node or not Path(node).exists():
            node = shutil.which("node") or ""
        if not node:
            raise RuntimeError("自动登录缺少 Node/Electron 运行时，无法执行离线 OCR")
        env = dict(os.environ)
        env["ELECTRON_RUN_AS_NODE"] = "1"
        env.setdefault("UVMP_OCR_MODEL", str(Path(helper).with_name("common.onnx")))
        env.setdefault(
            "UVMP_OCR_CHARSETS", str(Path(helper).with_name("charsets.json"))
        )
        if not env.get("UVMP_NODE_MODULES"):
            modules = SCRIPT_DIR / "desktop" / "node_modules"
            if modules.is_dir():
                env["UVMP_NODE_MODULES"] = str(modules)
        payload = json.dumps(
            {
                "width": pixels["width"],
                "height": pixels["height"],
                "gray": pixels["gray"],
            }
        )
        try:
            # OCR 助手是 node/Electron 控制台程序：从 GUI 核心调用会弹一个黑框
            proc = _run_hidden(
                [node, helper],
                input=payload,
                capture_output=True,
                text=True,
                encoding="utf-8",
                errors="replace",
                env=env,
                timeout=self.captcha_ocr_timeout,
            )
        except Exception as exc:
            raise RuntimeError("离线 OCR 调用失败：%s" % str(exc)[:200]) from exc
        lines = [line for line in (proc.stdout or "").splitlines() if line.strip()]
        if not lines:
            raise RuntimeError(
                "离线 OCR 无输出：%s" % (proc.stderr or "")[:200]
            )
        try:
            result = json.loads(lines[-1])
        except ValueError as exc:
            raise RuntimeError("离线 OCR 输出不是 JSON：%s" % lines[-1][:200]) from exc
        if result.get("error"):
            raise RuntimeError("离线 OCR 失败：%s" % result["error"])
        text = re.sub(r"[^0-9A-Za-z]", "", str(result.get("normalized") or ""))
        if not text:
            raise RuntimeError("离线 OCR 没有识别出字符")
        return text.upper()

    def _login_input(self, labels):
        """按候选文案查找登录表单项；labels 可为单个字符串或元组。"""
        if self.page is None:
            return None
        names = (labels,) if isinstance(labels, str) else tuple(labels)
        pattern = "|".join(re.escape(name) for name in names)
        try:
            locator = self.page.get_by_role("textbox", name=re.compile(pattern))
            if locator.count():
                for index in range(locator.count()):
                    item = locator.nth(index)
                    try:
                        if item.is_visible():
                            return item
                    except Exception:
                        continue
        except Exception:
            pass
        for name in names:
            try:
                locator = self.page.locator('input[placeholder*="%s"]' % name)
                for index in range(locator.count()):
                    item = locator.nth(index)
                    try:
                        if item.is_visible():
                            return item
                    except Exception:
                        continue
            except Exception:
                continue
        if "密码" in names:
            try:
                locator = self.page.locator('input[type="password"]')
                for index in range(locator.count()):
                    item = locator.nth(index)
                    try:
                        if item.is_visible():
                            return item
                    except Exception:
                        continue
            except Exception:
                pass
        return None

    def _captcha_image(self, near=None):
        """点击刷新验证码用的元素；按输入框距离/尺寸选最像验证码的一个。"""
        return self._captcha_candidate(near)

    def _login_error_text(self) -> str:
        if self.page is None or self.page.is_closed():
            return ""
        for selector in (
            ".el-message--error:visible",
            ".el-message:visible",
            ".el-form-item__error:visible",
        ):
            try:
                locator = self.page.locator(selector)
                if locator.count():
                    text = _normalize_text(locator.last.inner_text())
                    if text and any(
                        word in text
                        for word in (
                            "失败",
                            "错误",
                            "异常",
                            "验证码",
                            "密码",
                            "账号",
                            "锁定",
                            "过期",
                        )
                    ):
                        return text[:200]
            except Exception:
                continue
        return ""

    def _refresh_login_captcha(self, captcha=None) -> None:
        try:
            self._image_responses.clear()
        except Exception:
            pass
        image = self._captcha_image(captcha)
        if image is not None:
            # canvas 验证码点击不一定换图，直接刷新页面更稳。
            try:
                tag = str(image.evaluate("el => el.tagName") or "").upper()
            except Exception:
                tag = ""
            if tag != "CANVAS":
                try:
                    self._dom_click(image)
                    self._wait_for_timeout(self.page, 300)
                    return
                except Exception:
                    pass
        try:
            self.page.reload(wait_until="domcontentloaded")
        except Exception:
            pass

    def _captcha_diagnostic(self) -> str:
        """验证码取像素失败时记录登录页 DOM 概况，便于下次直接定位。"""
        if self.page is None or self.page.is_closed():
            return ""
        script = r"""
        () => {
          const box = (el) => {
            const r = el.getBoundingClientRect();
            return [Math.round(r.x), Math.round(r.y), Math.round(r.width), Math.round(r.height)];
          };
          return {
            url: location.href,
            frames: window.frames.length,
            inputs: Array.from(document.querySelectorAll('input')).map((el) => ({
              type: el.type,
              placeholder: el.placeholder || '',
              aria: el.getAttribute('aria-label') || '',
              name: el.name || '',
              id: el.id || '',
              box: box(el),
            })),
            images: Array.from(document.images).map((img) => ({
              src: String(img.currentSrc || img.src || '').slice(0, 120),
              natural: [img.naturalWidth || 0, img.naturalHeight || 0],
              complete: !!img.complete,
              box: box(img),
            })),
            canvases: Array.from(document.querySelectorAll('canvas')).map((el) => ({
              size: [el.width || 0, el.height || 0],
              box: box(el),
            })),
          };
        }
        """
        try:
            value = self.page.evaluate(script)
            text = json.dumps(value, ensure_ascii=False)
        except Exception as exc:
            text = "诊断失败：%s" % str(exc)[:200]
        return _redact_urls(text)[:1600]

    def _save_login_diagnostic(self) -> None:
        """登录表单识别失败时只留一次现场图，便于核对登录页结构。"""
        if self._login_diagnostic_saved:
            return
        self._login_diagnostic_saved = True
        self.save_diagnostic(self.page, "login_page")

    def _login_button(self):
        if self.page is None or self.page.is_closed():
            return None
        button = self._find_visible_button(
            self.page, "登录", require_enabled=False
        )
        if button is not None:
            return button
        try:
            role = self.page.get_by_role("button", name=re.compile("登录"))
            for index in range(role.count()):
                item = role.nth(index)
                try:
                    if item.is_visible():
                        return item
                except Exception:
                    continue
        except Exception:
            pass
        return None

    def _auto_login_once(self) -> bool:
        """执行一次自动登录；成功返回 True，失败返回 False。无限重试由外层负责。"""
        if self.page is None or self.page.is_closed():
            raise ReviewStopped("自动登录时页面已关闭")
        if not self.username or not self.password:
            raise RuntimeError("自动登录缺少用户名或密码")
        self._login_credential_failed = False
        self._remember_url()
        username = self._login_input(("用户名", "账号", "帐号", "账户", "手机号", "工号"))
        password = self._login_input(("密码",))
        captcha = self._login_input(("验证码",))
        if username is None or password is None:
            self.log("自动登录未找到用户名/密码输入框，刷新登录页后重试")
            self._save_login_diagnostic()
            self._refresh_login_captcha(captcha)
            return False
        try:
            username.fill(self.username)
            password.fill(self.password)
        except Exception as exc:
            if self._mark_connection_lost(exc):
                raise
            self.log("自动登录填表失败：%s" % _redact_urls(exc)[:200])
            return False
        code = ""
        if captcha is not None:
            # 验证码图片可能晚于登录表单加载；先等，别立刻刷新掉。
            pixels = self._wait_captcha_pixels(captcha)
            if pixels is None:
                # 现场站点有时要点击一次登录才加载验证码；凭证已填，
                # 空验证码只会触发表单校验，不提交账号密码。
                self.log("自动登录验证码暂未加载，尝试点击登录触发加载")
                trigger = self._login_button()
                if trigger is not None:
                    try:
                        self._dom_click(trigger)
                    except Exception as exc:
                        if self._mark_connection_lost(exc):
                            raise
                        self.log(
                            "触发验证码加载点击登录失败：%s"
                            % _redact_urls(exc)[:200]
                        )
                    else:
                        self._wait_for_timeout(self.page, 500)
                        if self._login_succeeded():
                            self._remember_url()
                            self.log("自动登录成功")
                            return True
                        fresh_username = self._login_input(
                            ("用户名", "账号", "帐号", "账户", "手机号", "工号")
                        )
                        fresh_password = self._login_input(("密码",))
                        fresh_captcha = self._login_input(("验证码",))
                        if fresh_username is not None:
                            username = fresh_username
                        if fresh_password is not None:
                            password = fresh_password
                        if fresh_captcha is not None:
                            captcha = fresh_captcha
                        try:
                            username.fill(self.username)
                            password.fill(self.password)
                        except Exception:
                            pass
                        pixels = self._wait_captcha_pixels(captcha)
            if pixels is None:
                self.log("自动登录未找到验证码图片（DOM 与原图均失败），刷新登录页后重试")
                if not self._captcha_diagnostic_logged:
                    self._captcha_diagnostic_logged = True
                    self.log("验证码诊断：%s" % self._captcha_diagnostic())
                self._save_login_diagnostic()
                self._refresh_login_captcha(captcha)
                return False
            code = self._ocr_captcha(pixels)
            self.log("自动登录识别验证码：%d 位" % len(code))
        else:
            self.log("自动登录页面没有验证码输入框，按无验证码登录继续")
        try:
            if captcha is not None:
                captcha.fill(code)
        except Exception as exc:
            if self._mark_connection_lost(exc):
                raise
            self.log("自动登录填表失败：%s" % _redact_urls(exc)[:200])
            return False
        self.log(
            "自动登录已填写用户名/密码%s，点击登录"
            % ("和验证码" if captcha is not None else "")
        )
        button = self._login_button()
        if button is None:
            self.log("自动登录未找到登录按钮，刷新登录页后重试")
            self._refresh_login_captcha(captcha)
            return False
        try:
            self._dom_click(button)
        except Exception as exc:
            if self._mark_connection_lost(exc):
                raise
            self.log("自动登录点击登录失败：%s" % _redact_urls(exc)[:200])
            return False

        deadline = time.monotonic() + self.login_result_timeout_ms / 1000.0
        while time.monotonic() < deadline:
            self.check_cancel()
            if self._login_succeeded():
                self._remember_url()
                self._login_credential_failed = False
                self.log("自动登录成功")
                return True
            error = self._login_error_text()
            if error:
                lowered = error.casefold()
                self._login_credential_failed = any(
                    word in lowered
                    for word in ("密码", "账号", "用户名", "锁定", "禁用")
                )
                self.log("自动登录失败：%s" % _redact_urls(error))
                self._refresh_login_captcha(captcha)
                return False
            if self.page is None or self.page.is_closed():
                raise ReviewStopped("自动登录等待结果时页面已关闭")
            self._wait_for_timeout(self.page, 200)
        self.log("自动登录等待结果超时，刷新验证码后重试")
        self._refresh_login_captcha(captcha)
        return False

    def _open_start_url(self) -> None:
        """打开配置的启动网址，由应用自行跳到登录页；脚本不主动拼 `/login`。"""
        if self.page is None or self.page.is_closed():
            return
        start_url = validate_start_url(
            getattr(self.args, "start_url", "") or self.last_known_url
        )
        if not start_url:
            return
        try:
            if str(self.page.url or "") != start_url:
                # 站点整页 reload 偶尔一直等不到 DOMContentLoaded（mask/资源
                # 加载不结束）；只等导航提交，随后由页面轮询判断登录/菜单。
                self.page.goto(start_url, wait_until="commit")
                self._remember_url()
        except Exception as exc:
            if self._mark_connection_lost(exc):
                raise
            if (
                self._task_list_visible()
                or self._login_page_visible()
                or self._session_ready()
            ):
                self.log("打开启动网址等待超时，但页面已加载，继续")
                self._remember_url()
                return
            self.log("打开启动网址失败：%s" % _redact_urls(exc)[:200])

    def _wait_for_login_or_session(self, timeout_ms: float) -> bool:
        """等待页面渲染出登录表单或系统菜单；返回二者是否已出现其一。"""
        deadline = time.monotonic() + max(1.0, float(timeout_ms)) / 1000.0
        while True:
            if self._login_form_ready() or self._session_ready():
                return True
            if self.page is None or self.page.is_closed():
                return False
            if time.monotonic() >= deadline:
                return False
            self._wait_for_timeout(self.page, 200)

    def _wait_for_post_login_home(self) -> bool:
        """登录成功后等待应用先跳 /home 并渲染菜单，再允许直开任务页。"""
        timeout_ms = min(float(self.timeout_ms), float(_HOME_SETTLE_TIMEOUT_MS))
        deadline = time.monotonic() + max(1.0, timeout_ms) / 1000.0
        while True:
            self.check_cancel()
            if self.page is None or self.page.is_closed():
                return False
            if self._is_404_page():
                self._handle_404_if_visible()
                return False
            if self._is_login_url():
                return False
            route = self._current_route().split("#", 1)[0].rstrip("/")
            if self._session_ready():
                if route == HOME_PAGE_PATH:
                    self.log("登录后首页 /home 已加载")
                else:
                    self.log("登录后应用页已加载：%s" % (route or "/"))
                return True
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                self.log(
                    "等待登录后首页加载超时（%.1f 秒），继续尝试进入任务页"
                    % (timeout_ms / 1000.0)
                )
                return False
            self._wait_for_timeout(
                self.page, int(min(_CANCEL_WAIT_SLICE_MS, remaining * 1000.0))
            )

    def ensure_authenticated(self) -> None:
        """自动登录入口：无重试上限，只有用户停止/连接断开才退出。"""
        attempt = 0
        delay = _LOGIN_RETRY_SECONDS
        while True:
            self.check_cancel()
            if self._handle_404_if_visible():
                continue
            if self._session_ready():
                if attempt:
                    self.log("自动登录已恢复（累计尝试 %d 次）" % attempt)
                return
            if not self.username or not self.password:
                raise RuntimeError("自动登录缺少用户名或密码")
            if not self._login_page_visible():
                # 应用会自己跳到 /login：先等被动跳转，绝不重复导航。
                # 只有真正的空白页没有可等待的跳转，才补开一次配置的启动网址。
                self._wait_for_login_or_session(_LOGIN_SETTLE_TIMEOUT_MS)
                if self._session_ready():
                    return
                if (
                    not self._login_page_visible()
                    and str(self.page.url or "") in ("", "about:blank")
                ):
                    self._open_start_url()
                    self._wait_for_login_or_session(_LOGIN_SETTLE_TIMEOUT_MS)
                if self._session_ready():
                    return
            attempt += 1
            try:
                if self._auto_login_once():
                    self._wait_for_post_login_home()
                    return
            except Exception as exc:
                if self.stop_requested:
                    raise
                if self._mark_connection_lost(exc):
                    raise
                self.log("自动登录第 %d 次异常：%s" % (attempt, _redact_urls(exc)[:200]))
            if self._login_credential_failed:
                delay = _LOGIN_RETRY_MAX_SECONDS
                self.log(
                    "自动登录第 %d 次遇到账号/密码类错误，%d 秒后继续重试"
                    "（无重试上限，避免频繁锁定）" % (attempt, delay)
                )
            else:
                self.log(
                    "自动登录第 %d 次未成功，%d 秒后继续重试（无重试上限）"
                    % (attempt, delay)
                )
            self._sleep_without_page(delay)
            if not self._login_credential_failed:
                delay = min(
                    _LOGIN_RETRY_MAX_SECONDS,
                    max(_LOGIN_RETRY_SECONDS, delay * 1.5),
                )
                if attempt % 10 == 0:
                    delay = _LOGIN_RETRY_SECONDS

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
        self._dom_click(item)

    def _wait_for_task_list(self, timeout_ms: float = None) -> None:
        wait_ms = self.timeout_ms if timeout_ms is None else max(1.0, timeout_ms)
        deadline = time.monotonic() + wait_ms / 1000.0
        while True:
            if self._is_404_page():
                self._handle_404_if_visible()
                raise RuntimeError("任务列表加载到 404 页，已点击返回首页")
            if self._task_list_visible():
                return
            if self.page.is_closed():
                raise RuntimeError("等待缺陷识别任务列表时页面已关闭")
            if time.monotonic() >= deadline:
                raise RuntimeError(
                    "未找到缺陷识别任务列表。请确认账号有权限，且自动登录后能进入"
                    "「智能分析 → 智能识别 → 缺陷识别任务」"
                )
            self._wait_for_timeout(self.page, 200)

    def _goto_task_page_client(self) -> bool:
        """优先走应用前端路由切任务页，避免整页深链接 404/加载卡住。"""
        if self.page is None or self.page.is_closed():
            return False
        try:
            changed = self.page.evaluate(
                r"""
                (path) => {
                  try {
                    if (window.location.pathname === path) return true;
                    if (window.location.hash.indexOf('#/') === 0) {
                      window.location.hash = '#' + path;
                      return true;
                    }
                    window.history.pushState({}, '', path);
                    window.dispatchEvent(new PopStateEvent('popstate'));
                    return true;
                  } catch (_err) {
                    return false;
                  }
                }
                """,
                TASK_PAGE_PATH,
            )
        except Exception:
            return False
        if not changed:
            return False
        try:
            self._wait_for_task_list(timeout_ms=min(self.timeout_ms, 10000))
            self._remember_url()
            self.log("已通过前端路由进入缺陷识别任务列表")
            return True
        except Exception as exc:
            if self.stop_requested:
                raise
            if self._mark_connection_lost(exc):
                raise
            if self._task_list_visible():
                self.log("前端路由未识别到列表，但任务页已可见，按已加载继续")
                self._remember_url()
                return True
            self.log(
                "前端路由切任务页未识别到列表：%s" % _redact_urls(exc)[:200]
            )
            self.log("任务页诊断：%s" % self._task_page_diagnostic())
            return False

    def _goto_task_page_direct(self) -> bool:
        """首页加载完成后尝试同源直达任务页；失败由调用方回启动网址走菜单。"""
        base = validate_start_url(
            getattr(self.args, "start_url", "") or self.last_known_url
        )
        if not base or self.page is None or self.page.is_closed():
            return False
        target = _origin_path(base, TASK_PAGE_PATH)
        if not target:
            return False
        if (
            _page_path(str(self.page.url or "")) == TASK_PAGE_PATH
            and self._task_list_visible()
        ):
            self._remember_url()
            self.log("已在缺陷识别任务列表，无需再导航")
            return True
        if self._goto_task_page_client():
            return True
        try:
            self.log("首页已加载，尝试直接打开任务页：%s" % _page_path(target))
            # 不再用 page.goto 等导航生命周期：站点深链接常出现 DOM 已就绪、
            # 但 commit/navigation 一直不结束，Playwright 会空等 15 秒并误判超时。
            # 这里只触发跳转，随后按「任务列表是否出现」判断业务页面是否就绪。
            try:
                self.page.evaluate(
                    "(url) => { window.location.assign(url); return true; }",
                    target,
                )
            except Exception as exc:
                if self.stop_requested:
                    raise
                if self._mark_connection_lost(exc):
                    raise
                # location.assign 会销毁旧执行上下文，evaluate 抛错是正常现象。
            self._wait_for_task_list(timeout_ms=min(self.timeout_ms, 10000))
            self._remember_url()
            self.log("已直达缺陷识别任务列表")
            return True
        except Exception as exc:
            if self.stop_requested:
                raise
            if self._mark_connection_lost(exc):
                raise
            if self._task_list_visible():
                self.log("直接打开任务页等待失败，但任务页已可见，按已加载继续")
                self._remember_url()
                return True
            self.log(
                "直接打开任务页失败，回启动网址后走菜单：%s"
                % _redact_urls(exc)[:200]
            )
            return False

    def _click_return_home(self) -> bool:
        """优先点 404 页「返回首页」或顶部「首页」，走前端路由回首页。

        站点整页 reload 偶尔卡在 mask/资源加载，Playwright 的 goto 会一直
        等 DOMContentLoaded；点应用自己的返回首页链接不会触发整页加载。
        """
        if self.page is None or self.page.is_closed():
            return False
        for text, exact in (("返回首页", True), ("首页", True), ("返回首页", False)):
            try:
                locator = self.page.get_by_text(text, exact=exact)
            except Exception:
                continue
            for index in range(locator.count()):
                item = locator.nth(index)
                try:
                    if not item.is_visible():
                        continue
                    self._dom_click(item)
                    self._wait_for_timeout(self.page, 500)
                    self.log("已点击「%s」返回首页" % text)
                    return True
                except Exception:
                    continue
        return False

    def _is_404_page(self) -> bool:
        if self.page is None or self.page.is_closed():
            return False
        text = ""
        try:
            body = self.page.locator("body").first
            if body.count():
                text = _normalize_text(body.inner_text(timeout=1000))
        except Exception:
            text = ""
        if not text:
            try:
                text = _normalize_text(self.page.inner_text(timeout=1000))
            except Exception:
                return False
        if "返回首页" not in text:
            return False
        # 首页/任务页外壳始终带「智能分析」「统计报表」菜单；这类页面即使
        # 残留 404 提示也不能当整页 404 处理，否则会点回首页打断任务导航。
        if "智能分析" in text and "统计报表" in text:
            return False
        lowered = text.casefold()
        # 404 文案必须与「返回首页」相邻，避免首页统计数字 404 误判。
        markers = ("404", "页面不存在", "页面未找到", "not found", "找不到页面")
        for marker in markers:
            escaped = re.escape(marker)
            if re.search(
                r"(?:%s).{0,40}返回首页|返回首页.{0,40}(?:%s)"
                % (escaped, escaped),
                lowered,
            ):
                return True
        return False

    def _handle_404_if_visible(self) -> bool:
        """任何位置看到 404 都先点「返回首页」；点不到再回启动网址。"""
        if not self._is_404_page():
            return False
        self.log("检测到 404 页面，点击「返回首页」恢复")
        if self._click_return_home():
            self._wait_for_login_or_session(_LOGIN_SETTLE_TIMEOUT_MS)
            return True
        self.log("404 页面没有可点击的「返回首页」，回启动网址")
        self._open_start_url()
        self._wait_for_login_or_session(_LOGIN_SETTLE_TIMEOUT_MS)
        return True

    def open_task_list(self) -> bool:
        if self.page is None:
            raise RuntimeError("尚未连接受控浏览器")
        # 任何位置看到 404 都先点「返回首页」；再按登录/菜单流程恢复。
        self._handle_404_if_visible()
        # 404/深链接错误页既没有登录表单也没有系统菜单：优先点应用自己的
        # 「返回首页」（前端路由，不触发整页 reload），点不到再回启动网址。
        if not self._session_ready() and not self._login_page_visible():
            self.log("当前既不是登录页也看不到系统菜单，先尝试返回首页")
            if not self._click_return_home():
                self._open_start_url()
            self._wait_for_login_or_session(_LOGIN_SETTLE_TIMEOUT_MS)
        # 只保留自动登录：掉登录/重启后都由脚本自己登录并进入业务页。
        self.ensure_authenticated()
        if self._task_list_visible():
            self._remember_url()
            self.log("当前页面已在缺陷识别任务列表，跳过菜单导航")
            return False
        # 登录后应用已落在 /home 且菜单渲染完成，再试前端路由/同源直达任务页；
        # 站点不支持深链接时回首页走菜单（不能停在 404 页）。
        if self._goto_task_page_direct():
            return True
        self.log("直接打开任务页不可用，改走菜单")
        if not self._session_ready():
            if not self._click_return_home():
                self._open_start_url()
            self._wait_for_login_or_session(_LOGIN_SETTLE_TIMEOUT_MS)
        if self._login_page_visible():
            self.ensure_authenticated()
        last_error = None
        for attempt in range(2):
            try:
                for label in TASK_LIST_MENU_PATH[:-1]:
                    self._open_menu(label)
                    self._wait_for_timeout(self.page, 300)
                self._click_menu(TASK_LIST_MENU_PATH[-1])
                self._wait_for_task_list()
                self._remember_url()
                self.log("已进入缺陷识别任务列表")
                return True
            except Exception as exc:
                last_error = exc
                if self.stop_requested:
                    raise
                if self._mark_connection_lost(exc):
                    raise
                self.log(
                    "菜单导航失败：%s" % _redact_urls(exc)[:200]
                )
                self.log("任务页诊断：%s" % self._task_page_diagnostic())
                if attempt == 0:
                    self.log("尝试返回首页后重试菜单导航")
                    if not self._click_return_home():
                        self._open_start_url()
                    self._wait_for_login_or_session(_LOGIN_SETTLE_TIMEOUT_MS)
                    if not self._session_ready():
                        self.ensure_authenticated()
        raise last_error or RuntimeError(
            "未找到缺陷识别任务列表。请确认账号有权限，且能进入"
            "「智能分析 → 智能识别 → 缺陷识别任务」"
        )

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

    def _visible_dropdowns(self) -> list:
        dropdowns = self.page.locator(".el-select-dropdown:visible")
        return [dropdowns.nth(index) for index in range(dropdowns.count())]

    def _find_dropdown_option_any(self, value: str):
        """在全部可见下拉中找选项，避免 .last 命中空的旧下拉。"""
        last_error = None
        for dropdown in self._visible_dropdowns():
            try:
                return self._find_dropdown_option(dropdown, value)
            except Exception as exc:
                last_error = exc
        raise last_error or RuntimeError("筛选下拉未展开")

    def _selected_text(self, item) -> str:
        texts = []
        select = item.locator(".el-select:visible").first
        if select.count():
            texts.append(select.inner_text(timeout=500))
            input_locator = select.locator("input").first
            if input_locator.count():
                texts.append(input_locator.input_value())
        texts.append(item.inner_text(timeout=500))
        return _normalize_text(" ".join(str(text or "") for text in texts))

    def _find_dropdown_option(self, dropdown, value: str):
        options = dropdown.locator(".el-select-dropdown__item:visible")
        for index in range(options.count()):
            option = options.nth(index)
            if _normalize_text(option.inner_text(timeout=500)) == value:
                return option
        raise RuntimeError("下拉中没有找到选项「%s」" % value)

    def _click_dropdown_option_by_text(self, value: str) -> bool:
        """按可见文本直接点击下拉项，绕过 nth 索引失效/定位器等待。"""
        return bool(
            self.page.evaluate(
                r"""
                (value) => {
                  const norm = (v) => String(v == null ? '' : v)
                    .replace(/\s+/g, ' ').trim();
                  const items = Array.from(
                    document.querySelectorAll('.el-select-dropdown__item')
                  );
                  for (const el of items) {
                    const rect = el.getBoundingClientRect();
                    if (!rect.width || !rect.height) continue;
                    if (getComputedStyle(el).visibility === 'hidden') continue;
                    if (norm(el.innerText) !== value) continue;
                    const opts = {
                      bubbles: true, cancelable: true, composed: true,
                      view: window, button: 0, buttons: 1
                    };
                    // 现场点击的是选项内部 span，事件冒泡到 li；按同样路径点。
                    const target = el.querySelector('.el-tooltip__trigger') || el;
                    target.dispatchEvent(new MouseEvent('mousedown', opts));
                    target.dispatchEvent(new MouseEvent('mouseup', opts));
                    target.dispatchEvent(new MouseEvent('click', opts));
                    return true;
                  }
                  return false;
                }
                """,
                value,
            )
        )

    def _wait_dropdown_option(
        self, value: str, description: str, timeout_ms: float = 3000.0
    ):
        """等待下拉选项渲染；慢页面/重新渲染时避免 150ms 后就判失败。"""
        deadline = time.monotonic() + max(0.1, float(timeout_ms)) / 1000.0
        last_error = None
        while True:
            self.check_cancel()
            try:
                return self._find_dropdown_option_any(value)
            except Exception as exc:
                last_error = exc
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise RuntimeError(
                    "筛选项「%s」等待选项「%s」超时：%s"
                    % (description, value, str(last_error or "")[:200])
                )
            self._wait_for_timeout(
                self.page, int(min(100, max(1, remaining * 1000.0)))
            )

    def _dropdown_open(self, timeout_ms: float = 500.0) -> bool:
        deadline = time.monotonic() + max(0.1, float(timeout_ms)) / 1000.0
        while True:
            try:
                if self.page.locator(".el-select-dropdown:visible").count():
                    return True
            except Exception:
                pass
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return False
            self._wait_for_timeout(
                self.page, int(min(100, max(1, remaining * 1000.0)))
            )

    def _select_is_open(self, select) -> bool:
        """只认当前筛选控件自身的展开状态，避免被页面上其它下拉误判。"""
        targets = [select]
        for selector in (
            ".el-select__wrapper",
            "input[role='combobox']",
            "input[aria-expanded]",
        ):
            try:
                target = select.locator(selector).first
                if target.count():
                    targets.append(target)
            except Exception:
                continue
        for target in targets:
            try:
                value = target.get_attribute("aria-expanded")
            except Exception:
                continue
            if str(value or "").lower() == "true":
                return True
        return False

    def _open_select_dropdown(self, select, description: str = "") -> None:
        """打开 Element 下拉；DOM click 打不开时用原生 click 兜底。"""
        if self._select_is_open(select):
            if description:
                self.log("筛选项「%s」下拉已展开" % description)
            return
        if self._dropdown_open(50):
            # 页面上残留的其它下拉不能当作当前控件已展开，先收起再点。
            self._reset_dropdown()
        if description:
            self.log("尝试展开筛选项「%s」下拉" % description)
        # 现场抓取确认：真实可点击目标是 .el-select__wrapper；优先用原生
        # 点击（可信事件），避免逐个 DOM click 失败各等一个 action_timeout。
        try:
            wrapper = select.locator(".el-select__wrapper").first
            if wrapper.count() and wrapper.is_visible():
                wrapper.click(timeout=1500, force=True)
                if self._dropdown_open(1500):
                    if description:
                        self.log("已展开筛选项「%s」下拉" % description)
                    return
        except Exception:
            pass
        # 原生点击被遮挡/窗口缩放时，不依赖坐标的鼠标事件序列优先。
        try:
            wrapper = select.locator(".el-select__wrapper").first
            if wrapper.count() and wrapper.is_visible():
                self._dispatch_mouse_click(wrapper)
                if self._dropdown_open(1200):
                    if description:
                        self.log("已展开筛选项「%s」下拉" % description)
                    return
        except Exception:
            pass
        # 最后只尝试 wrapper/select 两处 DOM click；不再遍历全部子元素，
        # 避免每个失败目标各等一个 action_timeout 造成几十秒空转。
        for target in (wrapper, select):
            try:
                self._dom_click(target)
            except Exception:
                continue
            if self._dropdown_open(800):
                if description:
                    self.log("已展开筛选项「%s」下拉" % description)
                return
        try:
            select.click(timeout=1500)
        except Exception as exc:
            raise RuntimeError(
                "筛选项「%s」下拉展开失败：%s"
                % (description, _redact_urls(exc)[:200])
            ) from exc
        if not self._dropdown_open(1000):
            raise RuntimeError("筛选项「%s」下拉未展开" % description)

    def _reset_dropdown(self) -> None:
        """收起当前下拉，供选项为空时重新展开。"""
        try:
            self.page.keyboard.press("Escape")
        except Exception:
            pass
        self._wait_for_timeout(self.page, 100)

    def _read_dropdown_options(
        self,
        select,
        description: str,
        attempts: int = 6,
        per_attempt_ms: float = 500.0,
    ) -> list:
        """读取下拉选项；为空时反复收起/重新展开，不靠输入过滤。"""
        last_error = None
        for attempt in range(1, max(1, attempts) + 1):
            if attempt > 1:
                self._reset_dropdown()
                self.log(
                    "筛选项「%s」下拉选项为空，重新展开（第 %d/%d 次）"
                    % (description, attempt, attempts)
                )
            try:
                self._open_select_dropdown(select, description)

                def options_ready():
                    try:
                        for dropdown in self._visible_dropdowns():
                            if (
                                dropdown.locator(
                                    ".el-select-dropdown__item:visible"
                                ).count()
                                > 0
                            ):
                                return True
                        return False
                    except Exception:
                        return False

                self._wait_for_value(
                    options_ready,
                    per_attempt_ms,
                    "筛选项「%s」下拉选项" % description,
                    RuntimeError,
                )
                texts = []
                for dropdown in self._visible_dropdowns():
                    options = dropdown.locator(".el-select-dropdown__item:visible")
                    for index in range(options.count()):
                        text = _normalize_text(options.nth(index).inner_text(timeout=500))
                        if text and text not in texts:
                            texts.append(text)
                if texts:
                    return texts
            except Exception as exc:
                last_error = exc
        raise RuntimeError(
            "筛选项「%s」下拉选项为空（已重新展开 %d 次）：%s"
            % (description, attempts, str(last_error or "")[:200])
        )

    def _pick_dropdown_option(
        self,
        select,
        value: str,
        description: str,
        attempts: int = 6,
        per_attempt_ms: float = 500.0,
    ):
        """按选项文字选择；下拉为空或选项未加载时反复收起/重新展开。"""
        last_error = None
        for attempt in range(1, max(1, attempts) + 1):
            if attempt > 1:
                self._reset_dropdown()
                self.log(
                    "筛选项「%s」下拉为空/未找到「%s」，重新展开"
                    "（第 %d/%d 次）"
                    % (description, value, attempt, attempts)
                )
            try:
                self._open_select_dropdown(select, description)
                return self._wait_dropdown_option(
                    value, description, timeout_ms=per_attempt_ms
                )
            except Exception as exc:
                last_error = exc
        raise RuntimeError(
            "筛选项「%s」选择「%s」失败（已重新展开 %d 次）：%s"
            % (description, value, attempts, str(last_error or "")[:200])
        )

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
        self.log("设置筛选「%s」=%s" % (description, value))
        option = self._pick_dropdown_option(select, value, description)
        if before_click is not None:
            before_click()
        # 下拉项可能因重新渲染/虚拟列表导致刚才的 nth 定位器失效；
        # 优先按文本在页面内重新查找并点击，坐标和索引都不参与。
        clicked = False
        try:
            clicked = self._click_dropdown_option_by_text(value)
        except Exception:
            clicked = False
        if not clicked:
            try:
                self._dom_click(option)
            except Exception as exc:
                raise RuntimeError(
                    "筛选项「%s」选择「%s」点击失败：%s"
                    % (description, value, str(exc)[:200])
                ) from exc
        self._wait_for_timeout(self.page, 200)
        actual = self._selected_text(item)
        if value not in actual:
            # JS 点击可能命中了同文本的其它下拉/未触发选择，再用定位器点一次。
            try:
                self._dom_click(option)
            except Exception:
                pass
            self._wait_for_timeout(self.page, 200)
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

    def _select_voltage(self, target: str, require_candidates: bool = True) -> None:
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
        if not data and require_candidates:
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
        if not self._wait_page_settled():
            raise RuntimeError("电压等级切换后页面导航未完成")

    def _type_line_filter(self, input_locator, value: str) -> bool:
        """像人一样先点输入框、全选删除、逐字输入，触发 Element 的过滤。"""
        try:
            input_locator.click(timeout=1500)
            try:
                input_locator.press("Control+A")
            except Exception:
                pass
            try:
                input_locator.press("Backspace")
            except Exception:
                pass
            type_slowly = getattr(input_locator, "press_sequentially", None)
            if type_slowly is not None:
                type_slowly(value, delay=30)
            else:
                input_locator.type(value, delay=30)
            return True
        except Exception:
            return False

    def _select_line(self, typed: str, verify_candidate: bool = True) -> str:
        item = self._find_form_item(("线路名称",), "线路名称")
        select = item.locator(".el-select:visible").first
        if not select.count():
            select = item.locator(".el-select").first
        if not select.count():
            raise RuntimeError("线路名称没有下拉控件")
        # Element 下拉里混有隐藏表单 input；必须优先用 el-select__input，
        # 否则 JS 写入的是隐藏值，页面/接口都看不到，窄窗口下最容易触发。
        input_locator = select.locator("input.el-select__input:visible").first
        if not input_locator.count():
            input_locator = select.locator("input.el-select__input").first
        if not input_locator.count():
            input_locator = select.locator("input:visible").first
        if not input_locator.count():
            input_locator = select.locator("input").first
        if not input_locator.count():
            input_locator = item.locator("input").first
        if not input_locator.count():
            raise RuntimeError("线路名称没有可输入的输入框")
        # 先清空可能残留的过滤文本，再先输入锁定候选；线路列表很长，
        # 直接展开会要求滚动才能看到“220kV吕汾Ⅰ线”。
        try:
            if str(input_locator.input_value() or "").strip():
                self._set_input_value(input_locator, "", "线路名称", js_only=True)
        except Exception:
            pass
        matched = [
            _normalize_text(line.get("lineName"))
            for line in self.line_candidates
            if isinstance(line, dict)
            and typed
            and typed in _normalize_text(line.get("lineName"))
        ]
        filter_text = typed
        if len(matched) == 1 and matched[0]:
            filter_text = matched[0]
        typed_ok = self._type_line_filter(input_locator, filter_text)
        try:
            actual_typed = str(input_locator.input_value() or "").strip()
        except Exception:
            actual_typed = ""
        if not typed_ok or filter_text not in actual_typed:
            self._set_input_value(
                input_locator, filter_text, "线路名称", js_only=False
            )
        self._wait_for_timeout(self.page, 300)

        chosen = ""
        candidate = None

        def line_options(values):
            return [
                text
                for text in (_normalize_text(value) for value in values)
                if text and text not in ("全部", "请选择", "全部线路")
            ]

        try:
            texts = self._read_dropdown_options(
                select, "线路名称", attempts=12, per_attempt_ms=500.0
            )
            chosen = choose_unique_line(line_options(texts), typed)
        except RuntimeError:
            # 输入过滤未触发时再用 JS 事件重写一次；仍失败则保留原错误。
            self._set_input_value(
                input_locator, filter_text, "线路名称", js_only=True
            )
            texts = self._read_dropdown_options(
                select, "线路名称", attempts=12, per_attempt_ms=500.0
            )
            chosen = choose_unique_line(line_options(texts), typed)
        for line in self.line_candidates:
            if (
                isinstance(line, dict)
                and _normalize_text(line.get("lineName")) == chosen
            ):
                candidate = line
                break
        if candidate is None and verify_candidate:
            raise RuntimeError(
                "线路下拉「%s」不在目标电压接口候选内，无法核对" % chosen
            )
        option = self._pick_dropdown_option(select, chosen, "线路名称")
        clicked = False
        try:
            clicked = self._click_dropdown_option_by_text(chosen)
        except Exception:
            clicked = False
        if not clicked:
            self._dom_click(option)
        self._wait_for_timeout(self.page, 200)
        actual = self._selected_text(item)
        if chosen not in actual:
            raise RuntimeError(
                "线路选择核对失败：预期「%s」，页面显示「%s」" % (chosen, actual)
            )
        self.selected_line_name = chosen
        self.selected_line_guid = (
            _normalize_text(candidate.get("lineGuid")) if candidate else ""
        )
        return chosen

    def _fill_date(self, labels: Sequence[str], value: str, description: str) -> None:
        item = self._find_form_item(labels, description)
        input_locator = item.locator("input:visible").first
        if not input_locator.count():
            input_locator = item.locator("input").first
        if not input_locator.count():
            raise RuntimeError("%s没有可填写的输入框" % description)
        self._dom_click(input_locator)
        self._set_input_value(input_locator, value, description, js_only=True)
        try:
            input_locator.press("Enter")
        except Exception:
            pass
        self._wait_for_timeout(self.page, 100)
        actual = str(input_locator.input_value() or "").strip()
        if not actual.startswith(value):
            raise RuntimeError(
                "%s填写失败：输入「%s」，页面显示「%s」" % (description, value, actual)
            )

    def _set_input_value(
        self,
        input_locator,
        value: str,
        description: str,
        js_only: bool = False,
    ) -> None:
        try:
            input_locator.evaluate(
                "(el) => { el.removeAttribute('readonly');"
                " el.removeAttribute('disabled'); }"
            )
        except Exception:
            pass
        if not js_only:
            try:
                input_locator.fill(value)
            except Exception:
                pass
        actual = str(input_locator.input_value() or "").strip()
        if js_only or not actual.startswith(value):
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
        self.selected_line_name = ""
        self.selected_line_guid = ""
        self.line_candidates = []
        self.check_cancel()
        self._select_form_option(("任务状态",), settings.task_status, "任务状态")
        self.check_cancel()
        if settings.mission_type:
            self._select_form_option(("作业性质",), settings.mission_type, "作业性质")
        else:
            self.log("作业性质留空：不操作该筛选项")
        self.check_cancel()
        if settings.voltage_level:
            self._select_voltage(
                settings.voltage_level, require_candidates=bool(settings.line_name)
            )
        else:
            self.log("电压等级留空：不操作该筛选项")
            self.line_candidates = []
            self.selected_line_name = ""
            self.selected_line_guid = ""
        self.check_cancel()
        if settings.line_name:
            line_name = self._select_line(
                settings.line_name, verify_candidate=bool(settings.voltage_level)
            )
            if not self._wait_page_settled():
                raise RuntimeError("线路筛选后页面导航未完成")
        else:
            self.log("线路名称留空：不操作该筛选项")
            line_name = ""
            self.selected_line_name = ""
            self.selected_line_guid = ""
        self.check_cancel()
        self._fill_date_range(settings.start_date, settings.end_date)
        self.log(
            "已设置筛选：任务状态=%s；作业性质=%s；电压等级=%s；线路=%s；"
            "开始=%s；结束=%s"
            % (
                settings.task_status,
                settings.mission_type or "全部",
                settings.voltage_level or "全部",
                line_name or "全部",
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
        if settings.mission_type:
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
        else:
            flags["mission_type"] = True
        if settings.line_name:
            expected_line = self.selected_line_name or _normalize_text(
                settings.line_name
            )
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
        else:
            flags["line"] = True
        if settings.voltage_level:
            if "voltageLevel" in params:
                actual_voltage = _normalize_text(params["voltageLevel"])
                expected_code = VOLTAGE_LEVEL_CODES[settings.voltage_level]
                if actual_voltage not in (expected_code, settings.voltage_level):
                    raise RuntimeError(
                        "查询请求 voltageLevel=%s，与筛选电压等级「%s」不一致"
                        % (actual_voltage, settings.voltage_level)
                    )
                flags["voltage"] = True
        else:
            flags["voltage"] = True
        return flags

    def _verify_records_conditions(
        self, records: Sequence[object], settings: ReviewSettings, flags: dict
    ) -> None:
        expected_status = TASK_STATUS_CODES[settings.task_status]
        expected_mission = (
            MISSION_TYPE_CODES[settings.mission_type] if settings.mission_type else ""
        )
        expected_line = (
            self.selected_line_name or _normalize_text(settings.line_name)
            if settings.line_name
            else ""
        )
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
            if settings.mission_type:
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
            if settings.line_name:
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
        self.log("筛选已设置，点击查询")
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
            header = _normalize_text(header_locator.inner_text(timeout=1000))
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
        """等接口记录对应的表格行渲染；系统卡时最多等 15 秒，避免误判空表。"""
        wait_ms = max(1000.0, min(float(self.timeout_ms), 15000.0))
        deadline = time.monotonic() + wait_ms / 1000.0
        last_error = None
        while True:
            try:
                return self._rows_for_records(records)
            except RuntimeError as exc:
                last_error = exc
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    break
                self._wait_for_timeout(
                    self.page, int(min(200, max(1, remaining * 1000.0)))
                )
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
        target_guid: Optional[str] = None,
    ):
        """扫描任务列表并按页收集候选。

        给 target_guid 时扫到该任务就立即返回：刷新后重找同一任务时浏览器
        仍停在目标页，惰性行不会因为后续翻页失效。普通扫描（含
        stop_at_first）行为不变，不会为每个正常任务多跳一次页。
        """
        excluded = _id_set(excluded_ids)
        target = _normalize_text(target_guid).casefold()
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
                is_target = bool(target) and guid.casefold() == target
                row = rows[index]
                if row is None:
                    if is_target:
                        # 目标行已定位到却不能核对：继续翻页只会让惰性行失效，
                        # 在目标页直接失败，交给 run 层按「找不到任务」处理。
                        raise ReviewStopped(
                            "目标任务在第 %d 页已找到，但对应行未渲染，"
                            "不能继续翻页查找" % page_number
                        )
                    summary["skipped"] += 1
                    continue
                button = self._enter_button(row)
                if button is None:
                    if is_target:
                        raise ReviewStopped(
                            "目标任务在第 %d 页已找到，但没有可用的「进入」按钮，"
                            "不能继续翻页查找" % page_number
                        )
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
                if is_target:
                    summary["scanned"] = seen
                    return candidates, summary
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
            # （如第 3 页变第 2 页、第 4 页变第 3 页），直接跳回该页即可。
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
                self._mark_connection_lost(exc)
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
        target_key = target.casefold()
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
                settings, start_page=start_page, target_guid=target
            )
            for candidate in candidates:
                if _normalize_text(candidate.get("guid")).casefold() == target_key:
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
        self.anomaly_keys = {_anomaly_row_key(row) for row in self.anomaly_rows}
        self.anomaly_dirty = True
        # JSONL 也要同步撤回：否则同一输出目录下次启停会把这些已失效的
        # 中止异常当成历史重新恢复回来。
        self._drop_jsonl_task_abort_anomalies(target)
        self.log("重试成功，已移除该任务的审核中止异常记录")

    def _drop_jsonl_task_abort_anomalies(self, task_guid: str) -> None:
        if not self.summary_jsonl_path.is_file():
            return
        records, _damaged = _summary_jsonl_records(self.summary_jsonl_path)
        kept = [
            (kind, row)
            for kind, row in records
            if not (
                kind == "anomaly"
                and len(row) > 4
                and _normalize_text(row[4]) == task_guid
                and row[1] in ("审核中止", "未知异常")
            )
        ]
        if len(kept) != len(records):
            self._write_summary_jsonl(kept)

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
            # 正常 Python 轮询视为“仍在活动”；真正的 Playwright 静默卡死不会走到这里。
            self.last_activity = time.monotonic()
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
        self._last_active_thumbnail_index = -1
        self._image_index_source = None
        self._image_index = {}
        # 任务边界清空图片身份：blob:/data: 回退不能继承上一张的 fileGuid。
        self._loaded_photo_guid = ""

    def _mark_loaded_photo(self, image: ReviewImage) -> None:
        """记下本轮刚加载成功的图片身份，供 blob:/data: 回退认领字节。"""
        guid = _normalize_text(getattr(image, "file_guid", ""))
        self._loaded_photo_guid = guid

    def _scan_tagging_details(self, wanted: str, start: int = 0):
        """在已收到的响应里找该图片的缺陷框明细，返回 (数据, 错误说明)。

        最新的响应用不上时继续往前找：后到的非 200/坏响应不能盖掉更早的
        有效 200；只要有一条有效 200 就以它为准（空列表也是有效结论）。
        只看起点之后的响应（监听本身每个任务开始时会清空）。
        """
        error = ""
        responses = self.monitor.responses[max(0, int(start)) :]
        for response in reversed(responses):
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
            status = getattr(response, "status", 0)
            if status != 200:
                error = error or "HTTP 状态 %s" % status
                continue
            try:
                payload = response.json()
            except Exception as exc:
                error = error or "响应读取失败：%s" % str(exc)[:120]
                continue
            try:
                data = parse_api_data(payload, "系统缺陷框明细")
            except RuntimeError as exc:
                error = error or str(exc)[:120]
                continue
            if not isinstance(data, list):
                error = error or "响应 data 不是数组"
                continue
            return data, ""
        return None, error

    def _collect_tagging_details(
        self,
        picture_guid: str,
        start_mark: int,
        timeout_ms: float = None,
        allow_missing: bool = False,
    ) -> list:
        """读取当前图片的系统缺陷框明细。

        读失败（非 200、解析失败、等待超时）一律抛 TaggingDetailError，
        交给任务级异常/重试处理；绝不返回 [] 冒充「没有缺陷框」。
        只有接口明确返回空数组，或 allow_missing 的短探针等不到响应时才返回 []。
        """
        wanted = picture_guid.casefold()
        start = max(0, int(start_mark or 0))
        if self.monitor is None:
            if allow_missing:
                return []
            raise TaggingDetailError(
                "尚未建立响应监听，读不到图片 %s 的系统缺陷框明细" % picture_guid
            )

        data, error = self._scan_tagging_details(wanted, start)
        if data is not None:
            return data
        if error:
            raise TaggingDetailError(
                "图片 %s 的系统缺陷框明细读取失败：%s" % (picture_guid, error)
            )
        wait_ms = (
            self.tagging_detail_timeout_ms
            if timeout_ms is None
            else max(0.0, float(timeout_ms))
        )
        if wait_ms <= 0:
            if allow_missing:
                return []
            raise TaggingDetailError(
                "图片 %s 的系统缺陷框明细尚未返回" % picture_guid
            )

        deadline = time.monotonic() + wait_ms / 1000.0
        while True:
            self.check_cancel()
            data, error = self._scan_tagging_details(wanted, start)
            if data is not None:
                return data
            if error:
                raise TaggingDetailError(
                    "图片 %s 的系统缺陷框明细读取失败：%s" % (picture_guid, error)
                )
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                if allow_missing:
                    return []
                raise TaggingDetailError(
                    "等待图片 %s 的系统缺陷框明细超时（%.1f 秒）"
                    % (picture_guid, wait_ms / 1000.0)
                )
            if self.page is not None and self.page.is_closed():
                raise TaggingDetailError(
                    "等待图片 %s 的系统缺陷框明细时页面已关闭" % picture_guid
                )
            if self.page is None:
                time.sleep(min(_CANCEL_WAIT_SLICE_MS / 1000.0, remaining))
            else:
                self._wait_for_timeout(
                    self.page, int(min(_CANCEL_WAIT_SLICE_MS, remaining * 1000.0))
                )

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

    def _photo_output_dir(self, task_info: dict, image: ReviewImage) -> Path:
        """输出目录/线路名称/杆塔号；优先图片级线路杆塔，缺失时用任务级兜底。"""
        line = _safe_path_part(
            image.line_name or (task_info or {}).get("line_name"),
            PHOTO_UNKNOWN_LINE,
        )
        tower = _safe_path_part(
            image.tower_name or (task_info or {}).get("tower_name"),
            PHOTO_UNKNOWN_TOWER,
        )
        directory = self.outdir / line
        if directory.exists() and not directory.is_dir():
            directory = self.outdir / (line + "_线路")
        directory = directory / tower
        if directory.exists() and not directory.is_dir():
            directory = directory.parent / (tower + "_杆塔")
        return directory

    def _photo_download_url(self, file_guid: str) -> str:
        base = ""
        if self.page is not None and not self.page.is_closed():
            base = _safe_origin(self.page.url)
        if not base:
            base = _safe_origin(getattr(self.args, "start_url", ""))
        if not base:
            raise RuntimeError("无法确定系统地址，不能下载缺陷照片")
        return base + FILE_DOWNLOAD_API + "?" + urlencode(
            {"fileGuid": file_guid, "isThumb": "no"}
        )

    @staticmethod
    def _original_photo_url(source: str) -> str:
        """把页面缩略图 URL 的 isThumb=yes/1/true 改成 no，拿到原图地址。"""
        value = str(source or "")
        if not value:
            return ""
        changed = re.sub(
            r"(?i)([?&]isThumb=)(?:yes|1|true)\b",
            r"\1no",
            value,
        )
        return changed if changed != value else ""

    def _fetch_loaded_photo(self, image: ReviewImage):
        """读取页面已加载的当前原图；返回 (bytes, mime, src, 是否缩略图)。"""
        if self.page is None or self.page.is_closed():
            return None
        wanted = _normalize_text(image.file_guid).casefold()
        # 页面上找不到当前 fileGuid 时不能拿别的大图顶替，否则会存错照片。
        if not wanted:
            return None
        frames = [self.page]
        try:
            frames.extend([frame for frame in self.page.frames if frame is not self.page])
        except Exception:
            pass
        best = None
        for frame in frames:
            try:
                value = frame.evaluate(
                    _PHOTO_LOADED_SCRIPT, {"guid": image.file_guid}
                )
            except Exception:
                continue
            if not isinstance(value, dict) or not value.get("b64"):
                continue
            source = str(value.get("src") or "")
            blob_like = source.lower().startswith(("blob:", "data:"))
            # http 地址必须带当前 fileGuid；blob:/data: 没有地址标识，只有
            # 本轮刚加载成功的正是这张图（fileGuid 一致）时才认领字节，
            # 否则会把残留的上一张图存成这一张。
            if not source or (wanted not in source.casefold() and not blob_like):
                continue
            if blob_like and (
                not self._loaded_photo_guid
                or self._loaded_photo_guid.casefold() != wanted
            ):
                continue
            try:
                data = base64.b64decode(str(value.get("b64")))
            except Exception:
                continue
            mime = str(value.get("type") or "").split(";")[0].strip()
            detected = _detect_image_mime(data)
            if not data or not (detected or mime.startswith("image/")):
                continue
            width = int(value.get("width") or 0)
            height = int(value.get("height") or 0)
            area = width * height
            # 现场审核页缩略图是 blob: URL，src 里没有 isThumb 标记，
            # 只能按已加载尺寸判断：长边不超过 200、短边不超过 150 视为缩略图
            # （横版 200×150 与竖版 150×200 都覆盖），交给拒绝保存分支；
            # 正常大图与尺寸未知不受影响。
            long_side = max(width, height)
            short_side = min(width, height)
            thumbnail = bool(value.get("thumbnail")) or (
                0 < short_side <= PHOTO_THUMBNAIL_MAX_SHORT_SIDE
                and long_side <= PHOTO_THUMBNAIL_MAX_LONG_SIDE
            )
            if best is None or area > best[2]:
                best = (
                    data,
                    detected or mime or _image_mime(data),
                    area,
                    str(value.get("src") or ""),
                    thumbnail,
                )
        return best[:2] + best[3:] if best else None

    def _fetch_photo_bytes(self, image: ReviewImage):
        """取缺陷照片原图：页面已加载响应 > 当前图片地址 > 合成下载地址。"""
        cached = self._cached_photo_bytes(image)
        if cached is not None:
            return cached
        sources = self._current_photo_sources(image.file_guid)
        errors = []
        # 当前页可能有多个原图地址（代理地址、改写后的缩略图地址等）：
        # 按顺序逐个尝试，任何一条成功即可，不能只试第一个就放弃。
        for source in sources:
            url = source
            if _is_thumbnail_photo_url(url):
                url = self._original_photo_url(url)
            if not url or url.startswith(("data:", "blob:")):
                continue
            try:
                return self._fetch_photo_url(url)
            except Exception as exc:
                errors.append(str(exc)[:200])
        try:
            return self._fetch_photo_url(self._photo_download_url(image.file_guid))
        except Exception as exc:
            errors.append(str(exc)[:200])
        raise RuntimeError(
            "；".join(error for error in errors if error) or "缺陷照片下载失败"
        )

    def _current_photo_sources(self, file_guid: str) -> list:
        """读当前页主图地址；只保留 URL 含当前 fileGuid 的项，避免存错图片。"""
        if self.page is None or self.page.is_closed():
            return []
        wanted = _normalize_text(file_guid).casefold()
        # 没有当前 fileGuid 时无法判断哪张图属于这张照片，宁可不取。
        if not wanted:
            return []
        frames = [self.page]
        try:
            frames.extend([frame for frame in self.page.frames if frame is not self.page])
        except Exception:
            pass
        sources = []
        for frame in frames:
            try:
                value = frame.evaluate(_PHOTO_SOURCE_SCRIPT)
            except Exception:
                continue
            if not isinstance(value, list):
                continue
            for item in value:
                source = str(item or "")
                if not source or source in sources:
                    continue
                if wanted not in source.casefold():
                    continue
                sources.append(source)
        return sources

    def _cached_photo_bytes(self, image: ReviewImage):
        """按 fileGuid 复用页面已加载的原图响应；最新响应优先。"""
        wanted = _normalize_text(image.file_guid).casefold()
        for url, (guid, response) in reversed(list(self._photo_responses.items())):
            if guid != wanted:
                continue
            try:
                data = response.body()
                mime = str((response.headers or {}).get("content-type") or "")
                mime = mime.split(";")[0].strip()
                detected = _detect_image_mime(data)
                if data and (detected or mime.startswith("image/")):
                    return data, detected or mime or _image_mime(data)
            except Exception:
                pass
            self._photo_responses.pop(url, None)
        return None

    def _fetch_photo_url(self, url: str):
        """按 URL 取原图；请求头复用页面图片请求，避免再次 401。"""
        last_error = ""
        if self.page is None or self.page.is_closed():
            raise RuntimeError("受控浏览器页面已关闭，不能下载缺陷照片")
        headers = {
            "Referer": self.page.url,
        }
        for key, value in (getattr(self, "_file_request_headers", {}) or {}).items():
            headers.setdefault(key, value)
        headers.setdefault(
            "Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8"
        )
        auth_header = getattr(self, "_file_auth_header", "")
        if auth_header:
            headers["Authorization"] = auth_header
        elif not getattr(self, "_photo_auth_warned", False):
            self._photo_auth_warned = True
            self.log("未捕获接口 Basic 鉴权头，缺陷照片下载可能返回 401")
        user_agent = getattr(self, "_photo_user_agent", None)
        if user_agent is None:
            try:
                user_agent = str(self.page.evaluate("navigator.userAgent") or "")
            except Exception:
                user_agent = ""
            self._photo_user_agent = user_agent
        if user_agent:
            headers["User-Agent"] = user_agent
        try:
            response = self.page.context.request.get(
                url,
                headers=headers,
                timeout=min(self.timeout_ms, PHOTO_DOWNLOAD_TIMEOUT_MS),
            )
            status = getattr(response, "status", 0)
            content_range = str(
                (response.headers or {}).get("content-range") or ""
            ).strip()
            if status == 206 or content_range:
                # 分段响应的字节只是原图的一部分，存下来就是残缺照片。
                last_error = "响应是部分内容（HTTP %s，Content-Range=%r）" % (
                    status,
                    content_range,
                )
            elif response.ok:
                declared = _response_length(response)
                if declared > PHOTO_RESPONSE_MAX_BYTES:
                    last_error = "响应声明 %d 字节，超过上限 %d 字节" % (
                        declared,
                        PHOTO_RESPONSE_MAX_BYTES,
                    )
                else:
                    data = response.body()
                    if len(data or b"") > PHOTO_RESPONSE_MAX_BYTES:
                        last_error = "响应 %d 字节，超过上限 %d 字节" % (
                            len(data),
                            PHOTO_RESPONSE_MAX_BYTES,
                        )
                    else:
                        mime = str((response.headers or {}).get("content-type") or "")
                        mime = mime.split(";")[0].strip()
                        detected = _detect_image_mime(data)
                        if data and (detected or mime.startswith("image/")):
                            return data, detected or mime or _image_mime(data)
                        last_error = "响应不是图片（content-type=%r，%d 字节）" % (
                            mime,
                            len(data or b""),
                        )
            else:
                last_error = "HTTP 状态 %s" % status
        except Exception as exc:
            last_error = str(exc)[:200]
        value = None
        fetch_headers = {}
        for key, value in (getattr(self, "_file_request_headers", {}) or {}).items():
            fetch_headers.setdefault(key, value)
        fetch_headers.setdefault(
            "Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8"
        )
        if auth_header:
            fetch_headers["Authorization"] = auth_header
        try:
            value = self.page.evaluate(
                _PHOTO_FETCH_SCRIPT,
                {
                    "url": url,
                    "headers": fetch_headers,
                    "maxBytes": PHOTO_RESPONSE_MAX_BYTES,
                },
            )
        except Exception as exc:
            if not last_error:
                last_error = str(exc)[:200]
        if isinstance(value, dict) and value.get("error"):
            last_error = last_error or "页面 fetch：%s" % str(value.get("error"))[:120]
        elif isinstance(value, dict) and value.get("b64"):
            try:
                data = base64.b64decode(str(value.get("b64")))
            except Exception:
                data = b""
            mime = str(value.get("type") or "").split(";")[0].strip()
            detected = _detect_image_mime(data)
            if len(data) > PHOTO_RESPONSE_MAX_BYTES:
                last_error = last_error or "页面 fetch 返回 %d 字节，超过上限 %d 字节" % (
                    len(data),
                    PHOTO_RESPONSE_MAX_BYTES,
                )
            elif data and (detected or mime.startswith("image/")):
                return data, detected or mime or _image_mime(data)
            else:
                last_error = last_error or "页面 fetch 返回的不是图片"
        raise RuntimeError(last_error or "缺陷照片下载失败")

    def _write_photo_file(self, path: Path, data: bytes) -> Path:
        """原子写入照片：同名文件已存在时追加序号，绝不覆盖已有照片。"""
        path.parent.mkdir(parents=True, exist_ok=True)
        stem, suffix = os.path.splitext(str(path))
        temp_path = Path(
            "%s.%d.%d.tmp" % (path, os.getpid(), threading.get_ident())
        )
        try:
            # 先写同目录临时文件，落位是一次原子操作：
            # 中途崩溃/断电只留下 .tmp，不会留下半截照片冒充已保存。
            with temp_path.open("wb") as handle:
                handle.write(data)
                handle.flush()
                try:
                    os.fsync(handle.fileno())
                except OSError:
                    pass
            for index in range(1, 1000):
                candidate = (
                    path if index == 1 else Path("%s (%d)%s" % (stem, index, suffix))
                )
                try:
                    # 硬链接原子占位：目标已存在时直接失败，避免覆盖同名照片。
                    os.link(temp_path, candidate)
                    return candidate
                except FileExistsError:
                    continue
                except OSError:
                    # 不支持硬链接的文件系统：退回先查后替换，仍不覆盖已有照片。
                    if candidate.exists():
                        continue
                    os.replace(temp_path, candidate)
                    return candidate
            raise RuntimeError("同名照片过多，无法保存：%s" % path.name)
        finally:
            try:
                if temp_path.exists():
                    temp_path.unlink()
            except OSError:
                pass

    def _display_path(self, path: Path) -> str:
        try:
            return str(path.relative_to(self.outdir))
        except ValueError:
            return str(path)

    def _annotate_photo(self, data: bytes, mime: str, boxes: list):
        """在当前页用离屏 canvas 画缺陷框；返回 (字节, mime, 已画数, 丢弃数)。"""
        if self.page is None or self.page.is_closed():
            return None
        output_mime = _photo_output_mime(mime)
        data_url = "data:%s;base64,%s" % (
            mime or "image/jpeg",
            base64.b64encode(data).decode("ascii"),
        )
        try:
            value = self.page.evaluate(
                _PHOTO_ANNOTATE_SCRIPT,
                {"dataUrl": data_url, "boxes": boxes, "outputMime": output_mime},
            )
        except Exception as exc:
            self.log("缺陷照片标注失败：%s" % _redact_urls(exc)[:200])
            return None
        if not isinstance(value, dict) or not value.get("b64"):
            return None
        try:
            marked = base64.b64decode(str(value.get("b64")))
        except Exception:
            return None
        if not marked:
            return None
        try:
            drawn = int(value.get("drawn") or 0)
            skipped = int(value.get("skipped") or 0)
        except (TypeError, ValueError):
            drawn, skipped = 0, 0
        marked_mime = str(value.get("type") or output_mime)
        return marked, marked_mime, drawn, skipped

    _MARKED_PHOTO_SUFFIXES = frozenset(
        (".jpg", ".jpeg", ".jpe", ".png", ".webp", ".gif", ".bmp", ".tif", ".tiff")
    )

    def _count_photo_failure(self, kind: str) -> None:
        """照片失败分项计数，photo_failures 保留为兼容旧汇总口径的总数。"""
        self.photo_failures += 1
        self._task_photo_failures += 1
        if kind == "download":
            self.photo_download_failures += 1
            self._task_photo_download_failures += 1
        elif kind == "mark":
            self.photo_mark_failures += 1
        else:
            self.photo_save_failures += 1

    def _load_photo_index(self) -> dict:
        """加载输出目录的照片归属索引；坏文件按空索引处理，不阻断归档。"""
        if self._photo_index_loaded:
            return self._photo_index
        self._photo_index_loaded = True
        entries = {}
        try:
            raw = json.loads(self.photo_index_path.read_text(encoding="utf-8"))
            photos = raw.get("photos") if isinstance(raw, dict) else None
            if isinstance(photos, dict):
                for key, value in photos.items():
                    if not isinstance(value, dict):
                        continue
                    guid = _normalize_text(value.get("guid"))
                    if not guid:
                        continue
                    entries[str(key)] = {
                        "guid": guid,
                        "size": max(0, int(value.get("size") or 0)),
                        "sha256": str(value.get("sha256") or ""),
                        "marked": str(value.get("marked") or ""),
                        "marked_size": max(0, int(value.get("marked_size") or 0)),
                    }
        except (OSError, ValueError, TypeError):
            entries = {}
        self._photo_index = entries
        return entries

    def _save_photo_index(self) -> None:
        """原子写归属索引：先写同目录临时文件再整体替换。"""
        payload = {
            "version": PHOTO_INDEX_VERSION,
            "photos": self._photo_index,
        }
        temp_path = self.photo_index_path.with_name(
            "%s.%d.%d.tmp"
            % (self.photo_index_path.name, os.getpid(), threading.get_ident())
        )
        try:
            self.outdir.mkdir(parents=True, exist_ok=True)
            temp_path.write_text(
                json.dumps(payload, ensure_ascii=False, indent=1, sort_keys=True),
                encoding="utf-8",
            )
            os.replace(temp_path, self.photo_index_path)
            self._photo_index_dirty = False
        except Exception as exc:
            self.log("照片归属索引写入失败：%s" % str(exc)[:200])
        finally:
            try:
                if temp_path.exists():
                    temp_path.unlink()
            except OSError:
                pass

    @staticmethod
    def _directory_listing(directory: Path) -> dict:
        """一次 iterdir 建「文件名（小写）-> 路径」表，后续查重不再扫目录。"""
        listing = {}
        try:
            for path in directory.iterdir():
                try:
                    if not path.is_file() or path.stat().st_size <= 0:
                        continue
                except OSError:
                    continue
                listing.setdefault(path.name.casefold(), path)
        except OSError:
            return {}
        return listing

    def _listing_marked(self, listing: dict, original_name: str):
        """在同一份目录表里找该原图的标注图 `原图名_标注.扩展名`。"""
        prefix = (os.path.splitext(original_name)[0] + PHOTO_MARKED_SUFFIX).casefold()
        for folded, path in listing.items():
            if not folded.startswith(prefix):
                continue
            if folded[len(prefix) :] not in self._MARKED_PHOTO_SUFFIXES:
                continue
            return path
        return None

    def _photo_owned_by(
        self, path: Path, guid: str, image: ReviewImage, memo: dict
    ) -> bool:
        """判断已落盘文件是不是当前这张照片：索引校验一致，或缓存字节一致。"""
        key = _photo_index_key(path, self.outdir)
        try:
            size = path.stat().st_size
        except OSError:
            return False
        entry = self._photo_index.get(key)
        if entry is not None:
            if entry["guid"].casefold() != guid:
                return False
            if entry["size"] != size or not entry["sha256"]:
                return False
            return _file_sha256(path) == entry["sha256"]
        # 索引缺失（旧版本留下的照片）：只有当前刚取到的字节能证明是同一张
        # 照片时才复用；证明不了就另存一份，绝不把旧原图配新框。
        if "cached" not in memo:
            memo["cached"] = self._cached_photo_bytes(image)
        cached = memo["cached"]
        if not cached:
            return False
        data = cached[0]
        if len(data) != size:
            return False
        sha = hashlib.sha256(data).hexdigest()
        if _file_sha256(path) != sha:
            return False
        # 内容一致：补写归属，下次重跑不再依赖网络缓存。
        self._photo_index[key] = {
            "guid": guid,
            "size": size,
            "sha256": sha,
            "marked": "",
            "marked_size": 0,
        }
        self._photo_index_dirty = True
        return True

    def _photo_name_variants(self, image: ReviewImage) -> list:
        """原图缺失扩展名时，按实际图片类型补齐的扩展名也算同名照片。"""
        primary = _photo_file_name(image, "")
        names = [primary]
        original = _normalize_text(getattr(image, "original_name", ""))
        if not os.path.splitext(os.path.basename(original))[1]:
            stem = os.path.splitext(primary)[0]
            for suffix in (".jpg", ".png", ".webp", ".gif", ".bmp", ".tif"):
                names.append(stem + suffix)
        return names

    def _find_photo_slot(self, directory: Path, image: ReviewImage):
        """在目录表里找可复用槽位或第一个空闲文件名。

        返回 (已有原图, 该槽位的标注, 空闲文件名, 目录表)：
        - 已有原图能证明属于当前 fileGuid 时返回它，调用方只补缺的标注；
        - 否则依次看 `名称 (2)`、`名称 (3)`…，跳过被别的照片或残留标注占用的
          槽位，返回第一个既无原图也无残留标注的文件名用于下载。
        目录表只建一次，同一张照片的查重与标注查找复用同一份。
        """
        guid = _normalize_text(image.file_guid).casefold()
        listing = self._directory_listing(directory)
        memo = {}
        fallback = ""
        for name in self._photo_name_variants(image):
            stem, suffix = os.path.splitext(name)
            fallback = fallback or name
            for index in range(1, 1000):
                candidate = name if index == 1 else "%s (%d)%s" % (stem, index, suffix)
                original = listing.get(candidate.casefold())
                marked = self._listing_marked(listing, candidate)
                if original is not None:
                    if self._photo_owned_by(original, guid, image, memo):
                        return original, marked, candidate, listing
                    # 同名文件属于别的照片/无法验证：看下一个序号。
                    continue
                if marked is not None:
                    # 只剩标注残留：该槽位不可信，另存一份，不动残留文件。
                    continue
                return None, None, candidate, listing
        return None, None, fallback, listing

    def _save_marked_photo(
        self,
        directory: Path,
        original_path: Path,
        data: bytes,
        mime: str,
        boxes: list,
        label: str,
        listing: dict = None,
    ) -> None:
        """用原图字节画框并另存标注图；同名标注图已存在时不重复写。"""
        listing = listing if listing is not None else self._directory_listing(directory)
        existing = self._listing_marked(listing, original_path.name)
        key = _photo_index_key(original_path, self.outdir)
        if existing is not None:
            self.log(
                "缺陷照片标注图已存在，跳过重复标注：%s"
                % self._display_path(existing)
            )
            self._record_marked_owner(key, existing)
            return
        marked = self._annotate_photo(data, mime, boxes)
        if marked is None:
            self._count_photo_failure("mark")
            self.log("缺陷照片标注失败：%s" % label)
            return
        marked_data, marked_mime, drawn, skipped = marked
        if drawn <= 0:
            # 一个框都没画出来时不能当标注成功：否则汇总里的标注数会虚增。
            self._count_photo_failure("mark")
            self.log(
                "缺陷照片标注未画出任何缺陷框：%s（缺陷框 %d 个，丢弃 %d 个）"
                % (label, len(boxes), skipped)
            )
            return
        if skipped:
            self.log("缺陷照片标注丢弃越界缺陷框 %d 个：%s" % (skipped, label))
        # 标注名按原图实际落盘名派生：同名照片加了序号后仍能一一对应。
        marked_name = _annotated_photo_name(original_path.name, marked_mime)
        marked_path = self._write_photo_file(directory / marked_name, marked_data)
        self._record_marked_owner(key, marked_path)
        self.photos_marked += 1
        self.log(
            "缺陷照片标注图已保存：%s（画框 %d 个）"
            % (self._display_path(marked_path), drawn)
        )

    def _record_marked_owner(self, key: str, marked_path: Path) -> None:
        entry = self._photo_index.get(key)
        if entry is None:
            return
        try:
            size = marked_path.stat().st_size
        except OSError:
            size = 0
        if entry.get("marked") != marked_path.name or entry.get("marked_size") != size:
            entry["marked"] = marked_path.name
            entry["marked_size"] = size
            self._photo_index_dirty = True

    def _save_defect_photo(
        self, task_info: dict, image: ReviewImage, details: list
    ) -> None:
        """保存原图；有系统缺陷框时再保存一份同名标注图。

        先查输出目录：归属索引证明是同一张 fileGuid、且文件校验一致，才算
        已归档（两份齐全就不下载；只缺标注就读本地原图补标）。索引缺失又
        无法用当前缓存字节验证时宁可另存 ` (2)`，不把旧原图配新框。
        """
        if not self.save_photos or not image.file_guid:
            return
        self.check_cancel()
        label = image.original_name or image.picture_guid or image.file_guid
        boxes = _photo_boxes(details)
        self._load_photo_index()
        try:
            directory = self._photo_output_dir(task_info, image)
        except Exception as exc:
            self._count_photo_failure("save")
            self.log(
                "缺陷照片保存失败：%s（%s）" % (label, _redact_urls(exc)[:200])
            )
            return
        existing_original, existing_marked, free_name, listing = (
            self._find_photo_slot(directory, image)
        )
        local_data = None
        local_mime = ""
        if existing_original is not None:
            try:
                local_data = existing_original.read_bytes()
            except OSError:
                local_data = None
            local_mime = _detect_image_mime(local_data or b"")
            if not local_data or not local_mime:
                # 名字对但内容不是图片（半截/写坏）：按未落盘处理，另存一份。
                existing_original, existing_marked, local_data = None, None, None
                free_name = free_name or _photo_file_name(image, "")
        if existing_original is not None:
            if not boxes or existing_marked is not None:
                self.log(
                    "缺陷照片已归档，跳过重复保存：%s"
                    % self._display_path(existing_original)
                )
                if self._photo_index_dirty:
                    self._save_photo_index()
                return
            self.log(
                "缺陷照片原图已存在，只补标注：%s"
                % self._display_path(existing_original)
            )
            try:
                self._save_marked_photo(
                    directory,
                    existing_original,
                    local_data,
                    local_mime,
                    boxes,
                    label,
                    listing=listing,
                )
            except Exception as exc:
                self._count_photo_failure("save")
                self.log(
                    "缺陷照片保存失败：%s（%s）"
                    % (label, _redact_urls(exc)[:200])
                )
            finally:
                if self._photo_index_dirty:
                    self._save_photo_index()
            return
        data = None
        mime = ""
        try:
            data, mime = self._fetch_photo_bytes(image)
            self.log("缺陷照片原图下载成功：%s" % label)
        except Exception as exc:
            download_error = _redact_urls(exc)[:200]
            loaded = self._fetch_loaded_photo(image)
            if loaded is not None:
                loaded_data, loaded_mime, loaded_src, thumbnail = loaded
                if not thumbnail:
                    data, mime = loaded_data, loaded_mime
                    self.log(
                        "原图接口失败，改用页面已加载原图：%s（%s）"
                        % (label, download_error)
                    )
                else:
                    original_url = self._original_photo_url(loaded_src)
                    if original_url:
                        try:
                            data, mime = self._fetch_photo_url(original_url)
                            self.log("缩略图地址改写后取得原图：%s" % label)
                        except Exception as rewrite_exc:
                            download_error = _redact_urls(rewrite_exc)[:200]
            if data is None:
                self._count_photo_failure("download")
                if loaded is not None and loaded[3]:
                    self.log(
                        "缺陷照片只加载到缩略图，已拒绝保存：%s（原图接口：%s）"
                        % (label, download_error)
                    )
                else:
                    self.log(
                        "缺陷照片原图下载失败：%s（%s）"
                        % (label, download_error)
                    )
                return
        if data is None:
            return
        try:
            name = free_name or _photo_file_name(image, mime)
            original_path = self._write_photo_file(directory / name, data)
            key = _photo_index_key(original_path, self.outdir)
            self._photo_index[key] = {
                "guid": _normalize_text(image.file_guid).casefold(),
                "size": len(data),
                "sha256": hashlib.sha256(data).hexdigest(),
                "marked": "",
                "marked_size": 0,
            }
            self._photo_index_dirty = True
            self.photos_saved += 1
            self._task_photos_saved += 1
            self.log("缺陷照片原图已保存：%s" % self._display_path(original_path))
            if boxes:
                self._save_marked_photo(
                    directory,
                    original_path,
                    data,
                    mime,
                    boxes,
                    label,
                    listing=listing,
                )
        except Exception as exc:
            self._count_photo_failure("save")
            self.log(
                "缺陷照片保存失败：%s（%s）"
                % (label, _redact_urls(exc)[:200])
            )
        finally:
            if self._photo_index_dirty:
                self._save_photo_index()

    def _record_system_defect(
        self,
        task_info: dict,
        settings: ReviewSettings,
        image: ReviewImage,
        details: list,
    ) -> None:
        # 与恢复历史时的去重键保持一致（都按图片 GUID 归一化），
        # 否则同一张图在重跑时会被当成新缺陷再登记一次。
        key = _normalize_text(image.picture_guid)
        if key in self.system_defect_guids:
            # 汇总行去重了，但照片可能上次没下到/没标注成：归档不能跟着跳过，
            # 否则同目录重跑永远补不齐漏下载的照片。
            self._save_defect_photo(task_info, image, details)
            return
        self.system_defect_guids.add(key)
        summary = summarize_tagging_details(details)
        line_name, line_tower, tower_name = _summary_line_values(task_info, image)
        row = [
            settings.voltage_level,
            line_name,
            line_tower,
            tower_name,
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
        self._rows_since_summary_write += 1
        self._append_summary_jsonl("defect", row)
        self.log(
            "已登记系统缺陷框图片：%s（%s %s）"
            % (
                image.original_name or image.picture_guid,
                task_info.get("line_tower", ""),
                summary["description"],
            )
        )
        self._save_defect_photo(task_info, image, details)

    def _append_summary_jsonl(self, kind: str, row: list) -> None:
        """审核结果逐条追加，进程中断时也不丢已记录行。"""
        try:
            with self.summary_jsonl_path.open("a", encoding="utf-8") as handle:
                handle.write(
                    json.dumps({"type": kind, "row": row}, ensure_ascii=False)
                    + "\n"
                )
        except Exception as exc:
            if not self._summary_jsonl_error_logged:
                self._summary_jsonl_error_logged = True
                self.log("审核汇总 JSONL 写入失败：%s" % str(exc)[:200])

    def _write_summary_jsonl(self, records) -> None:
        """按给定记录原子重写 JSONL：修坏行、或撤销已撤回的异常行。"""
        temp_path = self.summary_jsonl_path.with_name(
            "%s.%d.%d.tmp"
            % (self.summary_jsonl_path.name, os.getpid(), threading.get_ident())
        )
        try:
            self.outdir.mkdir(parents=True, exist_ok=True)
            with temp_path.open("w", encoding="utf-8") as handle:
                for kind, row in records:
                    handle.write(
                        json.dumps({"type": kind, "row": row}, ensure_ascii=False)
                        + "\n"
                    )
            os.replace(temp_path, self.summary_jsonl_path)
            self._summary_jsonl_error_logged = False
        except Exception as exc:
            self.log("审核汇总 JSONL 重写失败：%s" % str(exc)[:200])
        finally:
            try:
                if temp_path.exists():
                    temp_path.unlink()
            except OSError:
                pass

    def _restore_summary_history(self) -> None:
        """恢复同一输出目录的历史审核汇总，重跑既不丢历史也不重复登记。

        以 JSONL 为准：它逐条追加，进程中断也不丢已完成记录；坏行/半截行跳过
        并告警，同时把文件重写成干净内容，避免后续追加挂在断行后面。JSONL 没有
        可恢复记录时才回退读旧「审核汇总.xlsx」，并把恢复出的行补写进 JSONL，
        下次直接从 JSONL 恢复。图片 GUID 与异常键一并恢复，重复图片/异常不再写入。
        """
        if not (self.summary_jsonl_path.is_file() or self.summary_xlsx_path.is_file()):
            return
        records = []
        damaged = 0
        jsonl_exists = self.summary_jsonl_path.is_file()
        jsonl_has_text = False
        if jsonl_exists:
            try:
                jsonl_has_text = self.summary_jsonl_path.stat().st_size > 0
            except OSError:
                jsonl_has_text = False
            records, damaged = _summary_jsonl_records(self.summary_jsonl_path)
        if damaged:
            self.log(
                "审核汇总 JSONL 有 %d 行损坏或半截，已跳过并重写干净文件：%s"
                % (damaged, self.summary_jsonl_path)
            )
            self._write_summary_jsonl(records)
        if not records and self.summary_xlsx_path.is_file():
            if jsonl_has_text and not damaged:
                # 文件非空却一条都解析不出来：多半是读不出来（占用/权限），
                # 不能拿 xlsx 或空表去覆盖它，先告警保留原文件。
                self.log(
                    "审核汇总 JSONL 无法解析，已跳过历史恢复，保留原文件：%s"
                    % self.summary_jsonl_path
                )
                return
            records = _summary_xlsx_records(self.summary_xlsx_path)
            if records:
                self.log(
                    "审核汇总 JSONL 无可用记录，已从旧汇总表恢复 %d 条：%s"
                    % (len(records), self.summary_xlsx_path)
                )
                self._write_summary_jsonl(records)
        restored_defects = 0
        restored_anomalies = 0
        for kind, row in records:
            values = ["" if value is None else value for value in row]
            if kind == "defect":
                key = _defect_row_key(values)
                if key in self.system_defect_guids:
                    continue
                self.system_defect_guids.add(key)
                self.system_defect_rows.append(values)
                restored_defects += 1
            else:
                key = _anomaly_row_key(values)
                if key in self.anomaly_keys:
                    continue
                self.anomaly_keys.add(key)
                self.anomaly_rows.append(values)
                restored_anomalies += 1
        if not (restored_defects or restored_anomalies):
            return
        # 历史行还没进本次的 xlsx（上次强行中断时 xlsx 落后于 JSONL），
        # 标脏让本次收尾时补写；内容相同的重写是幂等的。
        if restored_defects:
            self.system_defect_dirty = True
        if restored_anomalies:
            self.anomaly_dirty = True
        self.log(
            "已恢复输出目录历史审核汇总：缺陷 %d 条，异常 %d 条（%s）"
            % (
                len(self.system_defect_rows),
                len(self.anomaly_rows),
                self.summary_xlsx_path,
            )
        )

    def _write_summary_xlsx(self, force: bool = False) -> None:
        if not force and not self.system_defect_dirty and not self.anomaly_dirty:
            return
        if (
            not force
            and self._tasks_since_summary_write < SUMMARY_WRITE_INTERVAL_TASKS
            and self._rows_since_summary_write < SUMMARY_WRITE_ROW_THRESHOLD
        ):
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
            self._tasks_since_summary_write = 0
            self._rows_since_summary_write = 0
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
        # 未显式配置跳过表时，默认跳过表就是这张输出目录表：交给同一条写入
        # 路径去重，避免两张表同名不同步、同一任务被写两遍。
        if self.skip_tasks_file is not None and _same_path(
            self.skip_tasks_file, self.output_skip_path
        ):
            self._remember_skip_task_name(name)
            return
        folded = name.casefold()
        if not self._output_skip_loaded:
            self.output_skip_names |= load_skip_task_names(self.output_skip_path)
            self._output_skip_loaded = True
        if folded in self.output_skip_names:
            return
        self.output_skip_names.add(folded)
        try:
            self._init_output_skip_file()
            # 用户手工编辑可能留下无换行末行，直接追加会与任务名粘连
            prefix = "\n" if _needs_leading_newline(self.output_skip_path) else ""
            with self.output_skip_path.open("a", encoding="utf-8") as handle:
                handle.write(prefix + name + "\n")
            self.log("已写入输出目录跳过任务表：%s" % name)
        except Exception as exc:
            self.log("跳过任务表写入失败：%s" % str(exc)[:200])

    def _remember_skip_task_name(self, task_name: str) -> bool:
        """记下需跳过的任务名；xlsx 跳过表批量刷新，txt/csv 立即追加。"""
        name = _normalize_text(task_name)
        if not name or self.skip_tasks_file is None:
            return False
        if not self._skip_names_loaded:
            self.skip_task_names |= load_skip_task_names(self.skip_tasks_file)
            self._skip_names_loaded = True
        folded = name.casefold()
        if folded in self.skip_task_names:
            return False
        path = Path(self.skip_tasks_file)
        suffix = path.suffix.casefold()
        if suffix in (".xls", ".xlsm"):
            return False
        if suffix == ".xlsx":
            self.skip_task_names.add(folded)
            self._skip_table_pending.append(name)
            self._skip_table_flush_counter += 1
            if self._skip_table_flush_counter >= SKIP_TABLE_FLUSH_INTERVAL_NAMES:
                self._flush_skip_table()
            return True
        try:
            with _SKIP_TABLE_LOCK:
                path.parent.mkdir(parents=True, exist_ok=True)
                is_new = not path.is_file() or path.stat().st_size == 0
                prefix = (
                    "\n" if (not is_new and _needs_leading_newline(path)) else ""
                )
                if suffix == ".csv":
                    with path.open("a", encoding="utf-8", newline="") as handle:
                        if prefix:
                            handle.write(prefix)
                        writer = csv.writer(handle)
                        if is_new:
                            writer.writerow([SKIP_TASK_HEADER])
                        writer.writerow([name])
                else:
                    if is_new:
                        prefix = SKIP_TASK_HEADER + "\n"
                    with path.open("a", encoding="utf-8") as handle:
                        handle.write(prefix + name + "\n")
        except Exception:
            return False
        self.skip_task_names.add(folded)
        self.log("已写入任务名称跳过表：%s" % name)
        return True

    def _flush_skip_table(self, force: bool = False) -> None:
        """把待写入的 xlsx 跳过表名称合并落盘；同一批失败时保留待重试。"""
        if not self._skip_table_pending:
            return
        if (
            not force
            and self._skip_table_flush_counter < SKIP_TABLE_FLUSH_INTERVAL_NAMES
        ):
            return
        pending = list(self._skip_table_pending)
        try:
            with _SKIP_TABLE_LOCK:
                existing, _ok = _read_skip_table_names(
                    self.skip_tasks_file, strict=True
                )
                seen = {row.casefold() for row in existing}
                names = [name for name in pending if name.casefold() not in seen]
                if not names:
                    added = 0
                else:
                    added = _append_skip_names_xlsx(Path(self.skip_tasks_file), names)
        except Exception as exc:
            self._skip_table_flush_counter = 0
            if not self._skip_table_error_logged:
                self._skip_table_error_logged = True
                self.log("任务名称跳过表批量写入失败：%s" % str(exc)[:200])
            return
        pending_folded = {name.casefold() for name in pending}
        self._skip_table_pending = [
            name
            for name in self._skip_table_pending
            if name.casefold() not in pending_folded
        ]
        self._skip_table_flush_counter = 0
        self.log("任务名称跳过表已批量写入：%d 条" % added)

    def _flush_outputs(self) -> None:
        self._flush_skip_table(force=True)
        self._write_summary_xlsx(force=True)

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
        ] + list(_summary_line_values(task_info, image)) + [
            image.original_name if image is not None else "",
            image.shoot_time if image is not None else "",
            picture_guid,
            count,
            result,
        ]
        self.anomaly_rows.append(row)
        self.anomaly_dirty = True
        self._rows_since_summary_write += 1
        self._append_summary_jsonl("anomaly", row)
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

    def _thumbnail_active(
        self, dialog, file_guid: str, active_index: int = None
    ) -> bool:
        """当前高亮缩略图是否是目标图片；优先只读取高亮那一张。"""
        lowered_guid = file_guid.casefold()
        index = (
            self._active_thumbnail_index(dialog)
            if active_index is None
            else active_index
        )
        items = self._thumbnail_items(dialog) if index >= 0 else None
        if items is not None:
            try:
                guid = self._thumbnail_item_guid(items.nth(index))
                if guid:
                    return guid.casefold() == lowered_guid
            except Exception:
                pass
        # 页面没有 li 缩略图结构或高亮项读不到 fileGuid 时保留旧兜底。
        return self._thumbnail_active_scan(dialog, file_guid)

    def _thumbnail_active_scan(self, dialog, file_guid: str) -> bool:
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
        selectors = (
            ".manual-audit .img-list-wrap li",
            ".img-list-wrap li",
            "li",
        )
        if self._thumbnail_selector:
            try:
                items = dialog.locator(self._thumbnail_selector)
                if items.count():
                    return items
            except Exception:
                self._thumbnail_selector = ""
        for selector in selectors:
            try:
                items = dialog.locator(selector)
                if items.count():
                    # 只缓存带容器限定的选择器；通用 li 可能命中其他列表。
                    if selector != "li":
                        self._thumbnail_selector = selector
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

    def _image_lookup(self, images: Sequence[ReviewImage]) -> dict:
        """任务内 fileGuid → 图片的索引；同一列表只构建一次。"""
        if images is None:
            return {}
        if self._image_index_source is not images:
            self._image_index_source = images
            self._image_index = {
                image.file_guid.casefold(): image
                for image in images
                if image.file_guid
            }
        return self._image_index

    def _thumbnail_item_guid(self, item) -> str:
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
                if guid:
                    return guid
        return ""

    def _thumbnail_image(self, item, images: Sequence[ReviewImage]):
        guid = self._thumbnail_item_guid(item)
        if not guid:
            return None
        return self._image_lookup(images).get(guid.casefold())

    def _image_from_active_index(
        self, dialog, active_index: int, images: Sequence[ReviewImage]
    ):
        if active_index < 0:
            return None
        items = self._thumbnail_items(dialog)
        if items is None:
            return None
        try:
            return self._thumbnail_image(items.nth(active_index), images)
        except Exception:
            return None

    def _current_image_from_dialog(
        self,
        dialog,
        images: Sequence[ReviewImage],
        active_index: int = None,
    ):
        if active_index is None:
            start = max(0, self._last_active_thumbnail_index)
            active_index = self._active_thumbnail_index(dialog, start)
            # 系统可能绕回第一张未审核图片，从上次下标扫不到时从头再找一次。
            if active_index < 0 and start > 0:
                active_index = self._active_thumbnail_index(dialog)
        active_image = self._image_from_active_index(dialog, active_index, images)
        if active_image is not None:
            self._last_active_thumbnail_index = active_index
            return active_image
        try:
            text = _normalize_text(dialog.inner_text())
        except Exception:
            text = ""
        named = [
            image
            for image in images
            if image.original_name and image.original_name in text
        ]
        # 弹窗文本同时出现多张图片名时不能取第一张冒充当前图；宁可留给
        # 后续身份校验/保守失败，也不按文件名猜。
        if len(named) == 1:
            return named[0]
        if len(named) > 1:
            self.log(
                "弹窗文本同时匹配 %d 张图片名，无法唯一确认当前图，不按文件名判定"
                % len(named)
            )
        lookup = self._image_lookup(images)
        for _image, source in reversed(self._visible_main_images(dialog)):
            try:
                params = parse_qs(urlsplit(source).query)
            except ValueError:
                continue
            guid = _normalize_text(params.get("fileGuid", [""])[0])
            if not guid:
                continue
            image = lookup.get(guid.casefold())
            if image is not None:
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

    def _dom_click(self, target, force: bool = False) -> None:
        """直接触发 DOM click，不移动鼠标、不滚动页面；必要时允许 force 兜底。"""
        try:
            target.evaluate("(el) => el.click()")
        except Exception:
            target.click(force=force)

    def _dispatch_mouse_click(self, target) -> None:
        """不依赖窗口坐标/缩放，向元素派发完整鼠标事件序列。"""
        target.evaluate(
            """
            (el) => {
              const opts = {
                bubbles: true, cancelable: true, composed: true,
                view: window, button: 0, buttons: 1
              };
              el.dispatchEvent(new MouseEvent('mousedown', opts));
              el.dispatchEvent(new MouseEvent('mouseup', opts));
              el.dispatchEvent(new MouseEvent('click', opts));
            }
            """
        )

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

    def _active_thumbnail_index(self, dialog, start_index: int = 0) -> int:
        start = max(0, int(start_index))
        try:
            result = dialog.evaluate(_ACTIVE_THUMBNAIL_SCRIPT, start)
        except Exception:
            result = None
        # 测试桩或非标准页面不支持该脚本时按原逻辑逐项兜底。
        if isinstance(result, int) and not isinstance(result, bool):
            return result
        return self._active_thumbnail_index_fallback(dialog, start)

    def _active_thumbnail_index_fallback(
        self, dialog, start_index: int = 0
    ) -> int:
        items = self._thumbnail_items(dialog)
        if items is None:
            return -1
        for index in range(max(0, int(start_index)), items.count()):
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
        retry_images=None,
    ):
        items = self._thumbnail_items(dialog)
        if items is None:
            return None, 0
        if retry_images is not None or attempted_guids:
            # 队列分支优先于旧的 start_index/高亮/文本逻辑：首轮一旦有已尝试的
            # 图片、或进入补审，就统一按「完整未审核队列」配对选图。
            return self._select_queued_image(
                dialog, items, images, attempted_guids, allowed_guids, retry_images
            )
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
            if self._active_thumbnail_index(dialog, index) == index:
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

    def _pending_unreviewed_items(self, items, images: Sequence[ReviewImage]) -> list:
        """完整未审核队列：每张未审核图片与它自己的缩略图配对。

        既定前提：DOM 是「仅未审核」列表，顺序与接口未审核顺序一致。接口侧
        按顺序取还没审核的图片（is_unreviewed_image 且本轮未审核成功），
        DOM 侧按顺序取可见且无对勾的缩略图，两边按相同顺序直接 zip 配对。
        点击时就用配对里的缩略图，不再用接口原始 index、队列下标或
        visible_items[position] 换算位置；DOM 前面多出的隐藏占位项不在
        可见序列里，配对不会错位。

        可见项比接口未审核项少：说明有图的 DOM 槽位仍带隐藏/已打勾标记（页面
        未刷新，或本轮审核成功后未刷新）。这时按槽位对齐——用接口侧全部
        未审核图（含本轮已审核成功的图，它们在 DOM 里只是被打勾）依次对上
        同槽位缩略图，槽位不可用的图直接丢弃，本轮已审核成功的图不进队列。
        """
        unreviewed = [
            image
            for image in images or ()
            if is_unreviewed_image(image)
            and image.picture_guid not in self.reviewed_guids
        ]
        if not unreviewed:
            return []
        visible = self._visible_thumbnail_items(items)
        if len(visible) >= len(unreviewed):
            return list(zip(unreviewed, visible))
        count = items.count()
        pairs = []
        slot = 0
        for image in images or ():
            if not is_unreviewed_image(image):
                continue
            if slot >= count:
                break
            item = items.nth(slot)
            slot += 1
            if image.picture_guid in self.reviewed_guids:
                continue
            try:
                if not item.is_visible():
                    continue
            except Exception:
                continue
            if self._thumbnail_has_checkmark(item):
                continue
            pairs.append((image, item))
        return pairs

    def _visible_thumbnail_items(self, items) -> list:
        """可见且没有对勾的缩略图项；队列分支固定过滤对勾。"""
        visible = []
        for index in range(items.count()):
            item = items.nth(index)
            try:
                if not item.is_visible():
                    continue
            except Exception:
                continue
            if self._thumbnail_has_checkmark(item):
                continue
            visible.append(item)
        return visible

    def _select_queued_image(
        self,
        dialog,
        items,
        images: Sequence[ReviewImage],
        attempted_guids: set,
        allowed_guids,
        retry_images,
    ):
        """首轮与补审共用的队列选图：目标与缩略图由队列配对直接决定。

        队列是「接口未审核图片 ↔ 该图自己的缩略图」的有序配对（见
        _pending_unreviewed_items）。目标选定后直接点配对里的缩略图，
        不再换算接口原始下标、队列下标或可见项位置；目标没有可点击的
        配对就返回 None，绝不改点别的图。
        """
        pairs = self._pending_unreviewed_items(items, images)
        if not pairs:
            return None, 0
        target = None
        target_item = None
        position = -1
        if retry_images is not None:
            # 补审：按失败原始顺序取第一个仍在 allowed_guids 且仍在队列的图。
            paired = {}
            for index, (image, item) in enumerate(pairs):
                paired.setdefault(image.picture_guid, (index, image, item))
            for candidate in retry_images:
                if candidate.picture_guid in self.reviewed_guids:
                    continue
                pair = paired.get(candidate.picture_guid)
                if pair is None:
                    continue
                if allowed_guids is not None and (
                    candidate.picture_guid not in allowed_guids
                ):
                    continue
                position, target, target_item = pair
                break
        else:
            # 首轮：第一个未 attempted、且非系统缺陷框的未审核图。
            for index, (candidate, item) in enumerate(pairs):
                if candidate.picture_guid in attempted_guids:
                    continue
                if not self.ignore_defect_boxes and is_system_defect_image(candidate):
                    continue
                position, target, target_item = index, candidate, item
                break
        if target is None or target_item is None:
            return None, 0
        mark = len(self.monitor.responses) if self.monitor else 0
        self.log(
            "%s第 %d 张缩略图：%s"
            % (
                "补审点击" if retry_images is not None else "按未审核队列点击",
                position + 1,
                target.original_name or target.picture_guid,
            )
        )
        try:
            self._click_thumbnail_box(dialog, target_item)
        except ReviewStopped:
            raise
        except Exception as exc:
            if self.stop_requested:
                raise
            raise ReviewStopped(
                "点击图片 %s 的缩略图失败：%s"
                % (target.picture_guid, str(exc)[:200])
            ) from exc
        return target, mark

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
        exc = ReviewStopped(
            "无法通过 fileGuid 定位图片 %s 的缩略图，不能点击其他图片"
            % image.picture_guid
        )
        # 标记「只是没定位到」，供 _click_current_thumbnail 决定是否可以
        # 退回点击当前高亮缩略图；其他点击错误不带这个标记，照旧上抛。
        exc.shade_thumbnail_missing = True
        raise exc

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
        if original_name not in text:
            return False
        # 文本里同时出现多张不同图片名时不能断言当前图就是这一张，
        # 否则多张图会被同时判成 selected。按去重后的名称集合判断，
        # 同一任务里两张同名图不算「多身份」。
        if not self.task_images:
            return True
        names = {
            image.original_name
            for image in self.task_images
            if image.original_name and image.original_name in text
        }
        return len(names) <= 1

    def _image_selected(
        self, dialog, image: ReviewImage, active_index: int = None
    ) -> bool:
        return self._thumbnail_active(
            dialog, image.file_guid, active_index
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
        try:
            self._click_thumbnail(dialog, image, "重新加载")
            return
        except ReviewStopped as exc:
            if self.stop_requested or not getattr(
                exc, "shade_thumbnail_missing", False
            ):
                raise
        # 缩略图没有 fileGuid 时直接点当前选中的缩略图：它就是刚选图/加载失败
        # 的同一项，不再要求文本或主图身份。
        item = self._selected_thumbnail_item(dialog)
        if item is None:
            raise ReviewStopped(
                "无法定位图片 %s 的缩略图，也没有可用的当前缩略图，不点击其他图片"
                % image.picture_guid
            )
        try:
            self._click_thumbnail_box(dialog, item)
        except ReviewStopped:
            raise
        except Exception as exc:
            raise ReviewStopped(
                "点击图片 %s 的当前缩略图失败：%s"
                % (image.picture_guid, str(exc)[:200])
            ) from exc
        self.log("已点击图片 %s 的当前缩略图（无 fileGuid）" % image.picture_guid)

    def _selected_thumbnail_item(self, dialog):
        """返回当前选中的缩略图项；没有可用项时返回 None。

        fileGuid 缺失的 blob 缩略图只能靠当前位置认定：优先取高亮项，
        没有再取上次选图记下的下标，都不成立就放弃。
        """
        items = self._thumbnail_items(dialog)
        if items is None:
            return None
        index = self._active_thumbnail_index(dialog)
        if index < 0:
            index = self._last_active_thumbnail_index
        if index < 0 or index >= items.count():
            return None
        try:
            item = items.nth(index)
            if not item.is_visible():
                return None
        except Exception:
            return None
        return item

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
                # 这张图是页面本轮真实加载成功的那一张：记下身份，
                # 之后 blob:/data: 回退才敢认领它的字节。
                self._mark_loaded_photo(image)
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
                    self._mark_loaded_photo(image)
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
            self.save_diagnostic(self.page, "button_not_ready", error=exc)
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
            self.save_diagnostic(self.page, "image_failure", error=exc)
            failed_images.append(image)
            return False
        details = (
            []
            if self.ignore_defect_boxes
            else self._collect_tagging_details(
                image.picture_guid,
                task_mark,
                timeout_ms=TAGGING_DETAIL_UI_WAIT_MS,
                # 审核前的短探针：这 100ms 内尚未返回按「暂无」继续审核，
                # 但接口已经明确失败时仍要报错，不能当成没有缺陷框。
                allow_missing=True,
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
            self.save_diagnostic(self.page, "image_failure", error=exc)
            failed_images.append(image)
            return False
        return True

    def _record_image_health(self, elapsed: float) -> None:
        """按“上一张成功到本张成功”的墙钟时间判断实例是否持续变慢。"""
        if self._last_image_success_at is not None:
            gap = max(0.0, float(elapsed))
            if gap >= self.slow_image_seconds:
                self._slow_image_streak += 1
                self.log(
                    "健康检测：单图耗时 %.1f 秒（阈值 %.1f 秒），连续 %d/%d 次"
                    % (
                        gap,
                        self.slow_image_seconds,
                        self._slow_image_streak,
                        self.slow_image_count,
                    )
                )
            else:
                self._slow_image_streak = 0
            if (
                self._slow_image_streak >= self.slow_image_count
                and not self.health_unhealthy
            ):
                self.health_unhealthy = True
                self.log(
                    "健康检测：连续 %d 张图片明显变慢，判定该实例不健康"
                    % self._slow_image_streak
                )
                if (
                    self.slow_restart_unreviewed > 0
                    and self.current_unreviewed_count
                    > self.slow_restart_unreviewed
                    and (
                        not self._last_health_restart_at
                        or time.monotonic() - self._last_health_restart_at
                        >= _HEALTH_RESTART_COOLDOWN
                    )
                ):
                    self.health_unhealthy = False
                    self.force_browser_restart = True
                    self.connection_lost = True
                    self._last_health_restart_at = time.monotonic()
                    self.log(
                        "健康检测：当前任务未审核 %d 张，超过立即重启阈值 %d 张；"
                        "立即重启该实例浏览器并从任务列表重入"
                        % (
                            self.current_unreviewed_count,
                            self.slow_restart_unreviewed,
                        )
                    )
                    raise BrowserUnhealthy(
                        "浏览器过慢且任务未审核数超过阈值，重启当前实例"
                    )
        self._last_image_success_at = time.monotonic()

    def _mark_reviewed(self, picture_guid: object) -> bool:
        guid = _normalize_text(picture_guid)
        if not guid or guid in self.reviewed_guids:
            return False
        self.reviewed_guids.add(guid)
        self.reviewed_count += 1
        # 张次口径：同一任务刷新/健康重试后再次审核成功也要累计。
        self.task_reviewed_total += 1
        now = time.monotonic()
        if self._last_image_success_at is not None:
            self._record_image_health(now - self._last_image_success_at)
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
        start = max(0, self._last_active_thumbnail_index)
        active_index = self._active_thumbnail_index(dialog, start)
        if active_index < 0 and start > 0:
            active_index = self._active_thumbnail_index(dialog)
        current = None
        if not self._image_selected(dialog, review_image, active_index):
            current = (
                self._current_image_from_dialog(
                    dialog, self.task_images, active_index
                )
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
            self._dom_click(button, force=True)
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
        # 照片失败计数按「同一任务跨重试」累计：这里不清零，
        # 由 run 层在进入任务前清零，finally 与 run 才能看到同一个值。
        monitor = self._ensure_monitor()
        monitor.reset()
        self._last_image_success_at = time.monotonic()
        self._slow_image_streak = 0
        self.current_unreviewed_count = 0
        self.current_task_name = task_name
        task_mark = 0
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
            self.current_unreviewed_count = int(
                task_info.get("unreviewed_count") or 0
            )
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
                    self.save_diagnostic(self.page, "close_failure", error=close_exc)
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
                # 明细读不到（标识未知）时不能拿空明细占一条「缺陷」行：
                # 只补存原图，缺陷内容留给人工核对。
                for image in system_defect_images:
                    self._save_defect_photo(task_info, image, [])
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
                    self.save_diagnostic(self.page, "close_failure", error=close_exc)
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
                    # 选图统一走「完整未审核队列」配对（见 _select_queued_image）：
                    # attempted_guids 非空时由队列分支决定目标与要点的缩略图，这里
                    # 不再传失败前缀长度或高亮下标，避免两套索引空间打架。
                    image, search_mark = self._select_unreviewed_image(
                        dialog,
                        images,
                        attempted_guids,
                        check_marks=not filtered,
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
                        self.check_cancel()
                        image, search_mark = self._select_unreviewed_image(
                            dialog,
                            images,
                            set(),
                            check_marks=not filtered,
                            allowed_guids=set(allowed),
                            retry_images=failed_images,
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
                        # 补审用的可能是没有 fileGuid 的 blob 缩略图：即使本次
                        # 审核成功，也要确认成功的正是目标图，否则按目标补审失败。
                        if image.picture_guid not in self.reviewed_guids:
                            reason = "补审实际审核的不是目标图片"
                            self._record_image_failure(
                                task_info,
                                settings,
                                image,
                                reason,
                            )
                            if image not in final_failed:
                                final_failed.append(image)
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
                try:
                    details = self._collect_tagging_details(
                        image.picture_guid, task_mark
                    )
                except TaggingDetailError as exc:
                    # 缺陷框已由 picDefectFlg 确认，明细只补描述与坐标；读不到时
                    # 不能反过来当成「没有缺陷框」继续审核，也不能因此丢掉照片：
                    # 照常归档并登记一行，缺陷描述留空交人工核对。
                    self.log(
                        "图片 %s 的系统缺陷框明细读取失败，缺陷描述留空待人工核对：%s"
                        % (image.picture_guid, _redact_urls(exc)[:200])
                    )
                    self._record_system_defect(task_info, settings, image, [])
                    continue
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
                    self.save_diagnostic(self.page, "close_failure", error=close_exc)
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
                    self.save_diagnostic(self.page, "close_failure", error=close_exc)
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
            self.save_diagnostic(self.page, "image_failure", error=exc)
            try:
                self.close_task(self.current_dialog)
            except ReviewStopped as close_exc:
                self.save_diagnostic(self.page, "close_failure", error=close_exc)
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
        except BrowserUnhealthy as exc:
            # 不是任务失败：浏览器健康重启后应从列表重入当前任务。
            self.log("健康重启中止当前任务处理：%s" % _redact_urls(exc)[:200])
            raise
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
            self.save_diagnostic(self.page, "review_stop", error=exc)
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
            self.save_diagnostic(self.page, "unexpected", error=exc)
            raise UnknownTaskError(reason) from exc
        finally:
            if failed_images or button_not_ready_images:
                if self._task_photo_failures:
                    # 照片没补齐的任务写进跳过表就再也不会重进，漏下的照片
                    # 永远补不上；最终是否写入由 run 层按同一累计值决定。
                    self.log(
                        "任务《%s》本次照片下载/标注失败 %d 张，任务内累计未清零，"
                        "暂不写入跳过任务表"
                        % (task_name, self._task_photo_failures)
                    )
                else:
                    self._remember_skip_task_name(task_name)
                    if task_name.casefold() not in self.skip_task_names:
                        self.log(
                            "警告：任务《%s》未能写入任务名称跳过表，"
                            "请检查文件是否被占用或不可写" % task_name
                        )
            self._tasks_since_summary_write += 1
            self._write_summary_xlsx()

    @staticmethod
    def _is_connection_error(exc) -> bool:
        text = str(exc or "").casefold()
        return any(
            marker in text
            for marker in (
                "connection closed",
                "connection terminated",
                "target page, context or browser has been closed",
                "target closed",
                "browser has been closed",
                "browser closed",
                "driver closed",
                "page crashed",
                "page closed",
                "crashed",
                "out of memory",
                "页面已关闭",
                "页面已崩溃",
            )
        )

    def _mark_connection_lost(self, exc) -> bool:
        if not self._is_connection_error(exc):
            return False
        if not self.connection_lost:
            self.log("浏览器连接已断开：跳过失败截图，准备重建连接并刷新")
        self.connection_lost = True
        return True

    def _reconnect(self) -> None:
        self.log("重建 CDP 连接（复用 Playwright 实例）")
        # 先记住当前地址：重启分支会在页面引用置空后使用它恢复。
        self._remember_url()
        # 只丢弃浏览器/页面连接，保留 Playwright 实例；否则同一线程重新 start()
        # 会被 Playwright 判定为“在 asyncio loop 内使用同步 API”。
        self.browser = None
        self.page = None
        self.monitor = None
        self.current_dialog = None
        if self.page_crashed or self.force_browser_restart:
            self.log(
                "页面此前已崩溃/实例不健康，直接重启受控浏览器"
            )
            self.force_browser_restart = False
            self._restart_browser()
            return
        try:
            self.connect(launched=False)
            self.connection_lost = False
        except Exception as exc:
            self.log("仅重建 CDP 连接失败：%s；重启受控浏览器" % _redact_urls(exc)[:200])
            self._restart_browser()

    def _restart_browser(self) -> None:
        """浏览器进程无响应/OOM/页面崩溃时重启；重启后自动登录并回任务列表。"""
        if self._recovering:
            return
        self._recovering = True
        self.recovery_mode = True
        try:
            self.log("开始重启受控浏览器")
            self._remember_url()
            # 重启只打开用户配置的根地址，不带任何路径：带路径直开是 404 的根源。
            configured = validate_start_url(
                getattr(self.args, "start_url", "") or self.last_known_url
            )
            origin = _safe_origin(configured)
            resume_url = (origin + "/") if origin else configured
            self.log(
                "准备重启受控浏览器，打开配置根地址：%s"
                % (resume_url or "<未配置>")
            )
            close_browser_for_restart(self.args.cdp_url, self.log)
            deadline = time.monotonic() + 15
            while cdp_reachable(self.args.cdp_url) and time.monotonic() < deadline:
                self._sleep_without_page(0.5)
            if cdp_reachable(self.args.cdp_url):
                port = _cdp_port(self.args.cdp_url)
                if port:
                    _terminate_browser_pids(_browser_pids_for_port(port), self.log)
                self._sleep_without_page(1)

            old_start = getattr(self.args, "start_url", "")
            self.args.start_url = resume_url or old_start
            try:
                while True:
                    self.check_cancel()
                    try:
                        if not cdp_reachable(self.args.cdp_url):
                            launch_browser(
                                self.args,
                                _cdp_port(self.args.cdp_url) or 9222,
                                self.log,
                            )
                        self.connect(launched=True)
                        break
                    except Exception as exc:
                        if self.stop_requested:
                            raise
                        self.log(
                            "重启受控浏览器失败：%s；10 秒后重试"
                            % _redact_urls(exc)[:200]
                        )
                        self._sleep_without_page(10)
            finally:
                self.args.start_url = old_start
            self.connection_lost = False
            self.page_crashed = False
            self._crash_restart_requested_at = 0.0
            self.log("受控浏览器已重启")
            self.ensure_authenticated()
            # 重启后的页面不保证停在任务列表；这里主动走菜单，避免只登录不导航。
            self.open_task_list()
            self._tasks_since_refresh = 0
            self._images_since_refresh = 0
        finally:
            self.recovery_mode = False
            self._recovering = False

    @staticmethod
    def _stop_playwright_safely(playwright) -> None:
        try:
            playwright.stop()
        except Exception:
            pass

    def _wait_page_settled(self, timeout_ms: float = 5000.0) -> bool:
        """确认页面没有挂起的整页导航；挂起时后续 click/fill 会被 5 秒超时拖死。"""
        if self.page is None or self.page.is_closed():
            return False
        wait = getattr(self.page, "wait_for_load_state", None)
        if wait is None:
            # 测试桩/非 Playwright 页面没有该能力，按已稳定处理。
            return True
        try:
            wait(
                "domcontentloaded",
                timeout=max(
                    500,
                    min(self.navigation_timeout_ms, int(timeout_ms)),
                ),
            )
            return True
        except Exception as exc:
            # 导航等待超时但任务列表已经可见时，按已加载继续；避免“页面好了
            # 脚本还认为没好”导致无谓刷新/重启。
            if self._task_list_visible():
                self.log("页面导航等待超时，但任务列表已可见，按已加载继续")
                return True
            self.log("页面导航未完成：%s" % _redact_urls(exc)[:200])
            return False

    def _refresh_after_task(
        self, settings: ReviewSettings, task_name: str = "", task_images: int = 0
    ) -> None:
        """按任务/图片计数刷新页面；未到阈值只记录指标，不重载页面。"""
        if self.stop_requested:
            return
        if self.monitor is not None:
            self.monitor.reset()
        self._tasks_since_refresh += 1
        self._images_since_refresh += max(0, int(task_images or 0))
        unhealthy = self.health_unhealthy
        self.health_unhealthy = False
        task_threshold = self.reload_after_tasks
        image_threshold = self.reload_after_images
        reached = (
            (task_threshold > 0 and self._tasks_since_refresh >= task_threshold)
            or (
                image_threshold > 0
                and self._images_since_refresh >= image_threshold
            )
        )
        if not unhealthy and not reached:
            self.log(
                "任务后监控：累计 %d 个任务/%d 张图片，未到刷新阈值"
                "（%d 任务/%d 图片），暂不刷新"
                % (
                    self._tasks_since_refresh,
                    self._images_since_refresh,
                    task_threshold,
                    image_threshold,
                )
            )
            self._slow_image_streak = 0
            return
        actual_tasks = self._tasks_since_refresh
        actual_images = self._images_since_refresh
        self._tasks_since_refresh = 0
        self._images_since_refresh = 0
        self._slow_image_streak = 0
        if unhealthy:
            if self._last_health_restart_at and (
                time.monotonic() - self._last_health_restart_at
                < _HEALTH_RESTART_COOLDOWN
            ):
                self.log(
                    "健康检测：该实例持续变慢，但 %.0f 秒内已重启过；"
                    "本次只刷新页面，避免反复重启"
                    % _HEALTH_RESTART_COOLDOWN
                )
            else:
                self.force_browser_restart = True
                self.connection_lost = True
                self._last_health_restart_at = time.monotonic()
                self.log(
                    "健康检测：任务《%s》结束后重启该实例浏览器，"
                    "自动登录后继续" % (task_name or "当前任务")
                )
        self.log(
            "任务《%s》结束，达到刷新阈值（%d/%d 任务，%d/%d 图片），"
            "刷新页面释放内存后继续"
            % (
                task_name or "当前任务",
                actual_tasks,
                task_threshold,
                actual_images,
                image_threshold,
            )
        )
        self._refresh_and_resume(settings, "任务完成后刷新")

    def _refresh_and_resume(
        self, settings: ReviewSettings, prefix: str = "异常后"
    ) -> bool:
        """刷新网页回任务列表并继续；连续失败时退避并重启浏览器，避免死循环。"""
        self.current_dialog = None
        attempt = 0
        failures = 0
        restarts = 0
        diagnostic_saved = False
        while True:
            self.check_cancel()
            attempt += 1
            if self.connection_lost:
                try:
                    self._reconnect()
                except Exception as exc:
                    if self.stop_requested:
                        raise
                    self.connection_lost = True
                    self.log(
                        "重建浏览器连接失败：%s；%d 秒后重试"
                        % (_redact_urls(exc)[:300], _REFRESH_RETRY_SECONDS)
                    )
                    self._sleep_without_page(_REFRESH_RETRY_SECONDS)
                    continue
            try:
                if self._page_recovered:
                    self._page_recovered = False
                    self.log(
                        "%s：检测到页面已恢复，跳过刷新（第 %d 次）"
                        % (prefix, attempt)
                    )
                else:
                    self.log("%s：刷新网页（第 %d 次）" % (prefix, attempt))
                    self.page.reload(wait_until="domcontentloaded")
                if not self._wait_page_settled():
                    raise RuntimeError("页面导航未完成")
                self.open_task_list()
                if not self._wait_page_settled():
                    raise RuntimeError("页面导航未完成")
                self.apply_filters(settings)
                self._tasks_since_refresh = 0
                self._images_since_refresh = 0
                self.log("已刷新网页回到任务列表，继续")
                return True
            except Exception as exc:
                if self.stop_requested:
                    raise
                if self._mark_connection_lost(exc):
                    self.log("刷新时浏览器连接已断开，立即重建连接")
                    continue
                failures += 1
                reason = _redact_urls(exc)
                if not diagnostic_saved:
                    diagnostic_saved = True
                    self.save_diagnostic(self.page, "refresh_failure", error=exc)
                if failures >= 3:
                    if restarts >= 2:
                        raise RuntimeError(
                            "连续 %d 次刷新/筛选失败，已重启浏览器 %d 次，"
                            "停止该实例：%s"
                            % (failures, restarts, reason[:300])
                        ) from exc
                    restarts += 1
                    failures = 0
                    self.log(
                        "连续刷新/筛选失败，重启当前实例浏览器"
                        "（第 %d 次）：%s"
                        % (restarts, reason[:300])
                    )
                    self.force_browser_restart = True
                    self.connection_lost = True
                    continue
                delay = min(5, failures + 1)
                self.log(
                    "刷新网页失败：%s；%d 秒后重试（连续 %d 次）"
                    % (reason[:300], delay, failures)
                )
                recovered = False
                try:
                    recovered = self._wait_for_page_recovery(
                        _REFRESH_RETRY_SECONDS
                    )
                except Exception:
                    if self.stop_requested:
                        raise
                if recovered:
                    self._page_recovered = True
                    self.log("检测到页面已恢复（登录页/任务列表），稍后继续")
                    self._wait_for_timeout(self.page, delay * 1000)
                    continue
                self._sleep_without_page(delay)

    def _wait_for_page_recovery(self, seconds: float) -> bool:
        """刷新失败等待期间轮询页面；用户手动回到登录页/任务页时提前继续。"""
        deadline = time.monotonic() + max(0.0, float(seconds))
        while time.monotonic() < deadline:
            self.check_cancel()
            try:
                if self._is_404_page():
                    self._handle_404_if_visible()
                    self.last_activity = time.monotonic()
                    return True
                if (
                    self._task_list_visible()
                    or self._login_page_visible()
                    or self._session_ready()
                ):
                    self.last_activity = time.monotonic()
                    return True
            except Exception:
                pass
            self._wait_for_timeout(
                self.page,
                int(
                    min(
                        500.0,
                        max(0.0, (deadline - time.monotonic()) * 1000.0),
                    )
                ),
            )
        return False

    def _start_watchdog(self) -> None:
        if self.stall_timeout_ms <= 0 or self._watchdog_thread is not None:
            return
        stop_event = threading.Event()
        self._watchdog_stop = stop_event
        # 只持弱引用：任务对象被释放后看门狗自行退出，避免测试/异常路径
        # 遗留的 runner 连同整页假对象被线程钉住（Linux 容器内会累积到 OOM）。
        runner_ref = weakref.ref(self)

        def loop():
            while not stop_event.wait(2.0):
                runner = runner_ref()
                if runner is None:
                    return
                try:
                    if runner.stop_requested or runner._recovering:
                        continue
                    if runner.page_crashed:
                        now = time.monotonic()
                        if (
                            now - runner._crash_restart_requested_at
                            >= _CRASH_RESTART_RETRY_SECONDS
                        ):
                            runner._crash_restart_requested_at = now
                            runner.last_activity = now
                            runner.log(
                                "看门狗：页面崩溃后主线程未恢复，关闭受控浏览器"
                                "以强制进入自动重启"
                            )
                            try:
                                close_browser_for_restart(
                                    runner.args.cdp_url, runner.log
                                )
                            except Exception:
                                pass
                        continue
                    idle = time.monotonic() - runner.last_activity
                    if idle * 1000.0 < runner.stall_timeout_ms:
                        continue
                    if runner.connection_lost:
                        continue
                    runner.log(
                        "看门狗：已 %.0f 秒无任务活动，判定页面/CDP 卡死；"
                        "关闭受控浏览器以触发自动恢复" % idle
                    )
                    runner.connection_lost = True
                    runner.last_activity = time.monotonic()
                    try:
                        close_browser_for_restart(runner.args.cdp_url, runner.log)
                    except Exception:
                        pass
                except Exception:
                    # 看门狗自身异常不能让线程退出；下一轮继续守护。
                    pass
                finally:
                    # 线程局部变量不能跨 wait 持有 runner：否则弱引用永远不失效，
                    # 任务对象连同整页页面树都无法回收。
                    runner = None

        self._watchdog_thread = threading.Thread(
            target=loop, name="defect-review-watchdog", daemon=True
        )
        self._watchdog_thread.start()

    def _stop_watchdog(self) -> None:
        if self._watchdog_stop is not None:
            self._watchdog_stop.set()
            self._watchdog_stop = None
        self._watchdog_thread = None

    def _sleep_without_page(self, seconds: float) -> None:
        deadline = time.monotonic() + max(0.0, float(seconds))
        while time.monotonic() < deadline:
            self.check_cancel()
            # 自动登录退避/重连等待是有意休眠，不是任务卡死；持续刷新活动时间，
            # 避免用户把「卡死检测」调到 30 秒时看门狗误杀正在正常退避的实例。
            self.last_activity = time.monotonic()
            time.sleep(min(1.0, max(0.0, deadline - time.monotonic())))

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
            "审核图片 %d 张次（含重试）" % summary.get("images", 0),
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
        if summary.get("photos_saved"):
            parts.append(
                "缺陷照片归档 %d 张（标注 %d 张）"
                % (
                    summary.get("photos_saved", 0),
                    summary.get("photos_marked", 0),
                )
            )
        if summary.get("photo_download_failures"):
            parts.append(
                "缺陷照片原图下载失败 %d 张"
                % summary.get("photo_download_failures", 0)
            )
        if summary.get("photo_mark_failures"):
            parts.append(
                "缺陷照片标注失败 %d 张" % summary.get("photo_mark_failures", 0)
            )
        if summary.get("photo_save_failures"):
            parts.append(
                "缺陷照片写入失败 %d 张" % summary.get("photo_save_failures", 0)
            )
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
        if self.ignore_defect_boxes:
            # 无视缺陷框时不产生缺陷记录，也就不归档照片：明确说清，
            # 不能让「归档目录」那行日志反着承诺。
            self.log("已勾选无视缺陷框：不判定缺陷框、不归档缺陷照片")
        elif self.save_photos:
            self.log(
                "缺陷照片归档目录：%s（按「线路名称/杆塔号/原文件名」保存）"
                % self.outdir
            )
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
            "photos_saved": 0,
            "photos_marked": 0,
            "photo_failures": 0,
            "photo_download_failures": 0,
            "photo_mark_failures": 0,
            "photo_save_failures": 0,
        }
        skip_names = load_skip_task_names(self.skip_tasks_file)
        self.skip_task_names = skip_names
        self._skip_names_loaded = True
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
        self._tasks_since_summary_write = 0
        self._rows_since_summary_write = 0
        self._init_output_skip_file()
        # 同一输出目录反复启停：先把已有审核汇总恢复进内存再跑，
        # 否则本次启动清空行列表、收尾重写 xlsx 就把历史抹掉了。
        self._restore_summary_history()
        self._start_watchdog()
        prepare_failures = 0
        try:
            while True:
                try:
                    self.open_task_list()
                    self.apply_filters(settings)
                    break
                except Exception as exc:
                    if self.stop_requested:
                        raise
                    self._mark_connection_lost(exc)
                    prepare_failures += 1
                    reason = _redact_urls(exc)
                    if prepare_failures >= _PREPARE_MAX_FAILURES:
                        raise RuntimeError(
                            "连续 %d 次准备任务列表/筛选失败，已停止：%s"
                            % (prepare_failures, reason[:300])
                        ) from exc
                    self.log(
                        "准备任务列表/筛选失败：%s；刷新网页后重试（%d/%d）"
                        % (reason[:300], prepare_failures, _PREPARE_MAX_FAILURES)
                    )
                    self._refresh_and_resume(settings, "准备任务列表/筛选失败后")
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
                # 是否真的重试过同一任务：只有重试后 process_task 正常返回，
                # 才能撤回首次失败记下的审核中止/未知异常；重试再失败要保留两条。
                retried = False
                # 照片失败闸门：同一任务跨刷新重试累计（process_task 内不清零），
                # 只有整条任务照片都补齐/标注成功的才允许进跳过表。
                self._task_photo_failures = 0
                self._task_photo_download_failures = 0
                self._task_photos_saved = 0
                # 张次累计同样按「同一任务跨刷新/健康重试」归零一次。
                self.task_reviewed_total = 0
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
                        if retried:
                            self._drop_task_abort_anomalies(task_guid)
                        break
                    except Exception as exc:
                        if self.stop_requested:
                            raise
                        if isinstance(exc, BrowserUnhealthy):
                            self.log(
                                "任务《%s》触发健康重启，自动登录后重入当前任务"
                                % task_name
                            )
                            self.connection_lost = True
                            self.force_browser_restart = True
                            if not self._last_health_restart_at:
                                self._last_health_restart_at = time.monotonic()
                            try:
                                self._refresh_and_resume(settings, "健康重启后")
                                fresh_entry = self._find_task_entry(
                                    settings, task_guid, entry.get("page")
                                )
                            except Exception as find_exc:
                                self.log(
                                    "健康重启后查找任务《%s》失败：%s"
                                    % (task_name, _redact_urls(find_exc)[:300])
                                )
                                fresh_entry = None
                            if fresh_entry is not None:
                                entry = fresh_entry
                                item = entry.get("item") or item
                                attempts = 0
                                continue
                            self.log(
                                "健康重启后任务《%s》已不在列表，按已结束处理"
                                % task_name
                            )
                            result = {
                                "status": "skipped",
                                "images": self.reviewed_count,
                                "reason": "健康重启后任务已不在列表",
                            }
                            break
                        self._mark_connection_lost(exc)
                        reason = _redact_urls(exc)
                        retryable = not isinstance(exc, WriteResultUnknown)
                        relocated_failed = False
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
                                entry = fresh_entry
                                item = entry.get("item") or item
                                attempts += 1
                                retried = True
                                continue
                            relocated_failed = True
                            self.log("刷新后任务《%s》已不在列表，跳过重试" % task_name)
                        else:
                            self.log(
                                "任务《%s》异常，刷新网页后跳过：%s"
                                % (task_name, reason[:300])
                            )
                        if relocated_failed:
                            # 重试重定位失败/目标已消失：不是任务本身再次失败，
                            # 按普通跳过结束，不记未知异常、不写跳过表。
                            self._refresh_and_resume(settings, "任务异常后")
                            result = {
                                "status": "skipped",
                                "images": self.reviewed_count,
                                "reason": "刷新后任务已不在列表，未重试：%s"
                                % reason,
                            }
                            break
                        self.last_run_summary["unknown_errors"] += 1
                        self._refresh_and_resume(settings, "任务异常后")
                        result = {
                            "status": "skipped",
                            "images": self.reviewed_count,
                            "reason": reason,
                            "unknown_error": True,
                        }
                        break
                self.last_run_summary["tasks_processed"] += 1
                # 张次（含重试）：process_task 返回的 reviewed_count 只算最后一次，
                # 刷新/健康重试前已成功的张次要靠同任务累计值补回。
                self.last_run_summary["images"] += max(
                    int(result.get("images") or 0),
                    int(self.task_reviewed_total or 0),
                )
                self.task_progress_done += 1
                self._report_task_progress()
                status = result.get("status")
                system_defects = result.get("system_defects") or []
                load_failures = result.get("load_failures") or []
                button_not_ready = result.get("button_not_ready") or []
                # 输出目录跳过表收集转人工任务：系统缺陷框、图片加载失败、
                # 未审核数超阈值，以及最终仍失败的未知异常任务。
                # 本轮照片下载/标注失败的任务例外：写进跳过表就再也不会进这个任务，
                # 漏下的照片永远补不上；等下次跑补完再按原规则写入。
                photo_failures = self._task_photo_failures
                unknown_error = bool(result.get("unknown_error"))
                if (
                    result.get("threshold_skipped")
                    or system_defects
                    or load_failures
                    or unknown_error
                ) and not photo_failures:
                    self._record_output_skip_task(task_name)
                    if unknown_error:
                        # 配置了独立跳过表时同样写入；默认跳过表就是输出目录表，
                        # _remember_skip_task_name 按名称去重，同一路径不会写两遍。
                        self._remember_skip_task_name(task_name)
                elif photo_failures:
                    self.log(
                        "任务《%s》累计缺陷照片下载/标注失败 %d 张，"
                        "不写入跳过任务表，下次运行继续补存"
                        % (task_name, photo_failures)
                    )
                    if (
                        self._task_photo_download_failures
                        and not self._task_photos_saved
                    ):
                        # 永久失败（如接口 401）会每轮重进：说清原因与出路，
                        # 不另加审核守卫，也不伪造成功。
                        self.log(
                            "提示：任务《%s》的照片全部下载失败（%d 张，本任务已归档 0 张），"
                            "请先核对鉴权/网络；修好后重跑同一输出目录即可补齐，"
                            "确需跳过可手工把任务名加入跳过任务表"
                            % (task_name, self._task_photo_download_failures)
                        )
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
                if (
                    status == "deferred"
                    and (load_failures or button_not_ready)
                    and not photo_failures
                ):
                    self._remember_skip_task_name(task_name)
                    if task_name.casefold() not in self.skip_task_names:
                        self.log(
                            "警告：任务《%s》未能写入任务名称跳过表，"
                            "请检查文件是否被占用或不可写" % task_name
                        )
                task_images = int(getattr(self, "task_image_total", 0) or 0)
                if status == "completed":
                    self.last_run_summary["completed"] += 1
                    self._refresh_after_task(settings, task_name, task_images)
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
                self._refresh_after_task(settings, task_name, task_images)
            if not self.task_progress_total:
                self.task_progress_total = int(
                    (self.last_scan_summary or {}).get("total") or 0
                )
            if self.task_progress_total:
                self.task_progress_done = self.task_progress_total
                self._report_task_progress()
        finally:
            summary["photos_saved"] = self.photos_saved
            summary["photos_marked"] = self.photos_marked
            summary["photo_failures"] = self.photo_failures
            summary["photo_download_failures"] = self.photo_download_failures
            summary["photo_mark_failures"] = self.photo_mark_failures
            summary["photo_save_failures"] = self.photo_save_failures
            self.last_run_summary = summary
            self._flush_outputs()
            self._write_summary(summary)
        self._log_run_summary(summary)
        return summary

    def save_diagnostic(self, page, prefix: str, error=None) -> None:
        if error is not None:
            self._mark_connection_lost(error)
        if self.connection_lost:
            self.log("连接已断开，跳过失败截图（%s）" % prefix)
            return
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
        self._stop_watchdog()
        # 兜底刷新批量输出：run() 正常结束时已刷过，这里只补写尚未落盘的部分。
        self._flush_skip_table(force=True)
        if self.system_defect_dirty or self.anomaly_dirty:
            self._write_summary_xlsx(force=True)
        # 先恢复宿主机关屏/休眠，避免后面断连或清理卡住时仍占着防休眠。
        self.release_keep_awake()
        if self.connection_lost:
            self.log("连接已断开，跳过弹窗清理，后台停止 Playwright")
            if self.playwright is not None:
                old = self.playwright
                self.playwright = None
                stopper = threading.Thread(
                    target=lambda: self._stop_playwright_safely(old)
                )
                stopper.daemon = True
                stopper.start()
                stopper.join(2)
            self.monitor = None
            return
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
        help="线路名称（留空=全部；未提供时交互输入）",
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
        "--launch", action="store_true", help="启动独立受控浏览器并自动登录"
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
        default="",
        help="任务名称跳过表（xlsx/txt/csv，一列，首行标题），"
        "留空使用输出目录的「跳过任务.txt」",
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
        runner.ensure_authenticated()
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
