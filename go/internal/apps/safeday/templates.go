// 安全日模板库：用户模板只保存名称、章节与 Dify API Key 密文，统一复用原活动记录底稿。
// 存储布局：{SAFEDAY_DATA_DIR}/templates/index.json；旧 templates/files 不再写入或作为生成前置条件。
// API Key 复用 config.ConfigCipher 以 enc:v1: 密文落盘；对外视图只暴露 hasApiKey，绝不返回明文或密文。
package safeday

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"shade/internal/config"
)

const (
	// TemplateModeStatic 仅用于旧记录兼容；新记录一律为 dynamic 用户模板。
	TemplateModeStatic  = "static"
	TemplateModeDynamic = "dynamic"

	sectionsPlaceholder = "章节区"

	templateIndexVersion   = 1
	maxTemplateRequestSize = 11 << 20
	maxTemplateNameRunes   = 64
	maxSectionTitleRunes   = 64
	maxTemplateSections    = 12
	maxTemplateAPIKeyLen   = 512
	maxTemplates           = 50
)

// baseSections 原底稿六段布局与旧 static 记录回调兼容定义，不是可选模板。
var baseSections = []TemplateSection{
	{Key: "accident_process", Title: "客观还原事故经过"},
	{Key: "cause_analysis", Title: "分层剖析事故原因"},
	{Key: "self_check", Title: "对照自查举一反三"},
	{Key: "rectification_list", Title: "制定整改清单"},
	{Key: "prevention_measures", Title: "讨论防范措施"},
	{Key: "supervision_inspection", Title: "定期督导检查防范措施落实情况和整改完成情况"},
}

var reservedSectionKeys = map[string]bool{
	"date": true, "class": true, "sections": true, "url": true,
	"record_id": true, "batch_id": true,
}

var reSectionKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

var (
	ErrTemplateNotFound      = errors.New("模板不存在")
	ErrTemplateRequired      = errors.New("请先创建安全日记录模板")
	ErrTemplateKeyMissing    = errors.New("模板未配置 Dify API Key")
	ErrTemplateCipherMissing = errors.New("模板存储未配置加密器，无法保存 API Key")
)

type templateValidationError struct{ message string }

func (e templateValidationError) Error() string { return e.message }

func invalidTemplate(format string, args ...any) error {
	return templateValidationError{message: fmt.Sprintf(format, args...)}
}

// TemplateSection key 对应 Dify 输出/callback 字段，title 原样作为文档小节标题。
type TemplateSection struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

// Template 模板管理视图（绝不含 API Key 明文/密文）；SHA256/Size 仅兼容旧索引视图。
type Template struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Mode      string            `json:"mode"`
	Sections  []TemplateSection `json:"sections"`
	SHA256    string            `json:"sha256,omitempty"`
	Size      int64             `json:"size,omitempty"`
	HasAPIKey bool              `json:"hasApiKey"`
	IsDefault bool              `json:"isDefault"`
	CreatedAt string            `json:"createdAt,omitempty"`
	UpdatedAt string            `json:"updatedAt,omitempty"`
}

type TemplateOption struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Mode      string            `json:"mode"`
	Sections  []TemplateSection `json:"sections"`
	IsDefault bool              `json:"isDefault"`
}

type TemplateUpdate struct {
	Name     string
	Sections []TemplateSection
	APIKey   string
}

// TemplateSnapshot 写入记录的模板快照（不含 API Key）；新记录 SHA256 为空。
type TemplateSnapshot struct {
	ID       string
	Name     string
	Mode     string
	SHA256   string
	Sections []TemplateSection
}

// templateRecord 的 SHA256/Size 只为读取旧索引保留，不再用于快照或生成校验。
type templateRecord struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Mode      string            `json:"mode"`
	Sections  []TemplateSection `json:"sections"`
	SHA256    string            `json:"sha256,omitempty"`
	Size      int64             `json:"size,omitempty"`
	APIKeyEnc string            `json:"apiKeyEnc,omitempty"`
	CreatedAt string            `json:"createdAt,omitempty"`
	UpdatedAt string            `json:"updatedAt,omitempty"`
}

