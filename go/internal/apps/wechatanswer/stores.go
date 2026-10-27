// 幂等事件 / 媒体下载参数缓存 / 撤回暂存 / 聊天日汇总态 存储抽象（MySQL 实现 + 内存实现）。
// 处理中撤回/更正的可靠幂等依赖 wa_event 唯一认领；媒体缓存只存下载参数不存二进制。
package wechatanswer

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
)

/* ------------------------- 媒体下载参数缓存 ------------------------- */

// MediaEntry 媒体（文件/视频/语音）下载参数缓存项（wa_media 一行；3 天过期清理）。
type MediaEntry struct {
	SvrID        string
	GroupID      string
	SenderID     string
	CapturedAt   int64
	Kind         string // file | video | voice
	FileName     string
	AppID        string
	AttachID     string
	DataLen      int64
	MsgID        string
	NewMsgID     string
	MasterBufID  string
	Length       int64
	FromUserName string
}

// MediaStore 媒体下载参数缓存（SQLMediaStore / memMediaStore）。
type MediaStore interface {
	// PutMedia 按 svr_id 幂等写入（已有条目保留下载参数以外的挂接状态）。
	PutMedia(ctx context.Context, e *MediaEntry) error
	// Media 按消息 svr_id 查缓存（无则 nil）。
	Media(ctx context.Context, svrID string) (*MediaEntry, error)
	// SweepMedia 清理 captured_at < before 的过期条目。
	SweepMedia(ctx context.Context, before int64) error
}

// SQLMediaStore MySQL 实现（wa_media）。
type SQLMediaStore struct {
	DB *sql.DB
}

var _ MediaStore = (*SQLMediaStore)(nil)

func (s *SQLMediaStore) PutMedia(ctx context.Context, e *MediaEntry) error {
	_, err := s.DB.ExecContext(ctx, `
    INSERT INTO wa_media (svr_id, group_id, sender_id, captured_at, kind, file_name, app_id, attach_id,
                          data_len, msg_id, new_msg_id, master_buf_id, length, from_user_name)
    VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
    ON DUPLICATE KEY UPDATE
      group_id=VALUES(group_id), sender_id=VALUES(sender_id), captured_at=VALUES(captured_at),
      kind=VALUES(kind), file_name=VALUES(file_name), app_id=VALUES(app_id), attach_id=VALUES(attach_id),
      data_len=VALUES(data_len), msg_id=VALUES(msg_id), new_msg_id=VALUES(new_msg_id),
      master_buf_id=VALUES(master_buf_id), length=VALUES(length), from_user_name=VALUES(from_user_name)`,
		e.SvrID, e.GroupID, e.SenderID, e.CapturedAt, e.Kind, e.FileName, e.AppID, e.AttachID,
		e.DataLen, e.MsgID, e.NewMsgID, e.MasterBufID, e.Length, e.FromUserName)
	return err
}

func (s *SQLMediaStore) Media(ctx context.Context, svrID string) (*MediaEntry, error) {
	var e MediaEntry
	err := s.DB.QueryRowContext(ctx, `
    SELECT svr_id, group_id, sender_id, captured_at, kind, file_name, app_id, attach_id,
           data_len, msg_id, new_msg_id, master_buf_id, length, from_user_name
    FROM wa_media WHERE svr_id=?`, svrID).
		Scan(&e.SvrID, &e.GroupID, &e.SenderID, &e.CapturedAt, &e.Kind, &e.FileName, &e.AppID, &e.AttachID,
			&e.DataLen, &e.MsgID, &e.NewMsgID, &e.MasterBufID, &e.Length, &e.FromUserName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *SQLMediaStore) SweepMedia(ctx context.Context, before int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM wa_media WHERE captured_at < ?`, before)
	return err
}

// memMediaStore 内存实现（测试/无 DB 回退）。
type memMediaStore struct {
	mu    sync.Mutex
	items map[string]*MediaEntry
}

func newMemMediaStore() *memMediaStore {
	return &memMediaStore{items: map[string]*MediaEntry{}}
}

var _ MediaStore = (*memMediaStore)(nil)

func (s *memMediaStore) PutMedia(_ context.Context, e *MediaEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = map[string]*MediaEntry{}
	}
	cp := *e
	s.items[e.SvrID] = &cp
	return nil
}

func (s *memMediaStore) Media(_ context.Context, svrID string) (*MediaEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.items[svrID]; ok {
		cp := *e
		return &cp, nil
	}
	return nil, nil
}

func (s *memMediaStore) SweepMedia(_ context.Context, before int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.items {
		if e.CapturedAt < before {
			delete(s.items, k)
		}
	}
	return nil
}

/* ------------------------- 消息幂等事件 ------------------------- */

// EventLog 消息幂等登记（处理中撤回/更正与重复投递的可靠幂等基础）。
type EventLog interface {
	// ClaimEvent 幂等认领：true=首次认领可处理；false=重复投递应跳过。
	ClaimEvent(ctx context.Context, msgKey, groupID, eventKind string) (bool, error)
	// SweepEvents 清理 created_at < before 的登记（默认保留 30 天）。
	SweepEvents(ctx context.Context, before int64) error
}

// SQLEventLog MySQL 实现（wa_event）。
type SQLEventLog struct {
	DB *sql.DB
}

var _ EventLog = (*SQLEventLog)(nil)

func (s *SQLEventLog) ClaimEvent(ctx context.Context, msgKey, groupID, eventKind string) (bool, error) {
	res, err := s.DB.ExecContext(ctx,
		`INSERT IGNORE INTO wa_event (msg_key, group_id, event_kind) VALUES (?,?,?)`,
		msgKey, groupID, eventKind)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (s *SQLEventLog) SweepEvents(ctx context.Context, before int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM wa_event WHERE created_at < ?`, before)
	return err
}

