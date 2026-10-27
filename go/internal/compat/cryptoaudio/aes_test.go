package cryptoaudio

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

// TestAESECBKnownVector openssl 实测向量（key "1234567890abcdef"，PKCS#7 填充）：
// printf 'hello' | openssl enc -aes-128-ecb -K 31323334353637383930616263646566 -nosalt
func TestAESECBKnownVector(t *testing.T) {
	key := []byte("1234567890abcdef")
	ct, err := AESEncryptECB([]byte("hello"), key)
	if err != nil {
		t.Fatal(err)
	}
	const want = "3ae1bf1edbd01914c379e7a9e993bf59"
	if hex.EncodeToString(ct) != want {
		t.Fatalf("密文 = %x，期望 %s", ct, want)
	}
	pt, err := AESDecryptECB(ct, key)
	if err != nil {
		t.Fatal(err)
	}
	if string(pt) != "hello" {
		t.Fatalf("解密 = %q", pt)
	}
}

func TestAECBRoundTrip(t *testing.T) {
	key := []byte("ABCDEFGHIJKLMNOP")
	for _, plain := range []string{"", "a", strings.Repeat("数据", 50), strings.Repeat("x", 16)} {
		ct, err := AESEncryptECB([]byte(plain), key)
		if err != nil {
			t.Fatal(err)
		}
		if len(ct)%16 != 0 {
			t.Fatalf("密文长度 %d 非块大小倍数", len(ct))
		}
		pt, err := AESDecryptECB(ct, key)
		if err != nil {
			t.Fatal(err)
		}
		if string(pt) != plain {
			t.Fatalf("回环不一致: %q → %q", plain, pt)
		}
	}
}

func TestAESB64AndKeyGen(t *testing.T) {
	key, err := GenAESKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 16 {
		t.Fatalf("密钥长度 = %d", len(key))
	}
	b64, err := AESEncryptECBB64("jsonm 通道明文", key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := AESDecryptECBB64(b64, key)
	if err != nil {
		t.Fatal(err)
	}
	if got != "jsonm 通道明文" {
		t.Fatalf("解密 = %q", got)
	}
	if _, err := AESEncryptECB([]byte("x"), []byte("short")); err == nil {
		t.Fatal("短密钥应报错")
	}
	if _, err := AESDecryptECB([]byte("bad"), []byte("ABCDEFGHIJKLMNOP")); err == nil {
		t.Fatal("非法密文应报错")
	}
}

func TestPKCS7UnpadRejectsBadPadding(t *testing.T) {
	if _, err := pkcs7Unpad(nil); err == nil {
		t.Fatal("空输入应报错")
	}
	bad := append(bytes.Repeat([]byte{4}, 15), 3)
	if _, err := pkcs7Unpad(bad); err == nil {
		t.Fatal("非法填充应报错")
	}
}
