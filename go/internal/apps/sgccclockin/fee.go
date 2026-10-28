// 费用模板装配与费用凭证上传：DtComponentListBean 78 字段表（Moshi 语义：原始类型恒输出、
// 装箱/String/List 仅非空）、saveFeeInfoNew 整树重存（PatrolFeeModel.d 实锤结构）、
// uploadImageV2 multipart 直传。口径对照 server/src/sgccclockin/protocol.js（勿改）。
package sgccclockin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// compField 78 字段表项：kind i=int j=long z=bool o=装箱/String/List/Bean（仅非空输出）。
type compField struct {
	name string
	kind byte
}

// compFields 取自 classes4.dex ApplyDetailBean$DataBean$DtComponentListBean
// （逆向分析仓 private/esgcc/sgcc，与 server/src/sgccclockin/comp_fields.json 同源）。
var compFields = []compField{
	{name: "advanceLowerList", kind: 'o'},
	{name: "bankId", kind: 'o'},
	{name: "bankJointSign", kind: 'o'},
	{name: "bankName", kind: 'o'},
	{name: "bankUserName", kind: 'o'},
	{name: "belongToType", kind: 'i'},
	{name: "canModify", kind: 'z'},
	{name: "cardNo", kind: 'o'},
	{name: "cardReplacementDate", kind: 'o'},
	{name: "cardReplacementReason", kind: 'o'},
	{name: "code", kind: 'o'},
	{name: "columnType", kind: 'i'},
	{name: "companyId", kind: 'i'},
	{name: "componentid", kind: 'i'},
	{name: "costAmountRequired", kind: 'o'},
	{name: "costInfoModify", kind: 'i'},
	{name: "createEnrollId", kind: 'o'},
	{name: "createtime", kind: 'j'},
	{name: "currencyCompany", kind: 'o'},
	{name: "currencyType", kind: 'o'},
	{name: "data", kind: 'o'},
	{name: "dateformat", kind: 'o'},
	{name: "detailList", kind: 'o'},
	{name: "endDate", kind: 'j'},
	{name: "enrollId", kind: 'o'},
	{name: "erpAccount", kind: 'o'},
	{name: "everyCostList", kind: 'o'},
	{name: "fileSortBeans", kind: 'o'},
	{name: "fileUpload", kind: 'z'},
	{name: "hasCanClockInReim", kind: 'o'},
	{name: "hasCanInterTravelReim", kind: 'o'},
	{name: "hasContainSafeTrain", kind: 'i'},
	{name: "hotelOverFlagCanEdit", kind: 'o'},
	{name: "id", kind: 'i'},
	{name: "isBase", kind: 'i'},
	{name: "isDeducTax", kind: 'o'},
	{name: "isDefault", kind: 'o'},
	{name: "isEntrustConstruction", kind: 'z'},
	{name: "isPersonSupplier", kind: 'z'},
	{name: "isShowOutgoingApproval", kind: 'z'},
	{name: "justShow", kind: 'z'},
	{name: "label", kind: 'o'},
	{name: "logic", kind: 'o'},
	{name: "manualModification", kind: 'i'},
	{name: "maxlen", kind: 'i'},
	{name: "moneyTypeOptionsJsonObject", kind: 'o'},
	{name: "moneyTypeRequired", kind: 'o'},
	{name: "name", kind: 'o'},
	{name: "options", kind: 'o'},
	{name: "optionsJsonObject", kind: 'o'},
	{name: "otherCity", kind: 'z'},
	{name: "otherComponents", kind: 'o'},
	{name: "otherCostTypeDTOList", kind: 'o'},
	{name: "otherFlight", kind: 'o'},
	{name: "otherprop", kind: 'o'},
	{name: "otherpropJsonObject", kind: 'o'},
	{name: "payeeType", kind: 'o'},
	{name: "picBeans", kind: 'o'},
	{name: "pid", kind: 'i'},
	{name: "placeholder", kind: 'o'},
	{name: "purchaseCostList", kind: 'o'},
	{name: "rateRequired", kind: 'o'},
	{name: "reApproval", kind: 'o'},
	{name: "reason", kind: 'o'},
	{name: "reasonapply", kind: 'o'},
	{name: "required", kind: 'o'},
	{name: "ruleInfo", kind: 'o'},
	{name: "scopeBusiness", kind: 'o'},
	{name: "selectTravelType", kind: 'o'},
	{name: "shareFeeList", kind: 'o'},
	{name: "showList", kind: 'o'},
	{name: "startDate", kind: 'j'},
	{name: "style", kind: 'o'},
	{name: "supplierCode", kind: 'o'},
	{name: "supplierName", kind: 'o'},
	{name: "type", kind: 'o'},
	{name: "value", kind: 'o'},
	{name: "whetherProTripMsgFlag", kind: 'i'},
}

