package netdisk

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// 分片上传（OpenList multipart 协议）：init / chunk / complete / status / abort；
// 0 字节文件直接走 put 建成；429 窗口满 / 409 分片冲突带 retry 标记由前端退避重传。

// uploadIDRe 会话 id（Node /i 口径：十六进制与连字符，大小写不敏感）。
var uploadIDRe = regexp.MustCompile(`(?i)^[0-9a-f-]{16,64}$`)

// chunkStream 分片流式读取器：边读边转给 OpenList，同时限制 64MB 上限。
type chunkStream struct {
	r         io.Reader
	remaining int64
	read      int64
	overflow  bool
}

var errChunkTooLarge = errors.New("chunk too large")

func (c *chunkStream) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		c.overflow = true
		return 0, errChunkTooLarge
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.r.Read(p)
	c.remaining -= int64(n)
	c.read += int64(n)
	if c.read > 64<<20 {
		c.overflow = true
	}
	return n, err
}

// handleUploadInit POST /upload/init：初始化（0 字节文件直接 put 建成）。
func (m *Module) handleUploadInit(w http.ResponseWriter, r *http.Request) {
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
	name := ValidName(rawString(body["name"]))
	size := int64(-1)
	if v := rawString(body["size"]); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			size = n
		}
	}
	if !ok || name == "" || size < 0 {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误：path/name/size", nil)
		return
	}
	if size > m.maxUploadBytes() {
		writeFail(w, http.StatusRequestEntityTooLarge, codeTooLarge,
			"单文件最大 "+strconv.FormatInt(m.maxUploadBytes()>>20, 10)+" MB", nil)
		return
	}
	abs := JoinPath(root, rel) + "/" + name
	// Web 暂存接收只写 Shade 本地盘；OpenList 配置、目录与可用性均由后台同步处理。
	if size > 0 && strings.EqualFold(strings.TrimSpace(rawString(body["staged"])), "true") {
		ident, ok := identityFrom(r.Context())
		if !ok {
			writeFail(w, http.StatusUnauthorized, codeBadTicket, "未登录或登录已过期", nil)
			return
		}
		st, err := m.stageMgr().findOrCreate(m.OL, ident.ID, root, rel, name, abs, size)
		if err != nil {
			writeFail(w, http.StatusInternalServerError, codeInternal, "创建暂存上传失败", nil)
			return
		}
		writeOK(w, st.snapshot(), "")
		return
	}
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	if err := ol.EnsureDir(r.Context(), JoinPath(root, rel)); err != nil {
		relayFail(w, err)
		return
	}
	if size == 0 {
		if err := ol.PutEmpty(r.Context(), abs); err != nil {
			relayFail(w, err)
			return
		}
		m.dirSize.invalidate()
		writeOK(w, map[string]any{"state": "completed", "instant": true}, "")
		return
	}
	// X-Chunk-Size：请求 4MB 分片（OpenList 会夹在 [1MB, 管理端上限]，以响应 chunk_size 为准）。
	// 4MB 让进度更细、重传代价更低，也降低小程序弱网下 10MB 单片超时风险。
	data, err := ol.Call(r.Context(), http.MethodPost, "/api/fs/multipart/init", CallOpts{
		Headers: map[string]string{
			"File-Path":    encodeURIPath(abs),
			"X-File-Size":  strconv.FormatInt(size, 10),
			"X-Chunk-Size": strconv.FormatInt(4<<20, 10),
		},
		Timeout: -1,
	})
	if err != nil {
		relayFail(w, err)
		return
	}
	writeRawData(w, data)
}

