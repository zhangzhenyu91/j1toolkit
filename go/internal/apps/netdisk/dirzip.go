package netdisk

import (
	"archive/zip"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// pipeDirZip 目录 ZIP 流式打包下载：逐层读取 OpenList 目录、逐文件流式写入，
// 保留目录层级与空文件夹；不整读文件入内存、不在网盘侧生成临时压缩包。
func (m *Module) pipeDirZip(w http.ResponseWriter, r *http.Request, ol OpenListClient, absRoot, rootName string) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename*=UTF-8''`+url.PathEscape(rootName+".zip"))
	w.WriteHeader(http.StatusOK)
	zw := zip.NewWriter(w)
	defer zw.Close()
	m.zipWalk(r, ol, zw, absRoot, rootName)
}

func (m *Module) zipWalk(r *http.Request, ol OpenListClient, zw *zip.Writer, abs, entry string) {
	data, err := ol.Call(r.Context(), http.MethodPost, "/api/fs/list", CallOpts{
		Body: map[string]any{"path": abs, "password": "", "page": 1, "per_page": 0, "refresh": false},
	})
	if err != nil {
		return
	}
	items := parseItems(data)
	if len(items) == 0 {
		// 空目录也保留条目（zip 目录项以 / 结尾）。
		if entry != "" {
			_, _ = zw.Create(entry + "/")
		}
		return
	}
	for _, item := range items {
		name := zipSegment(item.Name)
		if name == "" {
			continue
		}
		childEntry := entry + "/" + name
		if item.IsDir {
			m.zipWalk(r, ol, zw, abs+"/"+item.Name, childEntry)
			continue
		}
		rc, _, err := ol.DownloadStream(r.Context(), abs+"/"+item.Name)
		if err != nil {
			continue
		}
		fw, err := zw.Create(childEntry)
		if err != nil {
			_ = rc.Close()
			continue
		}
		_, _ = io.Copy(fw, rc)
		_ = rc.Close()
	}
}

// zipSegment ZIP 内单个路径段：拒绝分隔符/控制符与 .、..，防路径穿越与异常条目。
func zipSegment(name string) string {
	n := name
	if n == "" || n == "." || n == ".." || strings.ContainsAny(n, "/\\\r\n") {
		return ""
	}
	if i := strings.Index(n, ":"); i > 0 && i <= 3 && !strings.ContainsAny(n[:i], `/\\`) {
		return "" // 拒绝 ^\w+: 前缀（Windows 盘符式条目）
	}
	return n
}

// ---- 压缩包预览 / 解压 ----

// handleArchiveMeta POST /archive/meta：压缩包元信息（encrypted=true 时前端弹压缩密码框）。
func (m *Module) handleArchiveMeta(w http.ResponseWriter, r *http.Request) {
	m.archiveCall(w, r, "/api/fs/archive/meta", func(body map[string]json.RawMessage, abs string) map[string]any {
		return map[string]any{"path": abs, "password": "", "refresh": false, "archive_pass": rawString(body["archive_pass"])}
	})
}

// handleArchiveList POST /archive/list：按 inner_path 平铺列内层目录。
func (m *Module) handleArchiveList(w http.ResponseWriter, r *http.Request) {
	m.archiveCall(w, r, "/api/fs/archive/list", func(body map[string]json.RawMessage, abs string) map[string]any {
		return map[string]any{
			"path": abs, "password": "", "refresh": false,
			"archive_pass": rawString(body["archive_pass"]),
			"inner_path":   firstNonEmpty(rawString(body["inner_path"]), "/"),
			"page":         1, "per_page": 0,
		}
	})
}

// handleArchiveDecompress POST /archive/decompress：解压（大压缩包驱动回源耗时不可预估，不限超时）。
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
	var names []string
	if raw, exists := body["names"]; exists {
		_ = json.Unmarshal(raw, &names)
	}
	if len(names) == 0 || len(names) > 100 {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误：names（1-100 条）", nil)
		return
	}
	payload := map[string]any{
		"src_dir":          JoinPath(srcRoot, srcDir),
		"dst_dir":          JoinPath(dstRoot, dstDir),
		"name":             names,
		"inner_path":       firstNonEmpty(rawString(body["inner_path"]), "/"),
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
	writeRawData(w, data)
}

// handleArchiveDownload GET /archive/download：包内文件下载。
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
		writeFail(w, http.StatusBadRequest, codeParam, "路径不合法", nil)
		return
	}
	inner := firstNonEmpty(q.Get("inner"), "/")
	name := BaseName(inner)
	if name == "" {
		name = BaseName(rel)
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
	w.Header().Set("Content-Disposition", `attachment; filename*=UTF-8''`+url.PathEscape(name))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

// archiveCall 压缩包查询类共用骨架。
func (m *Module) archiveCall(w http.ResponseWriter, r *http.Request, api string, build func(body map[string]json.RawMessage, abs string) map[string]any) {
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
	abs := JoinPath(root, rel)
	data, err := ol.Call(r.Context(), http.MethodPost, api, CallOpts{Body: build(body, abs), Timeout: -1})
	if err != nil {
		relayFail(w, err)
		return
	}
	writeRawData(w, data)
}

func decodeBase64Field(raw json.RawMessage) ([]byte, error) {
	s := rawString(raw)
	if s == "" {
		return []byte{}, nil
	}
	// 支持 dataURL 前缀。
	if i := strings.Index(s, "base64,"); i >= 0 {
		s = s[i+len("base64,"):]
	}
	return base64.StdEncoding.DecodeString(strings.TrimSpace(s))
}
