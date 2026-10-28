// Package exporter 是派车单/轨迹并行导出编排（uvmp-export run 的核心）：
// 首行请求（config/params/codes）→ 查询池 → 预取池 → 独立浏览器渲染池 →
// 合并/清单/12点报表 → done 事件。行为对齐 toolkit/apps/pcd_export/backend.py
// 的 Python 编排（含 dev.mock 全流程），并行化后 stdout 事件流仍按清单序号
// 有序发出。查询/渲染经接口隔离（OrderAPI / Renderer），单测全走 fake。
package exporter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
)

// CodeItem 请求中的已解析清单行（Python _resolve_codes 产物）。
type CodeItem struct {
	Seq      int    `json:"seq"`
	Code     string `json:"code"`
	DateHint string `json:"date_hint"`
}

// Request stdin 首行请求（固定协议，密码只经此传入，不进命令行）。
type Request struct {
	Config map[string]any `json:"config"`
	Params map[string]any `json:"params"`
	Codes  []CodeItem     `json:"codes"`
}

// ReadRequest 从 r 读取首行 JSON 请求（后续行留给 PumpCancel）。
func ReadRequest(r *bufio.Reader) (Request, error) {
	var req Request
	for {
		line, err := r.ReadString('\n')
		line = strings.TrimSpace(line)
		if line != "" {
			if jerr := json.Unmarshal([]byte(line), &req); jerr != nil {
				return Request{}, fmt.Errorf("请求解析失败: %w", jerr)
			}
			break
		}
		if err != nil {
			return Request{}, fmt.Errorf("未读到请求（stdin 首行）: %w", err)
		}
	}
	return req, nil
}

// PumpCancel 持续读 stdin 后续行，收到 {"command":"cancel"} 即调用 cancel（只调一次）。
// 读到 EOF 或解析失败时退出（进程收尾管道关闭属正常）。
func PumpCancel(r *bufio.Reader, cancel func()) {
	for {
		line, err := r.ReadString('\n')
		line = strings.TrimSpace(line)
		if line != "" {
			var cmd struct {
				Command string `json:"command"`
			}
			if json.Unmarshal([]byte(line), &cmd) == nil && cmd.Command == "cancel" {
				cancel()
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// Emitter stdout NDJSON 事件输出（线程安全，逐行 flush）。
// stdout 只出事件：log / progress / done / error，杂音不得混入。
type Emitter struct {
	mu sync.Mutex
	w  *bufio.Writer
}

// NewEmitter 包装 w 为事件输出端。
func NewEmitter(w io.Writer) *Emitter {
	bw, _ := w.(*bufio.Writer)
	if bw == nil {
		bw = bufio.NewWriter(w)
	}
	return &Emitter{w: bw}
}

func (e *Emitter) emit(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.w.Write(b)
	e.w.WriteByte('\n')
	e.w.Flush()
}

// Log 事件：{"event":"log","line":...}。
func (e *Emitter) Log(format string, args ...any) {
	if len(args) == 0 {
		e.emit(struct {
			Event string `json:"event"`
			Line  string `json:"line"`
		}{"log", format})
		return
	}
	e.emit(struct {
		Event string `json:"event"`
		Line  string `json:"line"`
	}{"log", fmt.Sprintf(format, args...)})
}

// Progress 事件：{"event":"progress","current":n,"total":n,"label":...}。
func (e *Emitter) Progress(current, total int, label string) {
	e.emit(struct {
		Event   string `json:"event"`
		Current int    `json:"current"`
		Total   int    `json:"total"`
		Label   string `json:"label"`
	}{"progress", current, total, label})
}

// Done 事件：{"event":"done","result":{...}}（backend.run 返回形状）。
func (e *Emitter) Done(result any) {
	e.emit(struct {
		Event  string `json:"event"`
		Result any    `json:"result"`
	}{"done", result})
}

// Error 事件：{"event":"error","error":"..."}。
func (e *Emitter) Error(msg string) {
	e.emit(struct {
		Event string `json:"event"`
		Error string `json:"error"`
	}{"error", msg})
}
