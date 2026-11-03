// Package linecatalog 线路名归一与运维线路（team_line）目录匹配。
//
// 树障/交跨记录、微信智能应答、更正与手动编辑统一走这套口径：
// 先去电压前缀与空白、统一罗马数字（含 12 → ⅠⅡ），再按
// 精确线路名 → 归一线路名 → 关联别名 → 补「线」→ 唯一前缀 的顺序匹配；
// 关联别名由 team_line_link 维护（一条原线路名 → 关联后的统一线路名）。
package linecatalog

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
)

// Queryer 查询接口（*sql.DB / *sql.Tx 均满足）。
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Line 一条运维线路（team_line）。
type Line struct {
	Voltage string // 电压等级数字串（如 "110"）
	Name    string // 线路名称（team_line.line_name 原值）
}

// Alias 一条线路关联（team_line_link）：Alias 原线路名，Name 关联后的统一线路名。
type Alias struct {
	Alias string
	Name  string
}

// Catalog 线路目录（本班 team_line + team_line_link）。
type Catalog struct {
	lines      []Line
	byName     map[string]Line
	byNorm     map[string]Line
	aliasExact map[string]string
	aliasNorm  map[string]string
}

// Match 线路匹配结果。
type Match struct {
	Status     string // ok | ambiguous | unmatched
	Name       string // 命中规范名（未命中为归一后原文）
	Voltage    string // 命中线路电压（未命中为空）
	Candidates []Line // ambiguous 候选
}

// New 由线路与关联别名构造目录（纯函数，测试与注入用）。
func New(lines []Line, aliases []Alias) *Catalog {
	c := &Catalog{
		lines:      make([]Line, 0, len(lines)),
		byName:     make(map[string]Line, len(lines)),
		byNorm:     make(map[string]Line, len(lines)),
		aliasExact: make(map[string]string, len(aliases)),
		aliasNorm:  make(map[string]string, len(aliases)),
	}
	for _, l := range lines {
		name := strings.TrimSpace(l.Name)
		if name == "" {
			continue
		}
		l.Name = name
		l.Voltage = NormalizeVoltage(l.Voltage)
		c.lines = append(c.lines, l)
		if _, ok := c.byName[name]; !ok {
			c.byName[name] = l
		}
		norm := Normalize(name)
		if _, ok := c.byNorm[norm]; !ok {
			c.byNorm[norm] = l
		}
	}
	for _, a := range aliases {
		alias := strings.TrimSpace(a.Alias)
		name := strings.TrimSpace(a.Name)
		if alias == "" || name == "" || alias == name {
			continue
		}
		if _, ok := c.aliasExact[alias]; !ok {
			c.aliasExact[alias] = name
		}
		norm := Normalize(alias)
		if _, ok := c.aliasNorm[norm]; !ok {
			c.aliasNorm[norm] = name
		}
	}
	return c
}

// Load 读取本班线路目录（team_line 按 sort/id 序 + team_line_link）。
// teamID<=0 或 q 为 nil 时返回空目录。
func Load(ctx context.Context, q Queryer, teamID int64) (*Catalog, error) {
	if q == nil || teamID <= 0 {
		return New(nil, nil), nil
	}
	lines := make([]Line, 0)
	rows, err := q.QueryContext(ctx,
		`SELECT voltage_level, line_name FROM team_line WHERE team_id = ? ORDER BY sort, id`, teamID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var l Line
		if err := rows.Scan(&l.Voltage, &l.Name); err != nil {
			rows.Close()
			return nil, err
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	aliases := make([]Alias, 0)
	arows, err := q.QueryContext(ctx,
		`SELECT alias_name, line_name FROM team_line_link WHERE team_id = ? ORDER BY id`, teamID)
	if err != nil {
		return nil, err
	}
	for arows.Next() {
		var a Alias
		if err := arows.Scan(&a.Alias, &a.Name); err != nil {
			arows.Close()
			return nil, err
		}
		aliases = append(aliases, a)
	}
	if err := arows.Err(); err != nil {
		arows.Close()
		return nil, err
	}
	arows.Close()
	return New(lines, aliases), nil
}

var (
	voltPrefixRe = regexp.MustCompile(`^\d+\s*[kK][vV]\s*`)
	roman3TailRe = regexp.MustCompile(`III(线|$)`)
	roman2TailRe = regexp.MustCompile(`II(线|$)`)
	roman1TailRe = regexp.MustCompile(`I(线|$)`)
	// 双回线路的「12」写法（峰义12线）统一为「ⅠⅡ」，先于单数字替换。
	digitPairTailRe = regexp.MustCompile(`12(线|$)`)
	digitTailRe     = regexp.MustCompile(`([123])(线|$)`)
	voltDigitRe     = regexp.MustCompile(`\d+`)
)

var romanByDigit = map[byte]string{'1': "Ⅰ", '2': "Ⅱ", '3': "Ⅲ"}

// NormalizeVoltage 电压归一：取首个数字串（"110kV" → "110"）。
func NormalizeVoltage(value string) string {
	return voltDigitRe.FindString(value)
}

// Normalize 线路名归一：去电压等级前缀、去空白，阿拉伯/ASCII 罗马数字统一 Ⅰ/Ⅱ/Ⅲ
// （数字在「线」前或结尾均转换；12 → ⅠⅡ 兼容同塔双回写法）。
func Normalize(raw string) string {
	s := strings.TrimSpace(raw)
	s = voltPrefixRe.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), "")
	s = strings.ReplaceAll(s, " ", "")
	s = roman3TailRe.ReplaceAllString(s, "Ⅲ$1")
	s = roman2TailRe.ReplaceAllString(s, "Ⅱ$1")
	s = roman1TailRe.ReplaceAllString(s, "Ⅰ$1")
	s = digitPairTailRe.ReplaceAllString(s, "ⅠⅡ$1")
	s = digitTailRe.ReplaceAllStringFunc(s, func(t string) string {
		return romanByDigit[t[0]] + t[1:]
	})
	return s
}

