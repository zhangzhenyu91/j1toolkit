package netdisk

// office 文档源桥（office.NetdiskBridge 方法集口径）：main 把 *Module 直接注入
// office.NewNetdiskDoc 即可装配网盘文档源（回源 + 保存写回 + 签发期文档 key）。
// 所有路径经 NormRel 越界校验，分享票据与目录越界口径不在此放宽。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// NotFoundError 「文件不存在」错误（office 桥按 IsNotFound 归一为 ErrDocNotFound）。
type NotFoundError struct{ Message string }

func (e *NotFoundError) Error() string {
	if e.Message == "" {
		return "文件或目录不存在"
	}
	return e.Message
}

// IsNotFound 供桥接方判定「不存在」语义。
func (e *NotFoundError) IsNotFound() bool { return true }

// officeAbs 解析空间根 + 相对路径 → OpenList 绝对路径（含越界校验）。
func (m *Module) officeAbs(ctx context.Context, space, username string, teamID int64, relPath string) (string, error) {
	rel, ok := NormRel(relPath)
	if !ok || rel == "/" {
		return "", &NotFoundError{Message: "路径不合法"}
	}
	teamName := ""
	if teamID > 0 {
		_ = m.DB.QueryRowContext(ctx, `SELECT name FROM sys_team WHERE id = ?`, teamID).Scan(&teamName)
	}
	root, ok := SpaceRootOf(m.baseDirs(ctx), username, teamName, space)
	if !ok {
		return "", &NotFoundError{Message: "参数错误：space"}
	}
	if root == "" {
		return "", &NotFoundError{Message: "未分配班组，无公共区"}
	}
	return JoinPath(root, rel), nil
}

// officeOL OpenList 客户端可用性（桥接调用无 HTTP 层写错误的机会，直接返回错误）。
func (m *Module) officeOL() (OpenListClient, error) {
	if m.OL == nil {
		return nil, errors.New("网盘服务异常，请稍后再试")
	}
	if err := m.OL.EnsureConfigured(); err != nil {
		return nil, err
	}
	return m.OL, nil
}

// metaOf 取文件元信息（存在性/目录/size/modified）。
func (m *Module) metaOf(ctx context.Context, ol OpenListClient, abs string) (size int64, modified string, isDir bool, err error) {
	raw, err := ol.Call(ctx, http.MethodPost, "/api/fs/get", CallOpts{Body: map[string]any{"path": abs, "password": ""}})
	if err != nil {
		return 0, "", false, bridgeErr(err)
	}
	var meta struct {
		IsDir    bool   `json:"is_dir"`
		Size     int64  `json:"size"`
		Modified string `json:"modified"`
	}
	_ = json.Unmarshal(raw, &meta)
	if meta.IsDir {
		return 0, "", true, nil
	}
	return meta.Size, meta.Modified, false, nil
}

// bridgeErr 桥边界错误归一：上游 not found → NotFoundError（office 口径；文案同 Node relayFail
// 40400「文件或目录不存在」）。
func bridgeErr(err error) error {
	if err == nil {
		return nil
	}
	var ue *UpstreamError
	if errors.As(err, &ue) && strings.Contains(strings.ToLower(ue.Message), "not found") {
		return &NotFoundError{Message: "文件或目录不存在"}
	}
	return err
}

// ReadFileBytes 网盘文件回源：按空间/用户/班组解析空间根后下载整文件字节；
// 返回 contentType 按扩展名（office 回源口径，OO 靠它判格式）。
func (m *Module) ReadFileBytes(ctx context.Context, space, username string, teamID int64, relPath string) ([]byte, string, error) {
	abs, err := m.officeAbs(ctx, space, username, teamID, relPath)
	if err != nil {
		return nil, "", err
	}
	ol, err := m.officeOL()
	if err != nil {
		return nil, "", err
	}
	if _, _, isDir, err := m.metaOf(ctx, ol, abs); err != nil {
		return nil, "", err
	} else if isDir {
		return nil, "", &NotFoundError{Message: "该路径不是文件"}
	}
	rc, _, err := ol.DownloadStream(ctx, abs)
	if err != nil {
		return nil, "", bridgeErr(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, 512<<20))
	if err != nil {
		return nil, "", err
	}
	return data, docMIME(BaseName(relPath)), nil
}

// WriteFileBytes 保存产物写回原路径（office 保存回调）。
func (m *Module) WriteFileBytes(ctx context.Context, space, username string, teamID int64, relPath string, data []byte) error {
	abs, err := m.officeAbs(ctx, space, username, teamID, relPath)
	if err != nil {
		return err
	}
	ol, err := m.officeOL()
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return errors.New("回调产物为空")
	}
	return bridgeErr(ol.FsPut(ctx, abs, bytes.NewReader(data), int64(len(data))))
}

// DocKey 签发期校验存在并返回文档版本 key（sha1(绝对路径|size|modified)，内容变即滚动）。
func (m *Module) DocKey(ctx context.Context, space, username string, teamID int64, relPath string) (string, error) {
	abs, err := m.officeAbs(ctx, space, username, teamID, relPath)
	if err != nil {
		return "", err
	}
	ol, err := m.officeOL()
	if err != nil {
		return "", err
	}
	size, modified, isDir, err := m.metaOf(ctx, ol, abs)
	if err != nil {
		return "", err
	}
	if isDir {
		return "", &NotFoundError{Message: "路径不合法"}
	}
	return docKey(abs, size, modified), nil
}

// docMIME 回源 Content-Type（对照 Node RAW_MIME + 网盘 PREVIEW_MIME 口径）。
func docMIME(name string) string {
	switch strings.ToLower(extOf(name)) {
	case "doc":
		return "application/msword"
	case "docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case "xls":
		return "application/vnd.ms-excel"
	case "xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "ppt":
		return "application/vnd.ms-powerpoint"
	case "pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case "pdf":
		return "application/pdf"
	case "txt", "md", "csv":
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}
