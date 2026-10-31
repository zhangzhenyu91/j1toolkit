// 日志入口集中脱敏：所有写库/写文件路径（Logger.Record / WriteEntry / DurableSink / Queue）
// 在落盘前统一清洗 message/message_cn/raw/detail/context，防止调用方误把完整 URL、
// userinfo、Signature/token 等秘密写进 app_log 或用户可见流水。
package logging

import (
	"shade/internal/config"
	"shade/internal/logsafe"
)

// sanitizeEntry 就地脱敏日志条目文本字段（URL/query/Bearer/JSON 密钥/路径票据）。
func sanitizeEntry(e *config.AppLogEntry) {
	if e == nil {
		return
	}
	e.Message = logsafe.TextForLog(e.Message)
	e.MessageCN = logsafe.TextForLog(e.MessageCN)
	e.RawMessage = logsafe.TextForLog(e.RawMessage)
	e.Detail = logsafe.TextForLog(e.Detail)
	e.ContextJSON = logsafe.TextForLog(e.ContextJSON)
}
