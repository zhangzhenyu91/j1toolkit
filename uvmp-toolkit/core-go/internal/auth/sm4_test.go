package auth

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// TestSM4CBCGolden 黄金向量：与 vehicle_export.sm4_cbc_decrypt_hex（gmssl sm4 同源）逐字节对拍。
func TestSM4CBCGolden(t *testing.T) {
	key, _ := hex.DecodeString("0123456789abcdeffedcba9876543210")
	iv, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	cipher := "b119e199b80e74da1b3bbcdcd914189197c408056bea3f0b6912f2eb36761b04"
	plainHex := "534d342d43424320e6b58be8af95e6988ee6968720313233" // "SM4-CBC 测试明文 123"
	want, _ := hex.DecodeString(plainHex)

	got, err := Sm4CBCDecryptHex(cipher, key, iv)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("decrypt = %x, want %x", got, want)
	}
}

// TestSM4CBCEncryptRoundTrip 用测试内实现的加密回环验证解密与 PKCS7 全块填充。
func TestSM4CBCEncryptRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, 16)
	iv := bytes.Repeat([]byte{0x22}, 16)
	for _, pt := range [][]byte{
		[]byte("x"),
		bytes.Repeat([]byte("a"), 16), // 整块：PKCS7 填满 16 字节
		[]byte("SM4-CBC 测试明文 123"),
	} {
		ct := sm4CBCEncryptForTest(t, pt, key, iv)
		got, err := Sm4CBCDecryptHex(hex.EncodeToString(ct), key, iv)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, pt) {
			t.Fatalf("round-trip 失败: %x != %x", got, pt)
		}
	}
}

// TestSM4BadInput 非法输入报错。
func TestSM4BadInput(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 16)
	iv := bytes.Repeat([]byte{2}, 16)
	if _, err := Sm4CBCDecryptHex("zz", key, iv); err == nil {
		t.Fatal("非法 hex 应报错")
	}
	if _, err := Sm4CBCDecryptHex("0011", key, iv); err == nil {
		t.Fatal("长度非 16 倍数应报错")
	}
	if _, err := Sm4CBCDecryptHex("00", []byte{1}, iv); err == nil {
		t.Fatal("短 key 应报错")
	}
}

// sm4CBCEncryptForTest 测试夹具：SM4-CBC/PKCS7 加密（复用生产块实现，仅测试文件内）。
func sm4CBCEncryptForTest(t *testing.T, plain, key, iv []byte) []byte {
	t.Helper()
	pad := 16 - len(plain)%16
	data := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	rk := sm4RoundKeys(key, false)
	out := make([]byte, 0, len(data))
	last := iv
	for off := 0; off < len(data); off += 16 {
		block := make([]byte, 16)
		for i := 0; i < 16; i++ {
			block[i] = data[off+i] ^ last[i]
		}
		enc := sm4Block(block, rk)
		out = append(out, enc...)
		last = enc
	}
	return out
}
