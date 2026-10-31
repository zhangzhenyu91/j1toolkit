package httpapi

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"shade/internal/config"
	"shade/internal/logging"
)

// SQLAppCatalog 从 sys_app 读取应用目录，保持现有 sort/id 顺序。
type SQLAppCatalog struct{ DB *sql.DB }

func (c SQLAppCatalog) ListApps(ctx context.Context) ([]App, error) {
	rows, err := c.DB.QueryContext(ctx, `SELECT app_key, name, icon, path, terminal, sort, status FROM sys_app WHERE status = 1 ORDER BY sort, id`)
	if err != nil {
		return nil, DependencyUnavailable("应用目录读取失败")
	}
	defer rows.Close()
	out := make([]App, 0)
	for rows.Next() {
		var a App
		var status int
		if err := rows.Scan(&a.Code, &a.Name, &a.Icon, &a.Path, &a.Terminal, &a.Sort, &status); err != nil {
			return nil, err
		}
		a.AppKey = a.Code
		a.Enabled = status == 1
		out = append(out, a)
	}
	return out, rows.Err()
}

// adminOnlyAppKeys admin 无需授权即可见的管理应用（app/list 口径）。
var adminOnlyAppKeys = map[string]bool{"wechat-answer": true, "app-log": true}

// ListAppsForUser 按登录用户权限过滤（sys_user_app 授权且启用）；admin 追加管理应用
// wechat-answer / app-log（sys_app 中启用的行，已含则不重复）。
func (c SQLAppCatalog) ListAppsForUser(ctx context.Context, userID int64, role string) ([]App, error) {
	rows, err := c.DB.QueryContext(ctx,
		`SELECT a.app_key, a.name, a.icon, a.path, a.terminal, a.sort, a.status FROM sys_user_app ua
		 JOIN sys_app a ON a.id = ua.app_id
		 WHERE ua.user_id = ? AND a.status = 1
		 ORDER BY a.sort, a.id`, userID)
	if err != nil {
		return nil, DependencyUnavailable("应用目录读取失败")
	}
	out := make([]App, 0)
	seen := map[string]bool{}
	for rows.Next() {
		var a App
		var status int
		if err := rows.Scan(&a.Code, &a.Name, &a.Icon, &a.Path, &a.Terminal, &a.Sort, &status); err != nil {
			rows.Close()
			return nil, err
		}
		a.AppKey = a.Code
		a.Enabled = status == 1
		seen[a.Code] = true
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if role != "admin" {
		return out, nil
	}
	// admin 追加管理应用（按 sort,id 排在授权列表之后亦可；这里保持 sys_app 顺序合并）
	extraRows, err := c.DB.QueryContext(ctx,
		`SELECT app_key, name, icon, path, terminal, sort, status FROM sys_app
		 WHERE status = 1 AND app_key IN ('wechat-answer', 'app-log') ORDER BY sort, id`)
	if err != nil {
		return nil, DependencyUnavailable("应用目录读取失败")
	}
	defer extraRows.Close()
	for extraRows.Next() {
		var a App
		var status int
		if err := extraRows.Scan(&a.Code, &a.Name, &a.Icon, &a.Path, &a.Terminal, &a.Sort, &status); err != nil {
			return nil, err
		}
		if seen[a.Code] || !adminOnlyAppKeys[a.Code] {
			continue
		}
		a.AppKey = a.Code
		a.Enabled = status == 1
		seen[a.Code] = true
		out = append(out, a)
	}
	return out, extraRows.Err()
}

// StoreConfigAdmin 把 config.Store 适配为 HTTP 配置管理接口。
type StoreConfigAdmin struct{ Store *config.Store }

func (a StoreConfigAdmin) Get(ctx context.Context, key string) (ConfigItem, error) {
	value, err := a.Store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ConfigItem{}, NotFound("配置键不存在: " + key)
		}
		return ConfigItem{}, err
	}
	updated, err := a.Store.UpdatedAt(ctx, key)
	if err != nil {
		return ConfigItem{}, err
	}
	return ConfigItem{Key: key, Group: config.GroupForKey(key), Value: value, Sensitive: isSensitiveKey(key), UpdatedAt: updated}, nil
}

