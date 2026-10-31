// 日志归属与后台链路：WithActor 把登录用户/班组写入 context，Logger / WriteEntry /
// DurableSink 写库时在条目未显式给出归属时自动补齐；WithNewTraceID 为后台任务
// 从父 context 派生新链路 ID，避免整条后台链路共用请求链路或空 trace。
package logging

import (
	"context"

	"shade/internal/config"
)

// actorContextKey context 中的日志归属键。
type actorContextKey struct{}

// actor 日志归属（0 表示未知）。
type actor struct {
	userID int64
	teamID int64
}

// WithActor 把日志归属写入 context；nil context 归一为 background。
func WithActor(ctx context.Context, userID, teamID int64) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, actorContextKey{}, actor{userID: userID, teamID: teamID})
}

// ActorFromContext 取 context 中的日志归属（缺失返回 0/0）。
func ActorFromContext(ctx context.Context) (userID, teamID int64) {
	if ctx == nil {
		return 0, 0
	}
	a, _ := ctx.Value(actorContextKey{}).(actor)
	return a.userID, a.teamID
}

// WithNewTraceID 从父 context 派生新链路 ID（保留其他 context 值）。
func WithNewTraceID(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return WithTraceID(ctx, NewTraceID())
}

// fillActor 条目未显式给出归属时从 context 补齐（显式值优先）。
func fillActor(e *config.AppLogEntry, ctx context.Context) {
	if e == nil {
		return
	}
	userID, teamID := ActorFromContext(ctx)
	if e.UserID == 0 {
		e.UserID = userID
	}
	if e.TeamID == 0 {
		e.TeamID = teamID
	}
}
