// 后台传输任务回归：客户端请求上下文取消（关页/断网）不得中断已经在跑的任务。
package kvm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestPullTaskSurvivesRequestCancel 入队后取消请求上下文，任务仍须完成推送。
func TestPullTaskSurvivesRequestCancel(t *testing.T) {
	started := make(chan struct{})
	var startOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fileshare/push" {
			http.NotFound(w, r)
			return
		}
		buf := make([]byte, 1)
		if _, err := io.ReadFull(r.Body, buf); err == nil {
			startOnce.Do(func() { close(started) })
		}
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{"pushed":1}`))
	}))
	defer srv.Close()

	env := newTestServer(t, identUser)
	env.fake.on("FROM sys_team", teamRule().cols, teamRule().rows)
	env.withProxy(srv.URL)
	gated := &gatedNetdisk{first: "FIRST", rest: "REST", release: make(chan struct{})}
	env.module.Netdisk = gated

	ctx, cancel := context.WithCancel(context.Background())
	rec := env.doCtx(t, ctx, http.MethodPost, "/api/v1/kvm/devices/11/pull-from-netdisk", map[string]any{
		"space": "my", "paths": []string{"/docs/big.bin"},
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("入队应 202: %s", rec.Body.String())
	}
	var body struct {
		Code int `json:"code"`
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Data.ID == "" {
		t.Fatalf("入队信封不符: %s", rec.Body.String())
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		gated.once.Do(func() { close(gated.release) })
		cancel()
		t.Fatal("任务未开始推送")
	}
	// 模拟用户关闭 Web/小程序：请求上下文取消。
	cancel()
	gated.once.Do(func() { close(gated.release) })

	task := waitPullTask(t, env, 11, body.Data.ID)
	if task["state"] != pullTaskDone {
		t.Fatalf("取消请求后任务仍应完成: %v", task)
	}
	if !gated.closed.Load() {
		t.Fatal("任务完成必须关闭网盘 body")
	}
}

// TestPullTaskDeleteTerminal 终态任务可删除记录；运行中拒绝；删除后列表不再返回。
func TestPullTaskDeleteTerminal(t *testing.T) {
	env := newTestServer(t, identUser)
	mgr := env.module.pullTaskMgr()
	dev := env.platform.devices[0]
	task := mgr.newTask(identUser, dev, deviceDisplayName(dev), "my", []string{"/a"})
	mgr.mu.Lock()
	mgr.tasks[task.id] = task
	mgr.mu.Unlock()

	path := fmt.Sprintf("/api/v1/kvm/devices/%d/pull-tasks/%s", dev.ID, task.id)
	rec := env.do(t, http.MethodDelete, path, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("运行中任务应拒绝删除: %d %s", rec.Code, rec.Body.String())
	}

	task.finish(nil) // 无完成项 → failed 终态；终态记录允许删除
	rec = env.do(t, http.MethodDelete, path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("终态任务应可删除: %d %s", rec.Code, rec.Body.String())
	}

	rec = env.do(t, http.MethodGet, fmt.Sprintf("/api/v1/kvm/devices/%d/pull-tasks", dev.ID), nil)
	code, data := decodeData(t, rec)
	if code != 0 {
		t.Fatalf("任务列表失败: %s", rec.Body.String())
	}
	if items, _ := data["items"].([]any); len(items) != 0 {
		t.Fatalf("删除后列表不应再返回任务: %v", items)
	}
	if rec = env.do(t, http.MethodDelete, path, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("重复删除应 404: %d %s", rec.Code, rec.Body.String())
	}
}

// TestPullTaskListScopedToUser 任务列表只返回当前用户自己的任务。
func TestPullTaskListScopedToUser(t *testing.T) {
	env := newTestServer(t, identUser)
	mgr := env.module.pullTaskMgr()
	dev := env.platform.devices[0]
	t1 := mgr.newTask(identUser, dev, deviceDisplayName(dev), "my", []string{"/a"})
	t2 := mgr.newTask(identAdmin, dev, deviceDisplayName(dev), "my", []string{"/b"})
	mgr.mu.Lock()
	mgr.tasks[t1.id] = t1
	mgr.tasks[t2.id] = t2
	mgr.mu.Unlock()

	list := mgr.list(identUser.ID, dev.ID)
	if len(list) != 1 || list[0].ID != t1.id {
		t.Fatalf("只应返回本人任务: %+v", list)
	}
}
