package netdisk

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

// handleSpaces GET /spaces：空间列表（个人空间恒有；公共区按班组隔离，未分配班组不下发）。
func (m *Module) handleSpaces(w http.ResponseWriter, r *http.Request) {
	ident, _ := identityFrom(r.Context())
	spaces := []map[string]any{{"key": "my", "name": "我的空间", "manageable": true}}
	if team := m.teamNameOf(r.Context(), ident); team != "" {
		spaces = append(spaces, map[string]any{"key": "public", "name": "公共区", "team": team, "manageable": true})
	}
	writeOK(w, map[string]any{"max_upload_mb": m.maxUploadBytes() >> 20, "spaces": spaces}, "")
}

// handleList POST /list：目录列表（根目录自愈式创建；refresh=true 强制驱动回源）。
func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	var body struct {
		Space   string `json:"space"`
		Path    string `json:"path"`
		Refresh bool   `json:"refresh"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误", nil)
		return
	}
	root, ok := m.resolveRoot(w, r, body.Space)
	if !ok {
		return
	}
	rel, ok := NormRel(body.Path)
	if !ok {
		writeFail(w, http.StatusBadRequest, codeParam, "路径不合法", nil)
		return
	}
	if rel == "/" {
		_ = ol.EnsureDir(r.Context(), root)
	}
	data, err := ol.Call(r.Context(), http.MethodPost, "/api/fs/list", CallOpts{
		Body: map[string]any{"path": JoinPath(root, rel), "password": "", "page": 1, "per_page": 0, "refresh": body.Refresh},
	})
	if err != nil {
		relayFail(w, err)
		return
	}
	items := make([]map[string]any, 0)
	for _, it := range parseItems(data) {
		items = append(items, mapItem(it))
	}
	writeOK(w, map[string]any{"space": body.Space, "path": rel, "manageable": true, "items": items}, "")
}

// handleSearch POST /search：文件名搜索（范围限定当前空间根）。
func (m *Module) handleSearch(w http.ResponseWriter, r *http.Request) {
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	var body struct {
		Space    string `json:"space"`
		Keywords string `json:"keywords"`
		Page     int    `json:"page"`
		PerPage  int    `json:"per_page"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误", nil)
		return
	}
	root, ok := m.resolveRoot(w, r, body.Space)
	if !ok {
		return
	}
	keywords := strings.TrimSpace(body.Keywords)
	if len(keywords) > 100 {
		keywords = keywords[:100]
	}
	if keywords == "" {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误：keywords", nil)
		return
	}
	page := body.Page
	if page < 1 {
		page = 1
	}
	perPage := body.PerPage
	if perPage < 1 {
		perPage = 30
	}
	if perPage > 100 {
		perPage = 100
	}
	data, err := ol.Call(r.Context(), http.MethodPost, "/api/fs/search", CallOpts{
		Body: map[string]any{"parent": root, "keywords": keywords, "scope": 0, "page": page, "per_page": perPage},
	})
	if err != nil {
		relayFail(w, err)
		return
	}
	var wrap struct {
		Content []struct {
			mapItemSrc
			Parent string `json:"parent"`
		} `json:"content"`
		Total int64 `json:"total"`
	}
	_ = json.Unmarshal(data, &wrap)
	items := make([]map[string]any, 0)
	for _, o := range wrap.Content {
		it := mapItem(o.mapItemSrc)
		parent := o.Parent
		if parent == "" {
			parent = root
		}
		if parent == root {
			it["parent"] = "/"
		} else {
			it["parent"] = strings.TrimPrefix(parent, root)
			if it["parent"] == "" {
				it["parent"] = "/"
			}
		}
		items = append(items, it)
	}
	writeOK(w, map[string]any{"items": items, "total": wrap.Total, "page": page, "per_page": perPage}, "")
}

