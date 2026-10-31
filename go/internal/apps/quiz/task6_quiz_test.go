package quiz

// Task 6 边界回归：题库导入 Excel 行数预算。

import (
	"errors"
	"testing"
)

// TestCheckImportRows 超过行数预算显式报错（表头行不计）。
func TestCheckImportRows(t *testing.T) {
	old := maxQuizImportRows
	maxQuizImportRows = 3
	defer func() { maxQuizImportRows = old }()

	rows := make([][]string, 5) // 表头 + 4 数据行
	for i := range rows {
		rows[i] = []string{"x"}
	}
	if err := checkImportRows(rows); !errors.Is(err, errQuizRowsExceeded) {
		t.Fatalf("4 数据行 > 3 应报错，实际 %v", err)
	}
	if err := checkImportRows(rows[:4]); err != nil {
		t.Fatalf("3 数据行应通过: %v", err)
	}
}
