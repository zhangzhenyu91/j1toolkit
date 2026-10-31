// 安全日活动记录：JSON 文件存储（对齐 server/src/safeday/store.js，自 SafeDayLogs 原样移植语义）。
// 记录存 {DataDir}/records.json，生成产物 docx 存 {DataDir}/docs/；写盘原子（临时文件 + rename）。
// 读路径持 RWMutex 读缓存副本（不重复解析 JSON）；写路径在副本上修改 → 原子写盘 → 换 cache，
// 进程内并发安全，返回值一律深拷贝（空 Sources 归一为 []，不输出 null），调用方改写不影响库内数据。
package safeday

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RecordForm 生成表单字段（回调渲染 docx 时使用）。
type RecordForm struct {
	Host         string `json:"host"`
	Superior     string `json:"superior"`
	Recorder     string `json:"recorder"`
	Attendees    string `json:"attendees"`
	Absentees    string `json:"absentees"`
	AbsentReason string `json:"absentReason"`
}

// Record 一条安全日生成记录。
// Template* 与 Sections 为生成时的模板快照（不含 API Key）：模板随后改名/改章节/删除
// 均不影响在途记录，回调按快照 sha 读取内容寻址 docx 渲染。
type Record struct {
	ID           string            `json:"id"`
	BatchID      string            `json:"batchId,omitempty"` // 生成批次 ID（新记录回调必须同时匹配，旧记录为空）
	Name         string            `json:"name"`
	Date         string            `json:"date"`
	FileName     string            `json:"fileName"`
	Team         string            `json:"team"`
	Status       string            `json:"status"` // processing / done / failed
	SourceCount  int               `json:"sourceCount"`
	Sources      []string          `json:"sources"` // 空值保持 []（不输出 null）
	Form         RecordForm        `json:"form"`
	TemplateID   string            `json:"templateId,omitempty"`
	TemplateName string            `json:"templateName,omitempty"`
	TemplateMode string            `json:"templateMode,omitempty"`
	TemplateSHA  string            `json:"templateSha256,omitempty"`
	Sections     []TemplateSection `json:"sections,omitempty"`
	SourceCosKey string            `json:"sourceCosKey,omitempty"`
	Error        string            `json:"error,omitempty"`
	CreatedAt    string            `json:"createdAt"`
}

// RecordPatch 更新补丁：Error 带「是否存在」语义（对齐 Node store.update——
// patch 不带 error 字段即清除 error；带则写入）。
type RecordPatch struct {
	Status       *string
	Error        *string
	SetError     bool // true 时按 Error 写入/清除；false 时清除 error（Node patch.error === undefined 口径）
	SourceCosKey *string
}

// RecordStore JSON 文件记录存储（进程内 RWMutex 保护缓存副本）。
type RecordStore struct {
	mu       sync.RWMutex
	dataDir  string
	records  string // records.json 路径
	defaults string // form-defaults.json 路径
	cache    []Record
	forms    map[string]FormDefaults
}

// NewRecordStore 构造存储（确保目录存在并加载落盘数据）。
func NewRecordStore(dataDir string) (*RecordStore, error) {
	s := &RecordStore{
		dataDir:  dataDir,
		records:  filepath.Join(dataDir, "records.json"),
		defaults: filepath.Join(dataDir, "form-defaults.json"),
	}
	for _, dir := range []string{dataDir, filepath.Join(dataDir, "docs")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("safeday: 创建数据目录: %w", err)
		}
	}
	s.cache = s.readAllFromDisk()
	s.forms = s.readDefaultsFromDisk()
	return s, nil
}

// DataDir 数据根目录。
func (s *RecordStore) DataDir() string { return s.dataDir }

// DocsDir 产物目录（docs/）。
func (s *RecordStore) DocsDir() string { return filepath.Join(s.dataDir, "docs") }

// RecordFilePath 记录产物路径：优先 docs/{班组}/ 子目录，旧记录回退 docs/ 根目录。
func (s *RecordStore) RecordFilePath(record Record) string {
	base := filepath.Base(record.FileName)
	if record.Team != "" {
		nested := filepath.Join(s.DocsDir(), record.Team, base)
		if info, err := os.Stat(nested); err == nil && !info.IsDir() {
			return nested
		}
	}
	return filepath.Join(s.DocsDir(), base)
}

// cloneRecord 深拷贝记录（Sources/Sections 切片独立；空 Sources 归一为 []，不输出 null）。
func cloneRecord(record Record) Record {
	if len(record.Sources) == 0 {
		record.Sources = []string{}
	} else {
		record.Sources = append([]string(nil), record.Sources...)
	}
	if record.Sections != nil {
		record.Sections = append([]TemplateSection(nil), record.Sections...)
	}
	return record
}

