// 线路名归一与电压读取（移植 crane-answer src/lines.js）：
// 报送原文 → 归一（去电压前缀/空白、罗马数字统一 Ⅰ/Ⅱ/Ⅲ）→ 与本班 team_line 匹配，
// 命中取规范线路名与电压等级；未命中保留归一原文（line_name_raw）不带电压。
package wechatanswer

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"sync"
	"time"
)

// TeamLine 本班运维线路（team_line 一行）。
type TeamLine struct {
	Voltage string // 电压等级数字串（如 "110"）
	Name    string // 线路名称
}

// LineSource 线路表来源（默认 SQLLineSource 读 team_line；测试可注入）。
type LineSource interface {
	TeamLines(ctx context.Context, teamID int64) ([]TeamLine, error)
}

// TeamSource 班组名来源（默认 SQLTeamSource 读 sys_team）。
type TeamSource interface {
	TeamName(ctx context.Context, teamID int64) (string, error)
}

// SQLLineSource 读 team_line（10 分钟缓存）。
type SQLLineSource struct {
	DB  *sql.DB
	TTL time.Duration

	mu    sync.Mutex
	cache map[int64]linesEntry
}

type linesEntry struct {
	fetchedAt time.Time
	lines     []TeamLine
}

var _ LineSource = (*SQLLineSource)(nil)

func (s *SQLLineSource) ttl() time.Duration {
	if s.TTL > 0 {
		return s.TTL
	}
	return 10 * time.Minute
}

// TeamLines 本班运维线路（sort, id 序）。
func (s *SQLLineSource) TeamLines(ctx context.Context, teamID int64) ([]TeamLine, error) {
	if s.DB == nil || teamID <= 0 {
		return nil, nil
	}
	s.mu.Lock()
	if s.cache == nil {
		s.cache = map[int64]linesEntry{}
	}
	if e, ok := s.cache[teamID]; ok && time.Since(e.fetchedAt) < s.ttl() {
		s.mu.Unlock()
		return e.lines, nil
	}
	s.mu.Unlock()
	rows, err := s.DB.QueryContext(ctx,
		`SELECT voltage_level, line_name FROM team_line WHERE team_id = ? ORDER BY sort, id`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TeamLine
	for rows.Next() {
		var l TeamLine
		if err := rows.Scan(&l.Voltage, &l.Name); err != nil {
			return nil, err
		}
		l.Name = strings.TrimSpace(l.Name)
		if l.Name == "" {
			continue
		}
		l.Voltage = normalizeVoltageValue(l.Voltage)
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cache[teamID] = linesEntry{fetchedAt: time.Now(), lines: out}
	s.mu.Unlock()
	return out, nil
}

// SQLTeamSource 读 sys_team（启用班组名）。
type SQLTeamSource struct {
	DB *sql.DB
}

var _ TeamSource = (*SQLTeamSource)(nil)

// TeamName 班组名（无匹配返回空串）。
func (s SQLTeamSource) TeamName(ctx context.Context, teamID int64) (string, error) {
	if s.DB == nil || teamID <= 0 {
		return "", nil
	}
	var name string
	err := s.DB.QueryRowContext(ctx,
		`SELECT name FROM sys_team WHERE id = ? AND status = 1`, teamID).Scan(&name)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(name), nil
}

// normalizeVoltageValue 电压归一：取首个数字串（"110kV" → "110"）。
func normalizeVoltageValue(value string) string {
	m := regexp.MustCompile(`\d+`).FindString(value)
	return m
}

var (
	voltPrefixRe = regexp.MustCompile(`^\d+\s*[kK][vV]\s*`)
	roman3TailRe = regexp.MustCompile(`III(线|$)`)
	roman2TailRe = regexp.MustCompile(`II(线|$)`)
	roman1TailRe = regexp.MustCompile(`I(线|$)`)
	digitTailRe  = regexp.MustCompile(`([123])(线|$)`)
)

var romanByDigit = map[byte]string{'1': "Ⅰ", '2': "Ⅱ", '3': "Ⅲ"}

// NormalizeLineName 报送原文线路名归一：去电压等级前缀、去空白、
// 阿拉伯/ASCII 罗马数字统一 Ⅰ/Ⅱ/Ⅲ（数字在「线」前或结尾均转换）。
func NormalizeLineName(raw string) string {
	s := strings.TrimSpace(raw)
	s = voltPrefixRe.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), "")
	s = strings.ReplaceAll(s, " ", "")
	s = roman3TailRe.ReplaceAllString(s, "Ⅲ$1")
	s = roman2TailRe.ReplaceAllString(s, "Ⅱ$1")
	s = roman1TailRe.ReplaceAllString(s, "Ⅰ$1")
	s = digitTailRe.ReplaceAllStringFunc(s, func(t string) string {
		return romanByDigit[t[0]] + t[1:]
	})
	return s
}

// LineMatch 线路匹配结果。
type LineMatch struct {
	Status     string     // ok | ambiguous | unmatched
	Name       string     // 命中（或归一后）线路名
	Voltage    string     // 命中线路电压（未命中为空）
	Candidates []TeamLine // ambiguous 候选
}

// MatchLine 线路名匹配：exact → 补「线」exact → 双向前缀唯一候选。
func MatchLine(raw string, lines []TeamLine) LineMatch {
	name := NormalizeLineName(raw)
	if name == "" {
		return LineMatch{Status: "unmatched"}
	}
	for _, l := range lines {
		if l.Name == name {
			return LineMatch{Status: "ok", Name: l.Name, Voltage: l.Voltage}
		}
	}
	if !strings.HasSuffix(name, "线") {
		for _, l := range lines {
			if l.Name == name+"线" {
				return LineMatch{Status: "ok", Name: l.Name, Voltage: l.Voltage}
			}
		}
	}
	var candidates []TeamLine
	for _, l := range lines {
		if strings.HasPrefix(l.Name, name) || strings.HasPrefix(name, l.Name) {
			candidates = append(candidates, l)
		}
	}
	switch len(candidates) {
	case 1:
		return LineMatch{Status: "ok", Name: candidates[0].Name, Voltage: candidates[0].Voltage}
	case 0:
		return LineMatch{Status: "unmatched", Name: name}
	default:
		return LineMatch{Status: "ambiguous", Name: name, Candidates: candidates}
	}
}

// resolveLine 线路归一 + team_line 匹配（teamID<=0 或无表 → 仅归一）。
func (m *Module) resolveLine(ctx context.Context, teamID int64, raw string) LineMatch {
	name := NormalizeLineName(raw)
	if name == "" {
		return LineMatch{Status: "unmatched"}
	}
	lines, err := m.lineSource().TeamLines(ctx, teamID)
	if err != nil || len(lines) == 0 {
		return LineMatch{Status: "unmatched", Name: name}
	}
	return MatchLine(raw, lines)
}

// lineSource 取线路表来源（注入优先；缺省 MySQL team_line）。
func (m *Module) lineSource() LineSource {
	if m.Lines != nil {
		return m.Lines
	}
	return &SQLLineSource{DB: m.DB}
}

// teamSource 取班组名来源（注入优先；缺省 MySQL sys_team）。
func (m *Module) teamSource() TeamSource {
	if m.Teams != nil {
		return m.Teams
	}
	return SQLTeamSource{DB: m.DB}
}

// teamNameByID 班组名（teamID<=0 返回空）。
func (m *Module) teamNameByID(ctx context.Context, teamID int64) string {
	if teamID <= 0 {
		return ""
	}
	name, err := m.teamSource().TeamName(ctx, teamID)
	if err != nil {
		return ""
	}
	return name
}
