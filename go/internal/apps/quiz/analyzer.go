// 题库刷题：AI 解析进程内队列（对齐 server/src/quiz/analyzer.js，全局并发限 3）。
// 去重入队 + 3 个 worker：取题置 pending，成功写 analysis + done，异常置 failed；
// 未配置 Dify 时入队静默跳过（返回 0），解析功能整体停用，刷题等其余功能不受影响。
package quiz

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// QuestionForAI 单题解析入参。
type QuestionForAI struct {
	ID      int64
	Type    string
	Content string
	Options []string
	Answer  string
}

// QuestionAnalyzer AI 解析适配层（Dify 实现见 DifyAnalyzer；测试可注入桩）。
type QuestionAnalyzer interface {
	// Configured 是否已配置（未配置时解析整体停用）。
	Configured() bool
	// Analyze 生成单题解析，成功返回非空解析文本。
	Analyze(ctx context.Context, q QuestionForAI) (string, error)
}

// analysisWorkers 全局并发上限（硬性要求）。
const analysisWorkers = 3

// analysisQueue 进程内解析队列。
type analysisQueue struct {
	db     *sql.DB
	an     QuestionAnalyzer
	logger AppLogger // 应用日志（可注入；nil 降级标准库 log）
	mu     sync.Mutex
	inSet  map[int64]bool // 已入队/处理中的题目 id（防重复入队）
	queue  chan int64
	once   sync.Once
}

// newAnalysisQueue 构造队列（通道缓冲 1024，超出同步丢弃由 enqueue 计数控制）。
func newAnalysisQueue(db *sql.DB, an QuestionAnalyzer, logger AppLogger) *analysisQueue {
	return &analysisQueue{
		db:     db,
		an:     an,
		logger: logger,
		inSet:  map[int64]bool{},
		queue:  make(chan int64, 1024),
	}
}

// startWorkers 懒启动 3 个 worker。
func (q *analysisQueue) startWorkers() {
	q.once.Do(func() {
		for i := 0; i < analysisWorkers; i++ {
			go q.workerLoop(i + 1)
		}
	})
}

// workerLoop 单 worker 循环取题处理。
func (q *analysisQueue) workerLoop(idx int) {
	for id := range q.queue {
		q.processOne(context.Background(), id)
	}
}

// processOne 处理单题：任何异常都归为 failed（不抛出，worker 循环不中断）。
func (q *analysisQueue) processOne(ctx context.Context, id int64) {
	defer func() {
		q.mu.Lock()
		delete(q.inSet, id)
		q.mu.Unlock()
	}()
	if q.db == nil {
		return
	}
	var (
		questionType string
		content      string
		optionsRaw   string
		answer       string
	)
	err := q.db.QueryRowContext(ctx,
		`SELECT type, content, options, answer FROM quiz_question WHERE id = ?`, id).
		Scan(&questionType, &content, &optionsRaw, &answer)
	if err != nil {
		return // 题已被删除（如导入全量替换）
	}
	_, _ = q.db.ExecContext(ctx, `UPDATE quiz_question SET analysis_status = 'pending' WHERE id = ?`, id)
	analysis, err := q.an.Analyze(ctx, QuestionForAI{
		ID: id, Type: questionType, Content: content,
		Options: parseOptions(optionsRaw), Answer: answer,
	})
	if err != nil || analysis == "" {
		if err == nil {
			err = errEmptyAnalysis
		}
		appLogWith(q.logger, ctx, "error", "quiz.analysis.failed",
			fmt.Sprintf("题目 %d AI 解析失败：%v", id, err), map[string]any{"question_id": id})
		_, _ = q.db.ExecContext(ctx, `UPDATE quiz_question SET analysis_status = 'failed' WHERE id = ?`, id)
		return
	}
	_, _ = q.db.ExecContext(ctx,
		`UPDATE quiz_question SET analysis = ?, analysis_status = 'done' WHERE id = ?`, analysis, id)
}

// errEmptyAnalysis 解析为空。
var errEmptyAnalysis = errString("Dify 返回的解析为空")

type errString string

func (e errString) Error() string { return string(e) }

// Enqueue 批量入队，返回实际入队数（重复/未配置 Dify 的跳过）。
func (q *analysisQueue) Enqueue(questionIDs []int64) int {
	if q == nil || q.an == nil || !q.an.Configured() {
		return 0
	}
	q.startWorkers()
	q.mu.Lock()
	count := 0
	for _, id := range questionIDs {
		if id <= 0 || q.inSet[id] {
			continue
		}
		q.inSet[id] = true
		select {
		case q.queue <- id:
			count++
		default:
			delete(q.inSet, id) // 队列满：丢弃（可经 analyze-retry 重入）
		}
	}
	q.mu.Unlock()
	return count
}

// EnqueueBank 按题库 + 解析状态批量入队（如导入后补 none、失败后重试 none/failed）。
func (q *analysisQueue) EnqueueBank(ctx context.Context, bankID int64, statuses []string) int {
	if q == nil || q.an == nil || !q.an.Configured() {
		return 0
	}
	if len(statuses) == 0 {
		statuses = []string{"none"}
	}
	holders := make([]string, 0, len(statuses))
	args := []any{bankID}
	for _, s := range statuses {
		holders = append(holders, "?")
		args = append(args, s)
	}
	query := `SELECT id FROM quiz_question WHERE bank_id = ? AND status = 1 AND analysis_status IN (` +
		joinComma(holders) + `)`
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return 0
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return q.Enqueue(ids)
}

// joinComma 占位符拼接。
func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
