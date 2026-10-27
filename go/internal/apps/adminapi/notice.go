// 站内通知：已读 / 一键删已读 / 按人删除 / 超管推送（迁移 server/src/notice/index.js；
// GET /api/v1/notice/list 已由 httpapi 提供，本模块只补其余路由）。
// 可见性 = 角色命中 targets（角色数组）或 本人命中 user_ids（按人投放）；本人已删（sys_notice_del）不出现。
// 手动推送不发微信外发（Node 同口径：微信外发仅系统自动触发的通知使用）。
package adminapi

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// noticeVisibleSQL 可见性 SQL 片段（别名固定 n）；占位参数 [role, userID 字符串]。
// 与 httpapi.noticeVisibleSQL 同口径（列表 / 未读计数 / 标记已读 / 一键删已读 / 按人隐藏共用）。
const noticeVisibleSQL = `(JSON_CONTAINS(n.targets, JSON_QUOTE(?)) OR JSON_CONTAINS(n.user_ids, CAST(? AS JSON)))`

// noticeRead POST /api/v1/notice/{id}/read → data:null
// 标记单条已读（仅当前用户可见且未删的通知，不可见按不存在处理 40403）。
func (m *Module) noticeRead(w http.ResponseWriter, r *http.Request) {
	ident, ok := m.ident(w, r)
	if !ok {
		return
	}
	noticeID := pathID(r)
	uidText := strconv.FormatInt(ident.ID, 10)
	var found int64
	err := m.DB.QueryRowContext(r.Context(),
		`SELECT id FROM sys_notice n WHERE n.id = ?
		 AND `+noticeVisibleSQL+`
		 AND NOT EXISTS (SELECT 1 FROM sys_notice_del d WHERE d.notice_id = n.id AND d.user_id = ?)`,
		noticeID, ident.Role, uidText, ident.ID).Scan(&found)
	if err == sql.ErrNoRows {
		fail(w, http.StatusNotFound, codeNoticeMiss, "通知不存在")
		return
	}
	if err != nil {
		writeErr(w, DependencyUnavailableMsg("通知读取失败"))
		return
	}
	if _, err := m.DB.ExecContext(r.Context(),
		`INSERT IGNORE INTO sys_notice_read (notice_id, user_id) VALUES (?, ?)`, noticeID, ident.ID); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, nil)
}

// noticeReadAll POST /api/v1/notice/read-all → data:null
// 当前用户可见通知全部标记已读（本人已删跳过）。
func (m *Module) noticeReadAll(w http.ResponseWriter, r *http.Request) {
	ident, ok := m.ident(w, r)
	if !ok {
		return
	}
	uidText := strconv.FormatInt(ident.ID, 10)
	if _, err := m.DB.ExecContext(r.Context(),
		`INSERT IGNORE INTO sys_notice_read (notice_id, user_id)
		 SELECT n.id, ? FROM sys_notice n
		 WHERE `+noticeVisibleSQL+`
		   AND NOT EXISTS (SELECT 1 FROM sys_notice_del d WHERE d.notice_id = n.id AND d.user_id = ?)`,
		ident.ID, ident.Role, uidText, ident.ID); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, nil)
}

