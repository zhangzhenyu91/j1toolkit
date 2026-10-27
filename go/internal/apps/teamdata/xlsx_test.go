package teamdata

// 杆塔/运维线路 Excel 模板与导入测试（真实生成/解析 xlsx，跳过计数与全量替换事务走 fake DB）。

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// buildXLSX 生成测试用 xlsx。
func buildXLSX(t *testing.T, rows [][]string) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	for ri, row := range rows {
		for ci, v := range row {
			cell, err := excelize.CoordinatesToCellName(ci+1, ri+1)
			if err != nil {
				t.Fatalf("单元格名失败: %v", err)
			}
			if err := f.SetCellValue("Sheet1", cell, v); err != nil {
				t.Fatalf("写单元格失败: %v", err)
			}
		}
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatalf("写 xlsx 失败: %v", err)
	}
	return buf.Bytes()
}

// doUpload 发起 multipart 上传（字段名 file），返回状态与信封。
func doUpload(t *testing.T, srv *httptest.Server, path, filename string, content []byte, form map[string]string) (int, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("创建表单文件失败: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("写表单失败: %v", err)
	}
	for k, v := range form {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatalf("写表单字段失败: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("关闭表单失败: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, &body)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	var env map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("解码响应失败: %v", err)
	}
	return resp.StatusCode, env
}

// 杆塔模板：双文件名下载头 + 表头/示例行。
func TestTowerTemplate(t *testing.T) {
	_, srv := newTestEnv(t, admin())
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/teamdata/towers/template", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("模板应 200，got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "spreadsheetml") {
		t.Fatalf("Content-Type 应为 xlsx: %q", ct)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.Contains(cd, "tower-template.xlsx") || !strings.Contains(cd, "filename*=UTF-8''") {
		t.Fatalf("Content-Disposition 不符: %q", cd)
	}
	var raw bytes.Buffer
	if _, err := raw.ReadFrom(resp.Body); err != nil {
		t.Fatalf("读响应失败: %v", err)
	}
	rows, err := readSheetRows(raw.Bytes())
	if err != nil {
		t.Fatalf("模板应可解析: %v", err)
	}
	if len(rows) < 2 || rows[0][0] != "电压等级" || rows[1][0] != "110kV" {
		t.Fatalf("模板表头/示例行不符: %v", rows)
	}
}

// 杆塔导入：跳过计数 + 全量替换（DELETE+INSERT，sort 连续）+ 钩子触发。
func TestTowerImportCountsAndReplaces(t *testing.T) {
	hookCalls := 0
	sqlDB, fake := openFake(t)
	defer sqlDB.Close()
	mod := New(sqlDB, nil)
	mod.Permit = fakePermit{allow: map[string]bool{"team-data": true}}
	mod.OnTowersChanged = func(teamID int64) {
		hookCalls++
		if teamID != 2 {
			t.Fatalf("钩子班组应 2，got %d", teamID)
		}
	}
	fake.on("FROM sys_team WHERE id = ?", []string{"id", "name"},
		[][]driverRow{{int64(2), "检修二班"}})
	srv := httptest.NewServer(newRouterWith(mod))
	defer srv.Close()

	xlsx := buildXLSX(t, [][]string{
		{"电压等级", "线路名称", "杆塔号", "经度", "纬度"},      // 表头行（跳过不计数）
		{"110kV", "示例线路", "N1", "111.5", "37.5"}, // 有效
		{"110kV", "示例线路", "N2", "111.6", ""},     // 缺纬度 → skipped
		{"", "", "", "", ""}, // 空行（跳过不计数）
	})
	status, body := doUpload(t, srv, "/api/v1/teamdata/towers/import", "coords.xlsx", xlsx, nil)
	if status != http.StatusOK {
		t.Fatalf("导入应 200，got %d %v", status, body)
	}
	data := body["data"].(map[string]any)
	if data["imported"] != float64(1) || data["skipped"] != float64(1) {
		t.Fatalf("imported/skipped 应 1/1，got %v", data)
	}
	if msg, _ := body["message"].(string); !strings.Contains(msg, "已导入 1 条坐标") {
		t.Fatalf("提示不符: %q", msg)
	}
	if fake.execCount("DELETE FROM worklog_tower") != 1 {
		t.Fatalf("应全量替换（先 DELETE）")
	}
	ex, ok := fake.lastExec("INSERT INTO worklog_tower")
	if !ok {
		t.Fatalf("应有 INSERT")
	}
	// team_id, voltage, line, tower_no, lng, lat, sort
	if len(ex.args) != 7 || ex.args[2] != "示例线路" || ex.args[6] != int64(1) {
		t.Fatalf("INSERT 参数不符: %v", ex.args)
	}
	if hookCalls != 1 {
		t.Fatalf("OnTowersChanged 应触发 1 次，got %d", hookCalls)
	}
}

