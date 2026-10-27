package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// AES-256-GCM 配置值加密器（CONFIG_MASTER_KEY 缺省从 JWT_SECRET 派生）。
// 密文格式：EncPrefix + base64(nonce||ciphertext)，供 sys_runtime_config 敏感项落库。

const (
	// EncPrefix 敏感值密文前缀（版本留位，IsEncrypted 判定口径）。
	EncPrefix = "enc:v1:"
)

// ErrNoMasterKey 未配置任何主密钥来源（CONFIG_MASTER_KEY 与 JWT_SECRET 均为空）。
var ErrNoMasterKey = errors.New("config: 缺少主密钥（CONFIG_MASTER_KEY 与 JWT_SECRET 均为空）")

// ConfigCipher 配置值 AES-256-GCM 加解密器。
type ConfigCipher struct {
	aead cipher.AEAD
}

// NewConfigCipher 构造加密器。主密钥取值优先级：
// CONFIG_MASTER_KEY 非空时按「64 hex → 原 32 字节 / base64 → 原 32 字节 / 其他 → SHA-256(原文)」解释；
// 否则从 JWT_SECRET 派生（SHA-256）；两者皆空报 ErrNoMasterKey。
func NewConfigCipher(configMasterKey, jwtSecret string) (*ConfigCipher, error) {
	key, err := deriveKey(configMasterKey, jwtSecret)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &ConfigCipher{aead: aead}, nil
}

// deriveKey 派生 32 字节主密钥。
func deriveKey(configMasterKey, jwtSecret string) ([]byte, error) {
	if configMasterKey != "" {
		if raw, err := hex.DecodeString(configMasterKey); err == nil && len(raw) == 32 {
			return raw, nil
		}
		if raw, err := base64.StdEncoding.DecodeString(configMasterKey); err == nil && len(raw) == 32 {
			return raw, nil
		}
		sum := sha256.Sum256([]byte(configMasterKey))
		return sum[:], nil
	}
	if jwtSecret != "" {
		sum := sha256.Sum256([]byte(jwtSecret))
		return sum[:], nil
	}
	return nil, ErrNoMasterKey
}

// Encrypt 加密明文，返回 EncPrefix + base64(nonce||密文)。
func (c *ConfigCipher) Encrypt(plain string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plain), nil)
	return EncPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt 解开 Encrypt 产物；前缀/认证不合法均报错。
func (c *ConfigCipher) Decrypt(enc string) (string, error) {
	if !IsEncrypted(enc) {
		return "", fmt.Errorf("config: 密文前缀非法（期望 %s）", EncPrefix)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(enc, EncPrefix))
	if err != nil {
		return "", fmt.Errorf("config: 密文 base64 解码: %w", err)
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("config: 密文过短")
	}
	pt, err := c.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("config: 解密失败: %w", err)
	}
	return string(pt), nil
}

// IsEncrypted 判定库内值是否为密文（EncPrefix 前缀口径）。
func IsEncrypted(stored string) bool {
	return strings.HasPrefix(stored, EncPrefix)
}

// MaskSecret 敏感值脱敏（管理端公开口径）：仅保留首尾各 2 字符，中间以 **** 占位；
// 不足 5 字符全掩为 ****；空串返回空串。
func MaskSecret(plain string) string {
	if plain == "" {
		return ""
	}
	r := []rune(plain)
	if len(r) <= 4 {
		return "****"
	}
	return string(r[:2]) + "****" + string(r[len(r)-2:])
}
