// 应用目录管理与按人应用授权：
//
//	GET    /api/v1/admin/apps            全部应用（含停用，同 Node）
//	PUT    /api/v1/admin/apps/{id}       启停 / 排序（Go 侧补齐，Node 只读）
//	GET    /api/v1/admin/users/{id}/apps 某员工已授权应用
//	PUT    /api/v1/admin/users/{id}/apps 设置授权（全量替换，事务）
package adminapi

import (
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// appRow 应用目录条目（字段同 Node GET /admin/apps）。
type appRow struct {
	ID       int64  `json:"id"`
	AppKey   string `json:"app_key"`
	Name     string `json:"name"`
	Icon     string `json:"icon"`
	Path     string `json:"path"`
	Terminal string `json:"terminal"`
	Sort     int    `json:"sort"`
	Status   int    `json:"status"`
}

// listApps GET /api/v1/admin/apps → data:{list:[...]}（按 sort, id 排序）。
func (m *Module) listApps(w http.ResponseWriter, r *http.Request) {
	if _, ok := m.ident(w, r); !ok {
		return
	}
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT id, app_key, name, icon, path, terminal, sort, status FROM sys_app ORDER BY sort, id`)
	if err != nil {
		writeErr(w, DependencyUnavailableMsg("应用列表读取失败"))
		return
	}
	defer rows.Close()
	list := make([]appRow, 0)
	for rows.Next() {
		var a appRow
		if err := rows.Scan(&a.ID, &a.AppKey, &a.Name, &a.Icon, &a.Path, &a.Terminal, &a.Sort, &a.Status); err != nil {
			writeErr(w, err)
			return
		}
		list = append(list, a)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"list": list})
}

// updateApp PUT /api/v1/admin/apps/{id} → data:null
// 启停（status 显式数值归一 0/1）与排序（Number(sort) || 0）；至少改一项。
func (m *Module) updateApp(w http.ResponseWriter, r *http.Request) {
	if _, ok := m.ident(w, r); !ok {
		return
	}
	targetID := pathID(r)
	var exists int64
	if err := m.DB.QueryRowContext(r.Context(), `SELECT id FROM sys_app WHERE id = ?`, targetID).Scan(&exists); err == sql.ErrNoRows {
		fail(w, http.StatusNotFound, codeAppMissing, "应用不存在")
		return
	} else if err != nil {
		writeErr(w, DependencyUnavailableMsg("应用读取失败"))
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeNoChange, "请求体不是合法 JSON")
		return
	}
	_, hasStatus := body["status"]
	_, hasSort := body["sort"]
	fields := make([]string, 0, 2)
	params := make([]any, 0, 3)
	if hasStatus {
		fields = append(fields, "status = ?")
		params = append(params, rawFlag01(body["status"]))
	}
	if hasSort {
		fields = append(fields, "sort = ?")
		params = append(params, rawNumberOr0(body["sort"]))
	}
	if len(fields) == 0 {
		fail(w, http.StatusBadRequest, codeNoChange, "没有需要修改的内容")
		return
	}
	params = append(params, targetID)
	res, err := m.DB.ExecContext(r.Context(),
		`UPDATE sys_app SET `+strings.Join(fields, ", ")+` WHERE id = ?`, params...)
	if err != nil {
		writeErr(w, err)
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		fail(w, http.StatusNotFound, codeAppMissing, "应用不存在")
		return
	}
	m.logOp(r.Context(), "admin.app.update", "调整应用启停/排序", "admin app updated",
		map[string]any{"app_id": targetID})
	writeOK(w, nil, "已保存")
}

// userApps GET /api/v1/admin/users/{id}/apps → data:{app_ids:[...]}
func (m *Module) userApps(w http.ResponseWriter, r *http.Request) {
	if _, ok := m.ident(w, r); !ok {
		return
	}
	targetID := pathID(r)
	var exists int64
	if err := m.DB.QueryRowContext(r.Context(), `SELECT id FROM sys_user WHERE id = ?`, targetID).Scan(&exists); err == sql.ErrNoRows {
		fail(w, http.StatusNotFound, codeUserMissing, "用户不存在")
		return
	} else if err != nil {
		writeErr(w, DependencyUnavailableMsg("用户读取失败"))
		return
	}
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT app_id FROM sys_user_app WHERE user_id = ?`, targetID)
	if err != nil {
		writeErr(w, DependencyUnavailableMsg("授权读取失败"))
		return
	}
	defer rows.Close()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			writeErr(w, err)
			return
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"app_ids": ids})
}

