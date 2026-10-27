// 商旅费用照片远端联动（worklog.SgccPhotoRemote / worklog.PhotoOpStarter 实现）：
// COS 回源 → uploadImageV2 直传 → 成员费用模板 id=5「上传图片」组件关联 → 本地 sgcc_img_id/sgcc_synced 收尾；
// 删除场景远端解链先行、成功即时断链落库，本地删除/人名变更由 worklog 侧在全部远端成功后执行。
// 口径对照 server/src/sgccclockin/index.js 的 uploadPhotoToMembersRemote / syncPhotoToSgcc /
// removePhotoMembersRemote / unlinkPhotoFromSgcc（远端先行：任一远端失败即抛错，调用方本地保持不变）。
package sgccclockin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"shade/internal/apps/worklog"
)

// 编译期确认本模块即 worklog 侧注入的商旅照片依赖。
var (
	_ worklog.SgccPhotoRemote = (*Module)(nil)
	_ worklog.PhotoOpStarter  = (*Module)(nil)
	_ worklog.ActivePhotoOp   = (*PhotoOpManager)(nil)
	_ worklog.PhotoOpRec      = opRec{}
)

// PhotoFetcher 照片原文回源函数（COS 公网地址 GET）；nil 用默认 http 客户端（60s 超时）。
// 测试注入 fake / httptest，不访问真实 COS。
type PhotoFetcher func(ctx context.Context, url string) ([]byte, error)

// remoteNamedError 远端先行失败错误：携带失败成员姓名（worklog.failedNameOf 按 FailedName 识别）。
type remoteNamedError struct {
	name string
	msg  string
}

func (e *remoteNamedError) Error() string      { return e.msg }
func (e *remoteNamedError) FailedName() string { return e.name }

// failedNameOf 从远端错误取失败人名（remoteNamedError 携带；无法识别返回空串）。
func failedNameOf(err error) string {
	var ne interface{ FailedName() string }
	if errors.As(err, &ne) {
		return ne.FailedName()
	}
	return ""
}

// opRec 照片任务执行期回调适配（worklog.PhotoOpRec）：写任务字段一律走管理器锁。
type opRec struct {
	m  *PhotoOpManager
	id int64
}

func (r opRec) SetFailedName(name string) { r.m.SetFailedName(r.id, name) }

func (r opRec) SetPhoto(photoID int64, photoURL string) { r.m.SetPhoto(r.id, photoID, photoURL) }

// StartPhotoOp 登记照片任务并入后台串行队列执行（worklog.PhotoOpStarter 口径）。
// 商旅费用组件是 getFeeInfoNew → 改 → saveFeeInfoNew 的 read-modify-write，全局串行防并发互踩
// （同 Node photoOpQueue）；任务启动/完成/失败随 Ops 状态在 /op/status、/sync/active 一致可见。
// 卡片已有进行中任务时返回 0（worklog 侧已先查 ActivePhotoOp 拦截，此处兜底竞态）。
func (m *Module) StartPhotoOp(kind string, teamID, entryID, photoID int64, fn func(ctx context.Context, rec worklog.PhotoOpRec) error) int64 {
	if m.Ops == nil {
		return 0
	}
	op := m.Ops.New(kind, teamID, entryID, photoID)
	if op == nil {
		return 0
	}
	id := op.ID
	m.Ops.enqueueRun(func() {
		m.Ops.Run(id, func(*PhotoOp) error {
			return fn(context.Background(), opRec{m: m.Ops, id: id})
		})
	})
	return id
}

