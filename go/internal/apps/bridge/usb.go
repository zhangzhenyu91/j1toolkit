// treehazard / crossing UsbLink 真实适配：内网 U 盘同步设备链路（GLKVM fileshare
// mount/download/list/delete/push，与 server/src/{treehazard,crossing}/sync.js 同口径）。
// 设备操作收口到小型可注入接口 DeviceClient（真实现 GlkvmFileshare：kvm.Platform 换
// 代理会话 + /api/fileshare/* 转发）；设备定位 ddns 精确匹配 + MAC 归一校验（防换机误操作，
// 同 Node findDevice）。未配置 ddns / 未注入设备客户端时明确报错（不假成功）。
package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"shade/internal/apps/kvm"
	"shade/internal/apps/usbcmd"
	"shade/internal/integrations"
)

// ErrUsbNotConfigured 同步设备未配置/链路未接入（tree/crossing sync.go 同语义）。
var ErrUsbNotConfigured = errors.New("未配置同步设备（*_SYNC_DEVICE_DDNS）")

// UsbKeys 设备定位配置键组（treehazard / crossing / 派车单各一套）。
type UsbKeys struct {
	// DdnsKey / MacKey / UserKey 运行时配置键（如 TREEHAZARD_SYNC_DEVICE_DDNS）。
	DdnsKey string
	MacKey  string
	UserKey string
	// FallbackDdns / FallbackMac 共机回退键值（Node 口径「默认回退派车单同机」）；空 = 不回退。
	FallbackDdns string
	FallbackMac  string
}

// NewUsbLink 构造同步设备链路：platform 为 kvm 平台（GLKVMClient，设备定位 + fileshare 转发）；
// dev / find 可后续替换为测试桩。
func NewUsbLink(platform kvm.Platform, cfg ConfigGet, keys UsbKeys, adminUser string) *UsbLink {
	return &UsbLink{
		// 设备调用重试同 Node deviceCall（共试 3 次、间隔 1 分钟，两跳转发偶发抖动）。
		Dev:          &GlkvmFileshare{Platform: platform, Attempts: 3, RetryGap: time.Minute},
		Find:         &GlkvmFinder{Platform: platform},
		Cfg:          cfg,
		DdnsKey:      keys.DdnsKey,
		MacKey:       keys.MacKey,
		UserKey:      keys.UserKey,
		FallbackDdns: keys.FallbackDdns,
		FallbackMac:  keys.FallbackMac,
		AdminUser:    adminUser,
	}
}

// DeviceRef 设备定位结果（ddns 解析 + MAC 校验后的稳定标识）。
type DeviceRef struct {
	ID   string // 平台设备 id（ProxySession 参数）
	Ddns string
}

// DeviceClient KVM 设备 fileshare 操作最小面（可注入；真实现 GlkvmFileshare）。
// username 为代登平台账号（须在 GLKVM 平台可见该设备组）。
type DeviceClient interface {
	// MountToHost 挂载（共享）U 盘至被控机端。
	MountToHost(ctx context.Context, dev DeviceRef, username string) error
	// DownloadFile 下载 U 盘内文件；文件不存在返回 (nil, nil)（区别于链路故障报错）。
	DownloadFile(ctx context.Context, dev DeviceRef, username, name string) ([]byte, error)
	// ListFiles 列 U 盘根目录文件名。
	ListFiles(ctx context.Context, dev DeviceRef, username string) ([]string, error)
	// DeleteFiles 删除 U 盘内文件（names 一次删多个）。
	DeleteFiles(ctx context.Context, dev DeviceRef, username string, names []string) error
	// PushFile 推送文件到 U 盘。
	PushFile(ctx context.Context, dev DeviceRef, username, name string, data []byte) error
}

// DeviceFinder 设备定位（ddns 精确匹配 + MAC 归一校验；真实现 GlkvmFinder）。
type DeviceFinder interface {
	FindByDdns(ctx context.Context, username, ddns, mac string) (DeviceRef, error)
}

