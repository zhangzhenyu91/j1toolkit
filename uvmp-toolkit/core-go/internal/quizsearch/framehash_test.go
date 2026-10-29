// 帧哈希测试：与 .cjs frameHash 同口径的抽样 FNV-1a（手算参照值防同源算法自证）。
package quizsearch

import "testing"

func TestFrameHashReference(t *testing.T) {
	// len=20 → stride=max(16, 0)=16，采样下标 0、16
	buf := make([]byte, 20)
	buf[0] = 0x11
	buf[16] = 0x22
	// 手算：h0=0x811c9dc5；h=(h^b)*0x01000193（mod 2^32）
	h := uint32(0x811c9dc5)
	h = (h ^ 0x11) * 0x01000193
	h = (h ^ 0x22) * 0x01000193
	if got := FrameHash(buf); got != h {
		t.Errorf("FrameHash = %08x, want %08x", got, h)
	}
}

func TestFrameHashStableAndSensitive(t *testing.T) {
	buf := make([]byte, 20000) // stride = 20000/4096 = 4 → 提升到 16
	for i := range buf {
		buf[i] = byte(i)
	}
	a := FrameHash(buf)
	if a != FrameHash(append([]byte(nil), buf...)) {
		t.Error("same buffer should hash equal")
	}
	buf2 := append([]byte(nil), buf...)
	buf2[16] ^= 0xff // 采样点改动 → 哈希改变
	if FrameHash(buf2) == a {
		t.Error("sampled byte change should alter hash")
	}
	buf3 := append([]byte(nil), buf...)
	buf3[1] ^= 0xff // 非采样点改动 → 哈希不变（抽样口径）
	if FrameHash(buf3) != a {
		t.Error("unsampled byte change should not alter hash")
	}
}

func TestFrameHashEmpty(t *testing.T) {
	if FrameHash(nil) != 0x811c9dc5 {
		t.Error("empty buffer should return FNV offset basis")
	}
}
