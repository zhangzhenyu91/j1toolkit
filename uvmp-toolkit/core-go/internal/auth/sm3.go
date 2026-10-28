package auth

// sm3.go — SM3 密码杂凑，逐字移植 pylib/gmssl/sm3.py（GB/T 32905）。
// 用于 SSO 防重放三元组签名、密码混淆、SM2 KDF 与 C3 计算。

import "encoding/binary"

// sm3IV SM3 初始向量（gmssl/sm3.py IV）。
var sm3IV = [8]uint32{
	1937774191, 1226093241, 388252375, 3666478592,
	2842636476, 372324522, 3817729613, 2969243214,
}

// sm3Tj 常量 T_j：前 16 轮 0x79CC4519，后 48 轮 0x7A879D8A。
func sm3Tj(j int) uint32 {
	if j < 16 {
		return 0x79CC4519
	}
	return 0x7A879D8A
}

func rotl(x uint32, n uint) uint32 {
	return (x << n) | (x >> (32 - n))
}

func sm3FF(x, y, z uint32, j int) uint32 {
	if j < 16 {
		return x ^ y ^ z
	}
	return (x & y) | (x & z) | (y & z)
}

func sm3GG(x, y, z uint32, j int) uint32 {
	if j < 16 {
		return x ^ y ^ z
	}
	return (x & y) | (^x & z)
}

func sm3P0(x uint32) uint32 {
	return x ^ rotl(x, 9) ^ rotl(x, 17)
}

func sm3P1(x uint32) uint32 {
	return x ^ rotl(x, 15) ^ rotl(x, 23)
}

// sm3CF 压缩函数（gmssl sm3_cf）。
func sm3CF(vi [8]uint32, block []byte) [8]uint32 {
	var w [68]uint32
	for i := 0; i < 16; i++ {
		w[i] = binary.BigEndian.Uint32(block[i*4:])
	}
	for j := 16; j < 68; j++ {
		w[j] = sm3P1(w[j-16]^w[j-9]^rotl(w[j-3], 15)) ^ rotl(w[j-13], 7) ^ w[j-6]
	}
	var w1 [64]uint32
	for j := 0; j < 64; j++ {
		w1[j] = w[j] ^ w[j+4]
	}
	a, b, c, d, e, f, g, h := vi[0], vi[1], vi[2], vi[3], vi[4], vi[5], vi[6], vi[7]
	for j := 0; j < 64; j++ {
		ss1 := rotl((rotl(a, 12)+e+rotl(sm3Tj(j), uint(j%32)))&0xFFFFFFFF, 7)
		ss2 := ss1 ^ rotl(a, 12)
		tt1 := (sm3FF(a, b, c, j) + d + ss2 + w1[j]) & 0xFFFFFFFF
		tt2 := (sm3GG(e, f, g, j) + h + ss1 + w[j]) & 0xFFFFFFFF
		d = c
		c = rotl(b, 9)
		b = a
		a = tt1
		h = g
		g = rotl(f, 19)
		f = e
		e = sm3P0(tt2)
	}
	var out [8]uint32
	for i, v := range [8]uint32{a, b, c, d, e, f, g, h} {
		out[i] = v ^ vi[i]
	}
	return out
}

// sm3Sum 对 msg 做 SM3 摘要，返回 32 字节（gmssl sm3_hash 的二进制形式）。
func sm3Sum(msg []byte) []byte {
	// 填充：0x80 + 0x00… + 64 位大端比特长（gmssl sm3_hash 同口径）
	padded := make([]byte, 0, len(msg)+72)
	padded = append(padded, msg...)
	padded = append(padded, 0x80)
	for len(padded)%64 != 56 {
		padded = append(padded, 0x00)
	}
	var lenBE [8]byte
	binary.BigEndian.PutUint64(lenBE[:], uint64(len(msg))*8)
	padded = append(padded, lenBE[:]...)

	v := sm3IV
	for off := 0; off < len(padded); off += 64 {
		v = sm3CF(v, padded[off:off+64])
	}
	out := make([]byte, 32)
	for i := 0; i < 8; i++ {
		binary.BigEndian.PutUint32(out[i*4:], v[i])
	}
	return out
}

// sm3Hex 对 msg 做 SM3 摘要，返回 64 位小写 hex（vehicle_export.sm3_hex）。
func sm3Hex(msg []byte) string {
	const hexdigits = "0123456789abcdef"
	sum := sm3Sum(msg)
	buf := make([]byte, 64)
	for i, b := range sum {
		buf[i*2] = hexdigits[b>>4]
		buf[i*2+1] = hexdigits[b&0x0F]
	}
	return string(buf)
}

// sm3Kdf SM2 密钥派生函数 KDF（gmssl sm3_kdf 的等价实现，z 为原始字节）：
// 按 32 字节分块 SM3(z || ct(4 字节大端))，ct 从 1 起，取前 klen 字节。
func sm3Kdf(z []byte, klen int) []byte {
	rcnt := (klen + 31) / 32
	out := make([]byte, 0, rcnt*32)
	for ct := uint32(1); ct <= uint32(rcnt); ct++ {
		msg := make([]byte, 0, len(z)+4)
		msg = append(msg, z...)
		var ctBE [4]byte
		binary.BigEndian.PutUint32(ctBE[:], ct)
		msg = append(msg, ctBE[:]...)
		out = append(out, sm3Sum(msg)...)
	}
	return out[:klen]
}