// jsTruthy JS Boolean(v) 真值口径（空串 false、"0" true、0 false）。
func jsTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case int:
		return t != 0
	case int64:
		return t != 0
	}
	return true
}

// toMoshi 按 Moshi 语义装配单个组件：I/J 恒输出数字（空→0）、Z 恒输出布尔（空→false）、
// 其余仅非空时输出（与真实 App 序列化一致）。
func toMoshi(comp map[string]any) map[string]any {
	out := make(map[string]any, len(compFields))
	for _, f := range compFields {
		v, exists := comp[f.name]
		if !exists {
			v = nil
		}
		switch f.kind {
		case 'i', 'j':
			if v == nil {
				out[f.name] = 0
			} else {
				out[f.name] = num(v)
			}
		case 'z':
			out[f.name] = jsTruthy(v)
		default:
			if v != nil {
				out[f.name] = v
			}
		}
	}
	return out
}

// compIDKey 组件 id 归一为 overrides 查找键（JSON 数字 10 → "10"）。
func compIDKey(v any) string {
	switch t := v.(type) {
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case string:
		return strings.TrimSpace(t)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// findComp 取模板指定 id 组件（无则 nil）。
func findComp(tpl map[string]any, id string) map[string]any {
	comps, _ := tpl["dtComponentList"].([]any)
	for _, c := range comps {
		cm, ok := c.(map[string]any)
		if ok && compIDKey(cm["id"]) == id {
			return cm
		}
	}
	return nil
}

// BuildFeeTplJSON 费用模板装配：组件经 toMoshi 输出（overrides {组件id: value串} 同时覆盖
// value/data，如 id=10 补助明细、id=5 上传图片），外层只留 id/name/createtime/type/no。
// 返回 dtContent 字符串（saveFeeInfoNew 的 dtContentDetail.dtContent）。
func BuildFeeTplJSON(clockTemplate map[string]any, overrides map[string]string) string {
	comps, _ := clockTemplate["dtComponentList"].([]any)
	outComps := make([]any, 0, len(comps))
	for _, c := range comps {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if ov, hit := overrides[compIDKey(cm["id"])]; hit {
			cp := make(map[string]any, len(cm)+2)
			for k2, v := range cm {
				cp[k2] = v
			}
			cp["value"] = ov
			cp["data"] = ov
			outComps = append(outComps, toMoshi(cp))
			continue
		}
		outComps = append(outComps, toMoshi(cm))
	}
	tplBean := map[string]any{
		"id":              clockTemplate["id"],
		"name":            clockTemplate["name"],
		"createtime":      clockTemplate["createtime"],
		"type":            clockTemplate["type"],
		"no":              clockTemplate["no"],
		"dtComponentList": outComps,
	}
	b, _ := json.Marshal(tplBean)
	return string(b)
}

// FeeTplParams 费用模板取参（城市参数取当日打卡流水；缺省模板可能不带默认成本中心）。
type FeeTplParams struct {
	ClockInDate string
	CityName    string
	CityCode    string
	Position    string
}

// GetFeeInfoNew 费用模板（提交装配的数据源）：decoded.data.body.clockTemplate 为原始模板树。
func (p *Protocol) GetFeeInfoNew(ctx context.Context, token string, params FeeTplParams, opts CallOpts) (map[string]any, error) {
	opts.Token = token
	r, err := p.CallJSONX(ctx, "clockin/getFeeInfoNew", map[string]any{
		"clockInDate": params.ClockInDate,
		"cityName":    params.CityName,
		"cityCode":    params.CityCode,
		"position":    params.Position,
		"isPostMark":  "0",
	}, opts)
	if err != nil {
		return nil, err
	}
	return r.Decoded, nil
}

// SaveFeeInfoNew 费用保存（PatrolFeeModel.d 实锤结构；slapp 通道）：
// overrides {组件id: value串} 覆盖组件 value/data，经 toMoshi 装配后整树重存。
func (p *Protocol) SaveFeeInfoNew(ctx context.Context, token, date string, clockTemplate map[string]any, overrides map[string]string, opts CallOpts) (map[string]any, error) {
	opts.Token = token
	opts.Tenant = "slapp"
	r, err := p.CallJSONX(ctx, "clockin/saveFeeInfoNew", map[string]any{
		"clockInDate":       date,
		"changeSubsidyFlag": 0,
		"dtContentDetail":   map[string]any{"dtContent": BuildFeeTplJSON(clockTemplate, overrides)},
	}, opts)
	if err != nil {
		return nil, err
	}
	return r.Decoded, nil
}

// ExtractClockTemplate 从 getFeeInfoNew 应答取模板树（decoded.data.body.clockTemplate）；无则 nil。
func ExtractClockTemplate(decoded map[string]any) map[string]any {
	if decoded == nil {
		return nil
	}
	data, _ := decoded["data"].(map[string]any)
	body, _ := data["body"].(map[string]any)
	tpl, _ := body["clockTemplate"].(map[string]any)
	return tpl
}

// CostAllocOpts 成本分配选项码↔文案（组件 id=4 的五个选项；商旅自动带出时可能只给名称/编码、
// 选中码缺失或误为文案，须归一否则保存不生效）。
var CostAllocOpts = map[string]string{
	"1": "成本中心",
	"2": "WBS",
	"3": "内部订单号",
	"4": "成本中心&WBS",
	"5": "成本中心&内部订单号",
}

// NormalizeCostAllocComp 归一 comp4 的 value/data：选项码空→默认「成本中心」(1)；误写为选项文案→
// 映射回选项码；value.value=选项码 / data.value=选项文案（两字段不对称，勿同串覆盖）。返回生效选项码。
func NormalizeCostAllocComp(comp map[string]any, v map[string]any) string {
	code := strings.TrimSpace(fmt.Sprintf("%v", v["value"]))
	if code == "" || code == "<nil>" {
		code = "1"
	} else if _, ok := CostAllocOpts[code]; !ok {
		for c, label := range CostAllocOpts {
			if label == fmt.Sprintf("%v", v["value"]) {
				code = c
				break
			}
		}
	}
	codeName := fmt.Sprintf("%v", v["costCenterName"])
	if codeName == "<nil>" {
		codeName = ""
	}
	codeCode := fmt.Sprintf("%v", v["costCenterCode"])
	if codeCode == "<nil>" {
		codeCode = ""
	}
	projectType := fmt.Sprintf("%v", v["projectType"])
	if projectType == "<nil>" {
		projectType = ""
	}
	projectTypeName := fmt.Sprintf("%v", v["projectTypeName"])
	if projectTypeName == "<nil>" {
		projectTypeName = ""
	}
	valB, _ := json.Marshal(map[string]any{
		"costCenterName":  codeName,
		"value":           code,
		"projectType":     projectType,
		"projectTypeName": projectTypeName,
		"costCenterCode":  codeCode,
	})
	dataB, _ := json.Marshal(map[string]any{
		"costCenterName":  codeName,
		"value":           CostAllocOpts[code],
		"projectType":     projectType,
		"projectTypeName": projectTypeName,
		"costCenterCode":  codeCode,
	})
	comp["value"] = string(valB)
	comp["data"] = string(dataB)
	return code
}

// ExtractCostCenter 提取成本分配（id=4）当前值；无则空串对。
func ExtractCostCenter(tpl map[string]any) (code, name string) {
	comp := findComp(tpl, "4")
	if comp == nil {
		return "", ""
	}
	s, _ := comp["value"].(string)
	if s == "" {
		return "", ""
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return "", ""
	}
	code = fmt.Sprintf("%v", v["costCenterCode"])
	if code == "<nil>" {
		code = ""
	}
	name = fmt.Sprintf("%v", v["costCenterName"])
	if name == "<nil>" {
		name = ""
	}
	return code, name
}

// SubsidyJSON 补助明细（id=10）组件值：{foodFee, arrive} 全字符串口径（JS String(number)）。
func SubsidyJSON(foodFee, transitFee float64) string {
	b, _ := json.Marshal(map[string]string{
		"foodFee": jsNumberString(foodFee),
		"arrive":  jsNumberString(transitFee),
	})
	return string(b)
}

// jsNumberString JS String(number) 口径（0→"0"、25.5→"25.5"、1000000→"1000000"）。
func jsNumberString(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// FeeImageItem 费用模板 id=5「上传图片」组件的单图元素。
type FeeImageItem struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// ParseFeeImageItem 单图元素解析：JSON 数组元素 id/url 键兼容（壹匣 {id,url}、App {fileInfoId,url}
// （另兼容 imageId/imageUrl））；无可识别图片 id 返回 nil（键名再漂移时识别不出，调用方据此跳过保护本地）。
func ParseFeeImageItem(it map[string]any) *FeeImageItem {
	if it == nil {
		return nil
	}
	id := firstStr(it, "fileInfoId", "id", "imageId")
	if id == "" {
		return nil
	}
	return &FeeImageItem{ID: id, URL: firstStr(it, "url", "imageUrl")}
}

// ParseFeeImageList 上传图片组件 value 解析（JSON 数组；非数组/坏 JSON 回空表）。
func ParseFeeImageList(value any) []map[string]any {
	s, _ := value.(string)
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(s), &arr); err != nil {
		return nil
	}
	return arr
}

func firstStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s := jsString(v); s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return ""
}

// UploadedImage uploadImageV2 返回（fileInfoId 关联进费用上传图片组件）。
type UploadedImage struct {
	FileInfoID string
	HTTPURL    string
}

// UploadImageV2 费用凭证照片上传（App 3.3.6 新口径，2026-09-25 HAR 实证；原 clockin/reimbEnclosure/add
// 已被平台弃用不再返回 id）：multipart 直传 gwslapizb——单字段 file、明文 JSON 响应（不经 RSA 解密）、
// 无 sign 头、uniID 走 slapp 池；文件名对齐 App 口径：东八区 yyyyMMddHHmmssSSS.jpg。
// 再经 SaveFeeInfoNew 上传图片组件（id=5）关联入当日费用。
func (p *Protocol) UploadImageV2(ctx context.Context, token string, buf []byte, opts CallOpts) (UploadedImage, error) {
	k, proxy := p.resolve(ctx)
	p.mu.Lock()
	p.syncFingerprint(k, proxy)
	uniID, err := p.takeUniIDLocked(ctx, k, proxy, true)
	p.mu.Unlock()
	if err != nil {
		return UploadedImage{}, err
	}
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	boundary := "Boundary+" + strings.ToUpper(hex.EncodeToString(rnd[:]))
	now := time.Now().UTC().Add(8 * time.Hour)
	filename := now.Format("20060102150405") + fmt.Sprintf("%03d", now.Nanosecond()/1e6) + ".jpg"
	body := make([]byte, 0, len(buf)+256)
	body = append(body, []byte(fmt.Sprintf(
		"--%s\r\nContent-Disposition: form-data; name=\"file\"; filename=\"%s\"\r\nContent-Type: image/jpeg\r\n\r\n",
		boundary, filename))...)
	body = append(body, buf...)
	body = append(body, []byte(fmt.Sprintf("\r\n--%s--\r\n", boundary))...)

	deviceType := opts.DeviceType
	if deviceType == "" {
		deviceType = "Pixel 7"
	}
	systemVersion := opts.SystemVersion
	if systemVersion == "" {
		systemVersion = "Android 13"
	}
	// 头集走 JSONXHeaders 公共内核（slapp 口径；multipart 无 sign 头，2026-10-09 按 3.3.7 HAR 复核一致）
	headers := k.JSONXHeaders("slapp", deviceType, systemVersion, opts.Mobile,
		"multipart/form-data; boundary="+boundary, token, uniID)
	status, raw, err := p.post(ctx, proxy, p.hostZB(), "fastdfs/upload/image/sample/v2", headers, string(body))
	if err != nil {
		return UploadedImage{}, err
	}
	var j map[string]any
	_ = json.Unmarshal([]byte(raw), &j)
	if status == 200 && num(j["statusCode"]) == 200 {
		data, _ := j["data"].(map[string]any)
		result, _ := data["result"].(map[string]any)
		if result != nil && jsTruthy(result["success"]) {
			if id := firstStr(result, "fileInfoId"); id != "" {
				return UploadedImage{FileInfoID: id, HTTPURL: firstStr(result, "httpUrl")}, nil
			}
		}
	}
	// 失败信息带 HTTP 状态码与平台 statusCode（区分 99001 风控笼统拒绝与 fastdfs 服务故障）
	why := firstStr(j, "msg")
	if why == "" {
		if data, _ := j["data"].(map[string]any); data != nil {
			why = firstStr(data, "msg")
		}
	}
	if why == "" {
		why = truncate(raw, 120)
	}
	if why == "" {
		why = fmt.Sprintf("HTTP %d", status)
	}
	why = truncate(why, 120)
	code := ""
	if j != nil {
		if v, ok := j["statusCode"]; ok && v != nil {
			code = fmt.Sprintf(" statusCode=%v", v)
		}
	}
	return UploadedImage{}, fmt.Errorf("商旅图片上传失败（HTTP %d%s）：%s", status, code, why)
}