// UsbLink treehazard.UsbLink / crossing.UsbLink 实现：每次操作前按运行时配置解析设备
// （DdnsKey 缺失回退 FallbackDdns，MacKey/UserKey 同理），再交 DeviceClient。
type UsbLink struct {
	Dev  DeviceClient // KVM 设备客户端（可注入）
	Find DeviceFinder // 设备定位（可注入）
	Cfg  ConfigGet    // 运行时配置（*_SYNC_DEVICE_DDNS / *_SYNC_DEVICE_MAC / *_SYNC_KVM_USER）
	// DdnsKey / MacKey / UserKey 本模块配置键（treehazard 与 crossing 各自一套）。
	DdnsKey string
	MacKey  string
	UserKey string
	// FallbackDdns / FallbackMac 共机回退（Node 口径「默认回退派车单同机」）；空 = 不回退。
	FallbackDdns string
	FallbackMac  string
	// AdminUser 代登账号缺省（配置均缺失时用；Node 口径留空回退管理员账号）。
	AdminUser string
}

// resolve 解析设备与代登账号（未配置 ddns → ErrUsbNotConfigured）。
func (u *UsbLink) resolve(ctx context.Context) (DeviceRef, string, error) {
	if u == nil || u.Dev == nil || u.Find == nil {
		return DeviceRef{}, "", ErrUsbNotConfigured
	}
	var ddns, mac, username string
	if hub, ok := usbcmd.HubFrom(ctx); ok {
		ddns = strings.TrimSpace(hub.Ddns)
		mac = strings.TrimSpace(hub.Mac)
		username = strings.TrimSpace(hub.User)
	}
	if ddns == "" {
		ddns = tokenOf(ctx, u.Cfg, u.DdnsKey)
		if ddns == "" {
			ddns = strings.TrimSpace(u.FallbackDdns)
		}
	}
	if ddns == "" {
		return DeviceRef{}, "", ErrUsbNotConfigured
	}
	if mac == "" {
		mac = tokenOf(ctx, u.Cfg, u.MacKey)
		if mac == "" {
			mac = strings.TrimSpace(u.FallbackMac)
		}
	}
	if username == "" {
		username = tokenOf(ctx, u.Cfg, u.UserKey)
		if username == "" {
			username = strings.TrimSpace(u.AdminUser)
		}
	}
	dev, err := u.Find.FindByDdns(ctx, username, ddns, mac)
	if err != nil {
		return DeviceRef{}, "", err
	}
	return dev, username, nil
}

// MountToHost 挂载（共享）U 盘至被控机端。
func (u *UsbLink) MountToHost(ctx context.Context) error {
	dev, user, err := u.resolve(ctx)
	if err != nil {
		return err
	}
	return u.Dev.MountToHost(ctx, dev, user)
}

// DownloadFile 下载 U 盘内文件；文件不存在返回 (nil, nil)。
func (u *UsbLink) DownloadFile(ctx context.Context, name string) ([]byte, error) {
	dev, user, err := u.resolve(ctx)
	if err != nil {
		return nil, err
	}
	return u.Dev.DownloadFile(ctx, dev, user, name)
}

// ListFiles 列 U 盘根目录文件名。
func (u *UsbLink) ListFiles(ctx context.Context) ([]string, error) {
	dev, user, err := u.resolve(ctx)
	if err != nil {
		return nil, err
	}
	return u.Dev.ListFiles(ctx, dev, user)
}

// DeleteFiles 删除 U 盘内文件。
func (u *UsbLink) DeleteFiles(ctx context.Context, names []string) error {
	dev, user, err := u.resolve(ctx)
	if err != nil {
		return err
	}
	return u.Dev.DeleteFiles(ctx, dev, user, names)
}

// PushFile 推送文件到 U 盘。
func (u *UsbLink) PushFile(ctx context.Context, name string, data []byte) error {
	dev, user, err := u.resolve(ctx)
	if err != nil {
		return err
	}
	return u.Dev.PushFile(ctx, dev, user, name, data)
}

// ---- DeviceClient 真实现：GLKVM fileshare 转发 ----

