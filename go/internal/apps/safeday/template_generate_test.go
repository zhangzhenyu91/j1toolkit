package safeday

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"shade/internal/compat/exceldoc"
)

func TestGenerateWithCustomTemplate(t *testing.T) {
	env, ts := newTemplateServer(t, identTeamA)
	teamRules(env.fake)
	tpl, err := ts.Create("动态模板", testSections(), "tpl-key-1")
	if err != nil {
		t.Fatalf("建模板失败: %v", err)
	}

	rec := env.doMultipart(t, "/api/v1/safeday/generate", map[string]string{
		"name": "动态学习", "date": "2026.12.03", "template_id": tpl.ID,
		"host": "李主持", "recorder": "王记录", "superior": "张主任",
		"attendees": "张三 李四", "absentees": "赵五", "absentReason": "请假",
	}, map[string][]byte{"学习.pdf": []byte("%PDF-1.4 fake")})
	body := decodeBodyMap(t, rec)
	if rec.Code != http.StatusOK || body["ok"] != true {
		t.Fatalf("生成应成功: %d %v", rec.Code, body)
	}
	if strings.Contains(rec.Body.String(), "tpl-key-1") {
		t.Fatalf("生成响应不得包含模板 API Key: %s", rec.Body.String())
	}
	recMap, _ := body["record"].(map[string]any)
	if recMap["templateId"] != tpl.ID || recMap["templateName"] != "动态模板" || recMap["templateMode"] != TemplateModeDynamic {
		t.Fatalf("记录模板快照不符: %v", recMap)
	}
	if _, has := recMap["templateSha256"]; has {
		t.Fatalf("新记录不应写模板文件 SHA: %v", recMap)
	}
	sectionsJSON, _ := json.Marshal(recMap["sections"])
	wantJSON, _ := json.Marshal(tpl.Sections)
	if string(sectionsJSON) != string(wantJSON) {
		t.Fatalf("记录章节快照不符: %s != %s", sectionsJSON, wantJSON)
	}

	env.dify.mu.Lock()
	if len(env.dify.inputs) != 1 {
		env.dify.mu.Unlock()
		t.Fatalf("Dify 应触发一次: %v", env.dify.inputs)
	}
	in := env.dify.inputs[0]
	env.dify.mu.Unlock()
	if in.APIKey != "tpl-key-1" {
		t.Fatalf("应传递模板 API Key 覆盖: %q", in.APIKey)
	}
	if in.Date != "2026.12.03" || in.Class != "检修一班" || !strings.HasPrefix(in.URL, "https://cos.example.invalid/") {
		t.Fatalf("Dify 入参不符: %+v", in)
	}
	if defs := env.store.GetFormDefaults("检修一班"); defs.TemplateID != tpl.ID {
		t.Fatalf("表单默认值应记忆模板: %+v", defs)
	}

	cb := env.do(t, http.MethodPost, "/api/v1/safeday/callback?token=cb-token", map[string]any{
		"record_id": recMap["id"], "batch_id": recMap["batchId"],
		"date": "2026.12.03", "class": "检修一班",
		"part_a": "甲段正文", "part_b": "乙段正文",
	})
	if cb.Code != http.StatusOK {
		t.Fatalf("回调应成功: %d %s", cb.Code, cb.Body.String())
	}
	got, _ := env.store.Get(recMap["id"].(string))
	if got.Status != "done" {
		t.Fatalf("记录应 done: %+v", got)
	}
	out, err := readDocxFile(env.store.RecordFilePath(got))
	if err != nil {
		t.Fatalf("读产物失败: %v", err)
	}
	plain := docxText(t, out)
	for _, want := range []string{
		"活动主题：输电运检中心输电检修一班学习动态学习", "活动时间：2026.12.03", "主持人：李主持",
		"活动班组：输电检修一班", "上级参加人员：张主任", "本班组参加人员：张三 李四",
		"缺席人员：赵五", "缺席人员原因：请假", "记录人：王记录",
		"章节甲", "甲段正文", "章节乙", "乙段正文",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("动态产物缺少固定字段或章节 %q: %s", want, plain)
		}
	}
	for _, unwanted := range []string{
		"一、客观还原事故经过", "二、分层剖析事故原因", "三、对照自查举一反三",
		"四、制定整改清单", "五、讨论防范措施", "六、定期督导检查防范措施落实情况和整改完成情况",
		"{@章节区}", "{@客观还原事故经过}", "{@分层剖析事故原因}",
	} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("原活动内容或占位符不应残留 %q: %s", unwanted, plain)
		}
	}
}

