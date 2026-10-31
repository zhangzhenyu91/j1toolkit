// netdisk.Module 读写能力适配：worklog.NetdiskSaver / safeday.NetdiskSaver（保存产物到网盘）
// 与 kvm.NetdiskBridge（网盘 ↔ 设备互推，Save 为 io.Reader 流式直传）。
// 空间根解析镜像 netdisk.Module.baseDirs 口径（该方法未导出）：运行时配置非空优先，回退静态 Base；
// 路径净化、越界校验、分享票据口径一律走 netdisk 导出能力，不在此放宽。
package bridge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"shade/internal/apps/kvm"
	"shade/internal/apps/netdisk"
	"shade/internal/apps/safeday"
	"shade/internal/apps/worklog"
	"shade/internal/auth"
)

// Ops 网盘读写底层能力（*netdisk.Module 经 NewOps 适配；测试可注入桩）。
type Ops interface {
	// Save 流式保存 <空间>/<dir>/<name>（目录自动创建）；返回「我的空间/…」或「公共区/…」展示路径。
	Save(ctx context.Context, user auth.Identity, space, dir, name string, body io.Reader, size int64) (string, error)
	// Download 拉取空间内相对路径文件字节。
	Download(ctx context.Context, user auth.Identity, space, rel string) ([]byte, error)
	// Allow 校验用户持有 netdisk 应用权限。
	Allow(ctx context.Context, userID int64) (bool, error)
}

// opError 业务类失败（参数/归属/未接入；状态与业务码随信封原样下发）。
// 上游异常不在此列，由各适配层按 502 / 通用文案处理。
type opError struct {
	status int
	code   int
	msg    string
}

func (e *opError) Error() string { return e.msg }

// NewOps *netdisk.Module → Ops 适配。
func NewOps(nd *netdisk.Module) Ops { return &netdiskOps{nd: nd} }

// netdiskOps Ops 的真实实现（全部走 netdisk 导出字段/接口）。
type netdiskOps struct{ nd *netdisk.Module }

// baseDirs 镜像 netdisk.Module.baseDirs：运行时配置非空优先（热更新），回退静态 Base。
func (o *netdiskOps) baseDirs(ctx context.Context) netdisk.BaseDirs {
	d := netdisk.BaseDirs{}
	if o.nd == nil {
		return d
	}
	d = o.nd.Base
	if o.nd.Cfg == nil {
		return d
	}
	if v, err := o.nd.Cfg.Get(ctx, "NETDISK_PERSONAL_ROOT"); err == nil && strings.TrimSpace(v) != "" {
		d.PersonalRoot = strings.TrimSpace(v)
	}
	if v, err := o.nd.Cfg.Get(ctx, "NETDISK_PUBLIC_ROOT"); err == nil && strings.TrimSpace(v) != "" {
		d.PublicRoot = strings.TrimSpace(v)
	}
	return d
}

// client OpenList 客户端可用性（未接入/未配置 → 503 业务错误）。
func (o *netdiskOps) client() (netdisk.OpenListClient, error) {
	if o.nd == nil || o.nd.OL == nil {
		return nil, &opError{status: http.StatusServiceUnavailable, code: 50301, msg: "团队网盘未接入"}
	}
	if err := o.nd.OL.EnsureConfigured(); err != nil {
		return nil, &opError{status: http.StatusServiceUnavailable, code: 50301, msg: "团队网盘未接入"}
	}
	return o.nd.OL, nil
}

// resolveRoot 空间根：my={personal}/{username}；public={public}/{班组名}（未分配班组无公共区）。
func (o *netdiskOps) resolveRoot(ctx context.Context, user auth.Identity, space string) (string, error) {
	teamName := ""
	if user.TeamID > 0 && o.nd.DB != nil {
		_ = o.nd.DB.QueryRowContext(ctx, `SELECT name FROM sys_team WHERE id = ?`, user.TeamID).Scan(&teamName)
	}
	root, ok := netdisk.SpaceRootOf(o.baseDirs(ctx), user.Username, teamName, space)
	if !ok {
		return "", &opError{status: http.StatusBadRequest, code: 40001, msg: "参数错误：space"}
	}
	if root == "" {
		return "", &opError{status: http.StatusBadRequest, code: 40310, msg: "未分配班组，无公共区"}
	}
	return root, nil
}

// Save 流式保存（handleSaveToNetdisk 同口径：SanitizeRelDir/SanitizeName + EnsureDir + FsPut 直传）。
func (o *netdiskOps) Save(ctx context.Context, user auth.Identity, space, dir, name string, body io.Reader, size int64) (string, error) {
	if space == "" {
		space = "my"
	}
	ol, err := o.client()
	if err != nil {
		return "", err
	}
	rel, ok := netdisk.SanitizeRelDir(dir)
	if !ok {
		return "", &opError{status: http.StatusBadRequest, code: 40001, msg: "参数错误：dir"}
	}
	fileName := netdisk.SanitizeName(name)
	if fileName == "" {
		return "", &opError{status: http.StatusBadRequest, code: 40001, msg: "文件名不合法"}
	}
	root, err := o.resolveRoot(ctx, user, space)
	if err != nil {
		return "", err
	}
	target := root
	if rel != "" {
		target = root + "/" + rel
	}
	if err := ol.EnsureDir(ctx, target); err != nil {
		return "", err
	}
	if err := ol.FsPut(ctx, target+"/"+fileName, body, size); err != nil {
		return "", err
	}
	label := "我的空间"
	if space == "public" {
		label = "公共区"
	}
	path := label + "/" + fileName
	if rel != "" {
		path = label + "/" + rel + "/" + fileName
	}
	return path, nil
}

