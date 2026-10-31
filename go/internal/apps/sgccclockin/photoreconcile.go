// 费用照片双向对账（syncOne 照片段）：以商旅费用模板 id=5「上传图片」组件为准——
// 商旅有本地无 → 下载入库（同内容合并人名）；商旅已删 → source=1/sgcc 整照删除 / source=0/app 摘除本成员。
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

	"shade/internal/integrations"
	"shade/internal/logging"
	"shade/internal/logsafe"
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

// sampleKeys 脱敏样例键：升序拼接元素对象的键名（非对象元素回空串）。
// 仅用于诊断日志，不含键值（图片 JSON 可能含签名 URL）。
func sampleKeys(row map[string]any) string {
	if row == nil {
		return ""
	}
	ks := make([]string, 0, len(row))
	for k := range row {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, "/")
}

// syncFeePhotos 费用照片双向对账（Node syncFeePhotos 同口径）：
//   - 商旅侧集合：图片 id → 地址（键名兼容 ParseFeeImageItem）；任一元素识别不出 id → 整轮跳过保护本地；
//   - 方向一 商旅有本地无：下载入库（同 MD5 并入人名，不新建）；单张失败记日志继续；
//   - 方向二 商旅已删：按远端 ID 存在性判断，source=1/sgcc 整照删除，source=0/app（壹匣上传）摘除本成员链接与人名，摘空后整照删除；
//   - 收尾记「费用照片：拉下 X 张 / 合并 Y 张 / 删除 Z 张 / 摘除 W 人 / 失败 N 张」，有失败时收尾必为 fail。
func (m *Module) syncFeePhotos(ctx context.Context, acc *Account, date string, remoteImgs []any,
	teamID, memberID int64, log func(typ, result, detail string)) {
	remote := map[string]string{}
	remoteIDs := map[string]bool{}
	for idx, it := range remoteImgs {
		row, _ := it.(map[string]any)
		img := ParseFeeImageItem(row)
		if img == nil {
			// 任一异常即 fail-closed 整轮跳过：缺 id 元素的本地照片无法判定「商旅已删」，
			// 若只跳过该元素继续对账，会把它当已删而误删本地照片/COS 对象。
			keys := sampleKeys(row)
			if keys == "" {
				keys = "非对象元素"
			}
			// 脱敏：只记样例键名/元素形态，不输出原始图片 JSON（可能含签名 URL）
			stdlog.Printf("[商旅打卡] 费用照片对账跳过（成员 %d %s）：远端第 %d 张（共 %d 张）无可识别图片 id，样例键 %s",
				memberID, date, idx+1, len(remoteImgs), keys)
			log("photo", "fail",
				fmt.Sprintf("费用照片：远端 %d 张中第 %d 张无可识别图片 id（样例键 %s），已跳过整轮对账（详见服务端日志）",
					len(remoteImgs), idx+1, keys))
			return
		}
		remote[img.ID] = img.URL
		remoteIDs[img.ID] = true
	}

	// 本成员名与 user_id（入库标注 / Dify username 代绑兜底）
	memberName, memberUserID := m.memberNameUserID(ctx, memberID)
	memberKey := strconv.FormatInt(memberID, 10)

	// 本地侧：卡片上下文班组当日照片中与本成员相关的行
	type localRow struct {
		id     int64
		cosKey string
		source string
		imgID  string
	}
	var localPulled, localUploaded []localRow
	rows, err := m.DB.QueryContext(ctx,
		`SELECT p.id, p.cos_key, p.source, p.sgcc_img_id
		 FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id
		 WHERE e.team_id = ? AND e.log_date = ?`, teamID, date)
	if err != nil {
		// 查询失败不得伪装成「本地无照片」继续拉下（会重复入库、漏删）
		stdlog.Printf("[商旅打卡] 费用照片对账跳过（成员 %d %s）：本地照片查询失败：%s", memberID, date, err)
		log("photo", "fail", "费用照片：本地照片扫描失败，已跳过对账（详见服务端日志）")
		return
	}
	var scanErr error
	for rows.Next() {
		var p localRow
		var linksRaw sql.NullString
		if err := rows.Scan(&p.id, &p.cosKey, &p.source, &linksRaw); err != nil {
			scanErr = err
			continue
		}
		links := parseLinks(linksRaw.String)
		imgID := links[memberKey]
		if imgID == "" {
			continue // 与本成员无关
		}
		p.imgID = imgID
		// source 列双口径：worklog 新版 schema 为 VARCHAR('app'/'sgcc')，老库 sgccclockin 补列为
		// TINYINT(0/1)；字符串扫描兼容两种列类型，数值 1 与 sgcc 均视为商旅拉取。
		if p.source == "1" || p.source == "sgcc" {
			localPulled = append(localPulled, p)
		} else {
			localUploaded = append(localUploaded, p)
		}
	}
	rows.Close()
	if scanErr == nil {
		scanErr = rows.Err()
	}
	if scanErr != nil {
		stdlog.Printf("[商旅打卡] 费用照片对账跳过（成员 %d %s）：本地照片扫描异常：%s", memberID, date, scanErr)
		log("photo", "fail", "费用照片：本地照片扫描异常，已跳过对账（详见服务端日志）")
		return
	}
	known := map[string]bool{}
	for _, p := range localPulled {
		known[p.imgID] = true
	}
	for _, p := range localUploaded {
		known[p.imgID] = true
	}

	// 方向一：商旅有本地无 → 下载入库（挂到该成员当日首个出工记录下）
	pulled, merged, failed := 0, 0, 0
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
			failed += len(toPull)
			log("photo", "fail",
				fmt.Sprintf("费用照片：商旅侧 %d 张本地无，但成员当日无出工记录，已跳过入库", len(toPull)))
		case memberName == "":
			failed += len(toPull)
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
					failed++ // 单张失败计汇总，不抛出（pullFeePhoto 内已记）
					continue
				}
			}
		}
	}

	// 方向二：商旅侧已删 → 本地同步删除（一切以商旅平台为准；按远端 ID 存在性判断，
	// 不按 URL 非空判断——远端元素同 ID 但 url 为空时本地不得误删）
	deleted, unlinked := 0, 0
	for _, p := range localPulled {
		if remoteIDs[p.imgID] {
			continue
		}
		// 旧扫描链接已不在远端：读最新 members/links，判断共享行是否还有其他有效链接。
		var namesRaw, freshLinks, cosKeyFromRow string
		if err := m.DB.QueryRowContext(ctx,
			`SELECT members, sgcc_img_id, cos_key FROM worklog_photo WHERE id = ?`, p.id,
		).Scan(&namesRaw, &freshLinks, &cosKeyFromRow); err != nil {
			// 读不到最新链接时 fail-closed：不删整行/对象，避免共享行误删。
			failed++
			log("photo", "fail", fmt.Sprintf("费用照片：本地照片 %d（商旅图片 %s）重读失败，已跳过删除：%s",
				p.id, p.imgID, truncateStr(err.Error(), 200)))
			continue
		}
		_ = cosKeyFromRow
		links := parseLinks(freshLinks)
		if cur := links[memberKey]; cur != "" && remoteIDs[cur] {
			continue
		}
		liveOther := false
		for key, id := range links {
			if key != memberKey && remoteIDs[id] {
				liveOther = true
				break
			}
		}
		if liveOther {
			// 其他成员链接仍有效：只摘除本成员的人名与链接，保留行与 COS 对象。
			delete(links, memberKey)
			names := parseNameArray(namesRaw)
			filtered := names[:0]
			for _, n := range names {
				if n != memberName {
					filtered = append(filtered, n)
				}
			}
			nb, _ := json.Marshal(filtered)
			lb, _ := json.Marshal(links)
			if _, err := m.DB.ExecContext(ctx,
				`UPDATE worklog_photo SET members = ?, sgcc_img_id = ? WHERE id = ?`,
				string(nb), string(lb), p.id); err != nil {
				failed++
				log("photo", "fail", fmt.Sprintf("费用照片：本地照片 %d（商旅图片 %s）摘除失败：%s",
					p.id, p.imgID, truncateStr(err.Error(), 200)))
				continue
			}
			unlinked++
			continue
		}
		if m.Store != nil && p.cosKey != "" {
			_ = m.Store.DeleteObject(ctx, p.cosKey)
		}
		if _, err := m.DB.ExecContext(ctx, `DELETE FROM worklog_photo WHERE id = ?`, p.id); err != nil {
			failed++
			log("photo", "fail", fmt.Sprintf("费用照片：本地照片 %d（商旅图片 %s）删除失败：%s",
				p.id, p.imgID, truncateStr(err.Error(), 200)))
			continue
		}
		deleted++
	}
	for _, p := range localUploaded {
		if remoteIDs[p.imgID] {
			continue
		}
		// 旧扫描链接已不在远端：逐行读当前值（members/links 可能被并发改动），
		// 删除判断必须用最新链接——旧 imgID 失效不代表本成员当前链接也失效。
		var namesRaw, linksRaw, cosKey string
		if err := m.DB.QueryRowContext(ctx,
			`SELECT members, sgcc_img_id, cos_key FROM worklog_photo WHERE id = ?`, p.id,
		).Scan(&namesRaw, &linksRaw, &cosKey); err != nil {
			continue
		}
		links := parseLinks(linksRaw)
		if cur := links[memberKey]; cur == "" || remoteIDs[cur] {
			continue
		}
		names := parseNameArray(namesRaw)
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
				failed++
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
			failed++
			log("photo", "fail", fmt.Sprintf("费用照片：本地照片 %d（商旅图片 %s）摘除失败：%s",
				p.id, p.imgID, truncateStr(err.Error(), 200)))
			continue
		}
		unlinked++
	}

	result := "ok"
	if failed > 0 {
		result = "fail" // 单张失败优先：成功/差异状态不得覆盖失败事实
	} else if pulled > 0 || deleted > 0 || merged > 0 || unlinked > 0 {
		result = "diff"
	}
	log("photo", result, fmt.Sprintf("费用照片：拉下 %d 张 / 合并 %d 张 / 删除 %d 张 / 摘除 %d 人 / 失败 %d 张",
		pulled, merged, deleted, unlinked, failed))
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
		msg = logsafe.TextForLog(msg)
		log("photo", "fail", fmt.Sprintf("费用照片：商旅图片 %s 入库失败：%s", a.imgID, truncateStr(msg, 200)))
		return errors.New(msg)
	}
	if !strings.HasPrefix(a.imgURL, "http://") && !strings.HasPrefix(a.imgURL, "https://") {
		return failLog("商旅侧未返回有效图片地址")
	}
	ctx = withPhotoDownloadInfo(ctx, photoDownloadInfo{MemberID: a.memberID, TeamID: a.teamID, ImgID: a.imgID})
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
		// fetchSgccImage 已保证内容为 JPEG/PNG；按实测魔数写 content type（PNG 不再标成 jpeg）
		if err := m.Store.PutBuffer(ctx, key, buf, http.DetectContentType(buf)); err != nil {
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
		go m.VerifyPhoto.VerifyAndWriteBack(logging.WithNewTraceID(context.WithoutCancel(ctx)), photoID,
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

// newSgccImageClient 商旅图片下载客户端：与协议通道同口径禁跟随重定向（网关 302 直接报错，
// 防真实网关响应被藏成第三地址超时），下载上限放宽为 60 秒（大图慢链）。
func newSgccImageClient(rt http.RoundTripper) *http.Client {
	c := newSGCCHTTPClient(rt)
	c.Timeout = 60 * time.Second
	return c
}

// fetchSgccImage 商旅图片下载（可注入 fake）：显式 SGCC_PROXY_URL 时与 API 同出口代理
// （同一账号各链路 IP 一致防风控）；无显式代理时直连且不继承进程环境代理。请求禁默认 UA，
// 3xx 作为错误返回；50MB 上限后只接受 JPEG/PNG 魔数，HTML/空内容不返回。
func (m *Module) fetchSgccImage(ctx context.Context, url string) ([]byte, error) {
	if logging.TraceIDFromContext(ctx) == "" {
		ctx = logging.WithNewTraceID(ctx)
	}
	ctx = integrations.WithServiceName(ctx, "sgcc")
	m.logSgccPhotoDownloadStart(ctx, url)
	if m.FetchSgccImage != nil {
		return m.FetchSgccImage(ctx, url)
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, errors.New("商旅侧未返回有效图片地址")
	}
	// 显式代理与直连都由模块缓存复用：直连不再每张图片新建 Transport/连接池。
	rt, err := m.photoTransports.get(cfgValue(ctx, m.Cfg, CfgProxyURL))
	if err != nil {
		return nil, err
	}
	client := newSgccImageClient(rt)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// 同协议请求口径：显式空 UA 阻止 Go 自动补默认 User-Agent（实测会触发网关 302）
	req.Header["User-Agent"] = []string{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("照片下载失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("照片下载失败（HTTP %d）", resp.StatusCode)
	}
	buf, err := readAllLimited(resp.Body)
	if err != nil {
		return nil, err
	}
	ct := http.DetectContentType(buf)
	if ct != "image/jpeg" && ct != "image/png" {
		return nil, fmt.Errorf("照片内容校验失败：仅接受 JPEG/PNG 图片（实际 %s）", ct)
	}
	return buf, nil
}

// photoDownloadContextKey 商旅照片下载上下文键。
type photoDownloadContextKey struct{}

// photoDownloadInfo 照片下载归属（对账调用链补齐，事件 detail 必有字段）。
type photoDownloadInfo struct {
	MemberID int64
	TeamID   int64
	ImgID    string
}

// withPhotoDownloadInfo 把照片归属写入 ctx。
func withPhotoDownloadInfo(ctx context.Context, info photoDownloadInfo) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, photoDownloadContextKey{}, info)
}

// photoDownloadFromContext 取照片归属（缺失为零值）。
func photoDownloadFromContext(ctx context.Context) (photoDownloadInfo, bool) {
	if ctx == nil {
		return photoDownloadInfo{}, false
	}
	info, ok := ctx.Value(photoDownloadContextKey{}).(photoDownloadInfo)
	return info, ok
}

// logSgccPhotoDownloadStart 在真实/注入下载前记录脱敏完整 URL；签名/userinfo/token 掩码，
// fileInfoId 等非敏感参数保留，detail 固定含 url/member_id/team_id/img_id（缺上下文为零值）。
func (m *Module) logSgccPhotoDownloadStart(ctx context.Context, rawURL string) {
	safe, err := logsafe.URLForLog(rawURL)
	if err != nil {
		safe = logsafe.Mask
	}
	info, _ := photoDownloadFromContext(ctx)
	m.appLog(ctx, "info", "sgcc.photo.download.start", "开始下载商旅费用照片", safe, map[string]any{
		"url":       safe,
		"member_id": info.MemberID,
		"team_id":   info.TeamID,
		"img_id":    info.ImgID,
	})
}

// readAllLimited 读响应体（商旅图片 50MB 上限；多读 1 字节防止静默截断）。
func readAllLimited(r io.Reader) ([]byte, error) {
	buf, err := io.ReadAll(io.LimitReader(r, (50<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(buf) > 50<<20 {
		return nil, fmt.Errorf("照片大小应在 50MB 以内")
	}
	return buf, nil
}
