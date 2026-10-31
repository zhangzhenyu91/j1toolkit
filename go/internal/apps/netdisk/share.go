package netdisk

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// 分享免登录访问：提取码校验走 nd_share 表，验证通过发 2h 访问票据（JWT）；
// 提取码失败限流：每 IP 每分享 10 分钟 10 次（内存口径，重启清零）。

var shareIDRe = regexp.MustCompile(`^[a-f0-9]{10,32}$`)

// ShareRow nd_share 行。
type ShareRow struct {
	ID        int64
	ShareID   string
	UserID    int64
	Creator   string
	BasePath  string
	Paths     []string
	Password  string
	ExpireAt  *time.Time
	Status    int
	CreatedAt time.Time
}

func (m *Module) loadShare(ctx interface {
	Deadline() (deadline time.Time, ok bool)
}, id string) *ShareRow {
	_ = ctx
	sid := strings.TrimSpace(id)
	if len(sid) > 32 || !shareIDRe.MatchString(sid) {
		return nil
	}
	row := &ShareRow{}
	var pathsJSON string
	var expireAt *time.Time
	err := m.DB.QueryRow(`SELECT id, share_id, user_id, creator, base_path, paths, password, expire_at, status, created_at
     FROM nd_share WHERE share_id = ?`, sid).
		Scan(&row.ID, &row.ShareID, &row.UserID, &row.Creator, &row.BasePath, &pathsJSON, &row.Password, &expireAt, &row.Status, &row.CreatedAt)
	if err != nil {
		return nil
	}
	row.ExpireAt = expireAt
	_ = json.Unmarshal([]byte(pathsJSON), &row.Paths)
	if row.Paths == nil {
		row.Paths = []string{}
	}
	return row
}

// shareUsable 状态判定：停用 / 过期 / 生效（未通过时直接响应并返回 false）。
func shareUsable(w http.ResponseWriter, row *ShareRow) bool {
	if row == nil {
		writeFail(w, http.StatusNotFound, codeNotFound, "分享不存在或已删除", nil)
		return false
	}
	if row.Status != 1 {
		writeFail(w, http.StatusForbidden, codeShareDisabled, "分享已停用", nil)
		return false
	}
	if row.ExpireAt != nil && row.ExpireAt.Before(time.Now()) {
		writeFail(w, http.StatusGone, codeShareExpired, "分享已过期", nil)
		return false
	}
	return true
}

// shareState 状态派生：停用 > 过期 > 生效。
func shareState(row *ShareRow) string {
	if row.Status != 1 {
		return "disabled"
	}
	if row.ExpireAt != nil && row.ExpireAt.Before(time.Now()) {
		return "expired"
	}
	return "active"
}

// 提取码失败限流表（内存口径）。
const (
	sharePWWindow      = 10 * time.Minute // 失败计数窗口
	sharePWMaxPerIP    = 10               // 同 IP 同分享窗口内允许的失败次数
	sharePWMaxPerShare = 100              // 同一分享全局限额（防伪造 XFF 轮换无限枚举）
	shareIPFailsMax    = 5000             // IP+share 表上限（超出按最旧项淘汰，不整表清空）
	shareIPFailsTarget = 4000             // 淘汰目标（一次批量淘汰，摊薄扫描成本）
)

type pwRec struct {
	count   int
	resetAt time.Time
}

var (
	shareLimitMu  sync.Mutex
	shareIPFails  = map[string]pwRec{} // key = ip|share
	shareAllFails = map[string]pwRec{} // key = share（全域额度，防伪造 XFF 轮换）
)

// shareLimited 是否已触发限流：IP+share 与 share 全域任一超限即拒绝（只查询，不计数）。
func shareLimited(ip, sid string) bool {
	shareLimitMu.Lock()
	defer shareLimitMu.Unlock()
	now := time.Now()
	pruneShareFails(shareIPFails, now)
	pruneShareFails(shareAllFails, now)
	return shareOver(shareIPFails[ip+"|"+sid], now, sharePWMaxPerIP) ||
		shareOver(shareAllFails[sid], now, sharePWMaxPerShare)
}

func shareOver(rec pwRec, now time.Time, max int) bool {
	return rec.count >= max && now.Before(rec.resetAt)
}

func pruneShareFails(m map[string]pwRec, now time.Time) {
	for key, rec := range m {
		if !now.Before(rec.resetAt) {
			delete(m, key)
		}
	}
}

