// Package rpc stdio JSON-RPC 服务（移植自 toolkit/rpc.py）：Electron 壳经 spawn 管道调用核心。
//
// 协议（每行一个 JSON）：
//
//	请求  {"id": 1, "method": "getConfig", "params": {...}}
//	响应  {"id": 1, "ok": true, "result": ...} / {"id": 1, "ok": false, "error": "..."}
//	事件（无 id，服务端主动推）{"event": "job.log"|"job.progress"|"job.done", "job_id": "...", ...}
//
// 为什么是 stdio 而不是 HTTP：免端口/令牌管理，父子进程天然 1:1 生命周期，
// Electron 主进程 spawn 即用。响应/事件统一非 ASCII 转 \uXXXX（Python
// ensure_ascii=True 同口径，跨控制台编码 100% 安全，对端 JSON.parse 还原）。
package rpc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/apps"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/job"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/schedule"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/usb"
)

// Server stdio JSON-RPC 服务端。
type Server struct {
	Manager *job.JobManager
	// UsbOpts / LoadConf / Now 可注入（测试 fake）。
	UsbOpts  *usb.Options
	LoadConf func() config.Dict
	Now      func() time.Time

	mu  sync.Mutex // 输出串行化
	out io.Writer
	wg  sync.WaitGroup
}

// New 创建 Server；out 为响应/事件输出端（通常 os.Stdout）。
func New(out io.Writer) *Server {
	return &Server{
		Manager: job.NewManager(state.Default()),
		out:     out,
	}
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) load() config.Dict {
	if s.LoadConf != nil {
		return s.LoadConf()
	}
	return config.Load()
}

// ---------------------------------------------------------------- 底层

// marshalASCII JSON 序列化并把非 ASCII 字符转 \uXXXX（含代理对），
// 与 Python json.dumps(ensure_ascii=True) 同口径。
func marshalASCII(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var sb strings.Builder
	sb.Grow(len(b))
	for _, r := range string(b) {
		if r < 0x80 {
			sb.WriteRune(r)
			continue
		}
		if r > 0xFFFF {
			u := r - 0x10000
			sb.WriteString(fmt.Sprintf("\\u%04X\\u%04X", 0xD800+(u>>10), 0xDC00+(u&0x3FF)))
		} else {
			sb.WriteString(fmt.Sprintf("\\u%04X", r))
		}
	}
	return []byte(sb.String()), nil
}

func (s *Server) send(obj map[string]any) {
	b, err := marshalASCII(obj)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out.Write(b)
	s.out.Write([]byte("\n"))
}

func (s *Server) emit(event string, payload map[string]any) {
	obj := map[string]any{"event": event}
	for k, v := range payload {
		obj[k] = v
	}
	s.send(obj)
}

// ---------------------------------------------------------------- 方法表

type handler func(params map[string]any) (any, error)

// selftestFn 自检入口签名（render/sso/cdp）。
type selftestFn func(j *job.Job, params map[string]any) (map[string]any, error)

func (s *Server) methods() map[string]handler {
	return map[string]handler{
		"ping":                     s.mPing,
		"getSystemInfo":            s.mGetSystemInfo,
		"getConfig":                s.mGetConfig,
		"saveConfig":               s.mSaveConfig,
		"getApps":                  s.mGetApps,
		"getAppStatus":             s.mGetAppStatus,
		"launchAppBrowser":         s.mLaunchAppBrowser,
		"preview":                  s.mPreview,
		"runJob":                   s.mRunJob,
		"maybeRunDaily":            s.mMaybeRunDaily,
		"maybeRunTreeSyncTick":     s.mMaybeRunTreeSyncTick,
		"maybeRunCrossingSyncTick": s.mMaybeRunCrossingSyncTick,
		"cancelJob":                s.mCancelJob,
		"getJob":                   s.mGetJob,
		"getHistory":               s.mGetHistory,
		"openPath":                 s.mOpenPath,
		"getJobLog":                s.mGetJobLog,
		"clearLogs":                s.mClearLogs,
		"deleteHistory":            s.mDeleteHistory,
		"getLogStats":              s.mGetLogStats,
	}
}

