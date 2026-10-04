# -*- coding: utf-8 -*-
"""离线开发模拟数据：config.json 设 dev.mock=true 后，uvmp.make_client 返回 FakeClient。

仅供开发调试用（本机没有到派车系统的网络时验证全流程），生产环境禁止开启。
FakeClient 模拟 pcdmanager 分页查询（含 runCode/日期过滤）、getPrintDetails、
轨迹点查询、行程段查询四类接口的最小行为。
"""
import json
import time
from datetime import datetime, timedelta
from types import SimpleNamespace


def _mk_order(idx: int, day: datetime, run_state: str = "2") -> dict:
    date = day.strftime("%Y-%m-%d")
    begin = day.replace(hour=8 + idx % 8, minute=30, second=0)
    end = begin + timedelta(hours=2 + idx % 4)
    oid = "MOCK%04d" % idx
    return {
        "id": oid,
        "runCode": "PCD%s%02d" % (day.strftime("%Y%m%d"), idx),
        "vehicleId": "V%04d" % (idx % 3 + 1),
        "vehicleNumber": "晋AD%05d" % (10000 + idx),
        "licensePlateColor": "黄",
        "driverName": "测试司机%d" % idx,
        "driverTel": "1380000%04d" % idx,
        "userName": "测试用车人%d" % idx,
        "userTel": "1390000%04d" % idx,
        "postLevel": "高级技师",
        "departName": "检修一班",
        "isOutTag": "否",
        "vehicleProperty": "生产用车",
        "vehicleType": "小型普通客车",
        "powerType": "燃油",
        "planSendTime": begin.strftime("%Y-%m-%d %H:%M:%S"),
        "planBackTime": end.strftime("%Y-%m-%d %H:%M:%S"),
        "planSendTimeStr": begin.strftime("%Y-%m-%d %H:%M:%S"),
        "planBackTimeStr": end.strftime("%Y-%m-%d %H:%M:%S"),
        "planUseTime": 2 + idx % 4,
        "frmAddr": "单位大院",
        "crossAddr": "",
        "toAddr": "测试现场%d" % idx,
        "useReason": "检修作业",
        "useApplyRemark": "",
        "dispatchName": "测试派车人",
        "dispatchTime": int((begin - timedelta(days=1)).timestamp() * 1000),
        "runState": run_state,
        "orgName": "国网山西检修公司",
        "fixedNumbers": "3",
        "oilType": "02",
        # 归队登记（已完成单）
        "realSendTime": begin.strftime("%Y-%m-%d %H:%M:%S") + ".0" if run_state == "2" else "",
        "realBackTime": end.strftime("%Y-%m-%d %H:%M:%S") + ".0" if run_state == "2" else "",
        "realSendTimeStr": begin.strftime("%Y-%m-%d %H:%M:%S") if run_state == "2" else "",
        "realBackTimeStr": end.strftime("%Y-%m-%d %H:%M:%S") if run_state == "2" else "",
        "startMiles": 12000 + idx * 100, "endMiles": 12000 + idx * 100 + 87,
        "totalMiles": 87, "realNumber": 2, "oilFee": "", "oilUse": "", "oilPrice": "",
        "sxCardNumber": "", "roadbridgeFee": "", "crossAddrRegister": "", "toAddrRegister": "",
        "remark": "",
        "audinList": [
            {"wfInstanceName": "部门审批", "operatorName": "测试班长", "operatorType": "审批",
             "operateTime": (begin - timedelta(hours=2)).strftime("%Y-%m-%d %H:%M:%S")},
        ],
    }


def make_items(today: datetime = None) -> list:
    today = today or datetime.now()
    items = [_mk_order(i + 1, today, "2") for i in range(5)]
    items.append(_mk_order(6, today, "1"))   # 已派车未完结：每日同步要包含，轨迹导出应跳过
    items.append(_mk_order(7, today, "0"))   # 待派车
    items.append(_mk_order(11, today - timedelta(days=1), "2"))
    items.append(_mk_order(12, today - timedelta(days=1), "2"))
    return items


def _parse_filter(params_str: str) -> dict:
    try:
        flt = (json.loads(params_str) or {}).get("filter", "") or ""
    except ValueError:
        return {}
    out = {}
    for kv in flt.split("&"):
        k, _, v = kv.partition("=")
        if k:
            out[k.strip()] = v.strip()
    return out


