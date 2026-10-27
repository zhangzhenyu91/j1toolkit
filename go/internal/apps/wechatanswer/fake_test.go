// 测试专用假件：内存 RecordStore / PushQueue、可控 Sink/Dify/Sender/Archiver、
// 轻量 database/sql fake（补推队列 SQL 口径），不连外部数据库与网络。
package wechatanswer

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

/* ------------------------- 内存管线状态 ------------------------- */

type memState struct {
	mu      sync.Mutex
	records map[string]*Record
	photos  map[string][]Photo
	images  map[string]*ImageEntry
}

func newMemState() *memState {
	return &memState{records: map[string]*Record{}, photos: map[string][]Photo{}, images: map[string]*ImageEntry{}}
}

var _ RecordStore = (*memState)(nil)

func (s *memState) InsertRecord(_ context.Context, rec *Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *rec
	s.records[rec.Key] = &cp
	return nil
}

func (s *memState) SaveRecord(_ context.Context, rec *Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *rec
	s.records[rec.Key] = &cp
	return nil
}

func (s *memState) DeleteRecord(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, key)
	delete(s.photos, key)
	return nil
}

func (s *memState) GetRecord(_ context.Context, key string) (*Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec, ok := s.records[key]; ok {
		cp := *rec
		return &cp, nil
	}
	return nil, nil
}

func (s *memState) all() []*Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Record, 0, len(s.records))
	for _, rec := range s.records {
		cp := *rec
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CapturedAt < out[j].CapturedAt })
	return out
}

func (s *memState) FindTodayRecord(_ context.Context, groupID, msgID, content string, dayStart, dayEnd int64) (*Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	needle := strings.TrimSpace(content)
	try := func(kind string) *Record {
		var byContent *Record
		for _, rec := range s.records {
			if rec.GroupID != groupID || rec.Kind != kind || rec.Status == "dropped" {
				continue
			}
			if rec.CapturedAt < dayStart || rec.CapturedAt >= dayEnd {
				continue
			}
			if msgID != "" && rec.MsgID == msgID {
				cp := *rec
				return &cp
			}
			if needle != "" {
				c := strings.TrimSpace(rec.Content)
				if c == needle || strings.Contains(needle, c) || strings.Contains(c, needle) {
					byContent = rec
				}
			}
		}
		if byContent != nil {
			cp := *byContent
			return &cp
		}
		return nil
	}
	if rec := try(KindTree); rec != nil {
		return rec, nil
	}
	return try(KindCrossing), nil
}

func (s *memState) FindNearTime(_ context.Context, groupID string, at, windowSec int64) (*Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *Record
	for _, rec := range s.records {
		if rec.GroupID != groupID || rec.Status == "dropped" {
			continue
		}
		if rec.CapturedAt < at-windowSec || rec.CapturedAt > at+windowSec {
			continue
		}
		if best == nil || rec.CapturedAt > best.CapturedAt {
			best = rec
		}
	}
	if best == nil {
		return nil, nil
	}
	cp := *best
	return &cp, nil
}

func (s *memState) FindRecallTarget(_ context.Context, groupID, senderID string, windowSec int64, kind string, now int64) (*Record, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest *Record
	var own *Record
	for _, rec := range s.records {
		if rec.GroupID != groupID || rec.Kind != kind || rec.Status == "dropped" {
			continue
		}
		if now-rec.CapturedAt > windowSec {
			continue
		}
		if latest == nil || rec.CapturedAt > latest.CapturedAt {
			latest = rec
		}
		if senderID != "" && rec.SenderID == senderID {
			if own == nil || rec.CapturedAt > own.CapturedAt {
				own = rec
			}
		}
	}
	if own != nil {
		cp := *own
		return &cp, "sender", nil
	}
	if latest != nil {
		cp := *latest
		return &cp, "latest", nil
	}
	return nil, "", nil
}

