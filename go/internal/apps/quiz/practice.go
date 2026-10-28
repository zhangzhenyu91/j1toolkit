// 题库刷题：刷题取题 / 作答判分 / 错题本 / 收藏（对齐 server/src/quiz/index.js 后半段）。
package quiz

import (
	"database/sql"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// ===== GET /practice/questions：刷题取题 =====
// mode=seq 顺序（按 sort,id 分页）/ mode=rand 随机（ORDER BY RAND()，忽略 offset；可选 ids 收缩范围）/
// mode=wrong 错题本 / mode=fav 收藏（最近倒序）；lite=1 答题卡大纲（仅 id,type）；
// withAnswer=1 背题模式带答案与解析；withRecord=1 记录恢复（已答题放行 answer/analysis）。
func (m *Module) practiceQuestions(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	mode := r.URL.Query().Get("mode")
	if mode != "seq" && mode != "rand" && mode != "wrong" && mode != "fav" {
		mode = "seq"
	}
	// limit/offset 钳制对齐 Node：parseInt||20（0/非数字落 20）后 Math.max(1,…)、Math.min(50,…)；
	// offset parseInt||0 后 Math.max(0,…)
	limit := 20
	if v, ok := jsParseInt(r.URL.Query().Get("limit")); ok && v != 0 {
		limit = v
		if limit < 1 {
			limit = 1
		}
		if limit > 50 {
			limit = 50
		}
	}
	offset := 0
	if v, ok := jsParseInt(r.URL.Query().Get("offset")); ok && v != 0 {
		offset = v
		if offset < 0 {
			offset = 0
		}
	}
	bankID, _ := strconv.ParseInt(r.URL.Query().Get("bankId"), 10, 64)
	lite := r.URL.Query().Get("lite") == "1"
	withAnswer := !lite && r.URL.Query().Get("withAnswer") == "1"
	withRecord := r.URL.Query().Get("withRecord") == "1"
	if mode != "wrong" && mode != "fav" && bankID == 0 {
		fail(w, http.StatusBadRequest, codeParam, "请选择题库")
		return
	}
	if bankID != 0 {
		if _, ok := m.findVisibleBank(r.Context(), bankID, rc); !ok {
			fail(w, http.StatusNotFound, codeMissing, "题库不存在")
			return
		}
	}
	// 列选择：lite 只取 id/type；withAnswer/withRecord 追加 answer/analysis
	cols := "id, type, content, options"
	if withAnswer || withRecord {
		cols += ", answer, analysis"
	}
	if lite {
		cols = "id, type"
	}
	pageClause := "LIMIT " + strconv.Itoa(limit) + " OFFSET " + strconv.Itoa(offset)
	if lite {
		pageClause = ""
	}

	var total int64
	var rows *sql.Rows
	var err error
	switch mode {
	case "seq":
		err = m.DB.QueryRowContext(r.Context(),
			`SELECT COUNT(*) AS total FROM quiz_question WHERE bank_id = ? AND status = 1`, bankID).Scan(&total)
		if err == nil {
			rows, err = m.DB.QueryContext(r.Context(),
				`SELECT `+cols+` FROM quiz_question WHERE bank_id = ? AND status = 1
         ORDER BY sort, id `+pageClause, bankID)
		}
	case "rand":
		var ids []int64
		for _, part := range strings.Split(r.URL.Query().Get("ids"), ",") {
			if n, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil && n > 0 {
				ids = append(ids, n)
			}
		}
		idSQL := ""
		args := []any{bankID}
		if len(ids) > 0 {
			idSQL = " AND id IN (" + joinComma(placeholders(ids)) + ")"
			args = append(args, idsToAny(ids)...)
		}
		err = m.DB.QueryRowContext(r.Context(),
			`SELECT COUNT(*) AS total FROM quiz_question WHERE bank_id = ? AND status = 1`+idSQL, args...).Scan(&total)
		if err == nil {
			randClause := "ORDER BY RAND()"
			if !lite {
				randClause += " LIMIT " + strconv.Itoa(limit)
			}
			rows, err = m.DB.QueryContext(r.Context(),
				`SELECT `+cols+` FROM quiz_question WHERE bank_id = ? AND status = 1`+idSQL+` `+randClause, args...)
		}
	case "fav":
		teamID := rc.teamIDOr0()
		where := ""
		params := []any{rc.ident.ID, teamID}
		if bankID != 0 {
			where = " AND f.bank_id = ?"
			params = append(params, bankID)
		}
		err = m.DB.QueryRowContext(r.Context(),
			`SELECT COUNT(*) AS total FROM quiz_favorite f
       JOIN quiz_question q ON q.id = f.question_id AND q.status = 1
       JOIN quiz_bank b ON b.id = f.bank_id AND b.status = 1
       WHERE f.user_id = ? AND (b.scope = 'all' OR (b.scope = 'team' AND b.team_id = ?))`+where, params...).Scan(&total)
		if err == nil {
			qCols := qualifyCols(cols, "q.")
			rows, err = m.DB.QueryContext(r.Context(),
				`SELECT `+qCols+` FROM quiz_favorite f
       JOIN quiz_question q ON q.id = f.question_id AND q.status = 1
       JOIN quiz_bank b ON b.id = f.bank_id AND b.status = 1
       WHERE f.user_id = ? AND (b.scope = 'all' OR (b.scope = 'team' AND b.team_id = ?))`+where+`
       ORDER BY f.id DESC `+pageClause, params...)
		}
	default: // wrong
		teamID := rc.teamIDOr0()
		where := ""
		params := []any{rc.ident.ID, teamID}
		if bankID != 0 {
			where = " AND w.bank_id = ?"
			params = append(params, bankID)
		}
		err = m.DB.QueryRowContext(r.Context(),
			`SELECT COUNT(*) AS total FROM quiz_wrong w
       JOIN quiz_question q ON q.id = w.question_id AND q.status = 1
       JOIN quiz_bank b ON b.id = w.bank_id AND b.status = 1
       WHERE w.user_id = ? AND (b.scope = 'all' OR (b.scope = 'team' AND b.team_id = ?))`+where, params...).Scan(&total)
		if err == nil {
			qCols := qualifyCols(cols, "q.")
			rows, err = m.DB.QueryContext(r.Context(),
				`SELECT `+qCols+` FROM quiz_wrong w
       JOIN quiz_question q ON q.id = w.question_id AND q.status = 1
       JOIN quiz_bank b ON b.id = w.bank_id AND b.status = 1
       WHERE w.user_id = ? AND (b.scope = 'all' OR (b.scope = 'team' AND b.team_id = ?))`+where+`
       ORDER BY w.last_wrong_at DESC, w.id DESC `+pageClause, params...)
		}
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	type qRow struct {
		id           int64
		questionType string
		content      string
		optionsRaw   string
		answer       sql.NullString
		analysis     sql.NullString
	}
	list := []qRow{}
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var q qRow
			if lite {
				if rows.Scan(&q.id, &q.questionType) != nil {
					continue
				}
			} else if withAnswer || withRecord {
				if rows.Scan(&q.id, &q.questionType, &q.content, &q.optionsRaw, &q.answer, &q.analysis) != nil {
					continue
				}
			} else {
				if rows.Scan(&q.id, &q.questionType, &q.content, &q.optionsRaw) != nil {
					continue
				}
			}
			list = append(list, q)
		}
	}

	// 本批题目的本人收藏标记（lite 大纲不带）
	favSet := map[int64]bool{}
	if !lite && len(list) > 0 {
		qids := make([]int64, 0, len(list))
		for _, q := range list {
			qids = append(qids, q.id)
		}
		frows, err := m.DB.QueryContext(r.Context(),
			`SELECT question_id FROM quiz_favorite WHERE user_id = ? AND question_id IN (`+joinComma(placeholders(qids))+`)`,
			append([]any{rc.ident.ID}, idsToAny(qids)...)...)
		if err == nil {
			defer frows.Close()
			for frows.Next() {
				var qid int64
				if frows.Scan(&qid) == nil {
					favSet[qid] = true
				}
			}
		}
	}
	// 本批/本大纲题目的本人最近作答（withRecord=1 记录恢复）
	recMap := map[int64]map[string]any{}
	if withRecord && len(list) > 0 {
		qids := make([]int64, 0, len(list))
		for _, q := range list {
			qids = append(qids, q.id)
		}
		recMap = m.loadMyLatestRecords(r.Context(), rc.ident.ID, qids)
	}
	// 出参组装
	items := make([]any, 0, len(list))
	for _, q := range list {
		rec := recMap[q.id]
		if lite {
			item := map[string]any{"id": q.id, "type": q.questionType}
			if rec != nil {
				item["myRight"] = rec["right"]
			}
			items = append(items, item)
			continue
		}
		item := map[string]any{
			"id":      q.id,
			"type":    q.questionType,
			"content": q.content,
			"options": parseOptions(q.optionsRaw),
			"fav":     boolFlag(favSet[q.id]),
		}
		if rec != nil {
			item["myRight"] = rec["right"]
			item["myAnswer"] = rec["answer"]
		}
		if withAnswer || rec != nil {
			var answer any
			if q.answer.Valid {
				answer = q.answer.String
			}
			var analysis any
			// Node 口径 r.analysis || null：空串视同无解析出 null
			if q.analysis.Valid && q.analysis.String != "" {
				analysis = q.analysis.String
			}
			item["answer"] = answer
			item["analysis"] = analysis
		}
		items = append(items, item)
	}
	// seq 断点（跨端续刷）：lite 大纲响应附带服务端断点（null=未开始）
	var seqIdx any
	if lite && mode == "seq" && bankID != 0 {
		var pr sql.NullInt64
		if err := m.DB.QueryRowContext(r.Context(),
			`SELECT seq_idx FROM quiz_user_bank WHERE user_id = ? AND bank_id = ?`, rc.ident.ID, bankID).
			Scan(&pr); err == nil && pr.Valid {
			seqIdx = pr.Int64
		}
	}
	writeOK(w, map[string]any{"total": total, "list": items, "seqIdx": seqIdx})
}

