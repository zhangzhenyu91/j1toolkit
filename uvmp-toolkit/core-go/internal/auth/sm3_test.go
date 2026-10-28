package auth

import "testing"

// TestSM3Golden 标准向量（GB/T 32905 附录 A）与 gmssl sm3_hash 输出一致。
func TestSM3Golden(t *testing.T) {
	cases := []struct {
		msg  string
		want string
	}{
		{"abc", "66c7f0f462eeedd9d1f2d46bdc10e4e24167c4875cf2f7a2297da02b8f4ba8e0"},
		{repeat("abcd", 16), "debe9ff92275b8a138604889c18e5a4d6fdb70e5387e5765293dcba39c0c5732"},
	}
	for _, c := range cases {
		if got := sm3Hex([]byte(c.msg)); got != c.want {
			t.Errorf("sm3Hex(%q) = %s, want %s", clipT(c.msg), got, c.want)
		}
	}
}

// TestSM3SignTriple 黄金向量：SSO 防重放三元组 sign = SM3("requestTime,nonce")。
func TestSM3SignTriple(t *testing.T) {
	got := sm3Hex([]byte("1761600000000" + "," + "a1b2c3d4e5f64a7b8c9d0e1f2a3b4c5d"))
	want := "2dbb5e6dd683ffaaa48cba707ffa8f14a15955e1585cc93f65fe5c9ce2a3a5fc"
	if got != want {
		t.Errorf("sign triple sm3 = %s, want %s", got, want)
	}
}

// TestSM3Kdf KDF 长度与确定性（分块拼接口径）。
func TestSM3Kdf(t *testing.T) {
	z := make([]byte, 64)
	for i := range z {
		z[i] = byte(i)
	}
	k1 := sm3Kdf(z, 17)
	if len(k1) != 17 {
		t.Fatalf("kdf len = %d, want 17", len(k1))
	}
	k2 := sm3Kdf(z, 17)
	for i := range k1 {
		if k1[i] != k2[i] {
			t.Fatalf("kdf 不确定 @%d", i)
		}
	}
	// 前 32 字节即第一块 SM3(z||00000001)
	block := append(append([]byte{}, z...), 0, 0, 0, 1)
	if got, want := sm3Hex(k1[:0]), sm3Hex([]byte{}); got != want {
		t.Fatalf("sanity: %s != %s", got, want)
	}
	full := sm3Sum(block)
	for i := 0; i < 17; i++ {
		if k1[i] != full[i] {
			t.Fatalf("kdf 块错位 @%d", i)
		}
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

func clipT(s string) string {
	if len(s) > 24 {
		return s[:24] + "..."
	}
	return s
}
