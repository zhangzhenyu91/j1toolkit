package auth

// sm2.go — SM2 公钥加密（GM/T 0003.4），逐字移植 pylib/gmssl 3.2.2 sm2.CryptSM2.encrypt
// 的算法行为与 vehicle_export.py 的封装：
//
//   - sm2EncryptHex：C1C3C2 原始布局 "04"+hex(x||y||c3||c2)（getUserAuth 的 p 参数）；
//   - sm2EncryptASN1：SSO 密码加密，DER SEQUENCE{INTEGER x, INTEGER y,
//     OCTET STRING c3(32B), OCTET STRING c2}（vehicle_export.sm2_encrypt_asn1，
//     结构已由抓包验证）。
//
// C3 = SM3(x2||M||y2)，t = KDF(x2||y2)（gmssl sm3_kdf 的输入即 x2||y2 原始 64 字节，
// 与标准一致），C2 = M xor t；C1 = kG，随机 k 取 32 字节（gmssl random_hex(64) 等价）。

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// SM2 曲线参数（gmssl default_ecc_table）。
var (
	sm2P  = mustHexInt("FFFFFFFEFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF00000000FFFFFFFFFFFFFFFF")
	sm2A  = mustHexInt("FFFFFFFEFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF00000000FFFFFFFFFFFFFFFC")
	sm2B  = mustHexInt("28E9FA9E9D9F5E344D5A9E4BCF6509A7F39789F515AB8F92DDBCBD414D940E93")
	sm2N  = mustHexInt("FFFFFFFEFFFFFFFFFFFFFFFFFFFFFFFF7203DF6B21C6052B53BBF40939D54123")
	sm2Gx = mustHexInt("32C4AE2C1F1981195F9904466A39C9948FE30BBFF2660BE1715A4589334C74C7")
	sm2Gy = mustHexInt("BC3736A2F4F6779C59BDCEE36B692153D0A9877CC62A474002DF32E52139F0A0")
	// sm2A3 = a +3（gmssl ecc_a3，倍点公式用）
	sm2A3 = new(big.Int).Add(sm2A, big.NewInt(3))
)

func mustHexInt(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 16)
	if !ok {
		panic("auth: 非法曲线参数 " + s)
	}
	return v
}

// sm2DoublePoint 倍点（gmssl _double_point 公式逐字平移，Jacobian 加重射影坐标）。
func sm2DoublePoint(x1, y1, z1 *big.Int) (x3, y3, z3 *big.Int) {
	p := sm2P
	mod := func(v *big.Int) *big.Int { return v.Mod(v, p) }
	t6 := mod(new(big.Int).Mul(z1, z1))
	t2 := mod(new(big.Int).Mul(y1, y1))
	t3 := mod(new(big.Int).Add(x1, t6))
	t4 := mod(new(big.Int).Sub(x1, t6))
	t1 := mod(new(big.Int).Mul(t3, t4))
	t3 = mod(new(big.Int).Mul(y1, z1))
	t4 = mod(new(big.Int).Lsh(t2, 3))
	t5 := mod(new(big.Int).Mul(x1, t4))
	t1 = mod(new(big.Int).Mul(t1, big.NewInt(3)))
	t6 = mod(new(big.Int).Mul(t6, t6))
	t6 = mod(new(big.Int).Mul(sm2A3, t6))
	t1 = mod(new(big.Int).Add(t1, t6))
	z3 = mod(new(big.Int).Add(t3, t3))
	t3 = mod(new(big.Int).Mul(t1, t1))
	t2 = mod(new(big.Int).Mul(t2, t4))
	x3 = mod(new(big.Int).Sub(t3, t5))
	if t5.Bit(0) == 1 {
		// T4 = (T5 + (T5+p)>>1 - T3) mod p
		tmp := new(big.Int).Add(t5, p)
		tmp.Rsh(tmp, 1)
		t4 = mod(tmp.Add(tmp, t5).Sub(tmp, t3))
	} else {
		tmp := new(big.Int).Rsh(new(big.Int).Set(t5), 1)
		t4 = mod(tmp.Add(tmp, t5).Sub(tmp, t3))
	}
	t1 = mod(new(big.Int).Mul(t1, t4))
	y3 = mod(new(big.Int).Sub(t1, t2))
	return x3, y3, z3
}

