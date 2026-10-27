// 规则/管线状态/补推队列的存储抽象与 MySQL 实现。
// 管线状态（wa_record/wa_photo/wa_image）只服务更正/撤回/辅证定位与补推幂等，不是业务台账。
package wechatanswer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Rule 监听规则（含独立 Dify 密钥；API URL/User 等全局配置另走 config.Store）。
// 密钥列沿用早期命名存储（ExtractKey=tree_extract、CorrectKey=tree_correct、
// CrossingKey=crossing_extract、CrossingCorrectKey=crossing_correct），对外契约键名见 routes.go。
type Rule struct {
	ID                 int64
	GroupID            string
	GroupName          string
	Trigger            string // location | treeWatch
	Name               string // 规则名称（空时按类型默认）
	Enabled            bool
	AskEnabled         bool         // treeWatch 群内提问开关
	ExtractKey         string       // tree_extract
	CorrectKey         string       // tree_correct
	CrossingKey        string       // crossing_extract
	CrossingCorrectKey string       // crossing_correct
	AskNameKey         string       // ask_name（WF3 命名）
	ChatDigestKey      string       // chat_digest（WF4 总结）
	LocationKey        string       // location
	TeamID             int64        // 归属班组（wa_group.team_id）
	TeamName           string       // 归属班组名（legacy wa_rule.team_name；优先按 TeamID 解析）
	CreatedAt          sql.NullTime // 创建时间（CRUD 视图用）
	UpdatedAt          sql.NullTime // 更新时间（CRUD 视图用）
}

// TriggerType 规则触发类型。
const (
	TriggerLocation  = "location"
	TriggerTreeWatch = "treeWatch"
)

// RuleProvider 运行期规则查询（默认 SQLRuleProvider；测试可注入静态实现）。
type RuleProvider interface {
	RulesForGroup(ctx context.Context, groupID string) ([]Rule, error)
}

// SQLRuleProvider 从 wa_group + wa_rule 读取启用规则。
type SQLRuleProvider struct {
	DB *sql.DB
}

var _ RuleProvider = (*SQLRuleProvider)(nil)

// RulesForGroup 返回该群全部启用中的规则（含群备注与归属班组）。
func (p SQLRuleProvider) RulesForGroup(ctx context.Context, groupID string) ([]Rule, error) {
	rows, err := p.DB.QueryContext(ctx, `
    SELECT r.id, r.group_id, g.group_name, r.trigger_type, r.name, r.enabled, r.ask_enabled,
           r.extract_key, r.correct_key, r.crossing_key, r.crossing_correct_key,
           r.ask_name_key, r.chat_digest_key, r.location_key, g.team_id, r.team_name
    FROM wa_rule r JOIN wa_group g ON g.group_id = r.group_id
    WHERE r.group_id = ? AND r.enabled = 1 AND g.enabled = 1`, groupID)
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
			&r.AskNameKey, &r.ChatDigestKey, &r.LocationKey, &r.TeamID, &r.TeamName); err != nil {
			return nil, err
		}
		r.Enabled = enabled != 0
		r.AskEnabled = askEnabled != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// Record 管线技术状态（wa_record 一行 + 辅证照片）。
type Record struct {
	Key          string
	GroupID      string
	GroupName    string // 群备注（载荷 group_name；随规则解析带入，不落库）
	Kind         string // tree | crossing
	MsgID        string
	SenderID     string
	SenderName   string
	CoSenderID   string
	CoSenderName string
	Content      string
	CapturedAt   int64
	Status       string // pending | active | dropped
	Extracted    map[string]any
	LineNameRaw  string
	HandledNote  string
	PushStatus   string // 空 | ok | failed
	PushError    string
	Photos       []Photo
}

// Photo 辅证照片（wa_photo 一行）。
type Photo struct {
	ID         int64
	MsgID      string
	SenderID   string
	CapturedAt int64
	File       string
	Bytes      int
}

// ImageEntry 图片媒体缓存（wa_image 一行）。
type ImageEntry struct {
	SvrID      string
	GroupID    string
	SenderID   string
	CapturedAt int64
	FileNo     string
	AESKey     string
	DataLen    int64
	File       string
	RecordKey  string
}