// shareFailAttempt 原子记录一次口令失败并返回是否已触发限流：在同一临界区内复查
// IP+share 与 share 全域额度，未超限才累加；超限立即返回 true（不累加）。
// 这样并发失败最多只累计到上限，不会因「先检查后累加」的竞态多记。
func shareFailAttempt(ip, sid string) bool {
	shareLimitMu.Lock()
	defer shareLimitMu.Unlock()
	now := time.Now()
	pruneShareFails(shareIPFails, now)
	pruneShareFails(shareAllFails, now)
	if shareOver(shareIPFails[ip+"|"+sid], now, sharePWMaxPerIP) ||
		shareOver(shareAllFails[sid], now, sharePWMaxPerShare) {
		return true
	}
	bumpShareFail(shareIPFails, ip+"|"+sid, now)
	bumpShareFail(shareAllFails, sid, now)
	trimShareFails(shareIPFails, shareIPFailsMax, shareIPFailsTarget)
	return false
}

// trimShareFails 表超上限时按 resetAt 最旧（最早过期）逐项淘汰到 target，保留近期合法计数；
// 不整表清空，避免攻击者用大量伪造键重置限流额度。
func trimShareFails(m map[string]pwRec, max, target int) {
	if len(m) <= max {
		return
	}
	if target < 0 {
		target = 0
	}
	for len(m) > target {
		var oldestKey string
		var oldest time.Time
		for k, rec := range m {
			if oldestKey == "" || rec.resetAt.Before(oldest) {
				oldestKey, oldest = k, rec.resetAt
			}
		}
		delete(m, oldestKey)
	}
}

func bumpShareFail(m map[string]pwRec, key string, now time.Time) {
	rec, ok := m[key]
	if !ok || !now.Before(rec.resetAt) {
		m[key] = pwRec{count: 1, resetAt: now.Add(sharePWWindow)}
		return
	}
	rec.count++
	m[key] = rec
}

// shareFailClear 口令/票据通过：只清该 IP+share 计数；全域窗口保持累计，
// 成功请求不能替攻击者续上分享全域额度。
func shareFailClear(ip, sid string) {
	shareLimitMu.Lock()
	defer shareLimitMu.Unlock()
	delete(shareIPFails, ip+"|"+sid)
}

// shareAuthFail 口令失败统一出口：原子记录失败；若已触发限流则 429，否则 403 40311。
func shareAuthFail(w http.ResponseWriter, ip, sid string) {
	if shareFailAttempt(ip, sid) {
		shareAuthLimited(w)
		return
	}
	writeFail(w, http.StatusForbidden, codeBadTicket, "提取码错误", nil)
}

// shareAuthLimited 统一限流出口：应答 429 42901（verify/list/download 同码同文案）。
func shareAuthLimited(w http.ResponseWriter) {
	writeFail(w, http.StatusTooManyRequests, codeRateLimited, "尝试次数过多，请 10 分钟后再试", nil)
}

// shareAuth 访问凭据：ticket（verify 签发的 2h JWT）或 password（逐次校验）。
// body 由调用方一次性解析后传入（Node req.body 单次解析口径；避免二次读流丢参数）。
func (m *Module) shareAuth(r *http.Request, row *ShareRow, body map[string]json.RawMessage) bool {
	tk := strings.TrimSpace(r.URL.Query().Get("ticket"))
	if tk == "" {
		tk = rawString(body["ticket"])
	}
	if tk != "" {
		if claims := m.verifyTicket(tk); claims != nil && claimsString(claims, "sid") == row.ShareID {
			return true
		}
	}
	pw := strings.TrimSpace(r.URL.Query().Get("password"))
	if pw == "" {
		pw = rawString(body["password"])
	}
	return pw != "" && pw == row.Password
}

// resolveShareAbs 越界校验：rel 须为某个分享项本身或其子路径，返回 OpenList 绝对路径。
func resolveShareAbs(row *ShareRow, rel string) string {
	for _, p := range row.Paths {
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return JoinPath(row.BasePath, rel)
		}
	}
	return ""
}

// shareRootItems 首层内容：逐项取元信息（已被移动/删除的项跳过）。
func (m *Module) shareRootItems(r *http.Request, ol OpenListClient, row *ShareRow) []map[string]any {
	items := make([]map[string]any, 0)
	for _, p := range row.Paths {
		data, err := ol.Call(r.Context(), http.MethodPost, "/api/fs/get", CallOpts{Body: map[string]any{"path": JoinPath(row.BasePath, p), "password": ""}})
		if err != nil {
			continue
		}
		var meta mapItemSrc
		if json.Unmarshal(data, &meta) == nil {
			it := mapItem(meta)
			it["rel"] = p
			items = append(items, it)
		}
	}
	return items
}