// qualifyCols 把列清单加表别名前缀（联表查询消歧）。
func qualifyCols(cols, prefix string) string {
	parts := strings.Split(cols, ", ")
	for i, p := range parts {
		parts[i] = prefix + p
	}
	return strings.Join(parts, ", ")
}

// boolFlag bool → 1/0。
func boolFlag(v bool) int {
	if v {
		return 1
	}
	return 0
}

// ===== POST /practice/progress：顺序练习断点上报 =====

func (m *Module) practiceProgress(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：请求体不是合法 JSON")
		return
	}
	bankID := rawNumberOr0(body["bankId"])
	idx := rawNumberOr0(body["idx"])
	if idx < 0 {
		idx = 0
	}
	if bankID == 0 {
		fail(w, http.StatusBadRequest, codeParam, "请选择题库")
		return
	}
	if _, ok := m.findVisibleBank(r.Context(), bankID, rc); !ok {
		fail(w, http.StatusNotFound, codeMissing, "题库不存在")
		return
	}
	// 订阅行 upsert（未加入题库也可直答，行缺失时自愈补行）
	_, _ = m.DB.ExecContext(r.Context(),
		`INSERT INTO quiz_user_bank (user_id, bank_id, seq_idx) VALUES (?, ?, ?)
     ON DUPLICATE KEY UPDATE seq_idx = VALUES(seq_idx)`, rc.ident.ID, bankID, idx)
	writeOK(w, nil)
}