// RecordStore 管线状态存储（MySQL 实现 SQLRecordStore；测试可注入内存实现）。
type RecordStore interface {
	InsertRecord(ctx context.Context, rec *Record) error
	SaveRecord(ctx context.Context, rec *Record) error
	DeleteRecord(ctx context.Context, key string) error
	// GetRecord 按技术主键取记录（不存在返回 nil）。
	GetRecord(ctx context.Context, key string) (*Record, error)
	// FindTodayRecord 当天（[dayStart,dayEnd)）同群记录定位：tree 优先、crossing 其次，msgId 优先 content 兜底。
	FindTodayRecord(ctx context.Context, groupID, msgID, content string, dayStart, dayEnd int64) (*Record, error)
	// FindNearTime 按被引用消息创建时间 ±windowSec 定位记录（extcommoninfo 简形兜底）。
	FindNearTime(ctx context.Context, groupID string, at, windowSec int64) (*Record, error)
	// FindRecallTarget 撤回启发式定位：时间窗内同 kind 记录取较新者，senderID 命中优先；matchedBy 为 sender|latest。
	FindRecallTarget(ctx context.Context, groupID, senderID string, windowSec int64, kind string, now int64) (*Record, string, error)
	// FindRecentLine 该成员最近一次同类有效记录的线路名（省略线路自动补全）。
	FindRecentLine(ctx context.Context, senderID, kind string) (string, error)
	AddPhoto(ctx context.Context, key string, p Photo) error
	// RemovePhoto 按照片 id 或图片消息 svr_id 摘除，返回被摘条目（无则 nil）。
	RemovePhoto(ctx context.Context, key, photoKey string) (*Photo, error)
	PhotosByRecord(ctx context.Context, key string) ([]Photo, error)
	CacheImage(ctx context.Context, e *ImageEntry) error
	Image(ctx context.Context, svrID string) (*ImageEntry, error)
	// SetImageFile 回写已下载本体路径。
	SetImageFile(ctx context.Context, svrID, file string) error
	AttachImage(ctx context.Context, svrID, recordKey string) error
	FindRecallImage(ctx context.Context, groupID, senderID string, windowSec int64, now int64) (*ImageEntry, error)
	ImagesByRecord(ctx context.Context, recordKey string) ([]ImageEntry, error)
	RemoveImage(ctx context.Context, svrID string) error
	// SweepImages 清理未挂接且超龄（captured_at < before）的缓存条目，返回被清条目的已下载文件路径（调用方删文件）。
	SweepImages(ctx context.Context, before int64) ([]string, error)
}

// SQLRecordStore MySQL 实现。
type SQLRecordStore struct {
	DB *sql.DB
}

var _ RecordStore = (*SQLRecordStore)(nil)