func strParam(params map[string]any, key string) string {
	if v, ok := params[key].(string); ok {
		return v
	}
	return ""
}

func mapParam(params map[string]any, key string) map[string]any {
	m, _ := params[key].(map[string]any)
	return m
}

// mPing 连通性探测。
func (s *Server) mPing(_ map[string]any) (any, error) {
	return map[string]any{"core": "uvmp-toolkit", "jobs": s.Manager.Count()}, nil
}

// mGetSystemInfo 系统概要（平台/核心版本/浏览器/目录建议）。
func (s *Server) mGetSystemInfo(_ map[string]any) (any, error) {
	info := usb.PlatformSummary()
	cfg := s.load()
	chrome := ""
	if c, err := detectChrome(config.StrField(config.RenderCfg(cfg), "chrome")); err == nil {
		chrome = c
	}
	info["chrome"] = chrome
	info["state_dir"] = state.StateDir()
	info["config_path"] = config.ConfigPath()
	oe := config.MapField(cfg, "order_export")
	info["suggested_outdirs"] = usb.SuggestedOutdirs(
		config.StrField(config.MapField(cfg, "usb"), "label"),
		config.StrField(oe, "default_outdir"),
		exeDir(), s.UsbOpts)
	return info, nil
}

// mGetConfig 脱敏配置视图。
func (s *Server) mGetConfig(_ map[string]any) (any, error) {
	return config.MaskedView(s.load()), nil
}

// mSaveConfig 合并写回配置；密码留空 = 不修改。
func (s *Server) mSaveConfig(params map[string]any) (any, error) {
	updates := map[string]any{}
	for k, v := range params {
		updates[k] = v
	}
	if sso, ok := updates["sso"].(map[string]any); ok {
		if pw, _ := sso["password"].(string); pw == "" {
			filtered := map[string]any{}
			for k, v := range sso {
				if k != "password" {
					filtered[k] = v
				}
			}
			updates["sso"] = filtered
		}
	}
	if _, err := config.Save(updates); err != nil {
		return nil, err
	}
	return config.MaskedView(s.load()), nil
}

// mGetApps 应用清单（元数据 + 能力开关）。
func (s *Server) mGetApps(_ map[string]any) (any, error) {
	out := []map[string]any{}
	for _, a := range apps.All() {
		_, hasStatus := a.Backend.(apps.StatusBackend)
		_, hasPreview := a.Backend.(apps.PreviewBackend)
		_, hasBrowser := a.Backend.(apps.BrowserBackend)
		out = append(out, map[string]any{
			"id":          a.ID,
			"name":        a.Name,
			"badge":       a.Badge,
			"desc":        a.Desc,
			"order":       a.Order,
			"has_status":  hasStatus,
			"has_preview": hasPreview,
			"has_browser": hasBrowser,
		})
	}
	return out, nil
}

// mGetAppStatus 应用状态卡。
func (s *Server) mGetAppStatus(params map[string]any) (any, error) {
	a := apps.Get(strParam(params, "app_id"))
	sb, _ := aBackend(a).(apps.StatusBackend)
	if sb == nil {
		return map[string]any{}, nil
	}
	return sb.Status(s.load())
}

// mLaunchAppBrowser 启动受控浏览器。
func (s *Server) mLaunchAppBrowser(params map[string]any) (any, error) {
	a := apps.Get(strParam(params, "app_id"))
	bb, _ := aBackend(a).(apps.BrowserBackend)
	if bb == nil {
		return nil, fmt.Errorf("该应用不支持受控浏览器")
	}
	return bb.LaunchBrowser(s.load())
}

// mPreview 输入预览。
func (s *Server) mPreview(params map[string]any) (any, error) {
	a := apps.Get(strParam(params, "app_id"))
	pb, _ := aBackend(a).(apps.PreviewBackend)
	if pb == nil {
		return nil, fmt.Errorf("该应用不支持预览")
	}
	return pb.Preview(mapParam(params, "params"))
}

