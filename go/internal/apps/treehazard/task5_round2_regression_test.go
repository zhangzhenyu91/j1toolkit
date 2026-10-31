// Task 5 round2 回归：辅证切换的最终行状态必须是“旧行被替换、新行仍在”，
// 不能先 INSERT 再按 record_id+kind 全量 DELETE 把新行一起删掉。
package treehazard

import (
	"context"
	"strings"
	"testing"
)

// photoRow 模拟 treehazard_photo 表行，仅用于本测试验证 SQL 的最终效果。
type photoRow struct {
	id  int64
	key string
}

// applyPhotoSQL 按执行顺序把 treehazard_photo 相关 SQL 应用到最小行状态：
// INSERT 增加新行；按 id DELETE 精确删除；按 record_id+kind DELETE 全量清空（模拟真实语义）。
func applyPhotoSQL(rows []photoRow, execs []fakeExec, recordID int64) (out []photoRow, firstDelete, firstInsert int) {
	nextID := int64(100)
	firstDelete, firstInsert = -1, -1
	for i, e := range execs {
		switch {
		case strings.Contains(e.query, "INSERT INTO treehazard_photo"):
			if len(e.args) >= 2 {
				rows = append(rows, photoRow{id: nextID, key: e.args[1].(string)})
				nextID++
				if firstInsert < 0 {
					firstInsert = i
				}
			}
		case strings.Contains(e.query, "DELETE FROM treehazard_photo WHERE id = ?"):
			if len(e.args) >= 1 {
				id := e.args[0].(int64)
				kept := rows[:0]
				for _, r := range rows {
					if r.id != id {
						kept = append(kept, r)
					}
				}
				rows = kept
				if firstDelete < 0 {
					firstDelete = i
				}
			}
		case strings.Contains(e.query, "DELETE FROM treehazard_photo WHERE record_id = ? AND kind = 'evidence'"):
			if len(e.args) >= 1 && e.args[0].(int64) == recordID {
				rows = nil
				if firstDelete < 0 {
					firstDelete = i
				}
			}
		}
	}
	return rows, firstDelete, firstInsert
}

// TestReplaceEvidencePhotosFinalRowsMatchUploads 上传 2 张后最终必须恰好 2 行新 evidence，
// cos_key 指向新对象、旧行不存在；且旧行删除先于新行插入。
func TestReplaceEvidencePhotosFinalRowsMatchUploads(t *testing.T) {
	env := newTestServer(t, identUser)
	oldEvidenceRule(env.fake)

	stored, failed := env.module.replaceEvidencePhotos(context.Background(), "检修一班", 5, newPhotos())
	if stored != 2 || failed != 0 {
		t.Fatalf("上传 2 张应 stored=2 failed=0，实际 %d/%d", stored, failed)
	}
	rows, firstDelete, firstInsert := applyPhotoSQL([]photoRow{{id: 1, key: "treehazard/t/5/old-1.jpg"}}, env.fake.execs, 5)
	if len(rows) != 2 {
		t.Fatalf("最终应有 2 行新 evidence，实际 %d 行: %+v（旧行删除后新行被误删？）", len(rows), rows)
	}
	for _, r := range rows {
		if r.key == "treehazard/t/5/old-1.jpg" {
			t.Fatalf("旧行应被替换，仍在: %+v", rows)
		}
		if !strings.Contains(r.key, "evidence-") {
			t.Fatalf("最终行应为新上传对象: %+v", rows)
		}
	}
	if firstDelete < 0 || firstInsert < 0 || firstDelete > firstInsert {
		t.Fatalf("旧行 DELETE 必须先于新行 INSERT（delete=%d insert=%d）", firstDelete, firstInsert)
	}
	if len(rows) != len(env.objects.puts) {
		t.Fatalf("数据库行数 %d 与上传张数 %d 不一致", len(rows), len(env.objects.puts))
	}
}