// handleShareInfo GET /public/share/{id}/：分享信息（不含内容）。
func (m *Module) handleShareInfo(w http.ResponseWriter, r *http.Request) {
	row := m.loadShare(r.Context(), chiURLParam(r, "id"))
	if !shareUsable(w, row) {
		return
	}
	writeOK(w, map[string]any{"creator": row.Creator, "expire_at": row.ExpireAt, "created_at": row.CreatedAt}, "")
}

// handleShareVerify POST /public/share/{id}/verify：提取码验证，通过发 2h 票据并返回首层内容。
func (m *Module) handleShareVerify(w http.ResponseWriter, r *http.Request) {
	row := m.loadShare(r.Context(), chiURLParam(r, "id"))
	if !shareUsable(w, row) {
		return
	}
	ip := clientIP(r)
	if shareLimited(ip, row.ShareID) {
		shareAuthLimited(w)
		return
	}
	var body map[string]json.RawMessage
	_ = decodeBody(r, &body)
	pw := rawString(body["password"])
	if pw != row.Password {
		shareAuthFail(w, ip, row.ShareID)
		return
	}
	shareFailClear(ip, row.ShareID)
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	ticket := m.signTicket(jwt.MapClaims{"sid": row.ShareID}, 2*time.Hour)
	writeOK(w, map[string]any{"ticket": ticket, "items": m.shareRootItems(r, ol, row)}, "")
}

// handleShareList POST /public/share/{id}/list：目录浏览（分享项为文件夹时可下钻）。
func (m *Module) handleShareList(w http.ResponseWriter, r *http.Request) {
	row := m.loadShare(r.Context(), chiURLParam(r, "id"))
	if !shareUsable(w, row) {
		return
	}
	ip := clientIP(r)
	if shareLimited(ip, row.ShareID) {
		shareAuthLimited(w)
		return
	}
	var body map[string]json.RawMessage
	_ = decodeBody(r, &body)
	if !m.shareAuth(r, row, body) {
		shareAuthFail(w, ip, row.ShareID)
		return
	}
	shareFailClear(ip, row.ShareID)
	rel, ok := NormRel(rawString(body["path"]))
	if !ok {
		writeFail(w, http.StatusBadRequest, codeParam, "路径不合法", nil)
		return
	}
	ol, okc := m.requireOL(w)
	if !okc {
		return
	}
	if rel == "/" {
		writeOK(w, map[string]any{"path": "/", "items": m.shareRootItems(r, ol, row)}, "")
		return
	}
	abs := resolveShareAbs(row, rel)
	if abs == "" {
		writeFail(w, http.StatusForbidden, codeOutOfShare, "路径超出分享范围", nil)
		return
	}
	data, err := ol.Call(r.Context(), http.MethodPost, "/api/fs/list", CallOpts{
		Body: map[string]any{"path": abs, "password": "", "page": 1, "per_page": 0, "refresh": false},
	})
	if err != nil {
		relayFail(w, err)
		return
	}
	items := make([]map[string]any, 0)
	for _, it := range parseItems(data) {
		items = append(items, mapItem(it))
	}
	writeOK(w, map[string]any{"path": rel, "items": items}, "")
}

// handleShareDownload GET /public/share/{id}/download：文件下载（流式中转）。
func (m *Module) handleShareDownload(w http.ResponseWriter, r *http.Request) {
	row := m.loadShare(r.Context(), chiURLParam(r, "id"))
	if !shareUsable(w, row) {
		return
	}
	ip := clientIP(r)
	if shareLimited(ip, row.ShareID) {
		shareAuthLimited(w)
		return
	}
	var body map[string]json.RawMessage
	_ = decodeBody(r, &body)
	if !m.shareAuth(r, row, body) {
		shareAuthFail(w, ip, row.ShareID)
		return
	}
	shareFailClear(ip, row.ShareID)
	rel, ok := NormRel(r.URL.Query().Get("path"))
	if !ok || rel == "/" {
		writeFail(w, http.StatusBadRequest, codeParam, "路径不合法", nil)
		return
	}
	abs := resolveShareAbs(row, rel)
	if abs == "" {
		writeFail(w, http.StatusForbidden, codeOutOfShare, "路径超出分享范围", nil)
		return
	}
	ol, okc := m.requireOL(w)
	if !okc {
		return
	}
	m.pipeDownload(w, r, ol, abs, BaseName(rel), "attachment")
}