// AliasTarget 原线路名是否为已关联别名（精确或归一命中）；返回统一线路名。
func (c *Catalog) AliasTarget(raw string) (string, bool) {
	if c == nil {
		return "", false
	}
	trimmed := strings.TrimSpace(raw)
	if target, ok := c.aliasExact[trimmed]; ok {
		return target, true
	}
	if target, ok := c.aliasNorm[Normalize(raw)]; ok {
		return target, true
	}
	return "", false
}

// Resolve 完整匹配：精确线路 → 归一线路 → 关联别名 → 精确补「线」→ 双向前缀唯一候选。
func (c *Catalog) Resolve(raw string) Match {
	if c == nil {
		return Match{Status: "unmatched", Name: Normalize(raw)}
	}
	name := Normalize(raw)
	if name == "" {
		return Match{Status: "unmatched"}
	}
	trimmed := strings.TrimSpace(raw)
	if l, ok := c.byName[trimmed]; ok {
		return okMatch(l)
	}
	if l, ok := c.byNorm[name]; ok {
		return okMatch(l)
	}
	if target, ok := c.aliasExact[trimmed]; ok {
		return c.targetMatch(target)
	}
	if target, ok := c.aliasNorm[name]; ok {
		return c.targetMatch(target)
	}
	if !strings.HasSuffix(name, "线") {
		if l, ok := c.byNorm[name+"线"]; ok {
			return okMatch(l)
		}
	}
	candidates := make([]Line, 0)
	for _, l := range c.lines {
		norm := Normalize(l.Name)
		if strings.HasPrefix(norm, name) || strings.HasPrefix(name, norm) {
			candidates = append(candidates, l)
		}
	}
	switch len(candidates) {
	case 1:
		return okMatch(candidates[0])
	case 0:
		return Match{Status: "unmatched", Name: name}
	default:
		return Match{Status: "ambiguous", Name: name, Candidates: candidates}
	}
}

// ResolveExact 不做前缀猜测的匹配：精确线路 → 归一线路 → 关联别名 → 精确补「线」。
// 批量归一名（关联后重写记录）用，避免把短名称意外并入唯一前缀线路。
func (c *Catalog) ResolveExact(raw string) Match {
	if c == nil {
		return Match{Status: "unmatched", Name: Normalize(raw)}
	}
	name := Normalize(raw)
	if name == "" {
		return Match{Status: "unmatched"}
	}
	trimmed := strings.TrimSpace(raw)
	if l, ok := c.byName[trimmed]; ok {
		return okMatch(l)
	}
	if l, ok := c.byNorm[name]; ok {
		return okMatch(l)
	}
	if target, ok := c.aliasExact[trimmed]; ok {
		return c.targetMatch(target)
	}
	if target, ok := c.aliasNorm[name]; ok {
		return c.targetMatch(target)
	}
	if !strings.HasSuffix(name, "线") {
		if l, ok := c.byNorm[name+"线"]; ok {
			return okMatch(l)
		}
	}
	return Match{Status: "unmatched", Name: name}
}

// MatchLines 无关联别名时的线路匹配（保留给纯线路表调用方）。
func MatchLines(raw string, lines []Line) Match {
	return New(lines, nil).Resolve(raw)
}

func okMatch(l Line) Match {
	return Match{Status: "ok", Name: l.Name, Voltage: l.Voltage}
}

// targetMatch 关联别名命中：统一线路存在时取线路电压，缺失时仍返回规范名。
func (c *Catalog) targetMatch(name string) Match {
	if l, ok := c.byName[name]; ok {
		return okMatch(l)
	}
	for _, l := range c.lines {
		if Normalize(l.Name) == Normalize(name) {
			return okMatch(l)
		}
	}
	return Match{Status: "ok", Name: name}
}
