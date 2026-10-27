package chatlog

// 每日归档进程内直调入口（wechatanswer 经 bridge 把 WF4 纪要/文档直推 chatlog → WeKnora，
// 不走 HTTP 自循环）：业务核 ingestBody 与 handleIngest 共用（direct.go 为唯一实现，
// HTTP 入口为薄适配）。直调仍校验 CHATLOG_INGEST_TOKEN（未配置 ingest_token_missing /
// 不等 ingest_token_invalid，与 HTTP 入口同 fail-closed，不因进程内调用免密钥）。

import (
	"context"
	"fmt"
)

// IngestError 直调失败（Status/Code/Message 与 /ingest 失败信封同语义）。
type IngestError struct {
	Status  int
	Code    string
	Message string
}

func (e *IngestError) Error() string {
	return fmt.Sprintf("chatlog ingest 失败（%d）：%s %s", e.Status, e.Code, e.Message)
}

// IngestDirect 每日归档直调：token 必须与 CHATLOG_INGEST_TOKEN（或 IngestToken 字段）一致；
// body 同 /ingest 请求契约（kind=digest：digest_md；kind=file：file{name,data_base64}）。
// 成功返回 data（/ingest data 同形）；失败返回 *IngestError。
func (m *Module) IngestDirect(ctx context.Context, token string, body map[string]any) (map[string]any, error) {
	expected := m.ingestToken(ctx)
	if expected == "" {
		return nil, &IngestError{Status: 503, Code: "ingest_token_missing", Message: "未配置对接密钥"}
	}
	if !tokenEqual(token, expected) {
		return nil, &IngestError{Status: 401, Code: "ingest_token_invalid", Message: "对接密钥错误"}
	}
	data, ierr := m.ingestBody(ctx, body)
	if ierr != nil {
		return nil, ierr
	}
	return data, nil
}

// ingestBody 每日归档业务核（HTTP 与直调共用）：WeKnora 配置检查 → 入参校验 → 幂等落库。
func (m *Module) ingestBody(ctx context.Context, body map[string]any) (map[string]any, *IngestError) {
	fail := func(status int, code, message string) (map[string]any, *IngestError) {
		return nil, &IngestError{Status: status, Code: code, Message: message}
	}
	// WeKnora 未配置（WEKNORA_API_KEY 缺失）或知识库 ID 为空 → 503（密钥 fail-closed）
	kbID := m.configValue(ctx, "WEKNORA_CHATLOG_KB_ID")
	if m.KB == nil || m.configValue(ctx, "WEKNORA_API_KEY") == "" || kbID == "" {
		return fail(503, "weknora_not_configured",
			"未配置 WeKnora（WEKNORA_API_KEY / WEKNORA_CHATLOG_KB_ID）")
	}
	item, perr := parseBody(body)
	if perr != nil {
		return fail(perr.httpStatus, perr.code, perr.message)
	}
	seg := folderSegments(item.date)
	folderPath := seg[0] + "/" + seg[1]
	res, err := upsertFile(ctx, m.KB, kbID, folderPath, item.fileName, item.buf)
	if err != nil {
		return fail(502, "upstream_error", "写入知识库失败："+err.Error())
	}
	return map[string]any{
		"date":         item.date,
		"group_name":   item.groupName,
		"kind":         item.kind,
		"folder":       folderPath,
		"file_name":    res.FileName,
		"knowledge_id": res.KnowledgeID,
		"replaced":     res.Replaced,
	}, nil
}
