package cryptoaudio

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
)

// newTestRSAKey 生成 1024 位测试密钥（117/128 分块口径对应的模长）。
func newTestRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("生成测试密钥: %v", err)
	}
	return key
}

func TestRSAChunkRoundTrip(t *testing.T) {
	key := newTestRSAKey(t)
	if key.Size() != 128 {
		t.Fatalf("模长 = %d，期望 128（1024 位）", key.Size())
	}
	plain := bytes.Repeat([]byte("商旅jsonx分块加密测试数据"), 20) // >117B → 多块
	if len(plain) <= 117 {
		t.Fatalf("明文 %d 字节应超过单块 117", len(plain))
	}
	ct, err := RSAEncryptLong(&key.PublicKey, plain)
	if err != nil {
		t.Fatal(err)
	}
	blocks := (len(plain) + 116) / 117
	if len(ct) != blocks*128 {
		t.Fatalf("密文长度 = %d，期望 %d（%d 块 × 128B）", len(ct), blocks*128, blocks)
	}
	got, err := RSADecryptLong(key, ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("RSA 分块回环不一致")
	}
	if _, err := RSADecryptLong(key, ct[:100]); err == nil {
		t.Fatal("非整块密文应报错")
	}
}

func TestRSAKeyParseAllFormats(t *testing.T) {
	key := newTestRSAKey(t)

	pkixDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pkixPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pkixDER})
	pkcs1PubPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)})
	barePub := []byte(base64.StdEncoding.EncodeToString(pkixDER))

	for _, in := range [][]byte{pkixPEM, pkcs1PubPEM, barePub} {
		pub, err := ParseRSAPublicKey(in)
		if err != nil {
			t.Fatalf("公钥解析失败: %v", err)
		}
		if pub.N.Cmp(key.PublicKey.N) != 0 {
			t.Fatal("公钥不一致")
		}
	}

	pkcs8DER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8PEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8DER})
	pkcs1PEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	barePriv := []byte(base64.StdEncoding.EncodeToString(pkcs8DER))

	for _, in := range [][]byte{pkcs8PEM, pkcs1PEM, barePriv} {
		priv, err := ParseRSAPrivateKey(in)
		if err != nil {
			t.Fatalf("私钥解析失败: %v", err)
		}
		if priv.N.Cmp(key.PublicKey.N) != 0 {
			t.Fatal("私钥不一致")
		}
	}
}

func TestRSAWireB64(t *testing.T) {
	key := newTestRSAKey(t)
	pubPEM := B64ToPEM(base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PublicKey(&key.PublicKey)), "PUBLIC KEY")
	privPEM := B64ToPEM(base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(key)), "PRIVATE KEY")
	if !strings.HasPrefix(pubPEM, "-----BEGIN PUBLIC KEY-----") {
		t.Fatal("B64ToPEM 头部不符")
	}

	b64, err := RSAEncryptLongPEM([]byte(pubPEM), []byte(`{"funcCode":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := RSADecryptLongPEM([]byte(privPEM), b64)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"funcCode":"test"}` {
		t.Fatalf("解密 = %q", got)
	}
}

func TestRSABadInputs(t *testing.T) {
	if _, err := RSAEncryptLong(nil, []byte("x")); err == nil {
		t.Fatal("空公钥应报错")
	}
	if _, err := ParseRSAPublicKey([]byte("!!!")); err == nil {
		t.Fatal("非法公钥应报错")
	}
	if _, err := ParseRSAPrivateKey([]byte("!!!")); err == nil {
		t.Fatal("非法私钥应报错")
	}
}