func isSensitiveKey(key string) bool {
	for _, spec := range config.DefaultSpecs() {
		if spec.Key == key {
			return spec.Sensitive
		}
	}
	// 自定义应用密钥默认按敏感处理，避免把未知密钥明文返回。
	return true
}

func (a StoreConfigAdmin) List(ctx context.Context) ([]ConfigItem, error) {
	values, err := a.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ConfigItem, 0, len(values))
	for _, v := range values {
		out = append(out, ConfigItem{Key: v.Key, Group: config.GroupForKey(v.Key), Value: v.Value, Sensitive: isSensitiveKey(v.Key), UpdatedAt: v.UpdatedAt})
	}
	return out, nil
}

func (a StoreConfigAdmin) Update(ctx context.Context, key, value string) error {
	if err := a.Store.Update(ctx, key, value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return NotFound("配置键不存在: " + key)
		}
		return err
	}
	return nil
}

// UpdateBatch 事务型批量写入；未知键统一映射 404，其余错误原样上抛（HTTP 层 500/503）。
func (a StoreConfigAdmin) UpdateBatch(ctx context.Context, updates []ConfigUpdate) error {
	batch := make([]config.Update, 0, len(updates))
	for _, u := range updates {
		batch = append(batch, config.Update{Key: u.Key, Value: u.Value})
	}
	if err := a.Store.UpdateBatch(ctx, batch); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return NotFound("配置键不存在或批量更新失败")
		}
		return err
	}
	return nil
}

// 列表摘要预算：detail/raw_message 截断，避免列表查询把 LONGTEXT 全量拉入内存；
// 完整内容由单条详情接口读取。
const (
	logListDetailChars   = 240
	logListRawChars      = 240
	logListMessageChars  = 500
	logCountMaxOffset    = 5000   // 深分页不回表计数
	logMaxPageOffset     = 100000 // 超过后应改用 before_time/before_id 键集分页
	logPendingAlertLimit = 10000
)

// logPageOffset 安全计算 offset 并限制最大深度：先做除法溢出检查，再拒绝过深 page，
// 避免 (page-1)*page_size 溢出后触发 COUNT 或负 OFFSET。
func logPageOffset(page, pageSize int) (int64, error) {
	if page <= 1 {
		return 0, nil
	}
	if pageSize <= 0 {
		return 0, InvalidParam("page_size 需为 1~100 的整数")
	}
	index := int64(page - 1)
	if index > math.MaxInt64/int64(pageSize) {
		return 0, InvalidParam("page 过大，请使用 before_time/before_id 键集分页")
	}
	offset := index * int64(pageSize)
	if offset > logMaxPageOffset {
		return 0, InvalidParam("page 过大，请使用 before_time/before_id 键集分页")
	}
	return offset, nil
}

// SQLLogReader 从 app_log 查询结构化日志，支持应用、级别、类别、关键词、视图与时间范围。
// 查询固定使用 occurred_at 与 (is_access,occurred_at,id) 索引；键集分页避免深 OFFSET。
type SQLLogReader struct{ DB *sql.DB }

