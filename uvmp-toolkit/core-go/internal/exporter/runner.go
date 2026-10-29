package exporter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/render"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/uvmpapi"
)

// Deps 编排依赖（真实路径：auth.Manager + uvmpapi.Client + render worker 工厂；
// 单测与 dev.mock 以 fake 顶替，不启动真实浏览器）。
type Deps struct {
	API         OrderAPI        // 业务查询接口
	NewRenderer RendererFactory // 渲染 worker 工厂（mock 可 nil）
	Creds       CredsSource     // 凭证快照（Generation 检测，mock 可 nil）
}

// row 清单行（backend.run 返回 rows 同形状）。
type row struct {
	Seq         int    `json:"seq"`
	Code        string `json:"code"`
	Vehicle     string `json:"vehicle"`
	Date        string `json:"date"`
	PcdFile     string `json:"pcd_file"`
	PcdStatus   string `json:"pcd_status"`
	TrackFile   string `json:"track_file"`
	TrackStatus string `json:"track_status"`
	Note        string `json:"note"`
}

// Result done.result（backend.run 返回形状）。
type Result struct {
	Dir       string   `json:"dir"`
	Total     int      `json:"total"`
	Found     int      `json:"found"`
	NotFound  []string `json:"not_found"`
	PcdOk     int      `json:"pcd_ok"`
	PcdFail   int      `json:"pcd_fail"`
	TrackOk   int      `json:"track_ok"`
	TrackFail int      `json:"track_fail"`
	Rows      []row    `json:"rows"`
}

// foundItem 阶段1命中单据。
type foundItem struct {
	seq   int
	code  string
	order uvmpapi.Dict
}

// Run 事件化执行一次批次导出：
// 校验（空清单在 config 为空时也先给 error 事件，供 CI 冒烟）→ build 依赖
// （真实路径每批 Auth Manager 登录一次，登录走 job ctx——取消覆盖登录）→ 编排。
// 终止事件（done/error）由本函数发出，返回错误供进程退出码。密码只经 stdin 请求体，
// 不进日志。批次渲染 worker 目录成功收尾后整目录清理；失败/取消保留（诊断用，
// render 层日志/截图已脱敏）。
func Run(ctx context.Context, req Request, em *Emitter, build func(ctx context.Context, req Request, cfg *RunConfig) (Deps, error)) error {
	cfg, err := ParseRunConfig(req, os.Getenv, time.Now())
	if err != nil {
		em.Error(err.Error())
		return err
	}
	if err := os.MkdirAll(cfg.FilesDir, 0o755); err != nil {
		em.Error("创建输出目录失败: " + err.Error())
		return err
	}
	em.Log("共 %d 个派车单号；输出目录：%s", len(req.Codes), cfg.OutDir)
	deps, err := build(ctx, req, cfg)
	if err != nil {
		em.Error(err.Error())
		return err
	}
	res, err := orchestrate(ctx, cfg, req, deps, em)
	if err != nil {
		if ctx.Err() != nil {
			em.Error("已取消")
			return ctx.Err()
		}
		em.Error(err.Error())
		em.Log("浏览器 worker 诊断保留：%s", cfg.WorkerRootDir())
		return err
	}
	// 成功收尾：worker 已全部关闭，批次 profile/日志目录整目录清理
	// （唯一批次目录避免跨进程 profile 锁；失败时才留作诊断）。
	_ = os.RemoveAll(cfg.WorkerRootDir())
	em.Done(res)
	return nil
}