func (s *memState) FindRecentLine(_ context.Context, senderID, kind string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *Record
	for _, rec := range s.records {
		if rec.SenderID != senderID || rec.Kind != kind || rec.Status != "active" || rec.LineNameRaw == "" {
			continue
		}
		if best == nil || rec.CapturedAt > best.CapturedAt {
			best = rec
		}
	}
	if best == nil {
		return "", nil
	}
	return best.LineNameRaw, nil
}

func (s *memState) AddPhoto(_ context.Context, key string, p Photo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.ID = int64(len(s.photos[key]) + 1)
	s.photos[key] = append(s.photos[key], p)
	return nil
}

func (s *memState) RemovePhoto(_ context.Context, key, photoKey string) (*Photo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.photos[key]
	for i, p := range list {
		if fmt.Sprint(p.ID) == photoKey || (photoKey != "" && p.MsgID == photoKey) {
			s.photos[key] = append(list[:i], list[i+1:]...)
			removed := p
			return &removed, nil
		}
	}
	return nil, nil
}

func (s *memState) PhotosByRecord(_ context.Context, key string) ([]Photo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Photo(nil), s.photos[key]...), nil
}

func (s *memState) CacheImage(_ context.Context, e *ImageEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.images[e.SvrID]; ok {
		e.File = old.File
		e.RecordKey = old.RecordKey
	}
	cp := *e
	s.images[e.SvrID] = &cp
	return nil
}

func (s *memState) Image(_ context.Context, svrID string) (*ImageEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.images[svrID]; ok {
		cp := *e
		return &cp, nil
	}
	return nil, nil
}

func (s *memState) SetImageFile(_ context.Context, svrID, file string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.images[svrID]; ok {
		e.File = file
	}
	return nil
}

func (s *memState) AttachImage(_ context.Context, svrID, recordKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.images[svrID]; ok {
		e.RecordKey = recordKey
	}
	return nil
}

func (s *memState) FindRecallImage(_ context.Context, groupID, senderID string, windowSec int64, now int64) (*ImageEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *ImageEntry
	for _, e := range s.images {
		if e.GroupID != groupID || e.SenderID != senderID || now-e.CapturedAt > windowSec {
			continue
		}
		if best == nil || e.CapturedAt > best.CapturedAt {
			best = e
		}
	}
	if best == nil {
		return nil, nil
	}
	cp := *best
	return &cp, nil
}

func (s *memState) ImagesByRecord(_ context.Context, recordKey string) ([]ImageEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ImageEntry
	for _, e := range s.images {
		if e.RecordKey == recordKey {
			out = append(out, *e)
		}
	}
	return out, nil
}

func (s *memState) RemoveImage(_ context.Context, svrID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.images, svrID)
	return nil
}

func (s *memState) SweepImages(_ context.Context, before int64) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var files []string
	for id, e := range s.images {
		if e.RecordKey == "" && e.CapturedAt < before {
			if e.File != "" {
				files = append(files, e.File)
			}
			delete(s.images, id)
		}
	}
	return files, nil
}

/* ------------------------- 内存补推队列 ------------------------- */

type memQueue struct {
	mu      sync.Mutex
	items   []QueueItem
	settled map[int64]queueSettle
	nextID  int64
}

type queueSettle struct {
	ok   bool
	dead bool
	err  string
	next time.Time
}

func newMemQueue() *memQueue {
	return &memQueue{settled: map[int64]queueSettle{}}
}

var _ PushQueue = (*memQueue)(nil)

func (q *memQueue) Enqueue(_ context.Context, kind string, payload []byte) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.nextID++
	q.items = append(q.items, QueueItem{ID: q.nextID, Kind: kind, Payload: append([]byte(nil), payload...)})
	return nil
}

