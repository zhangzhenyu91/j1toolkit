// office 文档源桥（office.SafedayFiles 能力实现）：sdr 源——安全日记录产物（docx）签发/回源。
// 签发期（Resolve）校验记录存在、已生成（done）与班组归属（canAccess 同口径）并定名；
// 回源期（Fetch）按签名票据 id 取产物字节（产物未落盘报错，不返回空文件）。
package safeday

import (
	"context"
	"net/http"
	"os"
	"strings"

	"shade/internal/apps/office"
	"shade/internal/auth"
	"shade/internal/httpapi"
)

// officeCodeBadTicket 票据类错误业务码（office.codeBadTicket 同值，包外无法引用）。
const officeCodeBadTicket = 40311

// officeCodeNotFound 「不存在」业务码（office.codeNotFound 同值，包外无法引用）。
const officeCodeNotFound = 40400

// NewSafedayFiles 安全日记录产物能力（office.SafedayFiles；sdr 源）。
func NewSafedayFiles(m *Module) office.SafedayFiles { return &safedayFiles{m: m} }

// safedayFiles sdr 源能力实现（复用 RecordStore 查询与 RecordFilePath 产物路径）。
type safedayFiles struct{ m *Module }

// load 取记录并校验：Resolve 带身份做归属校验；Fetch 仅按签名票据 id（票据即授权）。
// 记录不存在/未生成/产物未落盘均按业务错误返回，绝不返回空产物。
func (s *safedayFiles) load(ctx context.Context, ref office.SdrRef, ident auth.Identity, atIssue bool) (Record, error) {
	id := strings.TrimSpace(ref.ID)
	if id == "" {
		if atIssue {
			return Record{}, &office.RefError{Message: "引用参数错误"}
		}
		return Record{}, &office.RefError{Status: http.StatusUnauthorized, Code: officeCodeBadTicket,
			Message: "回源票据无效或已过期"}
	}
	record, ok := s.m.Store.Get(id)
	if !ok {
		return Record{}, &office.RefError{Status: http.StatusNotFound, Code: officeCodeNotFound, Message: "记录不存在"}
	}
	if record.Status != "done" {
		return Record{}, &office.RefError{Status: http.StatusNotFound, Code: officeCodeNotFound, Message: "记录不存在或文件尚未生成"}
	}
	if atIssue && !s.canAccess(ctx, ident, record) {
		return Record{}, &office.RefError{Status: http.StatusForbidden, Code: 40304, Message: "无权访问其他班组的记录"}
	}
	path := s.m.Store.RecordFilePath(record)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return Record{}, &office.RefError{Status: http.StatusNotFound, Code: officeCodeNotFound, Message: "文件不存在，可能已被清理"}
	}
	return record, nil
}

// canAccess 归属校验（canAccess 同口径）：超管任意班组；其余角色仅本班（按班组名比对）。
func (s *safedayFiles) canAccess(ctx context.Context, ident auth.Identity, record Record) bool {
	if ident.Role == "admin" {
		return true
	}
	if s.m.DB == nil {
		return false
	}
	team, err := httpapi.ResolveTeam(ctx, s.m.DB,
		httpapi.IdentityAdapter{TeamID: ident.TeamID, Role: ident.Role}, 0)
	if err != nil {
		return false
	}
	return record.Team == team.Name
}

// Resolve 签发期校验：记录存在、已生成、班组归属并返回产物展示名（record.FileName）。
func (s *safedayFiles) Resolve(ctx context.Context, ident auth.Identity, ref office.SdrRef) (string, error) {
	record, err := s.load(ctx, ref, ident, true)
	if err != nil {
		return "", err
	}
	name := record.FileName
	if name == "" {
		name = record.Date + ".docx"
	}
	return name, nil
}

// Fetch 回源取产物字节（票据即授权；越权限制在签发期完成，此处按票据 id 取数）。
func (s *safedayFiles) Fetch(ctx context.Context, ref office.SdrRef) ([]byte, error) {
	record, err := s.load(ctx, ref, auth.Identity{}, false)
	if err != nil {
		return nil, err
	}
	buf, err := os.ReadFile(s.m.Store.RecordFilePath(record))
	if err != nil {
		return nil, office.ErrDocNotFound
	}
	return buf, nil
}
