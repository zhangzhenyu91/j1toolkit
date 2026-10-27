// 安全日活动记录：Dify 工作流封装（对齐 server/src/safeday/dify.js）。
// URL 口径：学习文件（合并产物/单文件原样）先存 COS 拿公网地址，工作流入参为三个 string
// （url 学习文件地址 / date / class），触发成功后立即返回，SSE 流后台消费；
// 工作流 failed/stopped 或流读取异常时调用 OnFailed(error)。工作流末尾由 HTTP 节点把六段文字
// + date + class 回传 /callback，后端据此渲染 docx 落盘（不再由工作流写文件）。
package safeday

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// WorkflowInput 工作流触发入参。
type WorkflowInput struct {
	URL      string      // 学习文件 COS 公网地址
	Date     string      // YYYY.MM.DD
	Class    string      // 班组名
	OnFailed func(error) // 工作流失败/流读取异常回调（可 nil）
}

// WorkflowRunner Dify 工作流触发适配层（可注入；测试用桩，不实连 Dify）。
type WorkflowRunner interface {
	Run(ctx context.Context, in WorkflowInput) error
}

// DifyWorkflow Dify「安全日生成」工作流客户端。
// 配置读取：Cfg 非 nil 时每次触发热读运行时配置（DIFY_API_URL / DIFY_SAFEDAY_API_KEY，
// 改配置即时生效，对齐 Node 每次调用读 config）；Cfg 为 nil 时回落 APIURL/APIKey 固定字段（测试注入）。
type DifyWorkflow struct {
	Cfg    ConfigReader // 运行时配置热读源（*config.Store 满足）
	APIURL string       // 固定配置 DIFY_API_URL（/v1 由代码拼接；Cfg 为 nil 时生效）
	APIKey string       // 固定配置 DIFY_SAFEDAY_API_KEY（Cfg 为 nil 时生效）
	HTTP   *http.Client
}

// workflowUser 工作流 user 标识。
const workflowUser = "safeday-web"

// workflowRunURL 拼 /v1/workflows/run（配置已带 /v1 也不重复）。
func workflowRunURL(apiURL string) string {
	base := strings.TrimRight(strings.TrimSpace(apiURL), "/")
	if strings.HasSuffix(base, "/v1") {
		return base + "/workflows/run"
	}
	return base + "/v1/workflows/run"
}

// resolve 读取工作流地址与密钥：Cfg 非 nil 时热读运行时配置，否则用固定字段。
func (d *DifyWorkflow) resolve(ctx context.Context) (apiURL, apiKey string) {
	if d == nil {
		return "", ""
	}
	if d.Cfg != nil {
		get := func(key string) string {
			v, err := d.Cfg.Get(ctx, key)
			if err != nil {
				return ""
			}
			return strings.TrimSpace(v)
		}
		return get("DIFY_API_URL"), get("DIFY_SAFEDAY_API_KEY")
	}
	return strings.TrimSpace(d.APIURL), strings.TrimSpace(d.APIKey)
}

// Configured 是否已配置（未配置时触发报错，对齐 Node 未配置文案）。
func (d *DifyWorkflow) Configured() bool {
	apiURL, apiKey := d.resolve(context.Background())
	return apiURL != "" && apiKey != ""
}

// Run 触发工作流（入参三个 string）；触发成功后立即返回，SSE 流在后台消费。
func (d *DifyWorkflow) Run(ctx context.Context, in WorkflowInput) error {
	apiURL, apiKey := d.resolve(ctx)
	if apiURL == "" || apiKey == "" {
		return errors.New("未配置 DIFY_API_URL 或 DIFY_SAFEDAY_API_KEY")
	}
	payload, _ := json.Marshal(map[string]any{
		"inputs":        map[string]string{"url": in.URL, "date": in.Date, "class": in.Class},
		"response_mode": "streaming",
		"user":          workflowUser,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, workflowRunURL(apiURL), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	client := d.HTTP
	if client == nil {
		client = &http.Client{Timeout: 0} // 流式响应不限总时长
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Dify 工作流触发失败：%w", err)
	}
	if resp.StatusCode != http.StatusOK {
		text, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		resp.Body.Close()
		return fmt.Errorf("Dify 工作流触发失败（HTTP %d）：%s", resp.StatusCode, truncateRunes(string(text), 200))
	}
	// 后台消费 SSE 流
	go func() {
		defer resp.Body.Close()
		if err := consumeStream(resp.Body, in.OnFailed); err != nil && in.OnFailed != nil {
			in.OnFailed(fmt.Errorf("工作流流读取异常：%w", err))
		}
	}()
	return nil
}

// sseEvent Dify SSE 流事件。
type sseEvent struct {
	Event string `json:"event"`
	Data  struct {
		Status  string `json:"status"`
		Error   string `json:"error"`
		Message string `json:"message"`
	} `json:"data"`
}

// consumeStream 消费 Dify workflow 的 SSE 流（后台运行）：
// workflow_finished 且 status=failed/stopped 时回调 onFailed；succeeded 不做完成标记
// （置 done 由 /callback 渲染落盘时置位）。
func consumeStream(body io.Reader, onFailed func(error)) error {
	reader := bufio.NewReader(body)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
				if payload != "" {
					var evt sseEvent
					if json.Unmarshal([]byte(payload), &evt) == nil && evt.Event == "workflow_finished" {
						status := evt.Data.Status
						if status == "failed" || status == "stopped" {
							msg := evt.Data.Error
							if msg == "" {
								msg = evt.Data.Message
							}
							if msg == "" {
								if status == "stopped" {
									msg = "Dify 工作流被停止"
								} else {
									msg = "Dify 工作流执行失败"
								}
							}
							if onFailed != nil {
								onFailed(errors.New(msg))
							}
						}
						return nil
					}
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// truncateRunes 按 rune 截断。
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// 编译期检查默认实现满足接口。
var _ WorkflowRunner = (*DifyWorkflow)(nil)
