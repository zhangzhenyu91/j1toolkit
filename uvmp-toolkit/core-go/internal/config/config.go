// Package config 配置加载与写回（移植自 toolkit/config.py）。
//
// 优先级（低到高）：内置默认值 < config.ini（只读引用，保持 CLI 版行为） < config.json（GUI 可写）。
//   - config.json 路径可用环境变量 UVMP_TOOLKIT_CONFIG 覆盖（root 定时器经 Environment 指定用户的配置文件）
//   - config.json 存放可调项与 SSO 凭据，写入后置 600 权限；不入仓（模板见 config.example.json）
//   - config.ini 仍是导出底层参数（网关/筛选/render/map）的来源，本模块只读不写
//
// 配置字典形状与 Python config.load() 完全一致（含 _ini 原始 ini 段、_config_path），
// 可直接喂 auth.ParseConfig / exporter.ParseRunConfig。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Dict 配置字典（Python config.load() 的返回形状）。
type Dict = map[string]any

// APP_DIR_NAME 用户配置/状态目录名（与 Python 侧一致）。
const APP_DIR_NAME = "uvmp-toolkit"

// Defaults 返回内置默认值（deep copy，调用方可随意改）。
// 键位与 Python config.DEFAULTS 逐项一致。
func Defaults() Dict {
	return Dict{
		"sso": Dict{"username": "", "password": ""}, // 留空则回退读 config.ini [auth]
		"schedule": Dict{
			"enabled": true,
			"time":    "09:15",
			"driver":  "os", // os=系统定时器执行（默认）；app=客户端驻留执行
		},
		"ui": Dict{
			"close_to_tray": true, // 关窗最小化到托盘（驻留调度的前提）
			"autostart":     false,
		}, // 开机自启（驻留调度建议开启）
		"usb": Dict{
			"label":        "GLKVM",
			"fallback_dir": "",
		}, // fallback_dir 为空时找不到 U 盘即失败
		"daily_export": Dict{"temp_dir": ""}, // 空=~/临时下载（保持旧脚本习惯）
		"tree_sync": Dict{ // 树障统计表同步（U盘指令 snapshot/replace）
			"local_path":   "",               // 内网树障统计表 xlsx 完整路径（必配）
			"file_name":    "检修一班树木统计表.xlsx", // 夜间兜底 snapshot 上盘用的文件名
			"nightly_time": "23:55",          // 夜间兜底执行时刻（服务端 23:50 挂载 U盘）
		},
		"crossing_sync": Dict{ // 交跨统计表同步（U盘指令 snapshot/replace，契约同 tree_sync）
			"local_path":   "",               // 内网交跨统计表 xlsx 完整路径（必配）
			"file_name":    "检修一班交跨统计表.xlsx", // 夜间兜底 snapshot 上盘用的文件名
			"nightly_time": "23:55",          // 夜间兜底执行时刻（服务端 23:50 挂载 U盘）
		},
		"order_export": Dict{
			"default_outdir": "",   // 空=项目目录/output
			"lookback_days":  62,   // 无日期提示时按单号兜底查询的回看天数
			"batch_subdir":   true, // 在输出目录下建批次子目录
			"query_workers":  4,    // Go 导出工作进程查询并发（合理范围由 Go 侧钳制）
			"render_workers": 2,    // Go 导出工作进程渲染并发（合理范围由 Go 侧钳制）
		},
		"dev": Dict{"mock": false}, // 开发用：假数据替代真实接口
		"render": Dict{
			"chrome":                    "",      // 浏览器路径（用户手选的公司浏览器；空=自动探测）
			"builtin":                   true,    // 内置浏览器优先（安装包自带，生产不依赖宿主 Chrome）
			"ignore_certificate_errors": false,   // 忽略 TLS 证书错误（内网自签证书临时排障用）
			"certificate_spki_pins":     []any{}, // 证书 SPKI 固定清单（sha256/SPKI-base64）
		},
		"browser_automation": Dict{
			"cdp_url":      "http://127.0.0.1:9222", // 仅允许本机回环地址
			"profile_dir":  "",                      // 空=用户状态目录/browser-automation/profile
			"start_url":    "",                      // 打开受控浏览器后自动访问；空=空白页
			"step_timeout": 120,
		},
		"course_autoplay": Dict{"max_attempts": 3}, // 达到单课尝试上限后跳过并继续
	}
}

// ConfigPath config.json 路径解析（优先级从高到低，与 Python config.config_path 一致）：
//  1. 环境变量 UVMP_TOOLKIT_CONFIG（root 定时器经 unit 的 Environment 指定）
//  2. 用户配置目录（linux ~/.config/uvmp-toolkit/，Windows %APPDATA%\uvmp-toolkit\）
//     ——安装包升级会清空安装目录，exe 旁的 config.json 只作旧版遗留处理：读到即迁移到用户目录
func ConfigPath() string {
	if env := os.Getenv("UVMP_TOOLKIT_CONFIG"); env != "" {
		return env
	}
	base := userConfigBase()
	userCfg := filepath.Join(base, APP_DIR_NAME, "config.json")
	if _, err := os.Stat(userCfg); err == nil {
		return userCfg
	}
	beside := filepath.Join(exeDir(), "config.json")
	if _, err := os.Stat(beside); err == nil {
		// 旧版遗留：迁移到用户目录（与 Python 冻结版行为一致）
		if err := os.MkdirAll(filepath.Dir(userCfg), 0o755); err == nil {
			if data, rerr := os.ReadFile(beside); rerr == nil {
				if werr := os.WriteFile(userCfg, data, 0o600); werr == nil {
					_ = os.Chmod(userCfg, 0o600)
				}
			}
		}
	}
	return userCfg
}

