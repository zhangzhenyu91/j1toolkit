// 安全日模板库：内置 static 模板 + 自定义 dynamic 模板。
// 存储布局：{SAFEDAY_DATA_DIR}/templates/index.json（元数据与 API Key 密文）+
// templates/files/{sha256}.docx（内容寻址，模板改删/替换均不清理旧文件，保证在途记录可渲染）。
// API Key 复用 config.ConfigCipher 以 enc:v1: 密文落 index.json；对外视图只暴露 hasApiKey，
// 绝不返回明文或密文。内置模板使用全局 DIFY_SAFEDAY_API_KEY（WorkflowAPIKey 对 builtin 返回空）。
package safeday

import (
	"crypto/rand"
	"crypto/sha256"
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

	"shade/internal/assetpath"
	"shade/internal/compat/exceldoc"
	"shade/internal/config"
)

const (
	// BuiltinTemplateID 内置模板 id（静态六段，未传 template_id 的旧端固定使用）。
	BuiltinTemplateID = "builtin"
	// TemplateModeStatic 静态模板（内置六段 {@标题} 占位）。
	TemplateModeStatic = "static"
	// TemplateModeDynamic 自定义动态模板（必须含独占 {@章节区}）。
	TemplateModeDynamic = "dynamic"

	// sectionsPlaceholder 动态模板章节区占位符名。
	sectionsPlaceholder = "章节区"
	// builtinTemplateName 内置模板展示名。
	builtinTemplateName = "内置模板"

	templateIndexVersion = 1
	maxTemplateFileSize  = 10 << 20 // 10MB
	maxTemplateNameRunes = 64
	maxSectionTitleRunes = 64
	maxTemplateSections  = 12 // 1–12 段
	maxTemplateAPIKeyLen = 512
	maxTemplates         = 50 // 自定义模板最多 50 套（不含内置）
)

// builtinSections 内置静态模板六段（key 与 Dify 输出/callback 扁平字段一致）。
var builtinSections = []TemplateSection{
	{Key: "accident_process", Title: "客观还原事故经过"},
	{Key: "cause_analysis", Title: "分层剖析事故原因"},
	{Key: "self_check", Title: "对照自查举一反三"},
	{Key: "rectification_list", Title: "制定整改清单"},
	{Key: "prevention_measures", Title: "讨论防范措施"},
	{Key: "supervision_inspection", Title: "定期督导检查防范措施落实情况和整改完成情况"},
}

// reservedSectionKeys 与回调信封字段冲突的保留 key。
var reservedSectionKeys = map[string]bool{
	"date": true, "class": true, "sections": true, "url": true,
}

// reSectionKey 章节标识：小写字母开头，可含小写字母/数字/下划线，≤32 字符。
var reSectionKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// reSHA256 内容寻址文件名（防路径穿越）。
var reSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// 模板库错误（handler 据此映射状态码；文案绝不包含 API Key）。
var (
	// ErrTemplateNotFound 模板不存在。
	ErrTemplateNotFound = errors.New("模板不存在")
	// ErrTemplateBuiltinReadonly 内置模板只读/不可删除。
	ErrTemplateBuiltinReadonly = errors.New("内置模板不可修改")
	// ErrTemplateKeyMissing 自定义模板未配置 API Key。
	ErrTemplateKeyMissing = errors.New("模板未配置 Dify API Key")
	// ErrTemplateFileMissing 模板 docx 缺失。
	ErrTemplateFileMissing = errors.New("模板文件缺失")
	// ErrTemplateFileInvalid 模板 docx 存在但损坏（sha 不一致/不是合法 docx/结构不符）。
	ErrTemplateFileInvalid = errors.New("模板文件损坏")
	// ErrTemplateCipherMissing 模板库未配置加密器。
	ErrTemplateCipherMissing = errors.New("模板存储未配置加密器，无法保存 API Key")
)

// templateValidationError 参数校验错误（handler 映射 400）。
type templateValidationError struct{ message string }