// handleDirSize GET /dirsize：递归统计 {size, files, dirs, truncated}。
func (m *Module) handleDirSize(w http.ResponseWriter, r *http.Request) {
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	root, ok := m.resolveRoot(w, r, r.URL.Query().Get("space"))
	if !ok {
		return
	}
	rel, ok := NormRel(r.URL.Query().Get("path"))
	if !ok || rel == "/" {
		writeFail(w, http.StatusBadRequest, codeParam, "路径不合法", nil)
		return
	}
	abs := JoinPath(root, rel)
	meta, err := ol.Call(r.Context(), http.MethodPost, "/api/fs/get", CallOpts{Body: map[string]any{"path": abs, "password": ""}})
	if err != nil {
		relayFail(w, err)
		return
	}
	var metaObj struct {
		IsDir bool `json:"is_dir"`
	}
	_ = json.Unmarshal(meta, &metaObj)
	if !metaObj.IsDir {
		writeFail(w, http.StatusBadRequest, codeParam, "该路径不是文件夹", nil)
		return
	}
	stat, err := m.dirSizeStat(r.Context(), ol, abs)
	if err != nil && stat == nil {
		if r.Context().Err() != nil {
			return // 客户端已断开，无需回写
		}
		if errors.Is(err, errDirSizeRateLimited) {
			writeFail(w, http.StatusTooManyRequests, codeRateLimited, errDirSizeRateLimited.Error(), nil)
			return
		}
		relayFail(w, err)
		return
	}
	writeOK(w, stat, "")
}

const (
	dirSizeTTL     = 5 * time.Minute  // 缓存有效期
	dirSizeMaxKeys = 128              // 缓存条数上限
	dirSizeTimeout = 35 * time.Second // leader 独立预算（脱离请求取消）

	// dirSizeGlobalConcurrency 全局 leader 遍历并发预算（防止多名用户同时递归上游）。
	dirSizeGlobalConcurrency = 2
	// dirSizeUserLimit / dirSizeUserWindow 单用户 leader 计算频率预算（缓存命中不计）。
	dirSizeUserLimit  = 20
	dirSizeUserWindow = time.Minute
	// thumbGenTimeout 缩略图下载+生成总预算。
	thumbGenTimeout = 60 * time.Second
)

// errDirSizeRateLimited 用户 leader 计算超出频率预算（路由映射 429）。
var errDirSizeRateLimited = errors.New("目录统计请求过于频繁，请稍后再试")

// dirSizeCache dirsize 短缓存与同 key 单飞（零值可用：映射按需创建）。
type dirSizeCache struct {
	mu      sync.Mutex
	entries map[string]dirSizeEntry
	flights map[string]*dirSizeFlight
	gen     uint64 // 失效代数：写操作清空后，旧 leader 不回填过期结果
}

type dirSizeEntry struct {
	at   time.Time
	stat map[string]any
}

type dirSizeFlight struct {
	gen  uint64 // 创建时代数：invalidate 后的新请求不得 join 旧 flight
	done chan struct{}
	stat map[string]any
	err  error
}

// errDirSizeAborted leader 遍历 panic 时的中止错误：等待者立即释放且不复用半成品。
var errDirSizeAborted = errors.New("目录统计计算中断")

func cloneDirSize(stat map[string]any) map[string]any {
	if stat == nil {
		return nil
	}
	out := make(map[string]any, len(stat))
	for k, v := range stat {
		out[k] = v
	}
	return out
}

// lookupLocked 读缓存（调用方持锁）。
func (c *dirSizeCache) lookupLocked(abs string, now time.Time) (map[string]any, bool) {
	e, ok := c.entries[abs]
	if !ok || now.Sub(e.at) >= dirSizeTTL {
		return nil, false
	}
	return cloneDirSize(e.stat), true
}

// storeLocked 写缓存并维持 128 条上限（调用方持锁）。
func (c *dirSizeCache) storeLocked(abs string, stat map[string]any, now time.Time) {
	if c.entries == nil {
		c.entries = map[string]dirSizeEntry{}
	}
	if _, exists := c.entries[abs]; !exists && len(c.entries) >= dirSizeMaxKeys {
		for k, e := range c.entries {
			if now.Sub(e.at) >= dirSizeTTL {
				delete(c.entries, k)
			}
		}
		for len(c.entries) >= dirSizeMaxKeys {
			oldestKey := ""
			var oldest time.Time
			for k, e := range c.entries {
				if oldestKey == "" || e.at.Before(oldest) {
					oldestKey, oldest = k, e.at
				}
			}
			delete(c.entries, oldestKey)
		}
	}
	c.entries[abs] = dirSizeEntry{at: now, stat: cloneDirSize(stat)}
}

