package config

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

// batchRowSource 为 UpdateBatch 的逐键 SELECT ... FOR UPDATE 返回行。
func batchRowSource(values map[string]string, sensitive map[string]bool) func(string, []driver.Value) (*fakeRows, error) {
	return func(query string, args []driver.Value) (*fakeRows, error) {
		if !strings.Contains(query, "FROM sys_runtime_config") {
			return rowsOf([]string{"v"}), nil
		}
		key, _ := args[0].(string)
		value, ok := values[key]
		if !ok {
			return rowsOf([]string{"cfg_value", "is_sensitive"}), nil
		}
		sens := int64(0)
		if sensitive[key] {
			sens = 1
		}
		return rowsOf([]string{"cfg_value", "is_sensitive"}, []driver.Value{value, sens}), nil
	}
}

func batchUpdates(values ...string) []Update {
	out := make([]Update, 0, len(values)/2)
	for i := 0; i+1 < len(values); i += 2 {
		out = append(out, Update{Key: values[i], Value: values[i+1]})
	}
	return out
}

// 批量更新：全部键校验通过后一次性提交，只发一次批量通知。
func TestUpdateBatchCommitsOnceAndNotifiesBatchOnce(t *testing.T) {
	db, f := openFake(t)
	f.onQuery = batchRowSource(map[string]string{"a": "1", "b": "2"}, nil)
	store := NewStore(db, nil)
	var batches [][]ChangeEvent
	store.OnChangeBatch(func(events []ChangeEvent) { batches = append(batches, events) })
	notified := 0
	store.OnChange(func(ChangeEvent) { notified++ })

	if err := store.UpdateBatch(context.Background(), batchUpdates("a", "10", "b", "20")); err != nil {
		t.Fatalf("UpdateBatch: %v", err)
	}
	if f.commits != 1 || f.rollbacks != 0 {
		t.Fatalf("应一次提交无回滚，实际 commits=%d rollbacks=%d", f.commits, f.rollbacks)
	}
	if n := len(f.execsWith("UPDATE sys_runtime_config")); n != 2 {
		t.Fatalf("应执行 2 条 UPDATE，实际 %d", n)
	}
	if len(batches) != 1 || len(batches[0]) != 2 {
		t.Fatalf("批量通知应只发 1 次且含 2 条变更，实际 %+v", batches)
	}
	if notified != 2 {
		t.Fatalf("兼容单键监听应收到 2 条变更，实际 %d", notified)
	}
}

// 任一键不存在：整体回滚，不得留下任何 UPDATE 与通知。
func TestUpdateBatchUnknownKeyWritesNothing(t *testing.T) {
	db, f := openFake(t)
	f.onQuery = batchRowSource(map[string]string{"a": "1"}, nil)
	store := NewStore(db, nil)
	notified := 0
	store.OnChange(func(ChangeEvent) { notified++ })
	batches := 0
	store.OnChangeBatch(func([]ChangeEvent) { batches++ })

	err := store.UpdateBatch(context.Background(), batchUpdates("a", "10", "missing", "20"))
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("未知键应返回 sql.ErrNoRows，实际 %v", err)
	}
	if f.commits != 0 || f.rollbacks != 1 {
		t.Fatalf("应回滚且不提交，实际 commits=%d rollbacks=%d", f.commits, f.rollbacks)
	}
	if n := len(f.execsWith("UPDATE sys_runtime_config")); n != 0 {
		t.Fatalf("回滚前不应写入任何键，实际 %d 条 UPDATE", n)
	}
	if notified != 0 || batches != 0 {
		t.Fatalf("失败不得通知，实际单键=%d 批量=%d", notified, batches)
	}
}

