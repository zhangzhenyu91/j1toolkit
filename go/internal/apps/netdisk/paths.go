package netdisk

import (
	"strings"
)

// 空间口径：my 个人空间 = {personalRoot}/{username}；public 公共区按班组隔离 = {publicRoot}/{班组名}。

// BaseDirs 空间根配置（env 读取后注入）。
type BaseDirs struct {
	PersonalRoot string // NETDISK_PERSONAL_ROOT
	PublicRoot   string // NETDISK_PUBLIC_ROOT
}

// SpaceRootOf 空间根解析：返回绝对路径；未分配班组的公共区返回空串，space 非法返回 ok=false。
func SpaceRootOf(d BaseDirs, username, teamName, space string) (string, bool) {
	switch space {
	case "my":
		return strings.TrimRight(d.PersonalRoot, "/") + "/" + username, true
	case "public":
		if teamName == "" {
			return "", true
		}
		return strings.TrimRight(d.PublicRoot, "/") + "/" + teamName, true
	}
	return "", false
}

// NormRel 归一相对路径：反斜杠转正、去多余斜杠、拒绝 .. 与单点、限长；"/" 表示空间根；非法返回 ok=false。
func NormRel(input string) (string, bool) {
	p := strings.TrimSpace(strings.ReplaceAll(strings.TrimSpace(input), "\\", "/"))
	if p == "" || p == "/" {
		return "/", true
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	segs := strings.Split(p, "/")
	clean := make([]string, 0, len(segs))
	for _, seg := range segs {
		if seg == "" {
			continue
		}
		if seg == ".." || seg == "." {
			return "", false
		}
		clean = append(clean, seg)
	}
	joined := "/" + strings.Join(clean, "/")
	if len(joined) > 400 {
		return "", false
	}
	return joined, true
}

// ValidName 文件名校验：不允许斜杠/反斜杠/控制符/纯点/超长；非法返回 ""。
func ValidName(name string) string {
	n := strings.TrimSpace(name)
	if n == "" || len(n) > 200 {
		return ""
	}
	if strings.ContainsAny(n, "/\\\r\n") || n == "." || n == ".." {
		return ""
	}
	for _, r := range n {
		if r < 0x20 {
			return ""
		}
	}
	return n
}

// JoinPath 空间根 + 相对路径拼接。
func JoinPath(root, rel string) string {
	if rel == "/" {
		return root
	}
	return root + rel
}

// BaseName 路径末段（文件名）。
func BaseName(p string) string {
	segs := strings.Split(p, "/")
	for i := len(segs) - 1; i >= 0; i-- {
		if segs[i] != "" {
			return segs[i]
		}
	}
	return ""
}

// DirName 路径父目录（绝对路径形式；根为 "/"）。
func DirName(p string) string {
	segs := strings.Split(p, "/")
	clean := make([]string, 0, len(segs))
	for _, seg := range segs {
		if seg != "" {
			clean = append(clean, seg)
		}
	}
	if len(clean) <= 1 {
		return "/"
	}
	return "/" + strings.Join(clean[:len(clean)-1], "/")
}

// SanitizeName 文件名净化（「保存到网盘」共享通道）：去路径分隔符与非法字符。
func SanitizeName(name string) string {
	repl := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", `"`, "_", "<", "_", ">", "_", "|", "_", "\r", "_", "\n", "_")
	n := strings.TrimSpace(repl.Replace(name))
	if len(n) > 200 {
		n = n[:200]
	}
	if n == "" || n == "." || n == ".." {
		return ""
	}
	return n
}

// SanitizeRelDir 目标空间内相对目录净化：每段走 SanitizeName；深度 ≤6、总长 ≤200；
// 合法返回相对路径（不含首尾斜杠；根为空串），非法返回 ok=false。
func SanitizeRelDir(dir string) (string, bool) {
	s := strings.TrimSpace(strings.Trim(dir, "/"))
	if s == "" {
		return "", true
	}
	segs := strings.Split(s, "/")
	if len(segs) > 6 {
		return "", false
	}
	clean := make([]string, 0, len(segs))
	for _, seg := range segs {
		c := SanitizeName(seg)
		if c == "" {
			return "", false
		}
		clean = append(clean, c)
	}
	joined := strings.Join(clean, "/")
	if len(joined) > 200 {
		return "", false
	}
	return joined, true
}
