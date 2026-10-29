package xlsx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteDispatchXlsxRoundtrip(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "2026-10-28-派车单.xlsx")
	items := []map[string]any{
		{
			"vehicleNumber": "晋A12345",
			"driverName":    "张三",
			"planSendTime":  "2026-10-28 08:30:00.0", // 时间串 → 截 19 位规整
			"runCode":       "PC20261028001",
			"runState":      "2", // → 已完成
			"remark":        `引号"与<标签>&`,
		},
		{
			"vehicleNumber": "晋B67890",
			// epoch 毫秒 → 本地时间串（2026-10-28 08:30:00 本地）
			"planSendTime": float64(time.Date(2026, 10, 28, 8, 30, 0, 0, time.Local).UnixMilli()),
			"runCode":      "PC20261028002",
			"runState":     "9", // 未知状态原样
			"planUseTime":  3.5,
		},
	}
	n, err := WriteDispatchXlsx(items, out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("记录数 = %d, want 2", n)
	}
	rows, err := ReadXlsxRows(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("行数 = %d, want 3（表头 + 2 数据行）", len(rows))
	}
	// 表头契约
	if rows[0][0] != "车牌号码" || rows[0][12] != "派车单号" || rows[0][13] != "派车单状态" || rows[0][19] != "创建时间" {
		t.Fatalf("表头错误: %v", rows[0])
	}
	// 第 1 行：runState 映射 + 时间规整 + 转义往返
	if rows[1][0] != "晋A12345" {
		t.Fatalf("车牌 = %q", rows[1][0])
	}
	if rows[1][5] != "2026-10-28 08:30:00" {
		t.Fatalf("时间串规整 = %q", rows[1][5])
	}
	if rows[1][13] != "已完成" {
		t.Fatalf("runState 映射 = %q", rows[1][13])
	}
	if rows[1][18] != `引号"与<标签>&` {
		t.Fatalf("转义往返 = %q", rows[1][18])
	}
	// 第 2 行：epoch 毫秒本地时区格式化 + 未知状态原样
	if rows[2][5] != "2026-10-28 08:30:00" {
		t.Fatalf("epoch 格式化 = %q", rows[2][5])
	}
	if rows[2][13] != "9" {
		t.Fatalf("未知 runState = %q", rows[2][13])
	}
	// 数值字段（planUseTime 不属于日期字段）
	if rows[2][7] != "3.5" {
		t.Fatalf("数值单元 = %q", rows[2][7])
	}
	// 列数 = 20
	if len(rows[1]) != 20 {
		t.Fatalf("列数 = %d, want 20", len(rows[1]))
	}
}

func TestDispatchColumnsContract(t *testing.T) {
	// 列契约锁定（壹匣服务端解析契约，禁止改动）
	want := [][2]string{
		{"vehicleNumber", "车牌号码"},
		{"licensePlateColor", "车牌颜色"},
		{"driverName", "驾驶员"},
		{"driverTel", "驾驶员电话"},
		{"userName", "用车人"},
		{"planSendTime", "预计用车时间"},
		{"planBackTime", "预计返回时间"},
		{"planUseTime", "预计用车时间(小时)"},
		{"frmAddr", "出发地"},
		{"toAddr", "目的地"},
		{"departName", "用车部门"},
		{"dispatchName", "派车人"},
		{"runCode", "派车单号"},
		{"runState", "派车单状态"},
		{"vehicleType", "车辆类型"},
		{"powerType", "能源类型"},
		{"userTel", "用车人电话"},
		{"useReason", "用车事由"},
		{"remark", "备注"},
		{"dispatchTime", "创建时间"},
	}
	if len(DispatchColumns) != len(want) {
		t.Fatalf("列数 = %d, want %d", len(DispatchColumns), len(want))
	}
	for i, w := range want {
		if DispatchColumns[i].Field != w[0] || DispatchColumns[i].Caption != w[1] {
			t.Fatalf("列 %d = %+v, want %v", i, DispatchColumns[i], w)
		}
	}
}

