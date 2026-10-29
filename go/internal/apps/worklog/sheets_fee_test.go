package worklog

// 费用汇总回归测试：范围内有出车卡片＋用车人时必须生成 docx，
// 不得误报「该日期范围没有出车记录」（persons 查询 SELECT 3 列须 Scan 取满，
// 少取目标 database/sql 报错、行被静默丢弃，persons 恒空 → 误判空范围的老回归）。
// fake DB + httptest + 最小 3 样板行模板，不访问外部服务。

import (
	"archive/zip"
	"bytes"
	"database/sql/driver"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// feeTplDocx 构造最小费用汇总模板 docx（word/document.xml 含 w:tbl 3 个样板行）。
func feeTplDocx(t *testing.T) []byte {
	t.Helper()
	cell := `<w:tc><w:tcPr><w:tcW w:w="2220"/></w:tcPr><w:p><w:r><w:t>格</w:t></w:r></w:p></w:tc>`
	row := `<w:tr>` + cell + cell + `</w:tr>`
	xml := `<w:document><w:body><w:tbl><w:tblPr><w:tblW w:w="13637"/></w:tblPr>` +
		row + row + row + `</w:tbl></w:body></w:document>`
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatalf("创建模板条目失败: %v", err)
	}
	if _, err := w.Write([]byte(xml)); err != nil {
		t.Fatalf("写入模板失败: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭模板失败: %v", err)
	}
	return buf.Bytes()
}

// withFeeTpl 挂最小费用汇总模板（WORKLOG_ASSETS_DIR 指向临时目录）。
func (e *testEnv) withFeeTpl(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fee-summary-template.docx"), feeTplDocx(t), 0o644); err != nil {
		t.Fatalf("落盘模板失败: %v", err)
	}
	e.module.Cfg = memConfig{"WORKLOG_ASSETS_DIR": dir}
	return e
}

// feeSheetRules 费用汇总生成四条查询预置行：1 张出车卡片 + 1 名用车人 + 当日费用。
func feeSheetRules(f *fakeDB) {
	f.on("SELECT e.id, DATE_FORMAT(e.log_date, '%Y-%m-%d')", []string{"id", "log_date"},
		[][]driver.Value{{int64(1), "2026-08-18"}})
	f.on("SELECT DISTINCT m.id, m.name, m.sort", []string{"id", "name", "sort"},
		[][]driver.Value{{int64(10), "赵登", int64(0)}})
	f.on("SELECT entry_id, member_id FROM worklog_entry_member WHERE entry_id IN", []string{"entry_id", "member_id"},
		[][]driver.Value{{int64(1), int64(10)}})
	f.on("fee_date BETWEEN ? AND ?", []string{"member_id", "fee_date", "food_fee", "transit_fee"},
		[][]driver.Value{{int64(10), "2026-08-18", 60.0, 0.0}})
}

// TestFeeSheetWithTrips 有出车记录应生成费用汇总 docx（回归：persons 行被 Scan 丢弃误报空范围）。
func TestFeeSheetWithTrips(t *testing.T) {
	env := newTestServer(t, identAdmin).withTeam().withFeeTpl(t)
	feeSheetRules(env.fake)

	rec := env.do(t, "GET", "/api/v1/worklog/fee-sheet?from=2026-08-18&to=2026-08-18&force=1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("有出车记录应 200 生成费用汇总，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "wordprocessingml") {
		t.Fatalf("应返回 docx，实际 Content-Type=%s", ct)
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("产物应为合法 docx: %v", err)
	}
	var docXML []byte
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("读取 document.xml 失败: %v", err)
			}
			docXML, _ = io.ReadAll(rc)
			rc.Close()
		}
	}
	if docXML == nil {
		t.Fatalf("docx 缺少 word/document.xml")
	}
	// 用车人列（persons 行须被正确 Scan）与费用单元格都要落入产物
	if !bytes.Contains(docXML, []byte("赵登")) {
		t.Fatalf("产物应含用车人列「赵登」，实际 document.xml=%s", docXML)
	}
	if !bytes.Contains(docXML, []byte("60×1=60")) {
		t.Fatalf("产物应含费用单元格「60×1=60」，实际 document.xml=%s", docXML)
	}
}

// TestFeeSheetEmptyRange 真正无出车记录时保留空范围提示。
func TestFeeSheetEmptyRange(t *testing.T) {
	env := newTestServer(t, identAdmin).withTeam().withFeeTpl(t)
	env.fake.on("SELECT e.id, DATE_FORMAT(e.log_date, '%Y-%m-%d')", []string{"id", "log_date"}, nil)

	rec := env.do(t, "GET", "/api/v1/worklog/fee-sheet?from=2026-08-18&to=2026-08-18&force=1", nil)
	envr := decodeEnv(t, rec)
	if envr.Code != codeNoPhotos || envr.Message != "该日期范围没有出车记录，无可生成的汇总" {
		t.Fatalf("空范围应 40402 保留空提示，实际 %d %q", envr.Code, envr.Message)
	}
}
