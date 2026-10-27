// wechatanswer.AskIngest 适配：进程内直调 callme.Module.IngestChat（群内提问业务核，
// 复用 wxid 唯一启用账号 + call-me 权限 + 会话按名找或建 + WeKnora 收流 + WF3 命名，
// 不走 HTTP 自循环）；对接密钥 CALLME_INGEST_TOKEN 经 ConfigGet 读出交 callme 校验
// （fail-closed，不因进程内调用免密钥）。业务失败映射 *wechatanswer.AskError（群内回执文案用）。
// 另附 callme.FileNamer 的 Dify WF3 命名实现（DIFY_WF3_API_KEY + integrations.WorkflowClient）。
package bridge

import (
	"context"
	"strings"

	"shade/internal/apps/callme"
	"shade/internal/apps/wechatanswer"
	"shade/internal/integrations"
)

// AskBridge wechatanswer.AskIngest 的真实实现（callme 直调）。
type AskBridge struct {
	CM  *callme.Module
	Cfg ConfigGet // CALLME_INGEST_TOKEN 来源（与 callme 模块同源）
}

// NewAskBridge 构造群内提问直调出口。
func NewAskBridge(cm *callme.Module, cfg ConfigGet) *AskBridge {
	return &AskBridge{CM: cm, Cfg: cfg}
}

// IngestChat 群内提问：入参翻译为 callme /ingest-chat 契约后直调业务核；
// 成功返回回答正文，失败返回 *wechatanswer.AskError（code/message/models 与 callme 失败信封同语义）。
func (a *AskBridge) IngestChat(ctx context.Context, req wechatanswer.AskRequest) (string, error) {
	if a == nil || a.CM == nil {
		return "", &wechatanswer.AskError{Code: "upstream_error", Message: "Call Me 未接入"}
	}
	token := tokenOf(ctx, a.Cfg, "CALLME_INGEST_TOKEN")
	callReq := callme.IngestChatRequest{
		Wxid:         req.Wxid,
		Query:        req.Query,
		SessionTitle: req.SessionTitle,
		WebSearch:    boolPtr(req.WebSearch),
		Image:        req.Image,
	}
	if req.File != nil {
		callReq.File = &callme.IngestChatFile{Name: req.File.Name, DataBase64: req.File.DataBase64}
	}
	res, err := a.CM.IngestChat(ctx, token, callReq)
	if err != nil {
		return "", askErrorOf(err)
	}
	return res.Answer, nil
}

// askErrorOf callme 失败 → wechatanswer.AskError（模型候选取显示名；其余归 upstream_error）。
func askErrorOf(err error) error {
	if ierr, ok := err.(*callme.IngestChatError); ok {
		ae := &wechatanswer.AskError{Code: ierr.Code, Message: ierr.Message}
		for _, mm := range ierr.Models {
			name := ""
			if s, ok := mm["name"].(string); ok {
				name = s
			}
			if name == "" {
				if s, ok := mm["id"].(string); ok {
					name = s
				}
			}
			if name != "" {
				ae.Models = append(ae.Models, name)
			}
		}
		return ae
	}
	return &wechatanswer.AskError{Code: "upstream_error", Message: "AI 服务暂时不可用，请稍后再试"}
}

// boolPtr 取址（AskRequest.WebSearch 值语义 → callme 契约可空语义）。
func boolPtr(v bool) *bool { return &v }

// ---- callme.FileNamer（Dify WF3 命名）----

// wf3NameInputLen WF3 的 answer 输入截断长度（对齐 crane-answer / wechatanswer 口径）。
const wf3NameInputLen = 4000

// wf3NameMax WF3 文件名截断长度。
const wf3NameMax = 40

// WF3Namer callme.FileNamer 实现：DIFY_WF3_API_KEY 工作流命名回答 md
// （inputs question/answer，输出 filename；失败/空名返回空串，callme 回退会话标题）。
type WF3Namer struct {
	Client *integrations.WorkflowClient
}

// NewWF3Namer 构造 WF3 文件名生成器（cfg 为运行时配置，DIFY_API_URL / DIFY_WF3_API_KEY 热读取）。
func NewWF3Namer(cfg integrations.Provider) *WF3Namer {
	return &WF3Namer{Client: &integrations.WorkflowClient{Cfg: cfg, APIKeyKey: "DIFY_WF3_API_KEY"}}
}

// NameFile 生成回答 md 文件名（不含扩展名）；未配置或上游失败返回空串（不抛错）。
func (n *WF3Namer) NameFile(ctx context.Context, username, sessionTitle, query, answer string) string {
	if n == nil || n.Client == nil {
		return ""
	}
	res, err := n.Client.Run(ctx, integrations.RunRequest{
		Inputs: map[string]any{
			"question": query,
			"answer":   truncateRunesLocal(answer, wf3NameInputLen),
		},
		User: username,
	})
	if err != nil {
		return ""
	}
	return sanitizeWF3Name(res.Outputs.String("filename"))
}

// sanitizeWF3Name 文件名净化：去 \/:*?"<>| 与换行、截 40 字（对齐 wechatanswer.sanitizeFileName）。
func sanitizeWF3Name(name string) string {
	repl := strings.NewReplacer(`/`, ``, `\`, ``, `:`, ``, `*`, ``, `?`, ``, `"`, ``, `<`, ``, `>`, ``, `|`, ``, "\r", ``, "\n", ``)
	return truncateRunesLocal(strings.TrimSpace(repl.Replace(name)), wf3NameMax)
}

// truncateRunesLocal 按 rune 截断。
func truncateRunesLocal(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// 编译期契约。
var (
	_ wechatanswer.AskIngest = (*AskBridge)(nil)
	_ callme.FileNamer       = (*WF3Namer)(nil)
)