// buildLogWhere 组装筛选条件（视图/应用/级别/类别/链路/关键词/时间/键集）。
func buildLogWhere(f LogFilter) ([]string, []any) {
	where := []string{"1 = 1"}
	args := make([]any, 0)
	switch f.View {
	case "access":
		where = append(where, "is_access = ?")
		args = append(args, 1)
	case "all":
		// 全部视图不过滤访问标记
	default:
		where = append(where, "is_access = ?")
		args = append(args, 0)
	}
	if f.AppCode != "" {
		where = append(where, "app_code = ?")
		args = append(args, f.AppCode)
	}
	if f.Level != "" {
		where = append(where, "level = ?")
		args = append(args, f.Level)
	}
	if f.Category != "" {
		where = append(where, "category = ?")
		args = append(args, f.Category)
	}
	if f.TraceID != "" {
		where = append(where, "trace_id = ?")
		args = append(args, f.TraceID)
	}
	if f.Keyword != "" {
		where = append(where, "(message LIKE ? OR message_cn LIKE ? OR raw_message LIKE ? OR event LIKE ?)")
		keyword := "%" + f.Keyword + "%"
		args = append(args, keyword, keyword, keyword, keyword)
	}
	if !f.Since.IsZero() {
		where = append(where, "occurred_at >= ?")
		args = append(args, f.Since)
	}
	if !f.Until.IsZero() {
		where = append(where, "occurred_at < ?")
		args = append(args, f.Until)
	}
	if !f.BeforeTime.IsZero() && f.BeforeID > 0 {
		where = append(where, "(occurred_at < ? OR (occurred_at = ? AND id < ?))")
		args = append(args, f.BeforeTime, f.BeforeTime, f.BeforeID)
	}
	return where, args
}

func (r SQLLogReader) ListLogs(ctx context.Context, f LogFilter) (LogPage, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 || f.PageSize > maxPageSize {
		f.PageSize = defaultPageSize
	}
	where, args := buildLogWhere(f)
	condition := strings.Join(where, " AND ")

	// 关键词搜索和深分页/键集分页不回表计数，total 返回 -1（未知）。
	keyset := !f.BeforeTime.IsZero() && f.BeforeID > 0
	var offset int64
	if !keyset {
		var err error
		offset, err = logPageOffset(f.Page, f.PageSize)
		if err != nil {
			return LogPage{}, err
		}
	}
	total := int64(-1)
	if !keyset && f.Keyword == "" && offset <= int64(logCountMaxOffset) {
		if err := r.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM app_log WHERE "+condition, args...).Scan(&total); err != nil {
			return LogPage{}, DependencyUnavailable("应用日志读取失败")
		}
	}

	query := `SELECT id, COALESCE(log_id, ''), app_code, level, category, event,
		LEFT(message, ?), LEFT(COALESCE(message_cn, ''), ?), LEFT(COALESCE(raw_message, ''), ?),
		LEFT(COALESCE(detail, ''), ?), COALESCE(CHAR_LENGTH(detail) > ?, 0) AS detail_truncated,
		trace_id, user_id, COALESCE(team_id, 0), occurred_at, created_at, source, is_access
		FROM app_log WHERE ` + condition + ` ORDER BY occurred_at DESC, id DESC LIMIT ?`
	listArgs := make([]any, 0, len(args)+5)
	listArgs = append(listArgs, logListMessageChars, logListMessageChars, logListRawChars, logListDetailChars, logListDetailChars)
	listArgs = append(listArgs, args...)
	listArgs = append(listArgs, f.PageSize)
	if !keyset {
		query += " OFFSET ?"
		listArgs = append(listArgs, offset)
	}

	rows, err := r.DB.QueryContext(ctx, query, listArgs...)
	if err != nil {
		return LogPage{}, DependencyUnavailable("应用日志读取失败")
	}
	defer rows.Close()
	items := make([]LogEntry, 0)
	for rows.Next() {
		var e LogEntry
		var category, event, message, trace, source string
		var messageCN, raw, detail sql.NullString
		var userID, teamID int64
		var truncated, isAccess bool
		if err := rows.Scan(&e.ID, &e.LogID, &e.AppCode, &e.Level, &category, &event,
			&message, &messageCN, &raw, &detail, &truncated,
			&trace, &userID, &teamID, &e.OccurredAt, &e.CreatedAt, &source, &isAccess); err != nil {
			return LogPage{}, err
		}
		e.Category, e.Event, e.Message, e.TraceID, e.Source = category, event, message, trace, source
		e.MessageCN, e.RawMessage, e.Detail = messageCN.String, raw.String, detail.String
		e.UserID, e.TeamID = userID, teamID
		e.DetailTruncated, e.IsAccess = truncated, isAccess
		items = append(items, e)
	}
	return LogPage{Items: items, Total: total, Page: f.Page, PageSize: f.PageSize}, rows.Err()
}

