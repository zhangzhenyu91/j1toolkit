// Package quiz 迁移 server/src/quiz：题库刷题——双题库池（scope=team 班组池 / scope=all 全部池）、
// 个人题库订阅、错题本与收藏（个人口径）、AI 解析队列（Dify 可注入）、Excel 导入全量替换。
//
// 路由基路径 /api/v1/quiz；全部接口需登录 + quiz 应用权限。管理接口（[manage]）粗筛角色
// （超管任意题库，班组管理员仅本班班组池），细粒度归属校验由 findManageBank 完成；
// 可见性 findVisibleBank：全部池全员可见，班组池仅本班。响应信封 {code,message,data} 沿用数字业务码。
package quiz

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"shade/internal/auth"
	"shade/internal/httpapi"
	"shade/internal/logging"
)

// AppPermit 校验用户是否拥有指定应用（app_key）的使用权限（对接 sys_user_app；nil 表示未接入，不拦截）。
type AppPermit interface {
	Allow(ctx context.Context, userID int64, appKey string) (bool, error)
}

// ConfigReader 运行时配置读取（*config.Store 满足；测试可注入桩）。
type ConfigReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// AppLogger 应用结构化日志写入（*logging.Logger 满足；nil 时降级标准库 log）。
type AppLogger interface {
	Info(ctx context.Context, category, event, messageCN, raw string, fields map[string]any) error
	Error(ctx context.Context, category, event, messageCN, raw string, fields map[string]any) error
}

// 编译期契约：生产装配按此注入。
var _ AppLogger = (*logging.Logger)(nil)

// Module 是题库刷题应用模块。
type Module struct {
	DB            *sql.DB
	Permit        AppPermit        // 应用权限检查（可注入；nil 不拦截）
	Cfg           ConfigReader     // 运行时配置（Dify 热配置读取源；可注入）
	Log           AppLogger        // 应用日志（可注入；nil 降级标准库 log）
	Analyzer      QuestionAnalyzer // AI 解析（可注入；nil/未配置时解析停用，入队静默跳过）
	AdminUsername string           // 启动种子：默认授权的管理员用户名
	queue         *analysisQueue
}

// New 构造题库刷题模块。
func New(db *sql.DB) *Module {
	return &Module{DB: db}
}

// Queue 解析队列（懒构造；Analyze 适配层注入后生效）。
func (m *Module) Queue() *analysisQueue {
	if m.queue == nil {
		m.queue = newAnalysisQueue(m.DB, m.Analyzer, m.Log)
	}
	return m.queue
}

// SetAnalyzer 注入 AI 解析实现并重置队列绑定。
func (m *Module) SetAnalyzer(an QuestionAnalyzer) {
	m.Analyzer = an
	m.queue = newAnalysisQueue(m.DB, an, m.Log)
}

// appLogWith 应用日志写入共用实现（解析队列用；注入结构化日志走日志库，否则降级标准库 log）。
func appLogWith(logger AppLogger, ctx context.Context, level, event, messageCN string, fields map[string]any) {
	if logger != nil {
		if level == "error" {
			_ = logger.Error(ctx, "business", event, messageCN, messageCN, fields)
		} else {
			_ = logger.Info(ctx, "business", event, messageCN, messageCN, fields)
		}
		return
	}
	log.Printf("[题库刷题] %s", messageCN)
}