// accountByMember 人级解析绑定账号（member_id 直连优先，缺省经 member.user_id 兜底；人已调班仍可用其账号）。
func (m *Module) accountByMember(ctx context.Context, memberID int64) *Account {
	scan := func(query string, args ...any) *Account {
		var a Account
		var mid, uid, tid sql.NullInt64
		if err := m.DB.QueryRowContext(ctx, query, args...).Scan(&a.ID, &tid, &uid, &mid,
			&a.Mobile, &a.Token, &a.DeviceType, &a.SystemVersion, &a.TokenStatus); err != nil {
			return nil
		}
		a.TeamID, a.UserID, a.MemberID = tid.Int64, uid.Int64, mid.Int64
		return &a
	}
	if a := scan(
		`SELECT id, team_id, user_id, member_id, mobile, token, device_type, system_version, token_status
		 FROM worklog_sgcc_account WHERE member_id = ?`, memberID); a != nil {
		return a
	}
	return scan(
		`SELECT a.id, a.team_id, a.user_id, a.member_id, a.mobile, a.token, a.device_type, a.system_version, a.token_status
		 FROM worklog_sgcc_account a
		 JOIN worklog_member m ON m.id = ?
		 WHERE m.user_id IS NOT NULL AND a.user_id = m.user_id`, memberID)
}

// memberByName 班组内按姓名取出工成员 id。
func (m *Module) memberByName(ctx context.Context, teamID int64, name string) (int64, bool) {
	var id int64
	if err := m.DB.QueryRowContext(ctx,
		`SELECT id FROM worklog_member WHERE team_id = ? AND name = ?`, teamID, name).Scan(&id); err != nil {
		return 0, false
	}
	return id, true
}

// memberName 成员姓名（查不到回退「成员{id}」）。
func (m *Module) memberName(ctx context.Context, memberID int64) string {
	return memberNameOf(ctx, m.DB, memberID)
}

// memberNameOf 成员姓名（查不到回退「成员{id}」）。
func memberNameOf(ctx context.Context, db *sql.DB, memberID int64) string {
	var name string
	if err := db.QueryRowContext(ctx,
		`SELECT name FROM worklog_member WHERE id = ?`, memberID).Scan(&name); err != nil || name == "" {
		return "成员" + strconv.FormatInt(memberID, 10)
	}
	return name
}

