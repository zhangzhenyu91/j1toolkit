package course

import (
	"strings"
	"testing"
)

// sampleCoursePage 课程列表响应样本（course/page resultValue 形状）。
func sampleCoursePage(list []map[string]any, total float64) map[string]any {
	items := []any{}
	for _, it := range list {
		items = append(items, it)
	}
	return map[string]any{
		"resultCode":  "200",
		"resultValue": map[string]any{"list": items, "total": total},
	}
}

// courseItem 一条课程记录（公开字段口径同模拟站 public_course）。
func courseItem(id, name string, rate float64) map[string]any {
	return map[string]any{
		"resourceid":   id,
		"coursename":   name,
		"studyRate":    rate,
		"contenttype":  float64(1),
		"hour":         float64(1),
		"courseStatus": float64(1),
		"coursetype":   float64(1),
		"coursespace":  float64(1),
	}
}

func TestAPISuccessAndFailure(t *testing.T) {
	if _, err := apiValue(map[string]any{"resultCode": "200", "resultValue": 1}, "测试"); err != nil {
		t.Fatalf("成功响应报错: %v", err)
	}
	if _, err := apiValue(map[string]any{"resultCode": float64(200), "resultValue": 1}, "测试"); err != nil {
		t.Fatalf("数值 resultCode 应通过: %v", err)
	}
	_, err := apiValue(map[string]any{"resultCode": "500", "resultValue": "服务器繁忙"}, "测试")
	if err == nil || !strings.Contains(err.Error(), "测试返回失败：服务器繁忙") {
		t.Fatalf("业务失败错误 = %v", err)
	}
	if _, err := apiValue(nil, "测试"); err == nil {
		t.Fatal("nil 载荷应报错")
	}
}

func TestParseCoursePage(t *testing.T) {
	payload := sampleCoursePage([]map[string]any{courseItem("v", "视频课", 0), courseItem("d", "文档课", 100)}, 3)
	page, err := ParseCoursePage(payload)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.Items) != 2 {
		t.Fatalf("解析 = total %d items %d", page.Total, len(page.Items))
	}

	// 缺 list
	if _, err := ParseCoursePage(map[string]any{"resultCode": "200", "resultValue": map[string]any{"total": 1}}); err == nil {
		t.Fatal("缺 list 应报错")
	}
	// 缺 total
	if _, err := ParseCoursePage(map[string]any{"resultCode": "200", "resultValue": map[string]any{"list": []any{}}}); err == nil {
		t.Fatal("缺 total 应报错")
	}
	// 缺 resourceid
	bad := sampleCoursePage([]map[string]any{{"coursename": "无 id"}}, 1)
	if _, err := ParseCoursePage(bad); err == nil || !strings.Contains(err.Error(), "缺少 resourceid") {
		t.Fatalf("缺 resourceid 错误 = %v", err)
	}
}

func TestFlattenWares(t *testing.T) {
	payload := map[string]any{
		"resultCode": "200",
		"resultValue": []any{
			map[string]any{
				"id": "catalog",
				"courseWareCatalogRelationDTOS": []any{
					map[string]any{"wareType": float64(1), "wareName": "视频1", "courseId": "v"},
					map[string]any{
						"wareType": float64(2), "wareName": "目录A",
						"courseWareCatalogRelationDTOS": []any{
							map[string]any{"wareType": float64(2), "wareName": "文档1", "courseId": "v"},
						},
					},
				},
			},
		},
	}
	wares, err := FlattenWares(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(wares) != 2 {
		t.Fatalf("叶子课件数 = %d，want 2", len(wares))
	}
	if wares[0]["wareName"] != "视频1" || wares[1]["wareName"] != "文档1" {
		t.Fatalf("展平顺序错误: %v", wares)
	}
	// 空清单
	empty := map[string]any{"resultCode": "200", "resultValue": []any{map[string]any{"id": "c"}}}
	if _, err := FlattenWares(empty); err == nil {
		t.Fatal("空课件清单应报错")
	}
}

func TestCoursePredicates(t *testing.T) {
	done := courseItem("a", "完成课", 100)
	if !CourseIsComplete(done) {
		t.Fatal("100% 应判完成")
	}
	zero := courseItem("b", "新课", 0)
	if CourseIsComplete(zero) || !CourseHasProgress(zero) {
		t.Fatal("0% 应未完成且纳入判断")
	}
	noHour := courseItem("c", "无学时", 50)
	noHour["hour"] = nil
	if CourseHasProgress(noHour) {
		t.Fatal("无 hour 不纳入进度判断")
	}
	ct3 := courseItem("d", "非进度", 50)
	ct3["contenttype"] = "3"
	if CourseHasProgress(ct3) {
		t.Fatal("contenttype=3 不纳入进度判断")
	}
	if p, ok := CourseProgress(zero); !ok || p != 0 {
		t.Fatalf("progress = %v %v", p, ok)
	}
	if _, ok := CourseProgress(map[string]any{"studyRate": "abc"}); ok {
		t.Fatal("非法 studyRate 应判未知")
	}
	if FormatProgress(map[string]any{"studyRate": nil}) != "未知" {
		t.Fatal("空进度文案应为未知")
	}
	if FormatProgress(map[string]any{"studyRate": float64(50)}) != "50%" {
		t.Fatalf("进度文案 = %s", FormatProgress(map[string]any{"studyRate": float64(50)}))
	}

	offline := courseItem("e", "下线", 0)
	offline["courseStatus"] = "0"
	if CourseBlockReason(offline) != "课程已下线或当前不可学习" {
		t.Fatal("下线判定错误")
	}
	notWeb := courseItem("f", "非网页", 0)
	notWeb["coursetype"] = "2"
	if CourseBlockReason(notWeb) != "不是网页课程" {
		t.Fatal("非网页判定错误")
	}
	appOnly := courseItem("g", "仅APP", 0)
	appOnly["coursespace"] = "2"
	if CourseBlockReason(appOnly) != "仅支持 APP 学习" {
		t.Fatal("仅 APP 判定错误")
	}
	if CourseBlockReason(courseItem("h", "可学", 0)) != "" {
		t.Fatal("正常课应可学")
	}
}