// GetLog 读取单条日志完整内容（含 context_json 与完整 detail）。
func (r SQLLogReader) GetLog(ctx context.Context, id int64) (LogEntry, error) {
	var e LogEntry
	var category, event, message, trace, source string
	var messageCN, raw, contextJSON, detail sql.NullString
	var userID, teamID int64
	var isAccess bool
	err := r.DB.QueryRowContext(ctx, `SELECT id, COALESCE(log_id, ''), app_code, level, category, event,
		message, COALESCE(message_cn, ''), COALESCE(raw_message, ''), COALESCE(context_json, ''),
		COALESCE(detail, ''), trace_id, user_id, COALESCE(team_id, 0), occurred_at, created_at, source, is_access
		FROM app_log WHERE id = ?`, id).Scan(
		&e.ID, &e.LogID, &e.AppCode, &e.Level, &category, &event,
		&message, &messageCN, &raw, &contextJSON, &detail,
		&trace, &userID, &teamID, &e.OccurredAt, &e.CreatedAt, &source, &isAccess)
	if errors.Is(err, sql.ErrNoRows) {
		return LogEntry{}, NotFound("日志不存在")
	}
	if err != nil {
		return LogEntry{}, DependencyUnavailable("应用日志读取失败")
	}
	e.Category, e.Event, e.Message, e.TraceID, e.Source = category, event, message, trace, source
	e.MessageCN, e.RawMessage, e.ContextJSON, e.Detail = messageCN.String, raw.String, contextJSON.String, detail.String
	e.UserID, e.TeamID, e.IsAccess = userID, teamID, isAccess
	return e, nil
}

// logFileSegment 导出读取的段文件；plain 为未压缩 .log。
type logFileSegment struct {
	name  string
	path  string
	base  string
	plain bool
}

// listLogFileSegments 列出 app-*.log / app-*.log.gz（同 base 优先未压缩活跃段），按 base 升序。
func listLogFileSegments(dir string) ([]logFileSegment, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, DependencyUnavailable("日志文件目录不存在: " + dir)
		}
		return nil, DependencyUnavailable("日志文件目录读取失败")
	}
	byBase := map[string]logFileSegment{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "app-") {
			continue
		}
		var seg logFileSegment
		switch {
		case strings.HasSuffix(name, ".log.gz"):
			seg = logFileSegment{name: name, path: filepath.Join(dir, name), base: strings.TrimSuffix(name, ".gz")}
		case strings.HasSuffix(name, ".log"):
			seg = logFileSegment{name: name, path: filepath.Join(dir, name), base: name, plain: true}
		default:
			continue
		}
		if prev, ok := byBase[seg.base]; ok && prev.plain {
			continue
		}
		byBase[seg.base] = seg
	}
	out := make([]logFileSegment, 0, len(byBase))
	for _, seg := range byBase {
		out = append(out, seg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].base < out[j].base })
	return out, nil
}