// orchestrate 批次编排主流程（backend._run_python 逐字口径 + 三池并行）。
func orchestrate(ctx context.Context, cfg *RunConfig, req Request, deps Deps, em *Emitter) (*Result, error) {
	log := em.Log
	if cfg.Mock {
		em.Log("[dev] dev.mock=true，使用模拟派车数据（当日 7 单 + 昨日 2 单）")
	} else if deps.NewRenderer == nil {
		return nil, errors.New("官方页面渲染器未启动——派车单/轨迹导出需要官方页面直出。" +
			"请到「设置」页选择公司指定浏览器后重试")
	}
	codes := req.Codes
	for i := range codes {
		if codes[i].Seq <= 0 {
			codes[i].Seq = i + 1
		}
	}
	mf := LoadManifest(filepath.Join(cfg.OutDir, "manifest.json"))
	width := len(strconv.Itoa(len(codes)))

	// ---- 阶段1：按号查询（查询池，事件按清单序流出）----
	em.Progress(0, len(codes), "按单号查询")
	found, notFound, err := queryPhase(ctx, cfg, deps, em, codes)
	if err != nil {
		return nil, err
	}
	em.Progress(len(codes), len(codes), "查询完成")
	em.Log("查询完成：找到 %d 单，未找到 %d 单", len(found), len(notFound))

	rows := map[int]*row{}
	for _, f := range found {
		o := f.order
		rows[f.seq] = &row{
			Seq:         f.seq,
			Code:        f.code,
			Vehicle:     strAny(o["vehicleNumber"]),
			Date:        cutLen(strAny(firstNonEmpty(o["planSendTimeStr"], o["planSendTime"])), 10),
			PcdStatus:   "未导出",
			TrackStatus: "未导出",
		}
	}

	var noonRows []noonRow
	if cfg.ExportPCD && len(found) > 0 {
		if err := exportPCDPhase(ctx, cfg, deps, em, mf, width, found, rows); err != nil {
			return nil, err
		}
	}
	if cfg.ExportTrack && len(found) > 0 {
		noonRows, err = exportTrackPhase(ctx, cfg, deps, em, mf, width, found, rows)
		if err != nil {
			return nil, err
		}
	}

	// ---- 合并：全部导完后按序号顺序合成总 PDF（不足 2 个不出）----
	ordered := sortedRows(rows)
	if cfg.ExportPCD && len(found) > 0 {
		files := make([]string, 0, len(ordered))
		for _, r := range ordered {
			if r.PcdFile != "" {
				files = append(files, filepath.Join(cfg.FilesDir, r.PcdFile))
			}
		}
		mergePDFs(ctx, files, filepath.Join(cfg.OutDir, "_合并_派车单.pdf"), log)
	}
	if cfg.ExportTrack && len(found) > 0 {
		files := make([]string, 0, len(ordered))
		for _, r := range ordered {
			if r.TrackFile != "" {
				files = append(files, filepath.Join(cfg.FilesDir, r.TrackFile))
			}
		}
		mergePDFs(ctx, files, filepath.Join(cfg.OutDir, "_合并_轨迹.pdf"), log)
	}

	// ---- 报告 ----
	if err := writeManifestCSV(cfg.OutDir, ordered); err != nil {
		return nil, err
	}
	if err := writeNotFound(cfg.OutDir, notFound, log); err != nil {
		return nil, err
	}
	if cfg.ExportTrack && len(found) > 0 {
		// 页码 = 该单在「轨迹成功序列」中的位次（_合并_轨迹.pdf 按 seq 升序、每单恰 1 页）；
		// 渲染失败的单没进合并 PDF，页码留空
		var okSeqs []int
		for _, r := range ordered {
			if r.TrackFile != "" {
				okSeqs = append(okSeqs, r.Seq)
			}
		}
		for i := range noonRows {
			if rank := sort.SearchInts(okSeqs, noonRows[i].Seq); rank < len(okSeqs) && okSeqs[rank] == noonRows[i].Seq {
				noonRows[i].Page = rank + 1
			}
		}
		if err := writeNoonEndReport(noonRows, filepath.Join(cfg.OutDir, "_12点前结束行程.xlsx"), log); err != nil {
			return nil, err
		}
	}

	pcdOK, pcdFail, trackOK, trackFail := 0, 0, 0, 0
	for _, r := range ordered {
		switch r.PcdStatus {
		case "成功":
			pcdOK++
		case "失败":
			pcdFail++
		}
		switch r.TrackStatus {
		case "成功":
			trackOK++
		case "失败":
			trackFail++
		}
	}
	log("全部完成：派车单 成功 %d/失败 %d；轨迹 成功 %d/失败 %d；未找到 %d",
		pcdOK, pcdFail, trackOK, trackFail, len(notFound))

	nf := make([]string, 0, len(notFound))
	for _, c := range notFound {
		nf = append(nf, c.Code)
	}
	return &Result{
		Dir:       cfg.OutDir,
		Total:     len(codes),
		Found:     len(found),
		NotFound:  nf,
		PcdOk:     pcdOK,
		PcdFail:   pcdFail,
		TrackOk:   trackOK,
		TrackFail: trackFail,
		Rows:      ordered,
	}, nil
}