// noticeDelRead POST /api/v1/notice/del-read → data:{deleted}
// 一键删除已读：本人已读且可见的通知全部按人删除（超管同样仅按人删除，不动其他成员未读）。
func (m *Module) noticeDelRead(w http.ResponseWriter, r *http.Request) {
	ident, ok := m.ident(w, r)
	if !ok {
		return
	}
	uidText := strconv.FormatInt(ident.ID, 10)
	res, err := m.DB.ExecContext(r.Context(),
		`INSERT IGNORE INTO sys_notice_del (notice_id, user_id)
		 SELECT n.id, ? FROM sys_notice n
		 JOIN sys_notice_read rd ON rd.notice_id = n.id AND rd.user_id = ?
		 WHERE `+noticeVisibleSQL+`
		   AND NOT EXISTS (SELECT 1 FROM sys_notice_del d WHERE d.notice_id = n.id AND d.user_id = ?)`,
		ident.ID, ident.ID, ident.Role, uidText, ident.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	deleted, _ := res.RowsAffected()
	writeOK(w, map[string]any{"deleted": deleted}, fmt.Sprintf("已删除 %d 条已读通知", deleted))
}

// noticePush POST /api/v1/notice/push → data:{id}
// 超管推送（Node requireAdmin）；targets 为 ROLES 非空子集；content 支持 Markdown 原文。
func (m *Module) noticePush(w http.ResponseWriter, r *http.Request) {
	ident, ok := m.ident(w, r)
	if !ok {
		return
	}
	if !m.requireAdmin(w, ident) {
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeNoticeInput, "请求体不是合法 JSON")
		return
	}
	title := rawString(body["title"])
	content := rawString(body["content"])
	if title == "" || content == "" {
		fail(w, http.StatusBadRequest, codeNoticeInput, "标题/内容不能为空")
		return
	}
	if len([]rune(title)) > 128 {
		fail(w, http.StatusBadRequest, codeNoticeInput, "标题不能超过 128 字")
		return
	}
	targets, ok := parseTargetsField(body["targets"])
	if !ok {
		fail(w, http.StatusBadRequest, codeNoticeTarget, "推送对象不合法")
		return
	}
	targetsJSON, err := json.Marshal(targets)
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := m.DB.ExecContext(r.Context(),
		`INSERT INTO sys_notice (title, content, targets, user_ids, created_by) VALUES (?, ?, ?, NULL, ?)`,
		title, content, string(targetsJSON), ident.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	m.logOp(r.Context(), "admin.notice.push", "推送站内通知", "admin notice pushed",
		map[string]any{"notice_id": id, "targets": targets})
	writeOK(w, map[string]any{"id": id}, "推送成功")
}

// noticeDelete DELETE /api/v1/notice/{id} → data:null
// 超管全局删除（连带清空已读与按人删除记录）；其余角色仅对本人隐藏（写 sys_notice_del）。
func (m *Module) noticeDelete(w http.ResponseWriter, r *http.Request) {
	ident, ok := m.ident(w, r)
	if !ok {
		return
	}
	noticeID := pathID(r)
	if ident.Role == "admin" {
		res, err := m.DB.ExecContext(r.Context(), `DELETE FROM sys_notice WHERE id = ?`, noticeID)
		if err != nil {
			writeErr(w, err)
			return
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			fail(w, http.StatusNotFound, codeNoticeMiss, "通知不存在")
			return
		}
		for _, q := range []string{
			`DELETE FROM sys_notice_read WHERE notice_id = ?`,
			`DELETE FROM sys_notice_del WHERE notice_id = ?`,
		} {
			if _, err := m.DB.ExecContext(r.Context(), q, noticeID); err != nil {
				writeErr(w, err)
				return
			}
		}
		m.logOp(r.Context(), "admin.notice.delete", "删除站内通知", "admin notice deleted",
			map[string]any{"notice_id": noticeID})
		writeOK(w, nil, "删除成功")
		return
	}
	uidText := strconv.FormatInt(ident.ID, 10)
	var found int64
	err := m.DB.QueryRowContext(r.Context(),
		`SELECT id FROM sys_notice n WHERE n.id = ? AND `+noticeVisibleSQL,
		noticeID, ident.Role, uidText).Scan(&found)
	if err == sql.ErrNoRows {
		fail(w, http.StatusNotFound, codeNoticeMiss, "通知不存在")
		return
	}
	if err != nil {
		writeErr(w, DependencyUnavailableMsg("通知读取失败"))
		return
	}
	if _, err := m.DB.ExecContext(r.Context(),
		`INSERT IGNORE INTO sys_notice_del (notice_id, user_id) VALUES (?, ?)`, noticeID, ident.ID); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, nil, "删除成功")
}

// parseTargetsField 校验推送对象（ROLES 非空子集，元素为字符串且不重复；Node every/includes 口径）。
func parseTargetsField(raw []byte) ([]string, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, "[") {
		return nil, false
	}
	var arr []any
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, false
	}
	if len(arr) == 0 {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	seen := map[string]bool{}
	for _, v := range arr {
		s, isStr := v.(string)
		if !isStr || !isRole(s) {
			return nil, false
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out, true
}
