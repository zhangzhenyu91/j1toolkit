package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/apps"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/quizsearch"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
)

// 本文件：CoreBackend 的题库（QuizAPI）与学堂刷课（CourseAPI）强类型通道。
// 题库侧直连 quizsearch.BankStore（挂接扫描服务后经 Scanner 以同步在途匹配索引）；
// 课程侧底层仍是任务通道 + state/course-autoplay/checkpoints.json（course 业务实现后续接入）。

// ---------------------------------------------------------------- QuizAPI

func (b *CoreBackend) quizStore() (*quizsearch.BankStore, error) {
	if b.quiz == nil {
		return nil, fmt.Errorf("题库目录不可用")
	}
	return b.quiz, nil
}

// bankOps 题库操作集（挂接扫描服务时经 Scanner，保持在途匹配索引同步）。
type bankOps interface {
	BankImportFile(name, path string) (quizsearch.ImportResult, *quizsearch.State, error)
	BankRemove(id string) (*quizsearch.State, error)
	BankRename(id, name string) (*quizsearch.State, error)
	BankSetEnabled(id string, enabled bool) (*quizsearch.State, error)
}

// QuizScanner 扫描服务依赖面（*quizsearch.Scanner 实现；测试注入 fake）：
// 题库操作 + 扫描控制 + 事件流 + 生命周期，CoreBackend 装配并持有。
type QuizScanner interface {
	bankOps
	Start() (*quizsearch.State, error)
	Stop() *quizsearch.State
	State() *quizsearch.State
	SetBoxVisible(id quizsearch.BoxID, visible bool) *quizsearch.State
	ResetBoxes() *quizsearch.State
	SetOptions(o quizsearch.Options) *quizsearch.State
	DebugCapture(dir string) (string, error)
	Selftest() quizsearch.SelftestResult
	Events() <-chan quizsearch.PageEvent
	Dispose()
}

func (b *CoreBackend) ops() bankOps {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.scanner != nil {
		return b.scanner
	}
	return nil
}

// scannerOps 取扫描服务（未挂接/未装配时尝试生产装配），用于扫描控制方法。
func (b *CoreBackend) scannerOps() (QuizScanner, error) {
	return b.scannerOrAssemble()
}

func (b *CoreBackend) QuizBanks(context.Context) ([]QuizBank, error) {
	s, err := b.quizStore()
	if err != nil {
		return nil, err
	}
	list := s.List()
	out := make([]QuizBank, 0, len(list))
	for _, m := range list {
		out = append(out, QuizBank{
			ID:         m.ID,
			Name:       m.Name,
			SourceFile: m.SourceFile,
			Count:      m.Count,
			ImportedAt: parseImportedAt(m.ImportedAt),
			Enabled:    m.Enabled,
		})
	}
	return out, nil
}

