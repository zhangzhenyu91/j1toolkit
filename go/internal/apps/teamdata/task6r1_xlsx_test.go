package teamdata

// Task 6 fix round 1：XLSX 预算不能只信 ZIP 声明的 UncompressedSize64，
// 必须在 excelize 解析前按实际解压内容限读（伪造声明也要拒绝）。

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"testing"
)

// forgedXLSX 构造一个声明解压大小为 4 字节、实际写入 payload 字节的 ZIP（伪造中央目录尺寸）。
func forgedXLSX(t *testing.T, payload int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write(bytes.Repeat([]byte("A"), payload)); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	b := buf.Bytes()
	// 中央目录头签名 0x02014b50，偏移 24 = UncompressedSize（4 字节）。
	patched := 0
	for i := 0; i+46 <= len(b); i++ {
		if binary.LittleEndian.Uint32(b[i:]) != 0x02014b50 {
			continue
		}
		binary.LittleEndian.PutUint32(b[i+24:], 4)
		patched++
	}
	if patched == 0 {
		t.Fatal("未找到中央目录头")
	}
	return b
}

// TestScanXLSXActualDecompressedBudget 伪造声明（4 字节）但实际 64 字节：按实际内容拒绝。
func TestScanXLSXActualDecompressedBudget(t *testing.T) {
	old := maxXLSXEntryBytes
	maxXLSXEntryBytes = 8
	defer func() { maxXLSXEntryBytes = old }()

	data := forgedXLSX(t, 64)
	if err := checkXLSXArchive(data); err == nil {
		t.Fatal("实际解压 64 字节 > 8 字节预算，应拒绝（不能只信声明）")
	}
}

// TestScanXLSXActualTotalBudget 实际解压总量超预算（声明被伪造为小值）也必须拒绝。
func TestScanXLSXActualTotalBudget(t *testing.T) {
	oldEntry, oldTotal := maxXLSXEntryBytes, maxXLSXUncompressed
	maxXLSXEntryBytes = 1024
	maxXLSXUncompressed = 16
	defer func() {
		maxXLSXEntryBytes, maxXLSXUncompressed = oldEntry, oldTotal
	}()
	if err := checkXLSXArchive(forgedXLSX(t, 64)); err == nil {
		t.Fatal("实际解压总量 64 > 16 字节预算，应拒绝")
	}
}