// invalidate 清空缓存并推进代数（写操作成功后收口调用）。
func (c *dirSizeCache) invalidate() {
	c.mu.Lock()
	c.entries = nil
	c.gen++
	c.mu.Unlock()
}

// allowDirSizeLocked 单用户 leader 频率预算（调用方持 dirSize.mu）。
func (m *Module) allowDirSizeLocked(userID int64, now time.Time) bool {
	if m.dirSizeRate == nil {
		m.dirSizeRate = map[int64][]time.Time{}
	}
	if len(m.dirSizeRate) > 64 {
		// 回收窗口内无任何记录的过期用户，避免 map 随用户数无界增长。
		for k, v := range m.dirSizeRate {
			latest := time.Time{}
			for _, t := range v {
				if t.After(latest) {
					latest = t
				}
			}
			if latest.IsZero() || now.Sub(latest) >= dirSizeUserWindow {
				delete(m.dirSizeRate, k)
			}
		}
	}
	times := m.dirSizeRate[userID]
	keep := times[:0]
	for _, t := range times {
		if now.Sub(t) < dirSizeUserWindow {
			keep = append(keep, t)
		}
	}
	if len(keep) >= dirSizeUserLimit {
		m.dirSizeRate[userID] = keep
		return false
	}
	m.dirSizeRate[userID] = append(keep, now)
	return true
}

// dirSizeStat 取目录统计：缓存命中直出；同 key 并发只由一个 leader 遍历上游，
// leader 用 WithoutCancel + 35s 独立预算，避免客户端断开把共享计算掐断。
// 上游失败时不写缓存（返回已统计的部分，沿用旧响应口径）。
func (m *Module) dirSizeStat(ctx context.Context, ol OpenListClient, abs string) (map[string]any, error) {
	now := time.Now()
	m.dirSize.mu.Lock()
	if stat, ok := m.dirSize.lookupLocked(abs, now); ok {
		m.dirSize.mu.Unlock()
		return stat, nil
	}
	// 只 join 同代 flight：写操作 invalidate 后，旧 flight 的结果是写前快照，
	// 新请求必须另起一轮，不能等旧 leader 拿到过期结果。
	if f, ok := m.dirSize.flights[abs]; ok && f.gen == m.dirSize.gen {
		m.dirSize.mu.Unlock()
		select {
		case <-f.done:
			return cloneDirSize(f.stat), f.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// 频率预算只约束真正 leader 遍历（缓存命中/同 key 等待不计），避免正常浏览被误拒。
	if ident, ok := identityFrom(ctx); ok && ident.ID > 0 {
		if !m.allowDirSizeLocked(ident.ID, now) {
			m.dirSize.mu.Unlock()
			return nil, errDirSizeRateLimited
		}
	}
	if m.dirSize.flights == nil {
		m.dirSize.flights = map[string]*dirSizeFlight{}
	}
	if m.dirSizeSem == nil {
		m.dirSizeSem = make(chan struct{}, dirSizeGlobalConcurrency)
	}
	sem := m.dirSizeSem
	gen := m.dirSize.gen
	flight := &dirSizeFlight{gen: gen, done: make(chan struct{})}
	m.dirSize.flights[abs] = flight
	m.dirSize.mu.Unlock()

	// 全局并发预算：等待期间可被请求取消；取消时释放 flight，等待者立即结束。
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		m.dirSize.mu.Lock()
		if m.dirSize.flights[abs] == flight {
			delete(m.dirSize.flights, abs)
		}
		flight.err = ctx.Err()
		close(flight.done)
		m.dirSize.mu.Unlock()
		return nil, ctx.Err()
	}

	leaderCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dirSizeTimeout)
	var (
		stat map[string]any
		err  error
	)
	// 无论正常返回还是 panic，都记录结果、删除 flight、关闭 done 释放等待者；panic 原样重抛。
	defer func() {
		cancel()
		panicVal := recover()
		if panicVal != nil {
			stat, err = nil, errDirSizeAborted
		}
		m.dirSize.mu.Lock()
		flight.stat, flight.err = stat, err
		// 只删除自己：失效后可能已有新 flight 顶替，旧 leader 不得误删。
		if m.dirSize.flights[abs] == flight {
			delete(m.dirSize.flights, abs)
		}
		if err == nil && stat != nil && m.dirSize.gen == gen {
			m.dirSize.storeLocked(abs, stat, time.Now())
		}
		close(flight.done)
		m.dirSize.mu.Unlock()
		if panicVal != nil {
			panic(panicVal)
		}
	}()
	stat, err = calcDirSize(leaderCtx, ol, abs)
	return cloneDirSize(stat), err
}

