package netdisk

import (
	"archive/zip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// pipeDirZip 目录 ZIP 流式打包下载：逐层读取 OpenList 目录、逐文件流式写入，
// 保留目录层级与空文件夹；不整读文件入内存、不在网盘侧生成临时压缩包。
// 打包不二次压缩（store，Node archiver {store:true} 同口径）；目录内出现不合法名称或上游断流
// 时中止连接（Node res.destroy 同口径，让浏览器把本次下载判失败，防半截包静默成功）。
func (m *Module) pipeDirZip(w http.ResponseWriter, r *http.Request, ol OpenListClient, absRoot, rootName string) {
	if zipSegment(rootName) == "" {
		writeFail(w, http.StatusBadGateway, codeUpstream, "目录内存在不合法的名称，无法打包", nil)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition",
		`attachment; filename="download.zip"; filename*=UTF-8''`+url.PathEscape(rootName+".zip"))
	w.WriteHeader(http.StatusOK)
	zw := zip.NewWriter(w)
	defer zw.Close()
	if err := m.zipWalk(r, ol, zw, absRoot, rootName); err != nil {
		// ZIP 已开始写出后不能再改 JSON 错误信封：断开连接让浏览器中止本次下载。
		panic(http.ErrAbortHandler)
	}
}

// 目录打包资源上限：深层递归/超大目录直接中止（防递归栈与内存无界增长）。
const (
	zipMaxDepth   = 32   // 目录层级上限（根为第 0 层）
	zipMaxEntries = 5000 // 条目总数上限（目录项 + 文件项）
)

// errZipLimitExceeded 目录层级或条目超出打包上限。
var errZipLimitExceeded = errors.New("目录层级或条目超出打包上限")

// zipWalk 递归写入目录（每层先写显式目录项，空文件夹也保留；文件走 store 不二次压缩）。
// 深度 1 预取：仅在同目录紧邻的下一个文件上预取下载流（峰值 ≤2），不跨目录递归预取，
// 条目顺序与串行下载完全一致；所有错误路径关闭在飞流（由 defer 兜底）。
func (m *Module) zipWalk(r *http.Request, ol OpenListClient, zw *zip.Writer, abs, entry string) error {
	entries := 0
	return m.zipWalkDepth(r, ol, zw, abs, entry, 0, &entries)
}

// zipWalkDepth 带深度与条目预算的递归实现。
func (m *Module) zipWalkDepth(r *http.Request, ol OpenListClient, zw *zip.Writer, abs, entry string, depth int, entries *int) error {
	if depth >= zipMaxDepth {
		return errZipLimitExceeded
	}
	*entries++
	if *entries > zipMaxEntries {
		return errZipLimitExceeded
	}
	if _, err := zw.Create(entry + "/"); err != nil {
		return err
	}
	data, err := ol.Call(r.Context(), http.MethodPost, "/api/fs/list", CallOpts{
		Body: map[string]any{"path": abs, "password": "", "page": 1, "per_page": 0, "refresh": false},
	})
	if err != nil {
		return err
	}
	var pending io.ReadCloser
	defer func() {
		if pending != nil {
			_ = pending.Close()
		}
	}()
	items := parseItems(data)
	for i, item := range items {
		name := zipSegment(item.Name)
		if name == "" {
			return errBadZipName
		}
		childEntry := entry + "/" + name
		if item.IsDir {
			if err := m.zipWalkDepth(r, ol, zw, abs+"/"+item.Name, childEntry, depth+1, entries); err != nil {
				return err
			}
			continue
		}
		*entries++
		if *entries > zipMaxEntries {
			return errZipLimitExceeded
		}
		rc := pending
		pending = nil
		if rc == nil {
			rc, _, err = ol.DownloadStream(r.Context(), abs+"/"+item.Name)
			if err != nil {
				return err
			}
		}
		// 只预取紧邻的下一个文件（下一项是目录/非法名称时留给后续串行处理，避免跨层叠流）。
		if i+1 < len(items) && !items[i+1].IsDir && zipSegment(items[i+1].Name) != "" {
			next, _, derr := ol.DownloadStream(r.Context(), abs+"/"+items[i+1].Name)
			if derr != nil {
				_ = rc.Close()
				return derr
			}
			pending = next
		}
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: childEntry, Method: zip.Store})
		if err != nil {
			_ = rc.Close()
			return err
		}
		_, err = io.Copy(fw, rc)
		_ = rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// errBadZipName 目录内存在不合法名称（Node zipSegment 抛错同口径）。
var errBadZipName = errors.New("目录内存在不合法的名称，无法打包")

// zipSegment ZIP 内单个路径段：拒绝分隔符/控制符与 .、..，防路径穿越与异常条目；
// 并拒绝 ^\w+: 前缀（archiver-utils.sanitizePath 落盘时会剥掉该前缀，entry 实名与请求名
// 不一致将挂起打包），同 Node zipSegment 口径。
func zipSegment(name string) string {
	n := name
	if n == "" || n == "." || n == ".." || strings.ContainsAny(n, "/\\\r\n") {
		return ""
	}
	if i := strings.Index(n, ":"); i > 0 {
		word := true
		for _, r := range n[:i] {
			if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
				word = false
				break
			}
		}
		if word {
			return ""
		}
	}
	return n
}