// RegisterRoutes 注册 /api/v1/quiz 下全部路由（挂载在 /api/v1 之下）。
func (m *Module) RegisterRoutes(r chi.Router) {
	r.Route("/quiz", func(r chi.Router) {
		r.Get("/overview", m.withApp(m.overview))
		r.Get("/banks", m.withApp(m.myBanks))
		r.Get("/banks/pool", m.withApp(m.bankPool))
		r.Get("/banks/manage", m.withApp(m.manage(m.bankManageList)))
		r.Get("/banks/template", m.withApp(m.bankTemplate))
		r.Get("/banks/{id}/home", m.withApp(m.bankHome))
		r.Post("/banks/{id}/join", m.withApp(m.bankJoin))
		r.Delete("/banks/{id}/join", m.withApp(m.bankLeave))
		r.Post("/banks", m.withApp(m.manage(m.bankCreate)))
		r.Put("/banks/{id}", m.withApp(m.manage(m.bankUpdate)))
		r.Delete("/banks/{id}", m.withApp(m.manage(m.bankDelete)))
		r.Post("/banks/{id}/import", m.withApp(m.manage(m.bankImport)))
		r.Get("/banks/{id}/questions", m.withApp(m.manage(m.questionList)))
		r.Post("/banks/{id}/questions", m.withApp(m.manage(m.questionCreate)))
		r.Put("/questions/{id}", m.withApp(m.manage(m.questionUpdate)))
		r.Delete("/questions/{id}", m.withApp(m.manage(m.questionDelete)))
		r.Post("/banks/{id}/analyze-retry", m.withApp(m.manage(m.analyzeRetry)))
		r.Get("/practice/questions", m.withApp(m.practiceQuestions))
		r.Post("/practice/progress", m.withApp(m.practiceProgress))
		r.Post("/practice/reset", m.withApp(m.practiceReset))
		r.Put("/practice/analysis", m.withApp(m.practiceAnalysis))
		r.Post("/practice/answer", m.withApp(m.practiceAnswer))
		r.Get("/wrongs", m.withApp(m.wrongs))
		r.Delete("/wrongs/{questionId}", m.withApp(m.wrongDelete))
		r.Post("/favorites/{questionId}", m.withApp(m.favoriteAdd))
		r.Delete("/favorites/{questionId}", m.withApp(m.favoriteRemove))
	})
}

// requestCtx 一次请求的身份 + 生效班组上下文。
type requestCtx struct {
	ident auth.Identity
	team  *httpapi.Team
}

// handlerFunc 带上下文的路由处理函数。
type handlerFunc func(w http.ResponseWriter, r *http.Request, rc requestCtx)

// withApp 登录 + quiz 应用权限 + 班组上下文（超管可用 ?team_id= 指定；其余角色固定本班）。
func (m *Module) withApp(next handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ident, ok := httpapi.IdentityFromContext(r.Context())
		if !ok {
			fail(w, http.StatusUnauthorized, codeNoLogin, "未登录或登录已过期")
			return
		}
		if m.Permit != nil {
			allow, err := m.Permit.Allow(r.Context(), ident.ID, "quiz")
			if err != nil {
				writeErr(w, err)
				return
			}
			if !allow {
				fail(w, http.StatusForbidden, codeNoApp, "暂无该应用的使用权限，请联系管理员开通")
				return
			}
		}
		team := m.resolveTeam(r.Context(), r, ident)
		next(w, r, requestCtx{ident: ident, team: team})
	}
}

// manage 管理接口粗筛：超管任意班组（team 由 ?team_id= 解析），班组管理员仅本班。
func (m *Module) manage(next handlerFunc) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request, rc requestCtx) {
		if rc.ident.Role == "admin" {
			next(w, r, rc)
			return
		}
		if rc.ident.Role == "team_admin" && rc.team != nil && rc.ident.TeamID == rc.team.ID {
			next(w, r, rc)
			return
		}
		fail(w, http.StatusForbidden, codeNotAdmin, "仅管理员可执行此操作")
	}
}

// resolveTeam 解析请求生效班组（超管可指定，其余固定本班；无可用班组返回 nil）。
func (m *Module) resolveTeam(ctx context.Context, r *http.Request, ident auth.Identity) *httpapi.Team {
	if m.DB == nil {
		return nil
	}
	wanted := parseWantedTeamID(r.URL.Query().Get("team_id"))
	adapter := httpapi.IdentityAdapter{TeamID: ident.TeamID, Role: ident.Role}
	team, err := httpapi.ResolveTeam(ctx, m.DB, adapter, wanted)
	if err != nil {
		return nil
	}
	return &team
}

// parseWantedTeamID 按 JS Number(x) + Number.isInteger 判定。
func parseWantedTeamID(raw string) int64 {
	n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || n <= 0 || n != float64(int64(n)) {
		return 0
	}
	return int64(n)
}

// teamIDOr0 生效班组 id（无班组 → 0，不匹配任何班组池行）。
func (rc requestCtx) teamIDOr0() int64 {
	if rc.team == nil {
		return 0
	}
	return rc.team.ID
}

// bank 题库行（池与归属班组）。
type bank struct {
	ID     int64
	TeamID int64
	Scope  string
	Name   string
}