// ===== POST /practice/reset：清空做题记录（错题本保留，断点一并重置） =====

func (m *Module) practiceReset(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：请求体不是合法 JSON")
		return
	}
	bankID := rawNumberOr0(body["bankId"])
	if bankID == 0 {
		fail(w, http.StatusBadRequest, codeParam, "请选择题库")
		return
	}
	if _, ok := m.findVisibleBank(r.Context(), bankID, rc); !ok {
		fail(w, http.StatusNotFound, codeMissing, "题库不存在")
		return
	}
	res, err := m.DB.ExecContext(r.Context(),
		`DELETE FROM quiz_record WHERE user_id = ? AND bank_id = ?`, rc.ident.ID, bankID)
	if err != nil {
		writeErr(w, err)
		return
	}
	cleared, _ := res.RowsAffected()
	_, _ = m.DB.ExecContext(r.Context(),
		`UPDATE quiz_user_bank SET seq_idx = NULL WHERE user_id = ? AND bank_id = ?`, rc.ident.ID, bankID)
	writeOK(w, map[string]any{"cleared": cleared}, "已清空做题记录")
}

// ===== PUT /practice/analysis：修改题目解析（全员可改） =====

func (m *Module) practiceAnalysis(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：请求体不是合法 JSON")
		return
	}
	questionID := rawNumberOr0(body["questionId"])
	if questionID == 0 {
		fail(w, http.StatusBadRequest, codeParam, "参数不完整")
		return
	}
	var bankID int64
	if err := m.DB.QueryRowContext(r.Context(),
		`SELECT bank_id FROM quiz_question WHERE id = ? AND status = 1`, questionID).Scan(&bankID); err != nil {
		fail(w, http.StatusNotFound, codeMissing, "题目不存在")
		return
	}
	// 题目所属题库需对用户可见（同作答口径）
	if _, ok := m.findVisibleBank(r.Context(), bankID, rc); !ok {
		fail(w, http.StatusNotFound, codeMissing, "题目不存在")
		return
	}
	analysis := cutRunes(rawString(body["analysis"]), 2000)
	if analysis != "" {
		_, err = m.DB.ExecContext(r.Context(),
			`UPDATE quiz_question SET analysis = ?, analysis_status = 'done' WHERE id = ?`, analysis, questionID)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeOK(w, map[string]any{"analysis": analysis}, "解析已更新")
		return
	}
	_, err = m.DB.ExecContext(r.Context(),
		`UPDATE quiz_question SET analysis = NULL, analysis_status = 'none' WHERE id = ?`, questionID)
	if err != nil {
		writeErr(w, err)
		return
	}
	m.Queue().Enqueue([]int64{questionID})
	writeOK(w, map[string]any{"analysis": nil}, "已清空解析，将重新生成")
}

