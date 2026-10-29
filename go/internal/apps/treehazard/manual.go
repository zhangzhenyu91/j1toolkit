package treehazard

// 手动新增与编辑正式记录（POST /records、PUT /records/{id}）：
// 业务字段与暂存编辑同口径（发现 12 列 + 处理 3 列，truncateRunes 截断、normDateStr 日期归一、
// vertical 变更重算 vertical_num、pole 区间缺省由区段文本解析），handled_at 空 → 清空处理三列并回退
// 未处理（同内网回写 intranetFields 口径）。不动 crane 幂等锚点（crane_session_id / crane_record_id）
// 与 synced_at（差异比对基准）——编辑只经 updated_at 自增触发「服务端同步后又改过」冲突判定；
// 新增 source='manual'、锚点与 synced_at 留 NULL，撤回 deletes 墓碑与内网行级锚点语义不受影响。
// 权限同删记录与暂存编辑（requireManagerNow：超管任意班组 / 班管仅本班）；更新按 id+team_id 定位不能越班。

import (
	"net/http"
	"strings"
)

// manualForm 清洗后的手动新增/编辑业务列（与 treehazard_record 对应；Status 由 handled_at 推导）。
type manualForm struct {
	VoltageLevel string
	LineName     string
	Section      string
	PoleFrom     *int64
	PoleTo       *int64
	PositionNote string
	TreeSpecies  string
	TreeCount    string
	DistSmall    string
	DistBig      string
	Diameter     string
	Horizontal   string
	Vertical     string
	VerticalNum  *float64
	FinderName   string
	FoundAt      string
	HandlerName  string
	HandleNote   string
	HandledAt    string
	Status       string
}

// manualTextCols 文本列截断宽度（与列宽 / 暂存编辑白名单一致）。
var manualTextCols = map[string]int{
	"voltage_level": 16, "line_name": 64, "section": 32, "position_note": 32,
	"tree_species": 32, "tree_count": 16, "dist_small": 16, "dist_big": 16,
	"diameter": 16, "horizontal": 16, "vertical": 16, "finder_name": 32,
	"handler_name": 32, "handle_note": 64,
}

// readManualBody 解析手动新增/编辑请求体（map 形态，数值字段按 asString 容错）。
func readManualBody(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	var body map[string]any
	if err := decodeJSON(r, &body); err != nil {
		writeFail(w, http.StatusBadRequest, codeParam, "请求体不是合法 JSON", nil)
		return nil, false
	}
	if body == nil {
		body = map[string]any{}
	}
	return body, true
}

// normManualDate 日期列归一：空 → ""；非空但不可识别 → 报错文案。
func normManualDate(raw any, label string) (string, string) {
	s := strings.TrimSpace(asString(raw))
	if s == "" {
		return "", ""
	}
	d := normDateStr(s)
	if d == "" || !dateRe.MatchString(d) {
		return "", label + "格式应为 YYYY-MM-DD"
	}
	return d, ""
}

// normalizeManualForm 清洗入参：文本列 trim + 截断；日期列归一（非空不可识别 → 报错）；
// pole 缺省由区段文本解析；vertical 重算 vertical_num；handled_at 空清空处理三列并回退未处理。
// 返回 errMsg 非空表示校验失败（调用方应 400 原样提示，不落库）。
func normalizeManualForm(body map[string]any) (manualForm, string) {
	var f manualForm
	get := func(key string) string {
		return truncateRunes(strings.TrimSpace(asString(body[key])), manualTextCols[key])
	}
	f.VoltageLevel = get("voltage_level")
	f.LineName = get("line_name")
	f.Section = get("section")
	f.PositionNote = get("position_note")
	f.TreeSpecies = get("tree_species")
	f.TreeCount = get("tree_count")
	f.DistSmall = get("dist_small")
	f.DistBig = get("dist_big")
	f.Diameter = get("diameter")
	f.Horizontal = get("horizontal")
	f.Vertical = get("vertical")
	f.FinderName = get("finder_name")
	f.HandlerName = get("handler_name")
	f.HandleNote = get("handle_note")
	if f.LineName == "" {
		return f, "线路名称不能为空"
	}
	foundAt, msg := normManualDate(body["found_at"], "发现时间")
	if msg != "" {
		return f, msg
	}
	handledAt, msg := normManualDate(body["handled_at"], "处理时间")
	if msg != "" {
		return f, msg
	}
	f.FoundAt = foundAt
	f.HandledAt = handledAt
	// 区段与位置分离：区段只留杆塔区段（字母杆号原样保留），尾部位置说明并入 position_note
	//（显式 position_note 非空时为准，为空才取区段尾部拆出的说明）。
	sec, extra := splitSectionLocation(f.Section)
	if f.PositionNote == "" {
		f.PositionNote = extra
	}
	f.Section = sec
	f.PoleFrom = toIntOrNull(body["pole_from"])
	f.PoleTo = toIntOrNull(body["pole_to"])
	if f.PoleFrom == nil || f.PoleTo == nil {
		from, to := parseSectionPoles(f.Section)
		if f.PoleFrom == nil {
			f.PoleFrom = from
		}
		if f.PoleTo == nil {
			f.PoleTo = to
		}
	}
	f.VerticalNum = parseVerticalNum(f.Vertical)
	if f.HandledAt == "" {
		// 无处理时间 = 未处理：处理三列一并清空（同内网回写 handled_at 空回退口径）。
		f.HandlerName, f.HandleNote = "", ""
		f.Status = "unhandled"
	} else {
		f.Status = "handled"
	}
	return f, ""
}