// sm2AddPoint 点加（gmssl _add_point：P1 为 Jacobian，P2 为仿射 z=1）。
func sm2AddPoint(x1, y1, z1, x2, y2 *big.Int) (x3, y3, z3 *big.Int) {
	p := sm2P
	mod := func(v *big.Int) *big.Int { return v.Mod(v, p) }
	t1 := mod(new(big.Int).Mul(z1, z1))
	t2 := mod(new(big.Int).Mul(y2, z1))
	t3 := mod(new(big.Int).Mul(x2, t1))
	t1 = mod(new(big.Int).Mul(t1, t2))
	t2 = mod(new(big.Int).Sub(t3, x1))
	t3 = mod(new(big.Int).Add(t3, x1))
	t4 := mod(new(big.Int).Mul(t2, t2))
	t1 = mod(new(big.Int).Sub(t1, y1))
	z3 = mod(new(big.Int).Mul(z1, t2))
	t2 = mod(new(big.Int).Mul(t2, t4))
	t3 = mod(new(big.Int).Mul(t3, t4))
	t5 := mod(new(big.Int).Mul(t1, t1))
	t4 = mod(new(big.Int).Mul(x1, t4))
	x3 = mod(new(big.Int).Sub(t5, t3))
	t2 = mod(new(big.Int).Mul(y1, t2))
	t3 = mod(new(big.Int).Sub(t4, x3))
	t1 = mod(new(big.Int).Mul(t1, t3))
	y3 = mod(new(big.Int).Sub(t1, t2))
	return x3, y3, z3
}

// sm2ToAffine Jacobian 加重射影坐标转仿射（gmssl _convert_jacb_to_nor）。
func sm2ToAffine(x, y, z *big.Int) (ax, ay *big.Int, ok bool) {
	p := sm2P
	if z.Sign() == 0 {
		return nil, nil, false
	}
	zInv := new(big.Int).Exp(z, new(big.Int).Sub(p, big.NewInt(2)), p)
	zInv2 := new(big.Int).Mul(zInv, zInv)
	zInv2.Mod(zInv2, p)
	zInv3 := new(big.Int).Mul(zInv2, zInv)
	zInv3.Mod(zInv3, p)
	ax = new(big.Int).Mul(x, zInv2)
	ax.Mod(ax, p)
	ay = new(big.Int).Mul(y, zInv3)
	ay.Mod(ay, p)
	return ax, ay, true
}

// sm2ScalarMult 标量乘 k·P（gmssl _kg：从 bit255 起左到右倍加）。
func sm2ScalarMult(k, px, py *big.Int) (x, y *big.Int, ok bool) {
	one := big.NewInt(1)
	// Temp 以 Jacobian (x,y,1) 表示，flag 表示已初始化
	var tx, ty, tz *big.Int
	flag := false
	kk := new(big.Int).Set(k)
	for n := 0; n < 256; n++ {
		if flag {
			tx, ty, tz = sm2DoublePoint(tx, ty, tz)
		}
		if kk.Bit(255) != 0 {
			if flag {
				tx, ty, tz = sm2AddPoint(tx, ty, tz, px, py)
			} else {
				flag = true
				tx, ty, tz = new(big.Int).Set(px), new(big.Int).Set(py), new(big.Int).Set(one)
			}
		}
		kk.Lsh(kk, 1)
	}
	if !flag {
		return nil, nil, false
	}
	return sm2ToAffine(tx, ty, tz)
}

// sm2EncryptWithK 用指定随机数 k 做 SM2 加密，返回 C1C3C2 原始字节（不含 04 前缀），
// 布局 x(32B)||y(32B)||c3(32B)||c2（gmssl CryptSM2.encrypt, mode=1）。测试固定 k 用。
func sm2EncryptWithK(plaintext []byte, pubHex string, k *big.Int) ([]byte, error) {
	px, py, err := parseSM2Pub(pubHex)
	if err != nil {
		return nil, err
	}
	x1, y1, ok := sm2ScalarMult(k, sm2Gx, sm2Gy)
	if !ok {
		return nil, fmt.Errorf("SM2 加密失败：kG 无效")
	}
	x2, y2, ok := sm2ScalarMult(k, px, py)
	if !ok {
		return nil, fmt.Errorf("SM2 加密失败：kP 无效")
	}
	x2b := pad32(x2)
	y2b := pad32(y2)
	z := make([]byte, 0, 64)
	z = append(z, x2b...)
	z = append(z, y2b...)
	t := sm3Kdf(z, len(plaintext))
	allZero := true
	for _, b := range t {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return nil, fmt.Errorf("SM2 加密失败：KDF 全零")
	}
	c2 := make([]byte, len(plaintext))
	for i := range plaintext {
		c2[i] = plaintext[i] ^ t[i]
	}
	c3in := make([]byte, 0, 64+len(plaintext))
	c3in = append(c3in, x2b...)
	c3in = append(c3in, plaintext...)
	c3in = append(c3in, y2b...)
	c3 := sm3Sum(c3in)
	out := make([]byte, 0, 96+len(plaintext))
	out = append(out, pad32(x1)...)
	out = append(out, pad32(y1)...)
	out = append(out, c3...)
	out = append(out, c2...)
	return out, nil
}

