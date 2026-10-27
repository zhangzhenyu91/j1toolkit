package office

// 文档来源适配（可注入）：view-only 源（wls 任务单 / sdr 安全日 / wlr 备注附件 / thd 树障 / crs 交跨）
// 实现 DocSource.Fetch；网盘源（ndp，可编辑协同）实现 NetdiskDoc（回源 + 保存写回）。
// 格式白名单对齐 ONLYOFFICE document-formats v10：可编辑 = 原生 edit + lossy-edit + MS 旧格式 auto-convert + pdf；
// 金山/苹果等 auto-convert 与纯查看格式仅预览。

import (
	"context"
	"errors"
	"strings"
)

// ErrDocNotFound 文档不存在。
var ErrDocNotFound = errors.New("文档不存在")

// DocSource 只读文档来源。
type DocSource interface {
	// Fetch 按引用取文档字节与展示名。
	Fetch(ctx context.Context, ref string) (data []byte, name string, err error)
}

// NetdiskDoc 网盘来源（可编辑协同）：回源 + 保存写回。
type NetdiskDoc interface {
	// Fetch 网盘文件回源（space/user/path/team 见 NDP 载荷）。
	Fetch(ctx context.Context, p NDP, name string) (data []byte, contentType string, err error)
	// WriteBack 保存产物写回原路径。
	WriteBack(ctx context.Context, p NDP, data []byte) error
}

// docTypes 可编辑格式 → OO 文档类型。
var docTypes = map[string]string{
	"doc": "word", "docx": "word", "docm": "word", "dot": "word", "dotx": "word", "dotm": "word",
	"odt": "word", "ott": "word", "rtf": "word", "txt": "word", "md": "word", "html": "word",
	"epub": "word", "fb2": "word",
	"xls": "cell", "xlsx": "cell", "xlsm": "cell", "xlt": "cell", "xltx": "cell", "xltm": "cell",
	"xlsb": "cell", "ods": "cell", "ots": "cell", "csv": "cell", "tsv": "cell",
	"ppt": "slide", "pptx": "slide", "pptm": "slide", "pot": "slide", "potx": "slide", "potm": "slide",
	"pps": "slide", "ppsx": "slide", "ppsm": "slide", "odp": "slide", "otp": "slide",
	"pdf": "pdf",
}

// viewTypes 仅预览格式（view 模式强制只读）。
var viewTypes = map[string]string{
	"fodt": "word", "hml": "word", "htm": "word", "hwp": "word", "hwpx": "word",
	"mht": "word", "mhtml": "word", "pages": "word", "stw": "word", "sxw": "word",
	"wps": "word", "wpt": "word", "xml": "word",
	"et": "cell", "ett": "cell", "fods": "cell", "numbers": "cell", "sxc": "cell",
	"dps": "slide", "dpt": "slide", "fodp": "slide", "key": "slide", "odg": "slide", "sxi": "slide",
	"djvu": "pdf", "xps": "pdf", "oxps": "pdf", "ofd": "pdf",
}

// fileExt 扩展名（不含点、小写）。
func fileExt(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i < 0 || i == len(name)-1 {
		return ""
	}
	return strings.ToLower(name[i+1:])
}

// DocTypeOf 按文件名解析 OO 文档类型；edit=false 表示仅预览格式（或不支持）。
func DocTypeOf(name string) (docType string, editable bool) {
	ext := fileExt(name)
	if t, ok := docTypes[ext]; ok {
		return t, true
	}
	if t, ok := viewTypes[ext]; ok {
		return t, false
	}
	return "", false
}

// viewOnlySources 只读来源集合（强制 mode=view、不配 callbackUrl）。
func viewOnlySources(src string) bool { return src != "ndp" }
