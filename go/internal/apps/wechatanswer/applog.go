// 应用日志接口（logging 风格）与原始报文脱敏：
// AppLogger 方法集与 shade/internal/logging.Logger 对齐，由 bridge 直接注入 *logging.Logger，
// 本包不 import logging（避免装配循环）；未注入时回退标准日志。
// 原始报文仅在 LOG_RAW_MESSAGE=1 时落日志，且一律脱敏（token/aes/key 等敏感值不落盘）。
package wechatanswer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
)

// AppLogger 应用结构化日志写入器（*logging.Logger 天然满足；nil 时静默回退 log.Printf）。
type AppLogger interface {
	Record(ctx context.Context, level, category, event, messageCN, raw string, fields map[string]any) error
	Info(ctx context.Context, category, event, messageCN, raw string, fields map[string]any) error
	Warn(ctx context.Context, category, event, messageCN, raw string, fields map[string]any) error
	Error(ctx context.Context, category, event, messageCN, raw string, fields map[string]any) error
}

// appLog 写一条应用日志（后台消息处理/补推统一入口）；AppLog 未注入时回退标准日志。
func (m *Module) appLog(ctx context.Context, level, category, event, messageCN, raw string, fields map[string]any) {
	if m.AppLog != nil {
		if err := m.AppLog.Record(ctx, level, category, event, messageCN, raw, fields); err == nil {
			return
		}
	}
	if messageCN == "" {
		messageCN = raw
	}
	log.Printf("[微信智能应答][%s][%s] %s", level, event, messageCN)
}

// appLogErr 错误级便捷封装。
func (m *Module) appLogErr(ctx context.Context, event, messageCN string, err error, fields map[string]any) {
	raw := ""
	if err != nil {
		raw = err.Error()
	}
	if fields == nil {
		fields = map[string]any{}
	}
	fields["error"] = raw
	m.appLog(ctx, "error", "business", event, messageCN, raw, fields)
}

// sensitiveKeyRe 敏感字段名（值一律脱敏）。
var sensitiveKeyRe = regexp.MustCompile(`(?i)token|aes|secret|passw|ticket|authorization|api[_-]?key|file_no|attach_id|master_buf`)

// redactValue 单值脱敏（短值整体打码，长值保留前后各 2 字符）。
func redactValue(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	r := []rune(v)
	if len(r) <= 4 {
		return "****"
	}
	return string(r[:2]) + "****" + string(r[len(r)-2:])
}

// RedactRaw 原始报文脱敏：命中 sensitiveKeyRe 的键值打码（深层递归）。
func RedactRaw(raw map[string]any) map[string]any {
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		if sensitiveKeyRe.MatchString(k) {
			out[k] = redactValue(toStr(v))
			continue
		}
		switch t := v.(type) {
		case map[string]any:
			out[k] = RedactRaw(t)
		case []any:
			arr := make([]any, 0, len(t))
			for _, item := range t {
				if m, ok := item.(map[string]any); ok {
					arr = append(arr, RedactRaw(m))
				} else {
					arr = append(arr, item)
				}
			}
			out[k] = arr
		default:
			out[k] = v
		}
	}
	return out
}

// rawLogAllowed 原始报文日志开关（LOG_RAW_MESSAGE=1 才允许）。
func (m *Module) rawLogAllowed(ctx context.Context) bool {
	return m.cfgValue(ctx, "LOG_RAW_MESSAGE", "0") == "1"
}

// rawLogf 原始报文日志：仅开关允许时输出，内容先脱敏再截断。
func (m *Module) rawLogf(ctx context.Context, format string, args ...any) {
	if !m.rawLogAllowed(ctx) {
		return
	}
	msg := fmt.Sprintf(format, args...)
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	log.Printf("[微信智能应答][原始报文] %s", msg)
}

// rawLogMessage 单条入站消息原始报文（脱敏 JSON）。
func (m *Module) rawLogMessage(ctx context.Context, msg *Message) {
	if msg == nil || !m.rawLogAllowed(ctx) {
		return
	}
	buf, err := json.Marshal(RedactRaw(msg.Raw))
	if err != nil {
		return
	}
	m.rawLogf(ctx, "入站消息 kind=%s svr_id=%s 报文=%s", msg.Kind, msg.SvrID, string(buf))
}
