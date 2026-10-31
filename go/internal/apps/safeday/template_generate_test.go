package safeday

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"shade/internal/compat/exceldoc"
)

// TestGenerateWithCustomTemplate 生成记录写模板快照、单次 API Key 覆盖、Dify 入参仍仅三字段。
func TestGenerateWithCustomTemplate(t *testing.T) {
	env, ts := newTemplateServer(t, identTeamA)
	teamRules(env.fake)
	tpl, err := ts.Create("动态模板", testSections(), "tpl-key-1", dynamicDocxBytes(t))
	if err != nil {
		t.Fatalf("建模板失败: %v", err)
	}

	rec := env.doMultipart(t, "/api/v1/safeday/generate", map[string]string{
		"name": "动态学习", "date": "2026.12.03", "template_id": tpl.ID,
	}, map[string][]byte{"学习.pdf": []byte("%PDF-1.4 fake")})
	body := decodeBodyMap(t, rec)
	if rec.Code != http.StatusOK || body["ok"] != true {
		t.Fatalf("生成应成功: %d %v", rec.Code, body)
	}
	if strings.Contains(rec.Body.String(), "tpl-key-1") {
		t.Fatalf("生成响应不得包含模板 API Key: %s", rec.Body.String())
	}
	recMap, _ := body["record"].(map[string]any)
	if recMap["templateId"] != tpl.ID || recMap["templateName"] != "动态模板" ||
		recMap["templateMode"] != TemplateModeDynamic || recMap["templateSha256"] != tpl.SHA256 {
		t.Fatalf("记录模板快照不符: %v", recMap)
	}
	sectionsJSON, _ := json.Marshal(recMap["sections"])
	wantJSON, _ := json.Marshal(tpl.Sections)
	if string(sectionsJSON) != string(wantJSON) {
		t.Fatalf("记录章节快照不符: %s != %s", sectionsJSON, wantJSON)
	}

	env.dify.mu.Lock()
	defer env.dify.mu.Unlock()
	if len(env.dify.inputs) != 1 {
		t.Fatalf("Dify 应触发一次: %v", env.dify.inputs)
	}
	in := env.dify.inputs[0]
	if in.APIKey != "tpl-key-1" {
		t.Fatalf("应传递模板 API Key 覆盖: %q", in.APIKey)
	}
	if in.Date != "2026.12.03" || in.Class != "检修一班" || !strings.HasPrefix(in.URL, "https://cos.example.invalid/") {
		t.Fatalf("Dify 入参不符: %+v", in)
	}
	if defs := env.store.GetFormDefaults("检修一班"); defs.TemplateID != tpl.ID {
		t.Fatalf("表单默认值应记忆模板: %+v", defs)
	}

	// callback 按快照 key 取扁平字段 → 动态渲染
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
	for _, want := range []string{"章节甲", "章节乙", "甲段正文", "乙段正文"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("动态产物缺少 %q: %s", want, plain)
		}
	}
	if strings.Contains(plain, "{@章节区}") {
		t.Fatalf("产物不得残留章节占位符: %s", plain)
	}
}

// TestGenerateTemplateValidation unknown/missing key/missing docx 均在 Create 前失败，旧记录不被替换。
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

	// 未知模板 → 400，旧记录仍在
	body := generate("tpl-missing")
	if body["ok"] != false || !strings.Contains(body["error"].(string), "模板不存在") {
		t.Fatalf("未知模板应明确报错: %v", body)
	}
	if _, ok := env.store.Get(old.ID); !ok {
		t.Fatalf("未知模板不得替换旧记录")
	}

	// 缺 key → 400，旧记录仍在
	tpl, err := ts.Create("缺 key 模板", testSections(), "good-key", dynamicDocxBytes(t))
	if err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	stripTemplateKey(t, ts, tpl.ID)
	body = generate(tpl.ID)
	if body["ok"] != false || !strings.Contains(body["error"].(string), "未配置 Dify API Key") {
		t.Fatalf("缺 key 应明确报错: %v", body)
	}
	if strings.Contains(body["error"].(string), "good-key") {
		t.Fatalf("错误文案不得包含 key: %v", body)
	}
	if _, ok := env.store.Get(old.ID); !ok {
		t.Fatalf("缺 key 不得替换旧记录")
	}

	// 缺 docx → 明确报错，旧记录仍在
	tpl2, err := ts.Create("缺文件模板", testSections(), "good-key-2", dynamicDocxBytes(t))
	if err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	removeTemplateDocx(t, ts, tpl2.SHA256)
	body = generate(tpl2.ID)
	if body["ok"] != false || !strings.Contains(body["error"].(string), "模板文件缺失") {
		t.Fatalf("缺 docx 应明确报错: %v", body)
	}
	if _, ok := env.store.Get(old.ID); !ok {
		t.Fatalf("缺 docx 不得替换旧记录")
	}

	// 空 template_id → builtin（不走默认自定义模板）
	if err := ts.SetDefault(tpl2.ID); err != nil {
		t.Fatalf("设默认失败: %v", err)
	}
	body = generate("")
	if body["ok"] != true {
		t.Fatalf("空 template_id 应继续 builtin: %v", body)
	}
	recMap, _ := body["record"].(map[string]any)
	if recMap["templateId"] != BuiltinTemplateID || recMap["templateMode"] != TemplateModeStatic {
		t.Fatalf("空 template_id 应为 builtin 快照: %v", recMap)
	}
	env.dify.mu.Lock()
	last := env.dify.inputs[len(env.dify.inputs)-1]
	env.dify.mu.Unlock()
	if last.APIKey != "" {
		t.Fatalf("builtin 应使用全局 key（空覆盖）: %q", last.APIKey)
	}
}

