package ocr

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrNoCharacterMetadata rec.onnx 内未找到 character 元数据（同 extract_keys.cjs）。
var ErrNoCharacterMetadata = errors.New("未找到 character 元数据")

// extractKeysMarker 模式：0x0A 0x09 "character" 0x12 <varint 长度> <payload>
// （StringStringEntry 的 key/value 两个字段，同 extract_keys.cjs）。
var extractKeysMarker = []byte{0x0a, 0x09, 'c', 'h', 'a', 'r', 'a', 'c', 't', 'e', 'r', 0x12}

// LoadKeys 读取识别字典（ppocr_keys.txt，每行一字符，UTF-8）。
// 解析口径同 JS：按 \n 切分、去行尾 \r、过滤空行。
func LoadKeys(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseKeys(data), nil
}

// ParseKeys 从字典文件内容解析字符表（同 JS split('\n')/去 \r/filter 非空）。
func ParseKeys(data []byte) []string {
	lines := strings.Split(string(data), "\n")
	keys := make([]string, 0, len(lines))
	for _, s := range lines {
		s = strings.TrimSuffix(s, "\r")
		if len(s) > 0 {
			keys = append(keys, s)
		}
	}
	return keys
}

// BuildChars 构造 CTC 词表：索引 0 为 blank、末尾追加空格符
// （同 RapidOCR insert_special_char 语义：blank@0、" "@len；同 JS p.chars 构造）。
func BuildChars(keys []string) []string {
	chars := make([]string, 0, len(keys)+2)
	chars = append(chars, blankLabel)
	chars = append(chars, keys...)
	chars = append(chars, spaceLabel)
	return chars
}

// ExtractKeys 从 rec.onnx 模型二进制中提取内嵌字符字典（metadata_props
// key="character"），同 extract_keys.cjs：varint 长度后 payload 按 \n 切分；
// 少于 1000 字视为解析错误。
func ExtractKeys(recONNX []byte) ([]string, error) {
	at := bytes.Index(recONNX, extractKeysMarker)
	if at < 0 {
		return nil, ErrNoCharacterMetadata
	}
	p := at + len(extractKeysMarker)
	// varint 解码
	length := 0
	shift := 0
	for {
		if p >= len(recONNX) {
			return nil, ErrNoCharacterMetadata
		}
		b := recONNX[p]
		p++
		length |= int(b&0x7f) << shift
		if b&0x80 == 0 {
			break
		}
		shift += 7
		if shift > 28 {
			return nil, ErrNoCharacterMetadata
		}
	}
	if length < 0 || p+length > len(recONNX) {
		return nil, ErrNoCharacterMetadata
	}
	payload := string(recONNX[p : p+length])
	chars := strings.Split(payload, "\n")
	if len(chars) < minExtractKeys {
		return nil, fmt.Errorf("字典过短，疑似解析错误：%d", len(chars))
	}
	return chars, nil
}
