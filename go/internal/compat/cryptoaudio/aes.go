// Package cryptoaudio 是 Go 全量迁移阶段 0 验证切片：国密/RSA 与 SILK 音频兼容能力。
// 对齐 Node 侧口径（server/src/sgccclockin/protocol.js、crane-answer fileStore.js）：
//   - SM2 加解密（sm-crypto mode 0：C1C2C3、C1 为无 04 前缀裸点，协议层外再补 04）；
//   - RSA PKCS#1 v1.5 分块（1024 位：117B 加密 / 128B 解密，jsonx 通道）；
//   - AES-128-ECB（PKCS#7 填充，jsonm 通道）；
//   - SILK v3 解码适配（微信语音 → PCM → WAV，silk-wasm 同口径）。
//
// 依赖：github.com/emmansun/gmsm（纯 Go 国密）、github.com/wdvxdr1123/go-silk（ccgo 转写纯 Go，
// 无需 CGO）；全部随机测试密钥本地生成，不接任何真实服务。
package cryptoaudio

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// aesBlockSize AES 块大小。
const aesBlockSize = aes.BlockSize

// pkcs7Pad PKCS#7 填充（Node crypto createCipheriv 默认填充口径）。
func pkcs7Pad(data []byte) []byte {
	pad := aesBlockSize - len(data)%aesBlockSize
	out := make([]byte, len(data)+pad)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

// pkcs7Unpad 去 PKCS#7 填充并校验。
func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data)%aesBlockSize != 0 {
		return nil, errors.New("cryptoaudio: 密文长度非法")
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > aesBlockSize || pad > len(data) {
		return nil, errors.New("cryptoaudio: 填充非法")
	}
	for _, b := range data[len(data)-pad:] {
		if int(b) != pad {
			return nil, errors.New("cryptoaudio: 填充非法")
		}
	}
	return data[:len(data)-pad], nil
}

// AESEncryptECB AES-128-ECB 加密（key 16 字节，PKCS#7 填充），返回原始密文。
func AESEncryptECB(plain, key []byte) ([]byte, error) {
	if len(key) != 16 {
		return nil, fmt.Errorf("cryptoaudio: AES-128 密钥须 16 字节，实际 %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padded := pkcs7Pad(plain)
	out := make([]byte, len(padded))
	for i := 0; i < len(padded); i += aesBlockSize {
		block.Encrypt(out[i:i+aesBlockSize], padded[i:i+aesBlockSize])
	}
	return out, nil
}

// AESDecryptECB AES-128-ECB 解密并去 PKCS#7 填充。
func AESDecryptECB(ct, key []byte) ([]byte, error) {
	if len(key) != 16 {
		return nil, fmt.Errorf("cryptoaudio: AES-128 密钥须 16 字节，实际 %d", len(key))
	}
	if len(ct) == 0 || len(ct)%aesBlockSize != 0 {
		return nil, errors.New("cryptoaudio: 密文长度非法")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(ct))
	for i := 0; i < len(ct); i += aesBlockSize {
		block.Decrypt(out[i:i+aesBlockSize], ct[i:i+aesBlockSize])
	}
	return pkcs7Unpad(out)
}

// AESEncryptECBB64 Node aesEncrypt 同口径：key16 为 16 字符 UTF-8 密钥，输出 base64 密文。
func AESEncryptECBB64(plainText, key16 string) (string, error) {
	ct, err := AESEncryptECB([]byte(plainText), []byte(key16))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ct), nil
}

// AESDecryptECBB64 Node aesDecrypt 同口径：base64 密文进、明文出。
func AESDecryptECBB64(b64, key16 string) (string, error) {
	ct, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("cryptoaudio: base64 解码: %w", err)
	}
	pt, err := AESDecryptECB(ct, []byte(key16))
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// GenAESKey 生成 16 位字母数字随机密钥（Node genAESKey 同字符集口径）。
func GenAESKey() (string, error) {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, 16)
	pick := make([]byte, 16)
	if _, err := rand.Read(pick); err != nil {
		return "", err
	}
	for i := range out {
		out[i] = chars[int(pick[i])%len(chars)]
	}
	return string(out), nil
}

// unusedCipherRef 保证 cipher 包引用稳定（ECB 手写分组，不依赖流模式）。
var _ cipher.Block
