// 题库搜题：题库本地存储（移植自 desktop/electron/quizsearch/bank-store.cjs）。
// 存储布局（dir = userData/quiz-search）：
//
//	banks-index.json      {banks:[{id,name,sourceFile,importedAt,count,enabled}]}
//	banks/<id>.json       {id,name,sourceFile,importedAt,questions:[{type,content,options[],answer,analysis,sort}]}
//
// 字段口径对齐壹匣刷题（server/src/quiz/schema.js quiz_question）：
// type: single 单选 / multiple 多选 / judge 判断；判断题固定选项 ["正确","错误"]，A=正确 B=错误。
// JSON 与旧 Electron 实现双向兼容（可互读互写同一目录）。
package quizsearch

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// BankMeta banks-index.json 中的题库条目。
type BankMeta struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	SourceFile string `json:"sourceFile"`
	ImportedAt string `json:"importedAt"`
	Count      int    `json:"count"`
	Enabled    bool   `json:"enabled"`
}

// Bank banks/<id>.json 题库文件。
type Bank struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	SourceFile string     `json:"sourceFile"`
	ImportedAt string     `json:"importedAt"`
	Questions  []Question `json:"questions"`
}

// ImportResult 导入结果（对齐 bank-store.cjs importRows 返回形状）。
type ImportResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Count   int    `json:"count"`
	Dropped int    `json:"dropped"`
	Enabled bool   `json:"enabled"`
}

// headerAliases Excel 表头别名 → 内部字段（兼容嗖嗖搜题表头与刷题导出表头）。
var headerAliases = []struct {
	field   string
	aliases []string
}{
	{"content", []string{"题干", "题目", "question", "content", "stem"}},
	{"answer", []string{"答案", "answer"}},
	{"analysis", []string{"解析", "答案解析", "analysis"}},
	{"type", []string{"题型", "type"}},
}

var optionLetterRe = regexp.MustCompile(`^选项\s*([a-f])$`)

var judgeAnswerRe = regexp.MustCompile(`^(正确|对|√|T|TRUE|YES)$`)
var judgeWrongRe = regexp.MustCompile(`^(错误|错|×|X|F|FALSE|NO)$`)
var judgeWordRe = regexp.MustCompile(`^(正确|错误|对|错|√|×|T|F|TRUE|FALSE|YES|NO)$`)

// optionLetters 选项列字母（最多 6 列）。
var optionLetters = []string{"A", "B", "C", "D", "E", "F"}

// BankStore 题库存储（目录内 JSON 读写）。
type BankStore struct {
	dir        string
	banksDir   string
	indexPath  string
	now        func() time.Time // 测试可注入
	randSource *rand.Rand       // 测试可注入
}

// NewBankStore 打开（必要时创建）题库目录。
func NewBankStore(dir string) (*BankStore, error) {
	banksDir := filepath.Join(dir, "banks")
	if err := os.MkdirAll(banksDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建题库目录失败：%w", err)
	}
	return &BankStore{
		dir:        dir,
		banksDir:   banksDir,
		indexPath:  filepath.Join(dir, "banks-index.json"),
		now:        time.Now,
		randSource: rand.New(rand.NewSource(time.Now().UnixNano())),
	}, nil
}

func (s *BankStore) bankPath(id string) string {
	return filepath.Join(s.banksDir, id+".json")
}

// readIndex 读索引（文件缺失/损坏按空索引处理，同 .cjs）。
func (s *BankStore) readIndex() []BankMeta {
	data, err := os.ReadFile(s.indexPath)
	if err != nil {
		return []BankMeta{}
	}
	var idx struct {
		Banks []BankMeta `json:"banks"`
	}
	if err := json.Unmarshal(data, &idx); err != nil {
		return []BankMeta{}
	}
	if idx.Banks == nil {
		return []BankMeta{}
	}
	return idx.Banks
}

func (s *BankStore) writeIndex(banks []BankMeta) error {
	if banks == nil {
		banks = []BankMeta{}
	}
	data, err := json.MarshalIndent(struct {
		Banks []BankMeta `json:"banks"`
	}{Banks: banks}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.indexPath, data, 0o644)
}

// List 题库索引列表。
func (s *BankStore) List() []BankMeta {
	return s.readIndex()
}

// GetBank 读题库文件；不存在或损坏返回 nil。
func (s *BankStore) GetBank(id string) *Bank {
	data, err := os.ReadFile(s.bankPath(id))
	if err != nil {
		return nil
	}
	var bank Bank
	if err := json.Unmarshal(data, &bank); err != nil {
		return nil
	}
	if bank.Questions == nil {
		bank.Questions = []Question{}
	}
	return &bank
}

