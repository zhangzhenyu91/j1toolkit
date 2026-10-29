package apps

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/job"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/usb"
)

// tableSyncSpec 树障/交跨统计表同步的差异参数（两应用契约完全同构，仅名字不同）。
type tableSyncSpec struct {
	AppID           string // tree_sync / crossing_sync
	CmdName         string // tree_sync_cmd.json / crossing_sync_cmd.json（服务端契约，不可改）
	StampNightly    string // tree_sync_nightly / crossing_sync_nightly
	DefaultFileName string
	CfgKey          string // tree_sync / crossing_sync
	PageName        string // 报错指引页名：树障同步页 / 交跨同步页
}

var (
	treeSyncSpec = tableSyncSpec{
		AppID:           "tree_sync",
		CmdName:         "tree_sync_cmd.json",
		StampNightly:    "tree_sync_nightly",
		DefaultFileName: "检修一班树木统计表.xlsx",
		CfgKey:          "tree_sync",
		PageName:        "树障同步页",
	}
	crossingSyncSpec = tableSyncSpec{
		AppID:           "crossing_sync",
		CmdName:         "crossing_sync_cmd.json",
		StampNightly:    "crossing_sync_nightly",
		DefaultFileName: "检修一班交跨统计表.xlsx",
		CfgKey:          "crossing_sync",
		PageName:        "交跨同步页",
	}
)

// allowedKinds 指令契约允许的 kind。
var allowedKinds = map[string]bool{"snapshot": true, "replace": true}

// tableSyncBackend 统计表同步（移植自 toolkit/apps/tree_sync/backend.py 与
// crossing_sync/backend.py，二者仅 spec 不同）。
//
// 指令契约（与服务端定稿）：服务端挂载 U 盘后在根目录放 <CmdName>：
//
//		{"version": 1, "cycle_id": "...", "kind": "snapshot|replace",
//		 "file": "检修一班树木统计表.xlsx", "issued_at": "..."}
//
//	  - snapshot：把内网表（local_path 指向的 xlsx）复制到 U 盘根，文件名用指令里的 file
//	  - replace：把 U 盘根的 file（服务端刚推入的新表）复制覆盖到 local_path
//	    （先备份原文件为 <原名>.bak 再覆盖；消费完的新表随指令一并删除）
//
// 执行成功后删除指令文件并 sync_fs() 落盘；失败保留指令文件（服务端有超时判定）。
// 向前兼容：未知字段忽略；未知 kind 跳过留痕；指令 JSON 损坏跳过留痕。
type tableSyncBackend struct {
	spec tableSyncSpec
	// 可注入依赖（测试 fake）；nil 用真实实现。
	UsbOpts  *usb.Options
	Now      func() time.Time
	StateSt  *state.Store
	LoadConf func() config.Dict
}

func newTableSyncBackend(spec tableSyncSpec) *tableSyncBackend {
	return &tableSyncBackend{spec: spec}
}

func (b *tableSyncBackend) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *tableSyncBackend) load() config.Dict {
	if b.LoadConf != nil {
		return b.LoadConf()
	}
	return config.Load()
}

func (b *tableSyncBackend) store() *state.Store {
	if b.StateSt != nil {
		return b.StateSt
	}
	return state.Default()
}

// inRoot 文件名安全校验：禁止路径穿越，只允许落在 U 盘根目录（指令 file 字段来自服务端）。
func inRoot(name string, root string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return false
	}
	if filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
		return false
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	targetAbs, err := filepath.Abs(filepath.Join(root, name))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// readCmd 读 U 盘根目录指令文件：无→nil；损坏→留痕返回 nil；正常→dict。
func (b *tableSyncBackend) readCmd(usbDir string, log func(string)) map[string]any {
	cmdFile := filepath.Join(usbDir, b.spec.CmdName)
	data, err := os.ReadFile(cmdFile)
	if err != nil {
		return nil
	}
	var raw any
	if jerr := json.Unmarshal(data, &raw); jerr != nil {
		log(fmt.Sprintf("指令文件 %s 解析失败，跳过（保留待人工处理）: %s", b.spec.CmdName, jerr))
		return nil
	}
	cmd, ok := raw.(map[string]any)
	if !ok {
		log(fmt.Sprintf("指令文件 %s 内容不是 JSON 对象，跳过", b.spec.CmdName))
		return nil
	}
	return cmd
}

// cmdStr 取指令字段字符串（缺失/nil → ""，Python str(cmd.get(k) or "") 口径）。
func cmdStr(cmd map[string]any, key string) string {
	v, ok := cmd[key]
	if !ok || v == nil {
		return ""
	}
	s := fmt.Sprintf("%v", v)
	if s == "<nil>" {
		return ""
	}
	return s
}

