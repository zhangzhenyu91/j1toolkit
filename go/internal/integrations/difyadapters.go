// 应用接口适配器：Dify 工作流客户端对接 worklog.DifyClient（照片水印识别）
// 与 safeday.WorkflowRunner（安全日生成触发）；编译期接口检查见 compat.go。
package integrations

import (
	"context"
	"errors"

	"shade/internal/apps/safeday"
	"shade/internal/apps/worklog"
)

// safedayWorkflowUser 安全日工作流 user 标识（对齐 Node safeday/dify.js）。
const safedayWorkflowUser = "safeday-web"

// PhotoVerifier 出工日志照片水印识别适配（满足 worklog.DifyClient）：
// blocking 工作流，inputs date/destination/picture(remote_url)，outputs
// title/time/weather/location/lng/lat；天气经 worklog.NormalizeWeather 归一。
// 任何异常一律归 OK=false 不抛出；业务层空内容重试由 worklog.recognize 承担，
// 传输层瞬时重试由 WorkflowClient.Retry 承担（接线时收住总预算，见包注释）。
type PhotoVerifier struct {
	Client *WorkflowClient
}

// NewPhotoVerifier 构造照片水印识别适配（client 不为 nil）。
func NewPhotoVerifier(client *WorkflowClient) *PhotoVerifier {
	return &PhotoVerifier{Client: client}
}

// VerifyPhoto 识别单张照片水印信息；异常归 OK=false。
func (v *PhotoVerifier) VerifyPhoto(ctx context.Context, username, date, destination, url string) worklog.VerifyResult {
	if v == nil || v.Client == nil {
		return worklog.VerifyResult{}
	}
	res, err := v.Client.Run(ctx, RunRequest{
		Inputs: map[string]any{
			"date":        date,
			"destination": destination,
			// 单文件变量传对象（数组会报 invalid_param: must be a file）
			"picture": map[string]any{"transfer_method": "remote_url", "url": url, "type": "image"},
		},
		User: username,
	})
	if err != nil {
		return worklog.VerifyResult{}
	}
	return worklog.VerifyResult{
		OK:          true,
		WorkContent: res.Outputs.String("title"),
		Time:        res.Outputs.String("time"),
		Weather:     worklog.NormalizeWeather(res.Outputs.String("weather")),
		Location:    res.Outputs.String("location"),
		Lng:         res.Outputs.String("lng"),
		Lat:         res.Outputs.String("lat"),
	}
}

// StreamRunner 安全日 Dify 工作流触发适配（满足 safeday.WorkflowRunner）：
// streaming 触发，inputs url/date/class 三个 string；触发成功立即返回、SSE 后台消费，
// 工作流 failed/stopped 或流读取异常时调用 OnFailed（回调串接给 WorkflowInput.OnFailed）。
type StreamRunner struct {
	Client *WorkflowClient
	// User 工作流 user 标识（默认 safeday-web，对齐 Node）。
	User string
}

// Run 触发安全日生成工作流。
func (r *StreamRunner) Run(ctx context.Context, in safeday.WorkflowInput) error {
	if r == nil || r.Client == nil {
		return errors.New("未配置 DIFY_API_URL 或 DIFY_SAFEDAY_API_KEY")
	}
	user := r.User
	if user == "" {
		user = safedayWorkflowUser
	}
	return r.Client.Start(ctx, StreamInput{
		Inputs:   map[string]any{"url": in.URL, "date": in.Date, "class": in.Class},
		User:     user,
		OnFailed: in.OnFailed,
	})
}
