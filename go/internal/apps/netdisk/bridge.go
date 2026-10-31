package netdisk

// office 文档源桥（office.NetdiskBridge 方法集口径）：main 把 *Module 直接注入
// office.NewNetdiskDoc 即可装配网盘文档源（回源 + 保存写回 + 签发期文档 key）。
// 所有路径经 NormRel 越界校验，分享票据与目录越界口径不在此放宽。

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// NotFoundError 「文件不存在」错误（office 桥按 IsNotFound 归一为 ErrDocNotFound）。
type NotFoundError struct{ Message string }

// ErrFileTooLarge Office 回源文件超出字节预算（显式报错，不返回截断内容）。
var ErrFileTooLarge = errors.New("文件超出大小限制")

func (e *NotFoundError) Error() string {
	if e.Message == "" {
		return "文件或目录不存在"
	}
	return e.Message
}

// IsNotFound 供桥接方判定「不存在」语义。
func (e *NotFoundError) IsNotFound() bool { return true }

// officeAbs 解析空间根 + 相对路径 → OpenList 绝对路径（含越界校验）。
// 公共区兜底归属校验：Office 签发端已按身份收敛 team_id，这里再按 sys_user 角色/班组复核，
// 非超管不得借票据读写他班公共区（拒绝时不触达 OpenList）。
func (m *Module) officeAbs(ctx context.Context, space, username string, teamID int64, relPath string) (string, error) {
	rel, ok := NormRel(relPath)
	if !ok || rel == "/" {
		return "", &NotFoundError{Message: "路径不合法"}
	}
	teamName := ""
	if teamID > 0 {
		if m.DB == nil {
			return "", &NotFoundError{Message: "无权访问其他班组的数据"}
		}
		_ = m.DB.QueryRowContext(ctx, `SELECT name FROM sys_team WHERE id = ?`, teamID).Scan(&teamName)
	}
	if space == "public" && teamID > 0 {
		inTeam, err := m.userInTeam(ctx, username, teamID)
		if err != nil {
			return "", err
		}
		if !inTeam {
			return "", &NotFoundError{Message: "无权访问其他班组的数据"}
		}
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

// userInTeam 公共区兜底归属判定：超管任意班组；其余用户须属于目标班组（查无/未分配均拒绝）。
func (m *Module) userInTeam(ctx context.Context, username string, teamID int64) (bool, error) {
	if username == "" {
		return false, nil
	}
	var role string
	var userTeam sql.NullInt64
	err := m.DB.QueryRowContext(ctx, `SELECT role, team_id FROM sys_user WHERE username = ?`, username).
		Scan(&role, &userTeam)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if role == "admin" {
		return true, nil
	}
	return userTeam.Valid && userTeam.Int64 == teamID, nil
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
// 小文件/旧适配器兼容路径；大文档请走 OpenFileStream，避免整读内存。
func (m *Module) ReadFileBytes(ctx context.Context, space, username string, teamID int64, relPath string) ([]byte, string, error) {
	rc, contentType, _, err := m.OpenFileStream(ctx, space, username, teamID, relPath)
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()
	data, err := readAllLimitBytes(rc, m.maxFileReadBytes())
	if err != nil {
		return nil, "", err
	}
	return data, contentType, nil
}

// OpenFileStream 网盘文件回源流：完成路径/归属/目录/大小校验后返回上游 body，
// 调用方必须 Close（提前关闭会终止上游连接）。size 来自 fs/get 元数据，供 Content-Length
// 与 KVM 空文件判定；全程不整读内存。
func (m *Module) OpenFileStream(ctx context.Context, space, username string, teamID int64, relPath string) (io.ReadCloser, string, int64, error) {
	abs, err := m.officeAbs(ctx, space, username, teamID, relPath)
	if err != nil {
		return nil, "", 0, err
	}
	ol, err := m.officeOL()
	if err != nil {
		return nil, "", 0, err
	}
	size, _, isDir, err := m.metaOf(ctx, ol, abs)
	if err != nil {
		return nil, "", 0, err
	}
	if isDir {
		return nil, "", 0, &NotFoundError{Message: "该路径不是文件"}
	}
	if size > m.maxFileReadBytes() {
		// 优先用元数据拒绝，避免为超限文件建立下载流。
		return nil, "", 0, fmt.Errorf("%w（%d > %d 字节）", ErrFileTooLarge, size, m.maxFileReadBytes())
	}
	rc, _, err := ol.DownloadStream(ctx, abs)
	if err != nil {
		return nil, "", 0, bridgeErr(err)
	}
	return rc, docMIME(BaseName(relPath)), size, nil
}

// readAllLimitBytes 读尽 r（至多 maxBytes）；limit+1 检出超限并显式报错，绝不静默截断。
func readAllLimitBytes(r io.Reader, maxBytes int64) ([]byte, error) {
	buf, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > maxBytes {
		return nil, fmt.Errorf("%w（> %d 字节）", ErrFileTooLarge, maxBytes)
	}
	return buf, nil
}

// WriteFileBytes 保存产物写回原路径（office 保存回调）。
func (m *Module) WriteFileBytes(ctx context.Context, space, username string, teamID int64, relPath string, data []byte) error {
	if len(data) == 0 {
		return errors.New("回调产物为空")
	}
	return m.WriteFileStream(ctx, space, username, teamID, relPath, bytes.NewReader(data), int64(len(data)))
}

// WriteFileStream 保存产物写回原路径（流式；office 大文档回调优先走这里，避免整读内存）。
func (m *Module) WriteFileStream(ctx context.Context, space, username string, teamID int64, relPath string, body io.Reader, size int64) error {
	abs, err := m.officeAbs(ctx, space, username, teamID, relPath)
	if err != nil {
		return err
	}
	ol, err := m.officeOL()
	if err != nil {
		return err
	}
	if err := bridgeErr(ol.FsPut(ctx, abs, body, size)); err != nil {
		return err
	}
	m.dirSize.invalidate() // 回写文档使目录统计过期
	return nil
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
