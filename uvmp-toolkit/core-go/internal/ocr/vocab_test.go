package ocr

import (
	"bytes"
	"errors"
	"testing"
)

// TestParseKeysGolden JS 金标对拍：字典解析（\r 去除、空行过滤）。
func TestParseKeysGolden(t *testing.T) {
	for i, c := range jsGoldenParseKeys {
		got := ParseKeys([]byte(c.Input))
		if len(got) != len(c.Output) {
			t.Fatalf("parseKeys[%d] 数量不符：got %q want %q", i, got, c.Output)
		}
		for j := range got {
			if got[j] != c.Output[j] {
				t.Fatalf("parseKeys[%d][%d] 不符：got %q want %q", i, j, got[j], c.Output[j])
			}
		}
	}
}

// TestBuildChars 词表构造语义：blank@0、" "@末（RapidOCR insert_special_char）。
func TestBuildChars(t *testing.T) {
	chars := BuildChars([]string{"a", "b"})
	want := []string{"blank", "a", "b", " "}
	if len(chars) != len(want) {
		t.Fatalf("长度不符：%q", chars)
	}
	for i := range want {
		if chars[i] != want[i] {
			t.Fatalf("chars[%d] = %q，want %q", i, chars[i], want[i])
		}
	}
}

// TestExtractKeysGolden 合成 rec.onnx 二进制（character 元数据 + varint 长度）提取字典。
func TestExtractKeysGolden(t *testing.T) {
	payload := bytes.Repeat([]byte("字"), 999)
	payload = append(bytes.Repeat([]byte("x\n"), 1000), payload...) // ≥1000 行（过短门槛）
	payload = append(payload, []byte("\n尾")...)
	buf := buildRecONNXStub(payload)
	keys, err := ExtractKeys(buf)
	if err != nil {
		t.Fatalf("ExtractKeys 失败：%v", err)
	}
	if len(keys) != 1002 {
		t.Fatalf("字典行数 = %d，want 1002", len(keys))
	}
	if keys[0] != "x" || keys[len(keys)-1] != "尾" {
		t.Fatalf("首尾字符不符：%q ... %q", keys[0], keys[len(keys)-1])
	}
}

// TestExtractKeysErrors 缺元数据/长度越界/字典过短的错误口径。
func TestExtractKeysErrors(t *testing.T) {
	if _, err := ExtractKeys([]byte("nothing here")); !errors.Is(err, ErrNoCharacterMetadata) {
		t.Fatalf("缺元数据错误不符：%v", err)
	}
	short := buildRecONNXStub([]byte("a\nb\nc"))
	if _, err := ExtractKeys(short); err == nil || errors.Is(err, ErrNoCharacterMetadata) {
		t.Fatalf("字典过短应报长度错误：%v", err)
	}
	// varint 后无 payload（截断）。
	trunc := append([]byte{0x0a, 0x09}, []byte("character")...)
	trunc = append(trunc, 0x12, 0x64) // 声称 100 字节但缺失
	if _, err := ExtractKeys(trunc); !errors.Is(err, ErrNoCharacterMetadata) {
		t.Fatalf("截断错误不符：%v", err)
	}
}

// buildRecONNXStub 构造带 character 元数据的伪 rec.onnx 字节流（同 extract_keys.cjs
// 模式：0x0A 0x09 "character" 0x12 <varint> <payload>）。
func buildRecONNXStub(payload []byte) []byte {
	buf := []byte{0x0a, 0x09}
	buf = append(buf, []byte("character")...)
	buf = append(buf, 0x12)
	// varint 编码长度
	n := len(payload)
	for {
		b := byte(n & 0x7f)
		n >>= 7
		if n > 0 {
			b |= 0x80
		}
		buf = append(buf, b)
		if n == 0 {
			break
		}
	}
	return append(buf, payload...)
}