func TestWriteTableXlsx(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "报表.xlsx")
	n, err := WriteTableXlsx(out, "12点前结束行程",
		[]string{"序号", "派车单号", "行程结束时间"},
		[][]any{
			{1, "PC001", "2026-10-28 11:30:00"},
			{2, "PC002", nil},
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("记录数 = %d", n)
	}
	rows, err := ReadXlsxRows(out)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0][0] != "序号" || rows[1][1] != "PC001" || rows[2][2] != "" {
		t.Fatalf("读回 = %v", rows)
	}
	if rows[1][0] != "1" {
		t.Fatalf("数值 = %q", rows[1][0])
	}
}

func TestExtractRunCodes(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "清单.xlsx")
	// 带表头 + 日期提示列
	_, err := WriteTableXlsx(out, "清单",
		[]string{"序号", "派车单号", "预计用车时间"},
		[][]any{
			{1, "PC001", "2026-10-28 08:00:00"},
			{2, "PC002", "2026-10-27"},
			{3, "PC001", "2026-10-28"}, // 重复 → 去重
			{4, "", "2026-10-28"},      // 空 → 跳过
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	codes, err := ExtractRunCodes(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 2 {
		t.Fatalf("单号数 = %d, want 2", len(codes))
	}
	if codes[0].Code != "PC001" || codes[0].Seq != 1 {
		t.Fatalf("codes[0] = %+v", codes[0])
	}
	if codes[0].DateHint == nil || *codes[0].DateHint != "2026-10-28" {
		t.Fatalf("date_hint = %v", codes[0].DateHint)
	}
	if codes[1].DateHint == nil || *codes[1].DateHint != "2026-10-27" {
		t.Fatalf("date_hint[1] = %v", codes[1].DateHint)
	}
}

func TestExtractCodesFromRowsNoHeader(t *testing.T) {
	rows := [][]string{
		{"PC001"},
		{"PC002"},
		{"PC001"},
	}
	codes := ExtractCodesFromRows(rows)
	if len(codes) != 2 || codes[0].Code != "PC001" || codes[1].Code != "PC002" {
		t.Fatalf("codes = %+v", codes)
	}
}

func TestExtractCodesSkipsTitleRow(t *testing.T) {
	rows := [][]string{
		{"派车单号"},
		{"PC001"},
	}
	codes := ExtractCodesFromRows(rows)
	if len(codes) != 1 || codes[0].Code != "PC001" {
		t.Fatalf("codes = %+v", codes)
	}
}

func TestParseCodesText(t *testing.T) {
	codes := ParseCodesText("PC001\nPC002，PC003; PC001\n")
	if len(codes) != 3 {
		t.Fatalf("codes = %+v", codes)
	}
	if codes[0].Seq != 1 || codes[2].Code != "PC003" {
		t.Fatalf("codes = %+v", codes)
	}
}

func TestReadInvalidFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.xlsx")
	if err := os.WriteFile(p, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ReadXlsxRows(p)
	if err == nil || !strings.Contains(err.Error(), "另存为") {
		t.Fatalf("err = %v", err)
	}
}

func TestSafeName(t *testing.T) {
	if got := SafeName(`a/b\c:d*e?f"g<h>i|j k`); got != "a_b_c_d_e_f_g_h_i_j_k" {
		t.Fatalf("SafeName = %q", got)
	}
	long := strings.Repeat("车", 100)
	if got := SafeName(long); len([]rune(got)) != 80 {
		t.Fatalf("截断后长度 = %d", len([]rune(got)))
	}
}

func TestColIndex(t *testing.T) {
	cases := map[string]int{"A1": 1, "B3": 2, "Z9": 26, "AA1": 27, "": 0, "1A": 0}
	for ref, want := range cases {
		if got := colIndex(ref); got != want {
			t.Fatalf("colIndex(%q) = %d, want %d", ref, got, want)
		}
	}
}
