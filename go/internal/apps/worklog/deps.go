// 出工日志外部依赖接口：Dify 照片识别、地理服务、对象存储、人工审核通知、派车单同步、
// 网盘保存与商旅费用照片联动；全部可注入，测试用 fake 实现（httptest / 内存桩），不访问真实云服务。
package worklog

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"shade/internal/auth"
)

// VerifyResult Dify 水印识别结果（仅识别；日期/地点比对在后端 CheckWatermark）。
type VerifyResult struct {
	OK          bool
	WorkContent string
	Time        string
	Weather     string
	Location    string
	Lng         string
	Lat         string
}

// DifyClient 照片水印识别工作流（blocking /v1/workflows/run）；任何异常归 OK=false，不抛出。
type DifyClient interface {
	VerifyPhoto(ctx context.Context, username, date, destination, url string) VerifyResult
}

// LocationWeather 地理服务取值（任一步失败为空串，前端留空手填）。
type LocationWeather struct {
	Location string `json:"location"`
	Weather  string `json:"weather"`
}

// GeoService 高德「地点 + 天气」取值（逆地理编码 + 实时天气）。
type GeoService interface {
	FetchLocationWeather(ctx context.Context, lng, lat string) LocationWeather
}

// ObjectStore 腾讯云 COS 风格对象存储（水印照片 / 备注附件）。
type ObjectStore interface {
	// PutBuffer 上传对象（CacheControl private,no-cache 由实现决定）。
	PutBuffer(ctx context.Context, key string, buf []byte, contentType string) error
	// DeleteObject 删除对象（不存在视为成功）。
	DeleteObject(ctx context.Context, key string) error
	// PublicURL 按对象键生成访问地址（域名配置变更后即时生效）。
	PublicURL(key string) string
}

// Notifier 照片需人工审核时的班组通知（站内 + 群消息；通知失败不阻断主流程）。
type Notifier interface {
	NotifyManualReview(ctx context.Context, photoID int64) error
}

// DispatchSyncer 派车单每日同步执行器（可注入；nil 时 sync-now 返回依赖未接入）。
type DispatchSyncer interface {
	// PrepareNow 每日准备：锁定设备并挂载 U 盘至被控机（Node prepareJob）；无开启班组跳过、
	// 失败解禁并通知后返回可读 error（排程侧记日志）。
	PrepareNow(ctx context.Context) error
	// SyncNow 立即同步一次派车单（取件建卡）；返回同步条数。
	// 当日无文件按旧版口径发「今日无文件」通知后返回 (0, nil)（正常结果，sync-now 不报 500）；
	// 取件/解析/建卡失败发「派车单同步失败」通知并返回可读 error（Node fetchJob catch 口径）。
	SyncNow(ctx context.Context, teamID int64) (int, error)
}

// ActivePhotoOp 查询卡片是否有进行中的商旅照片任务（删卡/传图前拦截）。
type ActivePhotoOp interface {
	ActivePhotoOp(teamID, entryID int64) bool
}

// NetdiskSaveRequest 保存到网盘入参（与 safeday 同契约）。
type NetdiskSaveRequest struct {
	Space string // my（缺省，我的空间）/ public（公共区，按班组隔离）
	Dir   string // 目标空间内相对路径（可多级），空串=空间根目录
	Name  string // 文件名
	Body  []byte // 文件内容
}

// NetdiskSaver 网盘保存适配层（netdisk 应用侧实现；可注入；nil 时 save-netdisk 报未接入）。
type NetdiskSaver interface {
	SaveToNetdisk(ctx context.Context, user auth.Identity, req NetdiskSaveRequest) (string, error)
}

// NetdiskSaveError 网盘保存业务失败（权限/参数类，随响应信封原样下发）。
type NetdiskSaveError struct {
	Status  int
	Code    int
	Message string
}

func (e *NetdiskSaveError) Error() string { return e.Message }

// PhotoOpRec 商旅照片任务执行期回调（记录失败人名 / 产物照片，供端侧轮询 /op/status 收尾）。
type PhotoOpRec interface {
	// SetFailedName 记录远端失败的成员姓名（收尾提示用）。
	SetFailedName(name string)
	// SetPhoto 记录本地产物照片（小程序任务收尾存相册用）。
	SetPhoto(photoID int64, photoURL string)
}

// SgccPhotoRemote 商旅费用照片远端联动（sgcc 侧实现；可注入）。
// 全部「远端先行」口径：任一远端失败即抛错，本地零写入，由调用方保持不变。
type SgccPhotoRemote interface {
	// UploadToMembers 把照片上传到各绑定成员的商旅费用照片；返回 sgcc_img_id 留存（链接 JSON 串）。
	UploadToMembers(ctx context.Context, teamID int64, logDate string, names []string, buf []byte) (string, error)
	// RemoveMembers 把照片从 removed 各人的商旅费用照片剔除（调人名剔除场景）。
	RemoveMembers(ctx context.Context, photoID int64, removed []string) error
	// SyncMembers 以全量名单补传商旅费用照片关联（已链接成员跳过、缺链接自愈）。
	SyncMembers(ctx context.Context, photoID int64, names []string) error
	// Unlink 解除照片与全部商旅费用照片的关联（删除场景）。
	Unlink(ctx context.Context, photoID int64) error
}

// PhotoOpStarter 商旅照片任务队列（sgcc 侧实现；可注入）。
// 登记即返回 opId，fn 入后台串行队列执行（远端先行 + 本地收尾），进度与失败由实现侧供端侧轮询；
// nil 时照片接口同步执行、响应不带 opId。
type PhotoOpStarter interface {
	StartPhotoOp(kind string, teamID, entryID, photoID int64, fn func(ctx context.Context, rec PhotoOpRec) error) int64
}

// remoteFail 远端先行失败错误（本地零写入；同步路径下按上游失败下发）。
type remoteFail struct{ msg string }

func (e *remoteFail) Error() string { return e.msg }

// writeRunErr 照片收尾执行失败出口：远端先行失败 502 上游失败，其余按内部错误。
func writeRunErr(w http.ResponseWriter, err error) {
	var rf *remoteFail
	if errors.As(err, &rf) {
		fail(w, http.StatusBadGateway, codeUpstream, err.Error())
		return
	}
	writeErr(w, err)
}

// maxVerifyRetry Dify 识别（失败/空施工内容）最大重试次数（不含首次调用）。
const maxVerifyRetry = 3

// verifyUsable 识别结果有效 = 调用成功且施工内容非空（空内容触发重试）。
func verifyUsable(vr VerifyResult) bool {
	return vr.OK && strings.TrimSpace(vr.WorkContent) != ""
}

// recognize Dify 识别（含失败/空内容重试，至多 maxVerifyRetry 次）：返回最终一次结果。
func recognize(ctx context.Context, client DifyClient, username, date, destination, url string) VerifyResult {
	var vr VerifyResult
	if client == nil {
		return vr
	}
	for attempt := 0; attempt <= maxVerifyRetry; attempt++ {
		vr = client.VerifyPhoto(ctx, username, date, destination, url)
		if verifyUsable(vr) {
			return vr
		}
		select {
		case <-ctx.Done():
			return vr
		case <-time.After(time.Millisecond): // 真实间隔由实现侧控制；测试不真睡
		}
	}
	return vr
}