func (q *memQueue) Due(_ context.Context, _ time.Time, limit int) ([]QueueItem, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := []QueueItem{}
	for _, it := range q.items {
		st, settled := q.settled[it.ID]
		if settled && st.ok {
			continue
		}
		if settled && st.dead {
			continue
		}
		if settled && st.next.After(time.Now().Add(time.Hour)) {
			continue // 测试内不模拟到期推进，next 很远视为未到期
		}
		out = append(out, it)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (q *memQueue) Settle(_ context.Context, id int64, ok bool, errMsg string, nextRetry time.Time, dead bool) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	prev := q.settled[id]
	if !ok {
		prev.ok = false
	}
	q.settled[id] = queueSettle{ok: ok, dead: dead, err: errMsg, next: nextRetry}
	if !ok {
		// attempts 累计体现在 items 上（SQL 版在库内累计）
		for i := range q.items {
			if q.items[i].ID == id {
				q.items[i].Attempts++
			}
		}
	}
	return nil
}

// pending 返回未完成条目数（测试断言用）。
func (q *memQueue) pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, it := range q.items {
		st := q.settled[it.ID]
		if !st.ok && !st.dead {
			n++
		}
	}
	return n
}

/* ------------------------- 其余假件 ------------------------- */

// fakeSink 可控 CaptureSink：记录全部载荷，按脚本返回错误/状态码。
type fakeSink struct {
	mu      sync.Mutex
	calls   []sinkCall
	results []sinkResult // 逐次消费；耗尽后默认 200
	sticky  int          // 非 0 时每次固定返回该状态码
}

type sinkCall struct {
	Kind    string
	Payload []byte
}

type sinkResult struct {
	status int
	body   []byte
	err    error
}

func (s *fakeSink) Process(_ context.Context, kind string, payload []byte) (int, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, sinkCall{Kind: kind, Payload: append([]byte(nil), payload...)})
	if s.sticky != 0 {
		return s.sticky, []byte(`{"ok":false}`), nil
	}
	if len(s.results) > 0 {
		r := s.results[0]
		s.results = s.results[1:]
		if r.status == 0 {
			r.status = 500
		}
		return r.status, r.body, r.err
	}
	return 200, []byte(`{"ok":true}`), nil
}

func (s *fakeSink) failNext(status int, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results = append(s.results, sinkResult{status: status, body: []byte(msg), err: nil})
}

func (s *fakeSink) errNext(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results = append(s.results, sinkResult{status: 0, err: err})
}

func (s *fakeSink) payloads() []sinkCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sinkCall(nil), s.calls...)
}

// fakeDify 按 API Key 返回预置输出；记录调用。
type fakeDify struct {
	mu      sync.Mutex
	outputs map[string]map[string]any // apiKey → outputs
	inputs  []map[string]string
	err     error
}

func (d *fakeDify) RunWorkflow(_ context.Context, apiKey string, inputs map[string]string) (map[string]any, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.inputs = append(d.inputs, map[string]string{"key": apiKey, "text": inputs["text"], "correction_text": inputs["correction_text"]})
	if d.err != nil {
		return nil, d.err
	}
	out := d.outputs[apiKey]
	if out == nil {
		return map[string]any{"is_tree_hazard": "true"}, nil
	}
	return out, nil
}

// fakeSender 记录群内回执。
type fakeSender struct {
	mu     sync.Mutex
	texts  []string
	quotes []string
	files  []sentFile
}

func (s *fakeSender) SendText(_ context.Context, _ string, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.texts = append(s.texts, text)
	return nil
}

func (s *fakeSender) SendQuote(_ context.Context, _ string, text string, _ QuoteRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quotes = append(s.quotes, text)
	return nil
}

// fakeArchiver 记录未消费归档。
type fakeArchiver struct {
	mu   sync.Mutex
	msgs []ChatMessage
	docs []ChatDocument
}

func (a *fakeArchiver) ArchiveMessage(_ context.Context, rec ChatMessage) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.msgs = append(a.msgs, rec)
	return nil
}

func (a *fakeArchiver) ArchiveDocument(_ context.Context, rec ChatDocument) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.docs = append(a.docs, rec)
	return nil
}

// staticNames 固定成员名。
type staticNames struct {
	names map[string]string // wxid → 显示名
}

func (n staticNames) DisplayName(_ context.Context, _, wxid string) (string, error) {
	if name, ok := n.names[wxid]; ok && name != "" {
		return name, nil
	}
	return wxid, nil
}

