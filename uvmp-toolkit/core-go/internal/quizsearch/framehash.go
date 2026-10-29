// 题库搜题：轻量帧哈希（移植自 index.cjs frameHash）——抽样 FNV-1a，画面未变时跳过 OCR，弱机减负。
package quizsearch

// FrameHash 计算位图帧哈希（抽样 FNV-1a 32 位）。
// stride = max(16, len/4096)，与 .cjs 版逐字节同序，保证跨实现帧比较语义一致。
func FrameHash(buf []byte) uint32 {
	stride := len(buf) / 4096
	if stride < 16 {
		stride = 16
	}
	h := uint32(0x811c9dc5)
	for i := 0; i < len(buf); i += stride {
		h ^= uint32(buf[i])
		h *= 0x01000193
	}
	return h
}