class FakeClient:
    """接口形状与 vehicle_export.UvmpClient 一致（get/post + .auth）"""

    def __init__(self, items: list):
        self._items = items
        self.auth = SimpleNamespace(token="MOCK_TOKEN", refresh_token="", mtk="")

    # ---- pcdmanager 分页查询 ----
    def _query_pcd(self, p_data: dict) -> dict:
        params = json.loads((p_data or {}).get("params", "{}"))
        flt = _parse_filter((p_data or {}).get("params", "{}"))
        page_index = int(params.get("pageIndex", 1))
        page_size = int(params.get("pageSize", 50))
        items = self._items
        if flt.get("runCode"):
            items = [it for it in items if it.get("runCode") == flt["runCode"]]
        d1 = (flt.get("planSendTime") or "")[:10]
        d2 = (flt.get("planSendTime2") or "")[:10]
        if d1:
            items = [it for it in items if str(it.get("planSendTime", ""))[:10] >= d1]
        if d2:
            items = [it for it in items if str(it.get("planSendTime", ""))[:10] <= d2]
        if flt.get("runState"):
            items = [it for it in items if str(it.get("runState")) == flt["runState"]]
        total = len(items)
        lo = (page_index - 1) * page_size
        return {"successful": True, "resultValue": {
            "itemCount": total, "items": items[lo:lo + page_size]}}

    # ---- 打印详情 ----
    def _print_details(self, base_path: str) -> dict:
        ids_part = base_path.split("id=", 1)[1].split("&", 1)[0] if "id=" in base_path else ""
        ids = [x for x in ids_part.split(",") if x]
        by_id = {it["id"]: it for it in self._items}
        return {"successful": True, "resultValue": {
            "items": [by_id[i] for i in ids if i in by_id]}}

    # ---- 轨迹点 ----
    @staticmethod
    def _points(p_data: dict) -> dict:
        begin = (p_data or {}).get("beginTimeStr", "")[:19] or "2026-09-23 08:30:00"
        try:
            t0 = datetime.strptime(begin, "%Y-%m-%d %H:%M:%S")
        except ValueError:
            t0 = datetime.now().replace(hour=8, minute=30, second=0)
        pts = []
        for i in range(40):
            t = t0 + timedelta(minutes=2 * i)
            pts.append({
                "dx": round(112.548 + i * 0.006, 6),
                "dy": round(37.857 + i * 0.004, 6),
                "speed": 30 + i % 25,
                "direction": (45 + i * 3) % 360,
                "locatetime": t.strftime("%Y-%m-%d %H:%M:%S"),
            })
        return {"successful": True, "data": pts}

    # ---- 行程段 ----
    def _segments(self) -> dict:
        segs = []
        for it in self._items:
            if str(it.get("runState")) != "2" or not it.get("realSendTime"):
                continue
            segs.append({
                "id": "SEG-" + it["id"],
                "vehicleId": it["vehicleId"],
                "vehicleNumber": it["vehicleNumber"],
                "routeNo": "R" + it["id"],
                "starttime": it["realSendTimeStr"],
                "endtime": it["realBackTimeStr"],
                "miles": it["totalMiles"],
                "startX": 112.548, "startY": 37.857, "endX": 112.782, "endY": 38.013,
            })
        return {"successful": True, "resultValue": {"itemCount": len(segs), "items": segs}}

    def get(self, base_path: str, p_data=None, desc: str = "") -> dict:
        time.sleep(0.05)  # 模拟网络耗时，让进度可见
        if "getPrintDetails" in base_path:
            return self._print_details(base_path)
        if "queryTrackHisByVehicle" in base_path:
            return self._points(p_data)
        if "simpleQuery" in base_path:
            return self._segments()
        if "pcdmanager/" in base_path:
            return self._query_pcd(p_data)
        raise RuntimeError("FakeClient 未模拟的接口: " + base_path)

    def post(self, base_path: str, body: dict, desc: str = "") -> dict:
        raise RuntimeError("FakeClient 未模拟的 POST 接口: " + base_path)


def fake_client(cfg: dict = None, log=print) -> FakeClient:
    log("[dev] dev.mock=true，使用模拟派车数据（当日 7 单 + 昨日 2 单）")
    return FakeClient(make_items())
