package apps

import (
	"context"
	"fmt"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/auth"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/uvmpapi"
)

// OrderQuerier 派车单查询接口（*uvmpapi.Client 真实实现；dev.mock 用 mockQuerier）。
type OrderQuerier interface {
	// QueryPcdAllFilter 自由 filter 全量页（uvmpapi.Client.QueryPcdAllFilter 同口径）。
	QueryPcdAllFilter(ctx context.Context, filterStr string, pageSize int) ([]map[string]any, error)
}

// LogFunc 日志回调（对齐 uvmpapi.LogFunc）。
type LogFunc func(format string, args ...any)

// MakeClient 派车系统客户端获取（移植自 toolkit/uvmp.py make_client）：
//   - dev.mock=true → 模拟数据（离线开发）
//   - config.ini [auth] mode=token → 直接用手动三件套
//   - mode=sso（默认）→ SSO 账号密码自动登录（凭据 config.json 优先、config.ini 兜底）
//
// 返回（client, tokenLen）：tokenLen 供 selftest:sso 回报凭证长度（mock 为 9）。
func MakeClient(cfg config.Dict, log LogFunc) (OrderQuerier, int, error) {
	if config.BoolField(config.MapField(cfg, "dev"), "mock", false) {
		if log != nil {
			log("[dev] dev.mock=true，使用模拟派车数据")
		}
		return mockQuerier{today: time.Now()}, 9, nil
	}
	acfg, err := auth.ParseConfig(cfg)
	if err != nil {
		return nil, 0, err
	}
	opts := []auth.Option{}
	if log != nil {
		opts = append(opts, auth.WithLogger(auth.LogFunc(log)))
	}
	mgr := auth.NewManager(acfg, opts...)
	creds, err := mgr.Login(context.Background())
	if err != nil {
		return nil, 0, err
	}
	if log != nil {
		if acfg.Mode == "token" {
			log("使用 config.ini 中的手动凭证（token 模式）")
		} else {
			log("登录成功，凭证已就绪")
		}
	}
	client := uvmpapi.NewClient(mgr, uvmpapi.WithLogger(uvmpapi.LogFunc(log)))
	return client, len(creds.Token), nil
}

// ---------------------------------------------------------------- dev.mock 模拟数据

// mockQuerier dev.mock 用离线模拟（toolkit/devmock.py make_items 同口径：
// 当日 7 单 + 昨日 2 单；仅覆盖每日同步所需的全量 filter 查询）。
type mockQuerier struct {
	today time.Time
}

// QueryPcdAllFilter 模拟「当日全部状态」查询（含昨日 2 单，供 is_today_order 二次过滤验证）。
func (m mockQuerier) QueryPcdAllFilter(_ context.Context, _ string, _ int) ([]map[string]any, error) {
	out := []map[string]any{}
	for i := 1; i <= 7; i++ {
		out = append(out, mockOrder(i, m.today))
	}
	out = append(out, mockOrder(11, m.today.AddDate(0, 0, -1)))
	out = append(out, mockOrder(12, m.today.AddDate(0, 0, -1)))
	return out, nil
}

// mockOrder 一条模拟派车单（toolkit/devmock._mk_order 精简同口径）。
func mockOrder(idx int, day time.Time) map[string]any {
	date := day.Format("2006-01-02")
	begin := time.Date(day.Year(), day.Month(), day.Day(), 8+idx%8, 30, 0, 0, time.Local)
	end := begin.Add(time.Duration(2+idx%4) * time.Hour)
	runState := "2"
	switch idx {
	case 6:
		runState = "1" // 已派车未完结：每日同步要包含，轨迹导出应跳过
	case 7:
		runState = "0" // 待派车
	}
	beginStr := begin.Format("2006-01-02 15:04:05")
	endStr := end.Format("2006-01-02 15:04:05")
	realSend, realBack := "", ""
	if runState == "2" {
		realSend = beginStr + ".0"
		realBack = endStr + ".0"
	}
	return map[string]any{
		"id":                fmt.Sprintf("MOCK%04d", idx),
		"runCode":           fmt.Sprintf("PCD%s%02d", day.Format("20060102"), idx),
		"vehicleId":         fmt.Sprintf("V%04d", idx%3+1),
		"vehicleNumber":     fmt.Sprintf("晋AD%05d", 10000+idx),
		"licensePlateColor": "黄",
		"driverName":        fmt.Sprintf("测试司机%d", idx),
		"driverTel":         fmt.Sprintf("1380000%04d", idx),
		"userName":          fmt.Sprintf("测试用车人%d", idx),
		"userTel":           fmt.Sprintf("1390000%04d", idx),
		"departName":        "检修一班",
		"vehicleType":       "小型普通客车",
		"powerType":         "燃油",
		"planSendTime":      beginStr,
		"planBackTime":      endStr,
		"planSendTimeStr":   beginStr,
		"planBackTimeStr":   endStr,
		"planUseTime":       2 + idx%4,
		"frmAddr":           "单位大院",
		"toAddr":            fmt.Sprintf("测试现场%d", idx),
		"useReason":         "检修作业",
		"dispatchName":      "测试派车人",
		"dispatchTime":      begin.AddDate(0, 0, -1).UnixMilli(),
		"runState":          runState,
		"realSendTime":      realSend,
		"realBackTime":      realBack,
		"realSendTimeStr":   realSend,
		"realBackTimeStr":   realBack,
		"remark":            "",
		"date":              date,
	}
}