// ---- 登录态分享管理 ----

// canManagePublic 分享管理专用判定（查看全部 / 停用删除他人分享）。
func canManagePublic(ident interface{ GetRole() string }) bool {
	return ident.GetRole() == "admin" || ident.GetRole() == "team_admin"
}

// mapShare 分享行 → 列表条目。
func mapShare(row *ShareRow, mine bool) map[string]any {
	items := make([]string, 0, len(row.Paths))
	for _, p := range row.Paths {
		items = append(items, BaseName(p))
	}
	return map[string]any{
		"share_id":   row.ShareID,
		"items":      items,
		"paths":      row.Paths,
		"password":   row.Password,
		"creator":    row.Creator,
		"mine":       mine,
		"expire_at":  row.ExpireAt,
		"state":      shareState(row),
		"created_at": row.CreatedAt,
	}
}

// handleSharesList GET /shares：分享列表（scope=mine 默认；scope=all 仅班管/超管）。
func (m *Module) handleSharesList(w http.ResponseWriter, r *http.Request) {
	ident, _ := identityFrom(r.Context())
	scope := "mine"
	if r.URL.Query().Get("scope") == "all" {
		scope = "all"
	}
	if scope == "all" && !canManagePublic(httpapiRoleAdapter(ident.Role)) {
		writeFail(w, http.StatusForbidden, codeForbidden, "仅班管/超管可查看全部分享", nil)
		return
	}
	var rows *sqlRows
	_ = rows
	list := make([]map[string]any, 0)
	q := `SELECT id, share_id, user_id, creator, base_path, paths, password, expire_at, status, created_at FROM nd_share ORDER BY id DESC LIMIT 500`
	args := []any{}
	if scope == "mine" {
		q = `SELECT id, share_id, user_id, creator, base_path, paths, password, expire_at, status, created_at FROM nd_share WHERE user_id = ? ORDER BY id DESC LIMIT 500`
		args = append(args, ident.ID)
	}
	rs, err := m.DB.QueryContext(r.Context(), q, args...)
	if err != nil {
		relayFail(w, err)
		return
	}
	defer rs.Close()
	for rs.Next() {
		row := &ShareRow{}
		var pathsJSON string
		var expireAt *time.Time
		if err := rs.Scan(&row.ID, &row.ShareID, &row.UserID, &row.Creator, &row.BasePath, &pathsJSON, &row.Password, &expireAt, &row.Status, &row.CreatedAt); err != nil {
			continue
		}
		row.ExpireAt = expireAt
		_ = json.Unmarshal([]byte(pathsJSON), &row.Paths)
		list = append(list, mapShare(row, row.UserID == ident.ID))
	}
	writeOK(w, list, "")
}