// ---------------------------------------------------------------- 阶段1：查询池

func queryPhase(ctx context.Context, cfg *RunConfig, deps Deps, em *Emitter, codes []CodeItem) ([]foundItem, []CodeItem, error) {
	n := len(codes)
	type qres struct {
		order uvmpapi.Dict
		err   error
	}
	results := make([]qres, n)
	ord := newItemSync(n)
	jobs := make(chan int, n)
	for i := range codes {
		jobs <- i
	}
	close(jobs)
	var wg sync.WaitGroup
	for w := 0; w < cfg.QueryWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				var r qres
				if err := ctx.Err(); err != nil {
					r.err = err
				} else {
					o, ok, err := deps.API.QueryPcdByRunCode(ctx, codes[i].Code, codes[i].DateHint, cfg.LookbackDays, uvmpapi.LogFunc(em.Log))
					if err != nil {
						r.err = err
					} else if ok {
						r.order = o
					}
				}
				results[i] = r
				ord.done(i)
			}
		}()
	}

	var found []foundItem
	var notFound []CodeItem
	for i, c := range codes {
		em.Progress(i, n, "查询 "+c.Code)
		if err := ord.wait(ctx, i); err != nil {
			return nil, nil, err
		}
		r := results[i]
		if r.err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			return nil, nil, r.err
		}
		if r.order != nil {
			found = append(found, foundItem{seq: c.Seq, code: c.Code, order: r.order})
		} else {
			notFound = append(notFound, c)
			em.Log("  未找到：%s", c.Code)
		}
	}
	return found, notFound, nil
}

// ---------------------------------------------------------------- 阶段2：派车单