// ---- 压缩包预览 / 解压 ----

// archiveItem 压缩包条目（OpenList content 树）。
type archiveItem struct {
	Name     string        `json:"name"`
	IsDir    bool          `json:"is_dir"`
	Size     int64         `json:"size"`
	Modified string        `json:"modified"`
	Children []archiveItem `json:"children"`
}

// mapArchiveItem 压缩包条目 → 前端条目（含内层 rel，递归映射 children 树；Node mapArchiveItem 同口径）。
func mapArchiveItem(o archiveItem, innerBase string) map[string]any {
	base := innerBase
	if base == "/" {
		base = ""
	}
	out := map[string]any{
		"name":     o.Name,
		"is_dir":   o.IsDir,
		"size":     o.Size,
		"modified": o.Modified,
		"rel":      base + "/" + o.Name,
	}
	if len(o.Children) > 0 {
		kids := make([]map[string]any, 0, len(o.Children))
		for _, c := range o.Children {
			kids = append(kids, mapArchiveItem(c, base+"/"+o.Name))
		}
		out["children"] = kids
	}
	return out
}

// handleArchiveMeta POST /archive/meta：压缩包元信息（encrypted=true 时前端弹压缩密码框）。
// 响应 {encrypted, comment, content}（content 递归映射 rel/children；上游 sign 为包内下载短时效
// 签名，不外发），同 Node mapArchiveItem 映射口径。
func (m *Module) handleArchiveMeta(w http.ResponseWriter, r *http.Request) {
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	body, err := decodeBodyMap(r)
	if err != nil {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误", nil)
		return
	}
	root, ok := m.resolveRoot(w, r, rawString(body["space"]))
	if !ok {
		return
	}
	rel, ok := NormRel(rawString(body["path"]))
	if !ok || rel == "/" {
		writeFail(w, http.StatusBadRequest, codeParam, "路径不合法", nil)
		return
	}
	data, err := ol.Call(r.Context(), http.MethodPost, "/api/fs/archive/meta", CallOpts{Body: map[string]any{
		"path": JoinPath(root, rel), "password": "", "refresh": false, "archive_pass": rawString(body["archive_pass"]),
	}})
	if err != nil {
		relayFail(w, err)
		return
	}
	var meta struct {
		Encrypted bool          `json:"encrypted"`
		Comment   string        `json:"comment"`
		Content   []archiveItem `json:"content"`
	}
	_ = json.Unmarshal(data, &meta)
	items := make([]map[string]any, 0, len(meta.Content))
	for _, o := range meta.Content {
		items = append(items, mapArchiveItem(o, "/"))
	}
	writeOK(w, map[string]any{"encrypted": meta.Encrypted, "comment": meta.Comment, "content": items}, "")
}

