package apps

import (
	"strings"
	"testing"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/job"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
)

// newTestManager 临时状态目录的 JobManager。
func newTestManager(t *testing.T) *job.JobManager {
	t.Helper()
	return job.NewManager(&state.Store{Root: t.TempDir()})
}

// runJobSync 跑一个任务并返回 job 与业务错误（用于断言状态/日志）。
func runJobSync(t *testing.T, b Backend, params map[string]any) (*job.Job, map[string]any, error) {
	t.Helper()
	m := newTestManager(t)
	j := m.Create("test", "测试")
	var res map[string]any
	var runErr error
	m.RunSync(j, func(jj *job.Job) (map[string]any, error) {
		r, err := b.Run(jj, params)
		res = r
		runErr = err
		return r, err
	}, false)
	return j, res, runErr
}

func TestBuiltinRegistry(t *testing.T) {
	Reset()
	Builtin()
	all := All()
	if len(all) != 6 {
		t.Fatalf("应用数 = %d, want 6", len(all))
	}
	// 排序：order 升序
	orders := []int{}
	for _, a := range all {
		orders = append(orders, a.Order)
	}
	for i := 1; i < len(orders); i++ {
		if orders[i] < orders[i-1] {
			t.Fatalf("排序错误: %v", orders)
		}
	}
	// 元数据契约（app.json 同源）
	want := map[string]struct {
		name     string
		order    int
		schedule bool
	}{
		"quiz_search":     {"题库搜题", 1, false},
		"course_autoplay": {"学堂刷课", 2, false},
		"pcd_export":      {"轨迹导出", 3, false},
		"tree_sync":       {"树障同步", 4, true},
		"crossing_sync":   {"交跨同步", 5, true},
		"daily_sync":      {"派车同步", 6, true},
	}
	for id, w := range want {
		a := Get(id)
		if a == nil {
			t.Fatalf("应用缺失: %s", id)
		}
		if a.Name != w.name || a.Order != w.order || a.Schedule != w.schedule {
			t.Fatalf("%s 元数据 = %+v, want %+v", id, a, w)
		}
		if a.Backend == nil {
			t.Fatalf("%s 后端缺失", id)
		}
	}
}

func TestShellOnlyBackend(t *testing.T) {
	Reset()
	Builtin()
	_, res, err := runJobSync(t, Get("quiz_search").Backend, nil)
	if err == nil || !strings.Contains(err.Error(), "shell_only") || !strings.Contains(err.Error(), "壳内") {
		t.Fatalf("quiz_search 应返回 shell_only 明确信息, err=%v", err)
	}
	if res == nil || res["shell_only"] != true || res["message"] == "" {
		t.Fatalf("quiz_search 结果应带 shell_only 标记与明确信息, res=%v", res)
	}
	if _, ok := Get("course_autoplay").Backend.(*courseAutoplayBackend); !ok {
		t.Fatalf("course_autoplay 应已接入真实后端, got %T", Get("course_autoplay").Backend)
	}
}

func TestRegisterAndReset(t *testing.T) {
	Reset()
	Register(&App{ID: "x", Name: "测试应用", Backend: shellOnly{id: "x", name: "测试应用"}})
	a := Get("x")
	if a == nil {
		t.Fatal("注册失败")
	}
	if a.Badge != "测试" {
		t.Fatalf("badge 缺省 = %q", a.Badge)
	}
	if a.Order != 99 {
		t.Fatalf("order 缺省 = %d", a.Order)
	}
	Reset()
	if Get("x") != nil {
		t.Fatal("Reset 未清空")
	}
}

func TestIsTodayOrder(t *testing.T) {
	today := "2026-10-28"
	// 字符串时间：第一个字段即定论
	if !IsTodayOrder(map[string]any{"planSendTime": "2026-10-28 08:00:00"}, today) {
		t.Fatal("当日应保留")
	}
	if IsTodayOrder(map[string]any{"planSendTime": "2026-10-27 23:59:59"}, today) {
		t.Fatal("昨日应过滤")
	}
	// planSendTime 空串 → 看 dispatchTime
	if !IsTodayOrder(map[string]any{"planSendTime": "", "dispatchTime": "2026-10-28 07:00:00.0"}, today) {
		t.Fatal("兜底字段当日应保留")
	}
	if IsTodayOrder(map[string]any{"planSendTime": "", "dispatchTime": "2026-10-26"}, today) {
		t.Fatal("兜底字段非当日应过滤")
	}
	// epoch 毫秒（本地时区当日）
	ms := time.Date(2026, 10, 28, 8, 30, 0, 0, time.Local).UnixMilli()
	if !IsTodayOrder(map[string]any{"planSendTime": float64(ms)}, today) {
		t.Fatal("epoch 当日应保留")
	}
	msY := time.Date(2026, 10, 27, 8, 30, 0, 0, time.Local).UnixMilli()
	if IsTodayOrder(map[string]any{"planSendTime": float64(msY)}, today) {
		t.Fatal("epoch 非当日应过滤")
	}
	// 字段缺失/全空 → 保留
	if !IsTodayOrder(map[string]any{}, today) {
		t.Fatal("缺失字段应保留")
	}
	if !IsTodayOrder(map[string]any{"planSendTime": "", "dispatchTime": ""}, today) {
		t.Fatal("全空字段应保留")
	}
	// 数值 0 视同空 → 保留
	if !IsTodayOrder(map[string]any{"planSendTime": float64(0), "dispatchTime": float64(0)}, today) {
		t.Fatal("0 应视同空")
	}
}