func exportPCDPhase(ctx context.Context, cfg *RunConfig, deps Deps, em *Emitter, mf *Manifest,
	width int, found []foundItem, rows map[int]*row,
) error {
	log := em.Log
	// 断点续传：manifest 已有的直接记成功
	var todo []foundItem
	skipped := 0
	for _, f := range found {
		id := strAny(f.order["id"])
		if mf.Has("pcd", id) {
			r := rows[f.seq]
			r.PcdFile = mf.Get("pcd", id)
			r.PcdStatus = "成功"
			r.Note = noteAppend(r.Note, "派车单此前已导出")
			skipped++
		} else {
			todo = append(todo, f)
		}
	}
	log("派车单：共 %d 单，已导出 %d 单，本次待导出 %d 单", len(found), skipped, len(todo))
	if len(todo) == 0 {
		return nil
	}

	type pcdRes struct {
		fname   string
		err     error
		replace bool // 渲染错误（需替换 worker）
	}
	results := make([]pcdRes, len(todo))
	details := make([]uvmpapi.Dict, len(todo))
	prepErr := make([]error, len(todo))
	ord := newItemSync(len(todo))
	renderQ := make(chan int, len(todo))

	// 预取池：打印详情（mock 同 Python 不查详情，直接用查询结果）
	var wgPrep sync.WaitGroup
	prepSem := make(chan struct{}, cfg.QueryWorkers)
	for i := range todo {
		wgPrep.Add(1)
		go func(i int) {
			defer wgPrep.Done()
			if err := ctx.Err(); err != nil {
				prepErr[i] = err
				renderQ <- i
				return
			}
			select {
			case prepSem <- struct{}{}:
			case <-ctx.Done():
				prepErr[i] = ctx.Err()
				renderQ <- i
				return
			}
			defer func() { <-prepSem }()
			f := todo[i]
			if cfg.Mock {
				details[i] = f.order
				renderQ <- i
				return
			}
			ds, err := deps.API.GetPrintDetails(ctx, []string{strAny(f.order["id"])}, 1)
			if err != nil {
				prepErr[i] = err
			} else if len(ds) > 0 {
				details[i] = ds[0]
			} else {
				details[i] = f.order
			}
			renderQ <- i
		}(i)
	}
	go func() {
		wgPrep.Wait()
		close(renderQ)
	}()

	// 渲染池：独立浏览器 worker
	pool := newRenderPool(cfg.RenderWorkers, deps.NewRenderer, deps.Creds, log)
	defer pool.close()
	var wgRender sync.WaitGroup
	for _, s := range pool.slots {
		wgRender.Add(1)
		go func(s *renderSlot) {
			defer wgRender.Done()
			workerLoop(ctx, s, renderQ, results, func(ctx context.Context, s *renderSlot, i int) pcdRes {
				if prepErr[i] != nil {
					return pcdRes{err: prepErr[i]}
				}
				f := todo[i]
				d := details[i]
				fname := pcdFileName(f.seq, width, d)
				outPDF := filepath.Join(cfg.FilesDir, fname)
				if cfg.Mock {
					if err := writeStubPDF(outPDF, "[mock] pcd "+f.code); err != nil {
						return pcdRes{err: err}
					}
					if err := mf.Put("pcd", strAny(f.order["id"]), fname); err != nil {
						return pcdRes{err: fmt.Errorf("manifest 写入失败: %w", err)}
					}
					return pcdRes{fname: fname}
				}
				r, err := s.get(ctx)
				if err != nil {
					return pcdRes{err: err}
				}
				if err := r.RenderPCDPDF(ctx, d, outPDF, fmt.Sprintf("p%d", f.seq), cfg.OutDir); err != nil {
					return pcdRes{err: err, replace: true}
				}
				if err := mf.Put("pcd", strAny(f.order["id"]), fname); err != nil {
					return pcdRes{err: fmt.Errorf("manifest 写入失败: %w", err)}
				}
				return pcdRes{fname: fname}
			}, func(r pcdRes) bool { return r.replace }, ord.done)
		}(s)
	}
	// 事件按清单序流出（与渲染并行；与 Python 逐单循环的事件流一致）
	done := 0
	var emitErr error
	for i := range todo {
		if err := ord.wait(ctx, i); err != nil {
			emitErr = err
			break
		}
		f := todo[i]
		r := results[i]
		if r.err != nil {
			if ctx.Err() != nil {
				emitErr = ctx.Err()
				break
			}
			rows[f.seq].PcdStatus = "失败"
			log("  失败 %s: %s", f.code, r.err)
			continue
		}
		done++
		rr := rows[f.seq]
		rr.PcdFile, rr.PcdStatus = r.fname, "成功"
		if cfg.Mock {
			rr.Note = noteAppend(rr.Note, "mock 占位")
		}
		em.Progress(done, len(todo), "派车单 "+r.fname)
		if cfg.Mock {
			log("[%d/%d] %s（mock 占位）", done, len(todo), r.fname)
		} else {
			log("[%d/%d] %s", done, len(todo), r.fname)
		}
	}
	wgRender.Wait()
	return emitErr
}

// ---------------------------------------------------------------- 阶段3：轨迹