// calcDirSize 栈式遍历（条数/深度/耗时三上限触顶即 truncated）；上游失败返回已统计部分与错误。
func calcDirSize(ctx context.Context, ol OpenListClient, absRoot string) (map[string]any, error) {
	const maxItems = 5000
	const maxDepth = 20
	deadline := time.Now().Add(30 * time.Second)
	stat := map[string]any{"size": int64(0), "files": 0, "dirs": 0, "truncated": false}
	size, files, dirs, truncated := int64(0), 0, 0, false
	type frame struct {
		abs   string
		depth int
	}
	visited := 0
	stack := []frame{{absRoot, 0}}
	var walkErr error
	for len(stack) > 0 && !truncated {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		data, err := ol.Call(ctx, http.MethodPost, "/api/fs/list", CallOpts{
			Body: map[string]any{"path": f.abs, "password": "", "page": 1, "per_page": 0, "refresh": false},
		})
		if err != nil {
			walkErr = err
			truncated = true
			break
		}
		for _, item := range parseItems(data) {
			if item.IsDir {
				dirs++
				if f.depth+1 < maxDepth {
					stack = append(stack, frame{f.abs + "/" + item.Name, f.depth + 1})
				} else {
					truncated = true
				}
			} else {
				size += item.Size
				files++
			}
			visited++
			if visited >= maxItems || time.Now().After(deadline) {
				truncated = true
				break
			}
		}
	}
	stat["size"], stat["files"], stat["dirs"], stat["truncated"] = size, files, dirs, truncated
	return stat, walkErr
}

// handleMkdir POST /mkdir：新建文件夹。
func (m *Module) handleMkdir(w http.ResponseWriter, r *http.Request) {
	m.mutate(w, r, "已创建", func(ol OpenListClient, root string, body map[string]json.RawMessage) error {
		rel, ok := NormRel(rawString(body["path"]))
		name := ValidName(rawString(body["name"]))
		if !ok || name == "" {
			return errParam
		}
		_, err := ol.Call(r.Context(), http.MethodPost, "/api/fs/mkdir", CallOpts{
			Body: map[string]any{"path": JoinPath(root, rel) + "/" + name},
		})
		return err
	})
}

// handleRename POST /rename：重命名。
func (m *Module) handleRename(w http.ResponseWriter, r *http.Request) {
	m.mutate(w, r, "已重命名", func(ol OpenListClient, root string, body map[string]json.RawMessage) error {
		rel, ok := NormRel(rawString(body["path"]))
		name := ValidName(rawString(body["name"]))
		if !ok || rel == "/" || name == "" {
			return errParam
		}
		_, err := ol.Call(r.Context(), http.MethodPost, "/api/fs/rename", CallOpts{
			Body: map[string]any{"path": JoinPath(root, rel), "name": name},
		})
		return err
	})
}

