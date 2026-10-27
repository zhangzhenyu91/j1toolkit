// office 文档源桥（office.TeamExport 能力实现）：crs 源——交跨统计表 xlsx 签发/回源。
// 签发期（Resolve）校验班组归属（非超管仅本班）并定名；回源期（Fetch）按签名票据参数
// 即时生成统计表，与 GET /export 下载同口径（loadTeamRecords + fetchLineOrder + buildWorkbook）。
package crossing

import (
	"context"
	"database/sql"
	"net/http"

	"shade/internal/apps/office"
	"shade/internal/auth"
	"shade/internal/httpapi"
)

// officeCodeBadTicket 票据类错误业务码（office.codeBadTicket 同值，包外无法引用）。
const officeCodeBadTicket = 40311

// NewTeamExport 交跨统计表能力（office.TeamExport；crs 源）。
func NewTeamExport(m *Module) office.TeamExport { return &teamExport{m: m} }

// teamExport crs 源能力实现（复用 /export 导出链路）。
type teamExport struct{ m *Module }

// bridgeTeam 桥侧班组解析（Resolve 期）：want<=0 回落签发人所属班组；非超管仅允许本班（防越权）。
func bridgeTeam(ctx context.Context, db *sql.DB, ident auth.Identity, want int64) (httpapi.Team, error) {
	if want <= 0 {
		want = ident.TeamID
	}
	if ident.Role != "admin" && want != ident.TeamID {
		return httpapi.Team{}, &office.RefError{Status: http.StatusForbidden, Code: codeNotAdmin,
			Message: "无权访问其他班组的数据"}
	}
	team, err := httpapi.ResolveTeam(ctx, db, httpapi.IdentityAdapter{TeamID: ident.TeamID, Role: ident.Role}, want)
	if err != nil {
		return httpapi.Team{}, &office.RefError{Status: http.StatusBadRequest, Code: codeNoTeam, Message: "无可用班组"}
	}
	return team, nil
}

// Resolve 签发期校验班组归属并返回统计表展示名（exportFileName 同口径）。
func (e *teamExport) Resolve(ctx context.Context, ident auth.Identity, ref office.LineRef) (string, error) {
	team, err := bridgeTeam(ctx, e.m.DB, ident, ref.Team)
	if err != nil {
		return "", err
	}
	return exportFileName(team.Name, ref.Line), nil
}

// Fetch 回源即时生成统计表（票据即授权）：按票据参数复验班组后走 /export 导出链路。
func (e *teamExport) Fetch(ctx context.Context, ref office.LineRef) ([]byte, error) {
	if ref.Team <= 0 {
		return nil, &office.RefError{Status: http.StatusUnauthorized, Code: officeCodeBadTicket,
			Message: "回源票据无效或已过期"}
	}
	team, err := httpapi.TeamByID(ctx, e.m.DB, ref.Team)
	if err != nil {
		return nil, &office.RefError{Status: http.StatusBadRequest, Code: codeNoTeam, Message: "无可用班组"}
	}
	records, err := e.m.loadTeamRecords(team.ID)
	if err != nil {
		return nil, err
	}
	lineRows, err := fetchLineOrder(e.m.DB, team.ID)
	if err != nil {
		return nil, err
	}
	lineOrder := map[string]int{}
	for i, l := range lineRows {
		lineOrder[l.LineName] = i
	}
	if ref.Line != "" {
		filtered := make([]Record, 0, len(records))
		for _, rec := range records {
			if rec.LineName == ref.Line {
				filtered = append(filtered, rec)
			}
		}
		records = filtered
	}
	return buildWorkbook(team.Name, records, ref.Split, lineOrder)
}