func exportTrackPhase(ctx context.Context, cfg *RunConfig, deps Deps, em *Emitter, mf *Manifest,
	width int, found []foundItem, rows map[int]*row,
) ([]noonRow, error) {
	log := em.Log
	type todoItem struct {
		f          foundItem
		begin, end string
	}
	var todo []todoItem
	for _, f := range found {
		o := f.order
		id := strAny(o["id"])
		begin := cleanTime(o["realSendTime"])
		end := cleanTime(o["realBackTime"])
		switch {
		case mf.Has("track", id):
			r := rows[f.seq]
			r.TrackFile = mf.Get("track", id)
			r.TrackStatus = "成功"
			r.Note = noteAppend(r.Note, "轨迹此前已导出")
		case strAny(o["vehicleId"]) != "" && begin != "" && end != "":
			todo = append(todo, todoItem{f: f, begin: begin, end: end})
		default:
			rows[f.seq].TrackStatus = "跳过（未完结或缺实际时间）"
		}
	}
	log("轨迹：待导出 %d 条（按派车单的实际出车/归队时间取轨迹）", len(todo))

	type trackPrep struct {
		points []uvmpapi.Dict
		seg    uvmpapi.Dict
		err    error
	}
	type trackRes struct {
		fname   string
		err     error
		replace bool
		nPoints int
		logs    []string
		noon    *noonRow
	}
	prep := make([]trackPrep, len(todo))
	results := make([]trackRes, len(todo))
	ord := newItemSync(len(todo))
	renderQ := make(chan int, len(todo))

	// 预取池：轨迹点 + 行程段（mock 同 Python 不查行程段）
	var wgPrep sync.WaitGroup
	prepSem := make(chan struct{}, cfg.QueryWorkers)
	for i := range todo {
		wgPrep.Add(1)
		go func(i int) {
			defer wgPrep.Done()
			if err := ctx.Err(); err != nil {
				prep[i] = trackPrep{err: err}
				renderQ <- i
				return
			}
			select {
			case prepSem <- struct{}{}:
			case <-ctx.Done():
				prep[i] = trackPrep{err: ctx.Err()}
				renderQ <- i
				return
			}
			defer func() { <-prepSem }()
			t := todo[i]
			var p trackPrep
			p.points, p.err = deps.API.QueryTrackPoints(ctx, strAny(t.f.order["vehicleId"]), t.begin, t.end)
			if p.err == nil && !cfg.Mock {
				p.seg, p.err = deps.API.QueryTrackSegment(ctx, strAny(t.f.order["vehicleId"]), t.begin, t.end)
			}
			prep[i] = p
			renderQ <- i
		}(i)
	}
	go func() {
		wgPrep.Wait()
		close(renderQ)
	}()

	pool := newRenderPool(cfg.RenderWorkers, deps.NewRenderer, deps.Creds, log)
	defer pool.close()
	var wgRender sync.WaitGroup
	for _, s := range pool.slots {
		wgRender.Add(1)
		go func(s *renderSlot) {
			defer wgRender.Done()
			workerLoop(ctx, s, renderQ, results, func(ctx context.Context, s *renderSlot, i int) trackRes {
				if prep[i].err != nil {
					return trackRes{err: prep[i].err}
				}
				t := todo[i]
				o := t.f.order
				label := fmt.Sprintf("%s %s", strAny(o["vehicleNumber"]), t.f.code)
				fname := fmt.Sprintf("%s_轨迹_%s_%s_%s.pdf", padSeq(t.f.seq, width),
					safeName(t.f.code), safeName(o["vehicleNumber"]), safeName(cutLen(t.begin, 10)))
				outPDF := filepath.Join(cfg.FilesDir, fname)
				res := trackRes{fname: fname, nPoints: len(prep[i].points)}
				preJS := ""

				if cfg.Mock {
					if err := writeStubPDF(outPDF, "[mock] track "+t.f.code); err != nil {
						return trackRes{err: err}
					}
				} else {
					// 行程结束 12 点前统计（+可选自动校准，截图前注入弹窗改写脚本）
					endNoon := segEndBeforeNoon(prep[i].seg)
					if endNoon != "" {
						nr := &noonRow{
							Seq:     t.f.seq,
							Code:    t.f.code,
							Vehicle: strAny(o["vehicleNumber"]),
							End:     endNoon,
						}
						if cfg.CalibrateNoon {
							if newDT, oldDur, newDur, ok := calibrateNoonEnd(prep[i].seg); ok {
								oldDT, _ := time.ParseInLocation("2006-01-02 15:04:05", cutLen(endNoon, 19), time.Local)
								preJS = render.BuildPopupRewriteJS(oldDT, newDT, oldDur, newDur)
								nr.CalEnd = newDT.Format("2006-01-02 15:04:05")
								nr.CalDur = fmtDurCN(int(newDur))
								res.logs = append(res.logs, fmt.Sprintf("  [校准] %s 行程结束 %s → %s，行驶时间 → %s",
									label, endNoon, nr.CalEnd, nr.CalDur))
							}
						}
						res.noon = nr
					}
					rr, err := s.get(ctx)
					if err != nil {
						return trackRes{err: err, nPoints: res.nPoints, logs: res.logs, noon: res.noon}
					}
					png := filepath.Join(cfg.FilesDir, strings.TrimSuffix(fname, ".pdf")+".png")
					if prep[i].seg != nil && strAny(prep[i].seg["id"]) != "" {
						_, err = rr.RenderRoutePNG(ctx, strAny(prep[i].seg["id"]), png, preJS) // 官方行程详情弹窗
					} else {
						_, err = rr.RenderPCDPNG(ctx, o, png, preJS) // 回退按派车单弹窗
					}
					if err != nil {
						return trackRes{err: err, replace: true, nPoints: res.nPoints, logs: res.logs, noon: res.noon}
					}
					if err := rr.PNGToPDF(ctx, png, outPDF); err != nil {
						return trackRes{err: err, replace: true, nPoints: res.nPoints, logs: res.logs, noon: res.noon}
					}
					_ = os.Remove(png)
				}

				csvName := fmt.Sprintf("%s_轨迹点_%s.csv", padSeq(t.f.seq, width), safeName(t.f.code))
				if err := writeTrackPointsCSV(filepath.Join(cfg.FilesDir, csvName), prep[i].points); err != nil {
					return trackRes{err: err, nPoints: res.nPoints, logs: res.logs, noon: res.noon}
				}
				if err := mf.Put("track", strAny(o["id"]), fname); err != nil {
					return trackRes{err: fmt.Errorf("manifest 写入失败: %w", err), nPoints: res.nPoints, logs: res.logs, noon: res.noon}
				}
				return res
			}, func(r trackRes) bool { return r.replace }, ord.done)
		}(s)
	}
	var noons []noonRow
	done := 0
	var emitErr error
	for i := range todo {
		if err := ord.wait(ctx, i); err != nil {
			emitErr = err
			break
		}
		t := todo[i]
		r := results[i]
		label := fmt.Sprintf("%s %s", strAny(t.f.order["vehicleNumber"]), t.f.code)
		if r.err != nil {
			if ctx.Err() != nil {
				emitErr = ctx.Err()
				break
			}
			rows[t.f.seq].TrackStatus = "失败"
			log("  失败 %s: %s", label, r.err)
			// 渲染/CSV 失败仍保留 12 点前结束记录（与 Python 一致）：
			// noon 在渲染前已判定，页码留空（未进合并轨迹 PDF）。
			if r.noon != nil {
				noons = append(noons, *r.noon)
			}
			continue
		}
		done++
		rr := rows[t.f.seq]
		rr.TrackFile, rr.TrackStatus = r.fname, "成功"
		if cfg.Mock {
			rr.Note = noteAppend(rr.Note, "mock 占位")
		}
		for _, line := range r.logs {
			log("%s", line)
		}
		em.Progress(done, len(todo), "轨迹 "+r.fname)
		log("[%d/%d] %s（%d 个轨迹点）", done, len(todo), r.fname, r.nPoints)
		if r.noon != nil {
			noons = append(noons, *r.noon)
		}
	}
	wgRender.Wait()
	return noons, emitErr
}