// handleRenameBatch POST /rename/batch：批量重命名（1-100 条；单条失败不中断，汇总返回）。
func (m *Module) handleRenameBatch(w http.ResponseWriter, r *http.Request) {
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	body, err := decodeBodyMap(r)
	if err != nil {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误", nil)
		return
	}
	root, ok := m.resolveRoot(w, r, rawString(body["space"]))
	if !ok {
		return
	}
	var items []map[string]json.RawMessage
	if raw, exists := body["items"]; exists {
		_ = json.Unmarshal(raw, &items)
	}
	if len(items) == 0 || len(items) > 100 {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误：items（1-100 条）", nil)
		return
	}
	failed := make([]map[string]any, 0)
	renamed := 0
	for _, it := range items {
		rel, ok := NormRel(rawString(it["path"]))
		name := ValidName(rawString(it["name"]))
		if !ok || rel == "/" || name == "" {
			failed = append(failed, map[string]any{"path": rawString(it["path"]), "name": rawString(it["name"]), "message": "参数不合法"})
			continue
		}
		if _, err := ol.Call(r.Context(), http.MethodPost, "/api/fs/rename", CallOpts{
			Body: map[string]any{"path": JoinPath(root, rel), "name": name},
		}); err != nil {
			failed = append(failed, map[string]any{"path": rel, "name": name, "message": errMessage(err)})
		} else {
			renamed++
		}
	}
	if renamed > 0 {
		m.dirSize.invalidate() // 批量重命名部分成功也要失效
	}
	writeOK(w, map[string]any{"renamed": renamed, "failed": failed}, "批量重命名完成")
}

// handleRemove POST /remove：批量删除（按目录分组逐批调上游）。
func (m *Module) handleRemove(w http.ResponseWriter, r *http.Request) {
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	body, err := decodeBodyMap(r)
	if err != nil {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误", nil)
		return
	}
	root, ok := m.resolveRoot(w, r, rawString(body["space"]))
	if !ok {
		return
	}
	var paths []string
	if raw, exists := body["paths"]; exists {
		_ = json.Unmarshal(raw, &paths)
	}
	if len(paths) == 0 || len(paths) > 100 {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误：paths（1-100 条）", nil)
		return
	}
	groups := map[string][]string{}
	order := make([]string, 0)
	for _, p := range paths {
		rel, ok := NormRel(p)
		if !ok || rel == "/" {
			writeFail(w, http.StatusBadRequest, codeParam, "路径不合法", nil)
			return
		}
		dir := DirName(rel)
		if _, exists := groups[dir]; !exists {
			order = append(order, dir)
		}
		groups[dir] = append(groups[dir], BaseName(rel))
	}
	for _, dir := range order {
		if _, err := ol.Call(r.Context(), http.MethodPost, "/api/fs/remove", CallOpts{
			Body: map[string]any{"dir": JoinPath(root, dir), "names": groups[dir]},
		}); err != nil {
			relayFail(w, err)
			return
		}
		m.dirSize.invalidate() // 每批成功即失效（后续批次失败也不会回填旧统计）
	}
	writeOK(w, nil, "已删除")
}

// handleMove / handleCopy：移动 / 复制（跨空间允许）。
func (m *Module) handleMove(w http.ResponseWriter, r *http.Request) {
	m.moveCopy(w, r, "move")
}
func (m *Module) handleCopy(w http.ResponseWriter, r *http.Request) {
	m.moveCopy(w, r, "copy")
}

func (m *Module) moveCopy(w http.ResponseWriter, r *http.Request, op string) {
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	body, err := decodeBodyMap(r)
	if err != nil {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误", nil)
		return
	}
	srcSpace := rawString(body["src_space"])
	dstSpace := rawString(body["dst_space"])
	srcRoot, ok := m.resolveRoot(w, r, srcSpace)
	if !ok {
		return
	}
	dstRoot, ok := m.resolveRoot(w, r, dstSpace)
	if !ok {
		return
	}
	srcDir, ok1 := NormRel(rawString(body["src_dir"]))
	dstDir, ok2 := NormRel(rawString(body["dst_dir"]))
	if !ok1 || !ok2 {
		writeFail(w, http.StatusBadRequest, codeParam, "路径不合法", nil)
		return
	}
	var rawNames []string
	if raw, exists := body["names"]; exists {
		_ = json.Unmarshal(raw, &rawNames)
	}
	names := make([]string, 0, len(rawNames))
	for _, n := range rawNames {
		clean := ValidName(n)
		if clean == "" {
			writeFail(w, http.StatusBadRequest, codeParam, "参数错误：names（1-100 条）", nil)
			return
		}
		names = append(names, clean)
	}
	if len(names) == 0 || len(names) > 100 {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误：names（1-100 条）", nil)
		return
	}
	if srcSpace == dstSpace && srcDir == dstDir {
		writeFail(w, http.StatusBadRequest, codeNoTeam, "源位置与目标位置相同", nil)
		return
	}
	// 目标目录自愈（个人空间根可能未建）。
	_ = ol.EnsureDir(r.Context(), JoinPath(dstRoot, dstDir))
	api := "/api/fs/move"
	if op == "copy" {
		api = "/api/fs/copy"
	}
	if _, err := ol.Call(r.Context(), http.MethodPost, api, CallOpts{
		Body: map[string]any{"src_dir": JoinPath(srcRoot, srcDir), "dst_dir": JoinPath(dstRoot, dstDir), "names": names},
	}); err != nil {
		relayFail(w, err)
		return
	}
	m.dirSize.invalidate() // 移动/复制改变源与目标目录统计
	msg := "已移动"
	if op == "copy" {
		msg = "已复制"
	}
	writeOK(w, nil, msg)
}

