package chatlog

// POST /api/v1/chatlog/ingest 每日归档直推（crane-answer 用，凭 X-Ingest-Token 免登录）：
// 入参校验 → /{yyyy}/{M.d}/ 文件夹幂等落库（同名先删后传、409 自愈）。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"shade/internal/apps/callme"
)

// 摄取渠道标记（列表过滤 source=，树障/交跨渠道清理只认各自渠道，互不干扰）。
const channelChatlog = "chatlog"

// fileMaxBytes 单文件上限（base64 纯内容解码后）。
const fileMaxBytes = 30 * 1024 * 1024

var (
	dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	b64Re  = regexp.MustCompile(`^[A-Za-z0-9+/]*={0,2}$`)
)

// fileExts 文档扩展名白名单（不含点小写；WeKnora 知识库仅支持文档类摄取——
// 与 callme 附件白名单的文档类子集对齐，不含音视频与图片）。
var fileExts = map[string]bool{
	"pdf": true, "doc": true, "docx": true, "ppt": true, "pptx": true,
	"xlsx": true, "xls": true, "epub": true, "mhtml": true, "csv": true,
	"md": true, "markdown": true, "txt": true, "json": true, "html": true,
}

// ingestResponse 直推响应契约 {ok, code?, message?, data?}（同 callme /ingest-chat 口径，不走统一信封）。
type ingestResponse struct {
	OK      bool           `json:"ok"`
	Code    string         `json:"code,omitempty"`
	Message string         `json:"message,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

func writeIngest(w http.ResponseWriter, status int, resp ingestResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

func ingestFail(w http.ResponseWriter, status int, code, message string) {
	writeIngest(w, status, ingestResponse{OK: false, Code: code, Message: message})
}

// ingestItem 校验通过的单件入参。
type ingestItem struct {
	date      string
	groupName string
	kind      string
	fileName  string
	buf       []byte
}

// parseErr 入参/类型错误（code/message/httpStatus）。
type parseErr struct {
	code       string
	message    string
	httpStatus int
}

// strField 取字符串字段（同 JS String(b.x ?? ”)：非字符串强转字符串）。
func strField(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// parseBody 入参校验（对齐 server/src/chatlog/index.js parseBody）：合法返回 *ingestItem，
// 非法返回 *parseErr（文档类型不支持为 HTTP 200 + unsupported_file，其余 400）。
func parseBody(body map[string]any) (*ingestItem, *parseErr) {
	bad := func(code, message string, status int) (*ingestItem, *parseErr) {
		return nil, &parseErr{code: code, message: message, httpStatus: status}
	}

	date := strings.TrimSpace(strField(body, "date"))
	if !dateRe.MatchString(date) {
		return bad("param_invalid", "date 应为 yyyy-MM-dd", http.StatusBadRequest)
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return bad("param_invalid", "date 不是有效日期", http.StatusBadRequest)
	}
	groupName := strings.TrimSpace(strField(body, "group_name"))
	if groupName == "" {
		return bad("param_invalid", "group_name 不能为空", http.StatusBadRequest)
	}

	kind := strings.TrimSpace(strField(body, "kind"))
	if kind == "digest" {
		digestMd, _ := body["digest_md"].(string) // 非字符串视为空（同 JS typeof 口径）
		if strings.TrimSpace(digestMd) == "" {
			return bad("param_invalid", "digest_md 不能为空", http.StatusBadRequest)
		}
		// 纪要：{群名}_{date}.md（群名净化非法字符）
		return &ingestItem{
			date: date, groupName: groupName, kind: kind,
			fileName: safeFileName(groupName) + "_" + date + ".md",
			buf:      []byte(digestMd),
		}, nil
	}
	if kind == "file" {
		f, _ := body["file"].(map[string]any)
		name := ""
		if f != nil {
			if s, ok := f["name"].(string); ok {
				name = strings.TrimSpace(s)
			}
		}
		if name == "" {
			return bad("param_invalid", "file.name 不能为空", http.StatusBadRequest)
		}
		// 扩展名白名单（不含点小写；无扩展名同样拒收）；业务拒绝口径同 callme /ingest-chat：HTTP 200 + ok:false
		if !fileExts[fileExt(name)] {
			return bad("unsupported_file", "该文件类型暂不支持入知识库", http.StatusOK)
		}
		b64 := ""
		if f != nil {
			if s, ok := f["data_base64"].(string); ok {
				b64 = s
			}
		}
		if strings.TrimSpace(b64) == "" {
			return bad("param_invalid", "file.data_base64 不能为空", http.StatusBadRequest)
		}
		compact := strings.Join(strings.Fields(b64), "")
		if !b64Re.MatchString(compact) || len(compact)%4 != 0 {
			return bad("param_invalid",
				"file.data_base64 应为纯内容 base64（无前缀、去空白后长度须为 4 的倍数）", http.StatusBadRequest)
		}
		buf, err := base64.StdEncoding.DecodeString(compact)
		if err != nil {
			return bad("param_invalid",
				"file.data_base64 应为纯内容 base64（无前缀、去空白后长度须为 4 的倍数）", http.StatusBadRequest)
		}
		if len(buf) == 0 {
			return bad("param_invalid", "file.data_base64 解码为空", http.StatusBadRequest)
		}
		if len(buf) > fileMaxBytes {
			return bad("file_too_large",
				fmt.Sprintf("单文件大小应在 30MB 以内（解码后 %d 字节）", len(buf)), http.StatusBadRequest)
		}
		// 文档用原名（仅净化非法字符）
		return &ingestItem{
			date: date, groupName: groupName, kind: kind,
			fileName: safeFileName(name), buf: buf,
		}, nil
	}
	return bad("unsupported_kind", "kind 应为 digest 或 file", http.StatusBadRequest)
}

// safeFileName 群名/文档名净化非法字符（同 treehazard export.safeFileName 口径；含 / 防路径逃逸出当天文件夹）。
func safeFileName(name string) string {
	s := strings.TrimSpace(strings.NewReplacer(
		`\`, "_", "/", "_", ":", "_", "*", "_", "?", "_", `"`, "_", "<", "_", ">", "_", "|", "_",
	).Replace(name))
	b := make([]rune, 0, len(s))
	for _, r := range s {
		if r < 32 {
			b = append(b, '_')
		} else {
			b = append(b, r)
		}
	}
	out := string(b)
	if out == "" {
		return "未命名"
	}
	return out
}

