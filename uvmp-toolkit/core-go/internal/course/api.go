// Package course 学堂刷课引擎（移植自仓根 course_autoplay_trial.py + toolkit/apps/course_autoplay/backend.py）。
//
// 分层：
//   - api/url/checkpoint：接口解析、列表页识别、检查点 schema v1（纯逻辑，无浏览器）
//   - monitor：ResponseMonitor（学习侧接口响应捕获与等待，playwright response 监听同口径）
//   - driver：受控浏览器页面语义操作契约（chromedp/CDP 实现见 driver_chromedp.go，测试 fake 同契约）
//   - runner：课程状态机（页面识别/筛选/分页/防误点/课件树/视频拖动与 ended/上报/3 次跳过/
//     失败截图/暂停回滚/续刷）
//   - runjob：backend.run 编排（检查点持久化/恢复/结果形状）
//
// 只接管自己打开的学习弹窗，不关闭课程列表或浏览器；不访问任何业务网络以外的地址。
package course

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// 国网学堂学习侧接口与页面路径（urlsplit path 后缀匹配，与 trial.py 同口径）。
const (
	TrainDetailPath = "/learning-c/train/trainDetail"
	CoursePageAPI   = "/learningc/trainclass/course/page"
	WareListAPI     = "/learningc/course/ware-catalog-relation/listRes"
	LastStudyAPI    = "/learningc/course/study-record-info/lastStudyHour"
	SaveStudyAPI    = "/learningc/course/study-record-info/getPassWare"
)

// APICapturePaths ResponseMonitor 需要捕获响应体的接口集合（trial.py ResponseMonitor 同口径）。
var APICapturePaths = []string{WareListAPI, SaveStudyAPI, LastStudyAPI, CoursePageAPI}

// apiValue 校验业务响应并取 resultValue（resultCode==200，与 trial.py _api_value 同口径）。
func apiValue(payload map[string]any, description string) (any, error) {
	if payload == nil {
		return nil, fmt.Errorf("%s响应不是 JSON 对象", description)
	}
	if fmt.Sprintf("%v", payload["resultCode"]) != "200" {
		value := payload["resultValue"]
		message := "未知错误"
		if s, ok := value.(string); ok {
			message = s
		}
		if len([]rune(message)) > 200 {
			message = string([]rune(message)[:200])
		}
		return nil, fmt.Errorf("%s返回失败：%s", description, message)
	}
	return payload["resultValue"], nil
}

// CoursePage 一页课程列表（course/page 响应）。
type CoursePage struct {
	Items []map[string]any
	Total int
}

// ParseCoursePage 解析课程列表响应（trial.py parse_course_page 同口径）。
func ParseCoursePage(payload map[string]any) (*CoursePage, error) {
	value, err := apiValue(payload, "课程列表")
	if err != nil {
		return nil, err
	}
	m, _ := value.(map[string]any)
	if m == nil {
		return nil, fmt.Errorf("课程列表响应缺少 resultValue.list")
	}
	listRaw, _ := m["list"].([]any)
	if listRaw == nil {
		return nil, fmt.Errorf("课程列表响应缺少 resultValue.list")
	}
	totalF, ok := toFloat(m["total"])
	if !ok {
		return nil, fmt.Errorf("课程列表响应缺少有效 total")
	}
	out := &CoursePage{Total: int(totalF)}
	for _, raw := range listRaw {
		item, _ := raw.(map[string]any)
		if item == nil || item["resourceid"] == nil || fmt.Sprintf("%v", item["resourceid"]) == "" {
			return nil, fmt.Errorf("课程列表存在缺少 resourceid 的记录")
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// FlattenWares 展平课件目录树为叶子课件列表（trial.py flatten_wares 同口径）。
func FlattenWares(payload map[string]any) ([]map[string]any, error) {
	value, err := apiValue(payload, "课件清单")
	if err != nil {
		return nil, err
	}
	nodes, _ := value.([]any)
	if nodes == nil {
		return nil, fmt.Errorf("课件清单 resultValue 不是数组")
	}
	result := []map[string]any{}
	var visit func(list []any)
	visit = func(list []any) {
		for _, raw := range list {
			node, _ := raw.(map[string]any)
			if node == nil {
				continue
			}
			children, _ := node["courseWareCatalogRelationDTOS"].([]any)
			if len(children) > 0 {
				visit(children)
			} else if node["wareType"] != nil {
				result = append(result, node)
			}
		}
	}
	visit(nodes)
	if len(result) == 0 {
		return nil, fmt.Errorf("课件清单为空，无法学习")
	}
	return result, nil
}

// CourseProgress 课程学习进度百分比（studyRate，非法/非有限值返回 ok=false；trial.py course_progress）。
func CourseProgress(item map[string]any) (float64, bool) {
	v, ok := toFloat(item["studyRate"])
	if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// CourseHasProgress 是否纳入进度判断（trial.py course_has_progress：非 contenttype=3 且有 hour）。
func CourseHasProgress(item map[string]any) bool {
	return fmt.Sprintf("%v", item["contenttype"]) != "3" && item["hour"] != nil
}

// CourseIsComplete 进度是否已达 100%（trial.py course_is_complete）。
func CourseIsComplete(item map[string]any) bool {
	p, ok := CourseProgress(item)
	return CourseHasProgress(item) && ok && p >= 100
}

// CourseBlockReason 不可学习原因（空串=可学习；trial.py course_block_reason）。
func CourseBlockReason(item map[string]any) string {
	if fmt.Sprintf("%v", item["courseStatus"]) != "1" {
		return "课程已下线或当前不可学习"
	}
	if fmt.Sprintf("%v", item["coursetype"]) != "1" {
		return "不是网页课程"
	}
	if fmt.Sprintf("%v", item["coursespace"]) == "2" {
		return "仅支持 APP 学习"
	}
	return ""
}

// FormatProgress 进度文案（trial.py format_progress："未知" / "50%"）。
func FormatProgress(item map[string]any) string {
	p, ok := CourseProgress(item)
	if !ok {
		return "未知"
	}
	return trimNum(p) + "%"
}

// trimNum %g 同口径（去掉多余 0 的十进制）。
func trimNum(v float64) string {
	return fmt.Sprintf("%g", v)
}

// toFloat 宽松取数值（JSON number/float/string）。
func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case nil:
		return 0, false
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}
