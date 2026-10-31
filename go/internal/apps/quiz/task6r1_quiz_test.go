package quiz

// Task 6 fix round 1：题库 XLSX 导入同样按实际解压内容限读（伪造声明无效）。

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"testing"
)

// forgedQuizXLSX 构造声明 4 字节、实际 payload 字节的 ZIP。
func forgedQuizXLSX(t *testing.T, payload int) []byte {
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

// TestCheckQuizXLSXActualBudget 实际解压超单条目/总量预算必须拒绝。
func TestCheckQuizXLSXActualBudget(t *testing.T) {
	oldEntry, oldTotal := maxQuizXLSXEntryBytes, maxQuizXLSXUncompressed
	maxQuizXLSXEntryBytes = 8
	maxQuizXLSXUncompressed = 1 << 20
	defer func() {
		maxQuizXLSXEntryBytes, maxQuizXLSXUncompressed = oldEntry, oldTotal
	}()
	if err := checkQuizXLSXArchive(forgedQuizXLSX(t, 64)); err == nil {
		t.Fatal("实际 64 字节 > 8 字节预算，应拒绝（不能只信声明）")
	}

	maxQuizXLSXEntryBytes = 1024
	maxQuizXLSXUncompressed = 16
	if err := checkQuizXLSXArchive(forgedQuizXLSX(t, 64)); err == nil {
		t.Fatal("实际总量 64 > 16 字节预算，应拒绝")
	}
}