// snapshot 内网表 → U 盘根（文件名 fileName）。
func (b *tableSyncBackend) snapshot(local, usbDir, fileName string, log func(string)) (map[string]any, error) {
	st, err := os.Stat(local)
	if err != nil || st.IsDir() {
		return nil, fmt.Errorf("内网表不存在: %s（请到%s检查 local_path 配置）", local, b.spec.PageName)
	}
	dest := filepath.Join(usbDir, fileName)
	if err := copyFile(local, dest); err != nil {
		return nil, err
	}
	fi, _ := os.Stat(dest)
	log(fmt.Sprintf("已拷贝内网表 → U盘: %s（%.1f KB）", dest, float64(fi.Size())/1024))
	return map[string]any{"action": "snapshot", "file": dest}, nil
}

// replace U 盘根的新表 → 覆盖内网表（先备份原文件为 <原名>.bak）；
// 覆盖成功后删除 U 盘上的新表（已消费，后续不再使用，防重复执行）。
func (b *tableSyncBackend) replace(local, usbDir, fileName string, log func(string)) (map[string]any, error) {
	src := filepath.Join(usbDir, fileName)
	if st, err := os.Stat(src); err != nil || st.IsDir() {
		return nil, fmt.Errorf("U盘上未找到新表: %s", src)
	}
	if _, err := os.Stat(local); err == nil {
		bak := local + ".bak"
		if err := copyFile(local, bak); err != nil {
			return nil, err
		}
		log(fmt.Sprintf("原内网表已备份: %s", bak))
	}
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return nil, err
	}
	if err := copyFile(src, local); err != nil {
		return nil, err
	}
	fi, _ := os.Stat(local)
	log(fmt.Sprintf("已用 U 盘新表覆盖内网表: %s（%.1f KB）", local, float64(fi.Size())/1024))
	if err := os.Remove(src); err != nil {
		return nil, err
	}
	log(fmt.Sprintf("U盘新表已删除（消费完成）: %s", src))
	return map[string]any{"action": "replace", "file": local, "src_deleted": fileName}, nil
}

// finish 执行成功：删除指令文件并落盘（服务端取回时据指令消失判定已执行）。
func (b *tableSyncBackend) finish(usbDir string, log func(string)) {
	cmdFile := filepath.Join(usbDir, b.spec.CmdName)
	if err := os.Remove(cmdFile); err == nil {
		log(fmt.Sprintf("指令文件已删除: %s", b.spec.CmdName))
	}
	usb.SyncFs(b.UsbOpts)
}