// memEventLog 内存实现。
type memEventLog struct {
	mu   sync.Mutex
	seen map[string]bool
}

func newMemEventLog() *memEventLog {
	return &memEventLog{seen: map[string]bool{}}
}

var _ EventLog = (*memEventLog)(nil)

func (s *memEventLog) ClaimEvent(_ context.Context, msgKey, _, _ string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen[msgKey] {
		return false, nil
	}
	s.seen[msgKey] = true
	return true, nil
}

func (s *memEventLog) SweepEvents(context.Context, int64) error { return nil }

/* ------------------------- 撤回暂存（可靠补判） ------------------------- */

// RecallLog 撤回暂存：撤回先于记录落库时持久化暂存、记录出现时补判（跨重启可靠）。
type RecallLog interface {
	StashRecall(ctx context.Context, groupID, senderID string, at int64) error
	// PopRecall 记录落库时补判：同发送者、撤回时间 ≥ 报送时间且在窗口内命中即消费该暂存。
	PopRecall(ctx context.Context, groupID, senderID string, capturedAt, windowSec int64) (bool, error)
	// SweepRecalls 清理 at < before 的过期暂存。
	SweepRecalls(ctx context.Context, before int64) error
}

// SQLRecallLog MySQL 实现（wa_recall）。
type SQLRecallLog struct {
	DB *sql.DB
}

var _ RecallLog = (*SQLRecallLog)(nil)

func (s *SQLRecallLog) StashRecall(ctx context.Context, groupID, senderID string, at int64) error {
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO wa_recall (group_id, sender_id, at) VALUES (?,?,?)`, groupID, senderID, at)
	return err
}

func (s *SQLRecallLog) PopRecall(ctx context.Context, groupID, senderID string, capturedAt, windowSec int64) (bool, error) {
	if senderID == "" {
		return false, nil
	}
	res, err := s.DB.ExecContext(ctx, `
    DELETE FROM wa_recall
    WHERE id = (
      SELECT id FROM (
        SELECT id FROM wa_recall
        WHERE group_id=? AND sender_id=? AND at >= ? AND at - ? <= ?
        ORDER BY at LIMIT 1
      ) t
    )`, groupID, senderID, capturedAt, capturedAt, windowSec)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *SQLRecallLog) SweepRecalls(ctx context.Context, before int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM wa_recall WHERE at < ?`, before)
	return err
}

// memRecallLog 内存实现（原有暂存语义）。
type memRecallLog struct {
	mu   sync.Mutex
	list []memRecall
}

type memRecall struct {
	GroupID  string
	SenderID string
	At       int64
}

func newMemRecallLog() *memRecallLog {
	return &memRecallLog{}
}

var _ RecallLog = (*memRecallLog)(nil)

func (s *memRecallLog) StashRecall(_ context.Context, groupID, senderID string, at int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := make([]memRecall, 0, 4)
	for _, item := range s.list {
		if at-item.At <= 86400 {
			kept = append(kept, item)
		}
	}
	kept = append(kept, memRecall{GroupID: groupID, SenderID: senderID, At: at})
	s.list = kept
	return nil
}

