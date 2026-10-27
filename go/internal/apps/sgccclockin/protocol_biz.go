// 协议业务方法：dayNew（登录态探测/对账共用）、打卡（markNew/updateMark）、费用保存（saveFeeInfoNew）。
package sgccclockin

import (
	"context"
	"fmt"
)

// DayNew 某日打卡详情（token 校验探测也用它：statusCode=200 即登录态有效）。
// 平台应答不可解返回 (nil, nil)，由调用方按通道异常口径处理（不翻转 token_status）。
func (p *Protocol) DayNew(ctx context.Context, token, date string, opts CallOpts) (map[string]any, error) {
	opts.Token = token
	r, err := p.CallJSONX(ctx, "clockin/calendar/dayNew", map[string]any{
		"clockInDate":      date,
		"orderNo":          "",
		"postClockInNo":    "",
		"reimOrderNo":      "",
		"informantId":      "",
		"isSubmitPlatform": "",
	}, opts)
	if err != nil {
		return nil, err
	}
	if r.Decoded == nil {
		return nil, nil // 应答不可解
	}
	return r.Decoded, nil
}

// MarkClockin 打卡（seq 1 首次 markNew / 2 结束带 NextClockIn / 0 更新 updateMark）。
func (p *Protocol) MarkClockin(ctx context.Context, token string, payload map[string]any, opts CallOpts) (map[string]any, error) {
	opts.Token = token
	seq := int(num(fmtOf(payload["seq"])))
	if seq == 0 {
		// 更新打卡
		r, err := p.CallJSONX(ctx, "clockin/updateMark", map[string]any{
			"detailId":  fmtOf(payload["detailId"]),
			"cityCode":  fmtOf(payload["cityCode"]),
			"cityName":  fmtOf(payload["cityName"]),
			"position":  fmtOf(payload["position"]),
			"longitude": fmtOf(payload["longitude"]),
			"latitude":  fmtOf(payload["latitude"]),
		}, opts)
		if err != nil {
			return nil, err
		}
		return r.Decoded, nil
	}
	body := map[string]any{
		"clockInDate": fmtOf(payload["date"]),
		"cityCode":    fmtOf(payload["cityCode"]),
		"cityName":    fmtOf(payload["cityName"]),
		"position":    fmtOf(payload["position"]),
		"longitude":   fmtOf(payload["longitude"]),
		"latitude":    fmtOf(payload["latitude"]),
		"remarks":     fmtOf(payload["remarks"]),
	}
	if seq == 2 {
		body["NextClockIn"] = true
	}
	r, err := p.CallJSONX(ctx, "clockin/markNew", body, opts)
	if err != nil {
		return nil, err
	}
	return r.Decoded, nil
}

// SaveFee 保存费用（完整模板 read-modify-write，slapp 通道）：
// 无 tpl 快照时先 getFeeInfoNew 取实时模板；补助明细（id=10）按 foodFee/transitFee 装配；
// overrides 为组件级透传 {组件id: value串}（如成本分配选项整段 value），原样进 saveFeeInfoNew。
// payload: {date, foodFee, transitFee, tpl?, overrides?, cityName?, cityCode?, position?}。
func (p *Protocol) SaveFee(ctx context.Context, token string, payload map[string]any, opts CallOpts) (map[string]any, error) {
	opts.Token = token
	date := fmtOf(payload["date"])
	overrides := stringMapOf(payload["overrides"])
	overrides["10"] = SubsidyJSON(num(payload["foodFee"]), num(payload["transitFee"]))
	tpl, _ := payload["tpl"].(map[string]any)
	if tpl == nil {
		d, err := p.GetFeeInfoNew(ctx, token, FeeTplParams{
			ClockInDate: date,
			CityName:    fmtOf(payload["cityName"]),
			CityCode:    fmtOf(payload["cityCode"]),
			Position:    fmtOf(payload["position"]),
		}, opts)
		if err != nil {
			return nil, err
		}
		tpl = ExtractClockTemplate(d)
		if tpl == nil {
			return nil, fmt.Errorf("获取商旅费用模板失败，请重试")
		}
	}
	return p.SaveFeeInfoNew(ctx, token, date, tpl, overrides, opts)
}

// stringMapOf 取字符串 map（overrides 透传；JSON 对象键值全按字符串口径）。
func stringMapOf(v any) map[string]string {
	out := map[string]string{}
	m, ok := v.(map[string]any)
	if !ok {
		if m2, ok2 := v.(map[string]string); ok2 {
			for k, s := range m2 {
				out[k] = s
			}
		}
		return out
	}
	for k, val := range m {
		out[k] = fmtOf(val)
	}
	return out
}

// fmtOf 取字符串（任意值）。
func fmtOf(v any) string {
	return str(v)
}