// setUserApps PUT /api/v1/admin/users/{id}/apps {app_ids:[...]} → data:{app_ids:[...]}
// 全量替换（事务）；过滤不存在的应用 id 防脏数据，返回过滤后的实际授权清单。
func (m *Module) setUserApps(w http.ResponseWriter, r *http.Request) {
	if _, ok := m.ident(w, r); !ok {
		return
	}
	targetID := pathID(r)
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeAppIDsBad, "请求体不是合法 JSON")
		return
	}
	rawIDs, exists := body["app_ids"]
	trimmed := strings.TrimSpace(string(rawIDs))
	if !exists || !strings.HasPrefix(trimmed, "[") {
		fail(w, http.StatusBadRequest, codeAppIDsBad, "app_ids 需为数组")
		return
	}
	var anyIDs []any
	if err := json.Unmarshal(rawIDs, &anyIDs); err != nil {
		fail(w, http.StatusBadRequest, codeAppIDsBad, "app_ids 需为数组")
		return
	}
	// 去重 + 只留正整数（Node: [...new Set(ids.map(Number).filter(n => Number.isInteger(n) && n > 0))]）
	ids := make([]int64, 0, len(anyIDs))
	seen := map[int64]bool{}
	for _, v := range anyIDs {
		n, ok := jsonToInt(v)
		if !ok || n <= 0 || seen[n] {
			continue
		}
		seen[n] = true
		ids = append(ids, n)
	}
	var userID int64
	if err := m.DB.QueryRowContext(r.Context(), `SELECT id FROM sys_user WHERE id = ?`, targetID).Scan(&userID); err == sql.ErrNoRows {
		fail(w, http.StatusNotFound, codeUserMissing, "用户不存在")
		return
	} else if err != nil {
		writeErr(w, DependencyUnavailableMsg("用户读取失败"))
		return
	}
	// 过滤掉不存在的应用 id，防止脏数据
	if len(ids) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		args := make([]any, 0, len(ids))
		for _, id := range ids {
			args = append(args, id)
		}
		rows, err := m.DB.QueryContext(r.Context(),
			`SELECT id FROM sys_app WHERE id IN (`+placeholders+`)`, args...)
		if err != nil {
			writeErr(w, DependencyUnavailableMsg("应用校验失败"))
			return
		}
		valid := make([]int64, 0, len(ids))
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				writeErr(w, err)
				return
			}
			valid = append(valid, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			writeErr(w, err)
			return
		}
		ids = valid
	}
	tx, err := m.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM sys_user_app WHERE user_id = ?`, targetID); err != nil {
		_ = tx.Rollback()
		writeErr(w, err)
		return
	}
	if len(ids) > 0 {
		var sb strings.Builder
		sb.WriteString(`INSERT IGNORE INTO sys_user_app (user_id, app_id) VALUES `)
		args := make([]any, 0, len(ids)*2)
		for i, id := range ids {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("(?,?)")
			args = append(args, targetID, id)
		}
		if _, err := tx.ExecContext(r.Context(), sb.String(), args...); err != nil {
			_ = tx.Rollback()
			writeErr(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeErr(w, err)
		return
	}
	m.logOp(r.Context(), "admin.user.apps.update", "设置员工应用授权", "admin user apps updated",
		map[string]any{"user_id": targetID, "app_ids": ids})
	writeOK(w, map[string]any{"app_ids": ids}, "已保存")
}

// jsonToInt 按 JS Number(x) + Number.isInteger 口径取正整数候选（0/负数/小数由调用方过滤）。
func jsonToInt(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		if x == math.Trunc(x) && !math.IsInf(x, 0) {
			return int64(x), true
		}
	case string:
		if f, err := strconv.ParseFloat(x, 64); err == nil && f == math.Trunc(f) && !math.IsInf(f, 0) {
			return int64(f), true
		}
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}
