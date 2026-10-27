// office 文档源桥（office.SheetBuilder / office.RemarkFiles 能力实现）：
// wls 源（工作任务单 / 费用汇总 / 派车汇总）与 wlr 源（备注附件）经此供 office.Module 签发/回源。
// 签发期（Resolve）做生成前权限（班管/超管）、班组归属、日期范围与 task/fee 汇总前核验并定名；
// 回源期（Fetch）按签名票据参数复验范围/班组（限制越权）后即时生成 / COS 回源，不返回空产物。
package worklog

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"shade/internal/apps/office"
	"shade/internal/auth"
	"shade/internal/httpapi"
)

// officeCodeBadTicket 票据类错误业务码（office.codeBadTicket 同值，包外无法引用）。
const officeCodeBadTicket = 40311

// errOfficeTicket 回源票据参数复验失败（签发侧已核验，此处仅拦脏票据/越权参数）。
var errOfficeTicket = &office.RefError{Status: http.StatusUnauthorized, Code: officeCodeBadTicket,
	Message: "回源票据无效或已过期"}

// bridgeReq 供既有汇总/附件 helper 复用的请求壳（这些 helper 仅使用 r.Context()）。
func bridgeReq(ctx context.Context) *http.Request {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://office-bridge.invalid/", nil)
	if err != nil {
		return &http.Request{}
	}
	return req
}

// bridgeTeam 桥侧班组解析（Resolve 期）：want<=0 回落签发人所属班组；非超管仅允许本班（防越权）。
func bridgeTeam(ctx context.Context, db *sql.DB, ident auth.Identity, want int64) (httpapi.Team, error) {
	if want <= 0 {
		want = ident.TeamID
	}
	if ident.Role != "admin" && want != ident.TeamID {
		return httpapi.Team{}, &office.RefError{Status: http.StatusForbidden, Code: codeForbidden,
			Message: "无权访问其他班组的数据"}
	}
	team, err := httpapi.ResolveTeam(ctx, db, httpapi.IdentityAdapter{TeamID: ident.TeamID, Role: ident.Role}, want)
	if err != nil {
		return httpapi.Team{}, &office.RefError{Status: http.StatusBadRequest, Code: codeNoTeam, Message: "无可用班组"}
	}
	return team, nil
}

// NewSheetBuilder 出工日志工作单产物能力（office.SheetBuilder；wls 源）。
func NewSheetBuilder(m *Module) office.SheetBuilder { return &sheetBuilder{m: m} }

// sheetBuilder wls 源能力实现（复用汇总表生成链路：sheetRange / loadSheetRows / buildXxxSheet）。
type sheetBuilder struct{ m *Module }

// sheetParams 票据参数校验（Resolve/Fetch 共用）：sheet 种类 + 日期范围（31 天内）。
func sheetParams(ref office.WlsRef) (from, to string, ok bool) {
	if ref.Sheet != "task" && ref.Sheet != "fee" && ref.Sheet != "dispatch" {
		return "", "", false
	}
	from, to, errCode := sheetRange(map[string]string{"from": ref.From, "to": ref.To})
	if errCode != 0 {
		return "", "", false
	}
	return from, to, true
}

// emptySheetMsg 无可生成内容文案（serveSheetPreview / serveSheet 同口径）。
func emptySheetMsg(kind string, preview bool) string {
	if preview {
		switch kind {
		case "fee":
			return "该日期范围没有出车记录，无可生成的汇总"
		case "dispatch":
			return "该日期范围没有派车单记录，无可生成的汇总"
		}
		return "该日期范围没有出车记录，无可生成的卡片"
	}
	return "该日期范围没有出车记录，无可生成的内容"
}

// Resolve 签发期校验：生成前权限（班管/超管）→ 班组归属 → 日期范围 → task/fee 汇总前核验 →
// 有无可生成内容（预检，避免回源拉到空产物）；返回产物展示名（sheetFileName 同口径）。
func (b *sheetBuilder) Resolve(ctx context.Context, ident auth.Identity, ref office.WlsRef) (string, error) {
	from, to, ok := sheetParams(ref)
	if !ok {
		return "", &office.RefError{Message: "参数错误：sheet（task/fee/dispatch）或日期范围"}
	}
	team, err := bridgeTeam(ctx, b.m.DB, ident, ref.Team)
	if err != nil {
		return "", err
	}
	// 生成前权限：超管任意班组；班组管理员仅本班（requireDictAdmin 同口径）
	if ident.Role != "admin" && !(ident.Role == "team_admin" && ident.TeamID == team.ID) {
		return "", &office.RefError{Status: http.StatusForbidden, Code: codeForbidden, Message: "仅管理员可执行此操作"}
	}
	req := bridgeReq(ctx)
	if ref.Sheet != "dispatch" && !ref.Force {
		if failures := b.m.sheetVerifyFailures(req, team.ID, from, to); len(failures) > 0 {
			return "", &office.RefError{Status: http.StatusConflict, Code: codeVerifyFailed,
				Message: "所选范围内有 " + strconv.Itoa(len(failures)) + " 条记录存在未通过项，请处理后再操作"}
		}
	}
	if !b.m.sheetHasRows(req, team.ID, from, to, ref.Sheet) {
		return "", &office.RefError{Status: http.StatusNotFound, Code: codeNoPhotos, Message: emptySheetMsg(ref.Sheet, true)}
	}
	return sheetFileName(ref.Sheet, from, to), nil
}