// ===== POST /practice/answer：提交作答判分 =====

func (m *Module) practiceAnswer(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：请求体不是合法 JSON")
		return
	}
	questionID := rawNumberOr0(body["questionId"])
	if questionID == 0 {
		fail(w, http.StatusBadRequest, codeParam, "参数不完整")
		return
	}
	var qBankID int64
	var qAnswer, qAnalysis sql.NullString
	if err := m.DB.QueryRowContext(r.Context(),
		`SELECT bank_id, answer, analysis FROM quiz_question WHERE id = ? AND status = 1`, questionID).
		Scan(&qBankID, &qAnswer, &qAnalysis); err != nil {
		fail(w, http.StatusNotFound, codeMissing, "题目不存在")
		return
	}
	// 题目所属题库需对用户可见（全部池全员可答；班组池仅本班，防跨班直答题）
	if _, ok := m.findVisibleBank(r.Context(), qBankID, rc); !ok {
		fail(w, http.StatusNotFound, codeMissing, "题目不存在")
		return
	}
	userAnswer := normalizeUserAnswer(rawString(body["answer"]))
	if userAnswer == "" {
		fail(w, http.StatusBadRequest, codeParam, "请作答后再提交")
		return
	}
	right := 0
	if userAnswer == qAnswer.String {
		right = 1
	}
	_, err = m.DB.ExecContext(r.Context(),
		`INSERT INTO quiz_record (user_id, bank_id, question_id, is_right, user_answer) VALUES (?, ?, ?, ?, ?)`,
		rc.ident.ID, qBankID, questionID, right, userAnswer)
	if err != nil {
		writeErr(w, err)
		return
	}
	var wrong map[string]any
	if right == 0 {
		// 答错：upsert 错题本（累计答错次数 +1、连对清零、刷新最近答错时间）
		_, _ = m.DB.ExecContext(r.Context(),
			`INSERT INTO quiz_wrong (user_id, bank_id, question_id, wrong_count, right_streak, last_wrong_at)
       VALUES (?, ?, ?, 1, 0, NOW())
       ON DUPLICATE KEY UPDATE wrong_count = wrong_count + 1, right_streak = 0, last_wrong_at = NOW()`,
			rc.ident.ID, qBankID, questionID)
		wrong = map[string]any{"inBook": true, "rightStreak": 0, "removed": false}
	} else {
		// 答对：在本子中则连对 +1，达 3 移出；不在本子无需处理
		var wrongID, streak int64
		err := m.DB.QueryRowContext(r.Context(),
			`SELECT id, right_streak FROM quiz_wrong WHERE user_id = ? AND question_id = ?`, rc.ident.ID, questionID).
			Scan(&wrongID, &streak)
		if err != nil {
			wrong = map[string]any{"inBook": false, "rightStreak": 0, "removed": false}
		} else {
			streak++
			if streak >= 3 {
				_, _ = m.DB.ExecContext(r.Context(), `DELETE FROM quiz_wrong WHERE id = ?`, wrongID)
				wrong = map[string]any{"inBook": false, "rightStreak": streak, "removed": true}
			} else {
				_, _ = m.DB.ExecContext(r.Context(), `UPDATE quiz_wrong SET right_streak = ? WHERE id = ?`, streak, wrongID)
				wrong = map[string]any{"inBook": true, "rightStreak": streak, "removed": false}
			}
		}
	}
	// 本题作答统计（含本次提交；本人/全员两个口径）
	var allTotal, allRights, myTotal, myRights sql.NullInt64
	_ = m.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(*) AS total, SUM(is_right) AS rights FROM quiz_record WHERE question_id = ?`, questionID).
		Scan(&allTotal, &allRights)
	_ = m.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(*) AS total, SUM(is_right) AS rights FROM quiz_record WHERE question_id = ? AND user_id = ?`,
		questionID, rc.ident.ID).Scan(&myTotal, &myRights)
	stats := map[string]any{
		"myTimes":      myTotal.Int64,
		"myRightRate":  rateOrNil(myTotal.Int64, myRights.Int64),
		"allTimes":     allTotal.Int64,
		"allRightRate": rateOrNil(allTotal.Int64, allRights.Int64),
	}
	var answerOut, analysisOut any
	if qAnswer.Valid {
		answerOut = qAnswer.String
	}
	if qAnalysis.Valid {
		analysisOut = qAnalysis.String
	}
	writeOK(w, map[string]any{
		"right": right == 1, "answer": answerOut, "analysis": analysisOut,
		"wrong": wrong, "stats": stats,
	})
}

