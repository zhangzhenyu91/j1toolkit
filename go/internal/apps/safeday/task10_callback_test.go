package safeday

import (
	"context"
	"database/sql/driver"
	"net/http"
	"testing"
)

// 新记录带批次 ID：旧回调（无 record_id/batch_id）不得写入；
// 携带错误批次也不得写入；只有记录 ID + 批次 ID 同时匹配才落盘。
func TestCallbackRejectsLegacyCallbackForNewRecord(t *testing.T) {
	env := newTestServer(t, identTeamA)
	rec, _, err := env.store.Create(Record{
		Name: "安全活动学习", Date: "2026.10.27", FileName: "2026.10.27.docx",
		Team: "检修一班", Status: "processing", BatchID: "batch-new",
	})
	if err != nil {
		t.Fatalf("建记录失败: %v", err)
	}
	texts := map[string]any{
		"accident_process": "事故经过……", "cause_analysis": "原因剖析……",
		"self_check": "自查……", "rectification_list": "整改……",
		"prevention_measures": "防范……", "supervision_inspection": "督导……",
	}
	base := map[string]any{"date": "2026.10.27", "class": "检修一班"}
	for k, v := range texts {
		base[k] = v
	}

	// 旧回调：无 ID，即使日期/班组完全匹配也不能写新记录。
	resp := env.do(t, http.MethodPost, "/api/v1/safeday/callback?token=cb-token", base)
	if resp.Code != http.StatusOK {
		t.Fatalf("旧回调应返回 200 忽略: %s", resp.Body.String())
	}
	if got, _ := env.store.Get(rec.ID); got.Status != "processing" {
		t.Fatalf("旧回调不得写新记录，状态变为 %s", got.Status)
	}

	// 记录 ID 正确但批次不符：仍拒绝。
	badBatch := map[string]any{"record_id": rec.ID, "batch_id": "batch-old"}
	for k, v := range texts {
		badBatch[k] = v
	}
	if resp := env.do(t, http.MethodPost, "/api/v1/safeday/callback?token=cb-token", badBatch); resp.Code != http.StatusOK {
		t.Fatalf("错误批次回调应返回 200 忽略: %s", resp.Body.String())
	}
	if got, _ := env.store.Get(rec.ID); got.Status != "processing" {
		t.Fatalf("批次不符不得写记录，状态变为 %s", got.Status)
	}

	// 正确记录 + 批次：落盘 done。
	good := map[string]any{"record_id": rec.ID, "batch_id": "batch-new", "date": "2026.10.27", "class": "检修一班"}
	for k, v := range texts {
		good[k] = v
	}
	resp = env.do(t, http.MethodPost, "/api/v1/safeday/callback?token=cb-token", good)
	if resp.Code != http.StatusOK {
		t.Fatalf("正确回调应成功: %s", resp.Body.String())
	}
	if got, _ := env.store.Get(rec.ID); got.Status != "done" {
		t.Fatalf("正确回调应置 done，实际 %s", got.Status)
	}
}

// 已终态记录收到重复/延迟回调不得改写状态。
func TestCallbackIgnoresStaleRecordID(t *testing.T) {
	env := newTestServer(t, identTeamA)
	rec, _, _ := env.store.Create(Record{
		Name: "安全活动学习", Date: "2026.10.27", FileName: "2026.10.27.docx",
		Team: "检修一班", Status: "done", BatchID: "batch-old",
	})
	body := map[string]any{"record_id": rec.ID, "batch_id": "batch-old", "accident_process": "迟到的回调"}
	resp := env.do(t, http.MethodPost, "/api/v1/safeday/callback?token=cb-token", body)
	if resp.Code != http.StatusOK {
		t.Fatalf("过期回调应返回 200 忽略: %s", resp.Body.String())
	}
	if got, _ := env.store.Get(rec.ID); got.Status != "done" {
		t.Fatalf("终态记录不应被改写，实际 %s", got.Status)
	}
}

// 旧记录（无批次 ID）继续兼容旧的按产物存在性终判。
func TestCallbackLegacyRecordStillJudged(t *testing.T) {
	env := newTestServer(t, identTeamA)
	rec, _, _ := env.store.Create(Record{
		Name: "旧记录", Date: "2026.10.26", FileName: "2026.10.26.docx",
		Team: "检修一班", Status: "processing",
	})
	resp := env.do(t, http.MethodPost, "/api/v1/safeday/callback?token=cb-token", map[string]any{"class": "检修一班"})
	if resp.Code != http.StatusOK {
		t.Fatalf("旧回调应成功: %s", resp.Body.String())
	}
	if got, _ := env.store.Get(rec.ID); got.Status != "failed" {
		t.Fatalf("旧记录无产物应置 failed，实际 %s", got.Status)
	}
}

// 生成请求把记录 ID / 批次 ID 传给工作流，回调可凭两者精确回写。
func TestGeneratePassesRecordAndBatchIDs(t *testing.T) {
	env := newTestServer(t, identTeamA)
	teamRules(env.fake)
	rec := env.doMultipart(t, "/api/v1/safeday/generate", map[string]string{
		"name": "学习", "date": "2026.12.05",
	}, map[string][]byte{"学习.pdf": []byte("%PDF-1.4 fake")})
	body := decodeBodyMap(t, rec)
	if rec.Code != http.StatusOK || body["ok"] != true {
		t.Fatalf("生成应成功: %d %v", rec.Code, body)
	}
	env.dify.mu.Lock()
	in := env.dify.inputs[0]
	env.dify.mu.Unlock()
	if in.RecordID == "" || in.BatchID == "" {
		t.Fatalf("工作流入参应包含 record_id/batch_id: %+v", in)
	}
	record, ok := env.store.Get(in.RecordID)
	if !ok || record.BatchID != in.BatchID {
		t.Fatalf("记录批次 ID 不一致: %+v", record)
	}
	cb := env.do(t, http.MethodPost, "/api/v1/safeday/callback?token=cb-token", map[string]any{
		"record_id": in.RecordID, "batch_id": in.BatchID,
		"accident_process": "事故经过", "cause_analysis": "原因", "self_check": "自查",
		"rectification_list": "整改", "prevention_measures": "防范", "supervision_inspection": "督导",
	})
	if cb.Code != http.StatusOK {
		t.Fatalf("回调应成功: %s", cb.Body.String())
	}
	if got, _ := env.store.Get(in.RecordID); got.Status != "done" {
		t.Fatalf("回调后应 done，实际 %s", got.Status)
	}
}

// 启动迁移：无 team 的旧记录回填首个启用班组。
func TestBackfillLegacyTeams(t *testing.T) {
	env := newTestServer(t, identTeamA)
	env.fake.on("SELECT name FROM sys_team WHERE status = 1", []string{"name"}, [][]driver.Value{{"检修一班"}})
	legacy, _, _ := env.store.Create(Record{Name: "旧", Date: "2026.10.01", FileName: "a.docx", Status: "processing"})
	kept, _, _ := env.store.Create(Record{Name: "新", Date: "2026.10.02", FileName: "b.docx", Team: "检修二班", Status: "processing"})
	n, err := env.module.BackfillLegacyTeams(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("应回填 1 条，实际 %d/%v", n, err)
	}
	if got, _ := env.store.Get(legacy.ID); got.Team != "检修一班" {
		t.Fatalf("旧记录班组未回填: %+v", got)
	}
	if got, _ := env.store.Get(kept.ID); got.Team != "检修二班" {
		t.Fatalf("已有班组不应被覆盖: %+v", got)
	}
}
