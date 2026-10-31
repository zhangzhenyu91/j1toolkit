// 登录安全事件：登录成功/失败与微信绑定成功写 category=security 的结构化事件，
// 只记用户名/来源/归属，绝不记录密码、openid、session_key 或 JWT。
package authwx

import (
	"context"

	"shade/internal/logging"
	"shade/internal/logsafe"
)

// security 写一条安全事件（Events 未注入时静默；自由文本脱敏；归属从字段回填 ctx）。
func (m *Module) security(ctx context.Context, level, event, messageCN, raw string, fields map[string]any) {
	if m == nil || m.Events == nil {
		return
	}
	userID, _ := fields["user_id"].(int64)
	teamID, _ := fields["team_id"].(int64)
	if userID != 0 || teamID != 0 {
		ctx = logging.WithActor(ctx, userID, teamID)
	}
	_ = m.Events.Record(ctx, level, "security", event, messageCN, logsafe.TextForLog(raw), fields)
}
