package rpc

import (
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/apps"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/job"
)

// 三类环境自检与桌面壳共用同一实现，避免浏览器运行时探测与结果契约分叉。
func runRenderSelftest(j *job.Job, params map[string]any) (map[string]any, error) {
	return apps.RunSelftest("render", j, params)
}

func runCDPSelftest(j *job.Job, params map[string]any) (map[string]any, error) {
	return apps.RunSelftest("cdp", j, params)
}

func runSSOSelftest(j *job.Job, params map[string]any) (map[string]any, error) {
	return apps.RunSelftest("sso", j, params)
}