// ---------------------------------------------------------------- 工具

// itemSync 并行执行、串行出事件：等第 i 项完成后才处理，保证事件流与
// Python 逐项循环一致。
type itemSync struct {
	ch []chan struct{}
}

func newItemSync(n int) *itemSync {
	s := &itemSync{ch: make([]chan struct{}, n)}
	for i := range s.ch {
		s.ch[i] = make(chan struct{})
	}
	return s
}

func (s *itemSync) done(i int) {
	if i >= 0 && i < len(s.ch) {
		close(s.ch[i]) // 每项恰好一次
	}
}

func (s *itemSync) wait(ctx context.Context, i int) error {
	select {
	case <-s.ch[i]:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// pcdFileName 派车单 PDF 名（backend._pcd_fname）：NNN_派车单_<runCode>_<车牌>_<用车日期>.pdf。
func pcdFileName(seq, width int, d uvmpapi.Dict) string {
	date := cutLen(strAny(firstNonEmpty(d["planSendTimeStr"], d["planSendTime"])), 10)
	return fmt.Sprintf("%s_派车单_%s_%s_%s.pdf", padSeq(seq, width),
		safeName(d["runCode"]), safeName(d["vehicleNumber"]), safeName(date))
}

// padSeq NNN = 清单行序，按最大位数补零。
func padSeq(seq, width int) string {
	return fmt.Sprintf("%0*d", width, seq)
}

// noteAppend 备注拼接（Python "(note + '；' if note else ”) + x" 口径）。
func noteAppend(note, add string) string {
	if note == "" {
		return add
	}
	return note + "；" + add
}

func sortedRows(rows map[int]*row) []row {
	out := make([]row, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}
