package cryptoaudio

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// RSA PKCS#1 v1.5 分块口径（jsonx 通道，1024 位密钥）：
// 加密块 = 模长-11 = 117B，解密块 = 模长 = 128B（Node protocol.js 同口径）。
// 非 1024 位密钥按实际模长换算分块（兼容测试用 2048 位）。

// B64ToPEM Node toPem 同口径：裸 base64 DER → PEM（每行 64 字符）。
func B64ToPEM(b64, label string) string {
	body := strings.Join(strings.Fields(b64), "")
	var lines []string
	for i := 0; i < len(body); i += 64 {
		end := i + 64
		if end > len(body) {
			end = len(body)
		}
		lines = append(lines, body[i:end])
	}
	return fmt.Sprintf("-----BEGIN %s-----\n%s\n-----END %s-----", label, strings.Join(lines, "\n"), label)
}

// parseDER 解析 PEM 或裸 base64 DER。
func parseDER(input []byte) ([]byte, error) {
	s := strings.TrimSpace(string(input))
	if strings.Contains(s, "-----BEGIN") {
		block, _ := pem.Decode([]byte(s))
		if block == nil {
			return nil, errors.New("cryptoaudio: PEM 解析失败")
		}
		return block.Bytes, nil
	}
	der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		return nil, fmt.Errorf("cryptoaudio: base64 DER 解码: %w", err)
	}
	return der, nil
}

// ParseRSAPublicKey 解析 RSA 公钥：PEM（PKIX / PKCS#1）或裸 base64 DER（env 配置口径）。
func ParseRSAPublicKey(input []byte) (*rsa.PublicKey, error) {
	der, err := parseDER(input)
	if err != nil {
		return nil, err
	}
	if pub, err := x509.ParsePKIXPublicKey(der); err == nil {
		if rp, ok := pub.(*rsa.PublicKey); ok {
			return rp, nil
		}
		return nil, errors.New("cryptoaudio: 非 RSA 公钥")
	}
	if rp, err := x509.ParsePKCS1PublicKey(der); err == nil {
		return rp, nil
	}
	return nil, errors.New("cryptoaudio: RSA 公钥解析失败")
}

// ParseRSAPrivateKey 解析 RSA 私钥：PEM（PKCS#8 / PKCS#1）或裸 base64 DER（env 配置口径）。
func ParseRSAPrivateKey(input []byte) (*rsa.PrivateKey, error) {
	der, err := parseDER(input)
	if err != nil {
		return nil, err
	}
	if key, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		if rp, ok := key.(*rsa.PrivateKey); ok {
			return rp, nil
		}
		return nil, errors.New("cryptoaudio: 非 RSA 私钥")
	}
	if rp, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return rp, nil
	}
	return nil, errors.New("cryptoaudio: RSA 私钥解析失败")
}

// RSAEncryptLong PKCS#1 v1.5 分块加密（1024 位：每 117B 一块），输出原始拼接密文。
func RSAEncryptLong(pub *rsa.PublicKey, data []byte) ([]byte, error) {
	if pub == nil {
		return nil, errors.New("cryptoaudio: 公钥为空")
	}
	chunk := pub.Size() - 11
	if chunk <= 0 {
		return nil, errors.New("cryptoaudio: RSA 模长非法")
	}
	var out []byte
	for i := 0; i < len(data); i += chunk {
		end := i + chunk
		if end > len(data) {
			end = len(data)
		}
		part, err := rsa.EncryptPKCS1v15(rand.Reader, pub, data[i:end])
		if err != nil {
			return nil, fmt.Errorf("cryptoaudio: RSA 分块加密: %w", err)
		}
		out = append(out, part...)
	}
	return out, nil
}

// RSADecryptLong PKCS#1 v1.5 分块解密（1024 位：每 128B 一块）。
func RSADecryptLong(priv *rsa.PrivateKey, ct []byte) ([]byte, error) {
	if priv == nil {
		return nil, errors.New("cryptoaudio: 私钥为空")
	}
	chunk := priv.Size()
	if len(ct)%chunk != 0 {
		return nil, fmt.Errorf("cryptoaudio: RSA 密文长度 %d 非 %d 的倍数", len(ct), chunk)
	}
	var out []byte
	for i := 0; i < len(ct); i += chunk {
		part, err := rsa.DecryptPKCS1v15(rand.Reader, priv, ct[i:i+chunk])
		if err != nil {
			return nil, fmt.Errorf("cryptoaudio: RSA 分块解密: %w", err)
		}
		out = append(out, part...)
	}
	return out, nil
}

// RSAEncryptLongPEM 同 RSAEncryptLong，入参为 PEM/裸 base64 DER 公钥；输出 base64（协议 wire 口径）。
func RSAEncryptLongPEM(pubInput []byte, data []byte) (string, error) {
	pub, err := ParseRSAPublicKey(pubInput)
	if err != nil {
		return "", err
	}
	ct, err := RSAEncryptLong(pub, data)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ct), nil
}

// RSADecryptLongPEM 同 RSADecryptLong，入参为 PEM/裸 base64 DER 私钥与 base64 密文。
func RSADecryptLongPEM(privInput []byte, b64 string) ([]byte, error) {
	priv, err := ParseRSAPrivateKey(privInput)
	if err != nil {
		return nil, err
	}
	ct, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(b64), ""))
	if err != nil {
		return nil, fmt.Errorf("cryptoaudio: base64 解码: %w", err)
	}
	return RSADecryptLong(priv, ct)
}