// Download 空间内相对路径文件字节（ReadFileBytes 同口径：越界校验 + 目录拒绝 + 512MB 上限）。
func (o *netdiskOps) Download(ctx context.Context, user auth.Identity, space, rel string) ([]byte, error) {
	if o.nd == nil {
		return nil, &opError{status: http.StatusServiceUnavailable, code: 50301, msg: "团队网盘未接入"}
	}
	data, _, err := o.nd.ReadFileBytes(ctx, space, user.Username, user.TeamID, rel)
	return data, err
}

// OpenFileStream 流式回源（KVM 大文件推送生产路径）：不整读内存，调用方必须 Close。
func (o *netdiskOps) OpenFileStream(ctx context.Context, space, username string, teamID int64, rel string) (io.ReadCloser, string, int64, error) {
	if o.nd == nil {
		return nil, "", 0, &opError{status: http.StatusServiceUnavailable, code: 50301, msg: "团队网盘未接入"}
	}
	return o.nd.OpenFileStream(ctx, space, username, teamID, rel)
}

// Allow netdisk 应用权限检查（netdisk withContext 同口径：Permit 为 nil 放行）。
func (o *netdiskOps) Allow(ctx context.Context, userID int64) (bool, error) {
	if o.nd == nil || o.nd.Permit == nil {
		return true, nil
	}
	return o.nd.Permit.Allow(ctx, userID, "netdisk")
}

// mapNetdiskErr 网盘侧错误归一：业务类失败 → *opError（可读文案透传）；上游错误原样交 502 口径。
func mapNetdiskErr(err error) error {
	if err == nil {
		return nil
	}
	var oe *opError
	if errors.As(err, &oe) {
		return err
	}
	var ue *netdisk.UpstreamError
	if errors.As(err, &ue) && ue.Exposable {
		status := ue.Status
		if status == 0 {
			status = http.StatusBadGateway
		}
		code := ue.Code
		if code == 0 {
			code = 50201
		}
		return &opError{status: status, code: code, msg: ue.Message}
	}
	return err
}

// requireNetdiskAllow 跨模块保存的网盘权限门：无 netdisk 应用权限 → 403 40301；
// 权限校验自身失败按 500 内部错误，不把底层细节透给调用方。
func requireNetdiskAllow(ctx context.Context, o Ops, userID int64) error {
	allow, err := o.Allow(ctx, userID)
	if err != nil {
		return &opError{status: http.StatusInternalServerError, code: 50000, msg: "应用权限校验失败"}
	}
	if !allow {
		return &opError{status: http.StatusForbidden, code: 40301, msg: "暂无该应用的使用权限，请联系管理员开通"}
	}
	return nil
}

// ---- worklog.NetdiskSaver ----

// WorklogSaver 适配 worklog.NetdiskSaver（任务单/费用汇总/照片 PDF 等保存到网盘）。
type WorklogSaver struct{ O Ops }

// NewWorklogSaver 构造 worklog 网盘保存适配。
func NewWorklogSaver(nd *netdisk.Module) *WorklogSaver { return &WorklogSaver{O: NewOps(nd)} }

// SaveToNetdisk 保存文件到网盘；业务失败映射 *worklog.NetdiskSaveError 随信封原样下发。
// 保存前按身份校验 netdisk 应用权限（worklog 侧无独立权限门，避免借保存接口写网盘）。
func (s *WorklogSaver) SaveToNetdisk(ctx context.Context, user auth.Identity, req worklog.NetdiskSaveRequest) (string, error) {
	if err := requireNetdiskAllow(ctx, s.O, user.ID); err != nil {
		return "", worklogSaveErr(err)
	}
	body := req.Body
	if body == nil {
		body = []byte{}
	}
	path, err := s.O.Save(ctx, user, req.Space, req.Dir, req.Name, bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return "", worklogSaveErr(err)
	}
	return path, nil
}

// worklogSaveErr 错误映射：业务类失败 → *worklog.NetdiskSaveError；其余原样（writeNetdiskErr 归 502）。
func worklogSaveErr(err error) error {
	var oe *opError
	if errors.As(err, &oe) {
		return &worklog.NetdiskSaveError{Status: oe.status, Code: oe.code, Message: oe.msg}
	}
	return mapNetdiskErr(err)
}

// ---- safeday.NetdiskSaver ----