// handleArchiveList POST /archive/list：按 inner_path 平铺列内层目录。
// 响应 {inner_path, items}（items 映射 rel/children），同 Node 口径。
func (m *Module) handleArchiveList(w http.ResponseWriter, r *http.Request) {
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	body, err := decodeBodyMap(r)
	if err != nil {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误", nil)
		return
	}
	root, ok := m.resolveRoot(w, r, rawString(body["space"]))
	if !ok {
		return
	}
	rel, ok := NormRel(rawString(body["path"]))
	if !ok || rel == "/" {
		writeFail(w, http.StatusBadRequest, codeParam, "路径不合法", nil)
		return
	}
	inner, ok := NormRel(rawString(body["inner_path"]))
	if !ok {
		writeFail(w, http.StatusBadRequest, codeParam, "内层路径不合法", nil)
		return
	}
	data, err := ol.Call(r.Context(), http.MethodPost, "/api/fs/archive/list", CallOpts{Body: map[string]any{
		"path": JoinPath(root, rel), "password": "", "refresh": false,
		"archive_pass": rawString(body["archive_pass"]),
		"inner_path":   inner,
		"page":         1, "per_page": 0,
	}})
	if err != nil {
		relayFail(w, err)
		return
	}
	var list []archiveItem
	var wrap struct {
		Content []archiveItem `json:"content"`
	}
	if json.Unmarshal(data, &wrap) == nil && wrap.Content != nil {
		list = wrap.Content
	} else {
		_ = json.Unmarshal(data, &list)
	}
	items := make([]map[string]any, 0, len(list))
	for _, o := range list {
		items = append(items, mapArchiveItem(o, inner))
	}
	writeOK(w, map[string]any{"inner_path": inner, "items": items}, "")
}

// handleArchiveDecompress POST /archive/decompress：解压（大压缩包驱动回源耗时不可预估，不限超时）。
// 双口径入参（客户端兼容）：
//   - Node 口径：{space, path, dst_dir, archive_pass, put_into_new_dir, overwrite}——单包解压到
//     dst_dir，src_dir/name 由 path 派生（miniprogram 与 server/public/netdisk.html 用）；
//   - 批量口径：{src_space, src_dir, names, dst_space, dst_dir, inner_path, ...}（web 网盘用）。
func (m *Module) handleArchiveDecompress(w http.ResponseWriter, r *http.Request) {
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	body, err := decodeBodyMap(r)
	if err != nil {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误", nil)
		return
	}
	if _, hasPath := body["path"]; hasPath {
		m.decompressOne(w, r, ol, body)
		return
	}
	m.decompressMany(w, r, ol, body)
}

// decompressOne Node 口径：单包 {space, path, dst_dir} 解压。
func (m *Module) decompressOne(w http.ResponseWriter, r *http.Request, ol OpenListClient, body map[string]json.RawMessage) {
	root, ok := m.resolveRoot(w, r, rawString(body["space"]))
	if !ok {
		return
	}
	rel, ok1 := NormRel(rawString(body["path"]))
	dstDir, ok2 := NormRel(rawString(body["dst_dir"]))
	if !ok1 || !ok2 || rel == "/" {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误：path/dst_dir", nil)
		return
	}
	dstAbs := JoinPath(root, dstDir)
	_ = ol.EnsureDir(r.Context(), dstAbs) // 目的目录自愈（Node ensureDir 同口径）
	payload := map[string]any{
		"src_dir":      DirName(JoinPath(root, rel)),
		"dst_dir":      dstAbs,
		"name":         []string{BaseName(rel)},
		"inner_path":   "/",
		"archive_pass": rawString(body["archive_pass"]),
		"cache_full":   false,
		// Node：put_into_new_dir 仅字面 false 才关（字符串 "false" 不算关）；overwrite 取 JS 真值。
		"put_into_new_dir": !isJSONFalse(body["put_into_new_dir"]),
		"overwrite":        jsTruthy(body["overwrite"]),
	}
	data, err := ol.Call(r.Context(), http.MethodPost, "/api/fs/archive/decompress", CallOpts{Body: payload, Timeout: -1})
	if err != nil {
		relayFail(w, err)
		return
	}
	m.dirSize.invalidate() // 解压产出新文件/目录
	writeRawData(w, data)
}

