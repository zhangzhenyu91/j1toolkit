package office

// 文档源可注入桥：main 以各业务模块能力装配后注入 Module.Sources / Module.Netdisk——
// 出工日志工作单（wls）/ 安全日产物（sdr）/ 出工日志备注附件（wlr）/ 树障统计表（thd）/ 交跨统计表（crs）
// 走 DocSource+Namer 桥，网盘文件（ndp）走 NetdiskDoc 桥。
// 票据 ref 为结构化 JSON 载荷：签发期由桥 Resolve 定名并校验（参数/归属/汇总前核验），回源期由桥 Fetch 取数；
// 桥错误统一归一（*RefError 直出业务码；「不存在」归一 ErrDocNotFound）。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"shade/internal/auth"
)

// RefError 引用业务错误（桥返回，签发/回源按 Status/Code 直出应答；缺省 400/40001）。
type RefError struct {
	Status  int
	Code    int
	Message string
}

func (e *RefError) Error() string { return e.Message }

// IsNotFounder 「不存在」判定（netdisk 桥哨兵 netdisk.NotFoundError 满足；不强制实现）。
type IsNotFounder interface{ IsNotFound() bool }

// mapDocErr 桥错误归一：*RefError / ErrDocNotFound 原样；「不存在/路径不合法」语义按 Node 口径
// 归一为对应业务码（路径不合法 40001、不存在 40400，保留原可读文案）。
func mapDocErr(err error) error {
	if err == nil {
		return nil
	}
	var re *RefError
	if errors.As(err, &re) || errors.Is(err, ErrDocNotFound) {
		return err
	}
	msg := err.Error()
	if nf, ok := err.(IsNotFounder); ok && nf.IsNotFound() {
		if msg == "路径不合法" || strings.HasPrefix(msg, "参数错误") {
			return &RefError{Status: http.StatusBadRequest, Code: codeParam, Message: msg}
		}
		if msg == "" {
			msg = "文件或目录不存在"
		}
		return &RefError{Status: http.StatusNotFound, Code: codeNotFound, Message: msg}
	}
	if strings.Contains(msg, "不存在") || strings.Contains(strings.ToLower(msg), "not found") {
		return &RefError{Status: http.StatusNotFound, Code: codeNotFound, Message: msg}
	}
	return err
}

// refFail 把桥错误映射为 /config 应答（RefError 直出；不存在 404；未配置 503；其余 502）。
func refFail(w http.ResponseWriter, err error) {
	var re *RefError
	if errors.As(err, &re) {
		status, code := re.Status, re.Code
		if status == 0 {
			status = 400
		}
		if code == 0 {
			code = codeParam
		}
		fail(w, status, code, re.Message)
		return
	}
	if errors.Is(err, ErrDocNotFound) {
		fail(w, 404, codeNotFound, "文档不存在")
		return
	}
	if strings.Contains(err.Error(), "未配置") {
		fail(w, http.StatusServiceUnavailable, codeNotConfigured, err.Error())
		return
	}
	fail(w, 502, codeUpstream, "在线编辑服务异常，请稍后再试")
}

// ---- 票据 ref 载荷（JSON；office.html 扁平 query 由 handleConfig 组装） ----

// WlsRef 出工日志工作单引用（src=wls）。
type WlsRef struct {
	Sheet string `json:"sheet"`           // task 工作任务单 / fee 费用汇总 / dispatch 派车汇总
	From  string `json:"from"`            // YYYY-MM-DD
	To    string `json:"to"`              // YYYY-MM-DD
	Team  int64  `json:"team"`            // 班组 id（0=签发人所属班组，桥内回落）
	Force bool   `json:"force,omitempty"` // 跳过汇总前核验（签发侧固化，回源不再核验）
}

// SdrRef 安全日记录产物引用（src=sdr）。
type SdrRef struct {
	ID string `json:"id"`
}

// WlrRef 出工日志备注附件引用（src=wlr）。
type WlrRef struct {
	ID   string `json:"id"`  // 日志条目 id
	Key  string `json:"key"` // 附件 cos_key（不可变，内容指纹）
	Team int64  `json:"team"`
}

// LineRef 班组统计表引用（src=thd 树障 / src=crs 交跨共用）。
type LineRef struct {
	Team  int64  `json:"team"`
	Line  string `json:"line,omitempty"`
	Split bool   `json:"split,omitempty"`
}

