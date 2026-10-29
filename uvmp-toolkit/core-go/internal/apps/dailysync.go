package apps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/job"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/schedule"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/usb"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/xlsx"
)

// dailySyncBackend 每日派车单同步（移植自 toolkit/apps/daily_sync/backend.py）：
// 导出当日（全部状态）派车单 → xlsx → 写入 U 盘（卷标 GLKVM）。
//   - xlsx 列结构/文件名逐字保持（壹匣服务端解析契约）
//   - 「当日」口径保持：页面 filter 当日 + 客户端 isTodayOrder 二次过滤兜底
//   - U 盘发现/挂载/复制前复检保持旧行为；找不到 U 盘时可用 usb.fallback_dir 兜底
type dailySyncBackend struct {
	// 可注入依赖（测试 fake）；nil 用真实实现。
	Querier  OrderQuerier
	UsbOpts  *usb.Options
	Now      func() time.Time
	StateSt  *state.Store
	LoadConf func() config.Dict
}

const dailySyncAppID = "daily_sync"

func (b *dailySyncBackend) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *dailySyncBackend) load() config.Dict {
	if b.LoadConf != nil {
		return b.LoadConf()
	}
	return config.Load()
}

// IsTodayOrder 派车单是否属于当日：预计用车时间优先，创建时间兜底；字段缺失/不可解析时保留
// （移植自 export_dispatch_orders.py 的 _is_today_order，口径一致：
// 第一个非空字段即定论，解析失败才看下一个；两字段都空/不可解析 → 保留）。
func IsTodayOrder(item map[string]any, today string) bool {
	for _, field := range []string{"planSendTime", "dispatchTime"} {
		value, ok := item[field]
		if !ok || isBlank(value) {
			continue
		}
		switch t := value.(type) {
		case int, int32, int64, float32, float64:
			ms := toMillis(t)
			return time.UnixMilli(ms).In(time.Local).Format("2006-01-02") == today
		case string:
			if len(t) >= 10 {
				return t[:10] == today
			}
			return t == today
		default:
			return false
		}
	}
	return true
}

// isBlank Python「假值」口径：None/空串/False/数值 0 均跳过。
func isBlank(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case bool:
		return !t
	case float64:
		return t == 0
	case float32:
		return t == 0
	case int, int32, int64:
		return toMillis(t) == 0
	}
	return false
}

func toMillis(v any) int64 {
	switch t := v.(type) {
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case int64:
		return t
	case float32:
		return int64(t)
	case float64:
		return int64(t)
	}
	return 0
}

// queryDayOrders 查询某日全部状态派车单（不附加 runState/vehicleState/keepTag 限制）。
func queryDayOrders(ctx context.Context, client OrderQuerier, day string, log func(string)) ([]map[string]any, error) {
	flt := fmt.Sprintf("planSendTime=%s 00:00&planSendTime2=%s 23:59:00", day, day)
	items, err := client.QueryPcdAllFilter(ctx, flt, 200)
	if err != nil {
		return nil, err
	}
	log(fmt.Sprintf("接口返回 %d 条（页面口径）", len(items)))
	kept := []map[string]any{}
	for _, it := range items {
		if IsTodayOrder(it, day) {
			kept = append(kept, it)
		}
	}
	if len(kept) != len(items) {
		log(fmt.Sprintf("已按当日二次过滤: %d -> %d 条", len(items), len(kept)))
	}
	return kept, nil
}