func TestGenerateTemplateValidation(t *testing.T) {
	env, ts := newTemplateServer(t, identTeamA)
	teamRules(env.fake)
	old, _, err := env.store.Create(Record{
		Name: "旧记录", Date: "2026.12.04", FileName: "2026.12.04.docx", Team: "检修一班", Status: "done",
	})
	if err != nil {
		t.Fatalf("建旧记录失败: %v", err)
	}
	generate := func(templateID string) map[string]any {
		rec := env.doMultipart(t, "/api/v1/safeday/generate", map[string]string{
			"name": "新学习", "date": "2026.12.04", "template_id": templateID,
		}, map[string][]byte{"学习.pdf": []byte("%PDF-1.4 fake")})
		return decodeBodyMap(t, rec)
	}
	assertOldKept := func() {
		t.Helper()
		if _, ok := env.store.Get(old.ID); !ok {
			t.Fatalf("模板校验失败不得替换旧记录")
		}
	}

	body := generate("")
	if body["ok"] != false || !strings.Contains(body["error"].(string), "请先创建安全日记录模板") {
		t.Fatalf("零模板应明确拒绝: %v", body)
	}
	assertOldKept()
	body = generate("tpl-missing")
	if body["ok"] != false || !strings.Contains(body["error"].(string), "模板不存在") {
		t.Fatalf("未知模板应明确报错: %v", body)
	}
	assertOldKept()

	missingKey, err := ts.Create("缺 key 模板", testSections(), "good-key")
	if err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	stripTemplateKey(t, ts, missingKey.ID)
	body = generate(missingKey.ID)
	if body["ok"] != false || !strings.Contains(body["error"].(string), "未配置 Dify API Key") {
		t.Fatalf("缺 key 应明确报错: %v", body)
	}
	if strings.Contains(body["error"].(string), "good-key") {
		t.Fatalf("错误文案不得包含 key: %v", body)
	}
	assertOldKept()

	defaultTpl, err := ts.Create("用户默认模板", testSections(), "default-key")
	if err != nil {
		t.Fatalf("建默认模板失败: %v", err)
	}
	if err := ts.SetDefault(defaultTpl.ID); err != nil {
		t.Fatalf("设默认失败: %v", err)
	}
	body = generate("")
	if body["ok"] != true {
		t.Fatalf("空 template_id 应使用用户默认模板: %v", body)
	}
	recMap, _ := body["record"].(map[string]any)
	if recMap["templateId"] != defaultTpl.ID || recMap["templateMode"] != TemplateModeDynamic {
		t.Fatalf("空 template_id 的用户模板快照不符: %v", recMap)
	}
	if _, has := recMap["templateSha256"]; has {
		t.Fatalf("新记录不应包含模板 SHA: %v", recMap)
	}
	env.dify.mu.Lock()
	last := env.dify.inputs[len(env.dify.inputs)-1]
	env.dify.mu.Unlock()
	if last.APIKey != "default-key" {
		t.Fatalf("默认模板应使用自己的 Dify Key: %q", last.APIKey)
	}
}