// openLogFile 打开段文件（归档段先解压）。
func openLogFile(seg logFileSegment) (io.Reader, func(), error) {
	f, err := os.Open(seg.path)
	if err != nil {
		return nil, nil, err
	}
	if seg.plain {
		return f, func() { _ = f.Close() }, nil
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return gz, func() {
		_ = gz.Close()
		_ = f.Close()
	}, nil
}

// logFileEntryMatches 导出筛选：与列表查询同口径（视图/应用/级别/类别/链路/关键词/时间）。
func logFileEntryMatches(e LogEntry, f LogFilter) bool {
	switch f.View {
	case "access":
		if !e.IsAccess {
			return false
		}
	case "all":
	default:
		if e.IsAccess {
			return false
		}
	}
	if f.AppCode != "" && e.AppCode != f.AppCode {
		return false
	}
	if f.Level != "" && e.Level != f.Level {
		return false
	}
	if f.Category != "" && e.Category != f.Category {
		return false
	}
	if f.TraceID != "" && e.TraceID != f.TraceID {
		return false
	}
	if f.Keyword != "" {
		hay := strings.ToLower(e.Message + "\n" + e.MessageCN + "\n" + e.RawMessage + "\n" + e.Event)
		if !strings.Contains(hay, strings.ToLower(f.Keyword)) {
			return false
		}
	}
	at := e.OccurredAt
	if at.IsZero() {
		at = e.CreatedAt
	}
	if !f.Since.IsZero() && at.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && !at.Before(f.Until) {
		return false
	}
	return true
}

// Probe 轻量预检：日志目录存在、所有段可打开、gzip 头可初始化；不读取全文。
// 用于签发原生下载 ticket 前拦截缺失目录/损坏归档，避免浏览器把 503 JSON 存成 .ndjson。
func (s NDJSONLogSource) Probe(ctx context.Context) error {
	dir := strings.TrimSpace(s.Dir)
	if dir == "" {
		return DependencyUnavailable("日志文件目录未配置")
	}
	segs, err := listLogFileSegments(dir)
	if err != nil {
		return err
	}
	for _, seg := range segs {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, closeFn, err := openLogFile(seg)
		if err != nil {
			return DependencyUnavailable("日志归档不可读：" + seg.name)
		}
		closeFn()
	}
	return nil
}

// NDJSONLogSource 从日志目录读取归档与活跃 NDJSON 段（流式过滤，不依赖数据库）。
// 文件格式与 logging.DurableSink 一致：app-YYYYMMDD-NNNNNN.log(.gz)，每行一条 JSON。
// DBIDs 可选：全量导出时用数据库 log_id 对账，发现仅存在于 DB 的磁盘回退行。
type NDJSONLogSource struct {
	Dir   string
	DBIDs func(context.Context) (map[string]struct{}, error)
}

// LogExportIncomplete 结构化不完整状态：坏行/半行/持久故障/DB-only 行导致无法证明完整导出。
type LogExportIncomplete struct {
	CorruptLines     int64
	HalfLines        int64
	DBOnly           int64
	IntegrityCorrupt bool
	DBUnavailable    bool
	Persisted        logging.IntegrityState
	PersistedSeen    bool
}

func (e *LogExportIncomplete) Error() string {
	reasons := make([]string, 0, 6)
	if e.IntegrityCorrupt {
		reasons = append(reasons, "integrity.json 损坏，历史失败未知")
	}
	if e.DBUnavailable {
		reasons = append(reasons, "数据库 log_id 对账查询失败")
	}
	if e.CorruptLines > 0 {
		reasons = append(reasons, fmt.Sprintf("坏行 %d 行", e.CorruptLines))
	}
	if e.HalfLines > 0 {
		reasons = append(reasons, fmt.Sprintf("半行 %d 处", e.HalfLines))
	}
	if e.DBOnly > 0 {
		reasons = append(reasons, fmt.Sprintf("仅数据库存在 %d 条", e.DBOnly))
	}
	if e.PersistedSeen {
		if e.Persisted.WriteErrors > 0 || e.Persisted.FsyncErrors > 0 || e.Persisted.DiskFallbacks > 0 || e.Persisted.CorruptLines > 0 {
			reasons = append(reasons, "存在历史写入/fsync/回退/损坏记录")
		}
	}
	if len(reasons) == 0 {
		return "日志文件副本无法证明完整"
	}
	return "日志文件副本不完整：" + strings.Join(reasons, "、")
}

// StreamLogs 按段文件时间顺序逐条回调。坏 JSON 行与半行不静默吞掉：
// 继续导出可读行，结束后返回结构化 *LogExportIncomplete；存在持久写入/fsync/回退/损坏记录
// 时同样返回不完整状态。全量导出且配置 DBIDs 时用数据库 log_id 对账发现 DB-only 回退行。
func (s NDJSONLogSource) StreamLogs(ctx context.Context, f LogFilter, fn func(LogEntry) error) error {
	dir := strings.TrimSpace(s.Dir)
	if dir == "" {
		return DependencyUnavailable("日志文件目录未配置")
	}
	segs, err := listLogFileSegments(dir)
	if err != nil {
		return err
	}
	var (
		corrupt, half int64
		fileIDs       map[string]struct{}
	)
	if fullExportScope(f) && s.DBIDs != nil {
		fileIDs = make(map[string]struct{})
	}
	for _, seg := range segs {
		if err := ctx.Err(); err != nil {
			return err
		}
		reader, closeFn, err := openLogFile(seg)
		if err != nil {
			return fmt.Errorf("读取日志文件 %s: %w", seg.name, err)
		}
		br := bufio.NewReaderSize(reader, 64<<10)
		for {
			if err := ctx.Err(); err != nil {
				closeFn()
				return err
			}
			line, readErr := br.ReadBytes('\n')
			if len(line) > 0 {
				if line[len(line)-1] != '\n' {
					// 尾部半行（崩溃/正在写入）：不导出，但必须记为不完整，不能声称完整副本。
					half++
				} else {
					body := bytes.TrimSpace(line)
					if len(body) > 0 {
						var e LogEntry
						if err := json.Unmarshal(body, &e); err != nil {
							corrupt++
						} else {
							if fileIDs != nil && e.LogID != "" {
								fileIDs[e.LogID] = struct{}{}
							}
							if logFileEntryMatches(e, f) {
								if e.CreatedAt.IsZero() {
									e.CreatedAt = e.OccurredAt // 文件记录无记录时间时回退事件时间
								}
								if err := fn(e); err != nil {
									closeFn()
									return err
								}
							}
						}
					}
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				closeFn()
				return fmt.Errorf("读取日志文件 %s: %w", seg.name, readErr)
			}
		}
		closeFn()
	}
	inc := &LogExportIncomplete{CorruptLines: corrupt, HalfLines: half}
	st, integrityErr := logging.ReadIntegrity(dir)
	if integrityErr != nil {
		inc.IntegrityCorrupt = true
	} else {
		inc.Persisted, inc.PersistedSeen = st, true
	}
	if fileIDs != nil {
		if dbIDs, dbErr := s.DBIDs(ctx); dbErr == nil {
			for id := range dbIDs {
				if _, ok := fileIDs[id]; !ok {
					inc.DBOnly++
				}
			}
		} else {
			inc.DBUnavailable = true
		}
	}
	if inc.CorruptLines > 0 || inc.HalfLines > 0 || inc.DBOnly > 0 ||
		inc.IntegrityCorrupt || inc.DBUnavailable ||
		(inc.PersistedSeen && (inc.Persisted.WriteErrors > 0 || inc.Persisted.FsyncErrors > 0 ||
			inc.Persisted.DiskFallbacks > 0 || inc.Persisted.CorruptLines > 0)) {
		return inc
	}
	return nil
}

// fullExportScope 全量导出（view=all 且无筛选/时间条件）才可做 DB log_id 全集对账。
func fullExportScope(f LogFilter) bool {
	return f.View == "all" && f.AppCode == "" && f.Level == "" && f.Category == "" &&
		f.TraceID == "" && f.Keyword == "" && f.Since.IsZero() && f.Until.IsZero()
}

// DurableLogHealth 把 logging.DurableSink 健康统计适配为 HTTP 快照。
type DurableLogHealth struct{ Sink *logging.DurableSink }

// LogHealth 返回健康快照；DurableSink 为空时返回零值（HTTP 层据此判定未接入）。
func (h DurableLogHealth) LogHealth() LogHealth {
	if h.Sink == nil {
		return LogHealth{}
	}
	s := h.Sink.Health()
	out := LogHealth{
		Dir: s.Dir, ActiveSegment: s.ActiveSegment, SegmentBytes: s.SegmentBytes,
		Written: s.Written, Imported: s.Imported, Pending: s.Pending,
		WriteErrors: s.WriteErrors, DiskFallbacks: s.DiskFallbacks,
		FsyncErrors: s.FsyncErrors, CorruptLines: s.CorruptLines, InsertErrors: s.InsertErrors,
		ArchivedSegments: s.ArchivedSegments, DBConnected: s.DBConnected,
		CursorSegment: s.CursorSegment, CursorOffset: s.CursorOffset,
		LastError: s.LastError,
	}
	if !s.LastImport.IsZero() {
		out.LastImport = s.LastImport.UTC().Format(time.RFC3339)
	}
	if oldest, ok := h.Sink.ArchiveCoverage(); ok {
		out.ArchiveOldest = oldest.Format("2006-01-02")
	}
	return out
}

// FixtureDeps 构造本地开发专用依赖，不访问数据库。仅当 SHADE_LOCAL_FIXTURE=1 时使用。
func FixtureDeps() Deps {
	f := &fixture{
		apps: []App{
			{Code: "work-log", Name: "出工日志", Description: "本地演示应用", Enabled: true},
			{Code: "wechat-answer", Name: "微信智能应答", Description: "本地演示应用", Enabled: true},
			{Code: "app-log", Name: "应用日志", Description: "本地演示应用", Enabled: true},
		},
		cfg: map[string]ConfigItem{
			"DIFY_API_URL":         {Key: "DIFY_API_URL", Group: "通用集成", Value: "https://dify.example.invalid", Sensitive: false, UpdatedAt: time.Now()},
			"DIFY_WORKLOG_API_KEY": {Key: "DIFY_WORKLOG_API_KEY", Group: "通用集成", Value: "local-fixture-secret", Sensitive: true, UpdatedAt: time.Now()},
		},
		logs: []LogEntry{
			{ID: 3, LogID: "local-3", AppCode: "wechat-answer", Level: "info", Category: "message", Event: "wechat.message.received", MessageCN: "收到监听群文本消息", Message: "[message] incoming group text", TraceID: "local-3", OccurredAt: time.Now().Add(-time.Minute), CreatedAt: time.Now().Add(-time.Minute), Source: "fixture"},
			{ID: 2, LogID: "local-2", AppCode: "work-log", Level: "error", Category: "external", Event: "dify.workflow.timeout", MessageCN: "照片识别工作流超时", Message: "timeout of 90000ms exceeded", TraceID: "local-2", OccurredAt: time.Now().Add(-2 * time.Minute), CreatedAt: time.Now().Add(-2 * time.Minute), Source: "fixture"},
			{ID: 1, LogID: "local-1", AppCode: "app-log", Level: "info", Category: "scheduler", Event: "log.retention.completed", MessageCN: "七天前日志清理完成", Message: "[app-log] retention cleanup completed", TraceID: "local-1", OccurredAt: time.Now().Add(-3 * time.Minute), CreatedAt: time.Now().Add(-3 * time.Minute), Source: "fixture"},
		},
	}
	return Deps{Apps: f, Cfg: f, Logs: f, Accounts: f, Notices: f}
}

type fixture struct {
	apps []App
	cfg  map[string]ConfigItem
	logs []LogEntry
}

func (f *fixture) ListApps(context.Context) ([]App, error) { return f.apps, nil }

func (f *fixture) Get(_ context.Context, key string) (ConfigItem, error) {
	it, ok := f.cfg[key]
	if !ok {
		return ConfigItem{}, NotFound("配置键不存在: " + key)
	}
	return it, nil
}

func (f *fixture) List(context.Context) ([]ConfigItem, error) {
	out := make([]ConfigItem, 0, len(f.cfg))
	for _, it := range f.cfg {
		out = append(out, it)
	}
	return out, nil
}

func (f *fixture) Update(ctx context.Context, key, value string) error {
	return f.UpdateBatch(ctx, []ConfigUpdate{{Key: key, Value: value}})
}

func (f *fixture) UpdateBatch(_ context.Context, updates []ConfigUpdate) error {
	next := make(map[string]ConfigItem, len(f.cfg))
	for k, v := range f.cfg {
		next[k] = v
	}
	now := time.Now()
	for _, u := range updates {
		it, ok := next[u.Key]
		if !ok {
			return NotFound("配置键不存在: " + u.Key)
		}
		it.Value = u.Value
		it.UpdatedAt = now
		next[u.Key] = it
	}
	f.cfg = next
	return nil
}

func (f *fixture) ListLogs(_ context.Context, filter LogFilter) (LogPage, error) {
	items := make([]LogEntry, 0)
	for _, e := range f.logs {
		switch filter.View {
		case "access":
			if !e.IsAccess {
				continue
			}
		case "all":
		default:
			if e.IsAccess {
				continue
			}
		}
		if filter.AppCode != "" && e.AppCode != filter.AppCode {
			continue
		}
		if filter.Level != "" && e.Level != filter.Level {
			continue
		}
		if filter.Category != "" && e.Category != filter.Category {
			continue
		}
		if filter.TraceID != "" && e.TraceID != filter.TraceID {
			continue
		}
		if filter.Keyword != "" && !strings.Contains(strings.ToLower(e.Message+e.MessageCN+e.RawMessage+e.Event), strings.ToLower(filter.Keyword)) {
			continue
		}
		at := e.OccurredAt
		if at.IsZero() {
			at = e.CreatedAt
		}
		if !filter.Since.IsZero() && at.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && !at.Before(filter.Until) {
			continue
		}
		if !filter.BeforeTime.IsZero() && filter.BeforeID > 0 {
			if !(at.Before(filter.BeforeTime) || (at.Equal(filter.BeforeTime) && e.ID < filter.BeforeID)) {
				continue
			}
		}
		items = append(items, e)
	}
	if !filter.BeforeTime.IsZero() && filter.BeforeID > 0 {
		filter.Page = 1 // 键集分页忽略 page，避免 fixture 重复第一页
	}
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PageSize < 1 || filter.PageSize > 100 {
		filter.PageSize = 20
	}
	start := (filter.Page - 1) * filter.PageSize
	if start > len(items) {
		start = len(items)
	}
	end := start + filter.PageSize
	if end > len(items) {
		end = len(items)
	}
	return LogPage{Items: items[start:end], Total: int64(len(items)), Page: filter.Page, PageSize: filter.PageSize}, nil
}

// GetLog 本地演示单条日志详情。
func (f *fixture) GetLog(_ context.Context, id int64) (LogEntry, error) {
	for _, e := range f.logs {
		if e.ID == id {
			return e, nil
		}
	}
	return LogEntry{}, NotFound("日志不存在")
}

// Profile 本地演示用户资料（fixture 模式固定账号）。
func (f *fixture) Profile(context.Context, int64) (UserProfile, error) {
	return UserProfile{ID: 1, Username: "demo", Nickname: "本地演示", Team: "检修一班", TeamID: 1, Role: "admin", CreatedAt: time.Now()}, nil
}

// Teams 本地演示班组列表。
func (f *fixture) Teams(context.Context) ([]TeamItem, error) {
	return []TeamItem{{ID: 1, Name: "检修一班"}, {ID: 2, Name: "运维二班"}}, nil
}

// ListNotices 本地演示通知（空列表 + 0 未读）。
func (f *fixture) ListNotices(context.Context, int64, string, int) (NoticePage, error) {
	return NoticePage{Items: []NoticeItem{}}, nil
}

// 防止适配器误把数据库错误当成无记录；供编译期检查接口一致性。
var _ AppCatalog = SQLAppCatalog{}
var _ ConfigAdmin = StoreConfigAdmin{}
var _ LogReader = SQLLogReader{}
var _ = fmt.Sprintf
