// Package usbcmd 定义壹匣服务端 ↔ 内网被控机 usb-agent 的 U 盘指令协议。
//
// 盘根文件：
//
//	{app}_cmd.json                 服务端下发（每应用一个，服务端全局会话串行）
//	{app}_result_<id>.json         被控机执行结果（按 id 分文件）
//	usb_agent_status.json          agent 心跳/挂载状态
//
// 服务端只认结果文件里的 id，不再用“指令文件消失/固定等待窗口”推断执行成功。
package usbcmd

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Command 下发的指令文件（同时保留旧 cycle_id/kind/file 字段，滚动升级期旧客户端可读）。
type Command struct {
	Version   int            `json:"version"`
	ID        string         `json:"id"`
	CycleID   string         `json:"cycle_id"`
	App       string         `json:"app"`
	Action    string         `json:"action"`
	Kind      string         `json:"kind"`
	File      string         `json:"file,omitempty"`
	Params    map[string]any `json:"params"`
	IssuedAt  string         `json:"issued_at"`
	ExpiresAt string         `json:"expires_at,omitempty"`
}

// Result 被控机执行结果。
type Result struct {
	Version    int            `json:"version"`
	ID         string         `json:"id"`
	App        string         `json:"app"`
	Action     string         `json:"action"`
	OK         bool           `json:"ok"`
	StartedAt  string         `json:"started_at"`
	FinishedAt string         `json:"finished_at"`
	DurationMS int64          `json:"duration_ms"`
	Error      string         `json:"error"`
	Data       map[string]any `json:"data"`
	Client     map[string]any `json:"client"`
}

var safeID = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// NewID 指令/结果标识：yyyyMMdd-HHmm-xxxx（北京时间 + 4 位随机）。
func NewID(now time.Time) string {
	cn := now.UTC().Add(8 * time.Hour)
	return fmt.Sprintf("%s-%04x", cn.Format("20060102-1504"), now.UnixNano()&0xffff)
}

// CmdName 指令文件名。
func CmdName(app string) string { return app + "_cmd.json" }

func sanitizeID(id string) string {
	out := safeID.ReplaceAllString(id, "_")
	if len(out) > 128 {
		out = out[:128]
	}
	if out == "" {
		out = "unknown"
	}
	return out
}

// ResultName 结果文件名（与客户端 usb_protocol.result_name 一致）。
func ResultName(app, id string) string {
	return app + "_result_" + sanitizeID(id) + ".json"
}

// Payload 构造带 file 参数的指令 JSON。ttl<=0 时不写 expires_at。
func Payload(app, id, action, fileName string, now time.Time, ttl time.Duration) []byte {
	params := map[string]any{}
	if fileName != "" {
		params["file"] = fileName
	}
	return PayloadWithParams(app, id, action, fileName, params, now, ttl)
}

// PayloadWithParams 构造任意 params 的指令 JSON（派车导出传 date）。
func PayloadWithParams(app, id, action, fileName string, params map[string]any, now time.Time, ttl time.Duration) []byte {
	if params == nil {
		params = map[string]any{}
	}
	cmd := Command{
		Version:  1,
		ID:       id,
		CycleID:  id,
		App:      app,
		Action:   action,
		Kind:     action,
		File:     fileName,
		Params:   params,
		IssuedAt: now.UTC().Add(8 * time.Hour).Format("2006-01-02T15:04:05+08:00"),
	}
	if ttl > 0 {
		cmd.ExpiresAt = now.Add(ttl).UTC().Add(8 * time.Hour).Format("2006-01-02T15:04:05+08:00")
	}
	raw, _ := json.Marshal(cmd)
	return raw
}

// ParseResult 解析结果 JSON；id/app 缺失或非法返回错误。
func ParseResult(raw []byte) (Result, error) {
	var res Result
	if err := json.Unmarshal(raw, &res); err != nil {
		return Result{}, fmt.Errorf("结果 JSON 解析失败: %w", err)
	}
	res.ID = strings.TrimSpace(res.ID)
	res.App = strings.TrimSpace(res.App)
	if res.ID == "" || res.App == "" {
		return Result{}, fmt.Errorf("结果缺少 id/app")
	}
	return res, nil
}

// Summary 结果摘要（日志/通知用）：成功返回空串，失败返回原因。
func (r Result) Summary() string {
	if r.OK {
		return ""
	}
	if strings.TrimSpace(r.Error) != "" {
		return r.Error
	}
	return "被控机返回失败（无错误详情）"
}