// TestGenerateRejectsCorruptedTemplateFile 文件存在但损坏时 generate 必须在 Create 前失败，
// 不得先替换同日期旧记录。
func TestGenerateRejectsCorruptedTemplateFile(t *testing.T) {
	env, ts := newTemplateServer(t, identTeamA)
	teamRules(env.fake)
	old, _, err := env.store.Create(Record{
		Name: "旧记录", Date: "2026.12.20", FileName: "2026.12.20.docx", Team: "检修一班", Status: "done",
	})
	if err != nil {
		t.Fatalf("建旧记录失败: %v", err)
	}
	tpl, err := ts.Create("损坏模板", testSections(), "key-corrupt-gen", dynamicDocxBytes(t))
	if err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ts.filesDir, tpl.SHA256+".docx"), []byte("corrupted"), 0o644); err != nil {
		t.Fatalf("写损坏文件失败: %v", err)
	}
	rec := env.doMultipart(t, "/api/v1/safeday/generate", map[string]string{
		"name": "新学习", "date": "2026.12.20", "template_id": tpl.ID,
	}, map[string][]byte{"学习.pdf": []byte("%PDF-1.4 fake")})
	body := decodeBodyMap(t, rec)
	if body["ok"] != false || !strings.Contains(body["error"].(string), "模板文件损坏") {
		t.Fatalf("损坏模板应明确失败: %v", body)
	}
	if strings.Contains(body["error"].(string), "key-corrupt-gen") {
		t.Fatalf("错误文案不得包含 key: %v", body)
	}
	if _, ok := env.store.Get(old.ID); !ok {
		t.Fatalf("损坏模板不得替换旧记录")
	}
}

// TestFormMetaTemplateDefault 表单默认模板：已保存模板优先，失效则回落默认/内置。
func TestFormMetaTemplateDefault(t *testing.T) {
	env, ts := newTemplateServer(t, identTeamA)
	teamRules(env.fake)
	tpl, err := ts.Create("默认模板", testSections(), "k-1", dynamicDocxBytes(t))
	if err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	getDefault := func() string {
		rec := env.do(t, http.MethodGet, "/api/v1/safeday/form-meta", nil)
		body := decodeBodyMap(t, rec)
		defaults, _ := body["defaults"].(map[string]any)
		id, _ := defaults["templateId"].(string)
		return id
	}
	if got := getDefault(); got != BuiltinTemplateID {
		t.Fatalf("未设默认应为 builtin: %q", got)
	}
	if err := ts.SetDefault(tpl.ID); err != nil {
		t.Fatalf("设默认失败: %v", err)
	}
	if got := getDefault(); got != tpl.ID {
		t.Fatalf("应返回默认模板: %q", got)
	}
	env.store.SaveFormDefaults("检修一班", FormDefaults{TemplateID: tpl.ID})
	if got := getDefault(); got != tpl.ID {
		t.Fatalf("应返回已记忆模板: %q", got)
	}
	if err := ts.Delete(tpl.ID); err != nil {
		t.Fatalf("删模板失败: %v", err)
	}
	if got := getDefault(); got != BuiltinTemplateID {
		t.Fatalf("失效模板应回落 builtin: %q", got)
	}
}

