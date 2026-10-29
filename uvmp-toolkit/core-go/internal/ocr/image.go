package ocr

// Planes RGB 三分量平面数组（每平面 w*h 个 float32，与 JS Float32Array 同布局）。
type Planes [3][]float32

// BGRAToRGBPlanes BGRA（Electron nativeImage.getBitmap 密集布局）→ RGB 三分量平面数组。
// buf 为密集 w*h*4 字节：字节序 B,G,R,A。
func BGRAToRGBPlanes(buf []byte, w, h int) Planes {
	n := w * h
	var out Planes
	for c := 0; c < 3; c++ {
		out[c] = make([]float32, n)
	}
	for i, j := 0, 0; i < n; i, j = i+1, j+4 {
		out[2][i] = float32(buf[j+2]) // R
		out[1][i] = float32(buf[j+1]) // G
		out[0][i] = float32(buf[j])   // B
	}
	return out
}

// ResizePlanes 双线性缩放（平面数组，三通道同比例）。数值口径逐值对齐 JS 实现：
// 半像素中心 fy=(y+0.5)*ratio-0.5，且权重 wy 取自钳位前的 y0（JS 既有行为）。
func ResizePlanes(planes Planes, sw, sh, dw, dh int) Planes {
	xRatio := float64(sw) / float64(dw)
	yRatio := float64(sh) / float64(dh)
	var out Planes
	for c := 0; c < 3; c++ {
		src := planes[c]
		dst := make([]float32, dw*dh)
		for y := 0; y < dh; y++ {
			fy := (float64(y)+0.5)*yRatio - 0.5
			y0 := int(floorFloat(fy))
			wy := fy - float64(y0)
			if y0 < 0 {
				y0 = 0
			}
			y1 := minInt(y0+1, sh-1)
			for x := 0; x < dw; x++ {
				fx := (float64(x)+0.5)*xRatio - 0.5
				x0 := int(floorFloat(fx))
				wx := fx - float64(x0)
				if x0 < 0 {
					x0 = 0
				}
				x1 := minInt(x0+1, sw-1)
				p00 := float64(src[y0*sw+x0])
				p01 := float64(src[y0*sw+x1])
				p10 := float64(src[y1*sw+x0])
				p11 := float64(src[y1*sw+x1])
				dst[y*dw+x] = float32(p00*(1-wx)*(1-wy) + p01*wx*(1-wy) + p10*(1-wx)*wy + p11*wx*wy)
			}
		}
		out[c] = dst
	}
	return out
}

// PlanesToNCHW 平面 RGB → NCHW Float32，归一化 (v/255-0.5)/0.5 即 v/127.5-1。
func PlanesToNCHW(planes Planes, w, h int) []float32 {
	n := w * h
	data := make([]float32, 3*n)
	for c := 0; c < 3; c++ {
		src := planes[c]
		off := c * n
		for i := 0; i < n; i++ {
			data[off+i] = float32(float64(src[i])/127.5 - 1)
		}
	}
	return data
}

// floorFloat math.Floor 的轻包装（语义同 JS Math.floor）。
func floorFloat(x float64) float64 {
	i := int64(x)
	if float64(i) > x {
		return float64(i - 1)
	}
	return float64(i)
}

// minInt 两整数取小。
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// maxInt 两整数取大。
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
