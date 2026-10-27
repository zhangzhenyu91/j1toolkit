// 出工日志：记录验证状态（verify_passed）与未通过明细（verify_reasons）计算，
// logs / day-status / report 共用；个人口径 myReportReasons 供 /report 与 /day-status 的 scope=mine 共用。
// 规则（见《开发指南》7.1 与 15.3 汇总前核验口径）：
// 卡片级前提：① 未出车不验证（exempt）；② 目的地已选、派车单号已填且有用车人（巡视内容可空，不计入）；
// a. 已绑定商旅的用车人均完成两次打卡（开始+结束，未绑定不参与）；b. 用车人均已上传水印照片；
// c. 水印照片施工内容一致且均非空；d. 水印照片地点均包含派车目的地；e. 水印照片拍摄时间均为记录当天；
// f. 用车人当日费用均已填写且达当日适用标准（市内 伙食60/交通0；市外 100/0；驻地 40/0，按记录日期取生效版本）；
// g. 已绑定商旅的用车人两次打卡地点至少一次包含派车目的地（两次打卡齐才判）。
// （d/e 即照片级核验 date_ok/dest_ok；非水印照片 is_watermark=0 不参与）
package worklog

import (
	"strconv"
	"strings"
)

// ClockinEntry 单次打卡（seq 1 开始 / 2 结束）。
type ClockinEntry struct {
	DetailID  string `json:"detailId"`
	Time      string `json:"time"`
	Position  string `json:"position"`
	WorkHours string `json:"workHours"`
	Lng       string `json:"lng"`
	Lat       string `json:"lat"`
	CityCode  string `json:"cityCode"`
	CityName  string `json:"cityName"`
}

// FeeEntry 当日费用（每人每日一条）。
type FeeEntry struct {
	FoodFee    float64 `json:"foodFee"`
	TransitFee float64 `json:"transitFee"`
}

// FeeStd 记录当日适用费用标准。
type FeeStd struct {
	FoodFee    float64 `json:"foodFee"`
	TransitFee float64 `json:"transitFee"`
	Scope      int     `json:"scope"`
}

// FeeStdRow worklog_fee_std 行（eff 为 'YYYY-MM-DD'，字典序可比）。
type FeeStdRow struct {
	Scope      int
	FoodFee    float64
	TransitFee float64
	Eff        string
}

// MemberRow 卡片用车人。
type MemberRow struct {
	ID            int64  `json:"id"`
	MemberID      int64  `json:"member_id"`
	Name          string `json:"name"`
	Checked       int    `json:"checked"`
	Sort          int    `json:"sort"`
	SgccBound     bool   `json:"sgccBound"`
	SgccTokenStat *int   `json:"sgccTokenStatus"`
}

// PhotoItem 卡片水印照片。
type PhotoItem struct {
	ID           int64    `json:"id"`
	URL          string   `json:"url"`
	Members      []string `json:"members"`
	VerifyStatus string   `json:"verify_status"`
	WorkContent  string   `json:"work_content"`
	ShotTime     string   `json:"shot_time"`
	Weather      string   `json:"weather"`
	Location     string   `json:"location"`
	Lng          string   `json:"lng"`
	Lat          string   `json:"lat"`
	DateOK       *int     `json:"date_ok"`
	DestOK       *int     `json:"dest_ok"`
	IsWatermark  int      `json:"is_watermark"`
	Source       string   `json:"source"`
	SgccSynced   int      `json:"sgcc_synced"`
}

// RemarkFile 备注附件。
type RemarkFile struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	CosKey string `json:"cos_key"`
	Type   string `json:"type"`
	Size   int64  `json:"size"`
}

