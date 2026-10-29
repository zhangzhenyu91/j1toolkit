package ui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/apps"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/job"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
)

// 本文件：CoreBackend 的任务通道（TaskAPI）与执行记录（HistoryAPI）——
// runJob 契约（启动即执行、事件经 EventAPI 推送）与 toolkit RPC 同口径。

var coreJobIDRe = regexp.MustCompile(`^\d{14}-\d{2}$`)

// RunJob 启动任务；"selftest:" 前缀为环境自检任务（设置页入口，与 RPC runJob 同约定）。
func (b *CoreBackend) RunJob(_ context.Context, appID string, params map[string]any) (string, error) {
	if appID == "" {
		return "", errors.New("app_id 不能为空")
	}
	if strings.HasPrefix(appID, "selftest:") {
		kind := strings.TrimPrefix(appID, "selftest:")
		return b.startJob("selftest", "环境自检·"+kind, func(j *job.Job) (map[string]any, error) {
			return apps.RunSelftest(kind, j, params)
		})
	}
	a := apps.Get(appID)
	if a == nil || a.Backend == nil {
		return "", fmt.Errorf("应用不存在: %s", appID)
	}
	if b.mgr.HasActive(appID) {
		return "", errors.New("该应用已有任务在运行")
	}
	backend := a.Backend
	return b.startJob(appID, a.Name, func(j *job.Job) (map[string]any, error) {
		return backend.Run(j, params)
	})
}

// startJob 创建任务、接好事件桥并后台执行。
func (b *CoreBackend) startJob(appID, title string, fn func(*job.Job) (map[string]any, error)) (string, error) {
	j := b.mgr.Create(appID, title)
	b.wireJob(j)
	b.mgr.Start(j, fn)
	return j.ID, nil
}

// wireJob job 回调 → EventAPI 事件桥（job.log / job.progress / job.done）。
func (b *CoreBackend) wireJob(j *job.Job) {
	id := j.ID
	j.OnLog = func(line string) {
		b.emit(Event{Kind: EventJobLog, JobID: id, Line: line})
	}
	j.OnProgress = func(current, total int, label string) {
		p := JobProgress{JobID: id, Current: current, Total: total, Label: label}
		b.emit(Event{Kind: EventJobProgress, JobID: id, Progress: &p})
	}
	j.OnDone = func(jj *job.Job) {
		uj := jobToUI(jj)
		b.emit(Event{Kind: EventJobDone, JobID: jj.ID, Job: &uj})
	}
}

// jobToUI 任务档案摘要 → 界面任务模型。
func jobToUI(j *job.Job) Job {
	d := j.ToDict()
	uj := Job{
		ID:     asString(d["id"]),
		AppID:  asString(d["app_id"]),
		Status: JobStatus(asString(d["status"])),
		Error:  asString(d["error"]),
		Result: asMap(d["result"]),
	}
	uj.StartedAt = parseJobTime(asString(d["created_at"]))
	uj.FinishedAt = parseJobTime(asString(d["finished_at"]))
	return uj
}

func parseJobTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (b *CoreBackend) CancelJob(_ context.Context, jobID string) error {
	j := b.mgr.Get(jobID)
	if j == nil {
		return fmt.Errorf("任务不存在：%s", jobID)
	}
	if st, _ := j.ToDict()["status"].(string); job.TerminalStatus[st] {
		return nil
	}
	j.Cancel()
	return nil
}

func (b *CoreBackend) GetJob(_ context.Context, jobID string) (Job, error) {
	if j := b.mgr.Get(jobID); j != nil {
		return jobToUI(j), nil
	}
	// 内存回收后回读任务档案（state/jobs/<id>.json）
	if !coreJobIDRe.MatchString(jobID) {
		return Job{}, fmt.Errorf("任务不存在：%s", jobID)
	}
	m := state.ReadJSONMap(filepath.Join(b.store.JobsDir(), jobID+".json"))
	if m == nil {
		return Job{}, fmt.Errorf("任务不存在：%s", jobID)
	}
	uj := Job{
		ID:     asString(m["id"]),
		AppID:  asString(m["app_id"]),
		Status: JobStatus(asString(m["status"])),
		Error:  asString(m["error"]),
		Result: asMap(m["result"]),
	}
	uj.StartedAt = parseJobTime(asString(m["created_at"]))
	uj.FinishedAt = parseJobTime(asString(m["finished_at"]))
	return uj, nil
}

func (b *CoreBackend) JobLog(_ context.Context, jobID string) (string, error) {
	if !coreJobIDRe.MatchString(jobID) {
		return "", errors.New("参数错误")
	}
	data, err := readFileText(filepath.Join(b.store.LogsDir(), jobID+".log"))
	if err != nil {
		return "", errors.New("日志不存在或已被清除")
	}
	return data, nil
}

func (b *CoreBackend) Preview(_ context.Context, appID string, params map[string]any) (map[string]any, error) {
	a := apps.Get(appID)
	if a == nil {
		return map[string]any{}, nil
	}
	pb, ok := a.Backend.(apps.PreviewBackend)
	if !ok {
		return map[string]any{}, nil
	}
	return pb.Preview(params)
}

// ---------------------------------------------------------------- HistoryAPI

func (b *CoreBackend) History(_ context.Context, appID string) ([]HistoryRecord, error) {
	runs := b.store.RecentRuns(appID, 200)
	out := make([]HistoryRecord, 0, len(runs))
	for _, r := range runs {
		rec := HistoryRecord{
			JobID:  asString(r["job_id"]),
			Time:   asString(r["time"]),
			Status: JobStatus(asString(r["status"])),
			Error:  asString(r["error"]),
			Result: asMap(r["result"]),
		}
		if d, ok := rec.Result["dir"].(string); ok {
			rec.Dir = d
		}
		out = append(out, rec)
	}
	return out, nil
}

// DeleteHistory 删执行记录：jobID 非空删单条，空清空该应用；连带任务日志/档案。
func (b *CoreBackend) DeleteHistory(_ context.Context, appID string, jobID string) (DeleteResult, error) {
	var ids []string
	if jobID != "" {
		if !coreJobIDRe.MatchString(jobID) {
			return DeleteResult{}, errors.New("参数错误")
		}
		ids = []string{jobID}
	}
	res, err := state.DeleteHistoryRecords(appID, ids)
	if err != nil {
		return DeleteResult{}, err
	}
	return DeleteResult{
		Records: asInt(res["records"]),
		Files:   asInt(res["files"]),
		Skipped: asInt(res["skipped"]),
	}, nil
}

func (b *CoreBackend) ClearLogs(context.Context) (ClearResult, error) {
	res := state.ClearLogs()
	return ClearResult{Removed: asInt(res["removed"]), Skipped: asInt(res["skipped"])}, nil
}

func (b *CoreBackend) LogStats(context.Context) (LogStats, error) {
	files, total := 0, int64(0)
	for _, d := range []string{b.store.LogsDir(), b.store.JobsDir()} {
		n, bytes := dirStat(d)
		files += n
		total += bytes
	}
	return LogStats{Dir: b.store.LogsDir(), Files: files, Bytes: total}, nil
}
