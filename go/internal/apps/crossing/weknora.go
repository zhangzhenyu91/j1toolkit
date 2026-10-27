package crossing

// 交跨台账 WeKnora 知识库同步（快照语义，按线路一表 xlsx，内容哈希增量 + 删旧传新，见 开发指南.md 19.7）。
// 同步对象：CROSSING_SYNC_TEAM 指定班组的全部记录，按 line_name 一线路一表（空名归「未分组线路」），
// 复用导出单表口径出表（去首行标题 skipTitle），fileName=「交跨台账/{线路名}.xlsx」上传。
// 增量判定=内容哈希（sortRecords 排序后序列化 sha256）；变了/新线路删旧传新（同名 409 → 删旧重试一次）；
// 状态文件本地 JSON（tmp+rename 原子写），缺失/损坏回退空状态=全量重传自愈。
// 未配置 WEKNORA_CROSSING_KB_ID / WEKNORA_API_KEY 时明确报未配置，不假成功。
// API 面可注入（WeKnoraClient），测试用假实现，不连真实服务。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"shade/internal/compat/exceldoc"
)

// WeKnora 同步常量（与 weknorasync.js 一致）。
const (
	weknoraFolderCrossing  = "交跨台账"
	weknoraChannelCrossing = "crossing-sync"
	weknoraUngroupedLine   = "未分组线路"
	weknoraPageSize        = 100
	weknoraListMaxPages    = 10
	weknoraBatchDeleteMax  = 200
)

// ErrWeKnoraNotConfigured WeKnora 未配置（明确返回，不假成功）。
var ErrWeKnoraNotConfigured = errors.New("WeKnora 未配置（WEKNORA_CROSSING_KB_ID / WEKNORA_API_KEY），知识库同步停用")

// WeKnoraDuplicateError 上传同名冲突（409）：带已有条目 id 供删旧重试。
type WeKnoraDuplicateError struct{ IDs []string }

func (e *WeKnoraDuplicateError) Error() string { return "WeKnora 已存在同名知识文件" }

// WeKnoraUpload 上传入参（fileName 带目录路径，路径即文件夹）。
type WeKnoraUpload struct {
	FileName string // 「交跨台账/{线路名}.xlsx」
	Title    string
	Channel  string
	Data     []byte
}

// WeKnoraClient WeKnora 知识库 API 适配层（可注入；HTTP 实现见 HTTPWeKnoraClient）。
type WeKnoraClient interface {
	ListKnowledge(ctx context.Context, kbID string, q map[string]string) ([]map[string]any, error)
	UploadKnowledgeFile(ctx context.Context, kbID string, p WeKnoraUpload) (string, error)
	DeleteKnowledge(ctx context.Context, id string) error
	BatchDeleteKnowledge(ctx context.Context, kbID string, ids []string) error
}

// WeKnoraLineState 单线路同步状态（哈希 + 上传产物 id）。
type WeKnoraLineState struct {
	Hash        string `json:"hash"`
	KnowledgeID string `json:"knowledgeId"`
	FileName    string `json:"fileName"`
	SyncedAt    string `json:"syncedAt"`
}

// WeKnoraState 本地同步状态（version=2：知识库表去首行标题口径）。
type WeKnoraState struct {
	Version int                         `json:"version"`
	Lines   map[string]WeKnoraLineState `json:"lines"`
}

// WeKnoraStateStore 同步状态存储（可注入）。
type WeKnoraStateStore interface {
	Load(ctx context.Context) (WeKnoraState, error) // 缺失/损坏 → 空状态（全量重传自愈）
	Save(ctx context.Context, st WeKnoraState) error
}

// FileWeKnoraState 状态文件存储（tmp+rename 原子写）。
type FileWeKnoraState struct{ Path string }

