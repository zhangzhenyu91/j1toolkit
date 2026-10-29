package ocr

// Box 轴对齐矩形（像素坐标，原图坐标系；x,y 为左上角）。
type Box struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Line 一行识别结果。
type Line struct {
	Text  string  `json:"text"`
	Score float64 `json:"score"`
	Box   Box     `json:"box"`
}

// Result 单帧 OCR 结果（text 为各行以 \n 连接）。
type Result struct {
	Text      string `json:"text"`
	Lines     []Line `json:"lines"`
	ElapsedMs int64  `json:"elapsedMs"`
}

// BoxID 扫描框别（id 低位奇偶承载，同 JS：0=A、1=B）。
type BoxID int

const (
	BoxA BoxID = 0
	BoxB BoxID = 1
)

// String 框别名（A/B），对齐 JS boxId 口径。
func (b BoxID) String() string {
	if b == BoxB {
		return "B"
	}
	return "A"
}