func marshalExtracted(m map[string]any) any {
	if len(m) == 0 {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return string(b)
}

func unmarshalExtracted(raw sql.NullString) map[string]any {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw.String), &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

const recordCols = `record_key, group_id, kind, msg_id, sender_id, sender_name, co_sender_id, co_sender_name,
  content, captured_at, status, extracted, line_name_raw, handled_note, push_status, push_error`

func scanRecord(row interface{ Scan(...any) error }) (*Record, error) {
	var rec Record
	var extracted sql.NullString
	if err := row.Scan(&rec.Key, &rec.GroupID, &rec.Kind, &rec.MsgID, &rec.SenderID, &rec.SenderName,
		&rec.CoSenderID, &rec.CoSenderName, &rec.Content, &rec.CapturedAt, &rec.Status,
		&extracted, &rec.LineNameRaw, &rec.HandledNote, &rec.PushStatus, &rec.PushError); err != nil {
		return nil, err
	}
	rec.Extracted = unmarshalExtracted(extracted)
	return &rec, nil
}

// InsertRecord 新增记录（status 默认 pending）。
func (s *SQLRecordStore) InsertRecord(ctx context.Context, rec *Record) error {
	_, err := s.DB.ExecContext(ctx, `
    INSERT INTO wa_record (`+recordCols+`)
    VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		rec.Key, rec.GroupID, rec.Kind, rec.MsgID, rec.SenderID, rec.SenderName,
		rec.CoSenderID, rec.CoSenderName, rec.Content, rec.CapturedAt, rec.Status,
		marshalExtracted(rec.Extracted), rec.LineNameRaw, rec.HandledNote, rec.PushStatus, rec.PushError)
	return err
}

// SaveRecord 全量更新状态列（照片另走 AddPhoto/RemovePhoto）。
func (s *SQLRecordStore) SaveRecord(ctx context.Context, rec *Record) error {
	_, err := s.DB.ExecContext(ctx, `
    UPDATE wa_record SET sender_id=?, sender_name=?, co_sender_id=?, co_sender_name=?, content=?,
      captured_at=?, status=?, extracted=?, line_name_raw=?, handled_note=?, push_status=?, push_error=?
    WHERE record_key=?`,
		rec.SenderID, rec.SenderName, rec.CoSenderID, rec.CoSenderName, rec.Content,
		rec.CapturedAt, rec.Status, marshalExtracted(rec.Extracted), rec.LineNameRaw,
		rec.HandledNote, rec.PushStatus, rec.PushError, rec.Key)
	return err
}

// DeleteRecord 删除记录（连带照片行由调用方清理）。
func (s *SQLRecordStore) DeleteRecord(ctx context.Context, key string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM wa_record WHERE record_key=?`, key)
	return err
}

// GetRecord 按技术主键取记录（不存在返回 nil）。
func (s *SQLRecordStore) GetRecord(ctx context.Context, key string) (*Record, error) {
	return s.byCond(ctx, `record_key=?`, key)
}

// FindNearTime 按被引用消息创建时间 ±windowSec 定位最近一条记录（extcommoninfo 简形兜底）。
func (s *SQLRecordStore) FindNearTime(ctx context.Context, groupID string, at, windowSec int64) (*Record, error) {
	return s.byCond(ctx,
		`group_id=? AND captured_at>=? AND captured_at<=? AND status<>'dropped'`,
		groupID, at-windowSec, at+windowSec)
}

func (s *SQLRecordStore) byCond(ctx context.Context, cond string, args ...any) (*Record, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+recordCols+` FROM wa_record WHERE `+cond+` ORDER BY id DESC LIMIT 1`, args...)
	rec, err := scanRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return rec, err
}

// FindTodayRecord 当天记录定位（tree 优先，msgId 优先 content 兜底）。
func (s *SQLRecordStore) FindTodayRecord(ctx context.Context, groupID, msgID, content string, dayStart, dayEnd int64) (*Record, error) {
	for _, kind := range []string{KindTree, KindCrossing} {
		if msgID != "" {
			rec, err := s.byCond(ctx,
				`group_id=? AND kind=? AND msg_id=? AND captured_at>=? AND captured_at<? AND status<>'dropped'`,
				groupID, kind, msgID, dayStart, dayEnd)
			if err != nil {
				return nil, err
			}
			if rec != nil {
				return rec, nil
			}
		}
		if content != "" {
			rec, err := s.byCond(ctx,
				`group_id=? AND kind=? AND captured_at>=? AND captured_at<? AND status<>'dropped' AND content=?`,
				groupID, kind, dayStart, dayEnd, strings.TrimSpace(content))
			if err != nil {
				return nil, err
			}
			if rec == nil {
				rec, err = s.byCond(ctx,
					`group_id=? AND kind=? AND captured_at>=? AND captured_at<? AND status<>'dropped' AND (content LIKE ? OR ? LIKE CONCAT('%',content,'%'))`,
					groupID, kind, dayStart, dayEnd, "%"+strings.TrimSpace(content)+"%", strings.TrimSpace(content))
				if err != nil {
					return nil, err
				}
			}
			if rec != nil {
				return rec, nil
			}
		}
	}
	return nil, nil
}

// FindRecallTarget 撤回定位（时间窗内较新者；撤回人命中优先，群主撤回他人消息兜底 latest）。
func (s *SQLRecordStore) FindRecallTarget(ctx context.Context, groupID, senderID string, windowSec int64, kind string, now int64) (*Record, string, error) {
	rows, err := s.DB.QueryContext(ctx, `
    SELECT `+recordCols+` FROM wa_record
    WHERE group_id=? AND kind=? AND captured_at>=? AND status<>'dropped'
    ORDER BY captured_at DESC`, groupID, kind, now-windowSec)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var latest *Record
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, "", err
		}
		if latest == nil {
			latest = rec
		}
		if senderID != "" && rec.SenderID == senderID {
			return rec, "sender", nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if latest != nil {
		return latest, "latest", nil
	}
	return nil, "", nil
}

