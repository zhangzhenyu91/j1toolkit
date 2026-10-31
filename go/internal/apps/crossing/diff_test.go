package crossing

import (
	"strings"
	"testing"
)

// 内网表是完整快照：服务端有、内网未出现的记录必须计为缺行并触发表下发。
func TestDiffRecordsMissingRows(t *testing.T) {
	server := []map[string]any{
		{
			"id": int64(1), "line_name": "甲线", "section": "1#-2#", "crossing_type": "道路",
			"dist_small": "5", "synced_at": "2026-10-01 08:00:00", "updated_at": "2026-10-01 07:00:00",
		},
		{
			"id": int64(2), "line_name": "乙线", "section": "3#-4#", "crossing_type": "河流",
			"dist_small": "6", "synced_at": "2026-10-01 08:00:00", "updated_at": "2026-10-01 07:00:00",
		},
	}
	rows := []map[string]any{{
		"record_id": int64(1), "line_name": "甲线", "section": "1#-2#", "crossing_type": "道路",
		"dist_small": "5",
	}}
	res := DiffRecords(server, rows, nil, nil)
	if res.Missing != 1 {
		t.Fatalf("应识别 1 条缺行，got %+v", res)
	}
	needPush, reason := decideIntranetAction(res, server)
	if !needPush || !strings.Contains(reason, "缺行 1") {
		t.Fatalf("缺行应触发表下发，got needPush=%v reason=%q", needPush, reason)
	}
	// 墓碑中的服务端记录不算缺行。
	res = DiffRecords(server, rows, []int64{2}, nil)
	if res.Missing != 0 {
		t.Fatalf("墓碑记录不应计缺行，got %+v", res)
	}
}