// formVals 业务列参数序（INSERT / UPDATE 共用）。
func (f manualForm) formVals() []any {
	return []any{
		f.VoltageLevel, f.LineName, f.Section, f.PoleFrom, f.PoleTo, f.PositionNote,
		f.TreeSpecies, f.TreeCount, f.DistSmall, f.DistBig, f.Diameter, f.Horizontal,
		f.Vertical, f.VerticalNum, f.FinderName, nullIfEmpty(f.FoundAt),
		f.HandlerName, f.HandleNote, nullIfEmpty(f.HandledAt), f.Status,
	}
}

// manualSetCols UPDATE 覆盖列（不含 id/team_id/锚点/synced_at/created_*）。
const manualSetCols = `voltage_level = ?, line_name = ?, section = ?, pole_from = ?, pole_to = ?, position_note = ?,
  tree_species = ?, tree_count = ?, dist_small = ?, dist_big = ?, diameter = ?, horizontal = ?, vertical = ?,
  vertical_num = ?, finder_name = ?, found_at = ?, handler_name = ?, handle_note = ?, handled_at = ?, status = ?`

// handleRecordCreate POST /records：手动新增（source='manual'，crane 锚点与 synced_at 留 NULL）。
func (m *Module) handleRecordCreate(w http.ResponseWriter, r *http.Request) {
	team, ident, ok := requireManagerNow(w, r)
	if !ok {
		return
	}
	body, ok := readManualBody(w, r)
	if !ok {
		return
	}
	form, msg := normalizeManualForm(body)
	if msg != "" {
		writeFail(w, http.StatusBadRequest, codeParam, msg, nil)
		return
	}
	res, err := m.DB.ExecContext(r.Context(),
		`INSERT INTO treehazard_record (team_id, voltage_level, line_name, section, pole_from, pole_to, position_note,
		  tree_species, tree_count, dist_small, dist_big, diameter, horizontal, vertical, vertical_num,
		  finder_name, found_at, handler_name, handle_note, handled_at, status, source, created_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'manual', ?)`,
		append([]any{team.ID}, append(form.formVals(), ident.ID)...)...)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "记录创建失败", nil)
		return
	}
	id, _ := res.LastInsertId()
	writeOK(w, map[string]any{"id": id}, "")
}

// handleRecordUpdate PUT /records/{id}：手动编辑业务列（全量覆盖，不触碰 crane 幂等锚点与 synced_at）。
// 按 id + team_id 定位不能越班；updated_at 自增即进入内网差异「服务端同步后又改过」判定。
func (m *Module) handleRecordUpdate(w http.ResponseWriter, r *http.Request) {
	team, _, ok := requireManagerNow(w, r)
	if !ok {
		return
	}
	recordID, ok := pathID(r)
	if !ok {
		writeFail(w, http.StatusBadRequest, codeParam, "id 无效", nil)
		return
	}
	body, ok := readManualBody(w, r)
	if !ok {
		return
	}
	form, msg := normalizeManualForm(body)
	if msg != "" {
		writeFail(w, http.StatusBadRequest, codeParam, msg, nil)
		return
	}
	var exist int64
	if err := m.DB.QueryRowContext(r.Context(),
		`SELECT id FROM treehazard_record WHERE id = ? AND team_id = ?`, recordID, team.ID).Scan(&exist); err != nil {
		writeFail(w, http.StatusNotFound, codeNotFound, "记录不存在", nil)
		return
	}
	if _, err := m.DB.ExecContext(r.Context(),
		`UPDATE treehazard_record SET `+manualSetCols+` WHERE id = ? AND team_id = ?`,
		append(form.formVals(), recordID, team.ID)...); err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "记录更新失败", nil)
		return
	}
	writeOK(w, map[string]any{"id": recordID}, "")
}