// handleDownload GET /download：下载/预览（disposition=inline 时内联）。
func (m *Module) handleDownload(w http.ResponseWriter, r *http.Request) {
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	root, ok := m.resolveRoot(w, r, r.URL.Query().Get("space"))
	if !ok {
		return
	}
	rel, ok := NormRel(r.URL.Query().Get("path"))
	if !ok || rel == "/" {
		writeFail(w, http.StatusBadRequest, codeParam, "路径不合法", nil)
		return
	}
	disposition := "attachment"
	if r.URL.Query().Get("disposition") == "inline" {
		disposition = "inline"
	}
	m.pipeDownload(w, r, ol, JoinPath(root, rel), BaseName(rel), disposition)
}

// pipeDownload 流式下载共用：透传长度与类型，按 disposition 强制内联/下载。
func (m *Module) pipeDownload(w http.ResponseWriter, r *http.Request, ol OpenListClient, abs, name, disposition string) {
	rc, header, err := ol.DownloadStream(r.Context(), abs)
	if err != nil {
		relayFail(w, err)
		return
	}
	defer rc.Close()
	if v := header.Get("Content-Length"); v != "" {
		w.Header().Set("Content-Length", v)
	}
	if v := header.Get("Content-Type"); v != "" {
		w.Header().Set("Content-Type", v)
	}
	w.Header().Set("Content-Disposition", disposition+`; filename*=UTF-8''`+url.PathEscape(name))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

// handleDownloadDir GET /download/dir：目录整包 ZIP（流式打包）。
func (m *Module) handleDownloadDir(w http.ResponseWriter, r *http.Request) {
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	root, ok := m.resolveRoot(w, r, r.URL.Query().Get("space"))
	if !ok {
		return
	}
	rel, ok := NormRel(r.URL.Query().Get("path"))
	if !ok || rel == "/" {
		writeFail(w, http.StatusBadRequest, codeParam, "路径不合法", nil)
		return
	}
	abs := JoinPath(root, rel)
	meta, err := ol.Call(r.Context(), http.MethodPost, "/api/fs/get", CallOpts{Body: map[string]any{"path": abs, "password": ""}})
	if err != nil {
		relayFail(w, err)
		return
	}
	var metaObj struct {
		IsDir bool `json:"is_dir"`
	}
	_ = json.Unmarshal(meta, &metaObj)
	if !metaObj.IsDir {
		writeFail(w, http.StatusBadRequest, codeParam, "该路径不是文件夹", nil)
		return
	}
	m.pipeDirZip(w, r, ol, abs, BaseName(rel))
}

// handleThumb GET /thumb：图片缩略图（≤384px webp 缓存；超限 302 回退 inline 直链）。
func (m *Module) handleThumb(w http.ResponseWriter, r *http.Request) {
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	root, ok := m.resolveRoot(w, r, q.Get("space"))
	if !ok {
		return
	}
	rel, ok := NormRel(q.Get("path"))
	if !ok || rel == "/" {
		writeFail(w, http.StatusBadRequest, codeParam, "路径不合法", nil)
		return
	}
	ext := strings.ToLower(extOf(BaseName(rel)))
	switch ext {
	case "jpg", "jpeg", "png", "gif", "webp", "bmp":
	default:
		writeFail(w, http.StatusBadRequest, codeNoTeam, "该格式不支持缩略图", nil)
		return
	}
	abs := JoinPath(root, rel)
	metaRaw, err := ol.Call(r.Context(), http.MethodPost, "/api/fs/get", CallOpts{Body: map[string]any{"path": abs, "password": ""}})
	if err != nil {
		relayFail(w, err)
		return
	}
	var meta struct {
		IsDir    bool   `json:"is_dir"`
		Size     int64  `json:"size"`
		Modified string `json:"modified"`
	}
	_ = json.Unmarshal(metaRaw, &meta)
	if meta.IsDir {
		writeFail(w, http.StatusBadRequest, codeParam, "路径不合法", nil)
		return
	}
	if meta.Size > m.thumbMaxBytes() {
		tk := ""
		if v := q.Get("token"); v != "" {
			tk = "&token=" + url.QueryEscape(v)
		}
		target := "/api/v1/netdisk/download?space=" + url.QueryEscape(q.Get("space")) +
			"&path=" + url.QueryEscape(rel) + "&disposition=inline" + tk
		http.Redirect(w, r, target, http.StatusFound)
		return
	}
	key := docKey(abs, meta.Size, meta.Modified)
	// 缓存命中直出（键 = sha1(绝对路径|size|modified)，文件变缓存键自动失效重生）。
	if file, ok := m.thumbCacheFile(key); ok {
		sendThumbCache(w, file)
		return
	}
	if m.Thumb == nil {
		writeFail(w, http.StatusServiceUnavailable, codeNotConfigured, "缩略图组件不可用", nil)
		return
	}
	// 生成并发闸 2（同 Node）；等待必须可被请求取消（客户端断开不再占住连接）。
	// 下载+生成共享总预算，避免大图/慢上游无限占位；超时返回明确错误而非 200 空体。
	ctx, cancel := context.WithTimeout(r.Context(), m.thumbTimeout())
	defer cancel()
	select {
	case m.thumbSem <- struct{}{}:
		defer func() { <-m.thumbSem }()
	case <-ctx.Done():
		if r.Context().Err() != nil {
			return // 客户端已断开，无法回写
		}
		writeFail(w, http.StatusGatewayTimeout, codeUpstream, "缩略图生成超时，请稍后重试", nil)
		return
	}
	if file, ok := m.thumbCacheFile(key); ok { // 排队期间已有请求生成
		sendThumbCache(w, file)
		return
	}
	rc, _, err := ol.DownloadStream(ctx, abs)
	if err != nil {
		relayFail(w, err)
		return
	}
	src, readErr := readAllLimitBytes(rc, m.thumbMaxBytes())
	_ = rc.Close()
	if readErr != nil {
		if errors.Is(readErr, ErrFileTooLarge) {
			writeFail(w, http.StatusRequestEntityTooLarge, codeTooLarge, "原图超出缩略图大小限制", nil)
			return
		}
		relayFail(w, readErr)
		return
	}
	webp, err := m.Thumb.MakeThumb(src)
	if err != nil {
		if errors.Is(err, ErrThumbTooLarge) {
			writeFail(w, http.StatusRequestEntityTooLarge, codeTooLarge, ErrThumbTooLarge.Error(), nil)
			return
		}
		relayFail(w, err)
		return
	}
	m.storeThumbCache(key, webp) // 落盘失败不致命，直出内存结果
	w.Header().Set("Content-Type", "image/webp")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(webp)
}

// handlePreviewSign POST /preview/sign：Office 在线预览票据签发（仅 Office 格式，10 分钟）。
func (m *Module) handlePreviewSign(w http.ResponseWriter, r *http.Request) {
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	body, err := decodeBodyMap(r)
	if err != nil {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误", nil)
		return
	}
	root, ok := m.resolveRoot(w, r, rawString(body["space"]))
	if !ok {
		return
	}
	rel, ok := NormRel(rawString(body["path"]))
	if !ok || rel == "/" {
		writeFail(w, http.StatusBadRequest, codeParam, "路径不合法", nil)
		return
	}
	name := BaseName(rel)
	if previewMIME(name) == "" {
		writeFail(w, http.StatusBadRequest, codeNoTeam, "该格式不支持在线预览", nil)
		return
	}
	abs := JoinPath(root, rel)
	if _, err := ol.Call(r.Context(), http.MethodPost, "/api/fs/get", CallOpts{Body: map[string]any{"path": abs, "password": ""}}); err != nil {
		relayFail(w, err)
		return
	}
	token := m.signTicket(jwt.MapClaims{"ndp": abs}, 10*time.Minute)
	writeOK(w, map[string]any{"token": token, "expires_in": 600}, "")
}

// handlePreviewStream GET /preview/:token/:name?：免登录回源取流（票据即鉴权）。
func (m *Module) handlePreviewStream(w http.ResponseWriter, r *http.Request) {
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	claims := m.verifyTicket(chiURLParam(r, "token"))
	abs := claimsString(claims, "ndp")
	if abs == "" || !strings.HasPrefix(abs, "/") {
		writeFail(w, http.StatusForbidden, codeBadTicket, "预览票据无效或已过期", nil)
		return
	}
	name := BaseName(abs)
	rc, header, err := ol.DownloadStream(r.Context(), abs)
	if err != nil {
		relayFail(w, err)
		return
	}
	defer rc.Close()
	if v := header.Get("Content-Length"); v != "" {
		w.Header().Set("Content-Length", v)
	}
	ct := previewMIME(name)
	if ct == "" {
		ct = header.Get("Content-Type")
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", "inline; filename*=UTF-8''"+url.PathEscape(name))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

// previewMIME Office 预览格式白名单（第三方查看器按 Content-Type 判格式；Node PREVIEW_MIME 同径，
// 不含 pdf——pdf 走 ONLYOFFICE 承载页，本路由仅微软查看器六族）。
func previewMIME(name string) string {
	switch strings.ToLower(extOf(name)) {
	case "doc":
		return "application/msword"
	case "docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case "xls":
		return "application/vnd.ms-excel"
	case "xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "ppt":
		return "application/vnd.ms-powerpoint"
	case "pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	}
	return ""
}

func extOf(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return ""
}

// ---- 共用小工具 ----

var errParam = errParamType{}

type errParamType struct{}

func (errParamType) Error() string { return "参数错误" }

// mutate 写操作共用骨架：OpenList 可用性 → 空间根 → 业务体 → 成功文案。
func (m *Module) mutate(w http.ResponseWriter, r *http.Request, okMsg string, fn func(ol OpenListClient, root string, body map[string]json.RawMessage) error) {
	ol, ok := m.requireOL(w)
	if !ok {
		return
	}
	body, err := decodeBodyMap(r)
	if err != nil {
		writeFail(w, http.StatusBadRequest, codeParam, "参数错误", nil)
		return
	}
	root, ok := m.resolveRoot(w, r, rawString(body["space"]))
	if !ok {
		return
	}
	if err := fn(ol, root, body); err != nil {
		if _, isParam := err.(errParamType); isParam {
			writeFail(w, http.StatusBadRequest, codeParam, "参数错误：path/name", nil)
			return
		}
		relayFail(w, err)
		return
	}
	m.dirSize.invalidate() // 写操作成功收口失效 dirsize 缓存
	writeOK(w, nil, okMsg)
}

func decodeBody(r *http.Request, target any) error {
	return json.NewDecoder(r.Body).Decode(target)
}

func decodeBodyMap(r *http.Request) (map[string]json.RawMessage, error) {
	body := map[string]json.RawMessage{}
	err := json.NewDecoder(r.Body).Decode(&body)
	return body, err
}

func rawString(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.Trim(string(raw), `"`)
}

func errMessage(err error) string {
	var ue *UpstreamError
	if errorsAs(err, &ue) {
		return ue.Message
	}
	return err.Error()
}

func errorsAs(err error, target any) bool {
	ue, ok := err.(*UpstreamError)
	if ok {
		switch t := target.(type) {
		case **UpstreamError:
			*t = ue
		}
	}
	return ok
}

// chiURLParam 读取路径参数（避免各 handler 重复引 chi）。
func chiURLParam(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}

var _ = strconv.Itoa
