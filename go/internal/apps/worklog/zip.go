// 照片 ZIP 打包下载（自 WorkLogs 独立服务移植，请求体形状不变：{ photos: [{ url, name }] }）。
// 逐张回源写入 zip（store 模式不二次压缩），失败记录进「下载失败清单.txt」继续，全部失败才报错；
// SSRF 防护：仅允许打包本仓 COS 地址（照片 url 均出自 Store.PublicURL）。
package worklog

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

const (
	zipMaxPhotos   = 300          // 一次最多打包照片数
	zipPhotoMaxLen = 50 << 20     // 单张照片字节上限（超出跳过）
	zipNameFile    = "下载失败清单.txt" // 部分失败时写入 zip 的清单文件名
)

// sanitizeZipFileName zip 内文件名净化：去路径分隔符与非法字符，防止 zip 内路径穿越。
func sanitizeZipFileName(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case '\\', '/', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		if r < 0x20 {
			return '_'
		}
		return r
	}, name)
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return "photo.jpg"
	}
	return cleaned
}

// handleZip POST /zip：批量打包水印照片为 zip 二进制（不走 {code,data} 信封）。
func (m *Module) handleZip(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	_ = team
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误")
		return
	}
	type zipItem struct {
		URL  string
		Name string
	}
	list := make([]zipItem, 0)
	if raw, has := body["photos"]; has {
		var arr []map[string]json.RawMessage
		if jsonUnmarshal(raw, &arr) != nil {
			fail(w, http.StatusBadRequest, codeNoPhoto, "没有可下载的照片")
			return
		}
		for _, p := range arr {
			list = append(list, zipItem{URL: strings.TrimSpace(rawString(p["url"])), Name: rawString(p["name"])})
		}
	}
	if len(list) == 0 {
		fail(w, http.StatusBadRequest, codeNoPhoto, "没有可下载的照片")
		return
	}
	if len(list) > zipMaxPhotos {
		fail(w, http.StatusBadRequest, codeNoPhoto, "一次最多打包 "+itoa(zipMaxPhotos)+" 张照片")
		return
	}
	// SSRF 防护：仅允许打包本仓 COS 地址（照片 url 均出自 Store.PublicURL），防服务端被当任意地址回源代理
	if m.Store == nil {
		fail(w, http.StatusServiceUnavailable, codeNotConfigured, "对象存储未接入，无法打包照片")
		return
	}
	cosPrefix := m.Store.PublicURL("")
	for i := range list {
		if !strings.HasPrefix(list[i].URL, cosPrefix) {
			fail(w, http.StatusBadRequest, codeNoPhoto, "照片地址不合法（仅支持本仓 COS 地址）")
			return
		}
		list[i].Name = sanitizeZipFileName(list[i].Name)
	}

	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	failed := make([]zipItem, 0)
	success := 0
	for _, item := range list {
		key := strings.TrimPrefix(item.URL, cosPrefix)
		buf, err := fetchObjectBytes(r.Context(), m.Store, key, item.URL)
		if err != nil || len(buf) > zipPhotoMaxLen {
			failed = append(failed, item)
			continue
		}
		hdr := &zip.FileHeader{Name: item.Name, Method: zip.Store}
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			failed = append(failed, item)
			continue
		}
		if _, err := fw.Write(buf); err != nil {
			failed = append(failed, item)
			continue
		}
		success++
	}
	if success == 0 {
		_ = zw.Close()
		fail(w, http.StatusInternalServerError, codeDownload, "照片下载失败")
		return
	}
	if len(failed) > 0 {
		lines := make([]string, 0, len(failed))
		for _, f := range failed {
			lines = append(lines, f.Name+" "+f.URL)
		}
		if fw, err := zw.Create(zipNameFile); err == nil {
			_, _ = fw.Write([]byte(strings.Join(lines, "\n")))
		}
	}
	if err := zw.Close(); err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	// RFC5987 编码中文文件名，附 ASCII fallback
	w.Header().Set("Content-Disposition",
		`attachment; filename="photos.zip"; filename*=UTF-8''`+encURI("水印照片.zip"))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(zbuf.Bytes())
}
