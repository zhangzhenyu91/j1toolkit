package imagepdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// PDFPage 页面尺寸（pt）。
type PDFPage struct {
	Width  float64
	Height float64
}

// MergePDFs 按顺序合并多个 PDF buffer（safeday/merge.js mergePdfs 同口径，纯 Go）。
func MergePDFs(buffers [][]byte) ([]byte, error) {
	if len(buffers) == 0 {
		return nil, errors.New("imagepdf: 没有可合并的 PDF")
	}
	readers := make([]io.ReadSeeker, len(buffers))
	for i, b := range buffers {
		if len(b) == 0 {
			return nil, fmt.Errorf("imagepdf: 第 %d 个 PDF 为空", i+1)
		}
		readers[i] = bytes.NewReader(b)
	}
	var out bytes.Buffer
	conf := model.NewDefaultConfiguration()
	if err := api.MergeRaw(context.Background(), readers, &out, false, conf); err != nil {
		return nil, fmt.Errorf("imagepdf: PDF 合并: %w", err)
	}
	return out.Bytes(), nil
}

// PDFPageCount 统计页数（pdfcpu 读取校验）。
func PDFPageCount(b []byte) (int, error) {
	n, err := api.PageCount(context.Background(), bytes.NewReader(b), model.NewDefaultConfiguration())
	if err != nil {
		return 0, fmt.Errorf("imagepdf: 统计页数: %w", err)
	}
	return n, nil
}

// PDFPageDims 返回各页 MediaBox 尺寸（pt，页序）。
func PDFPageDims(b []byte) ([]PDFPage, error) {
	dims, err := api.PageDims(context.Background(), bytes.NewReader(b), model.NewDefaultConfiguration())
	if err != nil {
		return nil, fmt.Errorf("imagepdf: 读取页面尺寸: %w", err)
	}
	out := make([]PDFPage, len(dims))
	for i, d := range dims {
		out[i] = PDFPage{Width: d.Width, Height: d.Height}
	}
	return out, nil
}
