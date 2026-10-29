package xlsx

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/zlib"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// CodeSeq 清单提取出的一行（seq 从 1 起；date_hint 可空）。
type CodeSeq struct {
	Seq      int     `json:"seq"`
	Code     string  `json:"code"`
	DateHint *string `json:"date_hint"`
}

// localName 去掉 XML 命名空间，取本地名。
func localName(tag string) string {
	if i := strings.LastIndex(tag, "}"); i >= 0 {
		return tag[i+1:]
	}
	return tag
}

var cellRefRe = regexp.MustCompile(`^([A-Z]+)`)

// colIndex 单元格引用（如 B3）的列号，1 起。
func colIndex(cellRef string) int {
	m := cellRefRe.FindStringSubmatch(cellRef)
	if m == nil {
		return 0
	}
	n := 0
	for _, ch := range m[1] {
		n = n*26 + int(ch-'A'+1)
	}
	return n
}

// readMember 读 zip 成员，带 WPS 怪癖兜底：WPS 的 xlsx 偶见「声明 deflate 实则异常」的成员，
// zip 读取直接报 incorrect header check；手工按本地文件头取压缩流，
// 按裸 deflate/zlib 封装轮流试。（已踩坑：见《开发指南》排障速查）
func readMember(zr *zip.Reader, name string) ([]byte, error) {
	var f *zip.File
	for _, zf := range zr.File {
		if zf.Name == name {
			f = zf
			break
		}
	}
	if f == nil {
		return nil, fmt.Errorf("zip 成员缺失: %s", name)
	}
	rc, err := f.Open()
	if err == nil {
		data, rerr := io.ReadAll(rc)
		_ = rc.Close()
		if rerr == nil {
			return data, nil
		}
	}
	// ---- WPS 怪癖兜底：按本地文件头手工解压 ----
	return rawInflate(f)
}

// rawInflate 按 zip 本地文件头定位压缩流，裸 deflate / zlib 封装轮流解。
func rawInflate(f *zip.File) ([]byte, error) {
	sr, err := f.OpenRaw()
	if err != nil {
		return nil, fmt.Errorf("成员解压失败: %s", f.Name)
	}
	comp, err := io.ReadAll(sr)
	if err != nil {
		return nil, fmt.Errorf("成员解压失败: %s", f.Name)
	}
	// 裸 deflate
	if r := flate.NewReader(bytes.NewReader(comp)); r != nil {
		if data, err := io.ReadAll(r); err == nil {
			_ = r.Close()
			return data, nil
		}
		_ = r.Close()
	}
	// zlib 封装
	if r, err := zlib.NewReader(bytes.NewReader(comp)); err == nil {
		data, err := io.ReadAll(r)
		_ = r.Close()
		if err == nil {
			return data, nil
		}
	}
	return nil, fmt.Errorf("成员解压失败: %s", f.Name)
}

// ReadXlsxRows 读取第一个工作表，返回行列表（每行为字符串列表，按列对齐，空单元为 ""）。
// 文件无效（WPS .et/损坏/非 zip）时返回错误并附中文指引。
func ReadXlsxRows(path string) ([][]string, error) {
	guidance := "清单文件不是有效的 xlsx（可能是 WPS 专有 .et 格式或文件已损坏）。" +
		"请用 Excel/WPS「另存为 → .xlsx」，或直接在界面粘贴单号。"
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("%s（%s）", guidance, err)
	}
	defer zr.Close()
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}

	// 找第一个工作表：workbook.xml 的第一个 sheet r:id → rels 里的 Target
	sheetPart := "xl/worksheets/sheet1.xml"
	if wbData, err := readMember(&zr.Reader, "xl/workbook.xml"); err == nil {
		if rid := firstSheetRID(wbData); rid != "" {
			if relsData, err := readMember(&zr.Reader, "xl/_rels/workbook.xml.rels"); err == nil {
				if target := relTarget(relsData, rid); target != "" {
					target = strings.TrimLeft(target, "/")
					if !strings.HasPrefix(target, "xl/") {
						target = "xl/" + target
					}
					if names[target] {
						sheetPart = target
					}
				}
			}
		}
	}

	// sharedStrings
	shared := []string{}
	if names["xl/sharedStrings.xml"] {
		if sstData, err := readMember(&zr.Reader, "xl/sharedStrings.xml"); err == nil {
			shared = parseSharedStrings(sstData)
		}
	}

	sheetData, err := readMember(&zr.Reader, sheetPart)
	if err != nil {
		return nil, fmt.Errorf("%s（%s）", guidance, err)
	}
	return parseSheetRows(sheetData, shared), nil
}

