// 安全日活动记录：JSON 文件存储（对齐 server/src/safeday/store.js，自 SafeDayLogs 原样移植语义）。
// 记录存 {DataDir}/records.json，生成产物 docx 存 {DataDir}/docs/；写盘原子（临时文件 + rename）。
package safeday

import (
	"crypto/rand"
	"encoding/json"
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
type Record struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Date         string     `json:"date"`
	FileName     string     `json:"fileName"`
	Team         string     `json:"team"`
	Status       string     `json:"status"` // processing / done / failed
	SourceCount  int        `json:"sourceCount"`
	Sources      []string   `json:"sources"`
	Form         RecordForm `json:"form"`
	SourceCosKey string     `json:"sourceCosKey,omitempty"`
	Error        string     `json:"error,omitempty"`
	CreatedAt    string     `json:"createdAt"`
}

// RecordPatch 更新补丁：Error 带「是否存在」语义（对齐 Node store.update——
// patch 不带 error 字段即清除 error；带则写入）。
type RecordPatch struct {
	Status       *string
	Error        *string
	SetError     bool // true 时按 Error 写入/清除；false 时清除 error（Node patch.error === undefined 口径）
	SourceCosKey *string
}

// RecordStore JSON 文件记录存储（进程内互斥）。
type RecordStore struct {
	mu       sync.Mutex
	dataDir  string
	records  string // records.json 路径
	defaults string // form-defaults.json 路径
}

// NewRecordStore 构造存储（确保目录存在）。
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

// readAllUnsafe 读全部记录（调用方持锁）。
func (s *RecordStore) readAllUnsafe() []Record {
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
	return arr
}

// writeAllUnsafe 原子写（先写临时文件再 rename，防进程崩溃截断致 readAll 静默回退 []）。
func (s *RecordStore) writeAllUnsafe(records []Record) error {
	if err := os.MkdirAll(s.dataDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", s.records, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.records)
}

// List 按 createdAt 倒序；teamName 传入时只列该班组。
func (s *RecordStore) List(teamName string) []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.readAllUnsafe()
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
	records := s.readAllUnsafe()
	replaced := make([]Record, 0)
	kept := make([]Record, 0, len(records))
	for _, r := range records {
		if r.Date == record.Date && r.Team == record.Team {
			replaced = append(replaced, r)
			continue
		}
		kept = append(kept, r)
	}
	full := record
	full.ID = newRecordID()
	if full.Status == "" {
		full.Status = "processing"
	}
	if full.CreatedAt == "" {
		full.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	kept = append(kept, full)
	if err := s.writeAllUnsafe(kept); err != nil {
		return Record{}, nil, err
	}
	return full, replaced, nil
}

// newRecordID 生成记录 id：毫秒时间戳-4 字节 hex（与 Node `${Date.now()}-${randomBytes(4).toString('hex')}` 同形）。
func newRecordID() string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return strconv.FormatInt(time.Now().UnixMilli(), 10) + "-" + fmt.Sprintf("%x", buf)
}

// Update 按补丁更新记录；记录不存在返回 (zero,false)。
func (s *RecordStore) Update(id string, patch RecordPatch) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := s.readAllUnsafe()
	for i := range records {
		if records[i].ID != id {
			continue
		}
		if patch.Status != nil {
			records[i].Status = *patch.Status
		}
		if patch.SourceCosKey != nil {
			records[i].SourceCosKey = *patch.SourceCosKey
		}
		if patch.SetError {
			records[i].Error = *patch.Error
			if *patch.Error == "" {
				records[i].Error = ""
			}
		} else {
			records[i].Error = ""
		}
		_ = s.writeAllUnsafe(records)
		return records[i], true
	}
	return Record{}, false
}

// Get 按 id 取记录。
func (s *RecordStore) Get(id string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.readAllUnsafe() {
		if r.ID == id {
			return r, true
		}
	}
	return Record{}, false
}

// Remove 删除记录并返回被删记录。
func (s *RecordStore) Remove(id string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := s.readAllUnsafe()
	for i := range records {
		if records[i].ID != id {
			continue
		}
		removed := records[i]
		kept := append(records[:i:i], records[i+1:]...)
		_ = s.writeAllUnsafe(kept)
		return removed, true
	}
	return Record{}, false
}

// BackfillTeam 班组迁移：无 team 字段的旧记录统一回填为指定班组名；返回改动条数。
func (s *RecordStore) BackfillTeam(defaultTeamName string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := s.readAllUnsafe()
	changed := 0
	for i := range records {
		if records[i].Team == "" {
			records[i].Team = defaultTeamName
			changed++
		}
	}
	if changed > 0 {
		_ = s.writeAllUnsafe(records)
	}
	return changed
}

// RenameTeam 班组改名级联：同步改写所有记录的 team 字段；返回改动条数。
func (s *RecordStore) RenameTeam(oldName, newName string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := s.readAllUnsafe()
	count := 0
	for i := range records {
		if records[i].Team == oldName {
			records[i].Team = newName
			count++
		}
	}
	if count > 0 {
		_ = s.writeAllUnsafe(records)
	}
	return count
}

// ===== 生成表单默认值（按班组记忆：上级参加人员 / 记录人；键=班组名） =====

// FormDefaults 按班组记忆的表单默认值。
type FormDefaults struct {
	Superior string `json:"superior,omitempty"`
	Recorder string `json:"recorder,omitempty"`
}

// GetFormDefaults 取某班组的表单默认值（无记录返回空值，调用方自行回落）。
func (s *RecordStore) GetFormDefaults(teamName string) FormDefaults {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.readDefaultsUnsafe()
	return all[teamName]
}

// SaveFormDefaults 写入某班组的表单默认值（浅合并 patch；仅保留非空字符串值）。
func (s *RecordStore) SaveFormDefaults(teamName string, patch FormDefaults) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.readDefaultsUnsafe()
	cur := all[teamName]
	if v := strings.TrimSpace(patch.Superior); v != "" {
		cur.Superior = v
	}
	if v := strings.TrimSpace(patch.Recorder); v != "" {
		cur.Recorder = v
	}
	all[teamName] = cur
	_ = s.writeDefaultsUnsafe(all)
}

func (s *RecordStore) readDefaultsUnsafe() map[string]FormDefaults {
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

func (s *RecordStore) writeDefaultsUnsafe(all map[string]FormDefaults) error {
	if err := os.MkdirAll(s.dataDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.defaults, data, 0o644)
}