// findBank 取题库行（仅启用行 status=1，与我的题库 / 题库池列表同口径）。
func (m *Module) findBank(ctx context.Context, bankID int64) (bank, bool) {
	var b bank
	var teamID sql.NullInt64
	err := m.DB.QueryRowContext(ctx,
		`SELECT id, team_id, scope FROM quiz_bank WHERE id = ? AND status = 1`, bankID).
		Scan(&b.ID, &teamID, &b.Scope)
	if err != nil {
		return bank{}, false
	}
	b.TeamID = teamID.Int64
	return b, true
}

// findVisibleBank 可见性：全部池全员可见；班组池仅本班（无班组用户只见全部池）。
func (m *Module) findVisibleBank(ctx context.Context, bankID int64, rc requestCtx) (bank, bool) {
	b, ok := m.findBank(ctx, bankID)
	if !ok {
		return bank{}, false
	}
	if b.Scope == "all" {
		return b, true
	}
	if rc.team != nil && b.TeamID == rc.team.ID {
		return b, true
	}
	return bank{}, false
}

// findManageBank 管理权：超管可管任意题库（含他班班组池与全部池）；班组管理员仅本班班组池。
// 管理取行不过滤 status（停用题库仍可管理维护）。
func (m *Module) findManageBank(ctx context.Context, bankID int64, rc requestCtx) (bank, bool) {
	var b bank
	var teamID sql.NullInt64
	err := m.DB.QueryRowContext(ctx,
		`SELECT id, team_id, scope FROM quiz_bank WHERE id = ?`, bankID).
		Scan(&b.ID, &teamID, &b.Scope)
	if err != nil {
		return bank{}, false
	}
	b.TeamID = teamID.Int64
	if rc.ident.Role == "admin" {
		return b, true
	}
	if rc.ident.Role == "team_admin" && b.Scope == "team" && rc.team != nil && b.TeamID == rc.team.ID {
		return b, true
	}
	return bank{}, false
}

// bankStats 题库统计。
type bankStats struct {
	QuestionCount int            `json:"questionCount"`
	Analysis      map[string]int `json:"analysis"`
}

// emptyStats 库内无启用题目时的默认值。
func emptyStats() bankStats {
	return bankStats{QuestionCount: 0, Analysis: map[string]int{"none": 0, "pending": 0, "failed": 0, "done": 0}}
}

// loadBankStats 批量取题库的题目数与解析四状态统计（GET /banks、/banks/pool、/banks/manage、home 共用）。
func (m *Module) loadBankStats(ctx context.Context, ids []int64) map[int64]bankStats {
	out := map[int64]bankStats{}
	if len(ids) == 0 {
		return out
	}
	holders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids)+0)
	for _, id := range ids {
		holders = append(holders, "?")
		args = append(args, id)
	}
	rows, err := m.DB.QueryContext(ctx,
		`SELECT bank_id, COUNT(*) AS total,
       SUM(CASE WHEN analysis_status = 'none' THEN 1 ELSE 0 END) AS none_cnt,
       SUM(CASE WHEN analysis_status = 'pending' THEN 1 ELSE 0 END) AS pending_cnt,
       SUM(CASE WHEN analysis_status = 'failed' THEN 1 ELSE 0 END) AS failed_cnt,
       SUM(CASE WHEN analysis_status = 'done' THEN 1 ELSE 0 END) AS done_cnt
     FROM quiz_question WHERE bank_id IN (`+joinComma(holders)+`) AND status = 1 GROUP BY bank_id`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var total, noneCnt, pendingCnt, failedCnt, doneCnt sql.NullInt64
		if err := rows.Scan(&id, &total, &noneCnt, &pendingCnt, &failedCnt, &doneCnt); err != nil {
			continue
		}
		out[id] = bankStats{
			QuestionCount: int(total.Int64),
			Analysis: map[string]int{
				"none": int(noneCnt.Int64), "pending": int(pendingCnt.Int64),
				"failed": int(failedCnt.Int64), "done": int(doneCnt.Int64),
			},
		}
	}
	return out
}

