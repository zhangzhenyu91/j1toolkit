// 出工日志：SQL 占位符 / JSON 解析 / 值归一等共享小工具。
package worklog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"shade/internal/outbound"
)

// monthRange 月份（YYYY-MM）转半开日期范围 [月初, 次月初)，让 log_date 走索引。
func monthRange(month string) (string, string) {
	t, err := time.Parse("2006-01", month)
	if err != nil {
		return month + "-01", month + "-01"
	}
	return t.Format("2006-01-02"), t.AddDate(0, 1, 0).Format("2006-01-02")
}

// placeholders 生成 n 个逗号分隔的 ? 占位符。
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

// idsToAny int64 切片转 any 切片（IN 列表参数）。
func idsToAny(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// strsToAny string 切片转 any 切片（IN 列表参数）。
func strsToAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// jsonUnmarshal 包一层，便于测试桩替换（当前直接 json.Unmarshal）。
func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

// splitJSONObjects 把 JSON 数组文本拆成逐个对象文本（备注附件宽松解析用）。
func splitJSONObjects(raw string) []string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	out := make([]string, 0)
	depth := 0
	start := -1
	inStr := false
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if esc {
			esc = false
			continue
		}
		if c == '\\' && inStr {
			esc = true
			continue
		}
		if c == '"' {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		switch c {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && start >= 0 {
				out = append(out, s[start:i+1])
				start = -1
			}
		}
	}
	return out
}

// normalizeSQLValue 把 database/sql 扫描出的 []byte 归一为 string。
func normalizeSQLValue(v any) any {
	switch t := v.(type) {
	case []byte:
		return string(t)
	default:
		return v
	}
}

// boolToInt 布尔转 0/1。
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// dedupIDs 去重保序。
func dedupIDs(ids []int64) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// readUploadFile 读取 multipart 字段 file（单文件，超限/缺文件写响应并 ok=false）。
// 解析前用 MaxBytesReader 限制请求体总量（maxBytes + 表单开销），避免大请求整体先落盘。
func readUploadFile(w http.ResponseWriter, r *http.Request, maxBytes int64) (data []byte, name string, ok bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+1<<20)
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		fail(w, http.StatusBadRequest, codeFile, "文件大小应在限制以内")
		return nil, "", false
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		fail(w, http.StatusBadRequest, codeFile, "请选择要上传的文件")
		return nil, "", false
	}
	defer file.Close()
	if header.Size > maxBytes {
		fail(w, http.StatusBadRequest, codeFile, "文件大小应在限制以内")
		return nil, "", false
	}
	buf := make([]byte, 0, header.Size)
	tmp := make([]byte, 32*1024)
	for {
		n, err := file.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if int64(len(buf)) > maxBytes {
			fail(w, http.StatusBadRequest, codeFile, "文件大小应在限制以内")
			return nil, "", false
		}
		if err != nil {
			break
		}
	}
	if len(buf) == 0 {
		fail(w, http.StatusBadRequest, codeFile, "请选择要上传的文件")
		return nil, "", false
	}
	return buf, header.Filename, true
}

// encURI encodeURIComponent 同口径百分号编码（RFC3986 unreserved 之外全转义，含 + 与空格）。
func encURI(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '!' || c == '~' || c == '*' || c == '\'' || c == '(' || c == ')' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0F])
	}
	return b.String()
}

// getFileExt 小写扩展名（不含点；无扩展名返回空串）。
func getFileExt(name string) string {
	idx := strings.LastIndex(name, ".")
	if idx == -1 {
		return ""
	}
	return strings.ToLower(name[idx+1:])
}

