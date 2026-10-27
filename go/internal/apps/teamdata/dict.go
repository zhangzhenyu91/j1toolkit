package teamdata

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// dictSpec 描述一类班组字典（数据复用 worklog_dict_* 表，端点镜像 worklog 管理端点）。
type dictSpec struct {
	path     string // 路由段：vehicles | destinations | members
	table    string // 表名
	field    string // 名称列：plate_no | name
	label    string // 中文名（错误提示用）
	nameLen  int    // 名称列宽（入库前截断防 ER_DATA_TOO_LONG）
	extraSel string // 列表附加列
	refSQL   string // 日志引用计数 SQL（? = 字典 id）
}

var dictSpecs = []dictSpec{
	{
		path: "vehicles", table: "worklog_vehicle", field: "plate_no", label: "车牌", nameLen: 32,
		refSQL: "SELECT COUNT(*) FROM worklog_entry WHERE vehicle_id = ?",
	},
	{
		path: "destinations", table: "worklog_destination", field: "name", label: "目的地", nameLen: 64,
		extraSel: ", out_of_city, stationed",
		refSQL:   "SELECT COUNT(*) FROM worklog_entry WHERE destination_id = ?",
	},
	{
		path: "members", table: "worklog_member", field: "name", label: "成员", nameLen: 64,
		extraSel: ", user_id",
		refSQL:   "SELECT COUNT(*) FROM worklog_entry_member WHERE member_id = ?",
	},
}

// dictList GET /{path}：本班字典列表（sort,id 序），{ list: [...] }；无班组返回空列表。
func (m *Module) dictList(w http.ResponseWriter, r *http.Request, spec dictSpec) {
	_, team, ok := m.enter(w, r, 0, false)
	if !ok {
		return
	}
	if team == nil {
		writeOK(w, map[string]any{"list": []any{}})
		return
	}
	rows, err := m.DB.QueryContext(r.Context(),
		"SELECT id, "+spec.field+" AS name, sort, status"+spec.extraSel+" FROM "+spec.table+" WHERE team_id = ? ORDER BY sort, id",
		team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		item, err := scanDictRow(rows, spec)
		if err != nil {
			writeErr(w, err)
			return
		}
		list = append(list, item)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"list": list})
}

// scanDictRow 按 spec 扫描一行字典（含附加列）。
func scanDictRow(rows *sql.Rows, spec dictSpec) (map[string]any, error) {
	var (
		id     int64
		name   string
		sort   int64
		status int64
	)
	item := map[string]any{}
	switch spec.path {
	case "destinations":
		var ooc, sta int64
		if err := rows.Scan(&id, &name, &sort, &status, &ooc, &sta); err != nil {
			return nil, err
		}
		item["out_of_city"], item["stationed"] = ooc, sta
	case "members":
		var userID sql.NullInt64
		if err := rows.Scan(&id, &name, &sort, &status, &userID); err != nil {
			return nil, err
		}
		item["user_id"] = userID.Int64
	default:
		if err := rows.Scan(&id, &name, &sort, &status); err != nil {
			return nil, err
		}
	}
	item["id"], item["name"], item["sort"], item["status"] = id, name, sort, status
	return item, nil
}

// dictCreate POST /{path} {name}：新增字典项（sort 排尾；同班重名 → 40900）。
func (m *Module) dictCreate(w http.ResponseWriter, r *http.Request, spec dictSpec) {
	_, team, ok := m.enter(w, r, 0, false)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoTeam, "无可用班组")
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "请求体不是合法 JSON")
		return
	}
	name := truncate(rawString(body["name"]), spec.nameLen)
	if name == "" {
		fail(w, http.StatusBadRequest, codeInput, "请输入"+spec.label+"名称")
		return
	}
	var maxSort int64
	if err := m.DB.QueryRowContext(r.Context(),
		"SELECT COALESCE(MAX(sort), 0) FROM "+spec.table+" WHERE team_id = ?", team.ID).Scan(&maxSort); err != nil {
		writeErr(w, err)
		return
	}
	res, err := m.DB.ExecContext(r.Context(),
		"INSERT INTO "+spec.table+" (team_id, "+spec.field+", sort) VALUES (?, ?, ?)", team.ID, name, maxSort+1)
	if err != nil {
		if isDupEntry(err) {
			fail(w, http.StatusConflict, codeDup, "「"+name+"」已存在")
			return
		}
		writeErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	writeOK(w, map[string]any{"id": id})
}

