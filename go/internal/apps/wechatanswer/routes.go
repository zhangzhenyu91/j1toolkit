// 监听群/规则 CRUD 路由（对外契约与 web/src/features/admin/api.js 对齐）：
// 监听群为主体，每群每类（location/treeWatch）最多一条规则，密钥独立。
// 密钥只写不读（响应以 keys_configured 布尔呈现），空值提交保持已存值（不丢已有键）。
// 列表返回 {items:[...]}（附 groups/rules 兼容旧键）；状态与重连在 /status 下。
package wechatanswer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"shade/internal/auth"
	"shade/internal/httpapi"
)

// ruleKeys 对外契约的密钥键名（keys / keys_configured）。
var ruleKeys = []string{
	"tree_extract", "tree_correct", "crossing_extract", "crossing_correct",
	"ask_name", "chat_digest", "location",
}

// legacyRuleKeys 旧扁平键名 → 契约键名（兼容旧客户端提交）。
var legacyRuleKeys = map[string]string{
	"extract_key":          "tree_extract",
	"correct_key":          "tree_correct",
	"crossing_key":         "crossing_extract",
	"crossing_correct_key": "crossing_correct",
}

// ruleKeyOf 取规则某契约键的密钥值。
func ruleKeyOf(r Rule, key string) string {
	switch key {
	case "tree_extract":
		return r.ExtractKey
	case "tree_correct":
		return r.CorrectKey
	case "crossing_extract":
		return r.CrossingKey
	case "crossing_correct":
		return r.CrossingCorrectKey
	case "ask_name":
		return r.AskNameKey
	case "chat_digest":
		return r.ChatDigestKey
	case "location":
		return r.LocationKey
	}
	return ""
}

// setRuleKey 设置规则某契约键的密钥值（空值不覆盖，不丢已有键）。
func setRuleKey(r *Rule, key, val string) {
	if val == "" {
		return
	}
	switch key {
	case "tree_extract":
		r.ExtractKey = val
	case "tree_correct":
		r.CorrectKey = val
	case "crossing_extract":
		r.CrossingKey = val
	case "crossing_correct":
		r.CrossingCorrectKey = val
	case "ask_name":
		r.AskNameKey = val
	case "chat_digest":
		r.ChatDigestKey = val
	case "location":
		r.LocationKey = val
	}
}

// ruleDefaultName 规则类型默认名。
func ruleDefaultName(trigger string) string {
	if trigger == TriggerTreeWatch {
		return "输电班组运维"
	}
	return "位置消息"
}

// ruleComplete 工作流密钥配置完整性（提问开启时 ask_name 必配）。
func ruleComplete(r Rule) bool {
	if r.Trigger == TriggerLocation {
		return r.LocationKey != ""
	}
	if r.ExtractKey == "" || r.CorrectKey == "" {
		return false
	}
	if r.AskEnabled && r.AskNameKey == "" {
		return false
	}
	return true
}