// EncodeRef 引用 → 票据 ref 字符串。
func EncodeRef(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ---- 能力接口（main 以业务模块注入）与桥 ----

// Namer 签发期解析（可选）：定名 + 签发校验（日期范围/归属/汇总前核验等）；
// 返回名为空时回落 body.Name。错误用 *RefError 表达业务码。
type Namer interface {
	Resolve(ctx context.Context, ident auth.Identity, ref string) (name string, err error)
}

// SheetBuilder 出工日志工作单产物能力（worklog 模块注入；wls 源）。
type SheetBuilder interface {
	// Resolve 签发期校验（含 task/fee 汇总前核验 40901、生成前权限）并返回展示名。
	Resolve(ctx context.Context, ident auth.Identity, ref WlsRef) (string, error)
	// Fetch 回源即时生成产物（票据即授权，签发侧已核验）。
	Fetch(ctx context.Context, ref WlsRef) ([]byte, error)
}

// SafedayFiles 安全日记录产物能力（safeday 模块注入；sdr 源）。
type SafedayFiles interface {
	// Resolve 签发期校验归属/存在并返回产物展示名。
	Resolve(ctx context.Context, ident auth.Identity, ref SdrRef) (string, error)
	// Fetch 回源取产物字节（票据即授权）。
	Fetch(ctx context.Context, ref SdrRef) ([]byte, error)
}

// RemarkFiles 出工日志备注附件能力（worklog 模块注入；wlr 源）。
type RemarkFiles interface {
	// Resolve 签发期校验附件存在并返回展示名。
	Resolve(ctx context.Context, ident auth.Identity, ref WlrRef) (string, error)
	// Fetch 回源取附件字节（票据即授权）。
	Fetch(ctx context.Context, ref WlrRef) ([]byte, error)
}

// TeamExport 班组统计表能力（treehazard / crossing 模块注入；thd / crs 源）。
type TeamExport interface {
	// Resolve 签发期校验班组并返回统计表展示名。
	Resolve(ctx context.Context, ident auth.Identity, ref LineRef) (string, error)
	// Fetch 回源即时生成统计表（与 /export 下载同口径）。
	Fetch(ctx context.Context, ref LineRef) ([]byte, error)
}

// NetdiskBridge 网盘读写能力（netdisk.Module 的 ReadFileBytes / WriteFileBytes / DocKey
// 方法集天然满足；main 直接注入 *netdisk.Module）。
type NetdiskBridge interface {
	ReadFileBytes(ctx context.Context, space, username string, teamID int64, relPath string) ([]byte, string, error)
	WriteFileBytes(ctx context.Context, space, username string, teamID int64, relPath string, data []byte) error
	DocKey(ctx context.Context, space, username string, teamID int64, relPath string) (string, error)
}

// NetdiskKeyer 网盘源签发期文档 key（可选实现；NewNetdiskDoc 产物满足）：
// sha1(绝对路径|size|modified)，内容变即滚动，OnlyOffice 按 key 归并会话与缓存。
type NetdiskKeyer interface {
	DocKeyOf(ctx context.Context, p NDP) (string, error)
}

// worklogSheetSource wls 源桥。
type worklogSheetSource struct{ b SheetBuilder }

func (s *worklogSheetSource) Resolve(ctx context.Context, ident auth.Identity, ref string) (string, error) {
	var r WlsRef
	if err := json.Unmarshal([]byte(ref), &r); err != nil {
		return "", &RefError{Message: "引用参数错误"}
	}
	name, err := s.b.Resolve(ctx, ident, r)
	return name, mapDocErr(err)
}

func (s *worklogSheetSource) Fetch(ctx context.Context, ref string) ([]byte, string, error) {
	var r WlsRef
	if err := json.Unmarshal([]byte(ref), &r); err != nil {
		return nil, "", &RefError{Status: 401, Code: codeBadTicket, Message: "回源票据无效或已过期"}
	}
	data, err := s.b.Fetch(ctx, r)
	return data, "", mapDocErr(err)
}

// safedaySource sdr 源桥。
type safedaySource struct{ b SafedayFiles }

func (s *safedaySource) Resolve(ctx context.Context, ident auth.Identity, ref string) (string, error) {
	var r SdrRef
	if err := json.Unmarshal([]byte(ref), &r); err != nil {
		return "", &RefError{Message: "引用参数错误"}
	}
	name, err := s.b.Resolve(ctx, ident, r)
	return name, mapDocErr(err)
}

func (s *safedaySource) Fetch(ctx context.Context, ref string) ([]byte, string, error) {
	var r SdrRef
	if err := json.Unmarshal([]byte(ref), &r); err != nil {
		return nil, "", &RefError{Status: 401, Code: codeBadTicket, Message: "回源票据无效或已过期"}
	}
	data, err := s.b.Fetch(ctx, r)
	return data, "", mapDocErr(err)
}

// remarkSource wlr 源桥。
type remarkSource struct{ b RemarkFiles }

func (s *remarkSource) Resolve(ctx context.Context, ident auth.Identity, ref string) (string, error) {
	var r WlrRef
	if err := json.Unmarshal([]byte(ref), &r); err != nil {
		return "", &RefError{Message: "引用参数错误"}
	}
	name, err := s.b.Resolve(ctx, ident, r)
	return name, mapDocErr(err)
}

func (s *remarkSource) Fetch(ctx context.Context, ref string) ([]byte, string, error) {
	var r WlrRef
	if err := json.Unmarshal([]byte(ref), &r); err != nil {
		return nil, "", &RefError{Status: 401, Code: codeBadTicket, Message: "回源票据无效或已过期"}
	}
	data, err := s.b.Fetch(ctx, r)
	return data, "", mapDocErr(err)
}

// lineSource thd / crs 源桥（共用 TeamExport 能力）。
type lineSource struct{ b TeamExport }

func (s *lineSource) Resolve(ctx context.Context, ident auth.Identity, ref string) (string, error) {
	var r LineRef
	if err := json.Unmarshal([]byte(ref), &r); err != nil {
		return "", &RefError{Message: "引用参数错误"}
	}
	name, err := s.b.Resolve(ctx, ident, r)
	return name, mapDocErr(err)
}

func (s *lineSource) Fetch(ctx context.Context, ref string) ([]byte, string, error) {
	var r LineRef
	if err := json.Unmarshal([]byte(ref), &r); err != nil {
		return nil, "", &RefError{Status: 401, Code: codeBadTicket, Message: "回源票据无效或已过期"}
	}
	data, err := s.b.Fetch(ctx, r)
	return data, "", mapDocErr(err)
}

// netdiskDoc ndp 源桥（可编辑协同 + 保存写回 + 签发期文档 key）。
type netdiskDoc struct{ b NetdiskBridge }

func (d *netdiskDoc) Fetch(ctx context.Context, p NDP, _ string) ([]byte, string, error) {
	data, contentType, err := d.b.ReadFileBytes(ctx, p.Space, p.User, p.Team, p.Path)
	return data, contentType, mapDocErr(err)
}

func (d *netdiskDoc) WriteBack(ctx context.Context, p NDP, data []byte) error {
	return mapDocErr(d.b.WriteFileBytes(ctx, p.Space, p.User, p.Team, p.Path, data))
}

// DocKeyOf 签发期文档 key（NetdiskKeyer）。
func (d *netdiskDoc) DocKeyOf(ctx context.Context, p NDP) (string, error) {
	key, err := d.b.DocKey(ctx, p.Space, p.User, p.Team, p.Path)
	return key, mapDocErr(err)
}

// NewWorklogSheetSource 出工日志工作单源（wls）。
func NewWorklogSheetSource(b SheetBuilder) DocSource { return &worklogSheetSource{b: b} }

// NewSafedaySource 安全日记录产物源（sdr）。
func NewSafedaySource(b SafedayFiles) DocSource { return &safedaySource{b: b} }

// NewWorklogRemarkSource 出工日志备注附件源（wlr）。
func NewWorklogRemarkSource(b RemarkFiles) DocSource { return &remarkSource{b: b} }

// NewTreehazardSource 树障统计表源（thd）。
func NewTreehazardSource(b TeamExport) DocSource { return &lineSource{b: b} }

// NewCrossingSource 交跨统计表源（crs）。
func NewCrossingSource(b TeamExport) DocSource { return &lineSource{b: b} }

// NewNetdiskDoc 网盘来源桥（ndp，可编辑协同 + 保存写回）。
func NewNetdiskDoc(b NetdiskBridge) NetdiskDoc { return &netdiskDoc{b: b} }

// NewSources 五源一次性装配（nil 跳过）：main 一行注入 Module.Sources。
func NewSources(sheet SheetBuilder, safeday SafedayFiles, remark RemarkFiles, thd, crs TeamExport) map[string]DocSource {
	out := map[string]DocSource{}
	if sheet != nil {
		out["wls"] = NewWorklogSheetSource(sheet)
	}
	if safeday != nil {
		out["sdr"] = NewSafedaySource(safeday)
	}
	if remark != nil {
		out["wlr"] = NewWorklogRemarkSource(remark)
	}
	if thd != nil {
		out["thd"] = NewTreehazardSource(thd)
	}
	if crs != nil {
		out["crs"] = NewCrossingSource(crs)
	}
	return out
}

// ---- body.src 归一与宽松字段（office.html query 全字符串透传口径） ----

// srcAlias body.src 归一：Node 扁平命名（worklog-sheet 等）与票据代码（wls 等）都认；缺省 = 网盘。
var srcAlias = map[string]string{
	"":               "ndp",
	"ndp":            "ndp",
	"netdisk":        "ndp",
	"worklog-sheet":  "wls",
	"wls":            "wls",
	"safeday":        "sdr",
	"sdr":            "sdr",
	"worklog-remark": "wlr",
	"wlr":            "wlr",
	"treehazard":     "thd",
	"thd":            "thd",
	"crossing":       "crs",
	"crs":            "crs",
}

// flexBool 宽松布尔：true/false/1/0/"1"/"true" 等（query 透传全是字符串）。
type flexBool bool

func (f *flexBool) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	switch strings.ToLower(s) {
	case "true", "1":
		*f = true
	default:
		*f = false
	}
	return nil
}

// flexInt64 宽松整数：数字或数字字符串；解析失败按 0（回落签发人班组）。
type flexInt64 int64

func (f *flexInt64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		*f = 0
		return nil
	}
	*f = flexInt64(n)
	return nil
}

// flexString 宽松字符串：数字/字符串都收（id 类参数）。
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*f = ""
		return nil
	}
	*f = flexString(strings.Trim(s, `"`))
	return nil
}