// IniPath config.ini 路径：exe 旁的优先（用户可改），否则源码树项目根；
// 环境变量 UVMP_TOOLKIT_CONFIG_INI 可显式覆盖（开发/测试用）。不存在时返回 ""。
func IniPath() string {
	if env := os.Getenv("UVMP_TOOLKIT_CONFIG_INI"); env != "" {
		return env
	}
	if p := filepath.Join(exeDir(), "config.ini"); fileExists(p) {
		return p
	}
	// 源码树运行：构建产物在 core-go/ 或 core-go/bin/，config.ini 在项目根（上一级或上两级）
	dir := exeDir()
	for i := 0; i < 2; i++ {
		dir = filepath.Dir(dir)
		if p := filepath.Join(dir, "config.ini"); fileExists(p) {
			return p
		}
	}
	return ""
}

// DeepMerge 深合并：over 覆盖 base，嵌套 dict 递归合并（Python config.deep_merge 同口径）。
func DeepMerge(base, over Dict) Dict {
	out := Dict{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		om, oOk := v.(Dict)
		bm, bOk := out[k].(Dict)
		if oOk && bOk {
			out[k] = DeepMerge(bm, om)
		} else {
			out[k] = v
		}
	}
	return out
}

// Load 加载合并后的完整配置（Python config.load 同口径）。
// config.json 解析失败时打印提示并使用默认值（与 Python 行为一致）。
func Load() Dict {
	cfg := DeepMerge(Defaults(), nil)
	cfg["_ini"] = ReadINI(IniPath())
	p := ConfigPath()
	if data, err := os.ReadFile(p); err == nil {
		var over Dict
		if jerr := json.Unmarshal(data, &over); jerr != nil {
			fmt.Printf("[config] config.json 解析失败（%s），使用默认值\n", jerr)
		} else {
			cfg = DeepMerge(cfg, over)
		}
	}
	cfg["_config_path"] = p
	return cfg
}

// Save 把 GUI 可写项合并写回 config.json（600 权限），返回写后的文件内容。
// 与 Python config.save 一致：_ini / _config_path 不落盘。
func Save(updates Dict) (Dict, error) {
	p := ConfigPath()
	cur := Dict{}
	if data, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(data, &cur) // 解析失败按空处理（与 Python 一致）
	}
	cur = DeepMerge(cur, updates)
	delete(cur, "_ini")
	delete(cur, "_config_path")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	out, err := marshalPretty(cur)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, []byte(out), 0o600); err != nil {
		return nil, err
	}
	_ = os.Chmod(p, 0o600) // Windows 无 Unix 权限位，忽略失败（与 Python 一致）
	return cur, nil
}

// marshalPretty JSON 序列化：indent=2、保留非 ASCII 原文、结尾补换行
// （Python json.dumps(cur, ensure_ascii=False, indent=2) + "\n" 同口径）。
func marshalPretty(v any) (string, error) {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// ---------------------------------------------------------------- 字段取值工具

// StrField 取 dict 字符串值（非字符串按空串）。
func StrField(m Dict, key string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

// StrFieldTrim 取 dict 字符串值并裁剪空白。
func StrFieldTrim(m Dict, key string) string {
	return strings.TrimSpace(StrField(m, key))
}

// MapField 取嵌套 dict；缺失或类型不符返回 nil。
func MapField(m Dict, key string) Dict {
	if m == nil {
		return nil
	}
	sub, _ := m[key].(Dict)
	return sub
}

// ListField 取列表值；缺失或类型不符返回 nil。
func ListField(m Dict, key string) []any {
	if m == nil {
		return nil
	}
	l, _ := m[key].([]any)
	return l
}

// BoolField 宽松布尔：bool 直取；字符串/数值解析；缺省取 def。
func BoolField(m Dict, key string, def bool) bool {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off":
			return false
		}
	case float64:
		return t != 0
	case int:
		return t != 0
	case json.Number:
		f, err := t.Float64()
		if err == nil {
			return f != 0
		}
	}
	return def
}

// IntField 宽松整数：数值/字符串解析；缺省取 def。
func IntField(m Dict, key string, def int) int {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return int(f)
		}
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(t), "%d", &n); err == nil {
			return n
		}
	}
	return def
}

// ---------------------------------------------------------------- 内部工具

func exeDir() string {
	p, err := os.Executable()
	if err != nil {
		return "."
	}
	if resolved, rerr := filepath.EvalSymlinks(p); rerr == nil {
		p = resolved
	}
	return filepath.Dir(p)
}

func userConfigBase() string {
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			return appData
		}
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "AppData", "Roaming")
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return xdg
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}