// FindRecentLine 该成员最近一次同类有效记录的线路名。
func (s *SQLRecordStore) FindRecentLine(ctx context.Context, senderID, kind string) (string, error) {
	var name string
	err := s.DB.QueryRowContext(ctx, `
    SELECT line_name_raw FROM wa_record
    WHERE sender_id=? AND kind=? AND status='active' AND line_name_raw<>'' AND COALESCE(JSON_EXTRACT(extracted,'$.line_name'),'')=''
    ORDER BY captured_at DESC LIMIT 1`, senderID, kind).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		// 归一化线路名在 tree/crossing 侧已入库，这里以 line_name_raw/extracted.line_name 兜底再查一次
		err = s.DB.QueryRowContext(ctx, `
      SELECT line_name_raw FROM wa_record
      WHERE sender_id=? AND kind=? AND status='active' AND line_name_raw<>''
      ORDER BY captured_at DESC LIMIT 1`, senderID, kind).Scan(&name)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}

// AddPhoto 挂接辅证照片。
func (s *SQLRecordStore) AddPhoto(ctx context.Context, key string, p Photo) error {
	_, err := s.DB.ExecContext(ctx, `
    INSERT INTO wa_photo (record_key, msg_id, sender_id, captured_at, file_path, bytes) VALUES (?,?,?,?,?,?)`,
		key, p.MsgID, p.SenderID, p.CapturedAt, p.File, p.Bytes)
	return err
}

// RemovePhoto 按照片 id 或图片消息 svr_id 摘除。
func (s *SQLRecordStore) RemovePhoto(ctx context.Context, key, photoKey string) (*Photo, error) {
	row := s.DB.QueryRowContext(ctx, `
    SELECT id, msg_id, sender_id, captured_at, file_path, bytes FROM wa_photo
    WHERE record_key=? AND (CAST(id AS CHAR)=? OR msg_id=?) ORDER BY id DESC LIMIT 1`, key, photoKey, photoKey)
	var p Photo
	if err := row.Scan(&p.ID, &p.MsgID, &p.SenderID, &p.CapturedAt, &p.File, &p.Bytes); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM wa_photo WHERE id=?`, p.ID); err != nil {
		return nil, err
	}
	return &p, nil
}

// PhotosByRecord 列出记录全部辅证照片。
func (s *SQLRecordStore) PhotosByRecord(ctx context.Context, key string) ([]Photo, error) {
	rows, err := s.DB.QueryContext(ctx, `
    SELECT id, msg_id, sender_id, captured_at, file_path, bytes FROM wa_photo WHERE record_key=? ORDER BY id`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Photo
	for rows.Next() {
		var p Photo
		if err := rows.Scan(&p.ID, &p.MsgID, &p.SenderID, &p.CapturedAt, &p.File, &p.Bytes); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CacheImage 缓存图片 CDN 下载参数（幂等 upsert，保留已下载文件与挂接）。
func (s *SQLRecordStore) CacheImage(ctx context.Context, e *ImageEntry) error {
	_, err := s.DB.ExecContext(ctx, `
    INSERT INTO wa_image (svr_id, group_id, sender_id, captured_at, file_no, aes_key, data_len, file_path, record_key)
    VALUES (?,?,?,?,?,?,?,?,?)
    ON DUPLICATE KEY UPDATE group_id=VALUES(group_id), sender_id=VALUES(sender_id), captured_at=VALUES(captured_at),
      file_no=VALUES(file_no), aes_key=VALUES(aes_key), data_len=VALUES(data_len)`,
		e.SvrID, e.GroupID, e.SenderID, e.CapturedAt, e.FileNo, e.AESKey, e.DataLen, e.File, e.RecordKey)
	return err
}

func (s *SQLRecordStore) imageByCond(ctx context.Context, cond string, args ...any) (*ImageEntry, error) {
	row := s.DB.QueryRowContext(ctx, `
    SELECT svr_id, group_id, sender_id, captured_at, file_no, aes_key, data_len, file_path, record_key
    FROM wa_image WHERE `+cond, args...)
	var e ImageEntry
	if err := row.Scan(&e.SvrID, &e.GroupID, &e.SenderID, &e.CapturedAt, &e.FileNo, &e.AESKey, &e.DataLen, &e.File, &e.RecordKey); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &e, nil
}

// Image 按 svr_id 取缓存条目。
func (s *SQLRecordStore) Image(ctx context.Context, svrID string) (*ImageEntry, error) {
	return s.imageByCond(ctx, `svr_id=?`, svrID)
}

// AttachImage 标注图片已挂接记录。
func (s *SQLRecordStore) AttachImage(ctx context.Context, svrID, recordKey string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE wa_image SET record_key=? WHERE svr_id=?`, recordKey, svrID)
	return err
}

