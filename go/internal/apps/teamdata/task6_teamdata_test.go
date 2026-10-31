package teamdata

// Task 6 边界回归：XLSX 在 excelize 解析前检查 ZIP 结构（条目/解压总量/深度），并限制行数。

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/xuri/excelize/v2"
)

// TestCheckXLSXArchiveBudget 超大条目数的 ZIP 在解析前拒绝；正常模板通过。
func TestCheckXLSXArchiveBudget(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i <= maxXLSXEntries; i++ {
		w, err := zw.Create(fmt.Sprintf("xl/worksheets/s%d.xml", i))
		if err != nil {
			t.Fatalf("zip: %v", err)
		}
		_, _ = w.Write([]byte("<x/>"))
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	if err := checkXLSXArchive(buf.Bytes()); err == nil {
		t.Fatal("条目数超限应拒绝")
	}

	ok, err := buildTemplate([][]string{{"电压等级", "线路名称"}}, "运维线路", []float64{12, 20})
	if err != nil {
		t.Fatalf("模板生成: %v", err)
	}
	if err := checkXLSXArchive(ok); err != nil {
		t.Fatalf("正常 xlsx 不应拒绝: %v", err)
	}
}

// TestReadSheetRowsRowBudget 行数超预算显式报错。
func TestReadSheetRowsRowBudget(t *testing.T) {
	old := maxImportRows
	maxImportRows = 5
	defer func() { maxImportRows = old }()

	f := excelize.NewFile()
	defer f.Close()
	for i := 0; i < 7; i++ {
		_ = f.SetCellValue("Sheet1", fmt.Sprintf("A%d", i+1), "x")
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatalf("xlsx: %v", err)
	}
	if _, err := readSheetRows(buf.Bytes()); !errors.Is(err, errTooManyRows) {
		t.Fatalf("行数超限应 errTooManyRows，实际 %v", err)
	}
}