// loadMyLatestRecords 批量取当前用户每题最近一次作答（刷题记录恢复：qid → {right, answer}）。
func (m *Module) loadMyLatestRecords(ctx context.Context, userID int64, qids []int64) map[int64]map[string]any {
	out := map[int64]map[string]any{}
	if len(qids) == 0 {
		return out
	}
	holders := make([]string, 0, len(qids))
	args := []any{userID}
	for _, id := range qids {
		holders = append(holders, "?")
		args = append(args, id)
	}
	rows, err := m.DB.QueryContext(ctx,
		`SELECT r.question_id, r.is_right, r.user_answer
     FROM quiz_record r
     JOIN (SELECT question_id, MAX(id) AS mid FROM quiz_record
           WHERE user_id = ? AND question_id IN (`+joinComma(holders)+`) GROUP BY question_id) t
       ON t.question_id = r.question_id AND t.mid = r.id`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var qid int64
		var isRight int
		var answer string
		if rows.Scan(&qid, &isRight, &answer) != nil {
			continue
		}
		right := 0
		if isRight != 0 {
			right = 1
		}
		out[qid] = map[string]any{"right": right, "answer": answer}
	}
	return out
}

// ===== GET /overview：当前用户答题总览 =====

func (m *Module) overview(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	var total, rights sql.NullInt64
	_ = m.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(*) AS total, SUM(is_right) AS rights FROM quiz_record WHERE user_id = ?`, rc.ident.ID).
		Scan(&total, &rights)
	var wrongCnt sql.NullInt64
	_ = m.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(*) AS cnt FROM quiz_wrong WHERE user_id = ?`, rc.ident.ID).Scan(&wrongCnt)
	totalAnswered := total.Int64
	var rightRate any
	if totalAnswered > 0 {
		rightRate = int(float64(rights.Int64) / float64(totalAnswered) * 100)
	} else {
		rightRate = nil
	}
	writeOK(w, map[string]any{
		"totalAnswered": totalAnswered,
		"rightRate":     rightRate,
		"wrongCount":    wrongCnt.Int64,
	})
}

// bankRow 题库列表行（联表 team 名）。
type bankRow struct {
	ID          int64
	Name        string
	Description string
	Scope       string
	TeamName    sql.NullString
	CreatedAt   string
}

// scanBanks 执行题库列表查询。
func (m *Module) scanBanks(ctx context.Context, query string, args []any) []bankRow {
	rows, err := m.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []bankRow{}
	for rows.Next() {
		var b bankRow
		var created sql.NullString
		if err := rows.Scan(&b.ID, &b.Name, &b.Description, &b.Scope, &b.TeamName, &created); err != nil {
			continue
		}
		b.CreatedAt = created.String
		out = append(out, b)
	}
	return out
}

// bankItem 列表项公共形状。
func bankItem(b bankRow, st bankStats) map[string]any {
	teamName := any(nil)
	if b.Scope == "team" {
		teamName = b.TeamName.String
		if teamName == "" {
			teamName = nil
		}
	}
	return map[string]any{
		"id":            b.ID,
		"name":          b.Name,
		"description":   b.Description,
		"scope":         b.Scope,
		"teamName":      teamName,
		"questionCount": st.QuestionCount,
		"analysis":      st.Analysis,
	}
}

// ===== GET /banks：我的题库（订阅，按添加时间升序） =====