func TestFormMetaTemplateDefault(t *testing.T) {
	env, ts := newTemplateServer(t, identTeamA)
	teamRules(env.fake)
	getDefault := func() string {
		rec := env.do(t, http.MethodGet, "/api/v1/safeday/form-meta", nil)
		body := decodeBodyMap(t, rec)
		defaults, _ := body["defaults"].(map[string]any)
		id, _ := defaults["templateId"].(string)
		return id
	}
	if got := getDefault(); got != "" {
		t.Fatalf("零模板时表单默认应为空: %q", got)
	}
	tpl, err := ts.Create("默认模板", testSections(), "k-1")
	if err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	if got := getDefault(); got != tpl.ID {
		t.Fatalf("应返回用户默认模板: %q", got)
	}
	env.store.SaveFormDefaults("检修一班", FormDefaults{TemplateID: tpl.ID})
	if got := getDefault(); got != tpl.ID {
		t.Fatalf("应返回已记忆模板: %q", got)
	}
	if err := ts.Delete(tpl.ID); err != nil {
		t.Fatalf("删模板失败: %v", err)
	}
	if got := getDefault(); got != "" {
		t.Fatalf("失效模板不得回落内置: %q", got)
	}
}

func TestCallbackDynamicVariants(t *testing.T) {
	env, ts := newTemplateServer(t, identTeamA)
	tpl, err := ts.Create("变体", testSections(), "k")
	if err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	mkRecord := func(date string) Record {
		rec, _, err := env.store.Create(Record{
			Name: "动态", Date: date, FileName: date + ".docx", Team: "检修一班", Status: "processing",
			TemplateID: tpl.ID, TemplateName: tpl.Name, TemplateMode: tpl.Mode, Sections: tpl.Sections,
		})
		if err != nil {
			t.Fatalf("建记录失败: %v", err)
		}
		return rec
	}
	render := func(date string, payload map[string]any) string {
		payload["date"] = date
		payload["class"] = "检修一班"
		rec := env.do(t, http.MethodPost, "/api/v1/safeday/callback?token=cb-token", payload)
		if rec.Code != http.StatusOK {
			t.Fatalf("回调应成功(%s): %d %s", date, rec.Code, rec.Body.String())
		}
		got, _ := env.store.Get(mkRecordLookupID(env, date))
		if got.Status != "done" {
			t.Fatalf("记录应 done: %+v", got)
		}
		out, err := readDocxFile(env.store.RecordFilePath(got))
		if err != nil {
			t.Fatalf("读产物失败: %v", err)
		}
		return docxText(t, out)
	}

	_ = mkRecord("2026.12.06")
	plain := render("2026.12.06", map[string]any{
		"sections": map[string]string{"part_a": "对象甲", "part_b": "对象乙"},
	})
	if !strings.Contains(plain, "对象甲") || !strings.Contains(plain, "对象乙") {
		t.Fatalf("sections 对象未渲染: %s", plain)
	}

	mkRecord("2026.12.07")
	raw, _ := json.Marshal(map[string]string{"part_a": "字符串甲", "part_b": "字符串乙"})
	plain = render("2026.12.07", map[string]any{"sections": string(raw)})
	if !strings.Contains(plain, "字符串甲") || !strings.Contains(plain, "字符串乙") {
		t.Fatalf("sections JSON 字符串未渲染: %s", plain)
	}

	mkRecord("2026.12.08")
	plain = render("2026.12.08", map[string]any{
		"part_a": "扁平甲", "part_b": "扁平乙",
		"sections": map[string]string{"part_a": "对象甲", "part_b": "对象乙"},
	})
	if !strings.Contains(plain, "扁平甲") || strings.Contains(plain, "对象甲") {
		t.Fatalf("扁平字段应优先: %s", plain)
	}
}

