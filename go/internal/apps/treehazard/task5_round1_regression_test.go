// Task 5 round1 回归：辅证照片重推必须先上传成功并准备新集合，再事务切换；任一失败保留旧集合。
package treehazard

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

// oldEvidenceRule 旧 evidence 照片行（id + cos_key）。
func oldEvidenceRule(fdb *fakeDB) {
	fdb.on("SELECT id, cos_key FROM treehazard_photo WHERE record_id = ? AND kind = 'evidence'",
		[]string{"id", "cos_key"}, [][]driver.Value{{int64(1), "treehazard/t/5/old-1.jpg"}})
}

// newPhotos 构造两张待上传 dataURL 照片。
func newPhotos() []dataURLPhoto {
	return []dataURLPhoto{
		{buf: []byte("NEW-1"), contentType: "image/jpeg", ext: "jpg"},
		{buf: []byte("NEW-2"), contentType: "image/jpeg", ext: "jpg"},
	}
}

// TestReplaceEvidencePhotosPutFailureKeepsOld 上传任一失败：旧集合与旧对象不得删除，
// 新上传成功的对象要回滚清理；不得先删旧再上传。
func TestReplaceEvidencePhotosPutFailureKeepsOld(t *testing.T) {
	env := newTestServer(t, identUser)
	oldEvidenceRule(env.fake)
	env.objects.putHook = func(key string) error {
		if strings.Contains(key, "evidence-") && strings.Contains(key, "-1.") {
			return errors.New("COS 上传失败")
		}
		return nil
	}

	stored, failed := env.module.replaceEvidencePhotos(context.Background(), "检修一班", 5, newPhotos())
	if stored != 0 || failed != 1 {
		t.Fatalf("上传失败应 stored=0 failed=1，实际 %d/%d", stored, failed)
	}
	if n := env.fake.execCount("DELETE FROM treehazard_photo"); n != 0 {
		t.Fatalf("上传失败不得删旧表行，实际 %d", n)
	}
	if n := env.fake.execCount("INSERT INTO treehazard_photo"); n != 0 {
		t.Fatalf("上传失败不得切换新集合，实际 %d", n)
	}
	for _, key := range env.objects.deletes {
		if strings.Contains(key, "old-1") {
			t.Fatalf("上传失败不得删除旧 COS 对象: %v", env.objects.deletes)
		}
	}
}

// TestReplaceEvidencePhotosDBFailureKeepsOld DB 切换失败：事务回滚，旧表行/对象保留，新对象清理。
func TestReplaceEvidencePhotosDBFailureKeepsOld(t *testing.T) {
	env := newTestServer(t, identUser)
	oldEvidenceRule(env.fake)
	env.fake.onErr("INSERT INTO treehazard_photo", errors.New("db down"))

	stored, failed := env.module.replaceEvidencePhotos(context.Background(), "检修一班", 5, newPhotos())
	if stored != 0 || failed != 2 {
		t.Fatalf("DB 失败应 stored=0 failed=2，实际 %d/%d", stored, failed)
	}
	if env.fake.execCount("DELETE FROM treehazard_photo WHERE record_id = ? AND kind = 'evidence'") != 0 {
		t.Fatal("不得按 record_id+kind 全量删除（会误删新行）")
	}
	if del, ok := env.fake.lastExec("DELETE FROM treehazard_photo WHERE id = ?"); ok {
		if len(del.args) != 1 || del.args[0] != driver.Value(int64(1)) {
			t.Fatalf("旧行删除应精确按旧 id=1，实际 %v", del.args)
		}
	}
	for _, key := range env.objects.deletes {
		if strings.Contains(key, "old-1") {
			t.Fatalf("DB 失败不得删除旧 COS 对象: %v", env.objects.deletes)
		}
	}
	events := env.fake.eventLog()
	sawRollback := false
	for _, e := range events {
		if e == "rollback" {
			sawRollback = true
		}
	}
	if !sawRollback {
		t.Fatalf("DB 失败应回滚事务，事件 %v", events)
	}
}

// TestReplaceEvidencePhotosSuccessAfterUpload 全部上传成功后才事务切换，提交后才删旧对象。
func TestReplaceEvidencePhotosSuccessAfterUpload(t *testing.T) {
	env := newTestServer(t, identUser)
	oldEvidenceRule(env.fake)
	var oldDeletedBeforeCommit bool
	env.objects.deleteHook = func(key string) {
		if !strings.Contains(key, "old-1") {
			return
		}
		committed := false
		for _, e := range env.fake.eventLog() {
			if e == "commit" {
				committed = true
			}
		}
		if !committed {
			oldDeletedBeforeCommit = true
		}
	}

	stored, failed := env.module.replaceEvidencePhotos(context.Background(), "检修一班", 5, newPhotos())
	if stored != 2 || failed != 0 {
		t.Fatalf("成功应 stored=2 failed=0，实际 %d/%d", stored, failed)
	}
	puts := len(env.objects.puts)
	if puts != 2 {
		t.Fatalf("应先上传 2 张新照片，实际 %d", puts)
	}
	if env.fake.execCount("INSERT INTO treehazard_photo") != 2 {
		t.Fatalf("应写入 2 行新照片，实际 %d", env.fake.execCount("INSERT INTO treehazard_photo"))
	}
	if env.fake.execCount("DELETE FROM treehazard_photo") != 1 {
		t.Fatalf("切换时应删除旧表行一次，实际 %d", env.fake.execCount("DELETE FROM treehazard_photo"))
	}
	if oldDeletedBeforeCommit {
		t.Fatal("旧 COS 对象必须在事务提交后删除")
	}
	if len(env.objects.deletes) != 1 || !strings.Contains(env.objects.deletes[0], "old-1") {
		t.Fatalf("提交后应删除旧对象: %v", env.objects.deletes)
	}
}

// TestReplaceEvidencePhotosReadOldFailureKeepsOld 旧行读取失败时不切换、不删除。
func TestReplaceEvidencePhotosReadOldFailureKeepsOld(t *testing.T) {
	env := newTestServer(t, identUser)
	env.fake.onErr("SELECT id, cos_key FROM treehazard_photo", errors.New("read fail"))
	stored, failed := env.module.replaceEvidencePhotos(context.Background(), "检修一班", 5, newPhotos())
	if stored != 0 || failed != 2 {
		t.Fatalf("旧行读取失败应放弃整组，实际 %d/%d", stored, failed)
	}
	if env.fake.execCount("INSERT INTO treehazard_photo") != 0 || env.fake.execCount("DELETE FROM treehazard_photo") != 0 {
		t.Fatal("读取失败不得写库")
	}
	if len(env.objects.puts) != 0 || len(env.objects.deletes) != 0 {
		t.Fatalf("读取失败不得上传/删除对象: puts=%v deletes=%v", env.objects.puts, env.objects.deletes)
	}
}