// sm2EncryptRetry 随机 k 重试封装（vehicle_export 的 for _ in range(10)）。
func sm2EncryptRetry(plaintext []byte, pubHex string) ([]byte, error) {
	for i := 0; i < 10; i++ {
		k, err := sm2RandomScalar()
		if err != nil {
			return nil, err
		}
		raw, err := sm2EncryptWithK(plaintext, pubHex, k)
		if err != nil {
			continue
		}
		return raw, nil
	}
	return nil, fmt.Errorf("SM2 加密失败")
}

// Sm2EncryptHex SM2 C1C3C2 加密，输出 "04"+C1+C3+C2 的 hex
// （vehicle_export.sm2_encrypt_hex，等价前端 SG_sm2Encrypt）。
func Sm2EncryptHex(plaintext []byte, pubHex string) (string, error) {
	raw, err := sm2EncryptRetry(plaintext, pubHex)
	if err != nil {
		return "", err
	}
	return "04" + hex.EncodeToString(raw), nil
}

// Sm2EncryptASN1 SSO 登录密码加密：SM2 C1C3C2 的 ASN.1 DER 编码 hex
// （vehicle_export.sm2_encrypt_asn1，gmssl 3.2.2 的 asn1 参数实际未生效，故手工 DER）。
func Sm2EncryptASN1(plaintext []byte, pubHex string) (string, error) {
	raw, err := sm2EncryptRetry(plaintext, pubHex)
	if err != nil {
		return "", err
	}
	x, y, c3, c2 := raw[:32], raw[32:64], raw[64:96], raw[96:]
	seq := concatBytes(derInt(x), derInt(y), derOctet(c3), derOctet(c2))
	return hex.EncodeToString(concatBytes([]byte{0x30}, derLen(len(seq)), seq)), nil
}

// parseSM2Pub 解析 128/130 位 hex 公钥，返回仿射坐标。
func parseSM2Pub(pubHex string) (x, y *big.Int, err error) {
	pk := normalizePubkey(pubHex)
	b, err := hex.DecodeString(pk[2:])
	if err != nil {
		return nil, nil, fmt.Errorf("SM2 公钥格式不正确（应为 128 或 04 开头的 130 位十六进制）")
	}
	x = new(big.Int).SetBytes(b[:32])
	y = new(big.Int).SetBytes(b[32:])
	return x, y, nil
}

// normalizePubkey 公钥归一化（vehicle_export._normalize_pubkey）。
func normalizePubkey(pk string) string {
	pk = strings.TrimSpace(pk)
	if len(pk) == 128 {
		pk = "04" + pk
	}
	if len(pk) != 130 || pk[:2] != "04" {
		return ""
	}
	return pk
}

// sm2RandomScalar 生成 [1, n) 随机标量（gmssl random_hex(64) 等价 + 范围兜底）。
func sm2RandomScalar() (*big.Int, error) {
	for {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		k := new(big.Int).SetBytes(buf)
		if k.Sign() != 0 && k.Cmp(sm2N) < 0 {
			return k, nil
		}
	}
}

// pad32 大端定宽 32 字节（gmssl '%064x' 等价）。
func pad32(v *big.Int) []byte {
	out := make([]byte, 32)
	b := v.Bytes()
	copy(out[32-len(b):], b)
	return out
}

// ---------------------------------------------------------------- DER（vehicle_export 逐字）

func derLen(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var s []byte
	for v := n; v > 0; v >>= 8 {
		s = append([]byte{byte(v)}, s...)
	}
	return append([]byte{byte(0x80 | len(s))}, s...)
}

func derInt(b []byte) []byte {
	// b.lstrip(b"\x00") or b"\x00"
	i := 0
	for i < len(b) && b[i] == 0 {
		i++
	}
	b = b[i:]
	if len(b) == 0 {
		b = []byte{0x00}
	}
	if b[0]&0x80 != 0 {
		b = append([]byte{0x00}, b...)
	}
	return concatBytes([]byte{0x02}, derLen(len(b)), b)
}

func derOctet(b []byte) []byte {
	return concatBytes([]byte{0x04}, derLen(len(b)), b)
}

func concatBytes(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