// tick 轮询一次：找 U 盘 → 有指令按 kind 执行；无指令时 nightly 兜底 snapshot。
func (b *tableSyncBackend) tick(j *job.Job, cfg config.Dict, params map[string]any) (map[string]any, error) {
	log := func(s string) { j.Log(s) }
	ts := config.MapField(cfg, b.spec.CfgKey)
	localPath := strings.TrimSpace(config.StrField(ts, "local_path"))
	nightly := config.BoolField(params, "nightly", false)

	j.SetProgress(0, 2, "检测U盘")
	usbCfg := config.MapField(cfg, "usb")
	mount := usb.FindUsbMount(config.StrField(usbCfg, "label"), log, b.UsbOpts)
	if mount == "" {
		// U 盘不在属常态（服务端未挂载），静默返回不算错误
		log("U盘未挂载，本次跳过")
		return map[string]any{"skipped": "usb_absent", "nightly": nightly}, nil
	}
	usbDir := mount

	j.SetProgress(1, 2, "检查指令")
	cmd := b.readCmd(usbDir, log)
	if cmd == nil {
		if !nightly {
			log(fmt.Sprintf("无指令文件（%s），本次跳过", b.spec.CmdName))
			return map[string]any{"skipped": "no_command"}, nil
		}
		// 夜间兜底：无指令也执行一次 snapshot，文件名用配置
		if localPath == "" {
			return nil, fmt.Errorf("未配置内网表路径（%s.local_path）：请到%s选择内网 xlsx 文件",
				b.spec.CfgKey, b.spec.PageName)
		}
		fileName := strings.TrimSpace(config.StrField(ts, "file_name"))
		if fileName == "" {
			fileName = b.spec.DefaultFileName
		}
		log(fmt.Sprintf("夜间兜底：U盘无指令，执行 snapshot（%s）", fileName))
		result, err := b.snapshot(localPath, usbDir, fileName, log)
		if err != nil {
			return nil, err
		}
		result["nightly"] = true
		usb.SyncFs(b.UsbOpts)
		j.SetProgress(2, 2, "完成")
		return result, nil
	}

	kind := cmdStr(cmd, "kind")
	cycleID := cmdStr(cmd, "cycle_id")
	fileName := cmdStr(cmd, "file")
	log(fmt.Sprintf("收到指令: kind=%s cycle_id=%s file=%s", kind, cycleID, fileName))
	if !allowedKinds[kind] {
		// 未知 kind：跳过留痕，不删指令（疑似新版服务端指令，等客户端升级处理）
		log(fmt.Sprintf("未知指令 kind=%q，跳过（cycle_id=%s，指令保留）", kind, cycleID))
		return map[string]any{"skipped": "unknown_kind", "kind": kind, "cycle_id": cycleID}, nil
	}
	if !inRoot(fileName, usbDir) {
		log(fmt.Sprintf("指令 file 字段非法: %q，跳过（指令保留）", fileName))
		return map[string]any{"skipped": "bad_file", "cycle_id": cycleID}, nil
	}
	if localPath == "" {
		// 有指令要执行但配置缺失：失败留痕，指令保留（配置补齐后下一轮 tick 自动补执行）
		return nil, fmt.Errorf("未配置内网表路径（%s.local_path）：请到%s选择内网 xlsx 文件",
			b.spec.CfgKey, b.spec.PageName)
	}

	j.SetProgress(2, 2, "执行指令")
	var (
		result map[string]any
		err    error
	)
	if kind == "snapshot" {
		result, err = b.snapshot(localPath, usbDir, fileName, log)
	} else {
		result, err = b.replace(localPath, usbDir, fileName, log)
	}
	if err != nil {
		return nil, err
	}
	b.finish(usbDir, log)
	result["cycle_id"] = cycleID
	result["kind"] = kind
	log(fmt.Sprintf("指令执行完成: cycle_id=%s kind=%s", cycleID, kind))
	return result, nil
}

// Run 实现 Backend（Python tree_sync.run / crossing_sync.run 同口径）：
// 缺省（GUI「立即执行一次」）= 执行一次 tick；nightly 由定时器 --nightly 传入。
func (b *tableSyncBackend) Run(j *job.Job, params map[string]any) (map[string]any, error) {
	cfg := b.load()
	in := map[string]any{}
	for k, v := range params {
		in[k] = v
	}
	if !config.BoolField(in, "nightly", false) {
		in["tick"] = true
	}
	result, err := b.tick(j, cfg, in)
	if err != nil {
		return nil, err
	}
	if config.BoolField(in, "nightly", false) {
		// 夜间兜底成功（含 U盘不在的跳过）：写成功戳，今日不再重试；
		// 真失败（返回 error）不写戳，由尝试间隔闸控制重试节奏
		extra := map[string]any{}
		if v, ok := result["cycle_id"]; ok {
			extra["cycle_id"] = v
		}
		if v, ok := result["kind"]; ok {
			extra["kind"] = v
		}
		st := b.store()
		st.SetStamp(b.spec.StampNightly, stampData(b.now().Format("2006-01-02"), extra))
	}
	return result, nil
}

// Status 实现 StatusBackend（Python tree_sync.status / crossing_sync.status 同口径）。
func (b *tableSyncBackend) Status(cfg map[string]any) (map[string]any, error) {
	ts := config.MapField(cfg, b.spec.CfgKey)
	usbCfg := config.MapField(cfg, "usb")
	localPath := strings.TrimSpace(config.StrField(ts, "local_path"))
	localExists := false
	if localPath != "" {
		if st, err := os.Stat(localPath); err == nil && !st.IsDir() {
			localExists = true
		}
	}
	fileName := config.StrFieldDefault(ts, "file_name", b.spec.DefaultFileName)
	nightlyTime := config.StrFieldDefault(ts, "nightly_time", "23:55")
	mount := usb.FindUsbMount(config.StrField(usbCfg, "label"), nil, b.UsbOpts)
	return map[string]any{
		b.spec.CfgKey: map[string]any{
			"local_path":   localPath,
			"local_exists": localExists,
			"file_name":    fileName,
			"nightly_time": nightlyTime,
		},
		"stamp":    b.store().GetStamp(b.spec.StampNightly),
		"last_run": b.store().LastRun(b.spec.AppID),
		"usb": map[string]any{
			"label": config.StrField(usbCfg, "label"),
			"mount": mount,
		},
	}, nil
}
