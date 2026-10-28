// 题库刷题：题目管理（分页/增删改/解析重试）与 Excel 导入/模板（对齐 server/src/quiz/index.js）。
package quiz

import (
	"bytes"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/xuri/excelize/v2"
)

// questionItem 题目出参项。
func questionItem(id int64, questionType, content string, optionsRaw any, answer string, analysis sql.NullString, analysisStatus, createdAt string) map[string]any {
	var analysisOut any
	if analysis.Valid && analysis.String != "" {
		analysisOut = analysis.String
	} else {
		analysisOut = nil
	}
	return map[string]any{
		"id":             id,
		"type":           questionType,
		"content":        content,
		"options":        parseOptions(optionsRaw),
		"answer":         answer,
		"analysis":       analysisOut,
		"analysisStatus": analysisStatus,
		"createdAt":      createdAt,
	}
}

// ===== GET /banks/{id}/questions：题目分页列表（q 模糊匹配题干） =====

func (m *Module) questionList(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	bankID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	b, ok := m.findManageBank(r.Context(), bankID, rc)
	if !ok {
		fail(w, http.StatusNotFound, codeMissing, "题库不存在")
		return
	}
	// page/size 钳制对齐 Node：parseInt||1/||20（0/非数字落默认值）后 Math.max(1,…)、Math.min(100,…)
	page := 1
	if v, ok := jsParseInt(r.URL.Query().Get("page")); ok && v != 0 {
		if v < 1 {
			v = 1
		}
		page = v
	}
	size := 20
	if v, ok := jsParseInt(r.URL.Query().Get("size")); ok && v != 0 {
		size = v
		if size < 1 {
			size = 1
		}
		if size > 100 {
			size = 100
		}
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	where := ""
	args := []any{b.ID}
	if q != "" {
		where = " AND content LIKE ?"
		args = append(args, "%"+q+"%")
	}
	var total int64
	_ = m.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(*) AS total FROM quiz_question WHERE bank_id = ?`+where, args...).Scan(&total)
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT id, type, content, options, answer, analysis, analysis_status,
       DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s') AS created_at
     FROM quiz_question WHERE bank_id = ?`+where+`
     ORDER BY sort, id LIMIT `+strconv.Itoa(size)+` OFFSET `+strconv.Itoa((page-1)*size), args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	list := []any{}
	for rows.Next() {
		var id int64
		var questionType, content, answer, analysisStatus, createdAt string
		var optionsRaw []byte
		var analysis sql.NullString
		if err := rows.Scan(&id, &questionType, &content, &optionsRaw, &answer, &analysis, &analysisStatus, &createdAt); err != nil {
			continue
		}
		list = append(list, questionItem(id, questionType, content, string(optionsRaw), answer, analysis, analysisStatus, createdAt))
	}
	writeOK(w, map[string]any{"total": total, "list": list})
}

// ===== POST /banks/{id}/questions：新增题目 =====

func (m *Module) questionCreate(w http.ResponseWriter, r *http.Request, rc requestCtx) {
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
	v, errMsg := validateQuestionPayload(body)
	if errMsg != "" {
		fail(w, http.StatusBadRequest, codeParam, errMsg)
		return
	}
	var maxSort int64
	_ = m.DB.QueryRowContext(r.Context(),
		`SELECT COALESCE(MAX(sort), 0) AS m FROM quiz_question WHERE bank_id = ?`, b.ID).Scan(&maxSort)
	status := "none"
	var analysis any
	if v.Analysis != "" {
		status = "done"
		analysis = v.Analysis
	}
	res, err := m.DB.ExecContext(r.Context(),
		`INSERT INTO quiz_question (bank_id, type, content, options, answer, analysis, analysis_status, sort) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, v.Type, v.Content, mustJSONOptions(v.Options), v.Answer, analysis, status, maxSort+1)
	if err != nil {
		writeErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	if v.Analysis == "" {
		m.Queue().Enqueue([]int64{id})
	}
	writeOK(w, map[string]any{"id": id})
}

// mustJSONOptions 选项数组序列化。
func mustJSONOptions(options []string) string {
	if options == nil {
		options = []string{}
	}
	parts := make([]string, 0, len(options))
	for _, o := range options {
		parts = append(parts, strconv.Quote(o))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// ===== PUT /questions/{id}：修改题目 =====

func (m *Module) questionUpdate(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	questionID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var bankID int64
	if err := m.DB.QueryRowContext(r.Context(),
		`SELECT bank_id FROM quiz_question WHERE id = ?`, questionID).Scan(&bankID); err != nil {
		fail(w, http.StatusNotFound, codeMissing, "题目不存在")
		return
	}
	if _, ok := m.findManageBank(r.Context(), bankID, rc); !ok {
		fail(w, http.StatusNotFound, codeMissing, "题目不存在")
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：请求体不是合法 JSON")
		return
	}
	v, errMsg := validateQuestionPayload(body)
	if errMsg != "" {
		fail(w, http.StatusBadRequest, codeParam, errMsg)
		return
	}
	status := "none"
	var analysis any
	if v.Analysis != "" {
		status = "done"
		analysis = v.Analysis
	}
	_, err = m.DB.ExecContext(r.Context(),
		`UPDATE quiz_question SET type = ?, content = ?, options = ?, answer = ?, analysis = ?, analysis_status = ? WHERE id = ?`,
		v.Type, v.Content, mustJSONOptions(v.Options), v.Answer, analysis, status, questionID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if v.Analysis == "" {
		m.Queue().Enqueue([]int64{questionID})
	}
	writeOK(w, nil)
}

// ===== DELETE /questions/{id}：删除题目（连带删答题记录/错题/收藏） =====

func (m *Module) questionDelete(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	questionID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var bankID int64
	if err := m.DB.QueryRowContext(r.Context(),
		`SELECT bank_id FROM quiz_question WHERE id = ?`, questionID).Scan(&bankID); err != nil {
		fail(w, http.StatusNotFound, codeMissing, "题目不存在")
		return
	}
	if _, ok := m.findManageBank(r.Context(), bankID, rc); !ok {
		fail(w, http.StatusNotFound, codeMissing, "题目不存在")
		return
	}
	for _, stmt := range []string{
		`DELETE FROM quiz_record WHERE question_id = ?`,
		`DELETE FROM quiz_wrong WHERE question_id = ?`,
		`DELETE FROM quiz_favorite WHERE question_id = ?`,
		`DELETE FROM quiz_question WHERE id = ?`,
	} {
		if _, err := m.DB.ExecContext(r.Context(), stmt, questionID); err != nil {
			writeErr(w, err)
			return
		}
	}
	writeOK(w, nil)
}

// ===== POST /banks/{id}/analyze-retry：none/failed 重新入 AI 队列 =====

func (m *Module) analyzeRetry(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	bankID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	b, ok := m.findManageBank(r.Context(), bankID, rc)
	if !ok {
		fail(w, http.StatusNotFound, codeMissing, "题库不存在")
		return
	}
	queued := m.Queue().EnqueueBank(r.Context(), b.ID, []string{"none", "failed"})
	writeOK(w, map[string]any{"queued": queued})
}

// ===== GET /banks/template：题库导入模板下载（xlsx） =====

func (m *Module) bankTemplate(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	f := excelize.NewFile()
	defer f.Close()
	sheet := "题目"
	_ = f.SetSheetName("Sheet1", sheet)
	rows := [][]string{
		{"题型", "题干", "选项A", "选项B", "选项C", "选项D", "选项E", "选项F", "答案", "解析"},
		{"单选题", "示例：安全带的正确挂扣方式是（ ）。", "高挂低用", "低挂高用", "平挂平用", "随意挂扣", "", "", "A", "安全带应高挂低用，坠落时冲击力更小。"},
		{"多选题", "示例：下列属于个人安全防护用品的有（ ）。", "安全帽", "安全带", "绝缘手套", "普通布鞋", "", "", "ABC", "普通布鞋不属于安全防护用品。"},
		{"判断题", "示例：雷雨天气可以进行户外登塔作业。", "", "", "", "", "", "", "B", "雷雨天气禁止户外登塔作业。"},
	}
	for i, row := range rows {
		for j, cell := range row {
			addr, _ := excelize.CoordinatesToCellName(j+1, i+1)
			_ = f.SetCellValue(sheet, addr, cell)
		}
	}
	widths := []float64{8, 50, 14, 14, 14, 14, 14, 14, 8, 40}
	for i, w := range widths {
		col, _ := excelize.ColumnNumberToName(i + 1)
		_ = f.SetColWidth(sheet, col, col, w)
	}
	tips := "填写说明"
	_, _ = f.NewSheet(tips)
	tipRows := []string{
		"题库导入填写说明",
		"1. 只读取第一张 sheet，首行表头固定为：题型 | 题干 | 选项A~F | 答案 | 解析。",
		"2. 题型列填写：单选题、多选题、判断题（兼容 单选/多选/判断 及 single/multiple/judge，不区分大小写）。",
		"3. 单选/多选题选项A~F 至少填写 2 个，按序取非空列；判断题无需填写选项，固定为「正确/错误」。",
		"4. 答案列：单选填 1 个字母（如 A）；多选填 2 个及以上字母（如 ABC，顺序不限）；判断填 对/错（或 A/B）。答案引用 E/F 时，对应 选项E/选项F 列必须已填写内容。",
		"5. 解析列可留空，留空的题目入库后由 AI 自动生成解析（需配置 DIFY_QUIZ_API_KEY）。",
		"6. 导入为全量替换该题库题目，导入前请删除第一张 sheet 的 3 行示例，仅保留表头与正式题目。",
	}
	for i, line := range tipRows {
		addr, _ := excelize.CoordinatesToCellName(1, i+1)
		_ = f.SetCellValue(tips, addr, line)
	}
	_ = f.SetColWidth(tips, "A", "A", 100)
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition",
		`attachment; filename="quiz-template.xlsx"; filename*=UTF-8''`+urlPathEscape("题库导入模板.xlsx"))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// urlPathEscape 文件名转义（RFC 5987）。
func urlPathEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// importRowError 导入逐行校验错误明细。
type importRowError struct {
	Row     int    `json:"row"`
	Message string `json:"message"`
}

// ===== POST /banks/{id}/import：导入题目 Excel（全量替换该库题目） =====

func (m *Module) bankImport(w http.ResponseWriter, r *http.Request, rc requestCtx) {
	bankID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	b, ok := m.findManageBank(r.Context(), bankID, rc)
	if !ok {
		fail(w, http.StatusNotFound, codeMissing, "题库不存在")
		return
	}
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		fail(w, http.StatusBadRequest, codeParam, "文件大小应在 10MB 以内")
		return
	}
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		fail(w, http.StatusBadRequest, codeParam, "请选择要上传的 Excel 文件")
		return
	}
	fh := files[0]
	f, err := fh.Open()
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "请选择要上传的 Excel 文件")
		return
	}
	data := make([]byte, 0, 1<<16)
	buf := make([]byte, 32<<10)
	total := 0
	for {
		n, rerr := f.Read(buf)
		total += n
		if total > 10*1024*1024 {
			f.Close()
			fail(w, http.StatusBadRequest, codeParam, "文件大小应在 10MB 以内")
			return
		}
		data = append(data, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	f.Close()
	if len(data) == 0 {
		fail(w, http.StatusBadRequest, codeParam, "请选择要上传的 Excel 文件")
		return
	}
	fname := fh.Filename
	if !strings.HasSuffix(strings.ToLower(fname), ".xlsx") {
		fail(w, http.StatusBadRequest, codeParam, "仅支持 .xlsx 文件，请使用模板填写")
		return
	}
	wb, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "Excel 解析失败，请使用模板文件填写")
		return
	}
	defer wb.Close()
	sheets := wb.GetSheetList()
	if len(sheets) == 0 {
		fail(w, http.StatusBadRequest, codeParam, "Excel 解析失败，请使用模板文件填写")
		return
	}
	rows, err := wb.GetRows(sheets[0]) // 只读第一张 sheet
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "Excel 解析失败，请使用模板文件填写")
		return
	}

	// 逐行校验（跳过第 1 行表头；空行跳过）：有错整体失败，不入库
	type importRow struct {
		questionType string
		content      string
		options      []string
		answer       string
		analysis     string
	}
	dataRows := []importRow{}
	rowErrors := []importRowError{}
	for i := 1; i < len(rows); i++ {
		cells := make([]string, 10)
		// 整行判空对齐 Node（cells.every(c => !c)：全列含 J 列之后都参与），仅 J 列后有内容的行
		// 不跳过、按题型无法识别出行错误
		rowEmpty := true
		for j, raw := range rows[i] {
			c := strings.TrimSpace(raw)
			if j < 10 {
				cells[j] = c
			}
			if c != "" {
				rowEmpty = false
			}
		}
		if rowEmpty {
			continue
		}
		rowNo := i + 1 // Excel 行号（1-based，含表头偏移）
		errMsg := ""
		questionType := normalizeType(cells[0])
		switch {
		case questionType == "":
			errMsg = "题型无法识别（应为 单选题/多选题/判断题）"
		case cells[1] == "":
			errMsg = "题干不能为空"
		default:
			options := []string{}
			if questionType == "judge" {
				options = []string{"正确", "错误"}
			} else {
				for _, c := range cells[2:8] {
					if c != "" {
						options = append(options, c)
					}
				}
			}
			if questionType != "judge" && len(options) < 2 {
				errMsg = "选项至少填写 2 个"
			} else {
				a := normalizeAnswer(questionType, cells[8], len(options))
				if a.Error != "" {
					errMsg = a.Error
				} else {
					analysis := cells[9]
					status := "none"
					if analysis != "" {
						status = "done"
					}
					_ = status
					dataRows = append(dataRows, importRow{
						questionType: questionType, content: cells[1],
						options: options, answer: a.Answer, analysis: analysis,
					})
				}
			}
		}
		if errMsg != "" {
			rowErrors = append(rowErrors, importRowError{Row: rowNo, Message: errMsg})
			if len(rowErrors) >= 20 {
				break // 错误最多返回 20 条
			}
		}
	}
	if len(rowErrors) > 0 {
		failRows(w, rowErrors, fmt.Sprintf("共 %d 行校验未通过，请修正后重新导入", len(rowErrors)))
		return
	}
	if len(dataRows) == 0 {
		fail(w, http.StatusBadRequest, codeParam, "未识别到有效题目行，请按模板列填写")
		return
	}

	// 全量替换：先删该库旧答题记录/错题/收藏/题目，再分批插入（sort 按行序）
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
	} {
		if _, err := tx.ExecContext(r.Context(), stmt, b.ID); err != nil {
			_ = tx.Rollback()
			writeErr(w, err)
			return
		}
	}
	for i := 0; i < len(dataRows); i += 500 {
		end := i + 500
		if end > len(dataRows) {
			end = len(dataRows)
		}
		chunk := dataRows[i:end]
		valueSQL := make([]string, 0, len(chunk))
		args := make([]any, 0, len(chunk)*8)
		for j, row := range chunk {
			valueSQL = append(valueSQL, "(?, ?, ?, ?, ?, ?, ?, ?)")
			var analysis any
			status := "none"
			if row.analysis != "" {
				analysis = row.analysis
				status = "done"
			}
			args = append(args, b.ID, row.questionType, row.content, mustJSONOptions(row.options),
				row.answer, analysis, status, i+j+1)
		}
		if _, err := tx.ExecContext(r.Context(),
			`INSERT INTO quiz_question (bank_id, type, content, options, answer, analysis, analysis_status, sort) VALUES `+
				strings.Join(valueSQL, ","), args...); err != nil {
			_ = tx.Rollback()
			writeErr(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeErr(w, err)
		return
	}

	// 提交后把无解析的题入 AI 队列（未配置 Dify 时静默跳过，queued=0）
	queued := m.Queue().EnqueueBank(r.Context(), b.ID, []string{"none"})
	writeOK(w, map[string]any{"imported": len(dataRows), "queued": queued},
		fmt.Sprintf("已导入 %d 道题目", len(dataRows)))
}