type templateIndex struct {
	Version   int              `json:"version"`
	DefaultID string           `json:"defaultId"`
	Templates []templateRecord `json:"templates"`
}

type TemplateStore struct {
	mu        sync.RWMutex
	dir       string
	indexPath string
	cipher    *config.ConfigCipher
	index     templateIndex
}

func NewTemplateStore(dataDir string, cipher *config.ConfigCipher) (*TemplateStore, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return nil, errors.New("safeday: 模板数据目录不能为空")
	}
	dir := filepath.Join(dataDir, "templates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("safeday: 创建模板目录: %w", err)
	}
	s := &TemplateStore{
		dir:       dir,
		indexPath: filepath.Join(dir, "index.json"),
		cipher:    cipher,
	}
	if err := s.loadIndex(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *TemplateStore) loadIndex() error {
	raw, err := os.ReadFile(s.indexPath)
	if errors.Is(err, os.ErrNotExist) {
		s.index = templateIndex{Version: templateIndexVersion}
		return nil
	}
	if err != nil {
		return fmt.Errorf("safeday: 读取模板索引: %w", err)
	}
	var idx templateIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		return fmt.Errorf("safeday: 解析模板索引: %w", err)
	}
	if idx.Version == 0 {
		idx.Version = templateIndexVersion
	}
	s.index = idx
	return nil
}

func (s *TemplateStore) writeIndexLocked() error {
	if s.index.Version == 0 {
		s.index.Version = templateIndexVersion
	}
	data, err := json.MarshalIndent(s.index, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, "index-*.tmp")
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
	if err := atomicReplaceFile(tmpName, s.indexPath); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

func (s *TemplateStore) List() []Template {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Template, 0, len(s.index.Templates))
	for _, rec := range s.index.Templates {
		out = append(out, s.viewLocked(rec))
	}
	return out
}

func (s *TemplateStore) Options() []TemplateOption {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]TemplateOption, 0, len(s.index.Templates))
	for _, rec := range s.index.Templates {
		view := s.viewLocked(rec)
		out = append(out, TemplateOption{ID: view.ID, Name: view.Name, Mode: view.Mode, Sections: view.Sections, IsDefault: view.IsDefault})
	}
	return out
}

func (s *TemplateStore) Get(id string) (Template, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, rec := range s.index.Templates {
		if rec.ID == id {
			return s.viewLocked(rec), true
		}
	}
	return Template{}, false
}

func (s *TemplateStore) DefaultID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.effectiveDefaultLocked()
}