// aBackend 取应用后端（app 可能为 nil）。
func aBackend(a *apps.App) apps.Backend {
	if a == nil {
		return nil
	}
	return a.Backend
}

// mRunJob 启动后台任务（app_id 前缀 "selftest:" 为环境自检）。
func (s *Server) mRunJob(params map[string]any) (any, error) {
	appID := strParam(params, "app_id")
	if strings.HasPrefix(appID, "selftest:") {
		return s.runSelftestJob(strings.TrimPrefix(appID, "selftest:"), params)
	}
	a := apps.Get(appID)
	if a == nil || a.Backend == nil {
		return nil, fmt.Errorf("应用不存在: %s", appID)
	}
	if s.Manager.HasActive(appID) {
		return nil, fmt.Errorf("该应用已有任务在运行")
	}
	j := s.Manager.Create(appID, a.Name)
	s.wireEvents(j)
	// wg 跟踪整个 runner 生命周期（OnDone 在 finalize 之后触发，保证事件推完才 Wait 返回）
	s.wg.Add(1)
	baseDone := j.OnDone
	j.OnDone = func(jj *job.Job) {
		if baseDone != nil {
			baseDone(jj)
		}
		s.wg.Done()
	}
	backend := a.Backend
	s.Manager.Start(j, func(j *job.Job) (map[string]any, error) {
		return backend.Run(j, mapParam(params, "params"))
	})
	return map[string]any{"job_id": j.ID}, nil
}

// wireEvents job → RPC 事件桥（job.log / job.progress / job.done）。
func (s *Server) wireEvents(j *job.Job) {
	id := j.ID
	j.OnLog = func(line string) {
		s.emit("job.log", map[string]any{"job_id": id, "line": line})
	}
	j.OnProgress = func(current, total int, label string) {
		s.emit("job.progress", map[string]any{
			"job_id": id, "current": current, "total": total, "label": label,
		})
	}
	j.OnDone = func(jj *job.Job) {
		s.emit("job.done", map[string]any{"job_id": jj.ID, "job": jj.ToDict()})
	}
}

// startSelftestJob 自检任务（同 jobs 事件模型）。
func (s *Server) runSelftestJob(kind string, params map[string]any) (any, error) {
	fn := map[string]selftestFn{
		"render": runRenderSelftest,
		"sso":    runSSOSelftest,
		"cdp":    runCDPSelftest,
	}[kind]
	if fn == nil {
		return nil, fmt.Errorf("未知自检类型: %s", kind)
	}
	j := s.Manager.Create("selftest", "环境自检·"+kind)
	s.wireEvents(j)
	inner := mapParam(params, "params")
	s.wg.Add(1)
	baseDone := j.OnDone
	j.OnDone = func(jj *job.Job) {
		if baseDone != nil {
			baseDone(jj)
		}
		s.wg.Done()
	}
	s.Manager.Start(j, func(j2 *job.Job) (map[string]any, error) {
		return fn(j2, inner)
	})
	return map[string]any{"job_id": j.ID}, nil
}

// mMaybeRunDaily 客户端驻留调度入口：客户端每分钟轮询调用；到点且 schedule.driver=app 才启动。
// 与 OS 定时器经 schedule.DueCheck 的 driver 门闸互斥，不会双跑。
// 树障/交跨统计表同步的夜间兜底复用同一唤醒（同 driver 门闸、
// 自有戳名 tree_sync_nightly / crossing_sync_nightly）。
func (s *Server) mMaybeRunDaily(_ map[string]any) (any, error) {
	cfg := s.load()
	treeNightly := s.maybeRunTableSyncNightly(cfg, "tree_sync")
	crossingNightly := s.maybeRunTableSyncNightly(cfg, "crossing_sync")
	due, reason, today := schedule.DueCheck(cfg, s.now(), "app", "", nil)
	if !due {
		return map[string]any{
			"started":               false,
			"reason":                reason,
			"tree_sync_nightly":     treeNightly,
			"crossing_sync_nightly": crossingNightly,
		}, nil
	}
	schedule.MarkAttempt(today, "") // 与 OS 定时器同口径：拉开失败重试间隔
	r, err := s.mRunJob(map[string]any{"app_id": "daily_sync", "params": map[string]any{"date": ""}})
	if err != nil {
		return map[string]any{
			"started":               false,
			"reason":                err.Error(),
			"tree_sync_nightly":     treeNightly,
			"crossing_sync_nightly": crossingNightly,
		}, nil
	}
	out := map[string]any{
		"started":               true,
		"reason":                reason,
		"tree_sync_nightly":     treeNightly,
		"crossing_sync_nightly": crossingNightly,
	}
	for k, v := range r.(map[string]any) {
		out[k] = v
	}
	return out, nil
}

