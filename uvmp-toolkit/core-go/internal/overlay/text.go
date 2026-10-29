// overlay：文本绘制（x/image sfnt/opentype，无 cgo；系统字体发现 + 尺寸缓存）。
// 用于扫描框标签与结果窗文案；中文依赖系统 CJK 字体，找不到时退回内置位图字体（仅 ASCII）。
package overlay

import (
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

var (
	fontOnce  sync.Once
	baseFont  *opentype.Font
	fontErr   error
	faceMu    sync.Mutex
	faceCache = map[int]font.Face{}
)

// fontCandidates 系统字体候选（按优先级）。
var fontCandidates = map[string][]string{
	"windows": {"msyh.ttc", "msyhbd.ttc", "simhei.ttf", "simsun.ttc", "Deng.ttf", "msjh.ttc"},
	"linux":   {"NotoSansCJK-Regular.ttc", "NotoSansCJKsc-Regular.otf", "wqy-microhei.ttc", "wqy-zenhei.ttc", "SourceHanSansSC-Regular.otf", "DroidSansFallbackFull.ttf", "simhei.ttf", "simsun.ttc"},
}

// findFontFile 查找系统字体文件（候选名按优先级精确命中；兜底递归找文件名含 CJK 线索者）。
func findFontFile() (string, error) {
	var dirs []string
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("WINDIR"); d != "" {
			dirs = append(dirs, filepath.Join(d, "Fonts"))
		}
	case "linux", "darwin":
		dirs = append(dirs,
			"/usr/share/fonts", "/usr/share/fonts/truetype", "/usr/share/fonts/opentype",
			"/usr/local/share/fonts", filepath.Join(os.Getenv("HOME"), ".fonts"))
	default:
		return "", errors.New("平台无字体发现路径")
	}
	for _, n := range fontCandidates[runtime.GOOS] {
		for _, dir := range dirs {
			p := filepath.Join(dir, n)
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	}
	for _, dir := range dirs {
		found := ""
		_ = filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
			if err != nil || e == nil || e.IsDir() {
				return nil
			}
			base := strings.ToLower(filepath.Base(p))
			if !strings.HasSuffix(base, ".ttf") && !strings.HasSuffix(base, ".ttc") && !strings.HasSuffix(base, ".otf") {
				return nil
			}
			if strings.Contains(base, "cjk") || strings.Contains(base, "wqy") ||
				strings.Contains(base, "han") || strings.Contains(base, "fallback") {
				found = p
				return filepath.SkipAll
			}
			return nil
		})
		if found != "" {
			return found, nil
		}
	}
	return "", errors.New("未找到可用系统字体")
}

// parseFontFile 解析字体文件（ttc 取第 1 个字体面）。
func parseFontFile(path string) (*opentype.Font, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(strings.ToLower(path), ".ttc") {
		col, err := opentype.ParseCollection(data)
		if err != nil {
			return nil, err
		}
		return col.Font(0)
	}
	return opentype.Parse(data)
}

// loadBaseFont 加载基础字体（须含中文字形「中」）。
func loadBaseFont() (*opentype.Font, error) {
	fontOnce.Do(func() {
		path, err := findFontFile()
		if err != nil {
			fontErr = err
			return
		}
		f, err := parseFontFile(path)
		if err != nil {
			fontErr = err
			return
		}
		if idx, err := f.GlyphIndex(nil, '中'); err != nil || idx == 0 {
			fontErr = errors.New("字体不含中文字形：" + path)
			return
		}
		baseFont = f
	})
	return baseFont, fontErr
}

// SetFontFile 显式指定字体文件（覆盖系统发现；用于测试与定制部署）。
func SetFontFile(path string) error {
	f, err := parseFontFile(path)
	if err != nil {
		return err
	}
	faceMu.Lock()
	defer faceMu.Unlock()
	faceCache = map[int]font.Face{}
	baseFont = f
	fontErr = nil
	fontOnce = sync.Once{}
	fontOnce.Do(func() {})
	return nil
}

// Face 取指定像素字号的字体面（缓存；无可用字体退回 ASCII 位图字体）。
func Face(px int) font.Face {
	if px < 6 {
		px = 6
	}
	faceMu.Lock()
	defer faceMu.Unlock()
	if f, ok := faceCache[px]; ok {
		return f
	}
	if bf, err := loadBaseFont(); err == nil && bf != nil {
		f, err := opentype.NewFace(bf, &opentype.FaceOptions{
			Size:    float64(px),
			DPI:     72, // 1pt = 1px
			Hinting: font.HintingFull,
		})
		if err == nil {
			faceCache[px] = f
			return f
		}
	}
	faceCache[px] = basicfont.Face7x13
	return basicfont.Face7x13
}

// measure 测量文本宽度（像素）。
func measure(f font.Face, s string) int {
	return font.MeasureString(f, s).Ceil()
}

// TextWidth 指定字号的单行文本宽度（像素）。
func TextWidth(s string, px int) int { return measure(Face(px), s) }

// DrawText 在 (x, y) 左上角绘制单行文本（基线按字体 ascent 折算；c 为直通色）。
func DrawText(b *Buffer, x, y int, s string, px int, c color.NRGBA) {
	f := Face(px)
	m := f.Metrics()
	d := &font.Drawer{
		Dst:  b.Image(),
		Src:  image.NewUniform(c),
		Face: f,
		Dot:  fixed.P(x, y+m.Ascent.Ceil()),
	}
	d.DrawString(s)
}

// Ellipsize 单行文本按最大宽度截断并加省略号。
func Ellipsize(s string, px, maxW int) string {
	f := Face(px)
	if measure(f, s) <= maxW {
		return s
	}
	rs := []rune(s)
	for len(rs) > 0 {
		rs = rs[:len(rs)-1]
		cand := string(rs) + "…"
		if measure(f, cand) <= maxW {
			return cand
		}
	}
	return "…"
}

// WrapText 按最大宽度折行（CJK 任意字符可断，ASCII 连续串按词断；返回至少一行）。
func WrapText(s string, px, maxW int) []string {
	f := Face(px)
	tokens := tokenize(s)
	lines := []string{}
	cur := ""
	for _, tk := range tokens {
		if tk == "\n" {
			lines = append(lines, cur)
			cur = ""
			continue
		}
		cand := cur + tk
		if cur != "" && measure(f, cand) > maxW {
			lines = append(lines, strings.TrimRight(cur, " "))
			cur = strings.TrimLeft(tk, " ")
		} else {
			cur = cand
		}
		// 单 token 超宽：逐字符硬断
		for measure(f, cur) > maxW && runLen(cur) > 1 {
			runes := []rune(cur)
			part := ""
			cut := 0
			for i, r := range runes {
				if part != "" && measure(f, part+string(r)) > maxW {
					cut = i
					break
				}
				part += string(r)
				cut = i + 1
			}
			lines = append(lines, part)
			cur = string(runes[cut:])
		}
	}
	lines = append(lines, cur)
	return lines
}

// tokenize 把文本切成折行 token：ASCII 单词整体、CJK 与标点逐字、换行独立。
func tokenize(s string) []string {
	var out []string
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			out = append(out, word.String())
			word.Reset()
		}
	}
	for _, r := range s {
		if r == '\n' {
			flush()
			out = append(out, "\n")
			continue
		}
		if r < 128 && (r == ' ' || isWordRune(r)) {
			word.WriteRune(r)
			continue
		}
		flush()
		out = append(out, string(r))
	}
	flush()
	return out
}

func isWordRune(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
}

func runLen(s string) int { return len([]rune(s)) }
