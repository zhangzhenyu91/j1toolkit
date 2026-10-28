package cryptoaudio

import (
	"bytes"
	"encoding/hex"
	"os"
	"testing"

	"github.com/emmansun/gmsm/sm2"
)

// TestSM2DeterministicInterop 用固定标量 k=1 对比 Go 与 sm-crypto 的完整密文。
func TestSM2DeterministicInterop(t *testing.T) {
	pubHex := os.Getenv("SM2_DETERMINISTIC_PUB")
	if pubHex == "" {
		t.Skip("未提供 SM2_DETERMINISTIC_PUB，跳过确定性互操作")
	}
	pub, err := ParseSM2PublicKey(pubHex)
	if err != nil {
		t.Fatal(err)
	}
	seed := make([]byte, 32)
	seed[31] = 1
	ct, err := sm2.Encrypt(bytes.NewReader(seed), pub, []byte("1234567890abcdef"),
		sm2.NewPlainEncrypterOpts(sm2.MarshalUncompressed, sm2.C1C2C3))
	if err != nil {
		t.Fatal(err)
	}
	goHex := hex.EncodeToString(ct)
	nodePath, workDir := nodeSm2Env(t)
	out := runNode(t, nodePath, workDir,
		`const {sm2}=require('sm-crypto');const u=require('sm-crypto/src/sm2/utils');
		const k='0000000000000000000000000000000000000000000000000000000000000001';
		u.generateKeyPairHex=()=>({privateKey:k,publicKey:sm2.getPublicKeyFromPrivateKey(k)});
		const [pub,go]=process.argv.slice(1);const node=sm2.doEncrypt('1234567890abcdef',pub,0);
		console.log(node===go?'MATCH':'DIFF');`, pubHex, goHex[2:])
	if out != "MATCH" {
		t.Fatalf("Go 与 sm-crypto 确定性密文不一致: %s", out)
	}
}
