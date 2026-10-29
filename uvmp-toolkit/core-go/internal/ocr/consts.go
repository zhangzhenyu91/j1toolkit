package ocr

import "math"

// 管线常量（与 ocr-pipeline.cjs 顶部常量逐一对齐；口径同 RapidOCR config.yaml）。
const (
	DetMaxSide     = 960     // 长边上限（mobile det 标准）
	DetMinTarget   = 736     // 短边不足时上采样至此（保留小字召回，同 RapidOCR 默认）
	DetAbsoluteMax = 2000    // 长边绝对上限
	DetThresh      = 0.3     // 二值化阈值（config.yaml thresh）
	DetBoxThresh   = 0.5     // 框平均得分阈值（config.yaml box_thresh）
	UnclipRatio    = 1.6     // 框外扩系数（config.yaml unclip_ratio）
	RecH           = 48      // 识别输入高（config.yaml rec_img_shape）
	RecMaxW        = 960     // 识别批量内单条最大宽（过长行截断，防显存/内存膨胀）
	TextScore      = 0.5     // 行置信度下限（config.yaml text_score）
	DetAlign       = 32      // det 输入边长对齐粒度
	MinBoxSide     = 6       // unclip 后框的最小边（更小视为噪点）
	MinCropSide    = 3       // 行裁剪最小边（更小跳过）
	MinCropTargetW = 4       // 行等比缩放后最小宽
	blankLabel     = "blank" // CTC 词表索引 0 占位符（永不输出）
	spaceLabel     = " "     // CTC 词表末尾追加的空格符（同 RapidOCR insert_special_char 语义）
	minExtractKeys = 1000    // ExtractKeys 字典下限（低于视为解析错误，同 extract_keys.cjs）
)

// jsRound 对齐 JS Math.round：向 +∞ 取整（floor(x+0.5)），与 Go math.Round
// （半数远离 0）在负半轴不同，逐值对拍必须同口径。
func jsRound(x float64) float64 {
	return math.Floor(x + 0.5)
}
