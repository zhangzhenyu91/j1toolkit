// wechatanswer.CaptureSink 适配：捕获载荷按 kind 直推 treehazard.Module.Ingest / crossing.Module.Ingest，
// 对接密钥取运行时配置（TREEHAZARD_INGEST_TOKEN / CROSSING_INGEST_TOKEN）并经 Ingest 内部校验
// （与 X-Ingest-Token 同口径，不提供绕过入口）；返回与 /ingest HTTP 路由一致的原 JSON 应答与状态码。
package bridge

import (
	"context"
	"encoding/json"
	"errors"

	"shade/internal/apps/crossing"
	"shade/internal/apps/treehazard"
	"shade/internal/apps/wechatanswer"
)

// CaptureSink 捕获出口（= wechatanswer.CaptureSink，main 装配 wam.Sink = NewCaptureSink(...)）。
type CaptureSink = wechatanswer.CaptureSink

// 载荷分派类型（与 wechatanswer.KindTree / KindCrossing 同值）。
const (
	KindTree     = wechatanswer.KindTree
	KindCrossing = wechatanswer.KindCrossing
)

// treeIngest 树障直推能力（treehazard.Module.Ingest 满足）。
type treeIngest interface {
	Ingest(ctx context.Context, token string, raw []byte) (*treehazard.IngestResult, error)
}

// crossIngest 交跨直推能力（crossing.Module.Ingest 满足）。
type crossIngest interface {
	Ingest(ctx context.Context, token string, raw []byte) (*crossing.IngestResult, error)
}

// ConfigGet 运行时配置读取（*config.Store 与各模块 ConfigReader 满足；测试可注入桩）。
type ConfigGet interface {
	Get(ctx context.Context, key string) (string, error)
}

// captureSink CaptureSink 实现。
type captureSink struct {
	tree  treeIngest
	cross crossIngest
	cfg   ConfigGet
}

// NewCaptureSink 构造捕获出口：tree/crossing 模块直推（nil 模块对应 kind 报未接入，进补推队列）。
func NewCaptureSink(tree *treehazard.Module, cross *crossing.Module, cfg ConfigGet) CaptureSink {
	s := &captureSink{cfg: cfg}
	if tree != nil {
		s.tree = tree
	}
	if cross != nil {
		s.cross = cross
	}
	return s
}

// tokenOf 读对接密钥（空/未配置 → 空串，Ingest 侧 fail-closed 报未配置）。
func tokenOf(ctx context.Context, cfg ConfigGet, key string) string {
	if cfg == nil {
		return ""
	}
	v, err := cfg.Get(ctx, key)
	if err != nil {
		return ""
	}
	return v
}

// Process 直推载荷：成功 = 2xx + /ingest 路由同款 JSON；业务失败同样返回原 JSON 与状态（err 恒 nil，
// 失败由非 2xx 状态表达，交 wechatanswer 入补推队列）。
func (s *captureSink) Process(ctx context.Context, kind string, payload []byte) (int, []byte, error) {
	switch kind {
	case KindTree:
		if s.tree == nil {
			return sinkFailJSON(503, 50301, "未配置对接密钥")
		}
		res, err := s.tree.Ingest(ctx, tokenOf(ctx, s.cfg, "TREEHAZARD_INGEST_TOKEN"), payload)
		if err != nil {
			return treeIngestFail(err)
		}
		if res.Mode == "direct" {
			return sinkOKJSON(map[string]any{
				"inserted": res.Inserted, "updated": res.Updated, "skipped": res.Skipped,
				"photos": res.Photos, "photos_failed": res.PhotosFailed, "deleted": res.Deleted,
			})
		}
		return sinkOKJSON(map[string]any{
			"batch_id": res.BatchID, "count": res.Count, "photos": res.Photos,
			"photos_failed": res.PhotosFailed, "deleted": res.Deleted,
		})
	case KindCrossing:
		if s.cross == nil {
			return sinkFailJSON(503, 50301, "未配置对接密钥")
		}
		res, err := s.cross.Ingest(ctx, tokenOf(ctx, s.cfg, "CROSSING_INGEST_TOKEN"), payload)
		if err != nil {
			return crossIngestFail(err)
		}
		if res.Mode == "direct" {
			return sinkOKJSON(map[string]any{
				"inserted": res.Inserted, "updated": res.Updated, "skipped": res.Skipped, "deleted": res.Deleted,
			})
		}
		return sinkOKJSON(map[string]any{"batch_id": res.BatchID, "count": res.Count, "deleted": res.Deleted})
	default:
		return sinkFailJSON(400, 40001, "参数错误：未知捕获类型")
	}
}

// sinkOKJSON 成功信封 {code:0,message:"ok",data}（treehazard/crossing writeOK 同口径）。
func sinkOKJSON(data map[string]any) (int, []byte, error) {
	return 200, mustJSON(map[string]any{"code": 0, "message": "ok", "data": data}), nil
}

// sinkFailJSON 失败信封 {code,message}（writeFail 同口径）。
func sinkFailJSON(status, code int, message string) (int, []byte, error) {
	return status, mustJSON(map[string]any{"code": code, "message": message}), nil
}

// mustJSON 序列化（map 键序不确定但语义等价；失败降级空对象，不抛错）。
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// treeIngestFail 树障直推错误 → 响应映射（treehazard writeIngestFail 同口径）。
func treeIngestFail(err error) (int, []byte, error) {
	var perr *treehazard.IngestParamError
	switch {
	case errors.Is(err, treehazard.ErrIngestNotConfigured):
		return sinkFailJSON(503, 50301, treehazard.ErrIngestNotConfigured.Error())
	case errors.Is(err, treehazard.ErrIngestToken):
		return sinkFailJSON(401, 40104, treehazard.ErrIngestToken.Error())
	case errors.Is(err, treehazard.ErrIngestBadJSON):
		return sinkFailJSON(400, 40001, treehazard.ErrIngestBadJSON.Error())
	case errors.As(err, &perr):
		return sinkFailJSON(400, 40001, perr.Error())
	case errors.Is(err, treehazard.ErrIngestTeamNotFound):
		return sinkFailJSON(404, 40400, treehazard.ErrIngestTeamNotFound.Error())
	default:
		return sinkFailJSON(500, 40001, err.Error())
	}
}

// crossIngestFail 交跨直推错误 → 响应映射（crossing writeIngestFail 同口径）。
func crossIngestFail(err error) (int, []byte, error) {
	var perr *crossing.IngestParamError
	switch {
	case errors.Is(err, crossing.ErrIngestNotConfigured):
		return sinkFailJSON(503, 50301, crossing.ErrIngestNotConfigured.Error())
	case errors.Is(err, crossing.ErrIngestToken):
		return sinkFailJSON(401, 40104, crossing.ErrIngestToken.Error())
	case errors.Is(err, crossing.ErrIngestBadJSON):
		return sinkFailJSON(400, 40001, crossing.ErrIngestBadJSON.Error())
	case errors.As(err, &perr):
		return sinkFailJSON(400, 40001, perr.Error())
	case errors.Is(err, crossing.ErrIngestTeamNotFound):
		return sinkFailJSON(404, 40400, crossing.ErrIngestTeamNotFound.Error())
	default:
		return sinkFailJSON(500, 50000, "服务器开小差了")
	}
}

// 编译期契约：捕获出口满足方法集。
var _ CaptureSink = (*captureSink)(nil)
