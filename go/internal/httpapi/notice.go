package httpapi

// 站内通知读取：GET /api/v1/notice/list 的表结构与 SQL 实现（迁移 server/src/notice 的可见性口径）。
// 可见性 = 角色命中 targets（角色数组）或 本人命中 user_ids（按人投放）；本人已删（sys_notice_del）不出现。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// NoticeSchemaSQL 通知三表（幂等建表；与 server/src/notice/schema.js 一致）。
var NoticeSchemaSQL = []string{
	`CREATE TABLE IF NOT EXISTS sys_notice (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    title VARCHAR(128) NOT NULL COMMENT '通知标题',
    content TEXT NOT NULL COMMENT '通知内容',
    targets JSON NOT NULL COMMENT '推送对象（角色数组，取值子集 ["admin","team_admin","user"]；空数组=仅按人投放）',
    user_ids JSON NULL COMMENT '指定接收人（sys_user.id 数组）；NULL=不按人投放',
    created_by BIGINT UNSIGNED NULL COMMENT '推送人，关联 sys_user.id；NULL=系统',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_created (created_at)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS sys_notice_read (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    notice_id BIGINT UNSIGNED NOT NULL COMMENT '通知，关联 sys_notice.id',
    user_id BIGINT UNSIGNED NOT NULL COMMENT '已读用户，关联 sys_user.id',
    read_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_notice_user (notice_id, user_id),
    KEY idx_user (user_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS sys_notice_del (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    notice_id BIGINT UNSIGNED NOT NULL COMMENT '通知，关联 sys_notice.id',
    user_id BIGINT UNSIGNED NOT NULL COMMENT '删除用户，关联 sys_user.id',
    del_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_notice_user (notice_id, user_id),
    KEY idx_user (user_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
}

// EnsureNoticeSchema 建通知表（幂等）+ 老库补列（sys_notice.user_ids，缺列才补）。
func EnsureNoticeSchema(ctx context.Context, db *sql.DB) error {
	for _, ddl := range NoticeSchemaSQL {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	var name string
	err := db.QueryRowContext(ctx,
		`SELECT COLUMN_NAME FROM information_schema.COLUMNS
		 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'sys_notice' AND COLUMN_NAME = 'user_ids'`).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := db.ExecContext(ctx,
			`ALTER TABLE sys_notice ADD COLUMN user_ids JSON NULL COMMENT '指定接收人（sys_user.id 数组）；NULL=不按人投放' AFTER targets`); err != nil {
			return err
		}
		return nil
	}
	return err
}

// noticeVisibleSQL 可见性 SQL 片段（别名固定 n）；占位参数 [role, userID 字符串]。
const noticeVisibleSQL = `(JSON_CONTAINS(n.targets, JSON_QUOTE(?)) OR JSON_CONTAINS(n.user_ids, CAST(? AS JSON)))`

// SQLNoticeReader 通知读取的 MySQL 实现。
type SQLNoticeReader struct{ DB *sql.DB }

// ListNotices 当前用户可见通知（倒序 + read 标记 + 未读总数；limit 1~200）。
func (r SQLNoticeReader) ListNotices(ctx context.Context, userID int64, role string, limit int) (NoticePage, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	uidText := strconv.FormatInt(userID, 10)
	rows, err := r.DB.QueryContext(ctx,
		`SELECT n.id, n.title, n.content, n.targets,
		        DATE_FORMAT(n.created_at, '%Y-%m-%d %H:%i:%s') AS created_at,
		        u.nickname AS created_by_name,
		        IF(rd.id IS NULL, 0, 1) AS is_read
		 FROM sys_notice n
		 LEFT JOIN sys_notice_read rd ON rd.notice_id = n.id AND rd.user_id = ?
		 LEFT JOIN sys_notice_del d ON d.notice_id = n.id AND d.user_id = ?
		 LEFT JOIN sys_user u ON u.id = n.created_by
		 WHERE `+noticeVisibleSQL+` AND d.id IS NULL
		 ORDER BY n.created_at DESC, n.id DESC
		 LIMIT ?`, userID, userID, role, uidText, limit)
	if err != nil {
		return NoticePage{}, DependencyUnavailable("通知列表读取失败")
	}
	defer rows.Close()
	page := NoticePage{Items: make([]NoticeItem, 0)}
	for rows.Next() {
		var it NoticeItem
		var targetsRaw []byte
		var createdAt, createdByName sql.NullString
		if err := rows.Scan(&it.ID, &it.Title, &it.Content, &targetsRaw, &createdAt, &createdByName, &it.Read); err != nil {
			return NoticePage{}, err
		}
		it.Targets = parseTargetsJSON(targetsRaw)
		it.CreatedAt = createdAt.String
		it.CreatedBy = createdByName.String
		if it.CreatedBy == "" {
			it.CreatedBy = "系统"
		}
		page.Items = append(page.Items, it)
	}
	if err := rows.Err(); err != nil {
		return NoticePage{}, err
	}
	if err := r.DB.QueryRowContext(ctx,
		`SELECT COUNT(*)
		 FROM sys_notice n
		 LEFT JOIN sys_notice_read rd ON rd.notice_id = n.id AND rd.user_id = ?
		 LEFT JOIN sys_notice_del d ON d.notice_id = n.id AND d.user_id = ?
		 WHERE `+noticeVisibleSQL+` AND rd.id IS NULL AND d.id IS NULL`,
		userID, userID, role, uidText).Scan(&page.Unread); err != nil {
		return NoticePage{}, DependencyUnavailable("通知未读数读取失败")
	}
	return page, nil
}

// parseTargetsJSON targets JSON 列防御解析（可能返回字符串或已解析对象；非数组归空）。
func parseTargetsJSON(raw []byte) []string {
	out := []string{}
	if len(raw) == 0 {
		return out
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err != nil {
		return out
	}
	return append(out, arr...)
}

// noticeList GET /api/v1/notice/list?limit= → data:{items,unread}
func (a *api) noticeList(w http.ResponseWriter, r *http.Request) {
	if a.d.Notices == nil {
		writeErr(w, DependencyUnavailable("通知数据未接入（数据库未配置）"))
		return
	}
	ident, ok := IdentityFromContext(r.Context())
	if !ok {
		writeErr(w, &APIError{Code: "unauthorized", Message: "未登录或登录已过期", Status: http.StatusUnauthorized})
		return
	}
	limit := 50
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	page, err := a.d.Notices.ListNotices(r.Context(), ident.ID, ident.Role, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, page)
}

// 编译期接口一致性检查。
var _ NoticeReader = SQLNoticeReader{}