// handleUploadChunk PUT /upload/chunk：传一片（裸字节中转）。
func (m *Module) handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	uploadID := strings.TrimSpace(r.Header.Get("X-Upload-Id"))
	idx, idxOK := AtoiLenient(r.Header.Get("X-Chunk-Index"))
	if !uploadIDRe.MatchString(uploadID) || !idxOK || idx < 0 {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误：X-Upload-Id / X-Chunk-Index", nil)
		return
	}
	// Node express.raw(type: application/octet-stream) 口径：Content-Type 非 octet-stream 不收。
	if ct := r.Header.Get("Content-Type"); ct == "" {
		writeFail(w, http.StatusBadRequest, codeParam, "分片内容为空或 Content-Type 非 application/octet-stream", nil)
		return
	} else if base, _, _ := strings.Cut(ct, ";"); !strings.EqualFold(strings.TrimSpace(base), "application/octet-stream") {
		writeFail(w, http.StatusBadRequest, codeParam, "分片内容为空或 Content-Type 非 application/octet-stream", nil)
		return
	}
	// 分片流式中转：浏览器 → Shade → OpenList 两跳直接对接，不先整读 4MB 再发；
	// 上限 64MB 与 Node express.raw 同口径，超限在读流中截断并回 413。
	if r.ContentLength == 0 {
		writeFail(w, http.StatusBadRequest, codeParam, "分片内容为空或 Content-Type 非 application/octet-stream", nil)
		return
	}
	if r.ContentLength > 64<<20 {
		writeFail(w, http.StatusRequestEntityTooLarge, codeTooLarge, "分片内容超限（64 MB）", nil)
		return
	}
	if ident, ok := identityFrom(r.Context()); ok {
		if st := m.stageMgr().lookup(ident.ID, uploadID); st != nil {
			m.handleStagedChunk(w, r, st, idx)
			return
		}
	}
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	cs := &chunkStream{r: r.Body, remaining: 64<<20 + 1}
	data, err := ol.Call(r.Context(), http.MethodPut, "/api/fs/multipart/chunk", CallOpts{
		Headers: map[string]string{
			"X-Upload-Id":   uploadID,
			"X-Chunk-Index": strconv.Itoa(idx),
			"Content-Type":  "application/octet-stream",
		},
		Body:          cs,
		ContentLength: r.ContentLength,
		Timeout:       -1,
	})
	if cs.overflow || cs.read > 64<<20 {
		writeFail(w, http.StatusRequestEntityTooLarge, codeTooLarge, "分片内容超限（64 MB）", nil)
		return
	}
	if err == nil && cs.read == 0 {
		writeFail(w, http.StatusBadRequest, codeParam, "分片内容为空或 Content-Type 非 application/octet-stream", nil)
		return
	}
	if err != nil {
		var ue *UpstreamError
		if asUpstream(err, &ue) {
			switch ue.State {
			case "failed_retriable":
				writeFail(w, http.StatusConflict, codeConflict, "分片上传会话失败，需从第 0 片重传",
					map[string]any{"restart": true, "state": ue.State})
				return
			case "failed_permanent":
				writeFail(w, http.StatusBadGateway, codeUpstream, "分片上传会话已失败："+ue.Message,
					map[string]any{"restart": false, "state": ue.State})
				return
			}
			switch ue.UpstreamCode {
			case 429:
				writeFail(w, http.StatusTooManyRequests, codeRateLimited, "网盘接收窗口已满，请稍后重试", map[string]any{"retry": true})
				return
			case 409:
				writeFail(w, http.StatusConflict, codeConflict, "分片上传冲突，请重试", map[string]any{"retry": true})
				return
			}
		}
		relayFail(w, err)
		return
	}
	writeRawData(w, data)
}

// handleUploadComplete POST /upload/complete：合并（阻塞到驱动落盘终态）。
func (m *Module) handleUploadComplete(w http.ResponseWriter, r *http.Request) {
	var body map[string]json.RawMessage
	_ = decodeBody(r, &body)
	uploadID := rawString(body["upload_id"])
	if !uploadIDRe.MatchString(uploadID) {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误：upload_id", nil)
		return
	}
	if ident, ok := identityFrom(r.Context()); ok {
		if st := m.stageMgr().lookup(ident.ID, uploadID); st != nil {
			m.handleStagedComplete(w, st)
			return
		}
	}
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	data, err := ol.Call(r.Context(), http.MethodPost, "/api/fs/multipart/complete", CallOpts{
		Headers: map[string]string{"X-Upload-Id": uploadID},
		Timeout: -1,
	})
	if err != nil {
		var ue *UpstreamError
		if asUpstream(err, &ue) {
			switch ue.State {
			case "failed_retriable":
				writeFail(w, http.StatusConflict, codeConflict, "网盘写入失败，需从第 0 片重传",
					map[string]any{"restart": true, "state": ue.State})
				return
			case "failed_permanent":
				writeFail(w, http.StatusBadGateway, codeUpstream, "网盘写入失败："+ue.Message,
					map[string]any{"restart": false, "state": ue.State})
				return
			}
		}
		relayFail(w, err)
		return
	}
	m.dirSize.invalidate()
	writeRawData(w, data)
}

