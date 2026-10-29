package appshell

import (
	"reflect"
	"testing"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

func TestMergeHistorySortsAllRecords(t *testing.T) {
	tasks := homeTaskList()
	records := map[string][]ui.HistoryRecord{
		"pcd_export": {
			{JobID: "job-c", Time: "2026-10-29 09:00:00", Status: ui.JobSuccess},
			{JobID: "job-old", Time: "2026-10-27 09:00:00", Status: ui.JobFailed, Error: "导出失败"},
		},
		"course_autoplay": {
			{JobID: "job-z", Time: "2026-10-29 11:00:00", Status: ui.JobSuccess},
		},
		"quiz_search": {
			{JobID: "job-quiz", Time: "2026-10-28 09:00:00", Status: ui.JobSuccess},
		},
		"daily_sync": {
			{JobID: "job-y", Time: "2026-10-29 11:00:00", Status: ui.JobSuccess},
		},
		"tree_sync": {
			{JobID: "job-new", Time: "2026-10-29 12:00:00", Status: ui.JobSuccess},
		},
		"crossing_sync": {
			{JobID: "job-crossing", Time: "2026-10-29 08:00:00", Status: ui.JobCancelled},
		},
	}

	merged := mergeHistory(tasks, records)
	got := make([]string, 0, len(merged))
	for _, entry := range merged {
		got = append(got, entry.rec.JobID)
	}
	want := []string{"job-new", "job-z", "job-y", "job-c", "job-crossing", "job-quiz", "job-old"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("合并排序不符：got=%v want=%v", got, want)
	}
	if len(merged) != 7 {
		t.Fatalf("统一历史页不应按首页六条截断：%d", len(merged))
	}
	if merged[0].appID != "tree_sync" || merged[0].appName != "树障同步" {
		t.Fatalf("工具元数据未保留：%+v", merged[0])
	}
	if summary := merged[len(merged)-1].summary; summary != friendlyTaskError("导出失败") {
		t.Fatalf("失败摘要未沿用业务友好口径：%q", summary)
	}
}

func TestMergeHistoryStableWhenTimeAndJobIDEqual(t *testing.T) {
	tasks := []homeTask{{id: "first", name: "第一工具"}, {id: "second", name: "第二工具"}}
	rec := ui.HistoryRecord{JobID: "same-job", Time: "2026-10-29 10:00:00", Status: ui.JobSuccess}
	merged := mergeHistory(tasks, map[string][]ui.HistoryRecord{
		"first":  {rec},
		"second": {rec},
	})
	if len(merged) != 2 || merged[0].appID != "first" || merged[1].appID != "second" {
		t.Fatalf("完全相同的排序键应保持原顺序：%+v", merged)
	}
}

func TestFilterHistory(t *testing.T) {
	entries := []historyEntry{
		{
			appID: "pcd_export", appName: "轨迹派车单导出", summary: "导出完成：Alpha",
			rec: ui.HistoryRecord{JobID: "JOB-100", Time: "2026-10-29 08:00:00", Status: ui.JobSuccess},
		},
		{
			appID: "daily_sync", appName: "派车同步", summary: "导出完成：6 条",
			rec: ui.HistoryRecord{JobID: "job-101", Time: "2026-10-29 09:00:00", Status: ui.JobFailed, Error: "SSO 登录失败"},
		},
		{
			appID: "course_autoplay", appName: "学堂刷课", summary: "已暂停，可回到页面继续",
			rec: ui.HistoryRecord{JobID: "job-102", Time: "2026-10-28 09:00:00", Status: ui.JobRunning, Result: map[string]any{"paused": true}},
		},
		{
			appID: "tree_sync", appName: "树障同步", summary: "同步完成",
			rec: ui.HistoryRecord{JobID: "job-103", Time: "2026-10-27 09:00:00", Status: ui.JobCancelled},
		},
	}

	tests := []struct {
		name   string
		filter historyFilter
		want   []string
	}{
		{name: "默认保留全部", want: []string{"JOB-100", "job-101", "job-102", "job-103"}},
		{name: "工具", filter: historyFilter{appID: "pcd_export"}, want: []string{"JOB-100"}},
		{name: "状态", filter: historyFilter{status: string(ui.JobFailed)}, want: []string{"job-101"}},
		{name: "暂停优先于任务状态", filter: historyFilter{status: "paused"}, want: []string{"job-102"}},
		{name: "关键词忽略大小写", filter: historyFilter{keyword: "alpha"}, want: []string{"JOB-100"}},
		{name: "关键词搜索错误", filter: historyFilter{keyword: "sso 登录"}, want: []string{"job-101"}},
		{name: "关键词搜索任务号", filter: historyFilter{keyword: "JOB-102"}, want: []string{"job-102"}},
		{name: "日期", filter: historyFilter{date: "2026-10-29"}, want: []string{"JOB-100", "job-101"}},
		{
			name: "组合条件",
			filter: historyFilter{
				appID: "daily_sync", status: string(ui.JobFailed), keyword: "6 条", date: "2026-10-29",
			},
			want: []string{"job-101"},
		},
		{name: "无匹配", filter: historyFilter{appID: "quiz_search"}, want: []string{}},
		{name: "非法日期不匹配也不猜日期", filter: historyFilter{date: "2026-02-30"}, want: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filtered := filterHistory(entries, tt.filter)
			got := make([]string, 0, len(filtered))
			for _, entry := range filtered {
				got = append(got, entry.rec.JobID)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("筛选结果不符：got=%v want=%v", got, tt.want)
			}
		})
	}
	if len(entries) != 4 || entries[0].rec.JobID != "JOB-100" {
		t.Fatalf("筛选不应修改已加载数据：%+v", entries)
	}
}

func TestValidHistoryDate(t *testing.T) {
	tests := []struct {
		date string
		want bool
	}{
		{date: "", want: true},
		{date: "2026-10-29", want: true},
		{date: "2028-02-29", want: true},
		{date: "2026-02-29", want: false},
		{date: "2026-13-01", want: false},
		{date: "2026-1-01", want: false},
		{date: "20261029", want: false},
	}
	for _, tt := range tests {
		if got := validHistoryDate(tt.date); got != tt.want {
			t.Errorf("validHistoryDate(%q)=%v，期望 %v", tt.date, got, tt.want)
		}
	}
}
