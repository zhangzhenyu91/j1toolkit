package treehazard

// GET /export/preview?line=&split=&team_id=：统计表在线预览地址（与 server/src/treehazard/index.js 同口径）。
// OO 启用（OO_PUBLIC_URL）→ 返回 /office.html 承载页地址（office 模块 src=treehazard 签发，thd 票据回源即时生成）；
// 否则微软 Office 查看器（凭地址内 ?token= 回源下方下载接口，同出工日志 /task-sheet/preview 口径；
// ?token= 回源鉴权依赖主装配把 query token 映射为 Authorization，见迁移报告）。

import (
	"net/http"
	"strings"
)

// handleExportPreview 导出在线预览地址。
func (m *Module) handleExportPreview(w http.ResponseWriter, r *http.Request) {
	team, ok := teamFrom(r.Context())
	if !ok || team.ID == 0 {
		writeFail(w, http.StatusBadRequest, 40010, "无可用班组", nil)
		return
	}
	q := r.URL.Query()
	line := strings.TrimSpace(q.Get("line"))
	// OO 分支：承载页地址（office.html 再向 /office/config 签发 thd 票据回源即时生成）。
	if m.configValue(r.Context(), "OO_PUBLIC_URL") != "" {
		params := []string{"src=treehazard"}
		if line != "" {
			params = append(params, "line="+encodeRFC5987(line))
		}
		if v := q.Get("team_id"); v != "" {
			params = append(params, "team_id="+encodeRFC5987(v))
		}
		if splitOn(q.Get("split")) {
			params = append(params, "split=1")
		}
		params = append(params, "back=/treehazard.html")
		writeOK(w, map[string]any{"url": "/office.html?" + strings.Join(params, "&")}, "")
		return
	}
	// 微软查看器分支：下载路径文件名缀尾（查看器按 URL 路径扩展名识别格式），query 原样保留 + token。
	dq := make([]string, 0, 3)
	if line != "" {
		dq = append(dq, "line="+encodeRFC5987(line))
	}
	if v := q.Get("team_id"); v != "" {
		dq = append(dq, "team_id="+encodeRFC5987(v))
	}
	if splitOn(q.Get("split")) {
		dq = append(dq, "split=1")
	}
	fileName := exportFileName(team.Name, line)
	url := m.buildExportPreviewURL(r, "/api/v1/treehazard/export", dq, fileName)
	if url == "" {
		writeFail(w, http.StatusBadRequest, 40011, "未配置文件预览服务", nil)
		return
	}
	writeOK(w, map[string]any{"url": url}, "")
}

// buildExportPreviewURL 微软 Office 查看器地址（仅 Office 格式；非 Office 格式返回空串由调用方降级）。
// downloadPath 为下载接口路径（不含 query）；文件名缀在路径尾，token 取当前请求 JWT 回源。
func (m *Module) buildExportPreviewURL(r *http.Request, downloadPath string, query []string, fileName string) string {
	lower := strings.ToLower(fileName)
	if !strings.HasSuffix(lower, ".xlsx") && !strings.HasSuffix(lower, ".xls") &&
		!strings.HasSuffix(lower, ".doc") && !strings.HasSuffix(lower, ".docx") &&
		!strings.HasSuffix(lower, ".ppt") && !strings.HasSuffix(lower, ".pptx") {
		return ""
	}
	viewer := m.configValue(r.Context(), "PREVIEW_VIEWER_URL")
	if viewer == "" {
		viewer = "https://view.officeapps.live.com/op/view.aspx"
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		proto = "http"
	}
	q := append(append([]string{}, query...), "token="+encodeRFC5987(bearerToken(r)))
	src := proto + "://" + r.Host + downloadPath + "/" + encodeRFC5987(fileName) + "?" + strings.Join(q, "&")
	return viewer + "?src=" + encodeRFC5987(src)
}

// bearerToken 取 Authorization Bearer 值（回源 ?token= 同值）。
func bearerToken(r *http.Request) string {
	v := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(v) >= 7 && strings.EqualFold(v[:7], "Bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return ""
}