// SetImageFile 回写已下载本体路径。
func (s *SQLRecordStore) SetImageFile(ctx context.Context, svrID, file string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE wa_image SET file_path=? WHERE svr_id=?`, file, svrID)
	return err
}

// FindRecallImage 撤回启发式取图：撤回人时间窗内最近一条缓存图片。
func (s *SQLRecordStore) FindRecallImage(ctx context.Context, groupID, senderID string, windowSec int64, now int64) (*ImageEntry, error) {
	if senderID == "" {
		return nil, nil
	}
	return s.imageByCond(ctx, `group_id=? AND sender_id=? AND captured_at>=? ORDER BY captured_at DESC LIMIT 1`,
		groupID, senderID, now-windowSec)
}

// ImagesByRecord 按记录反查全部缓存图片。
func (s *SQLRecordStore) ImagesByRecord(ctx context.Context, recordKey string) ([]ImageEntry, error) {
	rows, err := s.DB.QueryContext(ctx, `
    SELECT svr_id, group_id, sender_id, captured_at, file_no, aes_key, data_len, file_path, record_key
    FROM wa_image WHERE record_key=?`, recordKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImageEntry
	for rows.Next() {
		var e ImageEntry
		if err := rows.Scan(&e.SvrID, &e.GroupID, &e.SenderID, &e.CapturedAt, &e.FileNo, &e.AESKey, &e.DataLen, &e.File, &e.RecordKey); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// RemoveImage 删除缓存条目（文件清理由调用方负责）。
func (s *SQLRecordStore) RemoveImage(ctx context.Context, svrID string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM wa_image WHERE svr_id=?`, svrID)
	return err
}

// SweepImages 清理未挂接且超龄（captured_at < before）的缓存条目，返回被清条目的已下载文件路径。
func (s *SQLRecordStore) SweepImages(ctx context.Context, before int64) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT file_path FROM wa_image WHERE record_key='' AND captured_at<?`, before)
	if err != nil {
		return nil, err
	}
	var files []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			rows.Close()
			return nil, err
		}
		if f != "" {
			files = append(files, f)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM wa_image WHERE record_key='' AND captured_at<?`, before); err != nil {
		return files, err
	}
	return files, nil
}

// QueueItem 补推队列条目。
type QueueItem struct {
	ID       int64
	Kind     string
	Payload  []byte
	Attempts int
}

// PushQueue 失败补推技术队列（MySQL 实现 SQLPushQueue；测试可注入）。
type PushQueue interface {
	Enqueue(ctx context.Context, kind string, payload []byte) error
	// Due 取到期待补推条目（status=pending 且 next_retry_at<=now）。
	Due(ctx context.Context, now time.Time, limit int) ([]QueueItem, error)
	// Settle 补推结果：ok → done；失败 → attempts+1、按 nextRetry 续期（到期上限后由调用方置 dead）。
	Settle(ctx context.Context, id int64, ok bool, errMsg string, nextRetry time.Time, dead bool) error
}

// SQLPushQueue MySQL 实现。
type SQLPushQueue struct {
	DB *sql.DB
}

var _ PushQueue = (*SQLPushQueue)(nil)

