package ocr

// DecodeCTC CTC 贪心解码（同 JS _recognize：逐步 argmax、跳过 blank@0 与连续重复、
// 词表越界索引跳过不计分；score 为发射字符峰值均值，无发射时为 0）。
// logits 为 [1,T,C] 展平（长 T*C），chars 为 BuildChars 构造的词表。
func DecodeCTC(logits []float32, t, c int, chars []string) (text string, score float64) {
	var scoreSum float64
	var scoreCnt int
	prev := 0
	for i := 0; i < t; i++ {
		off := i * c
		maxI := 0
		maxV := logits[off]
		for j := 1; j < c; j++ {
			v := logits[off+j]
			if v > maxV {
				maxV = v
				maxI = j
			}
		}
		if maxI != 0 && maxI != prev {
			if maxI < len(chars) {
				ch := chars[maxI]
				if ch != "" {
					text += ch
					scoreSum += float64(maxV)
					scoreCnt++
				}
			}
		}
		prev = maxI
	}
	if scoreCnt > 0 {
		score = scoreSum / float64(scoreCnt)
	}
	return text, score
}