func (m *Module) myBanks(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	banks := m.scanBanks(r.Context(),
		`SELECT b.id, b.name, b.description, b.scope, t.name AS team_name, '' AS created_at
       FROM quiz_user_bank ub
       JOIN quiz_bank b ON b.id = ub.bank_id AND b.status = 1
       LEFT JOIN sys_team t ON b.scope = 'team' AND t.id = b.team_id
       WHERE ub.user_id = ?
       ORDER BY ub.created_at, ub.id`, []any{rc.ident.ID})
	if len(banks) == 0 {
		writeOK(w, map[string]any{"list": []any{}})
		return
	}
	ids := make([]int64, 0, len(banks))
	for _, b := range banks {
		ids = append(ids, b.ID)
	}
	stats := m.loadBankStats(r.Context(), ids)
	// 当前用户各库答题统计（答过题数按不同题计，正确率按答题记录计）
	ustats := map[int64]map[string]int64{}
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT bank_id, COUNT(DISTINCT question_id) AS answered, COUNT(*) AS total, SUM(is_right) AS rights
       FROM quiz_record WHERE user_id = ? AND bank_id IN (`+joinComma(placeholders(ids))+`) GROUP BY bank_id`,
		append([]any{rc.ident.ID}, idsToAny(ids)...)...)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var bid, answered, total, rights sql.NullInt64
			if rows.Scan(&bid, &answered, &total, &rights) == nil {
				ustats[bid.Int64] = map[string]int64{"answered": answered.Int64, "total": total.Int64, "rights": rights.Int64}
			}
		}
	}
	list := make([]any, 0, len(banks))
	for _, b := range banks {
		st := stats[b.ID]
		if st.Analysis == nil {
			st = emptyStats()
		}
		us := ustats[b.ID]
		uTotal := int64(0)
		answered := int64(0)
		var rightRate any
		if us != nil {
			uTotal = us["total"]
			answered = us["answered"]
			if uTotal > 0 {
				rightRate = int(float64(us["rights"]) / float64(uTotal) * 100)
			}
		}
		if rightRate == nil {
			rightRate = nil
		}
		item := bankItem(b, st)
		item["answeredCount"] = answered
		item["rightRate"] = rightRate
		list = append(list, item)
	}
	writeOK(w, map[string]any{"list": list})
}

// placeholders 生成 n 个 ?。
func placeholders(ids []int64) []string {
	out := make([]string, 0, len(ids))
	for range ids {
		out = append(out, "?")
	}
	return out
}

// idsToAny int64 列表转 any 列表。
func idsToAny(ids []int64) []any {
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, id)
	}
	return out
}

// ===== GET /banks/pool：可见题库池（全部池在前，其次班组池，再按 id） =====

func (m *Module) bankPool(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	teamID := rc.teamIDOr0()
	banks := m.scanBanks(r.Context(),
		`SELECT b.id, b.name, b.description, b.scope, t.name AS team_name, '' AS created_at
       FROM quiz_bank b
       LEFT JOIN sys_team t ON b.scope = 'team' AND t.id = b.team_id
       WHERE b.status = 1 AND (b.scope = 'all' OR (b.scope = 'team' AND b.team_id = ?))
       ORDER BY CASE WHEN b.scope = 'all' THEN 0 ELSE 1 END, b.id`, []any{teamID})
	if len(banks) == 0 {
		writeOK(w, map[string]any{"list": []any{}})
		return
	}
	ids := make([]int64, 0, len(banks))
	for _, b := range banks {
		ids = append(ids, b.ID)
	}
	stats := m.loadBankStats(r.Context(), ids)
	// 是否已加入我的题库
	added := map[int64]bool{}
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT bank_id FROM quiz_user_bank WHERE user_id = ? AND bank_id IN (`+joinComma(placeholders(ids))+`)`,
		append([]any{rc.ident.ID}, idsToAny(ids)...)...)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var bid int64
			if rows.Scan(&bid) == nil {
				added[bid] = true
			}
		}
	}
	list := make([]any, 0, len(banks))
	for _, b := range banks {
		st := stats[b.ID]
		if st.Analysis == nil {
			st = emptyStats()
		}
		item := bankItem(b, st)
		flag := 0
		if added[b.ID] {
			flag = 1
		}
		item["added"] = flag
		list = append(list, item)
	}
	writeOK(w, map[string]any{"list": list})
}

// ===== GET /banks/manage：管理列表 =====

func (m *Module) bankManageList(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	teamID := rc.teamIDOr0()
	where := "(b.scope = 'team' AND b.team_id = ?)"
	if rc.ident.Role == "admin" {
		where = "(b.scope = 'all' OR (b.scope = 'team' AND b.team_id = ?))"
	}
	banks := m.scanBanks(r.Context(),
		`SELECT b.id, b.name, b.description, b.scope, t.name AS team_name,
       DATE_FORMAT(b.created_at, '%Y-%m-%d %H:%i:%s') AS created_at
     FROM quiz_bank b
     LEFT JOIN sys_team t ON b.scope = 'team' AND t.id = b.team_id
     WHERE `+where+`
     ORDER BY b.id DESC`, []any{teamID})
	if len(banks) == 0 {
		writeOK(w, map[string]any{"list": []any{}})
		return
	}
	ids := make([]int64, 0, len(banks))
	for _, b := range banks {
		ids = append(ids, b.ID)
	}
	stats := m.loadBankStats(r.Context(), ids)
	list := make([]any, 0, len(banks))
	for _, b := range banks {
		st := stats[b.ID]
		if st.Analysis == nil {
			st = emptyStats()
		}
		item := bankItem(b, st)
		item["createdAt"] = b.CreatedAt
		list = append(list, item)
	}
	writeOK(w, map[string]any{"list": list})
}

