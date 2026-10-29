package ocr

import (
	"testing"
)

// TestDecodeCTCGolden JS 金标对拍：CTC 贪心解码（blank/连续重复/空格/词表越界跳过不计分）。
func TestDecodeCTCGolden(t *testing.T) {
	g := jsGoldenCTC
	text, score := DecodeCTC(g.Data, g.T, g.C, g.Chars)
	if text != g.Text {
		t.Fatalf("text = %q，want %q", text, g.Text)
	}
	diff := score - g.Score
	if diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("score = %.17g，want %.17g", score, g.Score)
	}
}

// TestDecodeCTCSynthetic 合成 logits：全 blank、单一字符、空词表回退。
func TestDecodeCTCSynthetic(t *testing.T) {
	chars := BuildChars([]string{"甲", "乙"})
	if len(chars) != 4 || chars[0] != "blank" || chars[3] != " " {
		t.Fatalf("词表构造不符：%q", chars)
	}
	// 全 blank → 空文本、0 分。
	blank := make([]float32, 3*len(chars))
	blank[0], blank[len(chars)], blank[2*len(chars)] = 1, 1, 1
	if text, score := DecodeCTC(blank, 3, len(chars), chars); text != "" || score != 0 {
		t.Fatalf("全 blank：text=%q score=%v", text, score)
	}
	// "甲 乙"：中间 blank 分隔 + 末尾空格符索引。
	logits := make([]float32, 4*len(chars))
	set := func(tStep, idx int, v float32) { logits[tStep*len(chars)+idx] = v }
	set(0, 1, 0.9) // 甲
	set(1, 0, 0.8) // blank
	set(2, 2, 0.7) // 乙
	set(3, 3, 0.6) // 空格符（词表末位）
	text, score := DecodeCTC(logits, 4, len(chars), chars)
	if text != "甲乙 " {
		t.Fatalf("text = %q，want %q", text, "甲乙 ")
	}
	want := (float64(float32(0.9)) + float64(float32(0.7)) + float64(float32(0.6))) / 3
	if diff := score - want; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("score = %v，want %v", score, want)
	}
}