// SafedaySaver 适配 safeday.NetdiskSaver（安全日记录 docx 保存到网盘）。
type SafedaySaver struct{ O Ops }

// NewSafedaySaver 构造 safeday 网盘保存适配。
func NewSafedaySaver(nd *netdisk.Module) *SafedaySaver { return &SafedaySaver{O: NewOps(nd)} }

// SaveToNetdisk 保存文件到网盘（safeday handler 按 NetdiskSaveError 的 Status/Code 直出）。
// 保存前按身份校验 netdisk 应用权限（无权限 403 40301，不写网盘）。
func (s *SafedaySaver) SaveToNetdisk(ctx context.Context, user auth.Identity, req safeday.NetdiskSaveRequest) (string, error) {
	if err := requireNetdiskAllow(ctx, s.O, user.ID); err != nil {
		return "", safedaySaveErr(err)
	}
	body := req.Body
	if body == nil {
		body = []byte{}
	}
	path, err := s.O.Save(ctx, user, req.Space, req.Dir, req.Name, bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return "", safedaySaveErr(err)
	}
	return path, nil
}

// safedaySaveErr 错误映射：业务类失败 → *safeday.NetdiskSaveError（handler 按 Status/Code 直出）；
// 其余原样（safeday handler 归 500）。
func safedaySaveErr(err error) error {
	var oe *opError
	if errors.As(err, &oe) {
		return &safeday.NetdiskSaveError{Status: oe.status, Code: oe.code, Message: oe.msg}
	}
	return mapNetdiskErr(err)
}

// ---- kvm.NetdiskBridge ----

// KvmBridge 适配 kvm.NetdiskBridge（网盘 → 设备推送与设备 → 网盘保存）。
type KvmBridge struct{ O Ops }

// NewKvmBridge 构造 kvm 团队网盘桥接。
func NewKvmBridge(nd *netdisk.Module) *KvmBridge { return &KvmBridge{O: NewOps(nd)} }

// AssertAccess 校验用户持有 netdisk 应用权限（无权限返回可读 kvm.BizError 40301）。
func (b *KvmBridge) AssertAccess(ctx context.Context, userID int64) error {
	allow, err := b.O.Allow(ctx, userID)
	if err != nil {
		return mapNetdiskErr(err)
	}
	if !allow {
		return &kvm.BizError{Message: "暂无该应用的使用权限，请联系管理员开通",
			Status: http.StatusForbidden, Code: 40301}
	}
	return nil
}

// streamDownloader 网盘桥可选流式回源能力（*netdisk.Module 满足）。
type streamDownloader interface {
	OpenFileStream(ctx context.Context, space, username string, teamID int64, relPath string) (io.ReadCloser, string, int64, error)
}

// Download 拉取网盘文件内容（space: my/public；rel 为空间内相对路径，经 NormRel 越界校验）。
// 小文件/旧适配器兼容路径；KVM 大文件推送走 DownloadStream。
func (b *KvmBridge) Download(ctx context.Context, user auth.Identity, space, rel string) ([]byte, error) {
	data, err := b.O.Download(ctx, user, space, rel)
	if err != nil {
		return nil, kvmSaveErr(err)
	}
	return data, nil
}

// DownloadStream 流式回源（网盘 → 设备第一跳）：底层支持 OpenFileStream 时不整读内存，
// 调用方必须 Close body；旧 Ops 实现回退 Download 并包装为内存流。
func (b *KvmBridge) DownloadStream(ctx context.Context, user auth.Identity, space, rel string) (io.ReadCloser, int64, error) {
	if sd, ok := b.O.(streamDownloader); ok {
		rc, _, size, err := sd.OpenFileStream(ctx, space, user.Username, user.TeamID, rel)
		if err != nil {
			return nil, 0, kvmSaveErr(err)
		}
		return rc, size, nil
	}
	data, err := b.Download(ctx, user, space, rel)
	if err != nil {
		return nil, 0, err
	}
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
}

// Save 保存文件到网盘（body 流式直传 OpenList，不整读内存）；返回保存路径。
func (b *KvmBridge) Save(ctx context.Context, user auth.Identity, space, dir, name string, body io.Reader, size int64) (string, error) {
	path, err := b.O.Save(ctx, user, space, dir, name, body, size)
	if err != nil {
		return "", kvmSaveErr(err)
	}
	return path, nil
}

// kvmSaveErr 错误映射：业务类失败 → *kvm.BizError（可读文案透传）；其余原样。
func kvmSaveErr(err error) error {
	var oe *opError
	if errors.As(err, &oe) {
		return &kvm.BizError{Message: oe.msg, Status: oe.status, Code: oe.code}
	}
	return mapNetdiskErr(err)
}

// 编译期契约：三适配满足各自注入接口。
var (
	_ worklog.NetdiskSaver = (*WorklogSaver)(nil)
	_ safeday.NetdiskSaver = (*SafedaySaver)(nil)
	_ kvm.NetdiskBridge    = (*KvmBridge)(nil)
	_ Ops                  = (*netdiskOps)(nil)
)