// ===== GET /banks/{id}/home：题库主页 =====

func (m *Module) bankHome(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	bankID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	b, ok := m.findVisibleBank(r.Context(), bankID, rc)
	if !ok {
		fail(w, http.StatusNotFound, codeMissing, "题库不存在或不可见")
		return
	}
	var name, description, scope string
	var teamName sql.NullString
	_ = m.DB.QueryRowContext(r.Context(),
		`SELECT b.name, b.description, b.scope, t.name AS team_name
     FROM quiz_bank b LEFT JOIN sys_team t ON b.scope = 'team' AND t.id = b.team_id
     WHERE b.id = ?`, b.ID).Scan(&name, &description, &scope, &teamName)
	st := m.loadBankStats(r.Context(), []int64{b.ID})[b.ID]
	if st.Analysis == nil {
		st = emptyStats()
	}
	var answered, total, rights sql.NullInt64
	_ = m.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(DISTINCT question_id) AS answered, COUNT(*) AS total, SUM(is_right) AS rights
     FROM quiz_record WHERE user_id = ? AND bank_id = ?`, rc.ident.ID, b.ID).Scan(&answered, &total, &rights)
	uTotal := total.Int64
	var rightRate any
	if uTotal > 0 {
		rightRate = int(float64(rights.Int64) / float64(uTotal) * 100)
	}
	var wrongCnt, favCnt sql.NullInt64
	_ = m.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(*) AS cnt FROM quiz_wrong WHERE user_id = ? AND bank_id = ?`, rc.ident.ID, b.ID).Scan(&wrongCnt)
	_ = m.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(*) AS cnt FROM quiz_favorite WHERE user_id = ? AND bank_id = ?`, rc.ident.ID, b.ID).Scan(&favCnt)
	var seqIdx sql.NullInt64
	_ = m.DB.QueryRowContext(r.Context(),
		`SELECT seq_idx FROM quiz_user_bank WHERE user_id = ? AND bank_id = ?`, rc.ident.ID, b.ID).Scan(&seqIdx)
	teamOut := any(nil)
	if scope == "team" && teamName.String != "" {
		teamOut = teamName.String
	}
	seqOut := any(nil)
	if seqIdx.Valid {
		seqOut = seqIdx.Int64
	}
	writeOK(w, map[string]any{
		"id": b.ID, "name": name, "description": description, "scope": scope,
		"teamName": teamOut, "questionCount": st.QuestionCount, "analysis": st.Analysis,
		"answeredCount": answered.Int64, "rightRate": rightRate,
		"wrongCount": wrongCnt.Int64, "favCount": favCnt.Int64, "seqIdx": seqOut,
	})
}

// ===== POST/DELETE /banks/{id}/join：个人题库订阅 =====

func (m *Module) bankJoin(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	bankID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	b, ok := m.findVisibleBank(r.Context(), bankID, rc)
	if !ok {
		fail(w, http.StatusNotFound, codeMissing, "题库不存在或不可见")
		return
	}
	_, _ = m.DB.ExecContext(r.Context(),
		`INSERT IGNORE INTO quiz_user_bank (user_id, bank_id) VALUES (?, ?)`, rc.ident.ID, b.ID)
	writeOK(w, nil)
}

func (m *Module) bankLeave(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	bankID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	_, _ = m.DB.ExecContext(r.Context(),
		`DELETE FROM quiz_user_bank WHERE user_id = ? AND bank_id = ?`, rc.ident.ID, bankID)
	writeOK(w, nil)
}

// ===== POST /banks：新建题库 =====

func (m *Module) bankCreate(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：请求体不是合法 JSON")
		return
	}
	scope := "team"
	if rawString(body["scope"]) == "all" {
		scope = "all"
	}
	if scope == "all" && rc.ident.Role != "admin" {
		fail(w, http.StatusForbidden, codeNotAdmin, "仅超管可上传至全部池")
		return
	}
	var teamID any
	if scope == "team" {
		if rc.team == nil {
			fail(w, http.StatusBadRequest, codeParam, "无可用班组")
			return
		}
		teamID = rc.team.ID
	}
	name := cutRunes(rawString(body["name"]), 100)
	description := cutRunes(rawString(body["description"]), 255)
	if name == "" {
		fail(w, http.StatusBadRequest, codeParam, "请输入题库名称")
		return
	}
	if scope == "all" {
		// uk_team_name 唯一键对 NULL team_id 不生效，全部池重名走应用层校验
		var dupID int64
		err := m.DB.QueryRowContext(r.Context(),
			`SELECT id FROM quiz_bank WHERE scope = 'all' AND name = ? LIMIT 1`, name).Scan(&dupID)
		if err == nil {
			fail(w, http.StatusConflict, codeDup, "「"+name+"」已存在")
			return
		}
	}
	res, err := m.DB.ExecContext(r.Context(),
		`INSERT INTO quiz_bank (team_id, name, description, scope, created_by) VALUES (?, ?, ?, ?, ?)`,
		teamID, name, description, scope, rc.ident.ID)
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

// cutRunes 按 rune 截断。
func cutRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) > n {
		return string(runes[:n])
	}
	return s
}

// ===== PUT /banks/{id}：修改题库名称/简介（body 带 scope 变更仅超管） =====

func (m *Module) bankUpdate(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	bankID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	b, ok := m.findManageBank(r.Context(), bankID, rc)
	if !ok {
		fail(w, http.StatusNotFound, codeMissing, "题库不存在")
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：请求体不是合法 JSON")
		return
	}
	name := cutRunes(rawString(body["name"]), 100)
	description := cutRunes(rawString(body["description"]), 255)
	if name == "" {
		fail(w, http.StatusBadRequest, codeParam, "请输入题库名称")
		return
	}
	scope := b.Scope
	var teamID any = b.TeamID
	if raw, has := body["scope"]; has && rawHas(raw) {
		wanted := "team"
		if rawString(raw) == "all" {
			wanted = "all"
		}
		if wanted != b.Scope {
			if rc.ident.Role != "admin" {
				fail(w, http.StatusForbidden, codeNotAdmin, "仅超管可变更题库池")
				return
			}
			if wanted == "all" {
				scope = "all"
				teamID = nil
			} else {
				if rc.team == nil {
					fail(w, http.StatusBadRequest, codeParam, "无可用班组")
					return
				}
				scope = "team"
				teamID = rc.team.ID
			}
		}
	}
	if scope == "all" {
		var dupID int64
		err := m.DB.QueryRowContext(r.Context(),
			`SELECT id FROM quiz_bank WHERE scope = 'all' AND name = ? AND id <> ? LIMIT 1`, name, b.ID).Scan(&dupID)
		if err == nil {
			fail(w, http.StatusConflict, codeDup, "「"+name+"」已存在")
			return
		}
	}
	_, err = m.DB.ExecContext(r.Context(),
		`UPDATE quiz_bank SET name = ?, description = ?, scope = ?, team_id = ? WHERE id = ?`,
		name, description, scope, teamID, b.ID)
	if err != nil {
		if isDupEntry(err) {
			fail(w, http.StatusConflict, codeDup, "「"+name+"」已存在")
			return
		}
		writeErr(w, err)
		return
	}
	writeOK(w, nil)
}

// ===== DELETE /banks/{id}：删除题库（连带删题目/答题记录/错题/收藏/订阅） =====

func (m *Module) bankDelete(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	bankID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	b, ok := m.findManageBank(r.Context(), bankID, rc)
	if !ok {
		fail(w, http.StatusNotFound, codeMissing, "题库不存在")
		return
	}
	tx, err := m.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	for _, stmt := range []string{
		`DELETE FROM quiz_record WHERE bank_id = ?`,
		`DELETE FROM quiz_wrong WHERE bank_id = ?`,
		`DELETE FROM quiz_favorite WHERE bank_id = ?`,
		`DELETE FROM quiz_question WHERE bank_id = ?`,
		`DELETE FROM quiz_user_bank WHERE bank_id = ?`,
		`DELETE FROM quiz_bank WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(r.Context(), stmt, b.ID); err != nil {
			_ = tx.Rollback()
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

// errQuestionValidation 题目校验失败。
var errQuestionValidation = errors.New("题目校验未通过")
