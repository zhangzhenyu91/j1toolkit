// 标准库 log 统一入口：把 log.Printf 等运行日志写入 DurableSink，
// 在数据库接入前即同步落文件；直接 fmt.Printf 的生产代码统一改为标准库 log。
//
// 约束：Sink 内部日志必须写各自独立 stderr（DurableSink / Queue 已如此），禁止在 Sink
// 内再调用标准库 log——标准库在输出期间持有 logger 锁，重入会死锁，Writer 不做也不应
// 假装能做这种保护。
package logging

import (
	"context"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"shade/internal/config"
	"shade/internal/logsafe"
)

// Writer 把标准库日志行转换为 app_log 条目（category=runtime，event=runtime.log）。
type Writer struct {
	Sink     Sink
	AppCode  string
	Fallback io.Writer // nil 不写回退；InstallStandardLogger 默认 stderr
}

// NewWriter 构造标准日志写入器（回退 stderr）。
func NewWriter(sink Sink, appCode string) *Writer {
	return &Writer{Sink: sink, AppCode: appCode, Fallback: os.Stderr}
}

// Write 按行拆分写入 Sink；空行跳过。写入失败/对象为空时原文回退 Fallback。
// 返回长度与 nil：标准库 logger 不处理错误，避免日志失败影响业务流程。
func (w *Writer) Write(p []byte) (int, error) {
	if w == nil || len(p) == 0 {
		return len(p), nil
	}
	for _, line := range strings.Split(string(p), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		w.writeLine(line)
	}
	return len(p), nil
}

// writeLine 单行写 Sink；原文先经 logsafe 脱敏（Bearer/token/JSON 密钥），失败回退原文。
func (w *Writer) writeLine(line string) {
	line = logsafe.TextForLog(line)
	if w.Sink != nil {
		e := config.AppLogEntry{
			AppCode:    w.AppCode,
			Level:      "info",
			Category:   "runtime",
			Event:      "runtime.log",
			Message:    line,
			MessageCN:  line,
			RawMessage: line,
			LogID:      config.NewAppLogID(),
			OccurredAt: time.Now(),
		}
		if err := w.Sink.Write(context.Background(), e); err == nil {
			return
		}
	}
	w.writeFallback(line + "\n")
}

// writeFallback 写回退输出（默认 stderr），失败吞掉。
func (w *Writer) writeFallback(s string) {
	if w.Fallback == nil || s == "" {
		return
	}
	_, _ = io.WriteString(w.Fallback, s)
}

// InstallStandardLogger 将标准库 log 输出接到 sink（启动早期 DB 未接入也可同步落文件）；
// 返回恢复函数（测试/嵌入场景用）。log.Fatal 同样先经此写入再退出。
func InstallStandardLogger(sink Sink, appCode string) func() {
	old := log.Writer()
	log.SetOutput(NewWriter(sink, appCode))
	return func() { log.SetOutput(old) }
}