func (s *memRecallLog) PopRecall(_ context.Context, groupID, senderID string, capturedAt, windowSec int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.list {
		if item.GroupID == groupID && item.SenderID == senderID &&
			item.At >= capturedAt && item.At-capturedAt <= windowSec {
			s.list = append(s.list[:i], s.list[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (s *memRecallLog) SweepRecalls(_ context.Context, before int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.list[:0]
	for _, item := range s.list {
		if item.At >= before {
			kept = append(kept, item)
		}
	}
	s.list = kept
	return nil
}

/* ------------------------- 聊天日汇总态（WF4） ------------------------- */

// ChatDayGroup 有待归档数据的（日, 群）。
type ChatDayGroup struct {
	Day       string
	GroupID   string
	GroupName string
	Attempts  int
}

// ChatDayStore 聊天日归档态（WF4 每日总结的进度与清理）。
type ChatDayStore interface {
	// PendingDays 返回 day < beforeDay 且仍有数据的（日, 群）。
	PendingDays(ctx context.Context, beforeDay string) ([]ChatDayGroup, error)
	// DayMessages 当日归档消息（时序）。
	DayMessages(ctx context.Context, day, groupID string) ([]ChatMessage, error)
	// DayDocuments 当日归档文档（时序）。
	DayDocuments(ctx context.Context, day, groupID string) ([]ChatDocument, error)
	// FinishDay 记录归档结果：ok → 清理当日数据；失败累计 attempts，≥3 丢弃（discarded=true）。
	FinishDay(ctx context.Context, day, groupID string, ok bool, errMsg string) (attempts int, discarded bool, err error)
	// DiscardDay 无消费者时主动丢弃当日数据。
	DiscardDay(ctx context.Context, day, groupID string) error
}

// SQLChatDayStore MySQL 实现（wa_chat_msg / wa_chat_doc / wa_chat_day）。
type SQLChatDayStore struct {
	DB *sql.DB
}

var _ ChatDayStore = (*SQLChatDayStore)(nil)

func (s *SQLChatDayStore) PendingDays(ctx context.Context, beforeDay string) ([]ChatDayGroup, error) {
	rows, err := s.DB.QueryContext(ctx, `
    SELECT t.day, t.group_id, MAX(t.group_name), COALESCE(MAX(d.attempts), 0)
    FROM (
      SELECT day, group_id, group_name FROM wa_chat_msg
      UNION ALL
      SELECT day, group_id, group_name FROM wa_chat_doc
    ) t
    LEFT JOIN wa_chat_day d ON d.day = t.day AND d.group_id = t.group_id
    WHERE t.day < ?
    GROUP BY t.day, t.group_id
    ORDER BY t.day, t.group_id`, beforeDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChatDayGroup
	for rows.Next() {
		var g ChatDayGroup
		if err := rows.Scan(&g.Day, &g.GroupID, &g.GroupName, &g.Attempts); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *SQLChatDayStore) DayMessages(ctx context.Context, day, groupID string) ([]ChatMessage, error) {
	rows, err := s.DB.QueryContext(ctx, `
    SELECT day, group_id, group_name, msg_svr_id, sender_id, sender_name, content, msg_time
    FROM wa_chat_msg WHERE day=? AND group_id=? ORDER BY msg_time, id`, day, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChatMessage
	for rows.Next() {
		var rec ChatMessage
		var content sql.NullString
		if err := rows.Scan(&rec.Day, &rec.GroupID, &rec.GroupName, &rec.SvrID, &rec.SenderID, &rec.SenderName, &content, &rec.Time); err != nil {
			return nil, err
		}
		rec.Content = content.String
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *SQLChatDayStore) DayDocuments(ctx context.Context, day, groupID string) ([]ChatDocument, error) {
	rows, err := s.DB.QueryContext(ctx, `
    SELECT day, group_id, group_name, msg_svr_id, sender_id, sender_name, file_name, unsupported, msg_time
    FROM wa_chat_doc WHERE day=? AND group_id=? ORDER BY msg_time, id`, day, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChatDocument
	for rows.Next() {
		var rec ChatDocument
		var unsup int64
		if err := rows.Scan(&rec.Day, &rec.GroupID, &rec.GroupName, &rec.SvrID, &rec.SenderID, &rec.SenderName, &rec.FileName, &unsup, &rec.Time); err != nil {
			return nil, err
		}
		rec.Unsupported = unsup != 0
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *SQLChatDayStore) clearDay(ctx context.Context, day, groupID string) error {
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM wa_chat_msg WHERE day=? AND group_id=?`, day, groupID); err != nil {
		return err
	}
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM wa_chat_doc WHERE day=? AND group_id=?`, day, groupID); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM wa_chat_day WHERE day=? AND group_id=?`, day, groupID)
	return err
}

func (s *SQLChatDayStore) FinishDay(ctx context.Context, day, groupID string, ok bool, errMsg string) (int, bool, error) {
	if ok {
		return 0, false, s.clearDay(ctx, day, groupID)
	}
	if _, err := s.DB.ExecContext(ctx, `
    INSERT INTO wa_chat_day (day, group_id, attempts, last_error) VALUES (?,?,1,?)
    ON DUPLICATE KEY UPDATE attempts = attempts + 1, last_error = VALUES(last_error)`,
		day, groupID, truncate(errMsg, 500)); err != nil {
		return 0, false, err
	}
	var attempts int
	if err := s.DB.QueryRowContext(ctx,
		`SELECT attempts FROM wa_chat_day WHERE day=? AND group_id=?`, day, groupID).Scan(&attempts); err != nil {
		return 0, false, err
	}
	if attempts >= 3 {
		return attempts, true, s.clearDay(ctx, day, groupID)
	}
	return attempts, false, nil
}

func (s *SQLChatDayStore) DiscardDay(ctx context.Context, day, groupID string) error {
	return s.clearDay(ctx, day, groupID)
}

// memChatDayStore 内存实现（测试/无 DB 回退）。
type memChatDayStore struct {
	mu   sync.Mutex
	msgs map[string][]ChatMessage // day|groupID
	docs map[string][]ChatDocument
	fail map[string]int
}

func newMemChatDayStore() *memChatDayStore {
	return &memChatDayStore{
		msgs: map[string][]ChatMessage{},
		docs: map[string][]ChatDocument{},
		fail: map[string]int{},
	}
}

var _ ChatDayStore = (*memChatDayStore)(nil)

func chatDayKey(day, groupID string) string { return day + "|" + groupID }

func (s *memChatDayStore) PendingDays(_ context.Context, beforeDay string) ([]ChatDayGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]ChatDayGroup{}
	for key, list := range s.msgs {
		if len(list) == 0 {
			continue
		}
		parts := strings.SplitN(key, "|", 2)
		if parts[0] >= beforeDay {
			continue
		}
		g := seen[key]
		g.Day, g.GroupID, g.GroupName = parts[0], parts[1], list[0].GroupName
		g.Attempts = s.fail[key]
		seen[key] = g
	}
	for key, list := range s.docs {
		if len(list) == 0 {
			continue
		}
		parts := strings.SplitN(key, "|", 2)
		if parts[0] >= beforeDay {
			continue
		}
		g := seen[key]
		g.Day, g.GroupID = parts[0], parts[1]
		if g.GroupName == "" {
			g.GroupName = list[0].GroupName
		}
		g.Attempts = s.fail[key]
		seen[key] = g
	}
	out := make([]ChatDayGroup, 0, len(seen))
	for _, g := range seen {
		out = append(out, g)
	}
	return out, nil
}

func (s *memChatDayStore) DayMessages(_ context.Context, day, groupID string) ([]ChatMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ChatMessage(nil), s.msgs[chatDayKey(day, groupID)]...), nil
}

func (s *memChatDayStore) DayDocuments(_ context.Context, day, groupID string) ([]ChatDocument, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ChatDocument(nil), s.docs[chatDayKey(day, groupID)]...), nil
}

func (s *memChatDayStore) FinishDay(_ context.Context, day, groupID string, ok bool, _ string) (int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := chatDayKey(day, groupID)
	if ok {
		delete(s.msgs, key)
		delete(s.docs, key)
		delete(s.fail, key)
		return 0, false, nil
	}
	s.fail[key]++
	attempts := s.fail[key]
	if attempts >= 3 {
		delete(s.msgs, key)
		delete(s.docs, key)
		delete(s.fail, key)
		return attempts, true, nil
	}
	return attempts, false, nil
}

func (s *memChatDayStore) DiscardDay(_ context.Context, day, groupID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := chatDayKey(day, groupID)
	delete(s.msgs, key)
	delete(s.docs, key)
	delete(s.fail, key)
	return nil
}

// addMessage/addDocument 供测试灌入归档数据。
func (s *memChatDayStore) addMessage(rec ChatMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := chatDayKey(rec.Day, rec.GroupID)
	s.msgs[key] = append(s.msgs[key], rec)
}

func (s *memChatDayStore) addDocument(rec ChatDocument) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := chatDayKey(rec.Day, rec.GroupID)
	s.docs[key] = append(s.docs[key], rec)
}