// 中途 UPDATE 失败：整体回滚，不得部分成功或通知。
func TestUpdateBatchExecFailureRollsBackWithoutNotify(t *testing.T) {
	db, f := openFake(t)
	f.onQuery = batchRowSource(map[string]string{"a": "1", "b": "2"}, nil)
	f.onExec = func(query string, args []driver.Value) (driver.Result, error) {
		if strings.Contains(query, "UPDATE sys_runtime_config") {
			if key, _ := args[1].(string); key == "b" {
				return nil, errors.New("disk full")
			}
		}
		return fakeResult(1), nil
	}
	store := NewStore(db, nil)
	notified := 0
	store.OnChange(func(ChangeEvent) { notified++ })

	err := store.UpdateBatch(context.Background(), batchUpdates("a", "10", "b", "20"))
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("应返回 UPDATE 错误，实际 %v", err)
	}
	if f.commits != 0 || f.rollbacks != 1 {
		t.Fatalf("应回滚且不提交，实际 commits=%d rollbacks=%d", f.commits, f.rollbacks)
	}
	if notified != 0 {
		t.Fatalf("失败不得通知，实际 %d", notified)
	}
}

// 批量更新敏感项：落库前用 ConfigCipher 加密，事件仍为明文；普通项保持明文。
func TestUpdateBatchEncryptsSensitiveValues(t *testing.T) {
	db, f := openFake(t)
	cipher, err := NewConfigCipher("", "unit-jwt")
	if err != nil {
		t.Fatal(err)
	}
	encOld, err := cipher.Encrypt("old-secret")
	if err != nil {
		t.Fatal(err)
	}
	f.onQuery = batchRowSource(
		map[string]string{"secret.key": encOld, "plain.key": "old"},
		map[string]bool{"secret.key": true},
	)
	store := NewStore(db, cipher)
	var events []ChangeEvent
	store.OnChangeBatch(func(list []ChangeEvent) { events = append(events, list...) })
	if err := store.UpdateBatch(context.Background(), batchUpdates(
		"secret.key", "new-secret", "plain.key", "new-plain")); err != nil {
		t.Fatalf("UpdateBatch: %v", err)
	}
	execs := f.execsWith("UPDATE sys_runtime_config")
	if len(execs) != 2 {
		t.Fatalf("应执行 2 条 UPDATE，实际 %d", len(execs))
	}
	var secretStored, plainStored string
	for _, e := range execs {
		switch e.args[1] {
		case "secret.key":
			secretStored, _ = e.args[0].(string)
		case "plain.key":
			plainStored, _ = e.args[0].(string)
		}
	}
	if !IsEncrypted(secretStored) {
		t.Fatalf("敏感项应以 enc:v1: 密文落库: %q", secretStored)
	}
	if plainStored != "new-plain" {
		t.Fatalf("普通项不应加密: %q", plainStored)
	}
	if plain, err := cipher.Decrypt(secretStored); err != nil || plain != "new-secret" {
		t.Fatalf("密文解密不符: %q %v", plain, err)
	}
	if len(events) != 2 || events[0].NewValue != "new-secret" || events[0].OldValue != "old-secret" || !events[0].Sensitive {
		t.Fatalf("敏感项变更事件应为明文: %+v", events)
	}
}

// 无 ConfigCipher 时批量更新敏感项必须在写入前失败，且不提交/不通知。
func TestUpdateBatchSensitiveWithoutCipherFailsBeforeWrite(t *testing.T) {
	db, f := openFake(t)
	f.onQuery = batchRowSource(map[string]string{"secret.key": "plain-old"}, map[string]bool{"secret.key": true})
	store := NewStore(db, nil)
	notified := 0
	store.OnChange(func(ChangeEvent) { notified++ })
	err := store.UpdateBatch(context.Background(), batchUpdates("secret.key", "new"))
	if !errors.Is(err, ErrNoMasterKey) {
		t.Fatalf("应返回 ErrNoMasterKey，实际 %v", err)
	}
	if f.commits != 0 || f.rollbacks != 1 {
		t.Fatalf("应回滚且不提交，实际 commits=%d rollbacks=%d", f.commits, f.rollbacks)
	}
	if n := len(f.execsWith("UPDATE sys_runtime_config")); n != 0 {
		t.Fatalf("不得写入任何 UPDATE，实际 %d", n)
	}
	if notified != 0 {
		t.Fatalf("失败不得通知，实际 %d", notified)
	}
}
