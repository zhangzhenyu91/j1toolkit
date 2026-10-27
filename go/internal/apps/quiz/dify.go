// 题库刷题：Dify「题目解析」工作流封装（对齐 server/src/quiz/dify.js）。
// POST {DIFY_API_URL}/v1/workflows/run（blocking）；inputs：type 题型（单选题/多选题/判断题）/
// stem 题干 / options 选项汇总（多行文本，'A. 选项内容' 每行一个）/ answer 答案字母；
// 输出 outputs.analysis（解析文本，缺失时取第一个非空字符串容错）。
package quiz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DifyAnalyzer Dify「题目解析」工作流客户端。
// 配置读取：Cfg 非 nil 时每次解析热读运行时配置（DIFY_API_URL / DIFY_QUIZ_API_KEY，
// 改配置即时生效，对齐 Node 每次调用读 config）；Cfg 为 nil 时回落 APIURL/APIKey 固定字段（测试注入）。
type DifyAnalyzer struct {
	Cfg    ConfigReader // 运行时配置热读源（*config.Store 满足）
	APIURL string       // 固定配置 DIFY_API_URL（/v1 由代码拼接；Cfg 为 nil 时生效）
	APIKey string       // 固定配置 DIFY_QUIZ_API_KEY（Cfg 为 nil 时生效）
	HTTP   *http.Client
}

// analyzeTimeout 单题解析超时（Node axios timeout: 90000）。
const analyzeTimeout = 90 * time.Second

// analyzeUser 工作流 user 标识前缀。
const analyzeUser = "quiz-q"

// typeText 题型中文名（工作流入参）。
var typeText = map[string]string{"single": "单选题", "multiple": "多选题", "judge": "判断题"}

// resolve 读取工作流地址与密钥：Cfg 非 nil 时热读运行时配置，否则用固定字段。
func (d *DifyAnalyzer) resolve(ctx context.Context) (apiURL, apiKey string) {
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
		return get("DIFY_API_URL"), get("DIFY_QUIZ_API_KEY")
	}
	return strings.TrimSpace(d.APIURL), strings.TrimSpace(d.APIKey)
}

// Configured 是否已配置（未配置时 AI 解析整体停用）。
func (d *DifyAnalyzer) Configured() bool {
	apiURL, apiKey := d.resolve(context.Background())
	return apiURL != "" && apiKey != ""
}

// workflowRunURL 拼 /v1/workflows/run（配置已带 /v1 也不重复）。
func workflowRunURL(apiURL string) string {
	base := strings.TrimRight(strings.TrimSpace(apiURL), "/")
	if strings.HasSuffix(base, "/v1") {
		return base + "/workflows/run"
	}
	return base + "/v1/workflows/run"
}

// Analyze 生成单题解析，成功返回非空解析文本；失败抛错（由队列归 failed）。
func (d *DifyAnalyzer) Analyze(ctx context.Context, q QuestionForAI) (string, error) {
	apiURL, apiKey := d.resolve(ctx)
	if apiURL == "" || apiKey == "" {
		return "", errors.New("Dify 未配置（DIFY_API_URL / DIFY_QUIZ_API_KEY）")
	}
	// 选项汇总成一个多行文本变量：'A. 选项内容\nB. 选项内容'
	lines := make([]string, 0, len(q.Options))
	for i, opt := range q.Options {
		letter := "A"
		if i < len(letters) {
			letter = letters[i]
		}
		lines = append(lines, letter+". "+opt)
	}
	optionsText := strings.Join(lines, "\n")
	// 判断题答案附中文标注（A=正确 B=错误），便于工作流理解
	answerText := q.Answer
	if q.Type == "judge" {
		if q.Answer == "A" {
			answerText = "A（正确）"
		} else {
			answerText = "B（错误）"
		}
	}
	typeName := typeText[q.Type]
	if typeName == "" {
		typeName = q.Type
	}
	payload, _ := json.Marshal(map[string]any{
		"inputs": map[string]string{
			"type":    typeName,
			"stem":    q.Content,
			"options": optionsText,
			"answer":  answerText,
		},
		"response_mode": "blocking",
		"user":          analyzeUser + fmt.Sprint(q.ID),
	})
	client := d.HTTP
	if client == nil {
		client = &http.Client{Timeout: analyzeTimeout}
	}
	reqCtx, cancel := context.WithTimeout(ctx, analyzeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, workflowRunURL(apiURL), bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Dify 工作流触发失败（HTTP %d）：%s", resp.StatusCode, truncateRunes(string(body), 200))
	}
	var out struct {
		Data struct {
			Outputs map[string]any `json:"outputs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	analysis := ""
	if s, ok := out.Data.Outputs["analysis"].(string); ok {
		analysis = s
	}
	// 容错：analysis 缺失/为空时取 outputs 第一个非空字符串值
	if strings.TrimSpace(analysis) == "" {
		for _, v := range out.Data.Outputs {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				analysis = s
				break
			}
		}
	}
	if strings.TrimSpace(analysis) == "" {
		return "", errors.New("Dify 返回的解析为空")
	}
	return strings.TrimSpace(analysis), nil
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
var _ QuestionAnalyzer = (*DifyAnalyzer)(nil)
