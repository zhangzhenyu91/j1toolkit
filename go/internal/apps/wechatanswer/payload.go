// CaptureSink 载荷装配：字段与 treehazard/crossing ingest 契约对齐（mode=direct upsert / deletes）。
// 展示文本格式化沿用 crane inspection/export.js 口径（区段/侧别距离/垂直距离/发现人）。
package wechatanswer

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// payloadSessionID 日桶 ID：`<群ID>_<北京日>`（交跨带 _crossing 后缀，与 crane 幂等键口径一致）。
func payloadSessionID(groupID, kind string, capturedAt int64) string {
	suffix := ""
	if kind == KindCrossing {
		suffix = "_crossing"
	}
	return groupID + "_" + beijingDay(capturedAt) + suffix
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func textOf(v any) string {
	if v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return strings.TrimSpace(s)
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

// formatSection 区段文本：`起#[-止#][位置备注]`（位置备注=塔底时省略）。
func formatSection(ex map[string]any) string {
	from, okFrom := asFloat(ex["pole_from"])
	if !okFrom || from <= 0 {
		return ""
	}
	fromI := int64(from)
	note := textOf(ex["position_note"])
	suffix := ""
	if note != "" && note != "塔底" {
		suffix = note
	}
	to, okTo := asFloat(ex["pole_to"])
	if !okTo || to <= 0 || int64(to) == fromI {
		return fmt.Sprintf("%d#%s", fromI, suffix)
	}
	return fmt.Sprintf("%d#-%d#%s", fromI, int64(to), suffix)
}

// formatVertical 垂直距离文本：over_height 优先（超高X），否则 vertical 原文。
func formatVertical(ex map[string]any) string {
	if oh := textOf(ex["over_height"]); oh != "" {
		return "超高" + oh
	}
	return textOf(ex["vertical"])
}

// formatSideDistance 侧别距离：distance 按 side 归入 small/big。
func formatSideDistance(ex map[string]any, side string) string {
	value := textOf(ex["distance"])
	if value == "" {
		return ""
	}
	recordSide := textOf(ex["side"])
	if side == "small" {
		if recordSide == "small" || recordSide == "both" {
			return value
		}
		return ""
	}
	if recordSide == "big" {
		return value
	}
	return ""
}

// formatFoundAt capturedAt → yyyy-MM-dd（北京日）。
func formatFoundAt(unixSec int64) string {
	if unixSec == 0 {
		return ""
	}
	return beijingDay(unixSec)
}

// finderNames 发现人展示：记录者在前，协作供图人拼接在后（上限两人）。
func finderNames(rec *Record) string {
	base := rec.SenderName
	if base == "" {
		base = rec.SenderID
	}
	if rec.CoSenderID == "" || rec.CoSenderID == rec.SenderID {
		return base
	}
	co := rec.CoSenderName
	if co == "" {
		co = rec.CoSenderID
	}
	return base + " " + co
}

// buildEvidencePhotos 读本地辅证文件转 base64 dataURL（恒为数组，空=清除壹匣既有辅证）。
func (m *Module) buildEvidencePhotos(rec *Record) []map[string]string {
	out := []map[string]string{}
	for _, p := range rec.Photos {
		if p.File == "" {
			continue
		}
		buf, err := os.ReadFile(m.photoPath(p.File))
		if err != nil {
			continue
		}
		mime := "image/jpeg"
		if strings.HasSuffix(strings.ToLower(p.File), ".png") {
			mime = "image/png"
		}
		out = append(out, map[string]string{
			"msg_id": p.MsgID,
			"image":  "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(buf),
		})
	}
	return out
}

// photoPath 相对 ServerDir 的照片绝对路径。
func (m *Module) photoPath(rel string) string {
	base := m.ServerDir
	if base == "" {
		base = "."
	}
	return filepath.Join(base, filepath.FromSlash(rel))
}

// buildRecordPayload 单条记录载荷（树障/交跨按 kind 分派）。
func (m *Module) buildRecordPayload(rec *Record) map[string]any {
	ex := rec.Extracted
	if ex == nil {
		ex = map[string]any{}
	}
	if rec.Kind == KindCrossing {
		return map[string]any{
			"crane_record_id":  rec.Key,
			"msg_id":           rec.MsgID,
			"voltage_level":    textOf(ex["voltage_level"]),
			"line_name":        textOf(ex["line_name"]),
			"line_name_raw":    rec.LineNameRaw,
			"section":          formatSection(ex),
			"pole_from":        intOrNil(ex["pole_from"]),
			"pole_to":          intOrNil(ex["pole_to"]),
			"crossing_type":    textOf(ex["crossing_type"]),
			"dist_small":       formatSideDistance(ex, "small"),
			"dist_big":         formatSideDistance(ex, "big"),
			"cross_horizontal": textOf(ex["cross_horizontal"]),
			"cross_vertical":   textOf(ex["cross_vertical"]),
			"cross_angle":      textOf(ex["cross_angle"]),
			"has_hazard":       textOf(ex["has_hazard"]),
			"finder_name":      finderNames(rec),
			"inspect_at":       formatFoundAt(rec.CapturedAt),
			"remark":           textOf(ex["remark"]),
		}
	}
	return map[string]any{
		"crane_record_id": rec.Key,
		"msg_id":          rec.MsgID,
		"voltage_level":   textOf(ex["voltage_level"]),
		"line_name":       textOf(ex["line_name"]),
		"line_name_raw":   rec.LineNameRaw,
		"section":         formatSection(ex),
		"pole_from":       intOrNil(ex["pole_from"]),
		"pole_to":         intOrNil(ex["pole_to"]),
		"position_note":   textOf(ex["position_note"]),
		"tree_species":    textOf(ex["tree_species"]),
		"tree_count":      textOf(ex["tree_count"]),
		"dist_small":      formatSideDistance(ex, "small"),
		"dist_big":        formatSideDistance(ex, "big"),
		"diameter":        textOf(ex["diameter"]),
		"horizontal":      textOf(ex["horizontal"]),
		"vertical":        formatVertical(ex),
		"finder_name":     finderNames(rec),
		"found_at":        formatFoundAt(rec.CapturedAt),
		"evidence_photos": m.buildEvidencePhotos(rec),
	}
}

// intOrNil 数值字段（无有效正整数 → null，对齐 ingest 侧）。
func intOrNil(v any) any {
	f, ok := asFloat(v)
	if !ok || f <= 0 {
		return nil
	}
	return int64(f)
}

// buildUpsertPayload upsert 载荷（单条记录，mode=direct）。
func (m *Module) buildUpsertPayload(rec *Record, teamName string) ([]byte, error) {
	body := map[string]any{
		"session_id": payloadSessionID(rec.GroupID, rec.Kind, rec.CapturedAt),
		"group_id":   rec.GroupID,
		"group_name": rec.GroupName,
		"team_name":  teamName,
		"mode":       "direct",
		"pusher":     map[string]any{"wxid": "", "name": ""},
		"records":    []any{m.buildRecordPayload(rec)},
	}
	return json.Marshal(body)
}

// buildDeletePayload 撤回删除载荷（deletes 契约）。
func (m *Module) buildDeletePayload(rec *Record, teamName string) ([]byte, error) {
	body := map[string]any{
		"session_id": payloadSessionID(rec.GroupID, rec.Kind, rec.CapturedAt),
		"group_name": rec.GroupName,
		"team_name":  teamName,
		"mode":       "direct",
		"deletes":    []any{map[string]any{"crane_record_id": rec.Key, "msg_id": rec.MsgID}},
	}
	return json.Marshal(body)
}