// cloneRecords 深拷贝记录列表。
func cloneRecords(records []Record) []Record {
	out := make([]Record, len(records))
	for i, record := range records {
		out[i] = cloneRecord(record)
	}
	return out
}

// readAllFromDisk 读全部记录（构造期一次性装载；缺失/损坏回退空列表，与旧口径一致）。
func (s *RecordStore) readAllFromDisk() []Record {
	raw, err := os.ReadFile(s.records)
	if err != nil {
		return []Record{}
	}
	var arr []Record
	if err := json.Unmarshal(raw, &arr); err != nil {
		return []Record{}
	}
	if arr == nil {
		return []Record{}
	}
	// 旧记录 sources 可能为 null/缺失，统一归一为 []（对外不再输出 null）
	for i := range arr {
		if arr[i].Sources == nil {
			arr[i].Sources = []string{}
		}
	}
	return arr
}

// writeAll 原子写（先写同目录临时文件再 rename，防进程崩溃截断致读取静默回退空列表）。
func (s *RecordStore) writeAll(records []Record) error {
	if err := os.MkdirAll(s.dataDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dataDir, "records-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := atomicReplaceFile(tmpName, s.records); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// atomicReplaceFile 以临时文件替换目标；Windows 上目标被短暂占用（杀毒/索引器）时
// 有界重试 rename，避免并发写盘偶发 “Access is denied”。
func atomicReplaceFile(tmpName, dst string) error {
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		err = os.Rename(tmpName, dst)
		if err == nil {
			return nil
		}
		if !errors.Is(err, os.ErrPermission) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}
	return err
}

// List 按 createdAt 倒序；teamName 传入时只列该班组。返回深拷贝。
func (s *RecordStore) List(teamName string) []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := cloneRecords(s.cache)
	sort.SliceStable(all, func(i, j int) bool { return all[i].CreatedAt > all[j].CreatedAt })
	if teamName == "" {
		return all
	}
	out := make([]Record, 0, len(all))
	for _, r := range all {
		if r.Team == teamName {
			out = append(out, r)
		}
	}
	return out
}

// Create 同一（班组 + date）只保留最新一条：插入前删除同班组同 date 旧记录；
// 返回 { record, replaced }——replaced 供调用方在新生成失败时清理其孤儿产物。
func (s *RecordStore) Create(record Record) (Record, []Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make([]Record, 0, len(s.cache)+1)
	replaced := make([]Record, 0)
	for _, r := range s.cache {
		if r.Date == record.Date && r.Team == record.Team {
			replaced = append(replaced, cloneRecord(r))
			continue
		}
		next = append(next, cloneRecord(r))
	}
	full := cloneRecord(record)
	full.ID = newRecordID()
	if full.Status == "" {
		full.Status = "processing"
	}
	if full.CreatedAt == "" {
		full.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	next = append(next, full)
	if err := s.writeAll(next); err != nil {
		return Record{}, nil, err
	}
	s.cache = next
	return cloneRecord(full), replaced, nil
}

// newRecordID 生成记录 id：毫秒时间戳-4 字节 hex（与 Node `${Date.now()}-${randomBytes(4).toString('hex')}` 同形）。
func newRecordID() string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return strconv.FormatInt(time.Now().UnixMilli(), 10) + "-" + fmt.Sprintf("%x", buf)
}

// newBatchID 生成生成批次 ID（与记录 id 同形，前缀 b 便于排查）。
func newBatchID() string { return "b" + newRecordID() }

// Update 按补丁更新记录；记录不存在或写盘失败返回 (zero,false)。
func (s *RecordStore) Update(id string, patch RecordPatch) (Record, bool) {
	rec, ok, _ := s.update(id, "", patch)
	return rec, ok
}

// UpdateIfStatus 仅当记录当前状态等于 expected 时更新（清扫与回调并发时的 CAS）；
// 状态不符/记录不存在返回 (zero,false,nil)，写盘失败返回非 nil error。
func (s *RecordStore) UpdateIfStatus(id, expected string, patch RecordPatch) (Record, bool, error) {
	return s.update(id, expected, patch)
}

