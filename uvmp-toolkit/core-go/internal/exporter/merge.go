package exporter

import (
	"context"
	"os"
	"path/filepath"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

// mergePDFs 按给定顺序合并 PDF（vehicle_export.merge_pdfs 口径）：
// 过滤不存在/空路径，不足 2 个不出合并文件（静默返回）；合并失败只告警，
// 不毁已导出的逐单文件。成功打「合并完成」日志。
func mergePDFs(ctx context.Context, files []string, outPath string, log func(string, ...any)) bool {
	var existing []string
	for _, f := range files {
		if f == "" {
			continue
		}
		if st, err := os.Stat(f); err == nil && st.Size() > 0 {
			existing = append(existing, f)
		}
	}
	if len(existing) < 2 {
		return false
	}
	if err := api.MergeCreateFile(ctx, existing, outPath, false, nil); err != nil {
		log("  [合并] 失败：%s（逐单文件不受影响）", err)
		return false
	}
	log("合并完成：%s（%d 个文件按序合并）", filepath.Base(outPath), len(existing))
	return true
}