// ruleView 规则视图（密钥脱敏：只回 keys_configured 布尔，绝不回显完整值）。
type ruleView struct {
	ID                 int64           `json:"id"`
	GroupID            string          `json:"group_id"`
	Type               string          `json:"type"`
	Trigger            string          `json:"trigger"` // legacy 同 type
	Name               string          `json:"name"`
	Enabled            bool            `json:"enabled"`
	Complete           bool            `json:"complete"`
	AskEnabled         bool            `json:"ask_enabled"`
	KeysConfigured     map[string]bool `json:"keys_configured"`
	TeamName           string          `json:"team_name"`
	HasExtractKey      bool            `json:"has_extract_key"`
	HasCorrectKey      bool            `json:"has_correct_key"`
	HasCrossingKey     bool            `json:"has_crossing_key"`
	HasCrossingCorrect bool            `json:"has_crossing_correct_key"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
}

// enter 登录 + 应用权限；失败已写出响应。
func (m *Module) enter(w http.ResponseWriter, r *http.Request) (auth.Identity, bool) {
	ident, found := httpapi.IdentityFromContext(r.Context())
	if !found {
		fail(w, http.StatusUnauthorized, codeNoLogin, "未登录或登录已过期")
		return auth.Identity{}, false
	}
	if m.Permit != nil {
		allow, err := m.Permit.Allow(r.Context(), ident.ID, AppKey)
		if err != nil {
			writeErr(w, err)
			return auth.Identity{}, false
		}
		if !allow {
			fail(w, http.StatusForbidden, codeNoApp, "暂无该应用的使用权限，请联系管理员开通")
			return auth.Identity{}, false
		}
	}
	return ident, true
}

// enterAdmin 在 enter 之上叠加管理门控：超管/班组管理员可写。
func (m *Module) enterAdmin(w http.ResponseWriter, r *http.Request) (auth.Identity, bool) {
	ident, ok := m.enter(w, r)
	if !ok {
		return ident, false
	}
	if ident.Role == "admin" || ident.Role == "team_admin" {
		return ident, true
	}
	fail(w, http.StatusForbidden, codeNotAdmin, "仅管理员可执行此操作")
	return auth.Identity{}, false
}

// ruleToView 规则行 → 脱敏视图。
func (m *Module) ruleToView(ctx context.Context, r Rule) ruleView {
	name := strings.TrimSpace(r.Name)
	if name == "" {
		name = ruleDefaultName(r.Trigger)
	}
	configured := map[string]bool{}
	for _, key := range ruleKeys {
		configured[key] = ruleKeyOf(r, key) != ""
	}
	teamName := r.TeamName
	if teamName == "" {
		teamName = m.teamNameByID(ctx, r.TeamID)
	}
	return ruleView{
		ID:                 r.ID,
		GroupID:            r.GroupID,
		Type:               r.Trigger,
		Trigger:            r.Trigger,
		Name:               name,
		Enabled:            r.Enabled,
		Complete:           ruleComplete(r),
		AskEnabled:         r.AskEnabled,
		KeysConfigured:     configured,
		TeamName:           teamName,
		HasExtractKey:      r.ExtractKey != "",
		HasCorrectKey:      r.CorrectKey != "",
		HasCrossingKey:     r.CrossingKey != "",
		HasCrossingCorrect: r.CrossingCorrectKey != "",
		CreatedAt:          formatDT(r.CreatedAt),
		UpdatedAt:          formatDT(r.UpdatedAt),
	}
}

// formatDT 时间列 → 展示字符串（空返回空串）。
func formatDT(t sql.NullTime) string {
	if !t.Valid {
		return ""
	}
	return t.Time.Format("2006-01-02 15:04:05")
}

// formatUnix 展示时间（Unix 秒 → 字符串；0 返回空串）。
func formatUnix(sec int64) string {
	if sec <= 0 {
		return ""
	}
	return time.Unix(sec, 0).Format("2006-01-02 15:04:05")
}

// groupRow wa_group 一行（CRUD 视图）。
type groupRow struct {
	ID            int64  `json:"id"`
	GroupID       string `json:"group_id"`
	GroupName     string `json:"name"`
	Enabled       bool   `json:"enabled"`
	TeamID        int64  `json:"team_id"`
	LastMessageAt int64  `json:"-"`
}

// listGroupRows 全部监听群。
func (m *Module) listGroupRows(ctx context.Context) ([]groupRow, error) {
	rows, err := m.DB.QueryContext(ctx,
		`SELECT id, group_id, group_name, enabled, team_id, last_message_at FROM wa_group ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []groupRow
	for rows.Next() {
		var g groupRow
		var enabled int64
		if err := rows.Scan(&g.ID, &g.GroupID, &g.GroupName, &enabled, &g.TeamID, &g.LastMessageAt); err != nil {
			return nil, err
		}
		g.Enabled = enabled != 0
		out = append(out, g)
	}
	return out, rows.Err()
}