// update 加锁更新；expected 非空时要求当前状态一致，否则放弃。
func (s *RecordStore) update(id, expected string, patch RecordPatch) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.cache {
		if s.cache[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 || (expected != "" && s.cache[idx].Status != expected) {
		return Record{}, false, nil
	}
	next := cloneRecords(s.cache)
	if patch.Status != nil {
		next[idx].Status = *patch.Status
	}
	if patch.SourceCosKey != nil {
		next[idx].SourceCosKey = *patch.SourceCosKey
	}
	if patch.SetError && patch.Error != nil {
		next[idx].Error = *patch.Error
	} else if !patch.SetError {
		next[idx].Error = ""
	}
	if err := s.writeAll(next); err != nil {
		return Record{}, false, err
	}
	s.cache = next
	return cloneRecord(next[idx]), true, nil
}

// Get 按 id 取记录（返回深拷贝）。
func (s *RecordStore) Get(id string) (Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.cache {
		if r.ID == id {
			return cloneRecord(r), true
		}
	}
	return Record{}, false
}

// Remove 删除记录并返回被删记录（返回深拷贝）。
func (s *RecordStore) Remove(id string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.cache {
		if s.cache[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Record{}, false
	}
	removed := cloneRecord(s.cache[idx])
	next := make([]Record, 0, len(s.cache)-1)
	next = append(next, cloneRecords(s.cache[:idx])...)
	next = append(next, cloneRecords(s.cache[idx+1:])...)
	if err := s.writeAll(next); err != nil {
		return Record{}, false
	}
	s.cache = next
	return removed, true
}

// BackfillTeam 班组迁移：无 team 字段的旧记录统一回填为指定班组名；
// 返回改动条数与写盘错误（写盘失败不得伪装成“无改动”）。
func (s *RecordStore) BackfillTeam(defaultTeamName string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneRecords(s.cache)
	changed := 0
	for i := range next {
		if next[i].Team == "" {
			next[i].Team = defaultTeamName
			changed++
		}
	}
	if changed == 0 {
		return 0, nil
	}
	if err := s.writeAll(next); err != nil {
		return 0, err
	}
	s.cache = next
	return changed, nil
}

// RenameTeam 班组改名级联：同步改写所有记录的 team 字段；返回改动条数。
func (s *RecordStore) RenameTeam(oldName, newName string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneRecords(s.cache)
	count := 0
	for i := range next {
		if next[i].Team == oldName {
			next[i].Team = newName
			count++
		}
	}
	if count == 0 {
		return 0
	}
	if err := s.writeAll(next); err != nil {
		return 0
	}
	s.cache = next
	return count
}

// ===== 生成表单默认值（按班组记忆：上级参加人员 / 记录人 / 模板；键=班组名） =====

// FormDefaults 按班组记忆的表单默认值（TemplateID 记忆上次选择，空值不覆盖）。
type FormDefaults struct {
	Superior   string `json:"superior,omitempty"`
	Recorder   string `json:"recorder,omitempty"`
	TemplateID string `json:"templateId,omitempty"`
}

// GetFormDefaults 取某班组的表单默认值（无记录返回空值，调用方自行回落）。
func (s *RecordStore) GetFormDefaults(teamName string) FormDefaults {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.forms[teamName]
}

// SaveFormDefaults 写入某班组的表单默认值（浅合并 patch；仅保留非空字符串值）。
func (s *RecordStore) SaveFormDefaults(teamName string, patch FormDefaults) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[string]FormDefaults, len(s.forms)+1)
	for k, v := range s.forms {
		next[k] = v
	}
	cur := next[teamName]
	if v := strings.TrimSpace(patch.Superior); v != "" {
		cur.Superior = v
	}
	if v := strings.TrimSpace(patch.Recorder); v != "" {
		cur.Recorder = v
	}
	if v := strings.TrimSpace(patch.TemplateID); v != "" {
		cur.TemplateID = v
	}
	next[teamName] = cur
	if err := s.writeDefaults(next); err != nil {
		return
	}
	s.forms = next
}

// readDefaultsFromDisk 读表单默认值（缺失/损坏回退空映射）。
func (s *RecordStore) readDefaultsFromDisk() map[string]FormDefaults {
	raw, err := os.ReadFile(s.defaults)
	if err != nil {
		return map[string]FormDefaults{}
	}
	var all map[string]FormDefaults
	if err := json.Unmarshal(raw, &all); err != nil || all == nil {
		return map[string]FormDefaults{}
	}
	return all
}

// writeDefaults 原子写表单默认值（临时文件 + rename）。
func (s *RecordStore) writeDefaults(all map[string]FormDefaults) error {
	if err := os.MkdirAll(s.dataDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dataDir, "form-defaults-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := atomicReplaceFile(tmpName, s.defaults); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