func TestCallbackDynamicAfterTemplateDelete(t *testing.T) {
	env, ts := newTemplateServer(t, identTeamA)
	tpl, err := ts.Create("将删除", testSections(), "k")
	if err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	rec, _, _ := env.store.Create(Record{
		Name: "在途", Date: "2026.12.09", FileName: "2026.12.09.docx", Team: "检修一班", Status: "processing",
		TemplateID: tpl.ID, TemplateName: tpl.Name, TemplateMode: tpl.Mode, Sections: tpl.Sections,
	})
	if err := ts.Delete(tpl.ID); err != nil {
		t.Fatalf("删模板失败: %v", err)
	}
	resp := env.do(t, http.MethodPost, "/api/v1/safeday/callback?token=cb-token", map[string]any{
		"date": "2026.12.09", "class": "检修一班", "part_a": "删除后甲", "part_b": "删除后乙",
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("删除模板后在途记录仍应渲染: %d %s", resp.Code, resp.Body.String())
	}
	got, _ := env.store.Get(rec.ID)
	if got.Status != "done" {
		t.Fatalf("记录应 done: %+v", got)
	}
	out, _ := readDocxFile(env.store.RecordFilePath(got))
	if plain := docxText(t, out); !strings.Contains(plain, "删除后甲") {
		t.Fatalf("产物内容不符: %s", plain)
	}
}

func TestCallbackDynamicIgnoresLegacyDocxSHA(t *testing.T) {
	env, _ := newTemplateServer(t, identTeamA)
	rec, _, _ := env.store.Create(Record{
		Name: "旧在途", Date: "2026.12.10", FileName: "2026.12.10.docx", Team: "检修一班", Status: "processing",
		TemplateID: "tpl-old", TemplateName: "旧模板", TemplateMode: TemplateModeDynamic,
		TemplateSHA: strings.Repeat("a", 64), Sections: testSections(),
	})
	resp := env.do(t, http.MethodPost, "/api/v1/safeday/callback?token=cb-token", map[string]any{
		"date": "2026.12.10", "class": "检修一班", "part_a": "甲", "part_b": "乙",
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("旧上传文件缺失不得影响统一底稿渲染: %d %s", resp.Code, resp.Body.String())
	}
	got, _ := env.store.Get(rec.ID)
	if got.Status != "done" {
		t.Fatalf("记录应 done: %+v", got)
	}
}

func TestCallbackDynamicRejectsNonSnapshotKeys(t *testing.T) {
	env, ts := newTemplateServer(t, identTeamA)
	tpl, err := ts.Create("自定义变量", testSections(), "k")
	if err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	rec, _, _ := env.store.Create(Record{
		Name: "变量不匹配", Date: "2026.12.13", FileName: "2026.12.13.docx", Team: "检修一班", Status: "processing",
		BatchID: "batch-mismatch", TemplateID: tpl.ID, TemplateName: tpl.Name, TemplateMode: tpl.Mode, Sections: tpl.Sections,
	})
	resp := env.do(t, http.MethodPost, "/api/v1/safeday/callback?token=cb-token", map[string]any{
		"record_id": rec.ID, "batch_id": "batch-mismatch", "date": "2026.12.13", "class": "检修一班",
		"accident_process": "不应写入自定义章节",
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("不匹配回调应按忽略处理: %d %s", resp.Code, resp.Body.String())
	}
	body := decodeBodyMap(t, resp)
	if body["ignored"] != float64(1) || body["done"] != float64(0) {
		t.Fatalf("非快照 key 不得判为有效正文: %v", body)
	}
	got, _ := env.store.Get(rec.ID)
	if got.Status != "processing" {
		t.Fatalf("非快照 key 不得完成记录: %+v", got)
	}
	if _, err := os.Stat(env.store.RecordFilePath(got)); !os.IsNotExist(err) {
		t.Fatalf("非快照 key 不得生成空章节文件: %v", err)
	}
}

func TestParseCallbackSectionsSkipsEnvelopeFields(t *testing.T) {
	body := map[string]json.RawMessage{
		"record_id": json.RawMessage(`"r1"`), "batch_id": json.RawMessage(`"b1"`),
		"date": json.RawMessage(`"2026.12.13"`), "class": json.RawMessage(`"检修一班"`),
		"url": json.RawMessage(`"https://example.invalid"`), "part_a": json.RawMessage(`"正文"`),
		"sections": json.RawMessage(`{"part_b":"乙","record_id":"不得作为章节"}`),
	}
	sections := parseCallbackSections(body)
	if sections["part_a"] != "正文" || sections["part_b"] != "乙" {
		t.Fatalf("章节正文解析不符: %v", sections)
	}
	for _, key := range []string{"record_id", "batch_id", "date", "class", "url"} {
		if _, ok := sections[key]; ok {
			t.Fatalf("信封字段不得作为章节正文 %s: %v", key, sections)
		}
	}
}

func TestConcurrentDynamicRender(t *testing.T) {
	env, ts := newTemplateServer(t, identTeamA)
	tpl, err := ts.Create("并发渲染", testSections(), "k")
	if err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	rec := Record{
		ID: "r1", Name: "并发", Date: "2026.12.11", FileName: "2026.12.11.docx", Team: "检修一班",
		TemplateID: tpl.ID, TemplateName: tpl.Name, TemplateMode: tpl.Mode, Sections: tpl.Sections,
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			text := "并发内容" + string(rune('A'+i))
			out, err := env.module.renderRecord(rec, map[string]string{"part_a": text, "part_b": "固定乙"})
			if err != nil {
				t.Errorf("并发渲染失败: %v", err)
				return
			}
			if plain := docxText(t, out); !strings.Contains(plain, text) || !strings.Contains(plain, "固定乙") {
				t.Errorf("并发渲染串数据: %s", plain)
			}
		}()
	}
	wg.Wait()
}

func TestDynamicRenderLimits(t *testing.T) {
	env, ts := newTemplateServer(t, identTeamA)
	tpl, err := ts.Create("限额", testSections(), "k")
	if err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	long := strings.Repeat("字", maxSectionChars) + "TAIL"
	out, err := env.module.renderRecord(Record{
		ID: "r2", Name: "限额", Date: "2026.12.12", FileName: "2026.12.12.docx", Team: "检修一班",
		TemplateID: tpl.ID, TemplateName: tpl.Name, TemplateMode: tpl.Mode, Sections: tpl.Sections,
	}, map[string]string{"part_a": long, "part_b": `<script>&"`})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	plain := docxText(t, out)
	if strings.Contains(plain, "TAIL") {
		t.Fatalf("超过 20000 字应截断")
	}
	if !strings.Contains(plain, "章节乙") {
		t.Fatalf("空段仍应保留标题: %s", plain)
	}
	if !strings.Contains(plain, `<script>&"`) {
		t.Fatalf("正文特殊字符应保留（XML 层面转义后还原）: %s", plain)
	}
}

func readDocxFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

var reDocxText = regexp.MustCompile(`(?s)<w:t[^>]*>(.*?)</w:t>`)

func docxText(t *testing.T, data []byte) string {
	t.Helper()
	parts, err := exceldoc.ReadDocx(data)
	if err != nil {
		t.Fatalf("读 docx 失败: %v", err)
	}
	var b strings.Builder
	for _, m := range reDocxText.FindAllStringSubmatch(string(parts["word/document.xml"]), -1) {
		b.WriteString(m[1])
	}
	return strings.NewReplacer(
		"&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&",
	).Replace(b.String())
}

func stripTemplateKey(t *testing.T, ts *TemplateStore, id string) {
	t.Helper()
	ts.mu.Lock()
	for i := range ts.index.Templates {
		if ts.index.Templates[i].ID == id {
			ts.index.Templates[i].APIKeyEnc = ""
		}
	}
	if err := ts.writeIndexLocked(); err != nil {
		ts.mu.Unlock()
		t.Fatalf("写索引失败: %v", err)
	}
	ts.mu.Unlock()
}

func mkRecordLookupID(env *testEnv, date string) string {
	list := env.store.List("检修一班")
	for _, r := range list {
		if r.Date == date {
			return r.ID
		}
	}
	return ""
}
