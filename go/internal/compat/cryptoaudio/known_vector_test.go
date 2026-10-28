package cryptoaudio

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestSM2PrivateKeyLeadingZeroScalar 用逆向仓的 33 字节前导零私钥验证 32B 规范化。
func TestSM2PrivateKeyLeadingZeroScalar(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "private", "esgcc", "sgcc", "tools", "sm2_test.json"))
	if err != nil {
		t.Skip("固定 SM2 私钥样例不可用")
	}
	var fixture struct {
		Priv string `json:"priv"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("读取固定私钥样例失败: %v", err)
	}
	if _, err := ParseSM2PrivateKey(fixture.Priv); err != nil {
		t.Fatalf("前导零私钥应规范化后解析成功: %v", err)
	}
}