// MapHeaders 表头名归一化（去空白/全角转半角/小写）后查别名表。
// 返回 字段名→列下标；同字段多列时以靠后的列为准（同 .cjs）。
// 选项列识别「选项a」~「选项f」（全角/空格容错）→ optionA~optionF。
func MapHeaders(headerRow []string) map[string]int {
	colOf := make(map[string]int)
	for idx, h := range headerRow {
		key := normalizeHeaderCell(h)
		if key == "" {
			continue
		}
		matched := false
		for _, group := range headerAliases {
			for _, alias := range group.aliases {
				if alias == key {
					colOf[group.field] = idx
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if matched {
			continue
		}
		if m := optionLetterRe.FindStringSubmatch(key); m != nil {
			colOf["option"+strings.ToUpper(m[1])] = idx
		}
	}
	return colOf
}

// normalizeHeaderCell 表头单元格归一化：去首尾空白、全角字母数字转半角、小写。
func normalizeHeaderCell(h string) string {
	key := strings.TrimSpace(h)
	var b strings.Builder
	for _, r := range key {
		if r >= 0xff01 && r <= 0xff5e {
			r -= 0xfee0
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

// NormAnswer 答案归一化：去空白与「，、」、转大写、字符排序（多选题答案顺序无关）。
func NormAnswer(raw string) string {
	clean := strings.Map(func(r rune) rune {
		if r == '，' || r == '、' || unicodeIsSpace(r) {
			return -1
		}
		return r
	}, raw)
	return sortedRunes(strings.ToUpper(strings.TrimSpace(clean)))
}

func unicodeIsSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' ||
		r == 0x00a0 || r == 0x1680 || (r >= 0x2000 && r <= 0x200a) ||
		r == 0x2028 || r == 0x2029 || r == 0x202f || r == 0x205f || r == 0x3000 || r == 0xfeff
}

// NormJudgeAnswer 判断题答案归一：正确/对/√/T → A；错误/错/×/X/F → B。
func NormJudgeAnswer(a string) string {
	if judgeAnswerRe.MatchString(a) {
		return "A"
	}
	if judgeWrongRe.MatchString(a) {
		return "B"
	}
	return a
}

// newID 生成题库 id：毫秒时间戳 36 进制 + 4 位随机 36 进制（对齐 .cjs newId 形态）。
func (s *BankStore) newID() string {
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	var sb strings.Builder
	sb.WriteString(strconv.FormatInt(s.now().UnixMilli(), 36))
	for i := 0; i < 4; i++ {
		sb.WriteByte(digits[s.randSource.Intn(len(digits))])
	}
	return sb.String()
}

// ImportRows 导入题库。rows 为表格二维数组（首行表头，值均为字符串）。
// 返回 {id, name, count, dropped, enabled}。
func (s *BankStore) ImportRows(name, sourceFile string, rows [][]string) (ImportResult, error) {
	if len(rows) < 2 {
		return ImportResult{}, errors.New("表格为空或缺少数据行")
	}
	colOf := MapHeaders(rows[0])
	contentCol, hasContent := colOf["content"]
	answerCol, hasAnswer := colOf["answer"]
	if !hasContent || !hasAnswer {
		return ImportResult{}, errors.New("未识别到「题干」「答案」表头列")
	}
	get := func(r []string, c int) string {
		if c < 0 || c >= len(r) {
			return ""
		}
		return r[c]
	}
	questions := make([]Question, 0, len(rows)-1)
	seen := make(map[string]struct{})
	dropped := 0
	for _, r := range rows[1:] {
		content := strings.TrimSpace(get(r, contentCol))
		answer := NormAnswer(get(r, answerCol))
		if content == "" || answer == "" {
			dropped++
			continue
		}
		options := make([]string, 0, 6)
		for _, L := range optionLetters {
			c, ok := colOf["option"+L]
			if !ok {
				continue
			}
			if v := strings.TrimSpace(get(r, c)); v != "" {
				options = append(options, v)
			}
		}
		analysis := ""
		if c, ok := colOf["analysis"]; ok {
			analysis = strings.TrimSpace(get(r, c))
		}
		qtype := ""
		if c, ok := colOf["type"]; ok {
			qtype = typeMap[strings.ToLower(strings.TrimSpace(get(r, c)))]
		}
		if qtype == "" {
			switch {
			case judgeWordRe.MatchString(answer):
				qtype = "judge"
			case len(options) == 0 && (answer == "A" || answer == "B"):
				qtype = "judge" // 无选项且答案仅 A/B：判断题（表内省略了 正确/错误 选项列）
			case len([]rune(answer)) > 1:
				qtype = "multiple"
			default:
				qtype = "single"
			}
		}
		if qtype == "judge" {
			answer = NormJudgeAnswer(answer)
			if len(options) == 0 {
				options = append(options, "正确", "错误")
			}
		}
		key := Normalize(content) + "|" + answer
		if _, dup := seen[key]; dup {
			dropped++
			continue // 库内去重（原软件 Distinct 语义）
		}
		seen[key] = struct{}{}
		questions = append(questions, Question{
			Type:     qtype,
			Content:  content,
			Options:  options,
			Answer:   answer,
			Analysis: analysis,
			Sort:     len(questions),
		})
	}
	if len(questions) == 0 {
		return ImportResult{}, errors.New("未解析到有效题目，请检查表头与数据")
	}
	id := s.newID()
	bankName := strings.TrimSpace(name)
	if bankName == "" {
		bankName = bankNameFromFile(sourceFile)
	}
	bank := Bank{
		ID:         id,
		Name:       bankName,
		SourceFile: sourceFile,
		ImportedAt: s.now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Questions:  questions,
	}
	data, err := json.Marshal(bank) // 同 .cjs：题库文件紧凑单行
	if err != nil {
		return ImportResult{}, err
	}
	if err := os.WriteFile(s.bankPath(id), data, 0o644); err != nil {
		return ImportResult{}, fmt.Errorf("写入题库文件失败：%w", err)
	}
	idx := s.readIndex()
	idx = append(idx, BankMeta{
		ID:         id,
		Name:       bankName,
		SourceFile: bank.SourceFile,
		ImportedAt: bank.ImportedAt,
		Count:      len(questions),
		Enabled:    true,
	})
	if err := s.writeIndex(idx); err != nil {
		return ImportResult{}, err
	}
	return ImportResult{ID: id, Name: bankName, Count: len(questions), Dropped: dropped, Enabled: true}, nil
}

// typeMap 表格题型列 → 内部题型。
var typeMap = map[string]string{
	"单选":       "single",
	"多选":       "multiple",
	"判断":       "judge",
	"single":   "single",
	"multiple": "multiple",
	"judge":    "judge",
}

// bankNameFromFile 未给题库名时取源文件名（去扩展名）。
func bankNameFromFile(sourceFile string) string {
	base := filepath.Base(sourceFile)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	if name == "" {
		return "题库"
	}
	return name
}

// Remove 删除题库（索引与文件；文件已不存在时忽略）。
func (s *BankStore) Remove(id string) error {
	idx := s.readIndex()
	filtered := idx[:0]
	for _, b := range idx {
		if b.ID != id {
			filtered = append(filtered, b)
		}
	}
	if err := s.writeIndex(filtered); err != nil {
		return err
	}
	if err := os.Remove(s.bankPath(id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Rename 重命名题库（索引与题库文件同步；name 为空保持原名）。
func (s *BankStore) Rename(id, name string) error {
	idx := s.readIndex()
	newName := ""
	found := false
	for i := range idx {
		if idx[i].ID == id {
			found = true
			newName = strings.TrimSpace(name)
			if newName == "" {
				newName = idx[i].Name
			}
			idx[i].Name = newName
		}
	}
	if found {
		if err := s.writeIndex(idx); err != nil {
			return err
		}
	}
	if bank := s.GetBank(id); bank != nil {
		if newName != "" {
			bank.Name = newName
		}
		data, err := json.Marshal(bank)
		if err != nil {
			return err
		}
		if err := os.WriteFile(s.bankPath(id), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// SetEnabled 启用/停用题库（仅启用库参与搜题）。
func (s *BankStore) SetEnabled(id string, enabled bool) error {
	idx := s.readIndex()
	for i := range idx {
		if idx[i].ID == id {
			idx[i].Enabled = enabled
		}
	}
	return s.writeIndex(idx)
}

// AllEnabledQuestions 全部启用库的合并题目（每题带上库名，结果窗展示来源）。
func (s *BankStore) AllEnabledQuestions() []Question {
	out := []Question{}
	for _, meta := range s.readIndex() {
		if !meta.Enabled {
			continue
		}
		bank := s.GetBank(meta.ID)
		if bank == nil {
			continue
		}
		for _, q := range bank.Questions {
			qq := q
			qq.BankName = bank.Name
			out = append(out, qq)
		}
	}
	return out
}