// parseImportedAt 题库导入时间（BankStore 落盘为 UTC "2006-01-02T15:04:05.000Z"）。
func parseImportedAt(s string) time.Time {
	for _, layout := range []string{"2006-01-02T15:04:05.000Z", time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func (b *CoreBackend) QuizImportBank(_ context.Context, path string) (QuizBank, error) {
	s, err := b.quizStore()
	if err != nil {
		return QuizBank{}, err
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	var res quizsearch.ImportResult
	if ops := b.ops(); ops != nil {
		res, _, err = ops.BankImportFile(name, path)
	} else {
		res, err = s.ImportFile(name, path)
	}
	if err != nil {
		return QuizBank{}, err
	}
	return QuizBank{
		ID:         res.ID,
		Name:       res.Name,
		SourceFile: path,
		Count:      res.Count,
		ImportedAt: time.Now(),
		Enabled:    res.Enabled,
	}, nil
}

func (b *CoreBackend) QuizSetBankEnabled(_ context.Context, bankID string, enabled bool) error {
	s, err := b.quizStore()
	if err != nil {
		return err
	}
	if ops := b.ops(); ops != nil {
		_, err = ops.BankSetEnabled(bankID, enabled)
		return err
	}
	return s.SetEnabled(bankID, enabled)
}

func (b *CoreBackend) QuizRenameBank(_ context.Context, bankID string, name string) error {
	s, err := b.quizStore()
	if err != nil {
		return err
	}
	if ops := b.ops(); ops != nil {
		_, err = ops.BankRename(bankID, name)
		return err
	}
	return s.Rename(bankID, name)
}

func (b *CoreBackend) QuizRemoveBank(_ context.Context, bankID string) error {
	s, err := b.quizStore()
	if err != nil {
		return err
	}
	if ops := b.ops(); ops != nil {
		_, err = ops.BankRemove(bankID)
		return err
	}
	return s.Remove(bankID)
}

// QuizMatch 题干文本匹配（全部启用库合并，覆盖率阈值默认 0.6）。
func (b *CoreBackend) QuizMatch(_ context.Context, text string) (QuizMatch, error) {
	s, err := b.quizStore()
	if err != nil {
		return QuizMatch{}, err
	}
	idx := quizsearch.BuildIndex(s.AllEnabledQuestions())
	hit := quizsearch.FindBest(idx, text, quizsearch.DefaultThreshold)
	if hit == nil {
		return QuizMatch{Matched: false}, nil
	}
	out := QuizMatch{Matched: hit.Question != nil, Score: hit.Score}
	if hit.Question != nil {
		q := toUIQuestion(*hit.Question)
		out.Question = q
	}
	if hit.Near != nil {
		q := toUIQuestion(*hit.Near)
		out.Near = &q
	}
	return out, nil
}

func (b *CoreBackend) QuizSaveTemplate(_ context.Context, path string) error {
	return quizsearch.WriteTemplate(path)
}

// toUIQuestion 题目模型转换：选项位置列表 → A~F 字母键；题型转中文文案。
func toUIQuestion(q quizsearch.Question) QuizQuestion {
	opts := map[string]string{}
	const letters = "ABCDEF"
	for i, o := range q.Options {
		if i >= len(letters) {
			break
		}
		opts[string(letters[i])] = o
	}
	return QuizQuestion{
		Content:  q.Content,
		Answer:   strings.ToUpper(q.Answer),
		Options:  opts,
		Type:     questionTypeCN(q.Type),
		Analysis: q.Analysis,
		BankName: q.BankName,
	}
}

func questionTypeCN(t string) string {
	switch t {
	case "single":
		return "单选题"
	case "multiple":
		return "多选题"
	case "judge":
		return "判断题"
	default:
		return t
	}
}

// ---------------------------------------------------------------- 扫描控制

// parseBoxID 扫描框编号校验（"A"/"B"，大小写容错）。
func parseBoxID(box string) (quizsearch.BoxID, error) {
	id := quizsearch.BoxID(strings.ToUpper(strings.TrimSpace(box)))
	if id != quizsearch.BoxA && id != quizsearch.BoxB {
		return "", fmt.Errorf("扫描框编号只能是 A/B：%q", box)
	}
	return id, nil
}

// toUIScanState 扫描服务状态 → 界面视图。
func toUIScanState(st *quizsearch.State) QuizScanState {
	out := QuizScanState{
		Scanning:     st.Scanning,
		EngineReady:  st.EngineReady,
		EngineError:  st.EngineError,
		CaptureError: st.CaptureError,
		SessionType:  st.SessionType,
		OcrMs:        st.OcrMs,
		Interval:     st.Settings.Interval,
		Threshold:    st.Settings.Threshold,
		Opacity:      st.Settings.Opacity,
		BoxVisible: map[string]bool{
			string(quizsearch.BoxA): st.Settings.Boxes.A.Visible,
			string(quizsearch.BoxB): st.Settings.Boxes.B.Visible,
		},
		QuestionCount: st.QuestionCount,
		BoxDebug:      map[string]*QuizBoxDebug{},
	}
	for k, v := range st.BoxDebug {
		if v == nil {
			out.BoxDebug[k] = nil
			continue
		}
		out.BoxDebug[k] = &QuizBoxDebug{Matched: v.Matched, Snippet: v.Snippet}
	}
	return out
}

// toUIQuizEvent 扫描页事件 → 界面事件（scan/engine/boxHidden 原样透传）。
func toUIQuizEvent(ev quizsearch.PageEvent) QuizEvent {
	return QuizEvent{Type: ev.Type, Scanning: ev.Scanning, Ready: ev.Ready, Error: ev.Error, Box: ev.Box}
}

func (b *CoreBackend) QuizScanState(context.Context) (QuizScanState, error) {
	s, err := b.scannerOps()
	if err != nil {
		return QuizScanState{}, err
	}
	return toUIScanState(s.State()), nil
}

func (b *CoreBackend) QuizScanStart(context.Context) (QuizScanState, error) {
	s, err := b.scannerOps()
	if err != nil {
		return QuizScanState{}, err
	}
	st, err := s.Start()
	if err != nil {
		return QuizScanState{}, err
	}
	return toUIScanState(st), nil
}

func (b *CoreBackend) QuizScanStop(context.Context) (QuizScanState, error) {
	s, err := b.scannerOps()
	if err != nil {
		return QuizScanState{}, err
	}
	return toUIScanState(s.Stop()), nil
}

func (b *CoreBackend) QuizSetBoxVisible(_ context.Context, box string, visible bool) (QuizScanState, error) {
	id, err := parseBoxID(box)
	if err != nil {
		return QuizScanState{}, err
	}
	s, err := b.scannerOps()
	if err != nil {
		return QuizScanState{}, err
	}
	return toUIScanState(s.SetBoxVisible(id, visible)), nil
}

func (b *CoreBackend) QuizResetBoxes(context.Context) (QuizScanState, error) {
	s, err := b.scannerOps()
	if err != nil {
		return QuizScanState{}, err
	}
	return toUIScanState(s.ResetBoxes()), nil
}

func (b *CoreBackend) QuizSetOptions(_ context.Context, opt QuizScanOptions) (QuizScanState, error) {
	s, err := b.scannerOps()
	if err != nil {
		return QuizScanState{}, err
	}
	return toUIScanState(s.SetOptions(quizsearch.Options{
		Interval:  opt.Interval,
		Threshold: opt.Threshold,
		Opacity:   opt.Opacity,
	})), nil
}

func (b *CoreBackend) QuizDebugCapture(_ context.Context, dir string) (string, error) {
	s, err := b.scannerOps()
	if err != nil {
		return "", err
	}
	if dir == "" {
		dir = b.quizDir
	}
	return s.DebugCapture(dir)
}

func (b *CoreBackend) QuizSelftest(context.Context) (QuizSelftestResult, error) {
	s, err := b.scannerOps()
	if err != nil {
		return QuizSelftestResult{}, err
	}
	r := s.Selftest()
	return QuizSelftestResult{OK: r.OK, ModelsDir: r.ModelsDir, Missing: r.Missing, Engine: r.Engine}, nil
}

func (b *CoreBackend) QuizSubscribe(context.Context) (QuizEventStream, error) {
	sub := &quizSub{b: b, ch: make(chan QuizEvent, 64)}
	b.mu.Lock()
	b.quizSubs[sub] = struct{}{}
	b.mu.Unlock()
	return sub, nil
}

// ---------------------------------------------------------------- CourseAPI

func (b *CoreBackend) CourseStatus(context.Context) (CourseStatus, error) {
	cfg := b.load()
	m := map[string]any{}
	if a := apps.Get("course_autoplay"); a != nil {
		if sb, ok := a.Backend.(apps.StatusBackend); ok {
			var err error
			m, err = sb.Status(cfg)
			if err != nil {
				return CourseStatus{}, err
			}
		}
	}
	st := CourseStatusFrom(m)
	ba := config.MapField(cfg, "browser_automation")
	if st.CDPURL == "" {
		st.CDPURL = config.StrFieldDefault(ba, "cdp_url", "http://127.0.0.1:9222")
	}
	if st.StartURL == "" {
		st.StartURL = config.StrField(ba, "start_url")
	}
	return st, nil
}

// CourseOpenBrowser 保存启动网址并启动/复用受控浏览器（course_autoplay 应用 BrowserBackend）。
func (b *CoreBackend) CourseOpenBrowser(ctx context.Context, startURL string) error {
	if strings.TrimSpace(startURL) != "" {
		if _, err := config.Save(config.Dict{
			"browser_automation": config.Dict{"start_url": startURL},
		}); err != nil {
			return err
		}
	}
	return b.LaunchAppBrowser(ctx, "course_autoplay")
}

// CourseStart 开始学习（scanOnly=仅扫描不进入课程），返回 job_id；底层即 runJob(course_autoplay)。
func (b *CoreBackend) CourseStart(ctx context.Context, opt CourseOptions) (string, error) {
	return b.RunJob(ctx, "course_autoplay", map[string]any{
		"scan_only":    opt.ScanOnly,
		"max_attempts": opt.MaxAttempts,
		"timeout":      opt.Timeout,
	})
}

// CoursePause 暂停学习（等价 CancelJob：保留浏览器与登录态，可续刷）。
func (b *CoreBackend) CoursePause(ctx context.Context, jobID string) error {
	return b.CancelJob(ctx, jobID)
}

// CourseCheckpoints 续刷检查点（state/course-autoplay/checkpoints.json，按培训班/列表独立持久化）。
// 兼容 Python toolkit 的 checkpoints 形状（resume_page/processed/last_errors/skipped_ids）与
// Go 侧显式 train_id/list_name 字段；列表键无显式名单时以键名兜底展示。
func (b *CoreBackend) CourseCheckpoints(context.Context) ([]CourseCheckpoint, error) {
	path := filepath.Join(b.store.RootDir(), "course-autoplay", "checkpoints.json")
	m := state.ReadJSONMap(path)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]CourseCheckpoint, 0, len(keys))
	for _, k := range keys {
		cp := asMap(m[k])
		if cp == nil {
			continue
		}
		c := CourseCheckpoint{
			TrainID:   asString(cp["train_id"]),
			ListName:  asString(cp["list_name"]),
			Page:      asInt(cp["resume_page"]),
			Processed: asInt(cp["processed"]),
			UpdatedAt: asString(cp["updated_at"]),
		}
		if errMap, ok := cp["last_errors"].(map[string]any); ok {
			c.Failed = len(errMap)
		}
		if skip, ok := cp["skipped_ids"].([]any); ok {
			c.Skipped = len(skip)
		}
		if c.ListName == "" {
			c.ListName = k // 旧 checkpoints.json 以列表键命名
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out, nil
}