// fixLatin1Name multipart 中文文件名修正：客户端按 UTF-8 原始字节发送、网关按 latin1 误解码时转回 UTF-8；
// 转换结果非法则原样返回。
func fixLatin1Name(name string) string {
	if name == "" {
		return ""
	}
	raw := make([]byte, 0, len(name))
	for _, r := range name {
		if r > 0xFF {
			return name
		}
		raw = append(raw, byte(r))
	}
	for i := 0; i < len(raw); {
		r, size := utf8.DecodeRune(raw[i:])
		if r == 0xFFFD && size == 1 {
			return name
		}
		i += size
	}
	return string(raw)
}

// nowMillis 当前毫秒时间戳（COS 键命名用）。
func nowMillis() int64 { return time.Now().UnixMilli() }

// readAllLimit 读尽 r（至多 maxBytes）；超限返回错误。
func readAllLimit(r io.Reader, maxBytes int64) ([]byte, error) {
	buf, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > maxBytes {
		return nil, errors.New("超出大小限制")
	}
	return buf, nil
}

// fetchObjectBytes 取对象字节：优先对象存储 GetBuffer（按 cos_key），无则 HTTP 拉取公开地址。
// URL 兜底同样有字节预算（objectFetchMaxBytes），超限显式报错不返回半截内容。
func fetchObjectBytes(ctx context.Context, store ObjectStore, cosKey, url string) ([]byte, error) {
	if store != nil && cosKey != "" {
		if g, ok := store.(interface {
			GetBuffer(ctx context.Context, key string) ([]byte, error)
		}); ok {
			buf, err := g.GetBuffer(ctx, cosKey)
			if err == nil && len(buf) > 0 {
				return buf, nil
			}
			if err != nil {
				// 回源失败（超预算/非 2xx/未知网络失败）一律终止：不再回退公网 URL 二次请求。
				// 二次回源会放大大文件、掩盖真实错误，也让超预算/状态码语义不可辨识；
				// 只有 GetBuffer 成功但返回空内容（历史数据无字节）才继续用库内 URL 兜底。
				return nil, err
			}
		}
	}
	if url == "" {
		return nil, errors.New("对象地址为空")
	}
	ctx = outbound.WithServiceName(ctx, "cos")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return readAllLimit(resp.Body, objectFetchMaxBytes)
}

// objectFetchMaxBytes URL 兜底回源单对象字节预算（默认 64MiB；测试可下调）。
var objectFetchMaxBytes int64 = 64 << 20

// fetchConcurrency 批量回源并发度（ZIP 打包 / 组合 PDF 取对象共用）。
const fetchConcurrency = 6

// fetchObjectsOrdered 有界并发回源并按输入序交付：每批最多 workers 张并发取回，
// 每张取回完成即调 onProgress(done, total)（内部串行化、done 严格递增，成功失败均计），
// 批齐后按输入序调 yield(i, buf, err)。内存峰值 ≈ 一批照片 + 产物；
// onProgress / yield 均可为 nil，workers < 1 时退化为串行。
func fetchObjectsOrdered(ctx context.Context, store ObjectStore, refs []photoRef, workers int, onProgress func(done, total int), yield func(i int, buf []byte, err error)) {
	if workers < 1 {
		workers = 1
	}
	total := len(refs)
	done := 0
	var progressMu sync.Mutex
	for start := 0; start < total; start += workers {
		if ctx.Err() != nil {
			break // 已取消（预算超限/客户端断开）：停止后续批次回源
		}
		end := start + workers
		if end > total {
			end = total
		}
		batch := refs[start:end]
		bufs := make([][]byte, len(batch))
		errs := make([]error, len(batch))
		var wg sync.WaitGroup
		for j := range batch {
			wg.Add(1)
			go func(j int) {
				defer wg.Done()
				bufs[j], errs[j] = fetchObjectBytes(ctx, store, batch[j].cosKey, batch[j].url)
				if onProgress != nil {
					progressMu.Lock()
					done++
					onProgress(done, total)
					progressMu.Unlock()
				}
			}(j)
		}
		wg.Wait()
		if yield != nil {
			for j := range batch {
				yield(start+j, bufs[j], errs[j])
			}
		}
	}
}
