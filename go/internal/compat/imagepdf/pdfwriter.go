package imagepdf

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"strconv"
)

// pdfwriter.go —— 最小 PDF 生成器（水印照片排版用）。
// 只覆盖所需能力：A4 任意尺寸页、JPEG（DCTDecode 直嵌）/PNG（转 RGB FlateDecode）图像 XObject、
// cm 矩阵摆放/旋转。产物为标准 xref 表 PDF，可被 pdfcpu 校验/合并（见 mergepdf.go）。

// pdfImage 待嵌入图像（JPEG 直嵌或 RGB 展开）。
type pdfImage struct {
	width, height int
	jpegData      []byte // 非空 → DCTDecode
	rgbData       []byte // 非空 → FlateDecode RGB（len = w*h*3）
}

// pdfPlacement 单张图在页面上的摆放矩阵（PDF cm：x'=a·u+c·v+e, y'=b·u+d·v+f）。
type pdfPlacement struct {
	imgIdx  int
	matrix6 [6]float64
}

// pdfPage 页面。
type pdfPage struct {
	width, height float64
	placements    []pdfPlacement
}

// pdfDoc 文档组装器。
type pdfDoc struct {
	images []*pdfImage
	pages  []*pdfPage
}

// addImage 登记图像，返回索引。
func (d *pdfDoc) addImage(img *pdfImage) int {
	d.images = append(d.images, img)
	return len(d.images) - 1
}

// addPage 追加空白页。
func (d *pdfDoc) addPage(w, h float64) *pdfPage {
	p := &pdfPage{width: w, height: h}
	d.pages = append(d.pages, p)
	return p
}

// flate zlib 压缩（PDF FlateDecode 口径）。
func flate(data []byte) []byte {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	_, _ = zw.Write(data)
	_ = zw.Close()
	return buf.Bytes()
}

// bytes 组装完整 PDF。
func (d *pdfDoc) bytes() ([]byte, error) {
	if len(d.pages) == 0 {
		return nil, errors.New("imagepdf: PDF 无页面")
	}
	// 对象编号：1=Catalog 2=Pages，之后每图一对象、每页 content+page 两对象
	type obj struct {
		id   int
		body []byte
	}
	var objs []obj
	next := 3
	imgObjID := make([]int, len(d.images))
	for i, img := range d.images {
		imgObjID[i] = next
		next++
		var body bytes.Buffer
		if len(img.jpegData) > 0 {
			fmt.Fprintf(&body, "<< /Type /XObject /Subtype /Image /Width %d /Height %d "+
				"/ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>\nstream\n",
				img.width, img.height, len(img.jpegData))
			body.Write(img.jpegData)
			body.WriteString("\nendstream")
		} else {
			comp := flate(img.rgbData)
			fmt.Fprintf(&body, "<< /Type /XObject /Subtype /Image /Width %d /Height %d "+
				"/ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n",
				img.width, img.height, len(comp))
			body.Write(comp)
			body.WriteString("\nendstream")
		}
		objs = append(objs, obj{id: imgObjID[i], body: body.Bytes()})
	}

	pageObjID := make([]int, len(d.pages))
	kids := make([]int, len(d.pages))
	for i, p := range d.pages {
		contentID := next
		next++
		pageID := next
		next++
		pageObjID[i] = pageID
		kids[i] = pageID

		var content bytes.Buffer
		for _, pl := range p.placements {
			m := pl.matrix6
			fmt.Fprintf(&content, "q\n%s %s %s %s %s %s cm\n/Im%d Do\nQ\n",
				fnum(m[0]), fnum(m[1]), fnum(m[2]), fnum(m[3]), fnum(m[4]), fnum(m[5]), pl.imgIdx)
		}
		var cbody bytes.Buffer
		fmt.Fprintf(&cbody, "<< /Length %d >>\nstream\n", content.Len())
		cbody.Write(content.Bytes())
		cbody.WriteString("endstream")
		objs = append(objs, obj{id: contentID, body: cbody.Bytes()})

		var xobj bytes.Buffer
		xobj.WriteString("<< ")
		for idx := range d.images {
			fmt.Fprintf(&xobj, "/Im%d %d 0 R ", idx, imgObjID[idx])
		}
		xobj.WriteString(">>")
		var pbody bytes.Buffer
		fmt.Fprintf(&pbody, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %s %s] "+
			"/Resources << /XObject %s >> /Contents %d 0 R >>",
			fnum(p.width), fnum(p.height), xobj.String(), contentID)
		objs = append(objs, obj{id: pageID, body: pbody.Bytes()})
	}

	// 组装 + xref：1（Catalog）、2（Pages）在最前，其余按登记序
	catalog := []byte("<< /Type /Catalog /Pages 2 0 R >>")
	pagesBody := []byte(fmt.Sprintf("<< /Type /Pages /Count %d /Kids %s >>",
		len(kids), kidsArray(kids)))
	ordered := []obj{{id: 1, body: catalog}, {id: 2, body: pagesBody}}
	ordered = append(ordered, objs...)

	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n%\xE2\xE3\xCF\xD3\n")
	offsets := make(map[int]int)
	for _, o := range ordered {
		offsets[o.id] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n", o.id)
		out.Write(o.body)
		out.WriteString("\nendobj\n")
	}

	total := len(ordered) + 1
	xrefPos := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", total)
	for id := 1; id < total; id++ {
		fmt.Fprintf(&out, "%010d 00000 n \n", offsets[id])
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", total, xrefPos)
	return out.Bytes(), nil
}

// kidsArray 生成 Kids 数组字面量。
func kidsArray(ids []int) string {
	s := "["
	for i, id := range ids {
		if i > 0 {
			s += " "
		}
		s += strconv.Itoa(id) + " 0 R"
	}
	return s + "]"
}

// fnum PDF 数字输出（去尾零）。
func fnum(v float64) string {
	s := strconv.FormatFloat(v, 'f', 4, 64)
	// 去尾零
	for len(s) > 1 && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	if len(s) > 1 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	return s
}
