// 匹配算法测试：归一化口径、字符集交集、覆盖率评分与阈值/下限兜底（fake 文本，无 IO）。
package quizsearch

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"ＡＢ１２３，。 xy", "AB123XY"}, // 全角转半角、标点剔除、大写
		{"安全生产，人人有责", "安全生产人人有责"}, // CJK 保留、全角逗号剔除
		{"　Ａ　", "A"},                     // 全角空格剔除
		{"㐀䶿豈﫿", "㐀䶿豈﫿"},                 // 扩展 A / 兼容汉字保留
		{"hello-world_1", "HELLOWORLD1"}, // 连字符下划线剔除
		{"", ""},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBuildIndexSkipsEmpty(t *testing.T) {
	idx := BuildIndex([]Question{
		{Content: "安全生产方针"},
		{Content: "！！！"}, // 归一化为空 → 跳过
		{Content: "个人防护"},
	})
	if len(idx) != 2 {
		t.Fatalf("index len = %d, want 2", len(idx))
	}
}

func TestFindBestHit(t *testing.T) {
	qs := []Question{
		{Content: "我国安全生产工作的方针是安全第一预防为主综合治理", Answer: "A", Options: []string{"安全第一", "质量第一"}},
		{Content: "下列属于个人防护用品的有", Answer: "ABD"},
	}
	idx := BuildIndex(qs)
	hit := FindBest(idx, "我国安全生产工作的方针是？安全第一、预防为主、综合治理 A", DefaultThreshold)
	if hit == nil || hit.Question == nil {
		t.Fatalf("want hit, got %+v", hit)
	}
	if hit.Question.Content != qs[0].Content {
		t.Errorf("matched wrong question: %q", hit.Question.Content)
	}
	if hit.Score <= 0 || hit.Score > 1 {
		t.Errorf("score out of range: %v", hit.Score)
	}
	// 覆盖率口径：得分 = 命中字符数 / 题干去重字符数
	want := float64(hit.Common) / float64(len(idx[0].Chars))
	if hit.Score != want {
		t.Errorf("score = %v, want %v", hit.Score, want)
	}
}

func TestFindBestNearBelowThreshold(t *testing.T) {
	qs := []Question{{Content: "一二三四五六七八九十甲乙丙丁", Answer: "A"}}
	idx := BuildIndex(qs)
	// 命中 6 个字符：过 6 字硬下限但覆盖率 6/14 < 0.6 判「未匹配」并给 near
	hit := FindBest(idx, "一二三四五六", DefaultThreshold)
	if hit == nil || hit.Question != nil {
		t.Fatalf("want near-miss, got %+v", hit)
	}
	if hit.Near == nil || hit.Near.Content != qs[0].Content {
		t.Fatalf("want near = stem, got %+v", hit.Near)
	}
	if hit.Score <= 0 {
		t.Errorf("near-miss score should be > 0, got %v", hit.Score)
	}
}

func TestFindBestCommonFloor(t *testing.T) {
	qs := []Question{{Content: "一二三四五六七八九十", Answer: "A"}}
	idx := BuildIndex(qs)
	hit := FindBest(idx, "一二三四五", DefaultThreshold) // 5 字 < 6 硬下限
	if hit == nil || hit.Question != nil || hit.Near != nil {
		t.Fatalf("want hard-floor miss, got %+v", hit)
	}
	if hit.Common != 5 {
		t.Errorf("common = %d, want 5", hit.Common)
	}
}

func TestFindBestEmptyOCR(t *testing.T) {
	idx := BuildIndex([]Question{{Content: "安全生产方针政策要点解读"}})
	if hit := FindBest(idx, "，。！ ", DefaultThreshold); hit != nil {
		t.Fatalf("empty normalized OCR should return nil, got %+v", hit)
	}
	if hit := FindBest(idx, "安全生产方针政策要点解读", 0); hit == nil || hit.Question == nil {
		t.Fatalf("threshold=0 should fall back to default, got %+v", hit)
	}
}

func TestFindBestShortStemNotSqueezed(t *testing.T) {
	// 短题不会被长题挤掉：覆盖率口径下短题满分可胜出（题干去重字符 ≥6 过硬下限）
	qs := []Question{
		{Content: "安全第一预防为主", Answer: "A"},
		{Content: "安全第一预防为主综合治理以人为本持续改进", Answer: "B"},
	}
	idx := BuildIndex(qs)
	hit := FindBest(idx, "安全第一预防为主", DefaultThreshold)
	if hit == nil || hit.Question == nil || hit.Question.Answer != "A" {
		t.Fatalf("short stem should win, got %+v", hit)
	}
}
