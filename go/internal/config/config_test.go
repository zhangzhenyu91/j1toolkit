package config

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestConfigCipherRoundTrip(t *testing.T) {
	// 从 JWT_SECRET 派生（缺省口径）
	c1, err := NewConfigCipher("", "unit-test-jwt-secret")
	if err != nil {
		t.Fatalf("NewConfigCipher(派生): %v", err)
	}
	plain := "敏感值-unit-测试-😀"
	enc, err := c1.Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if !strings.HasPrefix(enc, EncPrefix) {
		t.Fatalf("密文前缀错误: %s", enc[:16])
	}
	got, err := c1.Decrypt(enc)
	if err != nil || got != plain {
		t.Fatalf("解密不一致: %q %v", got, err)
	}
	// 随机 nonce：两次密文不同
	enc2, _ := c1.Encrypt(plain)
	if enc2 == enc {
		t.Fatal("两次加密密文不应相同")
	}
	// 同密钥派生可互解
	c1b, _ := NewConfigCipher("", "unit-test-jwt-secret")
	if _, err := c1b.Decrypt(enc); err != nil {
		t.Fatalf("同派生密钥应可解: %v", err)
	}
	// 异密钥不可解
	c2, _ := NewConfigCipher("", "other-secret")
	if _, err := c2.Decrypt(enc); err == nil {
		t.Fatal("异密钥应解密失败")
	}
	// 篡改密文失败
	tampered := enc[:len(enc)-4] + "AAAA"
	if _, err := c1.Decrypt(tampered); err == nil {
		t.Fatal("篡改密文应失败")
	}
	if _, err := c1.Decrypt("not-enc"); err == nil {
		t.Fatal("非密文应失败")
	}
}

func TestConfigCipherMasterKeyForms(t *testing.T) {
	// hex 32 字节
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	hexKey := hex.EncodeToString(raw)
	cHex, err := NewConfigCipher(hexKey, "")
	if err != nil {
		t.Fatalf("hex 主密钥: %v", err)
	}
	// base64 32 字节
	cB64, err := NewConfigCipher(base64.StdEncoding.EncodeToString(raw), "")
	if err != nil {
		t.Fatalf("base64 主密钥: %v", err)
	}
	enc, err := cHex.Encrypt("v")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := cB64.Decrypt(enc); err != nil {
		t.Fatalf("hex/base64 同密钥应互解: %v", err)
	}
	// 任意原文压缩
	if _, err := NewConfigCipher("任意长度主密钥", ""); err != nil {
		t.Fatalf("原文主密钥: %v", err)
	}
	// 双空报错
	if _, err := NewConfigCipher("", ""); err != ErrNoMasterKey {
		t.Fatalf("应报 ErrNoMasterKey, got %v", err)
	}
}