// listRuleRows 指定群（或全部）规则。
func (m *Module) listRuleRows(ctx context.Context, groupID string) ([]Rule, error) {
	q := `SELECT r.id, r.group_id, g.group_name, r.trigger_type, r.name, r.enabled, r.ask_enabled,
           r.extract_key, r.correct_key, r.crossing_key, r.crossing_correct_key,
           r.ask_name_key, r.chat_digest_key, r.location_key, g.team_id, r.team_name, r.created_at, r.updated_at
    FROM wa_rule r JOIN wa_group g ON g.group_id = r.group_id`
	args := []any{}
	if groupID != "" {
		q += ` WHERE r.group_id=?`
		args = append(args, groupID)
	}
	q += ` ORDER BY r.id`
	rows, err := m.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		var r Rule
		var enabled, askEnabled int64
		if err := rows.Scan(&r.ID, &r.GroupID, &r.GroupName, &r.Trigger, &r.Name, &enabled, &askEnabled,
			&r.ExtractKey, &r.CorrectKey, &r.CrossingKey, &r.CrossingCorrectKey,
			&r.AskNameKey, &r.ChatDigestKey, &r.LocationKey, &r.TeamID, &r.TeamName,
			&r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Enabled = enabled != 0
		r.AskEnabled = askEnabled != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// groupItem 监听群列表项（契约键 + 兼容旧键）。
func (m *Module) groupItem(ctx context.Context, g groupRow, rules []Rule) map[string]any {
	var views []any
	ruleCount, enabledCount := 0, 0
	for _, r := range rules {
		v := m.ruleToView(ctx, r)
		views = append(views, v)
		ruleCount++
		if r.Enabled {
			enabledCount++
		}
	}
	if views == nil {
		views = []any{}
	}
	teamName := m.teamNameByID(ctx, g.TeamID)
	return map[string]any{
		// 契约键
		"id":                 g.ID,
		"group_id":           g.GroupID,
		"name":               g.GroupName,
		"enabled":            g.Enabled,
		"team_id":            g.TeamID,
		"rule_count":         ruleCount,
		"enabled_rule_count": enabledCount,
		// 兼容旧键（网页端在用）
		"group_name":      g.GroupName,
		"team_name":       teamName,
		"last_message_at": formatUnix(g.LastMessageAt),
		"rules":           views,
	}
}

// handleGroupList GET /groups：监听群列表（data.items，附 data.groups 兼容）。
func (m *Module) handleGroupList(w http.ResponseWriter, r *http.Request) {
	if _, ok := m.enter(w, r); !ok {
		return
	}
	groups, err := m.listGroupRows(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	rules, err := m.listRuleRows(r.Context(), "")
	if err != nil {
		writeErr(w, err)
		return
	}
	byGroup := map[string][]Rule{}
	for _, rule := range rules {
		byGroup[rule.GroupID] = append(byGroup[rule.GroupID], rule)
	}
	items := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		items = append(items, m.groupItem(r.Context(), g, byGroup[g.GroupID]))
	}
	ok(w, map[string]any{"items": items, "groups": items})
}

// handleGroupCreate POST /groups：新建监听群（{name, group_id, team_id}；兼容 group_name）。
func (m *Module) handleGroupCreate(w http.ResponseWriter, r *http.Request) {
	if _, allowed := m.enterAdmin(w, r); !allowed {
		return
	}
	body, parsed := readBody(w, r)
	if !parsed {
		return
	}
	groupID := strings.TrimSpace(rawString(body["group_id"]))
	name := strings.TrimSpace(rawString(body["name"]))
	if name == "" {
		name = strings.TrimSpace(rawString(body["group_name"]))
	}
	if groupID == "" || !strings.HasSuffix(groupID, "@chatroom") {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：群 ID 应以 @chatroom 结尾")
		return
	}
	if name == "" || len([]rune(name)) > 64 {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：群备注必填且不超过 64 字")
		return
	}
	teamID := rawInt64(body["team_id"])
	enabled := boolInt(rawBool(body["enabled"], true))
	res, err := m.DB.ExecContext(r.Context(),
		`INSERT INTO wa_group (group_id, group_name, enabled, team_id) VALUES (?,?,?,?)`,
		groupID, name, enabled, teamID)
	if err != nil {
		writeErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	g := groupRow{ID: id, GroupID: groupID, GroupName: name, Enabled: enabled != 0, TeamID: teamID}
	ok(w, map[string]any{"group": m.groupItem(r.Context(), g, nil)})
}

// rawInt64 JSON 值转 int64（非法/缺省 → 0）。
func rawInt64(raw json.RawMessage) int64 {
	s := rawString(raw)
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// loadGroup 按主键取群；不存在返回 nil。
func (m *Module) loadGroup(ctx context.Context, id int64) (*groupRow, error) {
	var g groupRow
	var enabled int64
	err := m.DB.QueryRowContext(ctx,
		`SELECT id, group_id, group_name, enabled, team_id, last_message_at FROM wa_group WHERE id=?`, id).
		Scan(&g.ID, &g.GroupID, &g.GroupName, &enabled, &g.TeamID, &g.LastMessageAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	g.Enabled = enabled != 0
	return &g, nil
}

// handleGroupUpdate PUT /groups/{id}：更新群备注/归属班组/启用（群 ID 创建后不可改）。
func (m *Module) handleGroupUpdate(w http.ResponseWriter, r *http.Request) {
	if _, allowed := m.enterAdmin(w, r); !allowed {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：群 ID 非法")
		return
	}
	group, err := m.loadGroup(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if group == nil {
		fail(w, http.StatusNotFound, codeMissing, "监听群不存在")
		return
	}
	body, parsed := readBody(w, r)
	if !parsed {
		return
	}
	name := group.GroupName
	if raw, exists := body["name"]; exists {
		name = strings.TrimSpace(rawString(raw))
	} else if raw, exists := body["group_name"]; exists {
		name = strings.TrimSpace(rawString(raw))
	}
	if name == "" || len([]rune(name)) > 64 {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：群备注必填且不超过 64 字")
		return
	}
	teamID := group.TeamID
	if _, exists := body["team_id"]; exists {
		teamID = rawInt64(body["team_id"])
	}
	enabled := boolInt(group.Enabled)
	if raw, exists := body["enabled"]; exists {
		enabled = boolInt(rawBool(raw, group.Enabled))
	}
	if _, err := m.DB.ExecContext(r.Context(),
		`UPDATE wa_group SET group_name=?, enabled=?, team_id=? WHERE id=?`, name, enabled, teamID, id); err != nil {
		writeErr(w, err)
		return
	}
	group.GroupName = name
	group.Enabled = enabled != 0
	group.TeamID = teamID
	ok(w, map[string]any{"group": m.groupItem(r.Context(), *group, nil)})
}

// handleGroupDelete DELETE /groups/{id}：删除群（级联规则）。
func (m *Module) handleGroupDelete(w http.ResponseWriter, r *http.Request) {
	if _, allowed := m.enterAdmin(w, r); !allowed {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：群 ID 非法")
		return
	}
	group, err := m.loadGroup(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if group == nil {
		fail(w, http.StatusNotFound, codeMissing, "监听群不存在")
		return
	}
	if _, err := m.DB.ExecContext(r.Context(), `DELETE FROM wa_rule WHERE group_id=?`, group.GroupID); err != nil {
		writeErr(w, err)
		return
	}
	if _, err := m.DB.ExecContext(r.Context(), `DELETE FROM wa_group WHERE id=?`, id); err != nil {
		writeErr(w, err)
		return
	}
	ok(w, map[string]any{"id": id})
}

// handleGroupRuleList GET /groups/{id}/rules：规则列表（data.items，附 data.rules 兼容）。
func (m *Module) handleGroupRuleList(w http.ResponseWriter, r *http.Request) {
	if _, ok := m.enter(w, r); !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：群 ID 非法")
		return
	}
	group, err := m.loadGroup(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if group == nil {
		fail(w, http.StatusNotFound, codeMissing, "监听群不存在")
		return
	}
	rules, err := m.listRuleRows(r.Context(), group.GroupID)
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]any, 0, len(rules))
	for _, rule := range rules {
		items = append(items, m.ruleToView(r.Context(), rule))
	}
	ok(w, map[string]any{"items": items, "rules": items})
}

// ruleForm 规则提交字段（密钥空值保持已存值，不丢已有键）。
type ruleForm struct {
	Trigger    string
	Name       string
	HasEnabled bool
	Enabled    bool
	AskEnabled bool
	HasAsk     bool
	Keys       map[string]string
}

// parseRuleForm 解析规则提交体：keys 对象 + 旧扁平键名均收；type 编辑时可省略。
func parseRuleForm(body map[string]json.RawMessage) (ruleForm, error) {
	var f ruleForm
	f.Trigger = strings.TrimSpace(rawString(body["type"]))
	if f.Trigger == "" {
		f.Trigger = strings.TrimSpace(rawString(body["trigger"]))
	}
	f.Name = strings.TrimSpace(rawString(body["name"]))
	if raw, exists := body["enabled"]; exists {
		f.HasEnabled = true
		f.Enabled = rawBool(raw, true)
	}
	if raw, exists := body["ask_enabled"]; exists {
		f.HasAsk = true
		f.AskEnabled = rawBool(raw, false)
	}
	f.Keys = map[string]string{}
	if raw, exists := body["keys"]; exists {
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(raw, &keys); err != nil {
			return f, errors.New("keys 不是合法对象")
		}
		for k, v := range keys {
			f.Keys[k] = strings.TrimSpace(rawString(v))
		}
	}
	// 旧扁平键名兼容
	for legacy, key := range legacyRuleKeys {
		if raw, exists := body[legacy]; exists {
			f.Keys[key] = strings.TrimSpace(rawString(raw))
		}
	}
	for key, val := range f.Keys {
		if len(val) > 200 {
			return f, errors.New("Dify API Key 不能超过 200 个字符")
		}
		switch key {
		case "tree_extract", "tree_correct", "crossing_extract", "crossing_correct",
			"ask_name", "chat_digest", "location":
		default:
			return f, errors.New("不支持的密钥字段：" + key)
		}
	}
	if f.Trigger != "" && f.Trigger != TriggerLocation && f.Trigger != TriggerTreeWatch {
		return f, errors.New("触发条件仅支持 location 或 treeWatch")
	}
	if len([]rune(f.Name)) > 64 {
		return f, errors.New("规则名称不能超过 64 个字符")
	}
	return f, nil
}

// mergeRule 把表单合并进既有规则（空密钥保持已存值；编辑不改类型）。
func mergeRule(cur *Rule, f ruleForm) error {
	if f.Trigger != "" {
		cur.Trigger = f.Trigger
	}
	if f.Name != "" {
		cur.Name = f.Name
	} else if cur.Name == "" {
		cur.Name = ruleDefaultName(cur.Trigger)
	}
	if f.HasEnabled {
		cur.Enabled = f.Enabled
	}
	if f.HasAsk {
		cur.AskEnabled = f.AskEnabled
	}
	for key, val := range f.Keys {
		setRuleKey(cur, key, val)
	}
	if cur.Trigger == TriggerTreeWatch {
		if cur.ExtractKey == "" || cur.CorrectKey == "" {
			return errors.New("请填写树障提取与更正 Dify API Key")
		}
		if cur.AskEnabled && cur.AskNameKey == "" {
			return errors.New("启用群内提问时请填写回答文件命名 Dify API Key")
		}
	} else {
		// 旧客户端 location 规则走 extract_key 提交：迁移为 location Key（不丢已有键）
		if cur.LocationKey == "" && cur.ExtractKey != "" {
			cur.LocationKey = cur.ExtractKey
		}
		if cur.LocationKey == "" {
			return errors.New("请填写位置处理 Dify Workflow Key")
		}
	}
	return nil
}

// ruleRowSQL 规则写入列（沿用存储列名；location_key 与 extract_key 双写保证兼容）。
func ruleRowSQL() string {
	return `trigger_type, name, enabled, ask_enabled, extract_key, correct_key, crossing_key,
    crossing_correct_key, ask_name_key, chat_digest_key, location_key`
}

func ruleRowArgs(cur *Rule) []any {
	return []any{
		cur.Trigger, cur.Name, boolInt(cur.Enabled), boolInt(cur.AskEnabled),
		cur.ExtractKey, cur.CorrectKey, cur.CrossingKey, cur.CrossingCorrectKey,
		cur.AskNameKey, cur.ChatDigestKey, cur.LocationKey,
	}
}

// handleRuleUpsert POST /groups/{id}/rules：新建或更新该群指定类型的规则（每群每类最多一条）。
func (m *Module) handleRuleUpsert(w http.ResponseWriter, r *http.Request) {
	if _, allowed := m.enterAdmin(w, r); !allowed {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：群 ID 非法")
		return
	}
	group, err := m.loadGroup(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if group == nil {
		fail(w, http.StatusNotFound, codeMissing, "监听群不存在")
		return
	}
	body, parsed := readBody(w, r)
	if !parsed {
		return
	}
	form, err := parseRuleForm(body)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误："+err.Error())
		return
	}
	if form.Trigger == "" {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：缺少规则类型 type")
		return
	}
	rules, err := m.listRuleRows(r.Context(), group.GroupID)
	if err != nil {
		writeErr(w, err)
		return
	}
	var cur *Rule
	for i := range rules {
		if rules[i].Trigger == form.Trigger {
			cur = &rules[i]
			break
		}
	}
	if cur == nil {
		cur = &Rule{GroupID: group.GroupID, GroupName: group.GroupName, Trigger: form.Trigger, Enabled: true}
		if err := mergeRule(cur, form); err != nil {
			fail(w, http.StatusBadRequest, codeParam, "参数错误："+err.Error())
			return
		}
		res, err := m.DB.ExecContext(r.Context(),
			`INSERT INTO wa_rule (group_id, `+ruleRowSQL()+`) VALUES (?,`+strings.TrimSuffix(strings.Repeat("?,", 11), ",")+`)`,
			append([]any{cur.GroupID}, ruleRowArgs(cur)...)...)
		if err != nil {
			writeErr(w, err)
			return
		}
		cur.ID, _ = res.LastInsertId()
		ok(w, map[string]any{"rule": m.ruleToView(r.Context(), *cur)})
		return
	}
	if err := mergeRule(cur, form); err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误："+err.Error())
		return
	}
	if _, err := m.DB.ExecContext(r.Context(),
		`UPDATE wa_rule SET name=?, enabled=?, ask_enabled=?, extract_key=?, correct_key=?, crossing_key=?,
     crossing_correct_key=?, ask_name_key=?, chat_digest_key=?, location_key=? WHERE id=?`,
		cur.Name, boolInt(cur.Enabled), boolInt(cur.AskEnabled),
		cur.ExtractKey, cur.CorrectKey, cur.CrossingKey, cur.CrossingCorrectKey,
		cur.AskNameKey, cur.ChatDigestKey, cur.LocationKey, cur.ID); err != nil {
		writeErr(w, err)
		return
	}
	ok(w, map[string]any{"rule": m.ruleToView(r.Context(), *cur)})
}

// loadRule 按主键取规则；不存在返回 nil。
func (m *Module) loadRule(ctx context.Context, id int64) (*Rule, error) {
	row := m.DB.QueryRowContext(ctx, `
    SELECT r.id, r.group_id, g.group_name, r.trigger_type, r.name, r.enabled, r.ask_enabled,
           r.extract_key, r.correct_key, r.crossing_key, r.crossing_correct_key,
           r.ask_name_key, r.chat_digest_key, r.location_key, g.team_id, r.team_name, r.created_at, r.updated_at
    FROM wa_rule r JOIN wa_group g ON g.group_id = r.group_id WHERE r.id=?`, id)
	var r Rule
	var enabled, askEnabled int64
	err := row.Scan(&r.ID, &r.GroupID, &r.GroupName, &r.Trigger, &r.Name, &enabled, &askEnabled,
		&r.ExtractKey, &r.CorrectKey, &r.CrossingKey, &r.CrossingCorrectKey,
		&r.AskNameKey, &r.ChatDigestKey, &r.LocationKey, &r.TeamID, &r.TeamName,
		&r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.Enabled = enabled != 0
	r.AskEnabled = askEnabled != 0
	return &r, nil
}

// handleRuleGet GET /rules/{id}：规则详情（密钥脱敏；data.rule，附顶层兼容）。
func (m *Module) handleRuleGet(w http.ResponseWriter, r *http.Request) {
	if _, allowed := m.enter(w, r); !allowed {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：规则 ID 非法")
		return
	}
	rule, err := m.loadRule(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if rule == nil {
		fail(w, http.StatusNotFound, codeMissing, "监听规则不存在")
		return
	}
	ok(w, map[string]any{"rule": m.ruleToView(r.Context(), *rule)})
}

// handleRuleUpdate PUT /rules/{id}：更新规则（编辑不改类型；密钥留空保持不变）。
func (m *Module) handleRuleUpdate(w http.ResponseWriter, r *http.Request) {
	if _, allowed := m.enterAdmin(w, r); !allowed {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：规则 ID 非法")
		return
	}
	rule, err := m.loadRule(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if rule == nil {
		fail(w, http.StatusNotFound, codeMissing, "监听规则不存在")
		return
	}
	body, parsed := readBody(w, r)
	if !parsed {
		return
	}
	form, err := parseRuleForm(body)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误："+err.Error())
		return
	}
	if form.Trigger != "" && form.Trigger != rule.Trigger {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：不允许变更规则触发类型")
		return
	}
	if err := mergeRule(rule, form); err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误："+err.Error())
		return
	}
	if _, err := m.DB.ExecContext(r.Context(), `
    UPDATE wa_rule SET name=?, enabled=?, ask_enabled=?, extract_key=?, correct_key=?, crossing_key=?,
    crossing_correct_key=?, ask_name_key=?, chat_digest_key=?, location_key=? WHERE id=?`,
		rule.Name, boolInt(rule.Enabled), boolInt(rule.AskEnabled),
		rule.ExtractKey, rule.CorrectKey, rule.CrossingKey, rule.CrossingCorrectKey,
		rule.AskNameKey, rule.ChatDigestKey, rule.LocationKey, rule.ID); err != nil {
		writeErr(w, err)
		return
	}
	ok(w, map[string]any{"rule": m.ruleToView(r.Context(), *rule)})
}

// handleRuleDelete DELETE /rules/{id}：删除规则。
func (m *Module) handleRuleDelete(w http.ResponseWriter, r *http.Request) {
	if _, allowed := m.enterAdmin(w, r); !allowed {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：规则 ID 非法")
		return
	}
	res, err := m.DB.ExecContext(r.Context(), `DELETE FROM wa_rule WHERE id=?`, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		fail(w, http.StatusNotFound, codeMissing, "监听规则不存在")
		return
	}
	ok(w, map[string]any{"id": id})
}

// handleStatus GET /status：接入状态（禁止暴露 Token/URL 等敏感值）。
func (m *Module) handleStatus(w http.ResponseWriter, r *http.Request) {
	if _, allowed := m.enter(w, r); !allowed {
		return
	}
	ok(w, map[string]any{"status": m.statusSnapshot(r.Context())})
}

// handleReconnect POST /status/reconnect（兼容 POST /reconnect）：真实触发热重连并回最新状态。
func (m *Module) handleReconnect(w http.ResponseWriter, r *http.Request) {
	if _, allowed := m.enterAdmin(w, r); !allowed {
		return
	}
	m.Reconnect(r.Context())
	ok(w, map[string]any{"status": m.statusSnapshot(r.Context())})
}