func (n staticNames) WxidByName(_ context.Context, _, name string) (string, error) {
	for wxid, display := range n.names {
		if display == name {
			return wxid, nil
		}
	}
	return "", nil
}

// staticRules 静态规则来源。
type staticRules struct {
	byGroup map[string][]Rule
}

func (r staticRules) RulesForGroup(_ context.Context, groupID string) ([]Rule, error) {
	return r.byGroup[groupID], nil
}

// staticImages 固定图片字节。
type staticImages struct {
	data map[string][]byte // fileNo → 图片字节
	err  error
}

func (s staticImages) DownloadImage(_ context.Context, fileNo, _ string) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.data[fileNo], nil
}

// fakeCfg 静态运行配置。
type fakeCfg struct {
	values map[string]string
}

func (c fakeCfg) Get(_ context.Context, key string) (string, error) {
	if v, ok := c.values[key]; ok {
		return v, nil
	}
	return "", sql.ErrNoRows
}

/* ------------------------- 轻量 fake database/sql ------------------------- */

type fakeRule struct {
	match string
	cols  []string
	rows  [][]driver.Value
	err   error
}

type fakeExec struct {
	query string
	args  []driver.Value
}

type fakeDB struct {
	mu      sync.Mutex
	rules   []fakeRule
	queries []string
	execs   []fakeExec
}

func (f *fakeDB) on(match string, cols []string, rows [][]driver.Value) *fakeDB {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, fakeRule{match: match, cols: cols, rows: rows})
	return f
}

func (f *fakeDB) onErr(match string, err error) *fakeDB {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, fakeRule{match: match, err: err})
	return f
}

func (f *fakeDB) execCount(substr string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, e := range f.execs {
		if strings.Contains(e.query, substr) {
			n++
		}
	}
	return n
}

func (f *fakeDB) lastExec(substr string) (fakeExec, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.execs) - 1; i >= 0; i-- {
		if strings.Contains(f.execs[i].query, substr) {
			return f.execs[i], true
		}
	}
	return fakeExec{}, false
}

func (f *fakeDB) find(query string) *fakeRule {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.rules {
		if strings.Contains(query, f.rules[i].match) {
			return &f.rules[i]
		}
	}
	return nil
}

var fakeSeq atomic.Int64

func openFake(t interface{ Fatalf(string, ...any) }) (*sql.DB, *fakeDB) {
	f := &fakeDB{}
	name := fmt.Sprintf("wechatanswer-fake-%d", fakeSeq.Add(1))
	sql.Register(name, &fakeDriver{db: f})
	db, err := sql.Open(name, "fake")
	if err != nil {
		t.Fatalf("打开 fake db 失败: %v", err)
	}
	return db, f
}

type fakeDriver struct{ db *fakeDB }

func (d *fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{db: d.db}, nil }

type fakeConn struct{ db *fakeDB }

func (c *fakeConn) Prepare(q string) (driver.Stmt, error) { return &fakeStmt{db: c.db, query: q}, nil }
func (c *fakeConn) Close() error                          { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)             { return fakeTx{}, nil }

func (c *fakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return fakeTx{}, nil
}

func (c *fakeConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	return c.db.queryRows(q)
}

func (c *fakeConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	vals := make([]driver.Value, 0, len(args))
	for _, a := range args {
		vals = append(vals, a.Value)
	}
	return c.db.exec(q, vals)
}

type fakeTx struct{}

func (fakeTx) Commit() error   { return nil }
func (fakeTx) Rollback() error { return nil }

type fakeStmt struct {
	db    *fakeDB
	query string
}

func (s *fakeStmt) Close() error                                    { return nil }
func (s *fakeStmt) NumInput() int                                   { return -1 }
func (s *fakeStmt) Exec(args []driver.Value) (driver.Result, error) { return s.db.exec(s.query, args) }
func (s *fakeStmt) Query(args []driver.Value) (driver.Rows, error)  { return s.db.queryRows(s.query) }