// Enqueue 入队（崩溃安全）。
func (q *SQLPushQueue) Enqueue(ctx context.Context, kind string, payload []byte) error {
	_, err := q.DB.ExecContext(ctx, `
    INSERT INTO wa_push_queue (kind, payload, status, attempts, next_retry_at, last_error)
    VALUES (?, ?, 'pending', 0, ?, '')`, kind, string(payload), time.Now())
	return err
}

// Due 到期条目（按 id 升序）。
func (q *SQLPushQueue) Due(ctx context.Context, now time.Time, limit int) ([]QueueItem, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := q.DB.QueryContext(ctx, `
    SELECT id, kind, payload, attempts FROM wa_push_queue
    WHERE status='pending' AND next_retry_at<=? ORDER BY id LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueueItem
	for rows.Next() {
		var it QueueItem
		var payload string
		if err := rows.Scan(&it.ID, &it.Kind, &payload, &it.Attempts); err != nil {
			return nil, err
		}
		it.Payload = []byte(payload)
		out = append(out, it)
	}
	return out, rows.Err()
}

// Settle 结算补推结果。
func (q *SQLPushQueue) Settle(ctx context.Context, id int64, ok bool, errMsg string, nextRetry time.Time, dead bool) error {
	if ok {
		_, err := q.DB.ExecContext(ctx, `UPDATE wa_push_queue SET status='done', last_error='' WHERE id=?`, id)
		return err
	}
	if dead {
		_, err := q.DB.ExecContext(ctx,
			`UPDATE wa_push_queue SET status='dead', attempts=attempts+1, last_error=?, next_retry_at=? WHERE id=?`,
			truncate(errMsg, 500), nextRetry, id)
		return err
	}
	_, err := q.DB.ExecContext(ctx,
		`UPDATE wa_push_queue SET attempts=attempts+1, last_error=?, next_retry_at=? WHERE id=?`,
		truncate(errMsg, 500), nextRetry, id)
	return err
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// SQLChatArchiver 未消费聊天归档默认实现（写 MySQL）。
type SQLChatArchiver struct {
	DB *sql.DB
}

var _ ChatArchiver = (*SQLChatArchiver)(nil)

// ArchiveMessage 登记未消费文本消息（同日同群同消息去重）。
func (a *SQLChatArchiver) ArchiveMessage(ctx context.Context, rec ChatMessage) error {
	_, err := a.DB.ExecContext(ctx, `
    INSERT INTO wa_chat_msg (day, group_id, group_name, msg_svr_id, sender_id, sender_name, content, msg_time)
    VALUES (?,?,?,?,?,?,?,?)
    ON DUPLICATE KEY UPDATE content=VALUES(content)`,
		rec.Day, rec.GroupID, rec.GroupName, rec.SvrID, rec.SenderID, rec.SenderName, rec.Content, rec.Time)
	return err
}

// ArchiveDocument 登记未消费文档类文件（同日同群同消息去重）。
func (a *SQLChatArchiver) ArchiveDocument(ctx context.Context, rec ChatDocument) error {
	_, err := a.DB.ExecContext(ctx, `
    INSERT INTO wa_chat_doc (day, group_id, group_name, msg_svr_id, sender_id, sender_name, file_name, unsupported, msg_time)
    VALUES (?,?,?,?,?,?,?,?,?)
    ON DUPLICATE KEY UPDATE file_name=VALUES(file_name), unsupported=VALUES(unsupported)`,
		rec.Day, rec.GroupID, rec.GroupName, rec.SvrID, rec.SenderID, rec.SenderName,
		rec.FileName, boolInt(rec.Unsupported), rec.Time)
	return err
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// beijingDay Unix 秒 → 北京时间自然日 yyyy-MM-dd（日界口径与 crane 日桶一致）。
func beijingDay(unixSec int64) string {
	d := time.Unix(unixSec, 0).UTC().Add(8 * time.Hour)
	return d.Format("2006-01-02")
}

// beijingDayRange 当天北京时间 [start, end) 的 Unix 秒窗口。
func beijingDayRange(day string) (int64, int64, error) {
	t, err := time.ParseInLocation("2006-01-02", day, time.FixedZone("CST", 8*3600))
	if err != nil {
		return 0, 0, fmt.Errorf("非法日期 %s: %w", day, err)
	}
	return t.Unix(), t.Add(24 * time.Hour).Unix(), nil
}