// Load 读状态文件（缺失/损坏回退空状态）。
func (f FileWeKnoraState) Load(context.Context) (WeKnoraState, error) {
	st := WeKnoraState{Version: 2, Lines: map[string]WeKnoraLineState{}}
	raw, err := os.ReadFile(f.Path)
	if err != nil {
		return st, nil
	}
	var parsed struct {
		Version int                         `json:"version"`
		Lines   map[string]WeKnoraLineState `json:"lines"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed.Version != 2 || parsed.Lines == nil {
		return st, nil
	}
	return WeKnoraState{Version: 2, Lines: parsed.Lines}, nil
}

// Save 原子写状态文件（先写临时文件再 rename）。
func (f FileWeKnoraState) Save(_ context.Context, st WeKnoraState) error {
	if st.Lines == nil {
		st.Lines = map[string]WeKnoraLineState{}
	}
	st.Version = 2
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", f.Path, os.Getpid())
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

// WeKnoraSyncResult 一轮同步结果。
type WeKnoraSyncResult struct {
	OK      bool     `json:"ok"`
	Created int      `json:"created"`
	Updated int      `json:"updated"`
	Deleted int      `json:"deleted"`
	Skipped int      `json:"skipped"`
	Errors  []string `json:"errors"`
}

// wkCfg 读运行时配置（空串容忍）。
func (m *Module) wkCfg(ctx context.Context, key string) string {
	v, _ := m.cfgValue(ctx, key, "")
	return strings.TrimSpace(v)
}

// weknoraCfg 同步所需配置；缺任一必填项返回 ErrWeKnoraNotConfigured。
func (m *Module) weknoraCfg(ctx context.Context) (kbID string, client WeKnoraClient, state WeKnoraStateStore, err error) {
	kbID = m.wkCfg(ctx, "WEKNORA_CROSSING_KB_ID")
	apiKey := m.wkCfg(ctx, "WEKNORA_API_KEY")
	if kbID == "" || apiKey == "" {
		return "", nil, nil, ErrWeKnoraNotConfigured
	}
	client = m.WeKnora
	if client == nil {
		apiURL := m.wkCfg(ctx, "WEKNORA_API_URL")
		if apiURL == "" {
			apiURL = "https://know.j1net.com/api/v1"
		}
		client = NewHTTPWeKnoraClient(apiURL, apiKey, m.wkCfg(ctx, "WEKNORA_SYNC_USER"))
	}
	state = m.WeKnoraState
	if state == nil {
		state = FileWeKnoraState{Path: filepath.Join("data", "crossing", "weknora-sync-state.json")}
	}
	return kbID, client, state, nil
}

// recordsHash 内容哈希：对 exportGroupComparator 排序后的记录集序列化算 sha256
// （不哈希 xlsx buffer——zip 时间戳不稳定；记录内容不变 ⇒ 表内容不变）。
func recordsHash(records []Record) string {
	sorted := sortRecords(records, exportGroupComparator)
	raw, _ := json.Marshal(sorted)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type weknoraBook struct {
	LineName string
	FileName string
	Data     []byte
	Hash     string
}

// buildLineBooks 按 line_name 一线路一表（空名归「未分组线路」；单表列序/隐藏记录ID 列与整表导出一致，去首行标题）。
func buildLineBooks(records []Record) ([]weknoraBook, error) {
	groups := map[string][]Record{}
	names := make([]string, 0)
	for _, r := range records {
		name := strings.TrimSpace(r.LineName)
		if name == "" {
			name = weknoraUngroupedLine
		}
		if _, ok := groups[name]; !ok {
			names = append(names, name)
		}
		groups[name] = append(groups[name], r)
	}
	sort.Strings(names)
	books := make([]weknoraBook, 0, len(names))
	for _, name := range names {
		recs := groups[name]
		sorted := sortRecords(recs, exportGroupComparator)
		buf, err := exceldoc.WriteWorkbook(exceldoc.WorkbookSpec{
			Sheets: []exceldoc.SheetSpec{sheetSpecOf(sheetNameOf(name, map[string]bool{}), "", sorted)},
		})
		if err != nil {
			return nil, err
		}
		books = append(books, weknoraBook{
			LineName: name,
			FileName: safeFileName(name) + ".xlsx",
			Data:     buf,
			Hash:     recordsHash(recs),
		})
	}
	return books, nil
}

// weknoraListFolder 拉「交跨台账」文件夹内本渠道全部条目（分页兜底）。
func weknoraListFolder(ctx context.Context, client WeKnoraClient, kbID string) ([]map[string]any, error) {
	out := make([]map[string]any, 0)
	for page := 1; page <= weknoraListMaxPages; page++ {
		items, err := client.ListKnowledge(ctx, kbID, map[string]string{
			"folder_path":      weknoraFolderCrossing,
			"folder_recursive": "false",
			"source":           weknoraChannelCrossing,
			"page":             strconv.Itoa(page),
			"page_size":        strconv.Itoa(weknoraPageSize),
		})
		if err != nil {
			return out, err
		}
		out = append(out, items...)
		if len(items) < weknoraPageSize {
			break
		}
	}
	return out, nil
}

// weknoraBaseName 列表条目文件名（可能带 folder_path 前缀，取末段基名）。
func weknoraBaseName(k map[string]any) string {
	raw := strings.TrimSpace(toString(firstNonNil(k["file_name"], k["fileName"], k["title"])))
	return raw[strings.LastIndexByte(raw, '/')+1:]
}

func firstNonNil(vals ...any) any {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

// weknoraKnowledgeID 提取条目 id。
func weknoraKnowledgeID(k map[string]any) string {
	if k == nil {
		return ""
	}
	return strings.TrimSpace(toString(firstNonNil(k["id"], k["knowledge_id"])))
}

// weknoraUploadXlsx 上传线路表（同名 409 → 取响应已有 id 删除后重试一次，再失败上抛）。
func weknoraUploadXlsx(ctx context.Context, client WeKnoraClient, kbID string, book weknoraBook) (int, string, error) {
	payload := WeKnoraUpload{
		FileName: weknoraFolderCrossing + "/" + book.FileName,
		Title:    book.FileName,
		Channel:  weknoraChannelCrossing,
		Data:     book.Data,
	}
	doUpload := func() (string, error) {
		return client.UploadKnowledgeFile(ctx, kbID, payload)
	}
	id, err := doUpload()
	if err == nil {
		return 0, id, nil
	}
	var dup *WeKnoraDuplicateError
	if !errors.As(err, &dup) || len(dup.IDs) == 0 {
		return 0, "", err
	}
	for _, oldID := range dup.IDs {
		if dErr := client.DeleteKnowledge(ctx, oldID); dErr != nil {
			return 0, "", fmt.Errorf("删同名旧知识失败：%w", dErr)
		}
	}
	id, err = doUpload()
	if err != nil {
		return len(dup.IDs), "", err
	}
	return len(dup.IDs), id, nil
}

// RunWeKnoraSync 一轮知识库快照同步：逐线路哈希比对 → 未变跳过 / 变了删旧传新 →
// 已下线路删条目清状态项 → 兜底清理本渠道孤儿；单步失败记入 errors 不中断整批。
// 未配置返回 ErrWeKnoraNotConfigured（明确返回而非假成功）。
func (m *Module) RunWeKnoraSync(ctx context.Context) (WeKnoraSyncResult, error) {
	kbID, client, state, err := m.weknoraCfg(ctx)
	if err != nil {
		return WeKnoraSyncResult{OK: false, Errors: []string{err.Error()}}, err
	}
	res := WeKnoraSyncResult{OK: true}
	teamName := m.wkCfg(ctx, "CROSSING_SYNC_TEAM")
	if teamName == "" {
		teamName = "检修一班"
	}
	var teamID int64
	var matchedName string
	if err := m.DB.QueryRowContext(ctx,
		`SELECT id, name FROM sys_team WHERE name = ? AND status = 1`, teamName).Scan(&teamID, &matchedName); err != nil {
		e := fmt.Errorf("未找到同步班组「%s」（未建或已停用）", teamName)
		return WeKnoraSyncResult{OK: false, Errors: []string{e.Error()}}, e
	}
	records, err := m.loadTeamRecords(teamID)
	if err != nil {
		return WeKnoraSyncResult{OK: false, Errors: []string{err.Error()}}, err
	}
	books, err := buildLineBooks(records)
	if err != nil {
		return WeKnoraSyncResult{OK: false, Errors: []string{err.Error()}}, err
	}
	st, err := state.Load(ctx)
	if err != nil {
		st = WeKnoraState{Version: 2, Lines: map[string]WeKnoraLineState{}}
	}
	currentLines := map[string]bool{}
	currentNames := map[string]bool{}
	removedIDs := map[string]bool{}
	for _, b := range books {
		currentLines[b.LineName] = true
		currentNames[b.FileName] = true
	}

	for _, book := range books {
		old, hasOld := st.Lines[book.LineName]
		if hasOld && old.Hash == book.Hash {
			res.Skipped++
			continue
		}
		deleteErrMsg := ""
		if hasOld && old.KnowledgeID != "" {
			if dErr := client.DeleteKnowledge(ctx, old.KnowledgeID); dErr != nil {
				deleteErrMsg = fmt.Sprintf("删除旧「%s」失败：%v", book.FileName, dErr)
			} else {
				removedIDs[old.KnowledgeID] = true
				res.Deleted++
			}
		}
		conflictDeleted, knowledgeID, uErr := weknoraUploadXlsx(ctx, client, kbID, book)
		if uErr != nil {
			prefix := ""
			if deleteErrMsg != "" {
				prefix = deleteErrMsg + "；"
			}
			res.Errors = append(res.Errors, fmt.Sprintf("%s上传「%s」失败：%v", prefix, book.FileName, uErr))
			continue
		}
		res.Deleted += conflictDeleted
		id := knowledgeID
		if id == "" {
			id = old.KnowledgeID
		}
		st.Lines[book.LineName] = WeKnoraLineState{
			Hash: book.Hash, KnowledgeID: id, FileName: book.FileName,
			SyncedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if hasOld {
			res.Updated++
		} else {
			res.Created++
		}
	}

	for lineName, old := range st.Lines {
		if currentLines[lineName] {
			continue
		}
		if old.KnowledgeID != "" {
			if dErr := client.DeleteKnowledge(ctx, old.KnowledgeID); dErr != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("清理已下线路「%s」失败：%v", lineName, dErr))
				continue
			}
			removedIDs[old.KnowledgeID] = true
			res.Deleted++
		}
		delete(st.Lines, lineName)
	}

	if listed, lErr := weknoraListFolder(ctx, client, kbID); lErr != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("清理孤儿条目失败：%v", lErr))
	} else {
		stateIDs := map[string]bool{}
		for _, s := range st.Lines {
			if s.KnowledgeID != "" {
				stateIDs[s.KnowledgeID] = true
			}
		}
		orphans := make([]string, 0)
		seen := map[string]bool{}
		for _, k := range listed {
			id := weknoraKnowledgeID(k)
			if id == "" || seen[id] || removedIDs[id] {
				continue
			}
			seen[id] = true
			if currentNames[weknoraBaseName(k)] || stateIDs[id] {
				continue
			}
			orphans = append(orphans, id)
		}
		for i := 0; i < len(orphans); i += weknoraBatchDeleteMax {
			end := min(i+weknoraBatchDeleteMax, len(orphans))
			if bErr := client.BatchDeleteKnowledge(ctx, kbID, orphans[i:end]); bErr != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("清理孤儿条目失败：%v", bErr))
				continue
			}
			res.Deleted += end - i
		}
	}
	if sErr := state.Save(ctx, st); sErr != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("同步状态写入失败：%v", sErr))
	}
	res.OK = len(res.Errors) == 0
	return res, nil
}

// weknoraPushNow POST /weknora-sync/push-now：手动触发一轮知识库快照同步（班管/超管）。
func (m *Module) weknoraPushNow(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.enterAdmin(w, r, 0); !ok {
		return
	}
	ctx := r.Context()
	if m.wkCfg(ctx, "WEKNORA_CROSSING_KB_ID") == "" || m.wkCfg(ctx, "WEKNORA_API_KEY") == "" {
		fail(w, http.StatusServiceUnavailable, codeNoToken, "未配置知识库同步")
		return
	}
	m.syncMu.Lock()
	if m.weknoraRunning {
		m.syncMu.Unlock()
		fail(w, http.StatusLocked, 42301, "同步进行中，请稍后再试")
		return
	}
	m.weknoraRunning = true
	m.syncMu.Unlock()
	defer func() {
		m.syncMu.Lock()
		m.weknoraRunning = false
		m.syncMu.Unlock()
	}()
	res, err := m.RunWeKnoraSync(ctx)
	if err != nil && errors.Is(err, ErrWeKnoraNotConfigured) {
		fail(w, http.StatusServiceUnavailable, codeNoToken, "未配置知识库同步")
		return
	}
	msg := "同步完成，部分失败"
	if res.OK {
		msg = "已同步至知识库"
	}
	writeOK(w, map[string]any{
		"ok": res.OK, "created": res.Created, "updated": res.Updated,
		"deleted": res.Deleted, "skipped": res.Skipped, "errors": res.Errors,
	}, msg)
}

// HTTPWeKnoraClient 内置 WeKnora HTTP 实现（X-API-Key 认证 + X-External-User-ID）。
type HTTPWeKnoraClient struct {
	BaseURL  string
	APIKey   string
	SyncUser string
	HTTP     *http.Client
}

// NewHTTPWeKnoraClient 构造 HTTP 客户端（syncUser 空回退 admin，同 kbHeaders 口径）。
func NewHTTPWeKnoraClient(baseURL, apiKey, syncUser string) *HTTPWeKnoraClient {
	if syncUser == "" {
		syncUser = "admin"
	}
	return &HTTPWeKnoraClient{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		APIKey:   apiKey,
		SyncUser: syncUser,
		HTTP:     &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *HTTPWeKnoraClient) do(ctx context.Context, method, path string, query map[string]string, body io.Reader, contentType string) (map[string]any, []map[string]any, error) {
	u := c.BaseURL + path
	if len(query) > 0 {
		q := url.Values{}
		for k, v := range query {
			q.Set(k, v)
		}
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("X-API-Key", c.APIKey)
	req.Header.Set("X-External-User-ID", c.SyncUser)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusConflict {
		obj, list := weknoraParseData(raw)
		ids := make([]string, 0)
		for _, k := range list {
			if id := weknoraKnowledgeID(k); id != "" {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			if id := weknoraKnowledgeID(obj); id != "" {
				ids = append(ids, id)
			}
		}
		return nil, nil, &WeKnoraDuplicateError{IDs: ids}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("WeKnora 响应 %d", resp.StatusCode)
	}
	obj, list := weknoraParseData(raw)
	if obj != nil && obj["success"] == false {
		return nil, nil, fmt.Errorf("WeKnora 失败：%v", firstNonNil(obj["message"], obj["error"]))
	}
	return obj, list, nil
}

// weknoraParseData 解包 {success,data,...}：data 为数组 → list；为对象 → obj。
func weknoraParseData(raw []byte) (map[string]any, []map[string]any) {
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return nil, nil
	}
	if body["success"] == false {
		return body, nil
	}
	data, ok := body["data"]
	if !ok {
		return body, nil
	}
	if arr, ok := data.([]any); ok {
		list := make([]map[string]any, 0, len(arr))
		for _, it := range arr {
			if obj, ok := it.(map[string]any); ok {
				list = append(list, obj)
			}
		}
		return body, list
	}
	if obj, ok := data.(map[string]any); ok {
		for _, key := range []string{"items", "list"} {
			if arr, ok := obj[key].([]any); ok {
				list := make([]map[string]any, 0, len(arr))
				for _, it := range arr {
					if o, ok := it.(map[string]any); ok {
						list = append(list, o)
					}
				}
				return body, list
			}
		}
		return obj, nil
	}
	return body, nil
}

// ListKnowledge 列知识条目。
func (c *HTTPWeKnoraClient) ListKnowledge(ctx context.Context, kbID string, q map[string]string) ([]map[string]any, error) {
	_, list, err := c.do(ctx, http.MethodGet, "/knowledge-bases/"+url.PathEscape(kbID)+"/knowledge", q, nil, "")
	return list, err
}

// UploadKnowledgeFile 上传知识文件（multipart：file / fileName / title / channel）。
func (c *HTTPWeKnoraClient) UploadKnowledgeFile(ctx context.Context, kbID string, p WeKnoraUpload) (string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	baseName := p.FileName[strings.LastIndexByte(p.FileName, '/')+1:]
	part, err := mw.CreateFormFile("file", baseName)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(p.Data); err != nil {
		return "", err
	}
	_ = mw.WriteField("fileName", p.FileName)
	if p.Title != "" {
		_ = mw.WriteField("title", p.Title)
	}
	if p.Channel != "" {
		_ = mw.WriteField("channel", p.Channel)
	}
	_ = mw.Close()
	obj, _, err := c.do(ctx, http.MethodPost, "/knowledge-bases/"+url.PathEscape(kbID)+"/knowledge/file",
		nil, &buf, mw.FormDataContentType())
	if err != nil {
		return "", err
	}
	return weknoraKnowledgeID(obj), nil
}

// DeleteKnowledge 删单条知识。
func (c *HTTPWeKnoraClient) DeleteKnowledge(ctx context.Context, id string) error {
	_, _, err := c.do(ctx, http.MethodDelete, "/knowledge/"+url.PathEscape(id), nil, nil, "")
	return err
}

// BatchDeleteKnowledge 批量删知识。
func (c *HTTPWeKnoraClient) BatchDeleteKnowledge(ctx context.Context, kbID string, ids []string) error {
	payload, _ := json.Marshal(map[string]any{"kb_id": kbID, "knowledge_ids": ids})
	_, _, err := c.do(ctx, http.MethodPost, "/knowledge/batch-delete",
		nil, bytes.NewReader(payload), "application/json")
	return err
}

var _ WeKnoraClient = (*HTTPWeKnoraClient)(nil)
var _ WeKnoraStateStore = FileWeKnoraState{}
