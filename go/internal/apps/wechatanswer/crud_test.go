// 规则 CRUD 语义测试：每群每类最多一条（触发类型锁定）、独立密钥、空值保持已存值（不丢已有键），
// keys 对象与旧扁平键名双兼容。
package wechatanswer

import (
	"encoding/json"
	"testing"
)

func bodyOf(t *testing.T, raw string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestParseRuleFormValidates(t *testing.T) {
	if _, err := parseRuleForm(bodyOf(t, `{"type":"crossWatch"}`)); err == nil {
		t.Fatal("crossWatch 触发类型应被拒绝")
	}
	if _, err := parseRuleForm(bodyOf(t, `{"type":"treeWatch","name":"一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五"}`)); err == nil {
		t.Fatal("规则名称超长应被拒绝")
	}
	if _, err := parseRuleForm(bodyOf(t, `{"type":"treeWatch","keys":{"unknown":"x"}}`)); err == nil {
		t.Fatal("未知密钥字段应被拒绝")
	}
	f, err := parseRuleForm(bodyOf(t, `{"type":"treeWatch","name":"输电班组运维","enabled":true,"ask_enabled":true,
    "keys":{"tree_extract":"k1","tree_correct":"k2","crossing_extract":"k3","ask_name":"k4"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if f.Trigger != TriggerTreeWatch || f.Keys["crossing_extract"] != "k3" || f.Name != "输电班组运维" || !f.AskEnabled {
		t.Fatalf("表单解析不符: %+v", f)
	}
}

func TestParseRuleFormLegacyKeys(t *testing.T) {
	f, err := parseRuleForm(bodyOf(t, `{"type":"treeWatch","extract_key":"k1","correct_key":"k2","crossing_key":"k3","crossing_correct_key":"k4"}`))
	if err != nil {
		t.Fatal(err)
	}
	if f.Keys["tree_extract"] != "k1" || f.Keys["tree_correct"] != "k2" ||
		f.Keys["crossing_extract"] != "k3" || f.Keys["crossing_correct"] != "k4" {
		t.Fatalf("旧扁平键名应映射到契约键: %+v", f.Keys)
	}
}

func TestMergeRuleIndependentKeys(t *testing.T) {
	cur := Rule{Trigger: TriggerTreeWatch, ExtractKey: "old-extract", CorrectKey: "old-correct",
		CrossingKey: "old-cross", AskNameKey: "old-ask"}
	f, _ := parseRuleForm(bodyOf(t, `{"type":"treeWatch","name":"输电班组运维","keys":{"tree_extract":"","tree_correct":"new-correct","crossing_correct":"new-cross-correct"}}`))
	if err := mergeRule(&cur, f); err != nil {
		t.Fatal(err)
	}
	// 空值提交保持已存值（不丢已有键）；非空覆盖；各 Key 独立
	if cur.ExtractKey != "old-extract" || cur.CrossingKey != "old-cross" || cur.AskNameKey != "old-ask" {
		t.Fatalf("空 Key 应保持已存值: %+v", cur)
	}
	if cur.CorrectKey != "new-correct" || cur.CrossingCorrectKey != "new-cross-correct" {
		t.Fatalf("更新未生效: %+v", cur)
	}
}

func TestMergeRuleRequiresKeys(t *testing.T) {
	cur := Rule{Trigger: TriggerTreeWatch}
	f, _ := parseRuleForm(bodyOf(t, `{"type":"treeWatch"}`))
	if err := mergeRule(&cur, f); err == nil {
		t.Fatal("treeWatch 缺树障提取/更正 Key 应报错")
	}
	loc := Rule{Trigger: TriggerLocation}
	f2, _ := parseRuleForm(bodyOf(t, `{"type":"location"}`))
	if err := mergeRule(&loc, f2); err == nil {
		t.Fatal("location 缺位置 Key 应报错")
	}
	// 提问开启时 ask_name 必配
	ask := Rule{Trigger: TriggerTreeWatch}
	f3, _ := parseRuleForm(bodyOf(t, `{"type":"treeWatch","ask_enabled":true,"keys":{"tree_extract":"a","tree_correct":"b"}}`))
	if err := mergeRule(&ask, f3); err == nil {
		t.Fatal("启用群内提问缺命名 Key 应报错")
	}
	// location 规则写 location Key
	f4, _ := parseRuleForm(bodyOf(t, `{"type":"location","keys":{"location":"loc-key","tree_correct":"ignored"}}`))
	if err := mergeRule(&loc, f4); err != nil {
		t.Fatal(err)
	}
	if loc.LocationKey != "loc-key" {
		t.Fatalf("location Key 未写入: %+v", loc)
	}
}

func TestRuleViewNeverExposesSecrets(t *testing.T) {
	m := &Module{}
	r := Rule{ID: 1, GroupID: "g@chatroom", Trigger: TriggerTreeWatch, Enabled: true,
		ExtractKey: "secret-extract", CorrectKey: "secret-correct", AskEnabled: true, AskNameKey: "secret-ask"}
	v := m.ruleToView(nil, r)
	buf, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-extract", "secret-correct", "secret-ask"} {
		if containsStr(string(buf), secret) {
			t.Fatalf("规则视图泄密: %s", buf)
		}
	}
	if !v.KeysConfigured["tree_extract"] || !v.KeysConfigured["ask_name"] {
		t.Fatalf("keys_configured 未标记已配置: %+v", v.KeysConfigured)
	}
	if !v.Complete {
		t.Fatal("密钥齐全应 complete=true")
	}
	if v.Type != TriggerTreeWatch || v.Name != "输电班组运维" {
		t.Fatalf("视图字段不符: %+v", v)
	}
}

// containsStr 最小子串判断（避免引 strings 仅为测试）。
func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