// Error 错误文案。
func (e templateValidationError) Error() string { return e.message }

// invalidTemplate 构造参数校验错误。
func invalidTemplate(format string, args ...any) error {
	return templateValidationError{message: fmt.Sprintf(format, args...)}
}

// TemplateSection 章节定义：key 对应 Dify 输出/callback 字段，title 为文档小节标题。
type TemplateSection struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

// Template 模板管理视图（绝不含 API Key 明文/密文）。
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

// TemplateOption 生成表单可选模板（普通接口视图）。
type TemplateOption struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Mode      string            `json:"mode"`
	Sections  []TemplateSection `json:"sections"`
	IsDefault bool              `json:"isDefault"`
}

// TemplateUpdate 模板元数据更新（零值字段保持原值）。
type TemplateUpdate struct {
	Name     string
	Sections []TemplateSection
	APIKey   string // 空串保留原 Key
}

// TemplateSnapshot 写入记录的模板快照（不含 API Key）。
type TemplateSnapshot struct {
	ID       string
	Name     string
	Mode     string
	SHA256   string
	Sections []TemplateSection
}

// templateRecord index.json 内的持久化条目。
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

// templateIndex index.json 结构。
type templateIndex struct {
	Version   int              `json:"version"`
	DefaultID string           `json:"defaultId"`
	Templates []templateRecord `json:"templates"`
}

// TemplateStore 模板库（RWMutex 保护，读写返回副本）。
type TemplateStore struct {
	mu        sync.RWMutex
	dir       string
	filesDir  string
	indexPath string
	cipher    *config.ConfigCipher
	index     templateIndex

	// BuiltinPath 内置静态模板 docx 路径（默认按部署布局解析，测试可覆盖）。
	BuiltinPath string
	// SamplePath 动态样例 docx 路径（管理端下载样例用）。
	SamplePath string
}

// NewTemplateStore 构造模板库。dataDir 与 RecordStore 同根（模板落 {dataDir}/templates）；
// cipher 为 nil 时只读（新建/改 Key 明确报错），生产须传 config.NewConfigCipher 产物。
func NewTemplateStore(dataDir string, cipher *config.ConfigCipher) (*TemplateStore, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return nil, errors.New("safeday: 模板数据目录不能为空")
	}
	dir := filepath.Join(dataDir, "templates")
	filesDir := filepath.Join(dir, "files")
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		return nil, fmt.Errorf("safeday: 创建模板目录: %w", err)
	}
	s := &TemplateStore{
		dir:         dir,
		filesDir:    filesDir,
		indexPath:   filepath.Join(dir, "index.json"),
		cipher:      cipher,
		BuiltinPath: ResolveTemplatePath(),
		SamplePath:  assetpath.ResolveFile("safeday/activity-record-dynamic-sample.docx"),
	}
	if err := s.loadIndex(); err != nil {
		return nil, err
	}
	return s, nil
}

// loadIndex 装载 index.json（缺失按初始状态；损坏明确报错，避免静默丢模板）。
func (s *TemplateStore) loadIndex() error {
	raw, err := os.ReadFile(s.indexPath)
	if errors.Is(err, os.ErrNotExist) {
		s.index = templateIndex{Version: templateIndexVersion, DefaultID: BuiltinTemplateID}
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
	if strings.TrimSpace(idx.DefaultID) == "" {
		idx.DefaultID = BuiltinTemplateID
	}
	s.index = idx
	return nil
}

// writeIndexLocked 原子写 index.json（调用方须持写锁）。
func (s *TemplateStore) writeIndexLocked() error {
	if s.index.Version == 0 {
		s.index.Version = templateIndexVersion
	}
	if s.index.DefaultID == "" {
		s.index.DefaultID = BuiltinTemplateID
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

// List 全部模板：内置在前，自定义按创建顺序；返回副本。
func (s *TemplateStore) List() []Template {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Template, 0, len(s.index.Templates)+1)
	out = append(out, s.builtinViewLocked())
	for _, rec := range s.index.Templates {
		out = append(out, s.viewLocked(rec))
	}
	return out
}

// Options 生成表单可选模板（不含管理字段）。
func (s *TemplateStore) Options() []TemplateOption {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]TemplateOption, 0, len(s.index.Templates)+1)
	builtin := s.builtinViewLocked()
	out = append(out, TemplateOption{ID: builtin.ID, Name: builtin.Name, Mode: builtin.Mode, Sections: builtin.Sections, IsDefault: builtin.IsDefault})
	for _, rec := range s.index.Templates {
		view := s.viewLocked(rec)
		out = append(out, TemplateOption{ID: view.ID, Name: view.Name, Mode: view.Mode, Sections: view.Sections, IsDefault: view.IsDefault})
	}
	return out
}

