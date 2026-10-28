package exporter

import (
	"context"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/render"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/uvmpapi"
)

// OrderAPI 业务查询接口（*uvmpapi.Client 真实实现；devmock.FakeAPI / 测试 fake 同形状）。
// 按单号查询池、打印详情/轨迹数据预取池只依赖此接口。
type OrderAPI interface {
	QueryPcdByRunCode(ctx context.Context, code, dateHint string, lookbackDays int, log uvmpapi.LogFunc) (uvmpapi.Dict, bool, error)
	GetPrintDetails(ctx context.Context, ids []string, chunk int) ([]uvmpapi.Dict, error)
	QueryTrackPoints(ctx context.Context, vehicleID, begin, end string) ([]uvmpapi.Dict, error)
	QueryTrackSegment(ctx context.Context, vehicleID, begin, end string) (uvmpapi.Dict, error)
}

// Renderer 官方页面渲染器接口（*render.Worker 公开方法子集，测试以 fake 顶替）。
type Renderer interface {
	RenderPCDPDF(ctx context.Context, item map[string]any, outPDF, tag, diagDir string) error
	RenderRoutePNG(ctx context.Context, routeID, outPNG, preShotJS string) (render.RewriteResult, error)
	RenderPCDPNG(ctx context.Context, order map[string]any, outPNG, preShotJS string) (render.RewriteResult, error)
	PNGToPDF(ctx context.Context, pngPath, outPDF string) error
	Close() error
}

// RendererFactory 创建并完整官方登录一个渲染 worker。idx 用于独立 WorkDir
// （状态目录 browser-workers/job/worker-N）；creds 含令牌与 Generation。
type RendererFactory func(ctx context.Context, idx int, creds render.Credentials) (Renderer, error)

// CredsSource 凭证快照来源（auth.Manager 适配）：渲染池按 Generation 变化重启 worker。
type CredsSource interface {
	Snapshot() render.Credentials
}