// Create 新建用户模板：名称、章节和独立 Dify API Key，不再接收 docx。
func (s *TemplateStore) Create(name string, sections []TemplateSection, apiKey string) (Template, error) {
	cleanName, err := validateTemplateName(name)
	if err != nil {
		return Template{}, err
	}
	cleanSections, err := normalizeSections(sections)
	if err != nil {
		return Template{}, err
	}
	cleanKey, err := validateTemplateAPIKey(apiKey)
	if err != nil {
		return Template{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.index.Templates) >= maxTemplates {
		return Template{}, invalidTemplate("模板数量已达上限（最多 %d 套）", maxTemplates)
	}
	if s.cipher == nil {
		return Template{}, ErrTemplateCipherMissing
	}
	enc, err := s.cipher.Encrypt(cleanKey)
	if err != nil {
		return Template{}, fmt.Errorf("保存模板 API Key 失败：%w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rec := templateRecord{
		ID: newTemplateID(), Name: cleanName, Mode: TemplateModeDynamic,
		Sections: cleanSections, APIKeyEnc: enc, CreatedAt: now, UpdatedAt: now,
	}
	next := cloneTemplateIndex(s.index)
	next.Templates = append(next.Templates, cloneTemplateRecord(rec))
	next.DefaultID = defaultIDForIndex(next)
	if err := s.writeIndexLockedWith(next); err != nil {
		return Template{}, fmt.Errorf("保存模板索引失败：%w", err)
	}
	s.index = next
	return s.viewLocked(rec), nil
}

func (s *TemplateStore) Update(id string, upd TemplateUpdate) (Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.findLocked(id)
	if idx < 0 {
		return Template{}, ErrTemplateNotFound
	}
	next := cloneTemplateIndex(s.index)
	rec := &next.Templates[idx]
	if strings.TrimSpace(upd.Name) != "" {
		name, err := validateTemplateName(upd.Name)
		if err != nil {
			return Template{}, err
		}
		rec.Name = name
	}
	if upd.Sections != nil {
		sections, err := normalizeSections(upd.Sections)
		if err != nil {
			return Template{}, err
		}
		rec.Sections = sections
	}
	if strings.TrimSpace(upd.APIKey) != "" {
		key, err := validateTemplateAPIKey(upd.APIKey)
		if err != nil {
			return Template{}, err
		}
		if s.cipher == nil {
			return Template{}, ErrTemplateCipherMissing
		}
		enc, err := s.cipher.Encrypt(key)
		if err != nil {
			return Template{}, fmt.Errorf("保存模板 API Key 失败：%w", err)
		}
		rec.APIKeyEnc = enc
	}
	rec.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := s.writeIndexLockedWith(next); err != nil {
		return Template{}, fmt.Errorf("保存模板索引失败：%w", err)
	}
	s.index = next
	return s.viewLocked(*rec), nil
}

func (s *TemplateStore) SetDefault(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findLocked(id) < 0 {
		return ErrTemplateNotFound
	}
	next := cloneTemplateIndex(s.index)
	next.DefaultID = id
	if err := s.writeIndexLockedWith(next); err != nil {
		return fmt.Errorf("保存模板索引失败：%w", err)
	}
	s.index = next
	return nil
}

func (s *TemplateStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.findLocked(id)
	if idx < 0 {
		return ErrTemplateNotFound
	}
	next := cloneTemplateIndex(s.index)
	next.Templates = append(next.Templates[:idx], next.Templates[idx+1:]...)
	next.DefaultID = defaultIDForIndex(next)
	if err := s.writeIndexLockedWith(next); err != nil {
		return fmt.Errorf("保存模板索引失败：%w", err)
	}
	s.index = next
	return nil
}

func (s *TemplateStore) WorkflowAPIKey(id string) (string, error) {
	s.mu.RLock()
	idx := s.findLocked(id)
	var enc string
	if idx >= 0 {
		enc = s.index.Templates[idx].APIKeyEnc
	}
	s.mu.RUnlock()
	if idx < 0 {
		return "", ErrTemplateNotFound
	}
	if enc == "" {
		return "", ErrTemplateKeyMissing
	}
	if s.cipher == nil {
		return "", ErrTemplateCipherMissing
	}
	plain, err := s.cipher.Decrypt(enc)
	if err != nil {
		return "", errors.New("模板 API Key 解密失败，请重新设置")
	}
	return plain, nil
}

// Snapshot 只快照章节配置；旧 docx 是否存在不影响新生成。
func (s *TemplateStore) Snapshot(id string) (TemplateSnapshot, error) {
	s.mu.RLock()
	idx := s.findLocked(id)
	if idx < 0 {
		s.mu.RUnlock()
		return TemplateSnapshot{}, ErrTemplateNotFound
	}
	rec := cloneTemplateRecord(s.index.Templates[idx])
	s.mu.RUnlock()
	return TemplateSnapshot{ID: rec.ID, Name: rec.Name, Mode: rec.Mode, Sections: copySections(rec.Sections)}, nil
}

func (s *TemplateStore) viewLocked(rec templateRecord) Template {
	return Template{
		ID: rec.ID, Name: rec.Name, Mode: rec.Mode, Sections: copySections(rec.Sections),
		SHA256: rec.SHA256, Size: rec.Size, HasAPIKey: rec.APIKeyEnc != "",
		IsDefault: s.effectiveDefaultLocked() == rec.ID && rec.ID != "",
		CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt,
	}
}

func (s *TemplateStore) effectiveDefaultLocked() string {
	return defaultIDForIndex(s.index)
}

func defaultIDForIndex(idx templateIndex) string {
	id := strings.TrimSpace(idx.DefaultID)
	for _, rec := range idx.Templates {
		if rec.ID == id {
			return id
		}
	}
	if len(idx.Templates) > 0 {
		return idx.Templates[0].ID
	}
	return ""
}

func (s *TemplateStore) findLocked(id string) int {
	for i := range s.index.Templates {
		if s.index.Templates[i].ID == id {
			return i
		}
	}
	return -1
}

func (s *TemplateStore) writeIndexLockedWith(next templateIndex) error {
	prev := s.index
	s.index = next
	if err := s.writeIndexLocked(); err != nil {
		s.index = prev
		return err
	}
	return nil
}

func validateTemplateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", invalidTemplate("模板名称不能为空")
	}
	if utf8.RuneCountInString(name) > maxTemplateNameRunes {
		return "", invalidTemplate("模板名称过长（最多 %d 字）", maxTemplateNameRunes)
	}
	return name, nil
}

