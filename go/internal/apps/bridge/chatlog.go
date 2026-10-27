// wechatanswer.ChatlogSink 适配：进程内直调 chatlog.Module.IngestDirect（WF4 纪要/文档
// → WeKnora 知识库幂等落库），不走 HTTP 自循环；对接密钥 CHATLOG_INGEST_TOKEN 经 ConfigGet
// 读出交 chatlog 校验（fail-closed，不因进程内调用免密钥）。失败以 error 返回，
// wechatanswer 归档循环按 attempts 口径补跑/丢弃。
package bridge

import (
	"context"
	"errors"

	"shade/internal/apps/chatlog"
	"shade/internal/apps/wechatanswer"
)

// ChatlogBridge wechatanswer.ChatlogSink 的真实实现（chatlog 直调）。
type ChatlogBridge struct {
	CL  *chatlog.Module
	Cfg ConfigGet // CHATLOG_INGEST_TOKEN 来源（与 chatlog 模块同源）
}

// NewChatlogBridge 构造聊天归档出口。
func NewChatlogBridge(cl *chatlog.Module, cfg ConfigGet) *ChatlogBridge {
	return &ChatlogBridge{CL: cl, Cfg: cfg}
}

// Ingest 单件归档直推（kind=digest / kind=file，/chatlog/ingest 契约同形）；
// 失败返回可读 error（含 *chatlog.IngestError 的状态与业务码）。
func (b *ChatlogBridge) Ingest(ctx context.Context, item wechatanswer.ChatlogItem) error {
	if b == nil || b.CL == nil {
		return errors.New("聊天归档出口未接入（chatlog 模块未注入）")
	}
	body := map[string]any{
		"date":       item.Date,
		"group_name": item.GroupName,
		"kind":       item.Kind,
	}
	switch item.Kind {
	case "digest":
		body["digest_md"] = item.DigestMD
	case "file":
		if item.File == nil {
			return &chatlog.IngestError{Status: 400, Code: "param_invalid", Message: "file.name 不能为空"}
		}
		body["file"] = map[string]any{"name": item.File.Name, "data_base64": item.File.DataBase64}
	}
	token := tokenOf(ctx, b.Cfg, "CHATLOG_INGEST_TOKEN")
	_, err := b.CL.IngestDirect(ctx, token, body)
	return err
}

// 编译期契约。
var _ wechatanswer.ChatlogSink = (*ChatlogBridge)(nil)