// Entry 卡片全量装配（loadEntries 产物，verify/report/day-status 共用）。
type Entry struct {
	ID                   int64                           `json:"id"`
	TeamID               int64                           `json:"team_id"`
	LogDate              string                          `json:"log_date"`
	PatrolContent        string                          `json:"patrol_content"`
	Remark               string                          `json:"remark"`
	RemarkFiles          []RemarkFile                    `json:"remark_files"`
	CrossTeam            int                             `json:"cross_team"`
	VehicleID            int64                           `json:"vehicle_id"`
	PlateNo              string                          `json:"plate_no"`
	DestinationID        int64                           `json:"destination_id"`
	DestinationName      string                          `json:"destination_name"`
	DestinationOutOfCity int                             `json:"destination_out_of_city"`
	DestinationStationed int                             `json:"destination_stationed"`
	DispatchOrderNo      string                          `json:"dispatch_order_no"`
	CreatedBy            int64                           `json:"created_by"`
	Members              []MemberRow                     `json:"members"`
	Photos               []PhotoItem                     `json:"photos"`
	ClockinMap           map[string]map[int]ClockinEntry `json:"clockinMap"`
	FeeMap               map[string]FeeEntry             `json:"feeMap"`
	FeeStd               FeeStd                          `json:"feeStd"`
	VerifyPassed         string                          `json:"verify_passed"`
	VerifyReasons        []string                        `json:"verify_reasons"`
}

// wmPhotos 参与验证的照片集：剔除非水印照片。
func wmPhotos(entry *Entry) []PhotoItem {
	out := make([]PhotoItem, 0, len(entry.Photos))
	for _, p := range entry.Photos {
		if p.IsWatermark != 0 {
			out = append(out, p)
		}
	}
	return out
}

// contentOf 施工内容取值（空串/纯空白一律视为空）。
func contentOf(p PhotoItem) string { return strings.TrimSpace(p.WorkContent) }

// clockinDestOk 规则 g：两次打卡地点至少一次包含派车目的地（目的地为空不约束；两次不齐返回 true 交规则 a 报）。
func clockinDestOk(c map[int]ClockinEntry, dest string) bool {
	if dest == "" {
		return true
	}
	c1, ok1 := c[1]
	c2, ok2 := c[2]
	if !ok1 || !ok2 {
		return true
	}
	return strings.Contains(c1.Position, dest) || strings.Contains(c2.Position, dest)
}

// feeOk 规则 f：当日费用已填写且金额符合当日适用标准。
func feeOk(f *FeeEntry, std FeeStd) bool {
	if f == nil {
		return false
	}
	return f.FoodFee == std.FoodFee && f.TransitFee == std.TransitFee
}

// ResolveFeeStd 按口径取记录日期当日已生效的最新一行；无配置兜底 驻地 40/0、其余 60/0。
func ResolveFeeStd(rows []FeeStdRow, scope int, logDate string) FeeStd {
	var hit *FeeStdRow
	for i := range rows {
		r := &rows[i]
		if r.Scope != scope {
			continue
		}
		if r.Eff != "" && r.Eff <= logDate && (hit == nil || r.Eff > hit.Eff) {
			hit = r
		}
	}
	fallback := 60.0
	if scope == 2 {
		fallback = 40
	}
	if hit != nil {
		return FeeStd{FoodFee: hit.FoodFee, TransitFee: hit.TransitFee, Scope: scope}
	}
	return FeeStd{FoodFee: fallback, TransitFee: 0, Scope: scope}
}

// feeStdOf 记录当日适用标准（entry.feeStd 缺失按市内 60/0 兜底）。
func feeStdOf(entry *Entry) FeeStd {
	s := entry.FeeStd
	if s.FoodFee == 0 && s.TransitFee == 0 && s.Scope == 0 {
		return FeeStd{FoodFee: 60, TransitFee: 0, Scope: 0}
	}
	return s
}

// feeStdText 标准文案（市外/驻地追加标注）。
func feeStdText(std FeeStd) string {
	suffix := ""
	switch std.Scope {
	case 1:
		suffix = "（市外）"
	case 2:
		suffix = "（驻地）"
	}
	return feeNum(std.FoodFee) + "/" + feeNum(std.TransitFee) + suffix
}

func feeNum(v float64) string {
	s := trimFloat(v)
	return s
}

