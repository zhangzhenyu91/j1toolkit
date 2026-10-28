// 费用照片双向对账（syncOne 照片段）：以商旅费用模板 id=5「上传图片」组件为准——
// 商旅有本地无 → 下载入库（同内容合并人名）；商旅已删 → source=1 整照删除 / source=0 摘除本成员。
// 口径对照 server/src/sgccclockin/index.js syncFeePhotos（勿改）：对象存储 / 图片下载 / 水印识别
// 均可注入（测试用 fake，不触真实 COS / 商旅）。
package sgccclockin

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PhotoVerifier 水印识别回写钩子（异步触发，不阻塞对账；worklog 侧注入其实现）。
type PhotoVerifier interface {
	VerifyAndWriteBack(ctx context.Context, photoID int64, username, date, destination, url string)
}

// Store 腾讯云 COS 风格对象存储（拉下入库 / 删除对账用；nil 时拉下方向不可用，记 fail 日志）。
// 赋值：Module.Store = worklog.ObjectStore 实现（同接口形状）。
type PhotoStore interface {
	PutBuffer(ctx context.Context, key string, buf []byte, contentType string) error
	DeleteObject(ctx context.Context, key string) error
	PublicURL(key string) string
}

// syncPhotoPart 费用照片对账入口（syncOne 照片段）：模板 id=5「上传图片」组件形状守卫后进双向对账。
// 形状守卫：value 不是 JSON 数组（或组件缺失）时跳过对账并留诊断，防止误判「商旅无照片」把本地同步照片删光。
func (m *Module) syncPhotoPart(ctx context.Context, acc *Account, date, scope string, teamID, memberID int64, tpl map[string]any) {
	log := func(typ, result, detail string) {
		m.AddSyncLog(ctx, teamID, memberID, date, scope, typ, result, detail)
	}
	var imgs []any
	comp := findComp(tpl, "5")
	if comp != nil {
		val := strings.TrimSpace(str(comp["value"]))
		if val == "" {
			imgs = []any{} // value 为空 = 当日无费用照片（正常）
		} else {
			var arr []any
			if json.Unmarshal([]byte(val), &arr) == nil {
				imgs = arr
			}
		}
	}
	if imgs == nil {
		// 脱敏诊断：只记组件形态（缺失/空/非 JSON 数组），不输出 value 原文（可能含签名 URL）
		shape := "缺失"
		if comp != nil {
			shape = "value 非 JSON 数组（长度 " + strconv.Itoa(len(strings.TrimSpace(str(comp["value"])))) + "）"
		}
		stdlog.Printf("[商旅打卡] 费用照片对账跳过（成员 %d %s）：comp5 %s", memberID, date, shape)
		log("photo", "fail", "费用照片：上传图片组件形态异常，已跳过对账（详见服务端日志）")
		return
	}
	if len(imgs) > 0 {
		keys := ""
		if row, ok := imgs[0].(map[string]any); ok {
			ks := make([]string, 0, len(row))
			for k := range row {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			keys = "，样例键 " + strings.Join(ks, "/")
		}
		stdlog.Printf("[商旅打卡] 费用照片对账（成员 %d %s）：远端 %d 张%s", memberID, date, len(imgs), keys)
	}
	m.syncFeePhotos(ctx, acc, date, imgs, teamID, memberID, log)
}

// syncFeePhotos 费用照片双向对账（Node syncFeePhotos 同口径）：
//   - 商旅侧集合：图片 id → 地址（键名兼容 parseFeeImageItem）；有数据但识别不出 id → 跳过保护本地；
//   - 方向一 商旅有本地无：下载入库（同 MD5 并入人名，不新建）；单张失败记日志继续；
//   - 方向二 商旅已删：source=1 整照删除，source=0（壹匣上传）摘除本成员链接与人名，摘空后整照删除；
//   - 收尾记「费用照片：拉下 X 张 / 合并 Y 张 / 删除 Z 张 / 摘除 W 人」。
func (m *Module) syncFeePhotos(ctx context.Context, acc *Account, date string, remoteImgs []any,
	teamID, memberID int64, log func(typ, result, detail string)) {
	remote := map[string]string{}
	for _, it := range remoteImgs {
		row, _ := it.(map[string]any)
		img := ParseFeeImageItem(row)
		if img == nil {
			continue
		}
		remote[img.ID] = img.URL
	}
	if len(remoteImgs) > 0 && len(remote) == 0 {
		keys := ""
		if row, ok := remoteImgs[0].(map[string]any); ok {
			ks := make([]string, 0, len(row))
			for k := range row {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			keys = "，样例键 " + strings.Join(ks, "/")
		}
		// 脱敏：只记样例键名，不输出原始图片 JSON（可能含签名 URL）
		stdlog.Printf("[商旅打卡] 费用照片对账跳过（成员 %d %s）：远端 %d 张但无可识别图片 id%s",
			memberID, date, len(remoteImgs), keys)
		log("photo", "fail",
			fmt.Sprintf("费用照片：远端 %d 张但无可识别图片 id（键名异常），已跳过对账（详见服务端日志）", len(remoteImgs)))
		return
	}

	// 本成员名与 user_id（入库标注 / Dify username 代绑兜底）
	memberName, memberUserID := m.memberNameUserID(ctx, memberID)
	memberKey := strconv.FormatInt(memberID, 10)

	// 本地侧：卡片上下文班组当日照片中与本成员相关的行
	type localRow struct {
		id     int64
		cosKey string
		source int
		imgID  string
	}
	var localPulled, localUploaded []localRow
	rows, err := m.DB.QueryContext(ctx,
		`SELECT p.id, p.cos_key, p.source, p.sgcc_img_id
		 FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id
		 WHERE e.team_id = ? AND e.log_date = ?`, teamID, date)
	if err == nil {
		for rows.Next() {
			var p localRow
			var linksRaw sql.NullString
			if rows.Scan(&p.id, &p.cosKey, &p.source, &linksRaw) != nil {
				continue
			}
			links := parseLinks(linksRaw.String)
			imgID := links[memberKey]
			if imgID == "" {
				continue // 与本成员无关
			}
			p.imgID = imgID
			if p.source == 1 {
				localPulled = append(localPulled, p)
			} else {
				localUploaded = append(localUploaded, p)
			}
		}
		rows.Close()
	}
	known := map[string]bool{}
	for _, p := range localPulled {
		known[p.imgID] = true
	}
	for _, p := range localUploaded {
		known[p.imgID] = true
	}

	// 方向一：商旅有本地无 → 下载入库（挂到该成员当日首个出工记录下）
	pulled, merged := 0, 0
	type pullItem struct{ id, url string }
	toPull := make([]pullItem, 0, len(remote))
	for imgID, imgURL := range remote {
		if !known[imgID] {
			toPull = append(toPull, pullItem{id: imgID, url: imgURL})
		}
	}
	if len(toPull) > 0 {
		username := m.usernameOf(ctx, acc.UserID, memberUserID)
		entryID, destName, found := m.firstEntryOf(ctx, teamID, memberID, date)
		switch {
		case !found:
			log("photo", "fail",
				fmt.Sprintf("费用照片：商旅侧 %d 张本地无，但成员当日无出工记录，已跳过入库", len(toPull)))
		case memberName == "":
			log("photo", "fail", fmt.Sprintf("费用照片：成员 %d 不存在，已跳过入库", memberID))
		default:
			teamName := m.teamNameOf(ctx, teamID)
			prefix := cfgValue(ctx, m.Cfg, "COS_WORKLOG_PREFIX")
			if prefix == "" {
				prefix = "worklog/"
			}
			if !strings.HasSuffix(prefix, "/") {
				prefix += "/"
			}
			for _, it := range toPull {
				if err := m.pullFeePhoto(ctx, pullArgs{
					acc: acc, teamID: teamID, date: date, memberID: memberID, memberKey: memberKey,
					memberName: memberName, username: username, entryID: entryID, destName: destName,
					teamName: teamName, prefix: prefix, imgID: it.id, imgURL: it.url,
				}, &pulled, &merged, log); err != nil {
					continue // 单张失败记核查日志继续，不抛出（pullFeePhotos 内已记）
				}
			}
		}
	}

	// 方向二：商旅侧已删 → 本地同步删除（一切以商旅平台为准）
	deleted, unlinked := 0, 0
	for _, p := range localPulled {
		if remote[p.imgID] != "" {
			continue
		}
		if m.Store != nil && p.cosKey != "" {
			_ = m.Store.DeleteObject(ctx, p.cosKey)
		}
		if _, err := m.DB.ExecContext(ctx, `DELETE FROM worklog_photo WHERE id = ?`, p.id); err != nil {
			log("photo", "fail", fmt.Sprintf("费用照片：本地照片 %d（商旅图片 %s）删除失败：%s",
				p.id, p.imgID, truncateStr(err.Error(), 200)))
			continue
		}
		deleted++
	}
	for _, p := range localUploaded {
		if remote[p.imgID] != "" {
			continue
		}
		// 逐行读当前值（members/links 可能被并发改动），仅摘除本成员的链接与人名
		var namesRaw, linksRaw, cosKey string
		if err := m.DB.QueryRowContext(ctx,
			`SELECT members, sgcc_img_id, cos_key FROM worklog_photo WHERE id = ?`, p.id,
		).Scan(&namesRaw, &linksRaw, &cosKey); err != nil {
			continue
		}
		names := parseNameArray(namesRaw)
		links := parseLinks(linksRaw)
		delete(links, memberKey)
		filtered := names[:0]
		for _, n := range names {
			if n != memberName {
				filtered = append(filtered, n)
			}
		}
		if len(filtered) == 0 {
			if m.Store != nil && cosKey != "" {
				_ = m.Store.DeleteObject(ctx, cosKey)
			}
			if _, err := m.DB.ExecContext(ctx, `DELETE FROM worklog_photo WHERE id = ?`, p.id); err != nil {
				log("photo", "fail", fmt.Sprintf("费用照片：本地照片 %d（商旅图片 %s）摘除失败：%s",
					p.id, p.imgID, truncateStr(err.Error(), 200)))
				continue
			}
			deleted++
			continue
		}
		nb, _ := json.Marshal(filtered)
		lb, _ := json.Marshal(links)
		if _, err := m.DB.ExecContext(ctx,
			`UPDATE worklog_photo SET members = ?, sgcc_img_id = ? WHERE id = ?`,
			string(nb), string(lb), p.id); err != nil {
			log("photo", "fail", fmt.Sprintf("费用照片：本地照片 %d（商旅图片 %s）摘除失败：%s",
				p.id, p.imgID, truncateStr(err.Error(), 200)))
			continue
		}
		unlinked++
	}

	result := "ok"
	if pulled > 0 || deleted > 0 || merged > 0 || unlinked > 0 {
		result = "diff"
	}
	log("photo", result, fmt.Sprintf("费用照片：拉下 %d 张 / 合并 %d 张 / 删除 %d 张 / 摘除 %d 人",
		pulled, merged, deleted, unlinked))
}

// pullArgs 方向一单张入库参数集。
type pullArgs struct {
	acc        *Account
	teamID     int64
	date       string
	memberID   int64
	memberKey  string
	memberName string
	username   string
	entryID    int64
	destName   string
	teamName   string
	prefix     string
	imgID      string
	imgURL     string
}

// pullFeePhoto 方向一单张：下载 → 同 MD5 合并 / COS 入库 → 触发水印识别。失败记日志并返回错误（不抛出）。
func (m *Module) pullFeePhoto(ctx context.Context, a pullArgs, pulled, merged *int,
	log func(typ, result, detail string)) error {
	failLog := func(msg string) error {
		log("photo", "fail", fmt.Sprintf("费用照片：商旅图片 %s 入库失败：%s", a.imgID, truncateStr(msg, 200)))
		return errors.New(msg)
	}
	if !strings.HasPrefix(a.imgURL, "http://") && !strings.HasPrefix(a.imgURL, "https://") {
		return failLog("商旅侧未返回有效图片地址")
	}
	buf, err := m.fetchSgccImage(ctx, a.imgURL)
	if err != nil {
		return failLog(err.Error())
	}
	sum := md5.Sum(buf)
	imgMD5 := hex.EncodeToString(sum[:])
	// 相同照片内容合并（一图多人标注）：同班组当日已有同 MD5 照片 → 人名/链接并入，不再新建
	var dupID int64
	var dupNames, dupLinks string
	if err := m.DB.QueryRowContext(ctx,
		`SELECT p.id, p.members, p.sgcc_img_id FROM worklog_photo p
		 JOIN worklog_entry e ON e.id = p.entry_id
		 WHERE e.team_id = ? AND e.log_date = ? AND p.md5 = ? LIMIT 1`,
		a.teamID, a.date, imgMD5).Scan(&dupID, &dupNames, &dupLinks); err == nil && dupID != 0 {
		names := parseNameArray(dupNames)
		has := false
		for _, n := range names {
			if n == a.memberName {
				has = true
				break
			}
		}
		if !has {
			names = append(names, a.memberName)
		}
		links := parseLinks(dupLinks)
		links[a.memberKey] = a.imgID
		nb, _ := json.Marshal(names)
		lb, _ := json.Marshal(links)
		if _, err := m.DB.ExecContext(ctx,
			`UPDATE worklog_photo SET members = ?, sgcc_img_id = ? WHERE id = ?`,
			string(nb), string(lb), dupID); err != nil {
			return failLog(err.Error())
		}
		*merged++
		return nil
	}
	// COS key 规则同 worklog 照片（带图片 id 防同毫秒撞键）
	key := fmt.Sprintf("%s%s/%s/%d-%d-%s.jpg",
		a.prefix, a.teamName, strings.ReplaceAll(a.date, "-", "."), a.entryID, time.Now().UnixMilli(), a.imgID)
	localURL := ""
	if m.Store != nil {
		if err := m.Store.PutBuffer(ctx, key, buf, "image/jpeg"); err != nil {
			return failLog(err.Error())
		}
		localURL = m.Store.PublicURL(key)
	} else {
		return failLog("对象存储未接入，无法入库商旅照片")
	}
	links := map[string]string{a.memberKey: a.imgID}
	names := []string{a.memberName}
	nb, _ := json.Marshal(names)
	lb, _ := json.Marshal(links)
	// 拉下入库即视已同步（照片本就在商旅侧，勿落默认 0 误显「未同步」）
	res, err := m.DB.ExecContext(ctx,
		`INSERT INTO worklog_photo (entry_id, cos_key, url, members, is_watermark, source, sgcc_img_id, sgcc_synced, verify_status, md5)
		 VALUES (?, ?, ?, ?, 1, 1, ?, 1, 'pending', ?)`,
		a.entryID, key, localURL, string(nb), string(lb), imgMD5)
	if err != nil {
		return failLog(err.Error())
	}
	photoID, _ := res.LastInsertId()
	// 异步水印识别并回写（不阻塞对账）
	if m.VerifyPhoto != nil {
		go m.VerifyPhoto.VerifyAndWriteBack(context.Background(), photoID,
			a.username, strings.ReplaceAll(a.date, "-", "."), a.destName, localURL)
	}
	*pulled++
	return nil
}

// memberNameUserID 成员姓名与绑定 user_id（user_id 供 Dify username 代绑兜底）。
func (m *Module) memberNameUserID(ctx context.Context, memberID int64) (string, int64) {
	var name string
	var userID sql.NullInt64
	if err := m.DB.QueryRowContext(ctx,
		`SELECT name, user_id FROM worklog_member WHERE id = ?`, memberID).Scan(&name, &userID); err != nil {
		return "", 0
	}
	return name, userID.Int64
}

// usernameOf 绑定人 username（Dify user 入参；管理员代绑行 account.user_id 为空按成员 user_id 兜底）。
func (m *Module) usernameOf(ctx context.Context, accountUserID, memberUserID int64) string {
	id := accountUserID
	if id == 0 {
		id = memberUserID
	}
	if id == 0 {
		return ""
	}
	var username string
	if err := m.DB.QueryRowContext(ctx,
		`SELECT username FROM sys_user WHERE id = ?`, id).Scan(&username); err != nil {
		return ""
	}
	return username
}

// firstEntryOf 该成员当日所在首个出工记录（照片须挂在记录下）与目的地名。
func (m *Module) firstEntryOf(ctx context.Context, teamID, memberID int64, date string) (int64, string, bool) {
	var entryID int64
	var destName string
	err := m.DB.QueryRowContext(ctx,
		`SELECT e.id, COALESCE(d.name, '')
		 FROM worklog_entry e
		 JOIN worklog_entry_member em ON em.entry_id = e.id
		 LEFT JOIN worklog_destination d ON d.id = e.destination_id
		 WHERE e.team_id = ? AND em.member_id = ? AND e.log_date = ?
		 ORDER BY e.id LIMIT 1`, teamID, memberID, date).Scan(&entryID, &destName)
	if err != nil {
		return 0, "", false
	}
	return entryID, destName, true
}

// teamNameOf 班组名（COS key 用；查不到回退 id 文本）。
func (m *Module) teamNameOf(ctx context.Context, teamID int64) string {
	var name string
	if err := m.DB.QueryRowContext(ctx,
		`SELECT name FROM sys_team WHERE id = ?`, teamID).Scan(&name); err != nil || name == "" {
		return strconv.FormatInt(teamID, 10)
	}
	return name
}

// fetchSgccImage 商旅图片下载（与 API 同出口代理：同一账号各链路 IP 一致防风控；可注入 fake）。
func (m *Module) fetchSgccImage(ctx context.Context, url string) ([]byte, error) {
	if m.FetchSgccImage != nil {
		return m.FetchSgccImage(ctx, url)
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, errors.New("商旅侧未返回有效图片地址")
	}
	client := &http.Client{Timeout: 60 * time.Second}
	if proxyURL := cfgValue(ctx, m.Cfg, CfgProxyURL); proxyURL != "" {
		rt, err := m.photoTransports.get(proxyURL)
		if err != nil {
			return nil, err
		}
		client = &http.Client{Transport: rt, Timeout: 60 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("照片下载失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("照片下载失败（HTTP %d）", resp.StatusCode)
	}
	return readAllLimited(resp.Body)
}

// readAllLimited 读响应体（商旅图片 32MB 上限）。
func readAllLimited(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, 32<<20))
}