// Fetch 回源即时生成产物（票据即授权，签发侧已核验）：按票据参数复验范围与班组后走汇总表生成链路；
// 无内容报错而非空产物（serveSheet 同口径）。
func (b *sheetBuilder) Fetch(ctx context.Context, ref office.WlsRef) ([]byte, error) {
	from, to, ok := sheetParams(ref)
	if !ok || ref.Team <= 0 {
		return nil, errOfficeTicket
	}
	team, err := httpapi.TeamByID(ctx, b.m.DB, ref.Team)
	if err != nil {
		return nil, &office.RefError{Status: http.StatusBadRequest, Code: codeNoTeam, Message: "无可用班组"}
	}
	req := bridgeReq(ctx)
	var buf []byte
	switch ref.Sheet {
	case "task":
		var rows []map[string]string
		rows, err = b.m.loadSheetRows(req, team.ID, from, to)
		if err == nil {
			buf, err = b.m.buildTaskSheet(team.Name, rows)
		}
	case "fee":
		buf, err = b.m.buildFeeSheet(req, team.ID, from, to)
	case "dispatch":
		buf, err = b.m.buildDispatchSheet(req, team.ID, from, to)
	}
	if err != nil {
		return nil, err
	}
	if len(buf) == 0 {
		return nil, &office.RefError{Status: http.StatusNotFound, Code: codeNoPhotos, Message: emptySheetMsg(ref.Sheet, false)}
	}
	return buf, nil
}

// NewRemarkFiles 出工日志备注附件能力（office.RemarkFiles；wlr 源）。
func NewRemarkFiles(m *Module) office.RemarkFiles { return &remarkFiles{m: m} }

// remarkFiles wlr 源能力实现（复用 findRemarkFile 可见性与 fetchObjectBytes COS 回源）。
type remarkFiles struct{ m *Module }

// check 票据参数校验：entryID/key 必填；Resolve 期回落签发人班组并做归属校验，
// Fetch 期仅按签名票据参数（id/key/team 齐全且自洽，限制越权）。
func (r *remarkFiles) check(ctx context.Context, ident *auth.Identity, ref office.WlrRef, fetch bool) (entryID int64, key string, teamID int64, err error) {
	entryID, perr := strconv.ParseInt(strings.TrimSpace(ref.ID), 10, 64)
	key = strings.TrimSpace(ref.Key)
	bad := perr != nil || entryID <= 0 || key == ""
	if fetch {
		if bad || ref.Team <= 0 {
			return 0, "", 0, errOfficeTicket
		}
		return entryID, key, ref.Team, nil
	}
	if bad {
		return 0, "", 0, &office.RefError{Message: "引用参数错误"}
	}
	team, terr := bridgeTeam(ctx, r.m.DB, *ident, ref.Team)
	if terr != nil {
		return 0, "", 0, terr
	}
	return entryID, key, team.ID, nil
}

// Resolve 签发期校验：附件存在 + 卡片可见（entryVisibleSQL 口径）+ 班组归属；返回附件展示名。
func (r *remarkFiles) Resolve(ctx context.Context, ident auth.Identity, ref office.WlrRef) (string, error) {
	entryID, key, teamID, err := r.check(ctx, &ident, ref, false)
	if err != nil {
		return "", err
	}
	file, found, exists := r.m.findRemarkFile(bridgeReq(ctx), entryID, key, teamID)
	if !exists {
		return "", &office.RefError{Status: http.StatusNotFound, Code: codeMissing, Message: "日志不存在"}
	}
	if !found {
		return "", &office.RefError{Status: http.StatusNotFound, Code: codeMissing, Message: "附件不存在"}
	}
	if file.Name == "" {
		return "附件", nil
	}
	return file.Name, nil
}

// Fetch 回源取附件字节（COS 回源：GetBuffer 优先、公开地址回退，fetchObjectBytes 同口径）。
func (r *remarkFiles) Fetch(ctx context.Context, ref office.WlrRef) ([]byte, error) {
	entryID, key, teamID, err := r.check(ctx, nil, ref, true)
	if err != nil {
		return nil, err
	}
	file, found, exists := r.m.findRemarkFile(bridgeReq(ctx), entryID, key, teamID)
	if !exists || !found {
		return nil, &office.RefError{Status: http.StatusNotFound, Code: codeMissing, Message: "附件不存在"}
	}
	return fetchObjectBytes(ctx, r.m.Store, file.CosKey, r.m.photoURL(file.CosKey, file.URL))
}