// trimFloat 整数不带小数点（JS Number 同口径）。
func trimFloat(v float64) string {
	i := int64(v)
	if float64(i) == v {
		return strconv.FormatInt(i, 10)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// itoa int64 转字符串（clockinMap/FeeMap 的成员键）。
func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// WatermarkCheck 单张照片水印信息核验结果。
type WatermarkCheck struct {
	DateOK bool
	DestOK bool
	Status string // passed / mismatch
}

// CheckWatermark 单张照片核验（Dify 只识别，日期/地点比对在后端）：
// 日期相符 = 识别拍摄时间包含记录日期（YYYY.MM.DD 点分格式）；
// 地点相符 = 识别地点包含派车目的地（目的地为空不约束）。
func CheckWatermark(time, location, logDate, destination string) WatermarkCheck {
	dateOK := time != "" && logDate != "" && strings.Contains(time, logDate)
	dest := strings.TrimSpace(destination)
	destOK := dest == "" || strings.Contains(location, dest)
	status := "mismatch"
	if dateOK && destOK {
		status = "passed"
	}
	return WatermarkCheck{DateOK: dateOK, DestOK: destOK, Status: status}
}

// ComputeVerifyPassed 卡片验证状态：exempt / failed / passed。
func ComputeVerifyPassed(entry *Entry) string {
	if entry.VehicleID == 0 {
		return "exempt"
	}
	if entry.DestinationID == 0 {
		return "failed"
	}
	if strings.TrimSpace(entry.DispatchOrderNo) == "" {
		return "failed"
	}
	if len(entry.Members) == 0 {
		return "failed"
	}
	// a. 两次打卡（仅已绑定商旅者）
	ck := entry.ClockinMap
	for _, m := range entry.Members {
		if !m.SgccBound {
			continue
		}
		c := ck[itoa(m.MemberID)]
		if _, ok1 := c[1]; !ok1 {
			return "failed"
		}
		if _, ok2 := c[2]; !ok2 {
			return "failed"
		}
	}
	// g. 打卡地点至少一次含目的地
	dest := strings.TrimSpace(entry.DestinationName)
	for _, m := range entry.Members {
		if !m.SgccBound {
			continue
		}
		if !clockinDestOk(ck[itoa(m.MemberID)], dest) {
			return "failed"
		}
	}
	// f. 费用全员达标
	std := feeStdOf(entry)
	for _, m := range entry.Members {
		f := entry.FeeMap[itoa(m.MemberID)]
		if !feeOk(&f, std) {
			return "failed"
		}
	}
	// b. 用车人均有水印照片
	photos := wmPhotos(entry)
	photoNames := map[string]bool{}
	for _, p := range photos {
		for _, n := range p.Members {
			photoNames[n] = true
		}
	}
	for _, m := range entry.Members {
		if !photoNames[m.Name] {
			return "failed"
		}
	}
	if len(photos) == 0 {
		return "failed"
	}
	// d/e. 全部照片核验通过
	for _, p := range photos {
		if p.VerifyStatus != "passed" {
			return "failed"
		}
	}
	// c. 施工内容一致且非空
	contents := map[string]bool{}
	for _, p := range photos {
		c := contentOf(p)
		if c == "" {
			return "failed"
		}
		contents[c] = true
	}
	if len(contents) > 1 {
		return "failed"
	}
	return "passed"
}

// photoIssues 单张照片未通过项（passed 返回空；pending=验证中；failed=验证失败；mismatch 按 date_ok/dest_ok 列出）。
func photoIssues(p PhotoItem) []string {
	switch p.VerifyStatus {
	case "pending":
		return []string{"验证中"}
	case "failed":
		return []string{"验证失败"}
	case "passed":
		return nil
	}
	bad := make([]string, 0, 2)
	dateBad := (p.DateOK != nil && *p.DateOK == 0) || (p.DateOK == nil && p.VerifyStatus == "date_mismatch")
	destBad := (p.DestOK != nil && *p.DestOK == 0) || (p.DestOK == nil && p.VerifyStatus == "dest_mismatch")
	if dateBad {
		bad = append(bad, "日期不符")
	}
	if destBad {
		bad = append(bad, "地点不符")
	}
	if len(bad) == 0 {
		return []string{"未通过验证"}
	}
	return bad
}

// ComputeFailReasons 记录未通过明细：逐条列出全部不满足项（与 ComputeVerifyPassed 同口径但不短路）。
func ComputeFailReasons(entry *Entry) []string {
	if entry.VehicleID == 0 {
		return nil
	}
	reasons := make([]string, 0)
	if entry.DestinationID == 0 {
		reasons = append(reasons, "未选择目的地")
	}
	if strings.TrimSpace(entry.DispatchOrderNo) == "" {
		reasons = append(reasons, "未填写派车单号")
	}
	if len(entry.Members) == 0 {
		reasons = append(reasons, "未选择用车人")
	}
	ck := entry.ClockinMap
	dest := strings.TrimSpace(entry.DestinationName)
	for _, m := range entry.Members {
		if !m.SgccBound {
			continue
		}
		c := ck[itoa(m.MemberID)]
		_, ok1 := c[1]
		_, ok2 := c[2]
		switch {
		case !ok1 && !ok2:
			reasons = append(reasons, m.Name+"未打卡（缺开始与结束）")
		case !ok1:
			reasons = append(reasons, m.Name+"缺「开始打卡」")
		case !ok2:
			reasons = append(reasons, m.Name+"缺「结束打卡」")
		}
	}
	for _, m := range entry.Members {
		if !m.SgccBound {
			continue
		}
		if !clockinDestOk(ck[itoa(m.MemberID)], dest) {
			reasons = append(reasons, m.Name+"两次打卡地点均不含目的地")
		}
	}
	std := feeStdOf(entry)
	for _, m := range entry.Members {
		f := entry.FeeMap[itoa(m.MemberID)]
		if !feeOk(&f, std) {
			reasons = append(reasons, m.Name+"费用信息未填写或不为 "+feeStdText(std))
		}
	}
	photos := wmPhotos(entry)
	photoNames := map[string]bool{}
	for _, p := range photos {
		for _, n := range p.Members {
			photoNames[n] = true
		}
	}
	missing := make([]string, 0)
	for _, m := range entry.Members {
		if !photoNames[m.Name] {
			missing = append(missing, m.Name)
		}
	}
	if len(missing) > 0 {
		reasons = append(reasons, strings.Join(missing, "、")+"未上传水印照片")
	}
	if len(photos) == 0 {
		return reasons
	}
	for _, p := range photos {
		names := strings.Join(p.Members, "、")
		if names == "" {
			names = "未署名"
		}
		for _, t := range photoIssues(p) {
			reasons = append(reasons, names+"的水印照片"+t)
		}
	}
	allPassed := true
	for _, p := range photos {
		if p.VerifyStatus != "passed" {
			allPassed = false
			break
		}
	}
	if allPassed {
		for _, p := range photos {
			if contentOf(p) == "" {
				names := strings.Join(p.Members, "、")
				if names == "" {
					names = "未署名"
				}
				reasons = append(reasons, names+"的水印照片施工内容为空")
			}
		}
		contents := map[string]bool{}
		for _, p := range photos {
			contents[contentOf(p)] = true
		}
		if len(contents) > 1 {
			reasons = append(reasons, "多张水印照片施工内容不一致")
		}
	}
	return reasons
}

// MyReportReasons 个人口径报告原因（我未打卡 / 我未上传水印照片 / 我的照片未通过 / 我的费用不达标 / 我的打卡地点不含目的地）。
func MyReportReasons(entry *Entry, me MemberRow) []string {
	reasons := make([]string, 0)
	photos := wmPhotos(entry)
	var myRow *MemberRow
	for i := range entry.Members {
		if entry.Members[i].MemberID == me.MemberID {
			myRow = &entry.Members[i]
			break
		}
	}
	dest := strings.TrimSpace(entry.DestinationName)
	if myRow != nil && myRow.SgccBound {
		c := entry.ClockinMap[itoa(me.MemberID)]
		if _, ok := c[1]; !ok {
			reasons = append(reasons, "我缺「开始打卡」")
		}
		if _, ok := c[2]; !ok {
			reasons = append(reasons, "我缺「结束打卡」")
		}
		if !clockinDestOk(c, dest) {
			reasons = append(reasons, "我的两次打卡地点均不含目的地")
		}
	}
	if myRow != nil {
		f := entry.FeeMap[itoa(me.MemberID)]
		if !feeOk(&f, feeStdOf(entry)) {
			reasons = append(reasons, "我的费用信息未填写或不达标")
		}
		hasPhoto := false
		for _, p := range photos {
			for _, n := range p.Members {
				if n == me.Name {
					hasPhoto = true
				}
			}
		}
		if !hasPhoto {
			reasons = append(reasons, "我未上传水印照片")
		}
	}
	for _, p := range photos {
		own := false
		for _, n := range p.Members {
			if n == me.Name {
				own = true
			}
		}
		if !own {
			continue
		}
		for _, t := range photoIssues(p) {
			reasons = append(reasons, "我的水印照片"+t)
		}
	}
	return reasons
}
