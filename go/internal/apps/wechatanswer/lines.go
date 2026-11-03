// 线路名归一与电压读取：报送原文 → 归一（去电压前缀/空白、罗马数字统一 Ⅰ/Ⅱ/Ⅲ、12 → ⅠⅡ）
// → 与本班 team_line + team_line_link 匹配，命中取规范线路名与电压等级；
// 未命中保留归一原文（line_name_raw）不带电压。归一/别名匹配统一由 linecatalog 提供。
package wechatanswer

import (
	"context"
	"database/sql"
	"strings"

	"shade/internal/linecatalog"
)

// TeamLine 本班运维线路（team_line 一行）。
type TeamLine struct {
	Voltage string // 电压等级数字串（如 "110"）
	Name    string // 线路名称
}

// LineSource 线路表来源（测试可注入；nil 时直接读 team_line + team_line_link）。
type LineSource interface {
	TeamLines(ctx context.Context, teamID int64) ([]TeamLine, error)
}

// TeamSource 班组名来源（默认 SQLTeamSource 读 sys_team）。
type TeamSource interface {
	TeamName(ctx context.Context, teamID int64) (string, error)
}

// SQLTeamSource 读 sys_team（启用班组名）。
type SQLTeamSource struct {
	DB *sql.DB
}

var _ TeamSource = (*SQLTeamSource)(nil)

// TeamName 班组名（无匹配返回空串）。
func (s SQLTeamSource) TeamName(ctx context.Context, teamID int64) (string, error) {
	if s.DB == nil || teamID <= 0 {
		return "", nil
	}
	var name string
	err := s.DB.QueryRowContext(ctx,
		`SELECT name FROM sys_team WHERE id = ? AND status = 1`, teamID).Scan(&name)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(name), nil
}

// NormalizeLineName 报送原文线路名归一（口径见 linecatalog.Normalize）。
func NormalizeLineName(raw string) string { return linecatalog.Normalize(raw) }

// LineMatch 线路匹配结果。
type LineMatch struct {
	Status     string     // ok | ambiguous | unmatched
	Name       string     // 命中（或归一后）线路名
	Voltage    string     // 命中线路电压（未命中为空）
	Candidates []TeamLine // ambiguous 候选
}

// MatchLine 线路名匹配：精确 → 补「线」exact → 双向前缀唯一候选（无关联别名）。
func MatchLine(raw string, lines []TeamLine) LineMatch {
	return convertLineMatch(linecatalog.MatchLines(raw, toCatalogLines(lines)))
}

// resolveLine 线路归一 + 线路目录匹配（teamID<=0 或查表失败 → 仅归一）。
func (m *Module) resolveLine(ctx context.Context, teamID int64, raw string) LineMatch {
	name := NormalizeLineName(raw)
	if name == "" {
		return LineMatch{Status: "unmatched"}
	}
	if m.Lines != nil {
		lines, err := m.Lines.TeamLines(ctx, teamID)
		if err != nil || len(lines) == 0 {
			return LineMatch{Status: "unmatched", Name: name}
		}
		return MatchLine(raw, lines)
	}
	if m.DB == nil || teamID <= 0 {
		return LineMatch{Status: "unmatched", Name: name}
	}
	cat, err := linecatalog.Load(ctx, m.DB, teamID)
	if err != nil {
		return LineMatch{Status: "unmatched", Name: name}
	}
	return convertLineMatch(cat.Resolve(raw))
}

// toCatalogLines TeamLine → linecatalog.Line。
func toCatalogLines(lines []TeamLine) []linecatalog.Line {
	out := make([]linecatalog.Line, 0, len(lines))
	for _, l := range lines {
		out = append(out, linecatalog.Line{Voltage: l.Voltage, Name: l.Name})
	}
	return out
}

// convertLineMatch linecatalog.Match → LineMatch（保持模块既有契约）。
func convertLineMatch(m linecatalog.Match) LineMatch {
	out := LineMatch{Status: m.Status, Name: m.Name, Voltage: m.Voltage}
	if len(m.Candidates) > 0 {
		out.Candidates = make([]TeamLine, 0, len(m.Candidates))
		for _, l := range m.Candidates {
			out.Candidates = append(out.Candidates, TeamLine{Voltage: l.Voltage, Name: l.Name})
		}
	}
	return out
}

// teamSource 取班组名来源（注入优先；缺省 MySQL sys_team）。
func (m *Module) teamSource() TeamSource {
	if m.Teams != nil {
		return m.Teams
	}
	return SQLTeamSource{DB: m.DB}
}

// teamNameByID 班组名（teamID<=0 返回空）。
func (m *Module) teamNameByID(ctx context.Context, teamID int64) string {
	if teamID <= 0 {
		return ""
	}
	name, err := m.teamSource().TeamName(ctx, teamID)
	if err != nil {
		return ""
	}
	return name
}
