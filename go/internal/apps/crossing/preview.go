package crossing

// GET /export/preview?line=&split=&team_id=：统计表在线预览地址（与 server/src/crossing/index.js 同口径）。
// OO 启用（OO_PUBLIC_URL）→ 返回 /office.html 承载页地址（office 模块 src=crossing 签发，crs 票据回源即时生成）；
// 否则微软 Office 查看器（凭地址内 ?token= 回源下方下载接口，同出工日志 /task-sheet/preview 口径；
// ?token= 回源鉴权依赖主装配把 query token 映射为 Authorization，见迁移报告）。

import (
	"net/http"
	"strings"
)

// exportPreview GET /export/preview：导出在线预览地址。
func (m *Module) exportPreview(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r, 0)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoTeam, "无可用班组")
		return
	}
	q := r.URL.Query()
	line := strings.TrimSpace(q.Get("line"))
	// OO 分支：承载页地址（office.html 再向 /office/config 签发 crs 票据回源即时生成）。
	if v, _ := m.cfgValue(r.Context(), "OO_PUBLIC_URL", ""); v != "" {
		params := []string{"src=crossing"}
		if line != "" {
			params = append(params, "line="+urlEncode(line))
		}
		if t := q.Get("team_id"); t != "" {
			params = append(params, "team_id="+urlEncode(t))
		}
		if splitOn(q.Get("split")) {
			params = append(params, "split=1")
		}
		params = append(params, "back=/crossing.html")
		writeOK(w, map[string]any{"url": "/office.html?" + strings.Join(params, "&")})
		return
	}
	// 微软查看器分支：下载路径文件名缀尾（查看器按 URL 路径扩展名识别格式），query 原样保留 + token。
	dq := make([]string, 0, 3)
	if line != "" {
		dq = append(dq, "line="+urlEncode(line))
	}
	if t := q.Get("team_id"); t != "" {
		dq = append(dq, "team_id="+urlEncode(t))
	}
	if splitOn(q.Get("split")) {
		dq = append(dq, "split=1")
	}
	fileName := exportFileName(team.Name, line)
	url := m.buildExportPreviewURL(r, "/api/v1/crossing/export", dq, fileName)
	if url == "" {
		fail(w, http.StatusBadRequest, 40011, "未配置文件预览服务")
		return
	}
	writeOK(w, map[string]any{"url": url})
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
	viewer, _ := m.cfgValue(r.Context(), "PREVIEW_VIEWER_URL", "")
	if viewer == "" {
		viewer = "https://view.officeapps.live.com/op/view.aspx"
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		proto = "http"
	}
	q := append(append([]string{}, query...), "token="+urlEncode(bearerToken(r)))
	src := proto + "://" + r.Host + downloadPath + "/" + urlEncode(fileName) + "?" + strings.Join(q, "&")
	return viewer + "?src=" + urlEncode(src)
}

// bearerToken 取 Authorization Bearer 值（回源 ?token= 同值）。
func bearerToken(r *http.Request) string {
	v := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(v) >= 7 && strings.EqualFold(v[:7], "Bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return ""
}