// TestCallbackDynamicVariants callback 兼容 sections 对象 / JSON 字符串，并优先扁平字段。
func TestCallbackDynamicVariants(t *testing.T) {
	env, ts := newTemplateServer(t, identTeamA)
	tpl, err := ts.Create("变体", testSections(), "k", dynamicDocxBytes(t))
	if err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	mkRecord := func(date string) Record {
		rec, _, err := env.store.Create(Record{
			Name: "动态", Date: date, FileName: date + ".docx", Team: "检修一班", Status: "processing",
			TemplateID: tpl.ID, TemplateName: tpl.Name, TemplateMode: tpl.Mode,
			TemplateSHA: tpl.SHA256, Sections: tpl.Sections,
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

	// sections 对象
	recObj := mkRecord("2026.12.06")
	plain := render("2026.12.06", map[string]any{
		"sections": map[string]string{"part_a": "对象甲", "part_b": "对象乙"},
	})
	if !strings.Contains(plain, "对象甲") || !strings.Contains(plain, "对象乙") {
		t.Fatalf("sections 对象未渲染: %s", plain)
	}
	_ = recObj

	// sections JSON 字符串
	mkRecord("2026.12.07")
	raw, _ := json.Marshal(map[string]string{"part_a": "字符串甲", "part_b": "字符串乙"})
	plain = render("2026.12.07", map[string]any{"sections": string(raw)})
	if !strings.Contains(plain, "字符串甲") || !strings.Contains(plain, "字符串乙") {
		t.Fatalf("sections JSON 字符串未渲染: %s", plain)
	}

	// 扁平字段优先于对象
	mkRecord("2026.12.08")
	plain = render("2026.12.08", map[string]any{
		"part_a": "扁平甲", "part_b": "扁平乙",
		"sections": map[string]string{"part_a": "对象甲", "part_b": "对象乙"},
	})
	if !strings.Contains(plain, "扁平甲") || strings.Contains(plain, "对象甲") {
		t.Fatalf("扁平字段应优先: %s", plain)
	}
}

// TestCallbackDynamicAfterTemplateDelete 模板删除不影响在途记录（按 sha 读内容文件）。
func TestCallbackDynamicAfterTemplateDelete(t *testing.T) {
	env, ts := newTemplateServer(t, identTeamA)
	tpl, err := ts.Create("将删除", testSections(), "k", dynamicDocxBytes(t))
	if err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	rec, _, _ := env.store.Create(Record{
		Name: "在途", Date: "2026.12.09", FileName: "2026.12.09.docx", Team: "检修一班", Status: "processing",
		TemplateID: tpl.ID, TemplateName: tpl.Name, TemplateMode: tpl.Mode,
		TemplateSHA: tpl.SHA256, Sections: tpl.Sections,
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

// TestCallbackMissingDynamicDocxMarksFailed 快照文件缺失 → 记录 failed 且错误明确。
func TestCallbackMissingDynamicDocxMarksFailed(t *testing.T) {
	env, _ := newTemplateServer(t, identTeamA)
	rec, _, _ := env.store.Create(Record{
		Name: "缺文件", Date: "2026.12.10", FileName: "2026.12.10.docx", Team: "检修一班", Status: "processing",
		TemplateID: "tpl-x", TemplateName: "X", TemplateMode: TemplateModeDynamic,
		TemplateSHA: strings.Repeat("a", 64), Sections: testSections(),
	})
	resp := env.do(t, http.MethodPost, "/api/v1/safeday/callback?token=cb-token", map[string]any{
		"date": "2026.12.10", "class": "检修一班", "part_a": "甲", "part_b": "乙",
	})
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("缺 docx 应 500: %d %s", resp.Code, resp.Body.String())
	}
	got, _ := env.store.Get(rec.ID)
	if got.Status != "failed" || !strings.Contains(got.Error, "模板文件缺失") {
		t.Fatalf("记录应 failed 且错误明确: %+v", got)
	}
}

// TestConcurrentDynamicRender 并发渲染同一模板不串数据、无竞争。
func TestConcurrentDynamicRender(t *testing.T) {
	env, ts := newTemplateServer(t, identTeamA)
	tpl, err := ts.Create("并发渲染", testSections(), "k", dynamicDocxBytes(t))
	if err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	rec := Record{
		ID: "r1", Name: "并发", Date: "2026.12.11", FileName: "2026.12.11.docx", Team: "检修一班",
		TemplateID: tpl.ID, TemplateName: tpl.Name, TemplateMode: tpl.Mode,
		TemplateSHA: tpl.SHA256, Sections: tpl.Sections,
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

// TestDynamicRenderLimits 动态章节：20000 字截断、空段、转义。
func TestDynamicRenderLimits(t *testing.T) {
	env, ts := newTemplateServer(t, identTeamA)
	tpl, err := ts.Create("限额", testSections(), "k", dynamicDocxBytes(t))
	if err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	long := strings.Repeat("字", maxSectionChars) + "TAIL"
	out, err := env.module.renderRecord(Record{
		ID: "r2", Name: "限额", Date: "2026.12.12", FileName: "2026.12.12.docx", Team: "检修一班",
		TemplateID: tpl.ID, TemplateName: tpl.Name, TemplateMode: tpl.Mode,
		TemplateSHA: tpl.SHA256, Sections: tpl.Sections,
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

// ---- 测试辅助 ----

func readDocxFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

var reDocxText = regexp.MustCompile(`(?s)<w:t[^>]*>(.*?)</w:t>`)

// docxText 提取 docx 正文纯文本（拼接所有 <w:t> 并还原实体）。
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

// stripTemplateKey 模拟历史模板缺 key（直接改索引并重载）。
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

// removeTemplateDocx 删除模板内容文件（模拟磁盘丢失）。
func removeTemplateDocx(t *testing.T, ts *TemplateStore, sha string) {
	t.Helper()
	if err := os.Remove(filepath.Join(ts.filesDir, sha+".docx")); err != nil {
		t.Fatalf("删模板文件失败: %v", err)
	}
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