// 杆塔导入校验：非 xlsx / 无有效行 / 文件缺失。
func TestTowerImportValidation(t *testing.T) {
	_, srv := newTestEnv(t, admin())

	status, body := doUpload(t, srv, "/api/v1/teamdata/towers/import", "bad.csv", []byte("x"), nil)
	if status != http.StatusBadRequest || codeOf(body) != codeFile {
		t.Fatalf("非 xlsx 应 40022，got %d %v", status, body)
	}

	xlsx := buildXLSX(t, [][]string{
		{"电压等级", "线路名称", "杆塔号", "经度", "纬度"},
		{"110kV", "示例线路", "N1", "abc", "37.5"}, // 经度非数字
	})
	status, body = doUpload(t, srv, "/api/v1/teamdata/towers/import", "coords.xlsx", xlsx, nil)
	if status != http.StatusBadRequest || codeOf(body) != codeFile {
		t.Fatalf("无有效行应 40022，got %d %v", status, body)
	}
	if msg, _ := body["message"].(string); !strings.Contains(msg, "未识别到有效坐标行") {
		t.Fatalf("提示不符: %q", msg)
	}
}

// 运维线路导入：文件内重复线路名计跳过；全量替换 team_line。
func TestOplineImportSkipsDuplicates(t *testing.T) {
	fake, srv := newTestEnv(t, admin())
	xlsx := buildXLSX(t, [][]string{
		{"电压等级", "线路名称"},
		{"110", "Ⅰ线"}, // 有效
		{"220", "Ⅰ线"}, // 文件内重名 → skipped
		{"", "Ⅱ线"},    // 缺电压 → skipped
		{"110", "Ⅱ线"}, // 有效
	})
	status, body := doUpload(t, srv, "/api/v1/teamdata/oplines/import", "lines.xlsx", xlsx, map[string]string{"team_id": "2"})
	if status != http.StatusOK {
		t.Fatalf("导入应 200，got %d %v", status, body)
	}
	data := body["data"].(map[string]any)
	if data["imported"] != float64(2) || data["skipped"] != float64(2) {
		t.Fatalf("imported/skipped 应 2/2，got %v", data)
	}
	if fake.execCount("DELETE FROM team_line") != 1 {
		t.Fatalf("应全量替换 team_line")
	}
	if fake.execCount("INSERT INTO team_line") != 2 {
		t.Fatalf("应插入 2 行，execs=%v", fake.allExecs())
	}
	if msg, _ := body["message"].(string); !strings.Contains(msg, "已导入 2 条线路") {
		t.Fatalf("提示不符: %q", msg)
	}
}

// 运维线路模板：仅表头。
func TestOplineTemplate(t *testing.T) {
	_, srv := newTestEnv(t, admin())
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/teamdata/oplines/template", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	var raw bytes.Buffer
	if _, err := raw.ReadFrom(resp.Body); err != nil {
		t.Fatalf("读响应失败: %v", err)
	}
	rows, err := readSheetRows(raw.Bytes())
	if err != nil {
		t.Fatalf("模板应可解析: %v", err)
	}
	if len(rows) < 1 || rows[0][0] != "电压等级" || rows[0][1] != "线路名称" {
		t.Fatalf("模板表头不符: %v", rows)
	}
}