// firstSheetRID workbook.xml 中第一个 sheet 元素的 r:id。
func firstSheetRID(wbData []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(wbData))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		start, ok := tok.(xml.StartElement)
		if !ok || localName(start.Name.Local) != "sheet" {
			continue
		}
		for _, a := range start.Attr {
			if localName(a.Name.Local) == "id" {
				return a.Value
			}
		}
		return ""
	}
}

// relTarget workbook rels 中指定 Id 的 Target。
func relTarget(relsData []byte, rid string) string {
	dec := xml.NewDecoder(bytes.NewReader(relsData))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		start, ok := tok.(xml.StartElement)
		if !ok || localName(start.Name.Local) != "Relationship" {
			continue
		}
		id, target := "", ""
		for _, a := range start.Attr {
			switch a.Name.Local {
			case "Id":
				id = a.Value
			case "Target":
				target = a.Value
			}
		}
		if id == rid {
			return target
		}
	}
}

// parseSharedStrings 解析 sharedStrings.xml 的全部 <si> 文本（拼接全部 <t>）。
func parseSharedStrings(data []byte) []string {
	out := []string{}
	dec := xml.NewDecoder(bytes.NewReader(data))
	var cur strings.Builder
	inSI := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch localName(t.Name.Local) {
			case "si":
				inSI = true
				cur.Reset()
			case "t":
				if inSI {
					var text string
					if dec.DecodeElement(&text, &t) == nil {
						cur.WriteString(text)
					}
				}
			}
		case xml.EndElement:
			if localName(t.Name.Local) == "si" {
				out = append(out, cur.String())
				inSI = false
			}
		}
	}
	return out
}

// parseSheetRows 解析 worksheet 的行/单元格（对齐 Python read_xlsx_rows）。
func parseSheetRows(data []byte, shared []string) [][]string {
	rows := [][]string{}
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		start, ok := tok.(xml.StartElement)
		if !ok || localName(start.Name.Local) != "row" {
			continue
		}
		cells := map[int]string{}
		maxCol := 0
	rowLoop:
		for {
			tok2, err2 := dec.Token()
			if err2 != nil {
				break
			}
			switch t2 := tok2.(type) {
			case xml.StartElement:
				if localName(t2.Name.Local) != "c" {
					continue
				}
				ref, ctype := "", ""
				for _, a := range t2.Attr {
					switch a.Name.Local {
					case "r":
						ref = a.Value
					case "t":
						ctype = a.Value
					}
				}
				col := colIndex(ref)
				if col == 0 {
					col = maxCol + 1
				}
				if col > maxCol {
					maxCol = col
				}
				cells[col] = readCellValue(dec, ctype, shared)
			case xml.EndElement:
				if localName(t2.Name.Local) == "row" {
					break rowLoop
				}
			}
		}
		line := make([]string, maxCol)
		for i := 1; i <= maxCol; i++ {
			line[i-1] = cells[i]
		}
		rows = append(rows, line)
	}
	return rows
}