// handleShareCreate POST /shares：创建分享（paths 相对当前空间根，逐项校验存在性；提取码 4 位字母数字）。
func (m *Module) handleShareCreate(w http.ResponseWriter, r *http.Request) {
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	ident, _ := identityFrom(r.Context())
	body, err := decodeBodyMap(r)
	if err != nil {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误", nil)
		return
	}
	root, ok := m.resolveRoot(w, r, rawString(body["space"]))
	if !ok {
		return
	}
	var rawPaths []string
	if raw, exists := body["paths"]; exists {
		_ = json.Unmarshal(raw, &rawPaths)
	}
	if len(rawPaths) == 0 || len(rawPaths) > 20 {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误：paths（1-20 条）", nil)
		return
	}
	password := strings.ToUpper(strings.TrimSpace(rawString(body["password"])))
	if !regexp.MustCompile(`^[A-Z0-9]{4}$`).MatchString(password) {
		writeFail(w, http.StatusBadRequest, codeParam, "提取码须为 4 位字母或数字", nil)
		return
	}
	days := 0
	if v := rawString(body["expire_days"]); v != "" {
		if n, conv := AtoiLenient(v); conv {
			days = n
		}
	}
	if days < 0 || days > 365 {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误：expire_days", nil)
		return
	}
	rels := make([]string, 0, len(rawPaths))
	for _, p := range rawPaths {
		rel, ok := NormRel(p)
		if !ok || rel == "/" {
			writeFail(w, http.StatusBadRequest, codeParam, "路径不合法", nil)
			return
		}
		rels = append(rels, rel)
	}
	// 逐项校验存在（防止已移动/删除的文件进入分享）。
	for _, rel := range rels {
		if _, err := ol.Call(r.Context(), http.MethodPost, "/api/fs/get", CallOpts{Body: map[string]any{"path": JoinPath(root, rel), "password": ""}}); err != nil {
			relayFail(w, err)
			return
		}
	}
	var expireAt any
	if days > 0 {
		expireAt = time.Now().Add(time.Duration(days) * 24 * time.Hour)
	}
	creator := ident.Nickname
	if creator == "" {
		creator = ident.Username
	}
	// share_id 冲突重试（唯一键兜底）。
	for attempt := 0; attempt < 3; attempt++ {
		shareID := randomShareID()
		pathsJSON, _ := json.Marshal(rels)
		_, err := m.DB.ExecContext(r.Context(),
			`INSERT INTO nd_share (share_id, user_id, creator, base_path, paths, password, expire_at, status)
       VALUES (?, ?, ?, ?, ?, ?, ?, 1)`,
			shareID, ident.ID, creator, root, string(pathsJSON), password, expireAt)
		if err != nil {
			if strings.Contains(err.Error(), "Duplicate") && attempt < 2 {
				continue
			}
			relayFail(w, err)
			return
		}
		writeOK(w, map[string]any{"share_id": shareID, "expire_at": expireAt}, "已创建分享")
		return
	}
	writeFail(w, http.StatusInternalServerError, codeInternal, "分享标识生成失败，请重试", nil)
}

// loadOwnedShare 停用/启用/删除共用装载与权限校验（本人或班管/超管）。
func (m *Module) loadOwnedShare(w http.ResponseWriter, r *http.Request) *ShareRow {
	ident, _ := identityFrom(r.Context())
	sid := strings.TrimSpace(chiURLParam(r, "id"))
	if !shareIDRe.MatchString(sid) {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误：id", nil)
		return nil
	}
	row := m.loadShare(r.Context(), sid)
	if row == nil {
		writeFail(w, http.StatusNotFound, codeNotFound, "分享不存在", nil)
		return nil
	}
	if row.UserID != ident.ID && !canManagePublic(httpapiRoleAdapter(ident.Role)) {
		writeFail(w, http.StatusForbidden, codeForbidden, "仅创建人或班管/超管可操作", nil)
		return nil
	}
	return row
}

func (m *Module) handleShareDisable(w http.ResponseWriter, r *http.Request) {
	row := m.loadOwnedShare(w, r)
	if row == nil {
		return
	}
	if _, err := m.DB.ExecContext(r.Context(), `UPDATE nd_share SET status = 0 WHERE id = ?`, row.ID); err != nil {
		relayFail(w, err)
		return
	}
	writeOK(w, nil, "已停用")
}

func (m *Module) handleShareEnable(w http.ResponseWriter, r *http.Request) {
	row := m.loadOwnedShare(w, r)
	if row == nil {
		return
	}
	if shareState(row) == "expired" {
		writeFail(w, http.StatusBadRequest, codeNoTeam, "分享已过期，请重新创建", nil)
		return
	}
	if _, err := m.DB.ExecContext(r.Context(), `UPDATE nd_share SET status = 1 WHERE id = ?`, row.ID); err != nil {
		relayFail(w, err)
		return
	}
	writeOK(w, nil, "已重新启用")
}

func (m *Module) handleShareDelete(w http.ResponseWriter, r *http.Request) {
	row := m.loadOwnedShare(w, r)
	if row == nil {
		return
	}
	if _, err := m.DB.ExecContext(r.Context(), `DELETE FROM nd_share WHERE id = ?`, row.ID); err != nil {
		relayFail(w, err)
		return
	}
	writeOK(w, nil, "已删除")
}

func randomShareID() string {
	buf := make([]byte, 5)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// clientIP 客户端 IP（Node trust proxy 'loopback' 同口径：来自回环的反代信任 X-Forwarded-For 首项，
// 提取码失败限流按真实客户端 IP 计）。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if isLoopbackHost(host) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
				return first
			}
		}
	}
	return host
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type sqlRows struct{}

type roleAdapter string

func (a roleAdapter) GetRole() string { return string(a) }

func httpapiRoleAdapter(role string) roleAdapter { return roleAdapter(role) }
