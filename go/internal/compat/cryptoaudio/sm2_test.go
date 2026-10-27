package cryptoaudio

import (
	"strings"
	"testing"
)

func TestSM2KeyGenFormat(t *testing.T) {
	privHex, pubHex, err := GenerateSM2KeyPairHex()
	if err != nil {
		t.Fatal(err)
	}
	if len(privHex) != 64 {
		t.Fatalf("私钥 hex 长度 = %d，期望 64（sm-crypto 口径）", len(privHex))
	}
	if len(pubHex) != 130 || !strings.HasPrefix(pubHex, "04") {
		t.Fatalf("公钥 hex 长度/前缀不符: %q", pubHex)
	}
	if _, err := ParseSM2PublicKey(pubHex); err != nil {
		t.Fatalf("公钥解析: %v", err)
	}
	if _, err := ParseSM2PublicKey(strings.TrimPrefix(pubHex, "04")); err != nil {
		t.Fatalf("裸点公钥解析: %v", err)
	}
	if _, err := ParseSM2PrivateKey(privHex); err != nil {
		t.Fatalf("私钥解析: %v", err)
	}
}

func TestSM2RoundTripWireFormat(t *testing.T) {
	privHex, pubHex, err := GenerateSM2KeyPairHex()
	if err != nil {
		t.Fatal(err)
	}
	const msg = "商旅 jsonm 通道 AES 密钥"
	ctHex, err := SM2EncryptHex(msg, pubHex)
	if err != nil {
		t.Fatal(err)
	}
	// wire 格式：04||X||Y||C2||C3 → 65+32+len(msg) 字节
	if !strings.HasPrefix(ctHex, "04") {
		t.Fatalf("密文应带 04 前缀: %s…", ctHex[:8])
	}
	if want := 2 * (65 + 32 + len(msg)); len(ctHex) != want {
		t.Fatalf("密文 hex 长度 = %d，期望 %d（C1C2C3 wire 口径）", len(ctHex), want)
	}

	got, err := SM2DecryptHex(ctHex, privHex)
	if err != nil {
		t.Fatal(err)
	}
	if got != msg {
		t.Fatalf("解密 = %q", got)
	}

	// 无 04 前缀（sm-crypto doEncrypt 原生格式）也应可解
	got, err = SM2DecryptHex(ctHex[2:], privHex)
	if err != nil {
		t.Fatal(err)
	}
	if got != msg {
		t.Fatalf("无 04 解密 = %q", got)
	}
}

func TestSM2BadInputs(t *testing.T) {
	if _, err := SM2EncryptHex("x", "00"); err == nil {
		t.Fatal("非法公钥应报错")
	}
	privHex, pubHex, err := GenerateSM2KeyPairHex()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SM2DecryptHex("zz", privHex); err == nil {
		t.Fatal("非法密文应报错")
	}
	if _, err := SM2DecryptHex("04ab", privHex); err == nil {
		t.Fatal("过短密文应报错")
	}
	if _, err := SM2DecryptHex("04ab", "00"); err == nil {
		t.Fatal("非法私钥应报错")
	}
	if _, err := ParseSM2PublicKey("-----BEGIN PUBLIC KEY-----\nzz\n-----END PUBLIC KEY-----"); err == nil {
		t.Fatal("非法 PEM 应报错")
	}
	_ = pubHex
}
