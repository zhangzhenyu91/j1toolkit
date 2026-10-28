package auth

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
)

const goldenSM2K = "5927a2c43d3c6b0d5f9c1e6f6d8b3f5c4d2e1a0b9c8d7e6f5a4b3c2d1e0f1234"

// TestSM2EncryptGolden 黄金向量：固定 k 的 C1C3C2 原始布局，与 pylib/gmssl 3.2.2
// CryptSM2.encrypt(mode=1) 输出逐字节一致（由 gmssl 源码复刻脚本生成）。
func TestSM2EncryptGolden(t *testing.T) {
	pt := []byte("sm2-golden-vector")
	k, _ := new(big.Int).SetString(goldenSM2K, 16)
	raw, err := sm2EncryptWithK(pt, GDKey, k)
	if err != nil {
		t.Fatal(err)
	}
	want := "9ffdaad58dee9f66963b17099d99970a" +
		"920a304443b4f9839828e5ec5b396729" +
		"d69c5eb81b4a88f5d95f7ef371600114" +
		"07da587bfc47de28c94605ec1b30dd23" +
		"6591ab1f3476434d2e33a9b95e6bad78" +
		"69be5765a27f0d677a8b4a53f5cb33e0" +
		"2a5b0e12d38692231aa24598c6fba3e0" +
		"72"
	if hex.EncodeToString(raw) != want {
		t.Fatalf("raw = %x,\nwant %s", raw, want)
	}
}

// TestSM2GetUserAuthShape 黄金向量：getUserAuth 的 p 形态（js_quote 后 SM2，"04"+hex）。
func TestSM2GetUserAuthShape(t *testing.T) {
	tok := "T=abc.def-123&K=" + strings.Repeat("11", 16) + "&V=" + strings.Repeat("22", 16)
	quoted := JSQuote(tok)
	wantQuoted := "T%3Dabc.def-123%26K%3D11111111111111111111111111111111%26V%3D22222222222222222222222222222222"
	if quoted != wantQuoted {
		t.Fatalf("JSQuote = %s, want %s", quoted, wantQuoted)
	}
	k, _ := new(big.Int).SetString(goldenSM2K, 16)
	raw, err := sm2EncryptWithK([]byte(quoted), GDKey, k)
	if err != nil {
		t.Fatal(err)
	}
	if got := "04" + hex.EncodeToString(raw); len(got) != 2+len(raw)*2 {
		t.Fatalf("hex 长度错: %d", len(got))
	}
	// 与 gmssl 复刻脚本输出对拍（C1 相同 k 必然一致，此处全量比对）
	want := "9ffdaad58dee9f66963b17099d99970a" +
		"920a304443b4f9839828e5ec5b396729" +
		"d69c5eb81b4a88f5d95f7ef371600114" +
		"07da587bfc47de28c94605ec1b30dd23" +
		"cc8012901f9b2649a1d887692ab5346a" +
		"392ff0173b16bf5acf66f44702ee4b3c" +
		"0d130f7bd58b9d691ba90ec392aae4aa" +
		"32517b7e93bf2cb3659a1ea14df69f1e" +
		"b9ef0a2acca205a8acc371f93f0005e0" +
		"b65fb6bf5412e9cddec8d3fa45611fc6" +
		"eee7a68cd45bff3198b9a5789375eb7e" +
		"4513528387aba0be29fb9a864d"
	if hex.EncodeToString(raw) != want {
		t.Fatalf("getuserauth raw = %x,\nwant %s", raw, want)
	}
}

// TestSm2EncryptASN1DER DER 结构断言：SEQUENCE{INTEGER x, INTEGER y,
// OCTET STRING c3(32B), OCTET STRING c2}（结构已由抓包验证）。
func TestSm2EncryptASN1DER(t *testing.T) {
	pt := []byte("der-shape-check")
	hexStr, err := Sm2EncryptASN1(pt, GDKey)
	if err != nil {
		t.Fatal(err)
	}
	der, err := hex.DecodeString(hexStr)
	if err != nil {
		t.Fatal(err)
	}
	if der[0] != 0x30 {
		t.Fatalf("非 SEENCE: %x", der[0])
	}
	rest := der[1:]
	n, rest := consumeLen(rest)
	if n != len(rest) {
		t.Fatalf("SEQUENCE 长度不匹配: %d != %d", n, len(rest))
	}
	x, rest := consumeInt(rest)
	y, rest := consumeInt(rest)
	c3, rest := consumeOctet(rest)
	c2, rest := consumeOctet(rest)
	if len(rest) != 0 {
		t.Fatalf("DER 尾部残留 %d 字节", len(rest))
	}
	// x/y 为去前导零的整数，高位补零时 DER 可能带 0x00 前缀（≤33 字节）
	if len(x) > 33 || len(y) > 33 {
		t.Fatalf("x/y 超长: %d/%d", len(x), len(y))
	}
	if len(c3) != 32 {
		t.Fatalf("c3 长度 %d, want 32", len(c3))
	}
	if len(c2) != len(pt) {
		t.Fatalf("c2 长度 %d, want %d", len(c2), len(pt))
	}
}

// TestSM2NormalizePubkey 公钥归一化（vehicle_export._normalize_pubkey）。
func TestSM2NormalizePubkey(t *testing.T) {
	full := GDKey
	if got := normalizePubkey(full[2:]); got != full {
		t.Fatalf("128 位补 04 失败: %s", got)
	}
	if got := normalizePubkey("  " + full + "\n"); got != full {
		t.Fatalf("去空白失败: %s", got)
	}
	if got := normalizePubkey("04zz"); got != "" {
		t.Fatalf("非法公钥应返回空: %s", got)
	}
}

// consumeLen/consumeInt/consumeOctet 极简 DER 切分（测试断言用）。
func consumeLen(b []byte) (int, []byte) {
	if b[0]&0x80 == 0 {
		return int(b[0]), b[1:]
	}
	n := int(b[0] & 0x7F)
	v := 0
	for i := 0; i < n; i++ {
		v = v<<8 | int(b[1+i])
	}
	return v, b[1+n:]
}

func consumeInt(b []byte) ([]byte, []byte) {
	if b[0] != 0x02 {
		return nil, b
	}
	n, rest := consumeLen(b[1:])
	return rest[:n], rest[n:]
}

func consumeOctet(b []byte) ([]byte, []byte) {
	if b[0] != 0x04 {
		return nil, b
	}
	n, rest := consumeLen(b[1:])
	return rest[:n], rest[n:]
}