// GlkvmFileshare DeviceClient 真实现：kvm.Platform 换设备 fileshare 转发会话，
// 调设备 /api/fileshare/*（设备侧信封 server/public/kvm-device/fileshare.py）。
type GlkvmFileshare struct {
	Platform kvm.Platform // GLKVMClient（ProxySession 换转发会话）
	// HTTP 常规转发客户端（status/list/mount/delete；nil 用 60s 超时，同 Node axios timeout: 60000）。
	HTTP *http.Client
	// HTTPBig 大文件转发客户端（push/download，不限超时；nil 用无超时客户端）。
	HTTPBig *http.Client
	// Attempts 设备调用重试次数（Node deviceCall 共试 3 次；0 = 单次不重试，测试/直连构造用）。
	Attempts int
	// RetryGap 重试间隔（Node RETRY_GAP_MS 1 分钟；0 = 不等待，测试用）。
	RetryGap time.Duration
}

// std 常规客户端。
func (f *GlkvmFileshare) std() *http.Client {
	if f.HTTP != nil {
		return f.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// big 大文件客户端。
func (f *GlkvmFileshare) big() *http.Client {
	if f.HTTPBig != nil {
		return f.HTTPBig
	}
	return &http.Client{}
}

// doOnce 设备 fileshare 单次转发（big=true 走大文件客户端）；返回状态码与响应体。
func (f *GlkvmFileshare) doOnce(ctx context.Context, dev DeviceRef, username, method, path, contentType string, payload []byte, big bool) (int, []byte, error) {
	ctx = integrations.WithServiceName(ctx, "usb")
	if f == nil || f.Platform == nil {
		return 0, nil, errors.New("GLKVM Cloud 未配置（GLKVM_URL / GLKVM_PASSWORD）")
	}
	ps, err := f.Platform.ProxySession(ctx, dev.ID, username)
	if err != nil {
		return 0, nil, err
	}
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, ps.Origin+path, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Cookie", ps.Cookie)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	client := f.std()
	if big {
		client = f.big()
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, data, err
}

// do 设备 fileshare 转发，按 Node deviceCall 口径失败重试：每次换新代理会话，共试 attempts 次
// （默认 1 次；NewUsbLink 装配为 3 次）、间隔 RetryGap（1 分钟）；状态码被 ok 接受或重试耗尽才返回
// （下载 404 视文件不存在、不重试，同 Node validateStatus 口径）。
func (f *GlkvmFileshare) do(ctx context.Context, dev DeviceRef, username, method, path, contentType string, payload []byte, big bool, ok func(status int) bool) (int, []byte, error) {
	attempts := 1
	if f.Attempts > 0 {
		attempts = f.Attempts
	}
	var status int
	var data []byte
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		status, data, err = f.doOnce(ctx, dev, username, method, path, contentType, payload, big)
		if err == nil && ok(status) {
			return status, data, nil
		}
		if attempt < attempts && f.RetryGap > 0 {
			select {
			case <-ctx.Done():
				return status, data, ctx.Err()
			case <-time.After(f.RetryGap):
			}
		}
	}
	return status, data, err
}

// okStatus 仅 200 接受（mount/list/delete/push）。
func okStatus(status int) bool { return status == http.StatusOK }

// okDownload 200/404 接受（404 = 文件不存在，区别于链路故障）。
func okDownload(status int) bool {
	return status == http.StatusOK || status == http.StatusNotFound
}

// fileshareErr 设备错误信封 {ok:false,error} → 可读错误。
func fileshareErr(status int, data []byte, err error) error {
	if err != nil {
		return err
	}
	var obj struct {
		Error string `json:"error"`
	}
	if len(data) > 0 && json.Unmarshal(data, &obj) == nil && obj.Error != "" {
		return errors.New(obj.Error)
	}
	return fmt.Errorf("设备访问失败（HTTP %d）", status)
}

// MountToHost POST /api/fileshare/mount。
func (f *GlkvmFileshare) MountToHost(ctx context.Context, dev DeviceRef, username string) error {
	status, data, err := f.do(ctx, dev, username, http.MethodPost, "/api/fileshare/mount", "", nil, false, okStatus)
	if err != nil || status != http.StatusOK {
		return fileshareErr(status, data, err)
	}
	return nil
}

// DownloadFile GET /api/fileshare/download/{name}（404 → nil,nil；共享中由设备侧自动断开）。
func (f *GlkvmFileshare) DownloadFile(ctx context.Context, dev DeviceRef, username, name string) ([]byte, error) {
	status, data, err := f.do(ctx, dev, username, http.MethodGet,
		"/api/fileshare/download/"+url.PathEscape(name), "", nil, true, okDownload)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, fileshareErr(status, data, nil)
	}
	return data, nil
}

// ListFiles GET /api/fileshare/list（响应 {ok,files:[{name,size}]}）。
func (f *GlkvmFileshare) ListFiles(ctx context.Context, dev DeviceRef, username string) ([]string, error) {
	status, data, err := f.do(ctx, dev, username, http.MethodGet, "/api/fileshare/list", "", nil, false, okStatus)
	if err != nil || status != http.StatusOK {
		return nil, fileshareErr(status, data, err)
	}
	var obj struct {
		OK    bool `json:"ok"`
		Files []struct {
			Name string `json:"name"`
		} `json:"files"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("设备文件列表解析失败：%w", err)
	}
	names := make([]string, 0, len(obj.Files))
	for _, f := range obj.Files {
		names = append(names, f.Name)
	}
	return names, nil
}

// DeleteFiles POST /api/fileshare/delete（JSON {"names":[...]}）。
func (f *GlkvmFileshare) DeleteFiles(ctx context.Context, dev DeviceRef, username string, names []string) error {
	payload, _ := json.Marshal(map[string]any{"names": names})
	status, data, err := f.do(ctx, dev, username, http.MethodPost, "/api/fileshare/delete",
		"application/json", payload, false, okStatus)
	if err != nil || status != http.StatusOK {
		return fileshareErr(status, data, err)
	}
	return nil
}

// PushFile POST /api/fileshare/push（multipart 字段 files，单文件直传）。
// 设备 200 响应 {ok,saved:[{name,size}]} 必须恰好一项同名且大小等于 payload 长度；
// *_cmd.json 还须 payload 为顶层 JSON 对象，并在上传成功后立即回读逐字节比对。
// 上传已发起后的校验失败（saved 不可信 / 回读不符或缺失）都 best-effort 删除同名文件
// （否则调用方 RestoreShare 后被控机会认领坏指令），并保留原错误、不掩盖清理失败；
// 上传前的 JSON 校验失败不发请求、不动旧文件（旧文件可能是上一轮仍在被认领的合法指令）。
func (f *GlkvmFileshare) PushFile(ctx context.Context, dev DeviceRef, username, name string, data []byte) error {
	cmd := cmdFileName(name)
	if cmd {
		if err := checkCmdJSON(data); err != nil {
			return fmt.Errorf("指令文件 %s %w，已中止上传", name, err)
		}
	}
	// discard 已发起上传后的校验失败：先 best-effort 清理同名残留，再返回原错误。
	discard := func(err error) error {
		if derr := f.DeleteFiles(ctx, dev, username, []string{name}); derr != nil {
			return fmt.Errorf("%w（清理同名残留失败：%v）", err, derr)
		}
		return err
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("files", name)
	if err != nil {
		return err
	}
	if _, err := fw.Write(data); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	status, resp, err := f.do(ctx, dev, username, http.MethodPost, "/api/fileshare/push",
		mw.FormDataContentType(), buf.Bytes(), true, okStatus)
	if err != nil || status != http.StatusOK {
		return fileshareErr(status, resp, err)
	}
	if err := verifySaved(resp, name, len(data)); err != nil {
		return discard(err)
	}
	if !cmd {
		return nil
	}
	back, err := f.DownloadFile(ctx, dev, username, name)
	if err != nil {
		return discard(fmt.Errorf("回读 %s 失败：%w", name, err))
	}
	if back == nil {
		return discard(fmt.Errorf("回读 %s 失败：设备侧不存在刚上传的文件", name))
	}
	if !bytes.Equal(back, data) {
		return discard(fmt.Errorf("回读 %s 内容不一致（回读 %d 字节 ≠ 上传 %d 字节），已中止", name, len(back), len(data)))
	}
	return nil
}

// cmdFileName 指令文件名判定（{app}_cmd.json，见 usbcmd.CmdName）。
func cmdFileName(name string) bool { return strings.HasSuffix(name, "_cmd.json") }

// checkCmdJSON 指令 payload 合法性：必须合法 JSON 且顶层为对象（拒绝 null/数组/字符串/数字，
// 被控机 agent 按对象取字段）。
func checkCmdJSON(data []byte) error {
	if !json.Valid(data) {
		return errors.New("不是合法 JSON")
	}
	var head any
	if err := json.Unmarshal(data, &head); err != nil {
		return errors.New("不是合法 JSON")
	}
	if _, ok := head.(map[string]any); !ok {
		return errors.New("JSON 顶层不是对象")
	}
	return nil
}

// verifySaved 校验设备 push 响应 {ok,saved:[{name,size}]}（设备侧 fileshare.py 信封）：
// 必须恰好一项同名且大小等于上传长度，否则视为落盘不可信。
func verifySaved(resp []byte, name string, size int) error {
	var obj struct {
		OK    bool `json:"ok"`
		Saved []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"saved"`
	}
	if err := json.Unmarshal(resp, &obj); err != nil {
		return fmt.Errorf("设备上传响应解析失败：%w", err)
	}
	if !obj.OK {
		return fileshareErr(http.StatusOK, resp, nil)
	}
	matched := 0
	for _, s := range obj.Saved {
		if s.Name != name {
			continue
		}
		matched++
		if s.Size != int64(size) {
			return fmt.Errorf("设备保存 %s 大小不符（设备 %d ≠ 上传 %d 字节），已中止", name, s.Size, size)
		}
	}
	if matched != 1 {
		return fmt.Errorf("设备未确认保存 %s（saved 同名项 %d），已中止", name, matched)
	}
	return nil
}

// ---- DeviceFinder 真实现：GLKVM 平台设备定位 ----

// GlkvmFinder DeviceFinder 真实现：kvm.Platform.ListDevices 按 ddns 精确匹配；
// mac 非空时归一化（去非十六进制、小写）比对，不符报错防设备更换后误操作他机。
type GlkvmFinder struct {
	Platform kvm.Platform
}

// FindByDdns 定位设备。
func (g *GlkvmFinder) FindByDdns(ctx context.Context, username, ddns, mac string) (DeviceRef, error) {
	if g == nil || g.Platform == nil {
		return DeviceRef{}, errors.New("GLKVM Cloud 未配置（GLKVM_URL / GLKVM_PASSWORD）")
	}
	items, err := g.Platform.ListDevices(ctx, url.Values{}, username)
	if err != nil {
		return DeviceRef{}, err
	}
	for _, d := range items {
		if strings.TrimSpace(d.Ddns) != ddns {
			continue
		}
		if want := normMac(mac); want != "" {
			if got := normMac(rawString(d.Raw["mac"])); got != want {
				return DeviceRef{}, fmt.Errorf(
					"设备 %s MAC 校验不符（平台侧 %s ≠ 配置 %s），已中止防误操作", ddns, got, mac)
			}
		}
		return DeviceRef{ID: fmt.Sprintf("%d", d.ID), Ddns: d.Ddns}, nil
	}
	return DeviceRef{}, fmt.Errorf("未找到设备 ddns=%s（账号 %s 平台不可见或设备已下线）", ddns, username)
}

// normMac MAC 归一化（去非十六进制字符、转小写；空串原样返回）。
func normMac(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// rawString 取字符串字段（非字符串强转）。
func rawString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// 编译期契约：UsbLink 满足 tree/crossing 两侧注入接口；两实现满足各自小接口。
var (
	_ DeviceClient = (*GlkvmFileshare)(nil)
	_ DeviceFinder = (*GlkvmFinder)(nil)
)