func (s *fakeStmt) ExecContext(_ context.Context, _ []driver.NamedValue) (driver.Result, error) {
	return s.db.exec(s.query, nil)
}

func (s *fakeStmt) QueryContext(_ context.Context, _ []driver.NamedValue) (driver.Rows, error) {
	return s.db.queryRows(s.query)
}

func (f *fakeDB) queryRows(query string) (driver.Rows, error) {
	f.mu.Lock()
	f.queries = append(f.queries, query)
	f.mu.Unlock()
	rule := f.find(query)
	if rule == nil {
		return &fakeRows{}, nil
	}
	if rule.err != nil {
		return nil, rule.err
	}
	return &fakeRows{cols: rule.cols, rows: rule.rows}, nil
}

func (f *fakeDB) exec(query string, args []driver.Value) (driver.Result, error) {
	f.mu.Lock()
	f.execs = append(f.execs, fakeExec{query: query, args: args})
	f.mu.Unlock()
	rule := f.find(query)
	if rule != nil && rule.err != nil {
		return nil, rule.err
	}
	return fakeResult(fakeSeq.Add(1)), nil
}

type fakeResult int64

func (r fakeResult) LastInsertId() (int64, error) { return int64(r), nil }
func (r fakeResult) RowsAffected() (int64, error) { return 1, nil }

type fakeRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }

func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

/* ------------------------- WF3/WF4 假件 ------------------------- */

// sentFile 已发送群文件。
type sentFile struct {
	GroupID  string
	FileName string
	Data     []byte
}

// SendFile 记录群文件发送（FileSender）。
func (s *fakeSender) SendFile(_ context.Context, groupID string, data []byte, fileName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files = append(s.files, sentFile{GroupID: groupID, FileName: fileName, Data: append([]byte(nil), data...)})
	return nil
}

func (s *fakeSender) sentFiles() []sentFile {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sentFile(nil), s.files...)
}

// fakeAsk 群内提问假出口。
type fakeAsk struct {
	mu   sync.Mutex
	ans  string
	err  error
	reqs []AskRequest
}

func (a *fakeAsk) IngestChat(_ context.Context, req AskRequest) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reqs = append(a.reqs, req)
	if a.err != nil {
		return "", a.err
	}
	return a.ans, nil
}

// fakeChatlog 聊天归档假出口。
type fakeChatlog struct {
	mu    sync.Mutex
	items []ChatlogItem
	err   error
}

func (c *fakeChatlog) Ingest(_ context.Context, item ChatlogItem) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = append(c.items, item)
	return c.err
}

// fakeFiles 媒体下载假件（按 attach_id/msg_id 返回预置字节）。
type fakeFiles struct {
	data map[string][]byte
	err  error
}

func (f fakeFiles) DownloadMedia(_ context.Context, e MediaEntry) ([]byte, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	if buf, ok := f.data[e.AttachID]; ok {
		return buf, "bin", nil
	}
	if buf, ok := f.data[e.MsgID]; ok {
		return buf, "mp4", nil
	}
	return nil, "", errors.New("媒体不存在")
}

// fakeAppLog 记录应用日志（AppLogger 假件）。
type fakeAppLog struct {
	mu   sync.Mutex
	logs []string
}

func (l *fakeAppLog) Record(_ context.Context, level, category, event, messageCN, raw string, _ map[string]any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logs = append(l.logs, level+"/"+event+" "+messageCN)
	return nil
}

func (l *fakeAppLog) Info(ctx context.Context, category, event, messageCN, raw string, fields map[string]any) error {
	return l.Record(ctx, "info", category, event, messageCN, raw, fields)
}

func (l *fakeAppLog) Warn(ctx context.Context, category, event, messageCN, raw string, fields map[string]any) error {
	return l.Record(ctx, "warn", category, event, messageCN, raw, fields)
}

func (l *fakeAppLog) Error(ctx context.Context, category, event, messageCN, raw string, fields map[string]any) error {
	return l.Record(ctx, "error", category, event, messageCN, raw, fields)
}

func (l *fakeAppLog) events() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.logs...)
}