func TestMaskSecret(t *testing.T) {
	cases := map[string]string{
		"":         "",
		"ab":       "****",
		"abcd":     "****",
		"abcde":    "ab****de",
		"abcdefgh": "ab****gh",
		"长字符串内容xy": "长字****xy",
	}
	for in, want := range cases {
		if got := MaskSecret(in); got != want {
			t.Fatalf("MaskSecret(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

func TestSeedFromEnv(t *testing.T) {
	sdb, fdb := openFake(t)
	cipher, err := NewConfigCipher("", "unit-jwt")
	if err != nil {
		t.Fatalf("NewConfigCipher: %v", err)
	}
	store := NewStore(sdb, cipher)

	specs := []Spec{
		{Key: "worklog.dify_key", Env: "DIFY_WORKLOG_API_KEY", Sensitive: true, Default: ""},
		{Key: "preview.viewer_url", Env: "PREVIEW_VIEWER_URL", Default: "https://view.example"},
		{Key: "sgcc.sync_time", Env: "SGCC_SYNC_TIME", Default: "23:00"},
	}
	env := map[string]string{
		"DIFY_WORKLOG_API_KEY": "unit-sensitive-value",
		// PREVIEW_VIEWER_URL / SGCC_SYNC_TIME 缺省走 Default
	}
	getenv := func(k string) string { return env[k] }

	var events []ChangeEvent
	store.OnChange(func(ev ChangeEvent) { events = append(events, ev) })

	seeded, err := store.SeedFromEnv(context.Background(), specs, getenv)
	if err != nil {
		t.Fatalf("SeedFromEnv: %v", err)
	}
	if seeded != 3 {
		t.Fatalf("seeded = %d, 期望 3", seeded)
	}
	inserts := fdb.execsWith("INSERT INTO sys_runtime_config")
	if len(inserts) != 3 {
		t.Fatalf("INSERT 次数 = %d", len(inserts))
	}
	// 敏感项应以 enc:v1: 密文落库且可解
	var sensStored string
	for _, c := range inserts {
		if val, ok := c.args[0].(string); ok && val == "worklog.dify_key" {
			sensStored, _ = c.args[1].(string)
			if s, _ := c.args[2].(int64); s != 1 {
				t.Fatalf("敏感标记 = %v", c.args[2])
			}
		}
	}
	if !IsEncrypted(sensStored) {
		t.Fatalf("敏感项未加密落库: %q", sensStored)
	}
	if pt, err := cipher.Decrypt(sensStored); err != nil || pt != "unit-sensitive-value" {
		t.Fatalf("敏感项解密 = %q %v", pt, err)
	}
	// 普通项明文 + Default 补缺
	for _, c := range inserts {
		if val, _ := c.args[0].(string); val == "sgcc.sync_time" {
			if v, _ := c.args[1].(string); v != "23:00" {
				t.Fatalf("Default 导入值 = %q", v)
			}
		}
	}
	if len(events) != 3 || events[0].Source != SourceEnv {
		t.Fatalf("监听事件 = %+v", events)
	}

	// 已存在的键不再导入（模拟库内已有 preview.viewer_url）
	fdb.onQuery = func(q string, args []driver.Value) (*fakeRows, error) {
		if strings.Contains(q, "SELECT cfg_key") && len(args) > 0 {
			if k, _ := args[0].(string); k == "preview.viewer_url" {
				return rowsOf([]string{"cfg_key"}, []driver.Value{k}), nil
			}
		}
		return &fakeRows{cols: []string{"cfg_key"}}, nil
	}
	seeded, err = store.SeedFromEnv(context.Background(), specs, getenv)
	if err != nil {
		t.Fatalf("SeedFromEnv(2nd): %v", err)
	}
	if seeded != 2 {
		t.Fatalf("二次 seeded = %d, 期望 2（已有键跳过）", seeded)
	}
}

func TestUpdateAndPublicValue(t *testing.T) {
	sdb, fdb := openFake(t)
	cipher, _ := NewConfigCipher("", "unit-jwt")
	store := NewStore(sdb, cipher)

	sensEnc, err := cipher.Encrypt("old-secret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	// 行数据：普通 / 敏感各一键
	fdb.onQuery = func(q string, args []driver.Value) (*fakeRows, error) {
		if strings.Contains(q, "SELECT cfg_value") && len(args) > 0 {
			switch k := args[0].(string); k {
			case "normal.key":
				return rowsOf([]string{"cfg_value", "is_sensitive"}, []driver.Value{"old-normal", int64(0)}), nil
			case "secret.key":
				return rowsOf([]string{"cfg_value", "is_sensitive"}, []driver.Value{sensEnc, int64(1)}), nil
			}
		}
		return &fakeRows{cols: []string{"cfg_value", "is_sensitive"}}, nil
	}

	// Get
	if v, err := store.Get(context.Background(), "normal.key"); err != nil || v != "old-normal" {
		t.Fatalf("Get(normal) = %q %v", v, err)
	}
	if v, err := store.Get(context.Background(), "secret.key"); err != nil || v != "old-secret" {
		t.Fatalf("Get(secret) = %q %v", v, err)
	}
	// PublicValue：敏感脱敏、普通原值
	if v, err := store.PublicValue(context.Background(), "secret.key"); err != nil || v != MaskSecret("old-secret") {
		t.Fatalf("PublicValue(secret) = %q %v", v, err)
	}
	if v, err := store.PublicValue(context.Background(), "normal.key"); err != nil || v != "old-normal" {
		t.Fatalf("PublicValue(normal) = %q %v", v, err)
	}

	var events []ChangeEvent
	store.OnChange(func(ev ChangeEvent) { events = append(events, ev) })

	// Update 敏感：落库密文 + 监听明文新旧值
	if err := store.Update(context.Background(), "secret.key", "new-secret"); err != nil {
		t.Fatalf("Update(secret): %v", err)
	}
	upd := fdb.execsWith("UPDATE sys_runtime_config")
	if len(upd) != 1 {
		t.Fatalf("UPDATE 次数 = %d", len(upd))
	}
	stored, _ := upd[0].args[0].(string)
	if !IsEncrypted(stored) {
		t.Fatalf("敏感更新未加密: %q", stored)
	}
	if pt, err := cipher.Decrypt(stored); err != nil || pt != "new-secret" {
		t.Fatalf("敏感更新密文解密 = %q %v", pt, err)
	}
	if len(events) != 1 || events[0].OldValue != "old-secret" || events[0].NewValue != "new-secret" {
		t.Fatalf("监听事件 = %+v", events)
	}
	// Update 普通：明文落库
	if err := store.Update(context.Background(), "normal.key", "new-normal"); err != nil {
		t.Fatalf("Update(normal): %v", err)
	}
	upd = fdb.execsWith("UPDATE sys_runtime_config")
	if v, _ := upd[len(upd)-1].args[0].(string); v != "new-normal" {
		t.Fatalf("普通更新应明文落库: %q", v)
	}
	// 不存在的键
	if err := store.Update(context.Background(), "missing.key", "x"); err != sql.ErrNoRows {
		t.Fatalf("缺失键应返回 sql.ErrNoRows, got %v", err)
	}
}

func TestCleanupAppLog(t *testing.T) {
	sdb, fdb := openFake(t)
	fdb.onExec = func(q string, args []driver.Value) (driver.Result, error) {
		return fakeResult(5), nil
	}
	now := time.Now()
	n, err := CleanupAppLog(context.Background(), sdb, 7) // 显式 7 天
	if err != nil {
		t.Fatalf("CleanupAppLog: %v", err)
	}
	if n != 5 {
		t.Fatalf("删除行数 = %d, 期望 5", n)
	}
	dels := fdb.execsWith("DELETE FROM app_log")
	if len(dels) != 1 {
		t.Fatalf("DELETE 次数 = %d", len(dels))
	}
	cutoff, ok := dels[0].args[0].(time.Time)
	if !ok {
		t.Fatalf("cutoff 参数类型 = %T", dels[0].args[0])
	}
	want := now.AddDate(0, 0, -7)
	if !approxTime(cutoff, want, 2*time.Minute) {
		t.Fatalf("cutoff = %v, 期望 ≈ %v（7 天前）", cutoff, want)
	}
	// 自定义保留天数
	if _, err := CleanupAppLog(context.Background(), sdb, 3); err != nil {
		t.Fatalf("CleanupAppLog(3): %v", err)
	}
	dels = fdb.execsWith("DELETE FROM app_log")
	cutoff2, _ := dels[len(dels)-1].args[0].(time.Time)
	if !approxTime(cutoff2, now.AddDate(0, 0, -3), 2*time.Minute) {
		t.Fatalf("cutoff(3) = %v", cutoff2)
	}
	// 越界
	if _, err := CleanupAppLog(context.Background(), sdb, 3651); err == nil {
		t.Fatal("3651 天应报错")
	}
}

func TestInsertAppLog(t *testing.T) {
	sdb, fdb := openFake(t)
	if err := InsertAppLog(context.Background(), sdb, AppLogEntry{
		AppCode: "worklog", Message: "测试日志", UserID: 42, TraceID: "t-1",
	}); err != nil {
		t.Fatalf("InsertAppLog: %v", err)
	}
	ins := fdb.execsWith("INSERT INTO app_log")
	if len(ins) != 1 {
		t.Fatalf("INSERT 次数 = %d", len(ins))
	}
	if lvl, _ := ins[0].args[1].(string); lvl != "info" {
		t.Fatalf("缺省 level = %q", lvl)
	}
	if uid, _ := ins[0].args[4].(int64); uid != 42 {
		t.Fatalf("user_id = %v", ins[0].args[4])
	}
}

func TestSchemaSQL(t *testing.T) {
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS sys_runtime_config",
		"cfg_key", "cfg_value", "is_sensitive", "updated_at",
	} {
		if !strings.Contains(RuntimeConfigSchemaSQL, want) {
			t.Fatalf("sys_runtime_config SQL 缺少 %q", want)
		}
	}
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS app_log",
		"app_code", "level", "message", "trace_id", "created_at",
		"log_id", "occurred_at", "source", "is_access",
		"uk_app_log_log_id", "idx_app_log_trace", "idx_app_log_occurred", "idx_app_log_access_occurred",
		"idx_app_log_created",
	} {
		if !strings.Contains(AppLogSchemaSQL, want) {
			t.Fatalf("app_log SQL 缺少 %q", want)
		}
	}
}

func TestEnsureSchemaAndListPublic(t *testing.T) {
	sdb, fdb := openFake(t)
	cipher, _ := NewConfigCipher("", "unit-jwt")
	store := NewStore(sdb, cipher)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	if len(fdb.execsWith("CREATE TABLE IF NOT EXISTS sys_runtime_config")) != 1 {
		t.Fatal("EnsureSchema 未执行建表")
	}
	sensEnc, _ := cipher.Encrypt("top-secret")
	fdb.onQuery = func(q string, args []driver.Value) (*fakeRows, error) {
		if strings.Contains(q, "ORDER BY cfg_key") {
			return rowsOf([]string{"cfg_key", "cfg_value", "is_sensitive"},
				[]driver.Value{"a.normal", "plain", int64(0)},
				[]driver.Value{"b.secret", sensEnc, int64(1)}), nil
		}
		return &fakeRows{cols: []string{"cfg_key", "cfg_value", "is_sensitive"}}, nil
	}
	out, err := store.ListPublic(context.Background())
	if err != nil {
		t.Fatalf("ListPublic: %v", err)
	}
	if out["a.normal"] != "plain" {
		t.Fatalf("普通项 = %q", out["a.normal"])
	}
	if out["b.secret"] != MaskSecret("top-secret") {
		t.Fatalf("敏感项 = %q", out["b.secret"])
	}
}