// fetchPhoto 照片原文回源（COS 公网地址；默认 60s 超时同 Node AbortSignal.timeout）。
func (m *Module) fetchPhoto(ctx context.Context, url string) ([]byte, error) {
	if m.FetchPhoto != nil {
		return m.FetchPhoto(ctx, url)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("照片回源失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("照片回源失败（HTTP %d）", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// linksJSON 链接表序列化（{worklog_member_id: 商旅图片id}，键为十进制字符串同 Node JSON.stringify）。
func linksJSON(links map[string]string) string {
	b, _ := json.Marshal(links)
	return string(b)
}

// parseLinks sgcc_img_id 反序列化（坏 JSON 回空表）。
func parseLinks(raw string) map[string]string {
	out := map[string]string{}
	if raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// parseNameArray members 列反序列化（JSON 字符串数组；坏 JSON 回空表）。
func parseNameArray(raw string) []string {
	out := []string{}
	if raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// UploadToMembers 把照片上传到各绑定成员的商旅费用照片（worklog 上传远端先行；返回 sgcc_img_id 链接 JSON 串）。
// 未绑定成员跳过不计失败（记核查日志）；登录过期/远端调用失败即抛错（带失败人名），调用方本地零写入。
func (m *Module) UploadToMembers(ctx context.Context, teamID int64, logDate string, names []string, buf []byte) (string, error) {
	links := map[string]string{}
	failedName, err := m.uploadFeePhotosRemote(ctx, teamID, logDate, names, buf, links, "照片", nil)
	if err != nil {
		return linksJSON(links), &remoteNamedError{name: failedName, msg: err.Error()}
	}
	return linksJSON(links), nil
}

// uploadFeePhotosRemote 逐成员远端关联核心（上传新图 / 补传共用）：已链接成员跳过、缺链接自愈；
// 成员级失败即中止并返回（failedName, err）。progress 成功路径逐人回调（可空）。
func (m *Module) uploadFeePhotosRemote(ctx context.Context, teamID int64, logDate string, names []string,
	buf []byte, links map[string]string, photoLabel string, progress func()) (string, error) {
	if m.API == nil {
		return "", errors.New("商旅协议服务未接入")
	}
	for _, name := range names {
		memberID, found := m.memberByName(ctx, teamID, name)
		if !found {
			if progress != nil {
				progress()
			}
			continue
		}
		key := strconv.FormatInt(memberID, 10)
		if links[key] != "" {
			if progress != nil {
				progress()
			}
			continue
		}
		acc := m.accountByMember(ctx, memberID)
		if acc == nil {
			m.AddSyncLog(ctx, teamID, memberID, logDate, "daily", "photo", "fail",
				photoLabel+" 同步跳过：成员「"+name+"」未绑定商旅账号")
			if progress != nil {
				progress()
			}
			continue
		}
		if acc.TokenStatus != 1 {
			m.AddSyncLog(ctx, teamID, memberID, logDate, "daily", "photo", "fail",
				photoLabel+" 同步失败：成员「"+name+"」商旅登录已过期")
			return name, errors.New("商旅登录已过期，请重新登录商旅账号")
		}
		imgID, err := m.linkFeePhoto(ctx, acc, memberID, logDate, buf)
		if err != nil {
			msg := truncateStr(err.Error(), 200)
			m.AddSyncLog(ctx, teamID, memberID, logDate, "daily", "photo", "fail",
				photoLabel+" 同步失败：成员「"+name+"」"+msg)
			return name, errors.New(msg)
		}
		links[key] = imgID
		if progress != nil {
			progress()
		}
	}
	return "", nil
}

// linkFeePhoto 单成员远端关联：uploadImageV2 直传 → 当日费用模板 id=5「上传图片」组件追加 → 整树重存。
// 成本分配兜底同费用保存口径（ensureCostCenter）；任一步失败返回错误（该成员不计链接，本地不落）。
func (m *Module) linkFeePhoto(ctx context.Context, acc *Account, memberID int64, logDate string, buf []byte) (string, error) {
	up, err := m.API.UploadImageV2(ctx, acc.Token, buf, devOpt(acc))
	if err != nil {
		return "", err
	}
	d, err := m.API.GetFeeInfoNew(ctx, acc.Token, m.feeTplParams(ctx, memberID, logDate), devOpt(acc))
	if err != nil {
		return "", err
	}
	tpl := ExtractClockTemplate(d)
	if tpl == nil {
		return "", errors.New("获取费用模板失败")
	}
	m.ensureCostCenter(ctx, tpl, memberID)
	imgs := ParseFeeImageList(findCompValue(tpl, "5"))
	// 元素形状与商旅 App 手工上传一致：{fileInfoId, url}（imgId 即 uploadImageV2 返回的 fileInfoId）
	imgs = append(imgs, map[string]any{"fileInfoId": up.FileInfoID, "url": up.HTTPURL})
	b, _ := json.Marshal(imgs)
	sv, err := m.API.SaveFeeInfoNew(ctx, acc.Token, logDate, tpl, map[string]string{"5": string(b)}, devOpt(acc))
	if err != nil {
		return "", err
	}
	if sv == nil || num(sv["statusCode"]) != 200 {
		return "", errors.New("费用照片关联保存失败")
	}
	return up.FileInfoID, nil
}

// findCompValue 取模板指定 id 组件的 value（组件缺失回 nil）。
func findCompValue(tpl map[string]any, id string) any {
	if comp := findComp(tpl, id); comp != nil {
		return comp["value"]
	}
	return nil
}

// loadPhotoLinks 载入照片的商旅链接与上下文（entry 日志日期/班组）。
func (m *Module) loadPhotoLinks(ctx context.Context, photoID int64) (teamID int64, logDate string, links map[string]string, ok bool) {
	var raw string
	err := m.DB.QueryRowContext(ctx,
		`SELECT p.sgcc_img_id, DATE_FORMAT(e.log_date, '%Y-%m-%d'), e.team_id
		 FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id WHERE p.id = ?`, photoID,
	).Scan(&raw, &logDate, &teamID)
	if err != nil {
		return 0, "", nil, false
	}
	return teamID, logDate, parseLinks(raw), true
}

// savePhotoLinks 落库最新链接（部分成功即时留存，重试只补缺口/只处理剩余链接）。
func (m *Module) savePhotoLinks(ctx context.Context, photoID int64, links map[string]string) {
	_, _ = m.DB.ExecContext(ctx,
		`UPDATE worklog_photo SET sgcc_img_id = ? WHERE id = ?`, linksJSON(links), photoID)
}

// removeFeeImageRemote 从某成员当日商旅费用照片组件移除指定图片（同步；成本分配兜底同费用保存口径）。
// 商旅侧本就没有该图视为成功；识别不出图片 id 的元素一律保留（不误删）。
func (m *Module) removeFeeImageRemote(ctx context.Context, acc *Account, memberID int64, date, imgID string) error {
	if m.API == nil {
		return errors.New("商旅协议服务未接入")
	}
	d, err := m.API.GetFeeInfoNew(ctx, acc.Token, m.feeTplParams(ctx, memberID, date), devOpt(acc))
	if err != nil {
		return err
	}
	tpl := ExtractClockTemplate(d)
	if tpl == nil {
		return errors.New("获取费用模板失败")
	}
	m.ensureCostCenter(ctx, tpl, memberID)
	imgs := ParseFeeImageList(findCompValue(tpl, "5"))
	kept := make([]map[string]any, 0, len(imgs))
	for _, it := range imgs {
		img := ParseFeeImageItem(it)
		if img != nil && img.ID == imgID {
			continue
		}
		kept = append(kept, it)
	}
	if len(kept) == len(imgs) {
		return nil // 商旅侧本就没有该图，视为已删除
	}
	b, _ := json.Marshal(kept)
	sv, err := m.API.SaveFeeInfoNew(ctx, acc.Token, date, tpl, map[string]string{"5": string(b)}, devOpt(acc))
	if err != nil {
		return err
	}
	if sv == nil || num(sv["statusCode"]) != 200 {
		if sv != nil {
			if msg := msgOr(sv, ""); msg != "" {
				return errors.New(msg)
			}
		}
		return errors.New("商旅费用保存失败")
	}
	return nil
}

// unlinkFeeImageOnce 单成员一次远端解链：从该成员当日商旅费用照片移除该图，成功即时断开 links[key]
// 并落库（后续中止不回退，重试仅处理剩余链接）；失败记核查日志并返回错误（本地链接保留防核查回拉复活）。
func (m *Module) unlinkFeeImageOnce(ctx context.Context, photoID, teamID int64, logDate string,
	memberID int64, imgID string, links map[string]string, key string, failDetail func(error) string) error {
	acc := m.accountByMember(ctx, memberID)
	var err error
	switch {
	case acc == nil:
		err = errors.New("未绑定商旅账号")
	case acc.TokenStatus != 1:
		err = errors.New("商旅登录已过期")
	default:
		err = m.removeFeeImageRemote(ctx, acc, memberID, logDate, imgID)
	}
	if err != nil {
		m.AddSyncLog(ctx, teamID, memberID, logDate, "daily", "photo", "fail", failDetail(err))
		return err
	}
	delete(links, key)
	m.savePhotoLinks(ctx, photoID, links)
	return nil
}

// RemoveMembers 把照片从 removed 各人的商旅费用照片剔除（调人名剔除场景；远端先行）。
// 任一成员远端删除失败即中止并抛错（带失败人名），调用方本地人名不变更；
// 已成功成员的链接即时断开落库，未删链接全部保留（重试编辑时由补传链路恢复/继续删）。
func (m *Module) RemoveMembers(ctx context.Context, photoID int64, removed []string) error {
	if len(removed) == 0 {
		return nil
	}
	teamID, logDate, links, ok := m.loadPhotoLinks(ctx, photoID)
	if !ok || len(links) == 0 {
		return nil
	}
	for _, name := range removed {
		memberID, found := m.memberByName(ctx, teamID, name)
		if !found {
			continue
		}
		key := strconv.FormatInt(memberID, 10)
		imgID := links[key]
		if imgID == "" {
			continue // 该成员名下本就没同步成功过，无需远端删除
		}
		err := m.unlinkFeeImageOnce(ctx, photoID, teamID, logDate, memberID, imgID, links, key,
			func(e error) string {
				return fmt.Sprintf("照片 %d 剔除人名「%s」：商旅侧图片删除失败（%s），本地未变更，请重试",
					photoID, name, truncateStr(e.Error(), 120))
			})
		if err != nil {
			return &remoteNamedError{name: name, msg: err.Error()}
		}
	}
	return nil
}

// Unlink 解除照片与全部商旅费用照片的关联（删除场景；远端先行，本地照片删除在全部远端成功后由调用方执行）。
// 任一成员解除失败即中止并抛错（带失败人名）；已成功成员的链接即时断开落库，重试仅处理剩余链接。
func (m *Module) Unlink(ctx context.Context, photoID int64) error {
	teamID, logDate, links, ok := m.loadPhotoLinks(ctx, photoID)
	if !ok || len(links) == 0 {
		return nil
	}
	for key, imgID := range links {
		memberID, _ := strconv.ParseInt(key, 10, 64)
		name := m.memberName(ctx, memberID)
		err := m.unlinkFeeImageOnce(ctx, photoID, teamID, logDate, memberID, imgID, links, key,
			func(e error) string {
				return fmt.Sprintf("照片 %d 删除：解除成员「%s」商旅费用照片失败（%s），本地未删除，请重试",
					photoID, name, truncateStr(e.Error(), 120))
			})
		if err != nil {
			return &remoteNamedError{name: name, msg: err.Error()}
		}
	}
	return nil
}

// SyncMembers 以全量名单补传商旅费用照片关联（已链接成员跳过、缺链接自愈；远端先行）。
func (m *Module) SyncMembers(ctx context.Context, photoID int64, names []string) error {
	return m.syncPhotoToSgcc(ctx, photoID, names, nil)
}

// syncPhotoToSgcc 单张照片同步进所属人名的当日商旅费用照片（COS 回源 → 逐成员 uploadImageV2 → 费用模板同步）。
// targetNames 缺省读库内 members。无论成败都落库最新链接与状态（部分成功的链接即时留存，重试只补缺口；
// sgcc_synced：全部人名处理成功=1，否则=2——失败不假成功）。progress 逐人成功后回调（可空）。
func (m *Module) syncPhotoToSgcc(ctx context.Context, photoID int64, targetNames []string, progress func(done, total int)) error {
	var url, membersRaw, rawLinks string
	var teamID int64
	var logDate string
	err := m.DB.QueryRowContext(ctx,
		`SELECT p.url, p.members, p.sgcc_img_id, DATE_FORMAT(e.log_date, '%Y-%m-%d'), e.team_id
		 FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id WHERE p.id = ?`, photoID,
	).Scan(&url, &membersRaw, &rawLinks, &logDate, &teamID)
	if err != nil {
		return errors.New("照片不存在")
	}
	names := targetNames
	if len(names) == 0 {
		names = parseNameArray(membersRaw)
	}
	links := parseLinks(rawLinks)
	buf, err := m.fetchPhoto(ctx, url) // 图片原文（COS 回源）
	if err != nil {
		return err
	}
	label := "照片 " + strconv.FormatInt(photoID, 10)
	done, total := 0, len(names)
	prog := func() {
		done++
		if progress != nil {
			progress(done, total)
		}
	}
	failedName, runErr := m.uploadFeePhotosRemote(ctx, teamID, logDate, names, buf, links, label, prog)
	// 无论成败都落库最新链接与状态（部分成功的链接保留，重试只补缺口）
	synced := 1
	if runErr != nil {
		synced = 2
	}
	_, _ = m.DB.ExecContext(ctx,
		`UPDATE worklog_photo SET sgcc_img_id = ?, sgcc_synced = ? WHERE id = ?`,
		linksJSON(links), synced, photoID)
	if runErr != nil {
		return &remoteNamedError{name: failedName, msg: runErr.Error()}
	}
	return nil
}