// dictUpdate PUT /{id}：按出现的字段更新 name/sort/status（目的地另有 out_of_city/stationed 互斥标记）。
func (m *Module) dictUpdate(w http.ResponseWriter, r *http.Request, spec dictSpec) {
	_, team, ok := m.enter(w, r, 0, false)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoTeam, "无可用班组")
		return
	}
	id := pathID(r)
	var exist int64
	err := m.DB.QueryRowContext(r.Context(),
		"SELECT id FROM "+spec.table+" WHERE id = ? AND team_id = ?", id, team.ID).Scan(&exist)
	if err != nil {
		if err == sql.ErrNoRows {
			fail(w, http.StatusNotFound, codeMissing, spec.label+"不存在")
			return
		}
		writeErr(w, err)
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "请求体不是合法 JSON")
		return
	}
	if raw, present := body["name"]; present {
		name := truncate(rawString(raw), spec.nameLen)
		if name == "" {
			fail(w, http.StatusBadRequest, codeInput, "请输入"+spec.label+"名称")
			return
		}
		if _, err := m.DB.ExecContext(r.Context(),
			"UPDATE "+spec.table+" SET "+spec.field+" = ? WHERE id = ?", name, id); err != nil {
			if isDupEntry(err) {
				fail(w, http.StatusConflict, codeDup, "「"+name+"」已存在")
				return
			}
			writeErr(w, err)
			return
		}
	}
	if raw, present := body["sort"]; present {
		if _, err := m.DB.ExecContext(r.Context(),
			"UPDATE "+spec.table+" SET sort = ? WHERE id = ?", rawNumberOr0(raw), id); err != nil {
			writeErr(w, err)
			return
		}
	}
	if raw, present := body["status"]; present {
		if _, err := m.DB.ExecContext(r.Context(),
			"UPDATE "+spec.table+" SET status = ? WHERE id = ?", rawFlag01(raw), id); err != nil {
			writeErr(w, err)
			return
		}
	}
	// 市外/驻地标记（仅目的地字典，互斥——费用口径同出工日志）：标一个即清另一个；同置 1 拒绝
	if spec.path == "destinations" {
		oocRaw, hasOOC := body["out_of_city"]
		staRaw, hasSta := body["stationed"]
		if hasOOC || hasSta {
			ooc, sta := -1, -1
			if hasOOC {
				ooc = rawFlag01(oocRaw)
			}
			if hasSta {
				sta = rawFlag01(staRaw)
			}
			if ooc == 1 && sta == 1 {
				fail(w, http.StatusBadRequest, codeInput, "市外与驻地只能标记其一")
				return
			}
			sets := []string{}
			args := []any{}
			if ooc >= 0 {
				sets = append(sets, "out_of_city = ?")
				args = append(args, ooc)
			}
			if sta >= 0 {
				sets = append(sets, "stationed = ?")
				args = append(args, sta)
			}
			if ooc == 1 {
				sets = append(sets, "stationed = 0")
			}
			if sta == 1 {
				sets = append(sets, "out_of_city = 0")
			}
			args = append(args, id)
			if _, err := m.DB.ExecContext(r.Context(),
				"UPDATE "+spec.table+" SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...); err != nil {
				writeErr(w, err)
				return
			}
		}
	}
	writeOK(w, nil)
}

// dictDelete DELETE /{id}：无日志引用才可删（否则 40901 改为停用）。
func (m *Module) dictDelete(w http.ResponseWriter, r *http.Request, spec dictSpec) {
	_, team, ok := m.enter(w, r, 0, false)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoTeam, "无可用班组")
		return
	}
	id := pathID(r)
	var refs int64
	if err := m.DB.QueryRowContext(r.Context(), spec.refSQL, id).Scan(&refs); err != nil {
		writeErr(w, err)
		return
	}
	if refs > 0 {
		fail(w, http.StatusConflict, codeInUse,
			"该"+spec.label+"已被 "+strconv.FormatInt(refs, 10)+" 条日志引用，请改为停用")
		return
	}
	res, err := m.DB.ExecContext(r.Context(),
		"DELETE FROM "+spec.table+" WHERE id = ? AND team_id = ?", id, team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		fail(w, http.StatusNotFound, codeMissing, spec.label+"不存在")
		return
	}
	writeOK(w, nil)
}

// moveMember PUT /members/{id}/move：成员排序（与 oplines move 共用 moveRow）。
func (m *Module) moveMember(w http.ResponseWriter, r *http.Request) {
	m.moveRow(w, r, "worklog_member", "成员")
}

// moveRow 上移/下移公共助手：与班内相邻行换位，事务内整体重写 sort 为连续 1..N
// （兼容历史同值 sort；direction 为准，兼容 worklog 旧口径 dir；已在顶端/底端时次序不变）。
func (m *Module) moveRow(w http.ResponseWriter, r *http.Request, table, label string) {
	_, team, ok := m.enter(w, r, 0, false)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoTeam, "无可用班组")
		return
	}
	id := pathID(r)
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "请求体不是合法 JSON")
		return
	}
	dir := "down"
	rawDir := rawString(body["direction"])
	if rawDir == "" {
		rawDir = rawString(body["dir"])
	}
	if rawDir == "up" {
		dir = "up"
	}
	tx, err := m.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer tx.Rollback() //nolint:errcheck // 提交后回滚为空操作
	rows, err := tx.QueryContext(r.Context(),
		"SELECT id FROM "+table+" WHERE team_id = ? ORDER BY sort, id", team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	var order []int64
	for rows.Next() {
		var rowID int64
		if err := rows.Scan(&rowID); err != nil {
			rows.Close()
			writeErr(w, err)
			return
		}
		order = append(order, rowID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	idx := -1
	for i, rowID := range order {
		if rowID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		fail(w, http.StatusNotFound, codeMissing, label+"不存在")
		return
	}
	swapIdx := idx - 1
	if dir == "down" {
		swapIdx = idx + 1
	}
	if swapIdx < 0 || swapIdx >= len(order) {
		writeOK(w, nil) // 已在顶端 / 底端，次序不变
		return
	}
	moved := order[idx]
	order = append(order[:idx], order[idx+1:]...)
	order = append(order[:swapIdx], append([]int64{moved}, order[swapIdx:]...)...)
	for i, rowID := range order {
		if _, err := tx.ExecContext(r.Context(),
			"UPDATE "+table+" SET sort = ? WHERE id = ?", i+1, rowID); err != nil {
			writeErr(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, nil)
}

// pathID 取路由参数 id（非法值按 0 处理，后续查询自然落 40400，同 JS Number(param) 口径）。
func pathID(r *http.Request) int64 {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 0 {
		return 0
	}
	return id
}

// truncate 按列宽截断（JS slice 口径：按字符）。
func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}