func normalizeSections(sections []TemplateSection) ([]TemplateSection, error) {
	if len(sections) == 0 {
		return nil, invalidTemplate("模板章节不能为空")
	}
	if len(sections) > maxTemplateSections {
		return nil, invalidTemplate("模板章节过多（最多 %d 段）", maxTemplateSections)
	}
	out := make([]TemplateSection, 0, len(sections))
	seen := make(map[string]bool, len(sections))
	for _, section := range sections {
		key := strings.TrimSpace(section.Key)
		if !reSectionKey.MatchString(key) || reservedSectionKeys[key] {
			return nil, invalidTemplate("章节标识不合法：%s（须小写字母开头，仅字母/数字/下划线，且不得为 date/class/sections/url/record_id/batch_id）", key)
		}
		if seen[key] {
			return nil, invalidTemplate("章节标识重复：%s", key)
		}
		title := strings.TrimSpace(section.Title)
		if title == "" {
			return nil, invalidTemplate("章节标题不能为空：%s", key)
		}
		if utf8.RuneCountInString(title) > maxSectionTitleRunes {
			return nil, invalidTemplate("章节标题过长：%s（最多 %d 字）", key, maxSectionTitleRunes)
		}
		seen[key] = true
		out = append(out, TemplateSection{Key: key, Title: title})
	}
	return out, nil
}

func validateTemplateAPIKey(apiKey string) (string, error) {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return "", invalidTemplate("请填写 Dify API Key")
	}
	if strings.ContainsAny(key, "\r\n") || len(key) > maxTemplateAPIKeyLen {
		return "", invalidTemplate("Dify API Key 格式不正确")
	}
	return key, nil
}

func copySections(sections []TemplateSection) []TemplateSection {
	if sections == nil {
		return []TemplateSection{}
	}
	return append([]TemplateSection(nil), sections...)
}

func cloneTemplateRecord(rec templateRecord) templateRecord {
	rec.Sections = copySections(rec.Sections)
	return rec
}

func cloneTemplateIndex(idx templateIndex) templateIndex {
	out := idx
	out.Templates = make([]templateRecord, len(idx.Templates))
	for i, rec := range idx.Templates {
		out.Templates[i] = cloneTemplateRecord(rec)
	}
	return out
}

func newTemplateID() string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return "tpl-" + strconv.FormatInt(time.Now().UnixMilli(), 10) + "-" + hex.EncodeToString(buf)
}