// Get 按 id 取模板视图。
func (s *TemplateStore) Get(id string) (Template, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if id == BuiltinTemplateID {
		return s.builtinViewLocked(), true
	}
	for _, rec := range s.index.Templates {
		if rec.ID == id {
			return s.viewLocked(rec), true
		}
	}
	return Template{}, false
}

// DefaultID 生效默认模板 id（索引缺省/指向已删除模板时回落 builtin）。
func (s *TemplateStore) DefaultID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.effectiveDefaultLocked()
}

// Create 新建自定义动态模板（API Key 必填、docx 必含独占 {@章节区}）。
func (s *TemplateStore) Create(name string, sections []TemplateSection, apiKey string, docx []byte) (Template, error) {
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
	if err := validateTemplateDocx(docx); err != nil {
		return Template{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// 配额在落内容文件前校验（最多 50 套自定义模板）
	if len(s.index.Templates) >= maxTemplates {
		return Template{}, invalidTemplate("模板数量已达上限（最多 %d 套）", maxTemplates)
	}
	if s.cipher == nil {
		return Template{}, ErrTemplateCipherMissing
	}
	sha, err := s.storeDocx(docx)
	if err != nil {
		return Template{}, err
	}
	enc, err := s.cipher.Encrypt(cleanKey)
	if err != nil {
		return Template{}, fmt.Errorf("保存模板 API Key 失败：%w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rec := templateRecord{
		ID: newTemplateID(), Name: cleanName, Mode: TemplateModeDynamic,
		Sections: cleanSections, SHA256: sha, Size: int64(len(docx)),
		APIKeyEnc: enc, CreatedAt: now, UpdatedAt: now,
	}

	next := cloneTemplateIndex(s.index)
	next.Templates = append(next.Templates, cloneTemplateRecord(rec))
	if err := s.writeIndexLockedWith(next); err != nil {
		return Template{}, fmt.Errorf("保存模板索引失败：%w", err)
	}
	s.index = next
	return s.viewLocked(rec), nil
}

// Update 更新名称/章节/API Key（空值保持原值）；内置模板只读。
func (s *TemplateStore) Update(id string, upd TemplateUpdate) (Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == BuiltinTemplateID {
		return Template{}, ErrTemplateBuiltinReadonly
	}
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

// ReplaceDocx 替换模板 docx（旧内容文件保留，在途记录按 sha 继续可渲染）。
func (s *TemplateStore) ReplaceDocx(id string, docx []byte) (Template, error) {
	if id == BuiltinTemplateID {
		return Template{}, ErrTemplateBuiltinReadonly
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 先确认模板存在再落内容文件，未知 id 不产生孤儿文件
	idx := s.findLocked(id)
	if idx < 0 {
		return Template{}, ErrTemplateNotFound
	}
	if err := validateTemplateDocx(docx); err != nil {
		return Template{}, err
	}
	sha, err := s.storeDocx(docx)
	if err != nil {
		return Template{}, err
	}
	next := cloneTemplateIndex(s.index)
	rec := &next.Templates[idx]
	rec.SHA256 = sha
	rec.Size = int64(len(docx))
	rec.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := s.writeIndexLockedWith(next); err != nil {
		return Template{}, fmt.Errorf("保存模板索引失败：%w", err)
	}
	s.index = next
	return s.viewLocked(*rec), nil
}

// SetDefault 设默认模板（builtin 或已存在自定义模板）。
func (s *TemplateStore) SetDefault(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != BuiltinTemplateID && s.findLocked(id) < 0 {
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

// Delete 删除自定义模板配置（内容文件保留，在途记录仍可渲染）；内置模板不可删。
func (s *TemplateStore) Delete(id string) error {
	if id == BuiltinTemplateID {
		return ErrTemplateBuiltinReadonly
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.findLocked(id)
	if idx < 0 {
		return ErrTemplateNotFound
	}
	next := cloneTemplateIndex(s.index)
	next.Templates = append(next.Templates[:idx], next.Templates[idx+1:]...)
	if next.DefaultID == id {
		next.DefaultID = BuiltinTemplateID
	}
	if err := s.writeIndexLockedWith(next); err != nil {
		return fmt.Errorf("保存模板索引失败：%w", err)
	}
	s.index = next
	return nil
}

// Download 取模板当前 docx 与下载名。
func (s *TemplateStore) Download(id string) (string, []byte, error) {
	tpl, ok := s.Get(id)
	if !ok {
		return "", nil, ErrTemplateNotFound
	}
	var data []byte
	if id == BuiltinTemplateID {
		var err error
		data, err = os.ReadFile(s.builtinPath())
		if err != nil {
			return "", nil, fmt.Errorf("%w：内置模板文件缺失（%v）", ErrTemplateFileMissing, err)
		}
	} else {
		var err error
		data, err = s.readDocxBySHA(tpl.SHA256)
		if err != nil {
			return "", nil, err
		}
	}
	name := sanitizeDocxName(tpl.Name)
	if name == "" {
		name = "template"
	}
	return name + ".docx", data, nil
}

// DocxBySHA 按内容 sha 读取模板 docx（回调渲染在途记录用）。
func (s *TemplateStore) DocxBySHA(sha string) ([]byte, error) {
	return s.readDocxBySHA(sha)
}

// WorkflowAPIKey 取生成用 API Key：builtin 返回空（调用方沿用全局 DIFY_SAFEDAY_API_KEY）；
// 自定义模板解密 API Key，缺失/解密失败明确报错（文案不含明文/密文）。
func (s *TemplateStore) WorkflowAPIKey(id string) (string, error) {
	if id == BuiltinTemplateID {
		return "", nil
	}
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

// Snapshot 生成记录用模板快照：校验模板存在且 docx 可读（缺失明确报错）。
func (s *TemplateStore) Snapshot(id string) (TemplateSnapshot, error) {
	if id == BuiltinTemplateID {
		return s.builtinSnapshot()
	}
	s.mu.RLock()
	idx := s.findLocked(id)
	if idx < 0 {
		s.mu.RUnlock()
		return TemplateSnapshot{}, ErrTemplateNotFound
	}
	rec := cloneTemplateRecord(s.index.Templates[idx])
	s.mu.RUnlock()
	if rec.SHA256 == "" {
		return TemplateSnapshot{}, fmt.Errorf("%w：模板未上传 docx", ErrTemplateFileMissing)
	}
	data, err := s.readDocxBySHA(rec.SHA256)
	if err != nil {
		return TemplateSnapshot{}, err
	}
	// 不只看文件存在：动态模板必须仍是含独占 {@章节区} 的合法 docx
	if err := validateTemplateDocx(data); err != nil {
		return TemplateSnapshot{}, fmt.Errorf("%w：%v", ErrTemplateFileInvalid, err)
	}
	return TemplateSnapshot{
		ID: rec.ID, Name: rec.Name, Mode: rec.Mode, SHA256: rec.SHA256,
		Sections: copySections(rec.Sections),
	}, nil
}

// SampleDocx 动态模板样例（管理端下载）。
func (s *TemplateStore) SampleDocx() ([]byte, error) {
	path := strings.TrimSpace(s.SamplePath)
	if path == "" {
		return nil, fmt.Errorf("%w：动态样例模板未随部署提供", ErrTemplateFileMissing)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w：%v", ErrTemplateFileMissing, err)
	}
	return data, nil
}

// builtinSnapshot 内置静态模板快照（读取当前模板文件算 sha）。
func (s *TemplateStore) builtinSnapshot() (TemplateSnapshot, error) {
	return builtinSnapshotFromFile(s.builtinPath())
}

// builtinSnapshotFromFile 从指定路径读取内置静态模板并生成快照。
func builtinSnapshotFromFile(path string) (TemplateSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TemplateSnapshot{}, fmt.Errorf("%w：内置模板文件缺失（%v）", ErrTemplateFileMissing, err)
	}
	if _, err := exceldoc.ReadDocx(data); err != nil {
		return TemplateSnapshot{}, fmt.Errorf("%w：内置模板不是有效 docx（%v）", ErrTemplateFileInvalid, err)
	}
	sum := sha256.Sum256(data)
	return TemplateSnapshot{
		ID: BuiltinTemplateID, Name: builtinTemplateName, Mode: TemplateModeStatic,
		SHA256: hex.EncodeToString(sum[:]), Sections: copySections(builtinSections),
	}, nil
}

// builtinPath 内置模板路径（空时按部署布局解析）。
func (s *TemplateStore) builtinPath() string {
	if strings.TrimSpace(s.BuiltinPath) != "" {
		return s.BuiltinPath
	}
	return ResolveTemplatePath()
}

// builtinViewLocked 内置模板视图（调用方持锁）。
func (s *TemplateStore) builtinViewLocked() Template {
	return Template{
		ID: BuiltinTemplateID, Name: builtinTemplateName, Mode: TemplateModeStatic,
		Sections: copySections(builtinSections), HasAPIKey: false,
		IsDefault: s.effectiveDefaultLocked() == BuiltinTemplateID,
	}
}

// viewLocked 自定义模板视图（调用方持锁）。
func (s *TemplateStore) viewLocked(rec templateRecord) Template {
	return Template{
		ID: rec.ID, Name: rec.Name, Mode: rec.Mode, Sections: copySections(rec.Sections),
		SHA256: rec.SHA256, Size: rec.Size, HasAPIKey: rec.APIKeyEnc != "",
		IsDefault: s.effectiveDefaultLocked() == rec.ID,
		CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt,
	}
}

// effectiveDefaultLocked 生效默认 id（调用方持锁）。
func (s *TemplateStore) effectiveDefaultLocked() string {
	id := strings.TrimSpace(s.index.DefaultID)
	if id == "" || id == BuiltinTemplateID {
		return BuiltinTemplateID
	}
	for _, rec := range s.index.Templates {
		if rec.ID == id {
			return id
		}
	}
	return BuiltinTemplateID
}

// findLocked 按 id 查自定义模板下标（调用方持锁）。
func (s *TemplateStore) findLocked(id string) int {
	for i := range s.index.Templates {
		if s.index.Templates[i].ID == id {
			return i
		}
	}
	return -1
}

// writeIndexLockedWith 以指定索引内容原子写出并替换缓存（调用方持锁）。
func (s *TemplateStore) writeIndexLockedWith(next templateIndex) error {
	prev := s.index
	s.index = next
	if err := s.writeIndexLocked(); err != nil {
		s.index = prev
		return err
	}
	return nil
}

// storeDocx 内容寻址落盘，返回 sha256（同内容幂等）。
func (s *TemplateStore) storeDocx(data []byte) (string, error) {
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	path := s.docxPath(sha)
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		// 已存在同 sha 文件：确认内容未损坏则复用，损坏则用本次上传内容重写
		if existing, err := os.ReadFile(path); err == nil {
			sum2 := sha256.Sum256(existing)
			if hex.EncodeToString(sum2[:]) == sha {
				return sha, nil
			}
		}
	}
	tmp, err := os.CreateTemp(s.filesDir, "tpl-*.tmp")
	if err != nil {
		return "", fmt.Errorf("保存模板文件失败：%w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("保存模板文件失败：%w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("保存模板文件失败：%w", err)
	}
	if err := atomicReplaceFile(tmpName, path); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("保存模板文件失败：%w", err)
	}
	return sha, nil
}

// docxPath 内容寻址 docx 路径。
func (s *TemplateStore) docxPath(sha string) string {
	return filepath.Join(s.filesDir, sha+".docx")
}

// readDocxBySHA 校验 sha 后读取内容文件。
func (s *TemplateStore) readDocxBySHA(sha string) ([]byte, error) {
	if !reSHA256.MatchString(sha) {
		return nil, fmt.Errorf("%w：模板 sha 不合法", ErrTemplateFileMissing)
	}
	data, err := os.ReadFile(s.docxPath(sha))
	if err != nil {
		return nil, fmt.Errorf("%w：%s", ErrTemplateFileMissing, sha)
	}
	// 内容寻址文件必须与文件名 sha 一致，防损坏/误覆盖后静默渲染
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != sha {
		return nil, fmt.Errorf("%w：sha256 不一致", ErrTemplateFileInvalid)
	}
	return data, nil
}

// ===== 校验 =====

// validateTemplateName 名称去空白且非空（≤64 字）。
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

// normalizeSections 章节去空白、校验 key/标题/重复/保留字。
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
			return nil, invalidTemplate("章节标识不合法：%s（须小写字母开头，仅字母/数字/下划线，且不得为 date/class/sections/url）", key)
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

// validateTemplateAPIKey API Key 去空白且非空、不含换行（防请求头注入）。
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

// validateTemplateDocx docx 合法且含独占 {@章节区}。
func validateTemplateDocx(data []byte) error {
	if len(data) == 0 {
		return invalidTemplate("请上传模板 docx 文件")
	}
	if len(data) > maxTemplateFileSize {
		return invalidTemplate("模板文件过大（最大 %dMB）", maxTemplateFileSize>>20)
	}
	parts, err := exceldoc.ReadDocx(data)
	if err != nil {
		return invalidTemplate("模板不是有效的 docx 文件：%v", err)
	}
	xml := string(parts["word/document.xml"])
	if !exceldoc.HasExclusiveRawPlaceholder(xml, sectionsPlaceholder) {
		return invalidTemplate("模板必须包含独占一行的 {@%s} 占位符", sectionsPlaceholder)
	}
	return nil
}

// ===== 拷贝辅助 =====

// copySections 深拷贝章节切片。
func copySections(sections []TemplateSection) []TemplateSection {
	if sections == nil {
		return []TemplateSection{}
	}
	return append([]TemplateSection(nil), sections...)
}

// cloneTemplateRecord 深拷贝持久化条目。
func cloneTemplateRecord(rec templateRecord) templateRecord {
	rec.Sections = copySections(rec.Sections)
	return rec
}

// cloneTemplateIndex 深拷贝索引。
func cloneTemplateIndex(idx templateIndex) templateIndex {
	out := idx
	out.Templates = make([]templateRecord, len(idx.Templates))
	for i, rec := range idx.Templates {
		out.Templates[i] = cloneTemplateRecord(rec)
	}
	return out
}

// newTemplateID 生成模板 id：tpl-毫秒时间戳-4 字节 hex。
func newTemplateID() string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return "tpl-" + strconv.FormatInt(time.Now().UnixMilli(), 10) + "-" + hex.EncodeToString(buf)
}

// sanitizeDocxName 下载名清洗（去路径分隔符）。
func sanitizeDocxName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(name)
	return name
}