// maybeRunTableSyncNightly 统计表同步夜间兜底（driver=app 驻留调度侧，与 OS 定时器的
// --nightly --if-due 同口径）：启用条件=已配置 local_path；时刻=nightly_time；今日已成功则跳过。
func (s *Server) maybeRunTableSyncNightly(cfg config.Dict, appID string) map[string]any {
	ts := config.MapField(cfg, appID)
	if strings.TrimSpace(config.StrField(ts, "local_path")) == "" {
		return map[string]any{
			"started": false,
			"reason":  fmt.Sprintf("未配置内网表路径（%s.local_path）", appID),
		}
	}
	sch := schedule.Schedule{
		Enabled: true,
		Time:    config.StrFieldDefault(ts, "nightly_time", "23:55"),
		Driver:  config.StrFieldDefault(config.MapField(cfg, "schedule"), "driver", "os"),
	}
	stamp := appID + "_nightly"
	due, reason, today := schedule.DueCheck(cfg, s.now(), "app", stamp, &sch)
	if !due {
		return map[string]any{"started": false, "reason": reason}
	}
	schedule.MarkAttempt(today, stamp)
	r, err := s.mRunJob(map[string]any{"app_id": appID, "params": map[string]any{"nightly": true}})
	if err != nil {
		return map[string]any{"started": false, "reason": err.Error()}
	}
	out := map[string]any{"started": true, "reason": reason}
	for k, v := range r.(map[string]any) {
		out[k] = v
	}
	return out
}

// mMaybeRunTreeSyncTick 客户端驻留调度入口（树障统计表同步，每分钟轮询）：driver=app 时由壳每 60s 调用。
// 不做 due-check——U盘不在时后端静默返回秒退；有指令立即执行。
// 与 OS 定时器的 `tree-sync --tick` 经 driver 门闸互斥，不会双跑。
func (s *Server) mMaybeRunTreeSyncTick(_ map[string]any) (any, error) {
	return s.maybeRunTableSyncTick("tree_sync"), nil
}

// mMaybeRunCrossingSyncTick 客户端驻留调度入口（交跨统计表同步，每分钟轮询）；同 tree_sync tick。
func (s *Server) mMaybeRunCrossingSyncTick(_ map[string]any) (any, error) {
	return s.maybeRunTableSyncTick("crossing_sync"), nil
}

func (s *Server) maybeRunTableSyncTick(appID string) map[string]any {
	cfg := s.load()
	if config.StrFieldDefault(config.MapField(cfg, "schedule"), "driver", "os") != "app" {
		return map[string]any{"started": false, "reason": "由系统定时器调度"}
	}
	if strings.TrimSpace(config.StrField(config.MapField(cfg, appID), "local_path")) == "" {
		return map[string]any{
			"started": false,
			"reason":  fmt.Sprintf("未配置内网表路径（%s.local_path）", appID),
		}
	}
	r, err := s.mRunJob(map[string]any{"app_id": appID, "params": map[string]any{"tick": true}})
	if err != nil {
		return map[string]any{"started": false, "reason": err.Error()}
	}
	out := map[string]any{"started": true}
	for k, v := range r.(map[string]any) {
		out[k] = v
	}
	return out
}

// mCancelJob 请求取消任务。
func (s *Server) mCancelJob(params map[string]any) (any, error) {
	j := s.Manager.Get(strParam(params, "job_id"))
	if j == nil {
		return nil, fmt.Errorf("任务不存在")
	}
	j.Cancel()
	return map[string]any{"ok": true}, nil
}