// handleUploadStatus GET /upload/status：会话查询（upload_id，或 space+path+name+size 找回断线会话）。
func (m *Module) handleUploadStatus(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if uploadID := strings.TrimSpace(q.Get("upload_id")); uploadID != "" {
		if !uploadIDRe.MatchString(uploadID) {
			writeFail(w, http.StatusBadRequest, codeParam, "参数错误：upload_id", nil)
			return
		}
		if ident, ok := identityFrom(r.Context()); ok {
			if st := m.stageMgr().lookup(ident.ID, uploadID); st != nil {
				writeOK(w, st.snapshot(), "")
				return
			}
		}
		ol, ok := m.requireOL(w)
		if !ok {
			return
		}
		data, err := ol.Call(r.Context(), http.MethodGet, "/api/fs/multipart/status", CallOpts{Query: map[string]string{"upload_id": uploadID}})
		if err != nil {
			relayFail(w, err)
			return
		}
		writeRawData(w, data)
		return
	}
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	root, ok := m.resolveRoot(w, r, q.Get("space"))
	if !ok {
		return
	}
	rel, ok := NormRel(q.Get("path"))
	name := ValidName(q.Get("name"))
	size, sizeOK := AtoiLenient(q.Get("size"))
	if !ok || name == "" || !sizeOK || size <= 0 {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误：path/name/size", nil)
		return
	}
	data, err := ol.Call(r.Context(), http.MethodGet, "/api/fs/multipart/status", CallOpts{
		Query: map[string]string{"path": JoinPath(root, rel) + "/" + name, "size": strconv.Itoa(size)},
	})
	if err != nil {
		relayFail(w, err)
		return
	}
	writeRawData(w, data)
}

// handleUploadAbort POST /upload/abort：取消。
func (m *Module) handleUploadAbort(w http.ResponseWriter, r *http.Request) {
	var body map[string]json.RawMessage
	_ = decodeBody(r, &body)
	uploadID := rawString(body["upload_id"])
	if !uploadIDRe.MatchString(uploadID) {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误：upload_id", nil)
		return
	}
	if ident, ok := identityFrom(r.Context()); ok {
		if st := m.stageMgr().lookup(ident.ID, uploadID); st != nil {
			m.handleStagedAbort(w, st)
			return
		}
	}
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	data, err := ol.Call(r.Context(), http.MethodPost, "/api/fs/multipart/abort", CallOpts{
		Headers: map[string]string{"X-Upload-Id": uploadID},
	})
	if err != nil {
		relayFail(w, err)
		return
	}
	writeRawData(w, data)
}

// handleSaveToNetdisk POST /save：「保存到网盘」共享通道（服务端侧文件写入网盘）。
// body {space?, dir, name, data(base64)}；落点 <空间>/<dir>/<name>，目录自动创建。
func (m *Module) handleSaveToNetdisk(w http.ResponseWriter, r *http.Request) {
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	// JSON body 在解析前按 base64 编码总量限流：避免无关字段把请求体放大后再拒 data。
	r.Body = http.MaxBytesReader(w, r.Body, m.saveBodyLimit())
	body, err := decodeBodyMap(r)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeFail(w, http.StatusRequestEntityTooLarge, codeTooLarge,
				"单文件最大 "+strconv.FormatInt(m.maxUploadBytes()>>20, 10)+" MB", nil)
			return
		}
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误", nil)
		return
	}
	sp := rawString(body["space"])
	if sp == "" {
		sp = "my"
	}
	root, ok := m.resolveRoot(w, r, sp)
	if !ok {
		return
	}
	rel, ok := SanitizeRelDir(rawString(body["dir"]))
	if !ok {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误：dir", nil)
		return
	}
	name := SanitizeName(rawString(body["name"]))
	if name == "" {
		writeFail(w, http.StatusBadRequest, codeParam, "文件名不合法", nil)
		return
	}
	data, err := decodeBase64FieldLimited(body["data"], m.maxUploadBytes())
	if err != nil {
		if errors.Is(err, ErrFileTooLarge) {
			writeFail(w, http.StatusRequestEntityTooLarge, codeTooLarge,
				"单文件最大 "+strconv.FormatInt(m.maxUploadBytes()>>20, 10)+" MB", nil)
			return
		}
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误：data", nil)
		return
	}
	target := root
	if rel != "" {
		target = root + "/" + rel
	}
	if err := ol.EnsureDir(r.Context(), target); err != nil {
		relayFail(w, err)
		return
	}
	if err := ol.FsPut(r.Context(), target+"/"+name, strings.NewReader(string(data)), int64(len(data))); err != nil {
		relayFail(w, err)
		return
	}
	m.dirSize.invalidate()
	label := "我的空间"
	if sp == "public" {
		label = "公共区"
	}
	path := label + "/" + name
	if rel != "" {
		path = label + "/" + rel + "/" + name
	}
	writeOK(w, map[string]any{"path": path}, "已保存")
}

// writeRawData 把 OpenList data 载荷包成统一信封下发。
func writeRawData(w http.ResponseWriter, data json.RawMessage) {
	if len(data) == 0 {
		data = json.RawMessage("null")
	}
	writeJSON(w, http.StatusOK, Envelope{Code: codeOK, Message: "ok", Data: json.RawMessage(data)})
}

func asUpstream(err error, target **UpstreamError) bool {
	ue, ok := err.(*UpstreamError)
	if ok {
		*target = ue
	}
	return ok
}
