package config

import (
	"bufio"
	"os"
	"strings"
)

// ReadINI 读取 config.ini 为「段名 →（小写键 → 值）」字典
// （Python configparser.items 的口径：键统一小写、值裁剪空白）。
// 文件不存在或不可读返回空字典；只支持单行 key=value / key:value，
// '#' ';' 整行注释忽略（config.ini 实际形态如此）。
func ReadINI(path string) Dict {
	out := Dict{}
	if path == "" {
		return out
	}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	var section Dict
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			name := strings.TrimSpace(line[1 : len(line)-1])
			section = Dict{}
			out[name] = section
			continue
		}
		if section == nil {
			continue // 段前游离行忽略
		}
		k, v, ok := splitKV(line)
		if !ok {
			continue
		}
		section[strings.ToLower(k)] = v
	}
	return out
}

// splitKV 拆 key=value / key:value（第一个分隔符起切，值内允许再含分隔符）。
func splitKV(line string) (string, string, bool) {
	idx := strings.IndexAny(line, "=:")
	if idx <= 0 {
		return "", "", false
	}
	k := strings.TrimSpace(line[:idx])
	v := strings.TrimSpace(line[idx+1:])
	if k == "" {
		return "", "", false
	}
	return k, v, true
}
