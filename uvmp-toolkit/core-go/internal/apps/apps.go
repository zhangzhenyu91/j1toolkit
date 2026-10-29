// Package apps 应用注册器（移植自 toolkit/apps/__init__.py + 各应用 backend.py）。
//
// 新应用接入 = 在本包注册一个 App 元数据 + Backend 实现（Go 静态注册，替代 Python
// 扫描 apps/*/app.json 的动态加载）：
//
//	apps.Register(&apps.App{ID: "xxx", Name: "...", ..., Backend: &xxxBackend{}})
//
// Backend 契约函数：Run(job, params) -> (result, error)；可选 Status(cfg) / Preview(params) /
// LaunchBrowser(cfg)（需要人工准备会话的受控浏览器应用）。
// 注意：Backend 禁止依赖 GUI——CLI/定时器无头环境没有 GUI。
package apps

import (
	"fmt"
	"sort"
	"sync"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/job"
)

// Backend 应用后端契约（Python backend.run 同口径）。
type Backend interface {
	// Run 执行一次任务；日志/进度/取消经 job，返回业务摘要。
	Run(j *job.Job, params map[string]any) (map[string]any, error)
}

// StatusBackend 可选：应用状态卡数据（Python backend.status(cfg)）。
type StatusBackend interface {
	Status(cfg map[string]any) (map[string]any, error)
}

// PreviewBackend 可选：输入预览（Python backend.preview(params)）。
type PreviewBackend interface {
	Preview(params map[string]any) (map[string]any, error)
}

// BrowserBackend 可选：启动受控浏览器（Python backend.launch_browser(cfg)）。
type BrowserBackend interface {
	LaunchBrowser(cfg map[string]any) (map[string]any, error)
}

// App 应用元数据 + 后端（app.json 字段）。
type App struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Badge    string `json:"badge"`
	Desc     string `json:"desc"`
	Order    int    `json:"order"`
	Schedule bool   `json:"schedule"` // 支持定时调度（app.json "schedule": true）
	Page     string `json:"page"`     // 附加页面标识（Electron 壳用）
	Backend  Backend
}

var (
	regMu sync.RWMutex
	reg   = map[string]*App{}
)

// Register 注册应用（重复 id 覆盖；供内置与后续应用接入共用）。
func Register(a *App) {
	regMu.Lock()
	defer regMu.Unlock()
	if a.ID == "" {
		panic("apps.Register: 应用 id 不能为空")
	}
	if a.Name == "" {
		a.Name = a.ID
	}
	if a.Badge == "" {
		r := []rune(a.Name)
		if len(r) > 2 {
			a.Badge = string(r[:2])
		} else {
			a.Badge = a.Name
		}
	}
	if a.Order == 0 {
		a.Order = 99
	}
	reg[a.ID] = a
}

// Get 按 id 取应用（无则 nil）。
func Get(id string) *App {
	regMu.RLock()
	defer regMu.RUnlock()
	return reg[id]
}

// All 全部应用（按 order、name 排序，与 Python scan_apps 一致）。
func All() []*App {
	regMu.RLock()
	out := make([]*App, 0, len(reg))
	for _, a := range reg {
		out = append(out, a)
	}
	regMu.RUnlock()
	sort.Slice(out, func(i, k int) bool {
		if out[i].Order != out[k].Order {
			return out[i].Order < out[k].Order
		}
		return out[i].Name < out[k].Name
	})
	return out
}

// Reset 清空注册表（测试隔离用）。
func Reset() {
	regMu.Lock()
	defer regMu.Unlock()
	reg = map[string]*App{}
}

// shellOnly 壳内应用后端：功能由桌面壳页面直接提供（题库搜题在壳层搜题页——
// 截屏/OCR/悬浮窗全在壳层），不走任务通道。结果与错误都携带 shell_only 语义，
// 任务通道调用方可据此转到桌面页面。
type shellOnly struct{ id, name string }

// message 明确信息文案。
func (s shellOnly) message() string {
	return fmt.Sprintf("应用「%s」（%s）为壳内功能（shell_only）：由桌面壳「%s」页直接提供，不走任务通道", s.name, s.id, s.name)
}

// Run 任务通道不承载本应用：返回 shell_only 结果与明确错误。
func (s shellOnly) Run(*job.Job, map[string]any) (map[string]any, error) {
	msg := s.message()
	return map[string]any{"shell_only": true, "message": msg}, fmt.Errorf("%s", msg)
}

// Builtin 注册全部内置应用（幂等；rpc/CLI 启动时调用）。
func Builtin() {
	Register(&App{
		ID: "quiz_search", Name: "题库搜题", Badge: "搜题", Order: 1,
		Desc: "屏幕框选区域实时 OCR 搜题：本地题库导入管理，双扫描框覆盖页面顶底，答案悬浮窗直出",
		Page: "page.html",
		// 壳内应用（shell_only）：实现由桌面壳搜题页直接提供（截屏/OCR/悬浮窗在壳层），不走任务通道
		Backend: shellOnly{id: "quiz_search", name: "题库搜题"},
	})
	Register(&App{
		ID: "course_autoplay", Name: "学堂刷课", Badge: "学习", Order: 2,
		Desc:    "接管受控浏览器自动学习课程，支持暂停、切换培训班与断点续刷",
		Backend: &courseAutoplayBackend{},
	})
	Register(&App{
		ID: "pcd_export", Name: "轨迹导出", Badge: "导出", Order: 3,
		Desc:    "按派车单号清单（xlsx 或粘贴文本）按序导出派车单 PDF 与行驶轨迹 PDF",
		Page:    "page.html",
		Backend: &pcdExportBackend{},
	})
	Register(&App{
		ID: "tree_sync", Name: "树障同步", Badge: "树表", Order: 4, Schedule: true,
		Desc:    "按壹匣服务端经 U 盘下发的指令同步树障统计表 xlsx：snapshot 内网表上盘 / replace 新表回内网，夜间定时兜底上盘",
		Backend: newTableSyncBackend(treeSyncSpec),
	})
	Register(&App{
		ID: "crossing_sync", Name: "交跨同步", Badge: "交表", Order: 5, Schedule: true,
		Desc:    "按壹匣服务端经 U 盘下发的指令同步交跨统计表 xlsx：snapshot 内网表上盘 / replace 新表回内网，夜间定时兜底上盘",
		Backend: newTableSyncBackend(crossingSyncSpec),
	})
	Register(&App{
		ID: "daily_sync", Name: "派车同步", Badge: "同步", Order: 6, Schedule: true,
		Desc:    "每日定时导出当日派车单（全部状态）为 xlsx 写入 U 盘，供壹匣服务端取走同步",
		Page:    "page.html",
		Backend: &dailySyncBackend{},
	})
}
