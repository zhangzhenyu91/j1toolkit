package cryptoaudio

import (
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"github.com/emmansun/gmsm/sm2"
	"github.com/emmansun/gmsm/smx509"
)

// SM2 加解密口径（与 Node sm-crypto / server/src/sgccclockin/protocol.js 对齐）：
//   - sm-crypto doEncrypt(mode 0) = C1C2C3 拼接，C1 为 64B 裸点（X||Y，无 04 前缀）；
//     protocol.js 的 wire 格式 = '04' + doEncrypt(...)，即 04||X||Y||C2||C3 的 hex。
//   - gmsm 走 plain 编码 + MarshalUncompressed（SEC1 非压缩点，自带 04）+ C1C2C3 拼接，
//     本文件统一归一到上述 wire 格式；解密对 04 前缀有无均兼容。

// ParseSM2PublicKey 解析 SM2 公钥：04 前缀或裸点 hex（130/128 hex，sm-crypto 口径）或 PEM（PKIX）。
func ParseSM2PublicKey(s string) (*ecdsa.PublicKey, error) {
	body := strings.Join(strings.Fields(s), "")
	if body == "" {
		return nil, errors.New("cryptoaudio: SM2 公钥为空")
	}
	if strings.Contains(body, "-----BEGIN") {
		block, _ := pem.Decode([]byte(body))
		if block == nil {
			return nil, errors.New("cryptoaudio: SM2 公钥 PEM 解析失败")
		}
		pub, err := smx509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("cryptoaudio: SM2 公钥 DER 解析: %w", err)
		}
		ep, ok := pub.(*ecdsa.PublicKey)
		if !ok {
			return nil, errors.New("cryptoaudio: 非 SM2/EC 公钥")
		}
		return ep, nil
	}
	raw, err := hex.DecodeString(body)
	if err != nil {
		return nil, fmt.Errorf("cryptoaudio: SM2 公钥 hex 解码: %w", err)
	}
	if len(raw) == 64 { // sm-crypto 裸点 X||Y → 补 04
		raw = append([]byte{0x04}, raw...)
	}
	return sm2.NewPublicKey(raw)
}

// ParseSM2PrivateKey 解析 SM2 私钥：64 hex 标量（sm-crypto 口径）或 PEM（PKCS#8）。
func ParseSM2PrivateKey(s string) (*sm2.PrivateKey, error) {
	body := strings.Join(strings.Fields(s), "")
	if body == "" {
		return nil, errors.New("cryptoaudio: SM2 私钥为空")
	}
	if strings.Contains(body, "-----BEGIN") {
		block, _ := pem.Decode([]byte(body))
		if block == nil {
			return nil, errors.New("cryptoaudio: SM2 私钥 PEM 解析失败")
		}
		key, err := smx509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("cryptoaudio: SM2 私钥 DER 解析: %w", err)
		}
		sp, ok := key.(*sm2.PrivateKey)
		if !ok {
			return nil, errors.New("cryptoaudio: 非 SM2 私钥")
		}
		return sp, nil
	}
	raw, err := hex.DecodeString(body)
	if err != nil {
		return nil, fmt.Errorf("cryptoaudio: SM2 私钥 hex 解码: %w", err)
	}
	// sm-crypto 接受前导零标量（如 33 字节 00||32B）；Go 严格乘法器只收 32B。
	// 仅规整前导零，超出 32B 的非零前缀仍拒绝，不改变私钥数值。
	for len(raw) > 32 && raw[0] == 0 {
		raw = raw[1:]
	}
	if len(raw) > 32 {
		return nil, errors.New("cryptoaudio: SM2 私钥标量超过 32 字节")
	}
	if len(raw) < 32 {
		padded := make([]byte, 32)
		copy(padded[32-len(raw):], raw)
		raw = padded
	}
	return sm2.NewPrivateKey(raw)
}

// GenerateSM2KeyPairHex 生成随机 SM2 密钥对（测试用），返回 sm-crypto 口径 hex：
// priv 64 hex 标量；pub 130 hex（04||X||Y）。
func GenerateSM2KeyPairHex() (privHex, pubHex string, err error) {
	key, err := sm2.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	privHex = fmt.Sprintf("%064x", key.D)
	pubHex = fmt.Sprintf("04%064x%064x", key.PublicKey.X, key.PublicKey.Y)
	return privHex, pubHex, nil
}

// SM2EncryptHex 加密文本，返回 protocol.js wire 格式：04 前缀 + C1C2C3 hex。
func SM2EncryptHex(plain, pubStr string) (string, error) {
	pub, err := ParseSM2PublicKey(pubStr)
	if err != nil {
		return "", err
	}
	ct, err := sm2.Encrypt(rand.Reader, pub, []byte(plain),
		sm2.NewPlainEncrypterOpts(sm2.MarshalUncompressed, sm2.C1C2C3))
	if err != nil {
		return "", fmt.Errorf("cryptoaudio: SM2 加密: %w", err)
	}
	if len(ct) == 0 || ct[0] != 0x04 {
		ct = append([]byte{0x04}, ct...) // 归一到 wire 格式
	}
	return hex.EncodeToString(ct), nil
}

// SM2DecryptHex 解密 wire 格式（04 前缀可省略）hex 密文，返回明文文本。
func SM2DecryptHex(ctHex, privStr string) (string, error) {
	priv, err := ParseSM2PrivateKey(privStr)
	if err != nil {
		return "", err
	}
	ct, err := hex.DecodeString(strings.TrimSpace(ctHex))
	if err != nil {
		return "", fmt.Errorf("cryptoaudio: SM2 密文 hex 解码: %w", err)
	}
	if len(ct) < 64 {
		return "", errors.New("cryptoaudio: SM2 密文过短")
	}
	opts := sm2.NewPlainDecrypterOpts(sm2.C1C2C3)
	// 对 04 前缀有无均兼容：按原样、补 04、去 04 依次尝试
	variants := [][]byte{ct}
	if ct[0] == 0x04 {
		variants = append(variants, ct[1:])
	} else {
		variants = append([][]byte{append([]byte{0x04}, ct...)}, variants...)
	}
	var lastErr error
	for _, v := range variants {
		pt, err := priv.Decrypt(nil, v, opts)
		if err == nil {
			return string(pt), nil
		}
		lastErr = err
	}
	return "", fmt.Errorf("cryptoaudio: SM2 解密失败: %w", lastErr)
}