// readCellValue 读单个单元格值（inlineStr / shared string / 数值文本）。
// 调用时 <c> 起始标签已消费；本函数消费到配对的 </c> 为止（含自闭合）。
func readCellValue(dec *xml.Decoder, ctype string, shared []string) string {
	var sb strings.Builder
	vText := ""
	gotV := false
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := localName(t.Name.Local)
			if name == "t" {
				var text string
				if dec.DecodeElement(&text, &t) == nil {
					sb.WriteString(text)
				}
				continue // DecodeElement 已消费配对 </t>
			}
			if name == "v" && !gotV {
				var text string
				if dec.DecodeElement(&text, &t) == nil {
					vText = text
					gotV = true
				}
				continue
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
	if ctype == "inlineStr" {
		return sb.String()
	}
	if !gotV {
		return ""
	}
	if ctype == "s" {
		if idx, err := strconv.Atoi(vText); err == nil && idx >= 0 && idx < len(shared) {
			return shared[idx]
		}
		return vText
	}
	return vText
}

// ---------------------------------------------------------------- 清单提取

// 表头候选（识别「派车单号」列）。
var (
	codeHeaders = []string{"派车单号", "runcode", "单号"}
	dateHeaders = []string{"预计用车时间", "plansendtime", "用车时间"}
)

// ExtractRunCodes 从 xlsx 提取派车单号清单。优先按表头「派车单号」定位列，否则取第一列；
// 同时带出「预计用车时间」作为查询日期提示。保持行序、去重（保首次出现）。
func ExtractRunCodes(path string) ([]CodeSeq, error) {
	rows, err := ReadXlsxRows(path)
	if err != nil {
		return nil, err
	}
	return ExtractCodesFromRows(rows), nil
}

func containsHeader(text string, headers []string) bool {
	for _, h := range headers {
		if strings.Contains(text, h) {
			return true
		}
	}
	return false
}

// ExtractCodesFromRows 从已读出的行提取单号（口径同 Python extract_codes_from_rows）。
func ExtractCodesFromRows(rows [][]string) []CodeSeq {
	codeCol, dateCol, start := 0, -1, 0
	for rIdx, row := range rows {
		if rIdx >= 5 {
			break
		}
		for cIdx, cell := range row {
			text := strings.ToLower(strings.TrimSpace(cell))
			if text == "" {
				continue
			}
			if containsHeader(text, codeHeaders) {
				codeCol = cIdx
				// 同一行再找日期提示列
				for c2, cell2 := range row {
					t2 := strings.ToLower(strings.TrimSpace(cell2))
					if containsHeader(t2, dateHeaders) {
						dateCol = c2
						break
					}
				}
				start = rIdx + 1
				break
			}
		}
		if start != 0 {
			break
		}
	}
	out := []CodeSeq{}
	seen := map[string]bool{}
	for _, row := range rows[start:] {
		code := ""
		if codeCol < len(row) {
			code = strings.TrimSpace(row[codeCol])
		}
		if code == "" || seen[code] {
			continue
		}
		// 无表头模式下第一行若像标题（含「单号」字样）则跳过
		if start == 0 && len(out) == 0 && containsHeader(strings.ToLower(code), codeHeaders) {
			continue
		}
		seen[code] = true
		var hint *string
		if dateCol >= 0 && dateCol < len(row) {
			if m := dateRe.FindString(row[dateCol]); m != "" {
				hint = &m
			}
		}
		out = append(out, CodeSeq{Seq: len(out) + 1, Code: code, DateHint: hint})
	}
	return out
}

var dateRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)

// ParseCodesText 解析粘贴的单号文本（每行一个；兼容逗号/空白分隔），保持顺序去重。
func ParseCodesText(text string) []CodeSeq {
	out := []CodeSeq{}
	seen := map[string]bool{}
	for _, token := range regexp.MustCompile(`[\s,，;；]+`).Split(text, -1) {
		code := strings.TrimSpace(token)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, CodeSeq{Seq: len(out) + 1, Code: code, DateHint: nil})
	}
	return out
}

// SafeName 文件名安全化（vehicle_export.safe_name 同口径：路径/非法字符 → _，截 80）。
func SafeName(s string) string {
	re := regexp.MustCompile(`[\\/:*?"<>|\s]+`)
	out := re.ReplaceAllString(fmt.Sprintf("%v", s), "_")
	r := []rune(out)
	if len(r) > 80 {
		out = string(r[:80])
	}
	return out
}

// SaveUpload 前端 base64 上传的 xlsx 落盘，返回路径（stem 用 SafeName）。
func SaveUpload(dir, filename string, data []byte) (string, error) {
	stem := SafeName(strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename)))
	if stem == "" {
		stem = "清单"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, stem+".xlsx")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return "", err
	}
	return p, nil
}