// folderSegments date（yyyy-MM-dd）→ 文件夹两级名 ['yyyy', 'M.d']（月.日单层不带前导零，
// 如 2026-10-23 → ['2026','10.23']）。
func folderSegments(date string) [2]string {
	var y, mo, d int
	_, _ = fmt.Sscanf(date, "%d-%d-%d", &y, &mo, &d)
	return [2]string{strconv.Itoa(y), fmt.Sprintf("%d.%d", mo, d)}
}

// renameWithSuffix 同名追加序号后缀（插在扩展名前；无扩展名直接尾部追加）。
func renameWithSuffix(name string, n int) string {
	dot := strings.LastIndexByte(name, '.')
	if dot > 0 {
		return name[:dot] + "-" + strconv.Itoa(n) + name[dot:]
	}
	return name + "-" + strconv.Itoa(n)
}

// fileExt 扩展名提取（不含点、小写）。
func fileExt(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i < 0 || i == len(name)-1 {
		return ""
	}
	return strings.ToLower(name[i+1:])
}

// conflictIDs 上传 409 时取响应已有条目 id。
func conflictIDs(err error) []string {
	var ce *callme.KnowledgeConflictError
	if errors.As(err, &ce) {
		return ce.IDs
	}
	return nil
}

// upsertResult 幂等写入结果。
type upsertResult struct {
	FileName    string
	KnowledgeID string
	Replaced    int
}

// upsertFile 同文件夹同名文档先删后传（幂等重跑/补跑覆盖当天内容）；上传仍 409 → 取响应已有 id
// 删除后重试一次；再冲突（并发极端场景）追加「-1」后缀改名再传。
func upsertFile(ctx context.Context, kb KBClient, kbID, folderPath, fileName string, buf []byte) (upsertResult, error) {
	listed, err := kb.ListFolderKnowledge(ctx, kbID, folderPath)
	if err != nil {
		return upsertResult{}, fmt.Errorf("列知识失败：%w", err)
	}
	// 先在文件夹内查重（删除异步，「正在删除的同名文件不计为重复」，删后即传不会 409）
	seen := map[string]bool{}
	var dupIDs []string
	for _, it := range listed {
		if it.Name == fileName && it.ID != "" && !seen[it.ID] {
			seen[it.ID] = true
			dupIDs = append(dupIDs, it.ID)
		}
	}
	for _, id := range dupIDs {
		if err := kb.DeleteKnowledge(ctx, id); err != nil {
			return upsertResult{}, fmt.Errorf("删同名旧知识失败：%w", err)
		}
	}
	upload := func(name string) (string, error) {
		return kb.UploadKnowledgeFile(ctx, kbID, folderPath+"/"+name, name, channelChatlog, buf)
	}
	if id, err := upload(fileName); err == nil {
		return upsertResult{FileName: fileName, KnowledgeID: id, Replaced: len(dupIDs)}, nil
	} else if ids := conflictIDs(err); len(ids) == 0 {
		return upsertResult{}, err
	} else {
		for _, cid := range ids {
			if err := kb.DeleteKnowledge(ctx, cid); err != nil {
				return upsertResult{}, fmt.Errorf("删同名旧知识失败：%w", err)
			}
		}
		if id2, err2 := upload(fileName); err2 == nil {
			return upsertResult{FileName: fileName, KnowledgeID: id2, Replaced: len(dupIDs) + len(ids)}, nil
		} else if conflictIDs(err2) == nil {
			return upsertResult{}, err2
		} else {
			// 再冲突（并发极端场景）追加「-1」后缀改名再传
			renamed := renameWithSuffix(fileName, 1)
			id3, err3 := upload(renamed)
			if err3 != nil {
				return upsertResult{}, err3
			}
			return upsertResult{FileName: renamed, KnowledgeID: id3, Replaced: len(dupIDs) + len(ids)}, nil
		}
	}
}

// handleIngest POST /ingest 每日归档直推（鉴权凭 X-Ingest-Token 在本路由完成；
// 薄适配：密钥校验 + 编解码，业务核 ingestBody 与直调共用）。
func (m *Module) handleIngest(w http.ResponseWriter, r *http.Request) {
	if !m.checkIngestToken(w, r) {
		return
	}
	rawBody, _ := io.ReadAll(io.LimitReader(r.Body, 60<<20)) // 单文件 30MB base64 后约 41MB
	var body map[string]any
	if err := json.Unmarshal(rawBody, &body); err != nil {
		ingestFail(w, http.StatusBadRequest, "param_invalid", "请求体不是合法 JSON")
		return
	}
	data, ierr := m.ingestBody(r.Context(), body)
	if ierr != nil {
		ingestFail(w, ierr.Status, ierr.Code, ierr.Message)
		return
	}
	writeIngest(w, http.StatusOK, ingestResponse{OK: true, Data: data})
}