// rateOrNil 正确率百分比（无样本返回 null）；取整对齐 Node Math.round（非截断）。
func rateOrNil(total, rights int64) any {
	if total == 0 {
		return nil
	}
	return int(math.Round(float64(rights) / float64(total) * 100))
}

// ===== GET /wrongs：当前用户错题本（最近答错倒序，含来源题库） =====

func (m *Module) wrongs(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	bankID, _ := strconv.ParseInt(r.URL.Query().Get("bankId"), 10, 64)
	teamID := rc.teamIDOr0()
	where := ""
	params := []any{rc.ident.ID, teamID}
	if bankID != 0 {
		where = " AND w.bank_id = ?"
		params = append(params, bankID)
	}
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT w.question_id, w.bank_id, b.name AS bank_name, q.type, q.content, q.options,
       w.wrong_count, w.right_streak, DATE_FORMAT(w.last_wrong_at, '%Y-%m-%d %H:%i:%s') AS last_wrong_at
     FROM quiz_wrong w
     JOIN quiz_question q ON q.id = w.question_id AND q.status = 1
     JOIN quiz_bank b ON b.id = w.bank_id AND b.status = 1
     WHERE w.user_id = ? AND (b.scope = 'all' OR (b.scope = 'team' AND b.team_id = ?))`+where+`
     ORDER BY w.last_wrong_at DESC, w.id DESC`, params...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	list := []any{}
	for rows.Next() {
		var questionID, bankRowID, wrongCount, rightStreak int64
		var bankName, questionType, content, lastWrongAt string
		var optionsRaw []byte
		if rows.Scan(&questionID, &bankRowID, &bankName, &questionType, &content, &optionsRaw, &wrongCount, &rightStreak, &lastWrongAt) != nil {
			continue
		}
		list = append(list, map[string]any{
			"questionId":  questionID,
			"bankId":      bankRowID,
			"bankName":    bankName,
			"type":        questionType,
			"content":     content,
			"options":     parseOptions(string(optionsRaw)),
			"wrongCount":  wrongCount,
			"rightStreak": rightStreak,
			"lastWrongAt": lastWrongAt,
		})
	}
	writeOK(w, map[string]any{"list": list})
}

// ===== DELETE /wrongs/{questionId}：移出错题本（按人） =====

func (m *Module) wrongDelete(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	questionID, _ := strconv.ParseInt(chi.URLParam(r, "questionId"), 10, 64)
	_, _ = m.DB.ExecContext(r.Context(),
		`DELETE FROM quiz_wrong WHERE user_id = ? AND question_id = ?`, rc.ident.ID, questionID)
	writeOK(w, nil)
}

// ===== POST /favorites/{questionId}：收藏（幂等） =====

func (m *Module) favoriteAdd(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	questionID, _ := strconv.ParseInt(chi.URLParam(r, "questionId"), 10, 64)
	var bankID int64
	if err := m.DB.QueryRowContext(r.Context(),
		`SELECT bank_id FROM quiz_question WHERE id = ? AND status = 1`, questionID).Scan(&bankID); err != nil {
		fail(w, http.StatusNotFound, codeMissing, "题目不存在")
		return
	}
	if _, ok := m.findVisibleBank(r.Context(), bankID, rc); !ok {
		fail(w, http.StatusNotFound, codeMissing, "题目不存在")
		return
	}
	_, _ = m.DB.ExecContext(r.Context(),
		`INSERT IGNORE INTO quiz_favorite (user_id, bank_id, question_id) VALUES (?, ?, ?)`,
		rc.ident.ID, bankID, questionID)
	writeOK(w, nil)
}

// ===== DELETE /favorites/{questionId}：取消收藏（按人） =====

func (m *Module) favoriteRemove(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	questionID, _ := strconv.ParseInt(chi.URLParam(r, "questionId"), 10, 64)
	_, _ = m.DB.ExecContext(r.Context(),
		`DELETE FROM quiz_favorite WHERE user_id = ? AND question_id = ?`, rc.ident.ID, questionID)
	writeOK(w, nil)
}