// decompressMany 批量口径：{src_space/src_dir/names/dst_space/dst_dir/inner_path}（web 网盘用）。
func (m *Module) decompressMany(w http.ResponseWriter, r *http.Request, ol OpenListClient, body map[string]json.RawMessage) {
	srcSpace := rawString(body["src_space"])
	if srcSpace == "" {
		srcSpace = rawString(body["space"])
	}
	dstSpace := rawString(body["dst_space"])
	if dstSpace == "" {
		dstSpace = srcSpace
	}
	srcRoot, ok := m.resolveRoot(w, r, srcSpace)
	if !ok {
		return
	}
	dstRoot, ok := m.resolveRoot(w, r, dstSpace)
	if !ok {
		return
	}
	srcDir, ok1 := NormRel(rawString(body["src_dir"]))
	dstDir, ok2 := NormRel(rawString(body["dst_dir"]))
	if !ok1 || !ok2 {
		writeFail(w, http.StatusBadRequest, codeParam, "路径不合法", nil)
		return
	}
	inner, ok := NormRel(rawString(body["inner_path"]))
	if !ok {
		writeFail(w, http.StatusBadRequest, codeParam, "内层路径不合法", nil)
		return
	}
	var names []string
	if raw, exists := body["names"]; exists {
		_ = json.Unmarshal(raw, &names)
	}
	if len(names) == 0 || len(names) > 100 {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误：names（1-100 条）", nil)
		return
	}
	dstAbs := JoinPath(dstRoot, dstDir)
	_ = ol.EnsureDir(r.Context(), dstAbs) // 目的目录自愈
	payload := map[string]any{
		"src_dir":          JoinPath(srcRoot, srcDir),
		"dst_dir":          dstAbs,
		"name":             names,
		"inner_path":       inner,
		"archive_pass":     rawString(body["archive_pass"]),
		"cache_full":       false,
		"put_into_new_dir": rawString(body["put_into_new_dir"]) != "false",
		"overwrite":        rawString(body["overwrite"]) == "true",
	}
	data, err := ol.Call(r.Context(), http.MethodPost, "/api/fs/archive/decompress", CallOpts{Body: payload, Timeout: -1})
	if err != nil {
		relayFail(w, err)
		return
	}
	m.dirSize.invalidate() // 解压产出新文件/目录
	writeRawData(w, data)
}

// isJSONFalse 仅字面 false 判定（Node put_into_new_dir !== false 同口径）。
func isJSONFalse(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "false"
}

// jsTruthy JS 真值（Node overwrite !! 同口径：false/0/""/null 为假，其余为真）。
func jsTruthy(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return !(s == "" || s == "false" || s == "null" || s == "0" || s == `""`)
}

// handleArchiveDownload GET /archive/download：包内文件下载（inner 为包内路径）。
func (m *Module) handleArchiveDownload(w http.ResponseWriter, r *http.Request) {
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	root, ok := m.resolveRoot(w, r, q.Get("space"))
	if !ok {
		return
	}
	rel, ok := NormRel(q.Get("path"))
	if !ok || rel == "/" {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误：path/inner", nil)
		return
	}
	inner, ok := NormRel(q.Get("inner"))
	if !ok || inner == "/" {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误：path/inner", nil)
		return
	}
	rc, header, err := ol.ArchiveDownloadStream(r.Context(), JoinPath(root, rel), inner, q.Get("archive_pass"))
	if err != nil {
		relayFail(w, err)
		return
	}
	defer rc.Close()
	if v := header.Get("Content-Length"); v != "" {
		w.Header().Set("Content-Length", v)
	}
	if v := header.Get("Content-Type"); v != "" {
		w.Header().Set("Content-Type", v)
	}
	w.Header().Set("Content-Disposition", `attachment; filename*=UTF-8''`+url.PathEscape(BaseName(inner)))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

func decodeBase64Field(raw json.RawMessage) ([]byte, error) {
	return decodeBase64FieldLimited(raw, 0)
}

// decodeBase64FieldLimited 解码 base64 字段（maxBytes>0 时在解码前按编码长度限总量、
// 解码后再按实际字节数复核；超限返回 ErrFileTooLarge，绝不把超量内容交给上层）。
func decodeBase64FieldLimited(raw json.RawMessage, maxBytes int64) ([]byte, error) {
	s := rawString(raw)
	if s == "" {
		return []byte{}, nil
	}
	// 支持 dataURL 前缀。
	if i := strings.Index(s, "base64,"); i >= 0 {
		s = s[i+len("base64,"):]
	}
	s = strings.TrimSpace(s)
	if maxBytes > 0 {
		// base64 编码长度上限 = 4*ceil(maxBytes/3)；超出即拒绝，不进入解码分配。
		if int64(len(s)) > 4*((maxBytes+2)/3) {
			return nil, fmt.Errorf("%w（编码长度 %d）", ErrFileTooLarge, len(s))
		}
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w（%d > %d 字节）", ErrFileTooLarge, len(data), maxBytes)
	}
	return data, nil
}
