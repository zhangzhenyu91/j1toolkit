package ocr

import "math"

// Component 连通域（像素集的外接矩形与像素数）。
type Component struct {
	MinX   int `json:"minX"`
	MaxX   int `json:"maxX"`
	MinY   int `json:"minY"`
	MaxY   int `json:"maxY"`
	Pixels int `json:"pixels"`
}

// Dilate 3x3 膨胀（合并一行内的字符碎片，config.yaml use_dilation）。
// 以 bitmap 原始值判定、写入 out，一次膨胀不连锁（同 JS）。
func Dilate(bitmap []uint8, w, h int) []uint8 {
	out := append([]uint8(nil), bitmap...)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if bitmap[y*w+x] == 0 {
				continue
			}
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					nx := x + dx
					ny := y + dy
					if nx >= 0 && nx < w && ny >= 0 && ny < h {
						out[ny*w+nx] = 1
					}
				}
			}
		}
	}
	return out
}

// ConnectedComponents 连通域标记（两遍法、4 连通、并查集按小根归并），
// 返回各域外接矩形，顺序为像素扫描中根节点首见顺序（同 JS Map 插入序）。
func ConnectedComponents(bitmap []uint8, w, h int) []Component {
	labels := make([]int32, w*h)
	parent := []int32{0}
	next := int32(1)
	var find func(a int32) int32
	find = func(a int32) int32 {
		for parent[a] != a {
			parent[a] = parent[parent[a]]
			a = parent[a]
		}
		return a
	}
	union := func(a, b int32) {
		ra := find(a)
		rb := find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if bitmap[i] == 0 {
				continue
			}
			var up, left int32
			if y > 0 {
				up = labels[i-w]
			}
			if x > 0 {
				left = labels[i-1]
			}
			switch {
			case up != 0 && left != 0:
				ru := find(up)
				rl := find(left)
				if ru < rl {
					labels[i] = ru
				} else {
					labels[i] = rl
				}
				union(up, left)
			case up != 0:
				labels[i] = find(up)
			case left != 0:
				labels[i] = find(left)
			default:
				labels[i] = next
				parent = append(parent, next)
				next++
			}
		}
	}
	// 第二遍：按最终根归并外接矩形，保持插入序。
	type entry struct {
		root int32
		comp *Component
	}
	var order []entry
	byRoot := map[int32]*Component{}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			l := labels[i]
			if l == 0 {
				continue
			}
			r := find(l)
			bx, ok := byRoot[r]
			if !ok {
				bx = &Component{MinX: x, MaxX: x, MinY: y, MaxY: y}
				byRoot[r] = bx
				order = append(order, entry{root: r, comp: bx})
			}
			if x < bx.MinX {
				bx.MinX = x
			}
			if x > bx.MaxX {
				bx.MaxX = x
			}
			if y < bx.MinY {
				bx.MinY = y
			}
			if y > bx.MaxY {
				bx.MaxY = y
			}
			bx.Pixels++
		}
	}
	comps := make([]Component, 0, len(order))
	for _, e := range order {
		comps = append(comps, *e.comp)
	}
	return comps
}

// UnclipRect 轴对齐 unclip：每边外扩 d = area*ratio/perimeter
// （矩形情形下与 Vatti unclip 等效），结果钳位进 [0,maxW)×[0,maxH)。
func UnclipRect(bx Component, maxW, maxH int) Box {
	w := bx.MaxX - bx.MinX + 1
	h := bx.MaxY - bx.MinY + 1
	d := float64(w) * float64(h) * UnclipRatio / (2 * float64(w+h))
	x0 := maxInt(0, int(jsRound(float64(bx.MinX)-d)))
	y0 := maxInt(0, int(jsRound(float64(bx.MinY)-d)))
	x1 := minInt(maxW, int(jsRound(float64(bx.MaxX)+d)))
	y1 := minInt(maxH, int(jsRound(float64(bx.MaxY)+d)))
	return Box{X: x0, Y: y0, W: x1 - x0 + 1, H: y1 - y0 + 1}
}

// DetPreprocessSize det 前处理尺寸：等比缩放（短边上采样至 DetMinTarget、
// 长边封顶 DetMaxSide/DetAbsoluteMax），32 对齐；usedRatio=dw/width（JS 同口径，
// 非 dh/height，坐标映射必须用同一 ratio）。
func DetPreprocessSize(width, height int) (dw, dh int, usedRatio float64) {
	minSide := minInt(width, height)
	maxSide := maxInt(width, height)
	ratio := 1.0
	if minSide < DetMinTarget {
		ratio = float64(DetMinTarget) / float64(minSide)
	}
	if float64(maxSide)*ratio > DetAbsoluteMax {
		ratio = float64(DetAbsoluteMax) / float64(maxSide)
	}
	if float64(maxSide)*ratio > DetMaxSide && ratio <= 1 {
		ratio = float64(DetMaxSide) / float64(maxSide)
	}
	dw = maxInt(DetAlign, int(jsRound(float64(width)*ratio/DetAlign))*DetAlign)
	dh = maxInt(DetAlign, int(jsRound(float64(height)*ratio/DetAlign))*DetAlign)
	usedRatio = float64(dw) / float64(width)
	return dw, dh, usedRatio
}

// DetPostprocess det 概率图后处理：阈值 → 膨胀 → 连通域 → unclip → 框平均得分
// （score_mode=fast 近似：外接矩形内位图像素均值）→ 映射回原图坐标 → 按 (y,x) 升序。
// prob 为 [1,1,dh,dw] 展平的概率图（长 dh*dw）。
func DetPostprocess(prob []float32, dw, dh int, usedRatio float64, srcW, srcH int) []Box {
	bitmap := make([]uint8, dw*dh)
	for i := 0; i < dw*dh; i++ {
		if prob[i] > DetThresh {
			bitmap[i] = 1
		}
	}
	dilated := Dilate(bitmap, dw, dh)
	comps := ConnectedComponents(dilated, dw, dh)
	var boxes []Box
	for _, c := range comps {
		rect := UnclipRect(c, dw, dh)
		if rect.W < MinBoxSide || rect.H < MinBoxSide {
			continue // 噪点
		}
		var sum float64
		var cnt int
		for y := rect.Y; y < rect.Y+rect.H; y++ {
			for x := rect.X; x < rect.X+rect.W; x++ {
				i := y*dw + x
				if bitmap[i] != 0 {
					sum += float64(prob[i])
					cnt++
				}
			}
		}
		if cnt == 0 || sum/float64(cnt) < DetBoxThresh {
			continue
		}
		// 映射回原图坐标（floor/ceil 口径同 JS）。
		x := maxInt(0, int(math.Floor(float64(rect.X)/usedRatio)))
		y := maxInt(0, int(math.Floor(float64(rect.Y)/usedRatio)))
		x1 := minInt(srcW, int(math.Ceil(float64(rect.X+rect.W)/usedRatio)))
		y1 := minInt(srcH, int(math.Ceil(float64(rect.Y+rect.H)/usedRatio)))
		boxes = append(boxes, Box{X: x, Y: y, W: x1 - x, H: y1 - y})
	}
	sortBoxes(boxes)
	return boxes
}

// sortBoxes 按 (y,x) 升序稳定排序（同 JS Array.prototype.sort 稳定序）。
func sortBoxes(boxes []Box) {
	for i := 1; i < len(boxes); i++ {
		for j := i; j > 0; j-- {
			a, b := boxes[j-1], boxes[j]
			if a.Y < b.Y || (a.Y == b.Y && a.X <= b.X) {
				break
			}
			boxes[j-1], boxes[j] = b, a
		}
	}
}
