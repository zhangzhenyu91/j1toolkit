package crossing

// 线路归一接入：读（列表/导出/同步/知识库）、写（手动新增编辑/直推/暂存导入/内网回写）
// 统一按本班 team_line + team_line_link 匹配；命中取规范名与电压，未命中保持原文。

import (
	"context"

	"shade/internal/linecatalog"
)

// loadLineCatalog 读取本班线路目录；失败返回 nil（调用方按原文处理）。
func (m *Module) loadLineCatalog(ctx context.Context, teamID int64) *linecatalog.Catalog {
	cat, err := linecatalog.Load(ctx, m.DB, teamID)
	if err != nil {
		return nil
	}
	return cat
}

// canonicalizeRecordLines 读后归一：列表/导出/同步统一展示规范线路名。
func (m *Module) canonicalizeRecordLines(ctx context.Context, teamID int64, records []Record) {
	if len(records) == 0 {
		return
	}
	cat := m.loadLineCatalog(ctx, teamID)
	if cat == nil {
		return
	}
	for i := range records {
		if match := cat.Resolve(records[i].LineName); match.Status == "ok" {
			records[i].LineName = match.Name
		}
	}
}

// resolveLineName 写入前归一：命中返回规范名与电压；未命中返回原文与空电压。
func (m *Module) resolveLineName(ctx context.Context, teamID int64, raw string) (string, string) {
	cat := m.loadLineCatalog(ctx, teamID)
	if cat == nil {
		return raw, ""
	}
	match := cat.Resolve(raw)
	if match.Status != "ok" || match.Name == "" {
		return raw, ""
	}
	return match.Name, match.Voltage
}

// canonicalizeIngestRecords 直推/暂存记录写入前归一（线路名 + 空电压补全）。
func (m *Module) canonicalizeIngestRecords(ctx context.Context, teamID int64, records []*ingestRecord) {
	if len(records) == 0 {
		return
	}
	cat := m.loadLineCatalog(ctx, teamID)
	if cat == nil {
		return
	}
	for _, rec := range records {
		if rec == nil {
			continue
		}
		match := cat.Resolve(rec.LineName)
		if match.Status != "ok" || match.Name == "" {
			continue
		}
		rec.LineName = match.Name
		if rec.VoltageLevel == "" {
			rec.VoltageLevel = match.Voltage
		}
	}
}

// canonicalizeIntranetRows 内网表行在差异比对前归一，避免同一线路因写法差异反复冲突。
func (m *Module) canonicalizeIntranetRows(ctx context.Context, teamID int64, rows []map[string]any) {
	if len(rows) == 0 {
		return
	}
	cat := m.loadLineCatalog(ctx, teamID)
	if cat == nil {
		return
	}
	for _, row := range rows {
		raw := normStr(row["line_name"])
		if raw == "" {
			continue
		}
		if match := cat.Resolve(raw); match.Status == "ok" {
			row["line_name"] = match.Name
		}
	}
}