// Run 实现 Backend（Python daily_sync.run 同口径）。
func (b *dailySyncBackend) Run(j *job.Job, params map[string]any) (map[string]any, error) {
	cfg := b.load()
	day := strings.TrimSpace(config.StrField(params, "date"))
	if day == "" {
		day = b.now().Format("2006-01-02")
	}
	log := func(s string) { j.Log(s) }
	log(fmt.Sprintf("开始导出派车单（日期：%s）", day))

	j.SetProgress(0, 4, "检查U盘")
	usbCfg := config.MapField(cfg, "usb")
	usbDir, source := usb.UsbWritableDir(
		config.StrField(usbCfg, "label"),
		config.StrField(usbCfg, "fallback_dir"),
		log, b.UsbOpts)
	if usbDir == "" {
		return nil, fmt.Errorf("U盘不可用且未配置后备目录（可在设置页配置 usb.fallback_dir）")
	}

	j.SetProgress(1, 4, "登录派车系统")
	client := b.Querier
	if client == nil {
		var err error
		client, _, err = MakeClient(cfg, LogFunc(func(format string, args ...any) {
			log(fmt.Sprintf(format, args...))
		}))
		if err != nil {
			return nil, err
		}
	}

	j.SetProgress(2, 4, "查询派车单")
	items, err := queryDayOrders(context.Background(), client, day, log)
	if err != nil {
		return nil, err
	}
	log(fmt.Sprintf("共获取 %d 条当日派车单", len(items)))
	if len(items) == 0 {
		log("警告：未获取到任何数据（当日可能无派车）")
		markSuccess(b.StateSt, day, map[string]any{"count": 0, "file": ""})
		return map[string]any{"date": day, "count": 0, "file": "", "dest": ""}, nil
	}

	j.SetProgress(3, 4, "生成Excel")
	tempDir := strings.TrimSpace(config.StrField(config.MapField(cfg, "daily_export"), "temp_dir"))
	if tempDir == "" {
		home, _ := os.UserHomeDir()
		tempDir = filepath.Join(home, "临时下载") // 保持旧脚本习惯
	}
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return nil, err
	}
	fname := fmt.Sprintf("%s-派车单.xlsx", day)
	tempPath := filepath.Join(tempDir, fname)
	if _, err := xlsx.WriteDispatchXlsx(items, tempPath, log); err != nil {
		return nil, err
	}

	j.SetProgress(4, 4, "写入交付目录")
	// 抓取耗时期间 U 盘可能断开，复制前重新检测（移植旧脚本行为）
	if st, err := os.Stat(usbDir); err != nil || !st.IsDir() {
		log("复制前检测到目标目录不可用，重新检测U盘 ...")
		usbDir, source = usb.UsbWritableDir(
			config.StrField(usbCfg, "label"),
			config.StrField(usbCfg, "fallback_dir"),
			log, b.UsbOpts)
		if usbDir == "" {
			return nil, fmt.Errorf("写入前 U 盘不可用；文件保留在: %s", tempPath)
		}
	}
	finalPath := filepath.Join(usbDir, fname)
	if err := copyFile(tempPath, finalPath); err != nil {
		return nil, err
	}
	usb.SyncFs(b.UsbOpts)
	fi, err := os.Stat(finalPath)
	if err != nil {
		return nil, err
	}
	log(fmt.Sprintf("导出完成! 文件已保存至: %s（%.1f KB，%d 条）",
		finalPath, float64(fi.Size())/1024, len(items)))

	dest := "U盘"
	if source != usb.SourceUSB {
		dest = "后备目录"
	}
	markSuccess(b.StateSt, day, map[string]any{"count": len(items), "file": finalPath})
	return map[string]any{
		"date":  day,
		"count": len(items),
		"file":  finalPath,
		"dest":  dest,
		"dir":   usbDir,
	}, nil
}

// Status 实现 StatusBackend（Python daily_sync.status 同口径）。
func (b *dailySyncBackend) Status(cfg map[string]any) (map[string]any, error) {
	sch := config.MapField(cfg, "schedule")
	usbCfg := config.MapField(cfg, "usb")
	due, reason, _ := schedule.DueCheck(cfg, time.Time{}, "os", "", nil)
	st := b.StateSt
	if st == nil {
		st = state.Default()
	}
	mount := usb.FindUsbMount(config.StrField(usbCfg, "label"), nil, b.UsbOpts)
	return map[string]any{
		"schedule": map[string]any{
			"enabled":  config.BoolField(sch, "enabled", true),
			"time":     config.StrFieldDefault(sch, "time", "09:15"),
			"driver":   config.StrFieldDefault(sch, "driver", "os"),
			"next_run": schedule.NextRunText(cfg, time.Time{}),
		},
		"stamp":    st.GetStamp(schedule.STAMP_NAME),
		"last_run": st.LastRun(dailySyncAppID),
		"usb": map[string]any{
			"label":        config.StrField(usbCfg, "label"),
			"fallback_dir": config.StrField(usbCfg, "fallback_dir"),
			"mount":        mount,
		},
		"due": map[string]any{"due": due, "reason": reason},
	}, nil
}

// markSuccess 写每日同步成功戳（store 为 nil 用默认状态目录）。
func markSuccess(st *state.Store, today string, extra map[string]any) {
	if st == nil {
		state.SetStamp(schedule.STAMP_NAME, stampData(today, extra))
		return
	}
	st.SetStamp(schedule.STAMP_NAME, stampData(today, extra))
}

func stampData(today string, extra map[string]any) map[string]any {
	data := map[string]any{"date": today, "time": time.Now().Format("2006-01-02 15:04:05")}
	for k, v := range extra {
		data[k] = v
	}
	return data
}

// copyFile 复制文件（shutil.copy2 的内容语义；时间戳尽力保留）。
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return err
	}
	if st, err := os.Stat(src); err == nil {
		_ = os.Chtimes(dst, st.ModTime(), st.ModTime())
	}
	return nil
}
