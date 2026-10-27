// Package assetpath 静态素材路径解析：同一套素材在容器布局（WORKDIR /app/server，
// server/assets → /app/server/assets，素材位于 assets/…）与仓根开发布局（素材位于
// server/assets/…）下相对路径不同。按「当前目录 assets/… 优先、server/assets/… 回退」
// 自当前目录向上逐级探测：命中当前目录层返回相对路径（与 env/运行配置给定值同口径，
// 如 WORKLOG_ASSETS_DIR=assets/worklog），命中上层返回可直接使用的拼接路径，
// 均未命中返回空串，由调用方决定缺省值与报错口径。
package assetpath

import (
	"os"
	"path/filepath"
)

// maxWalkUp 自当前目录向上探测的最大层数（覆盖 go/internal/apps/* 等测试与开发深度）。
const maxWalkUp = 8

// Resolve 探测 assets/<sub> 目录（sub 为 assets 下相对路径，如 "fonts"、"worklog"）。
func Resolve(sub string) string { return probe(sub, true) }

// ResolveFile 探测 assets/<sub> 文件（sub 为 assets 下相对路径，如 "safeday/xxx.docx"）。
func ResolveFile(sub string) string { return probe(sub, false) }

// probe 自当前目录向上逐级探测；命中 cwd 层返回相对路径，命中上层返回拼接路径。
func probe(sub string, wantDir bool) string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	dir := cwd
	for i := 0; i < maxWalkUp; i++ {
		for _, rel := range []string{filepath.Join("assets", sub), filepath.Join("server", "assets", sub)} {
			cand := filepath.Join(dir, rel)
			if st, err := os.Stat(cand); err == nil && st.IsDir() == wantDir {
				if dir == cwd {
					return rel
				}
				return cand
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}