// mGetJob 任务摘要。
func (s *Server) mGetJob(params map[string]any) (any, error) {
	j := s.Manager.Get(strParam(params, "job_id"))
	if j == nil {
		return nil, nil
	}
	return j.ToDict(), nil
}

// mGetHistory 应用运行历史（最近 10 条）。
func (s *Server) mGetHistory(params map[string]any) (any, error) {
	runs := state.RecentRuns(strParam(params, "app_id"), 10)
	if runs == nil {
		return []map[string]any{}, nil
	}
	return runs, nil
}

// mOpenPath 用系统文件管理器打开目录/文件。
func (s *Server) mOpenPath(params map[string]any) (any, error) {
	p := strParam(params, "path")
	if p == "" {
		return nil, fmt.Errorf("路径不存在")
	}
	if ok := fileExists(p); !ok {
		return nil, fmt.Errorf("路径不存在")
	}
	return map[string]any{"ok": usb.OpenPath(p)}, nil
}

var jobIDRe = regexp.MustCompile(`^\d{14}-\d{2}$`)

// mGetJobLog 读任务完整日志（logs/<job_id>.log 全文）；job_id 正则校验防路径穿越。
func (s *Server) mGetJobLog(params map[string]any) (any, error) {
	jobID := strParam(params, "job_id")
	if !jobIDRe.MatchString(jobID) {
		return nil, fmt.Errorf("参数错误")
	}
	data, err := readFileString(fmt.Sprintf("%s/%s.log", state.LogsDir(), jobID))
	if err != nil {
		return nil, fmt.Errorf("日志不存在或已被清除")
	}
	return map[string]any{"text": data}, nil
}

// mClearLogs 一键清除全部任务日志与任务档案（占用中的文件跳过并计数）。
func (s *Server) mClearLogs(_ map[string]any) (any, error) {
	return state.ClearLogs(), nil
}

// mDeleteHistory 删除应用执行记录：传 job_id 删单条，不传清空该应用（连带任务日志/档案）。
func (s *Server) mDeleteHistory(params map[string]any) (any, error) {
	appID := strParam(params, "app_id")
	jobID := strParam(params, "job_id")
	var ids []string
	if jobID != "" {
		if !jobIDRe.MatchString(jobID) {
			return nil, fmt.Errorf("参数错误")
		}
		ids = []string{jobID}
	}
	res, err := state.DeleteHistoryRecords(appID, ids)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// mGetLogStats 日志目录统计（logs/ + jobs/）：文件数与总字节。
func (s *Server) mGetLogStats(_ map[string]any) (any, error) {
	files, total := 0, int64(0)
	for _, d := range []string{state.LogsDir(), state.JobsDir()} {
		n, b := dirStat(d)
		files += n
		total += b
	}
	return map[string]any{"dir": state.LogsDir(), "files": files, "bytes": total}, nil
}

// ---------------------------------------------------------------- 主循环

// Serve 逐行读请求、写响应；stdin 关闭后等运行中的任务把事件推完再返回
// （Python serve 尾部 join job 线程同口径）。
func (s *Server) Serve(r io.Reader) {
	methods := s.methods()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		reqID := any(nil)
		if err := json.Unmarshal([]byte(line), &req); err == nil {
			reqID = req.ID
		}
		fn := methods[req.Method]
		if fn == nil {
			if reqID != nil {
				s.send(map[string]any{"id": reqID, "ok": false, "error": "未知方法: " + req.Method})
			}
			continue
		}
		params := req.Params
		if params == nil {
			params = map[string]any{}
		}
		result, err := fn(params)
		if reqID != nil {
			if err != nil {
				s.send(map[string]any{"id": reqID, "ok": false, "error": err.Error()})
			} else {
				s.send(map[string]any{"id": reqID, "ok": true, "result": result})
			}
		}
	}
	// stdin 关闭（如管道一次性喂请求）后，等运行中的任务把事件推完再退出
	s.wg.Wait()
}

// ---------------------------------------------------------------- 工具

func fileExists(p string) bool {
	f, err := openFile(p)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
