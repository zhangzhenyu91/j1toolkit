package cryptoaudio

import (
	"os"
	"testing"
)

// TestAlternativeSM2C1Valid 生产 SM2EncryptHex 的 C1 必须被 Node sm-crypto 认可。
func TestAlternativeSM2C1Valid(t *testing.T) {
	pubHex := os.Getenv("SM2_DETERMINISTIC_PUB")
	if pubHex == "" {
		t.Skip("未提供 SM2_DETERMINISTIC_PUB，跳过生产 SM2 输出验证")
	}
	ct, err := SM2EncryptHex("1234567890abcdef", pubHex)
	if err != nil {
		t.Fatal(err)
	}
	nodePath, workDir := nodeSm2Env(t)
	out := runNode(t, nodePath, workDir,
		`const {sm2}=require('sm-crypto');const c1=process.argv[1];console.log(sm2.verifyPublicKey('04'+c1));`, ct[2:130])
	if out != "true" {
		t.Fatalf("生产 SM2 C1 被 sm-crypto 判为非法: %s", out)
	}
}

func hexEncode(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = digits[v>>4]
		out[i*2+1] = digits[v&15]
	}
	return string(out)
}
