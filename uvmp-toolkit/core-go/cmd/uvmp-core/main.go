// uvmp-core 内网工具箱核心（移植自 toolkit/app.py 入口）：CLI 与 stdio JSON-RPC 服务。
//
// 用法：
//
//	uvmp-core                          打印 GUI 迁移提示（exit 2）
//	uvmp-core daily-export [--if-due] [--date YYYY-MM-DD]
//	                                   每日派车单同步（定时器调用；--if-due 时做 due-check 判定）
//	uvmp-core tree-sync [--tick|--nightly] [--if-due]
//	                                   树障统计表同步（U盘指令轮询 / 夜间兜底）
//	uvmp-core crossing-sync [--tick|--nightly] [--if-due]
//	                                   交跨统计表同步（契约同 tree-sync）
//	uvmp-core order-export --outdir 目录 (--xlsx 清单.xlsx | --codes "单号1,单号2")
//	                                   [--type pcd|track|all] [--no-batch-dir] [--calibrate-noon]
//	                                   按派车单号按序导出（无头模式，供计划任务/调试）
//	uvmp-core selfcheck                自检：加载全部应用后端（构建冒烟用）
//	uvmp-core rpc                      stdio JSON-RPC 服务（Electron 壳 spawn 调用）
//
// 退出码：0 成功/未到点；1 失败；2 GUI 依赖缺失。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/apps"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/job"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/rpc"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/schedule"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
)

// nowTime 当前时刻（CLI due-check 用）。
func nowTime() time.Time { return time.Now() }

func main() {
	cfg := config.Load()
	// 启动自洁：清理 30 天前的任务日志/档案（rpc/daily-export/order-export 各入口均经此处）
	func() {
		defer func() { _ = recover() }()
		state.PruneOldLogs(30)
	}()
	apps.Builtin()

	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}
	var code int
	switch cmd {
	case "", "gui":
		code = cmdGUI()
	case "selfcheck":
		code = cmdSelfcheck()
	case "rpc":
		rpc.New(os.Stdout).Serve(os.Stdin)
		code = 0
	case "daily-export":
		code = cmdDailyExport(cfg, args)
	case "tree-sync":
		code = cmdTableSync(cfg, "tree_sync", args)
	case "crossing-sync":
		code = cmdTableSync(cfg, "crossing_sync", args)
	case "order-export":
		code = cmdOrderExport(cfg, args)
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n", cmd)
		usage()
		code = 2
	}
	os.Exit(code)
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法: uvmp-core [gui|selfcheck|rpc|daily-export|tree-sync|crossing-sync|order-export] ...")
}

// cmdGUI 桌面客户端已迁移到 Electron 版（desktop/，安装包见 Release）；
// 本入口（uvmp-core）仅保留 CLI 与 RPC 服务能力。
func cmdGUI() int {
	fmt.Println("GUI 已迁移到 Electron 桌面客户端（见 Release 的安装包/桌面端源码 desktop/）。")
	fmt.Println("本程序仅提供命令行与 RPC 能力：daily-export / order-export / selfcheck / rpc")
	return 2
}

// runHeadless 无头执行一次应用任务（日志同步 stdout），返回退出码。
func runHeadless(appID, title string, params map[string]any) int {
	app := apps.Get(appID)
	if app == nil || app.Backend == nil {
		fmt.Fprintf(os.Stderr, "应用不存在或后端加载失败: %s\n", appID)
		return 1
	}
	mgr := job.NewManager(state.Default())
	j := mgr.Create(appID, title)
	mgr.RunSync(j, func(j2 *job.Job) (map[string]any, error) {
		return app.Backend.Run(j2, params)
	}, true)
	fmt.Println(strings.Repeat("-", 50))
	fmt.Printf("任务状态：%s\n", j.Status)
	if len(j.Result) > 0 {
		b, _ := json.MarshalIndent(j.Result, "", " ")
		fmt.Printf("结果：%s\n", b)
	}
	if j.Status == job.StatusSuccess {
		return 0
	}
	return 1
}

// cmdDailyExport 每日派车单同步。
func cmdDailyExport(cfg config.Dict, args []string) int {
	fs := flag.NewFlagSet("daily-export", flag.ContinueOnError)
	ifDue := fs.Bool("if-due", false, "先按配置时间做 due-check 判定")
	date := fs.String("date", "", "导出日期 YYYY-MM-DD（默认今天）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *ifDue {
		due, reason, today := schedule.DueCheck(cfg, nowTime(), "os", "", nil)
		if !due {
			fmt.Printf("[daily-export] 跳过：%s\n", reason)
			return 0
		}
		schedule.MarkAttempt(today, "") // 分钟级 tick 下拉开失败重试间隔
	}
	return runHeadless("daily_sync", "每日派车单同步", map[string]any{"date": *date})
}

// cmdTableSync 树障/交跨统计表同步：--tick 每分钟轮询 U盘指令（不做 due_check，U盘不在秒退）；
// --nightly --if-due 夜间兜底（到点判定后执行，无指令也 snapshot 一次）。
func cmdTableSync(cfg config.Dict, appID string, args []string) int {
	fs := flag.NewFlagSet(appID, flag.ContinueOnError)
	tick := fs.Bool("tick", false, "轮询一次 U盘指令（默认；每分钟定时器用）")
	nightly := fs.Bool("nightly", false, "夜间兜底：无指令也 snapshot 一次")
	ifDue := fs.Bool("if-due", false, "仅配合 --nightly：先按 nightly_time 做 due-check 判定")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	_ = tick
	title := "树障统计表同步"
	if appID == "crossing_sync" {
		title = "交跨统计表同步"
	}
	if *nightly {
		if *ifDue {
			ts := config.MapField(cfg, appID)
			if strings.TrimSpace(config.StrField(ts, "local_path")) == "" {
				fmt.Printf("[%s] 跳过：未配置内网表路径（%s.local_path）\n", appID, appID)
				return 0
			}
			sch := schedule.Schedule{
				Enabled: true,
				Time:    config.StrFieldDefault(ts, "nightly_time", "23:55"),
				Driver:  config.StrFieldDefault(config.MapField(cfg, "schedule"), "driver", "os"),
			}
			stamp := appID + "_nightly"
			due, reason, today := schedule.DueCheck(cfg, nowTime(), "os", stamp, &sch)
			if !due {
				fmt.Printf("[%s] 跳过：%s\n", appID, reason)
				return 0
			}
			schedule.MarkAttempt(today, stamp)
		}
		return runHeadless(appID, title, map[string]any{"nightly": true})
	}
	return runHeadless(appID, title, map[string]any{"tick": true})
}

// cmdOrderExport 按派车单号按序导出。
func cmdOrderExport(cfg config.Dict, args []string) int {
	fs := flag.NewFlagSet("order-export", flag.ContinueOnError)
	xlsxPath := fs.String("xlsx", "", "派车单号清单 xlsx 路径")
	codes := fs.String("codes", "", "直接给单号（逗号/空格分隔）")
	outdir := fs.String("outdir", "", "输出目录")
	typ := fs.String("type", "all", "导出类型 pcd|track|all")
	noBatchDir := fs.Bool("no-batch-dir", false, "不建批次子目录，直接写入输出目录")
	calibrateNoon := fs.Bool("calibrate-noon", false,
		"自动校准行程结束时间：早于12点的行程按1小时逐次叠加至12点后，行驶时间同步增加")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*outdir) == "" {
		fmt.Fprintln(os.Stderr, "请用 --outdir 指定输出目录")
		return 2
	}
	if *typ != "pcd" && *typ != "track" && *typ != "all" {
		fmt.Fprintln(os.Stderr, "--type 只能是 pcd|track|all")
		return 2
	}
	params := map[string]any{
		"outdir":         *outdir,
		"batch_subdir":   !*noBatchDir,
		"batch_name":     "派车单",
		"export_pcd":     *typ == "pcd" || *typ == "all",
		"export_track":   *typ == "track" || *typ == "all",
		"calibrate_noon": *calibrateNoon,
	}
	switch {
	case *xlsxPath != "":
		params["xlsx_path"] = *xlsxPath
		params["batch_name"] = strings.TrimSuffix(filepath.Base(*xlsxPath), filepath.Ext(*xlsxPath))
	case *codes != "":
		params["codes_text"] = *codes
	default:
		fmt.Fprintln(os.Stderr, "请用 --xlsx 或 --codes 提供派车单号来源")
		return 2
	}
	return runHeadless("pcd_export", "派车单·轨迹导出", params)
}

// cmdSelfcheck 构建冒烟：加载全部应用后端（覆盖装配链）。
func cmdSelfcheck() int {
	all := apps.All()
	if len(all) == 0 {
		fmt.Println("selfcheck 失败：未发现任何应用")
		return 1
	}
	ok := true
	for _, a := range all {
		if a.Backend != nil {
			fmt.Printf("  后端 OK: %s\n", a.ID)
		} else {
			fmt.Printf("  后端缺失: %s\n", a.ID)
			ok = false
		}
	}
	if ok {
		fmt.Println("selfcheck 通过")
		return 0
	}
	fmt.Println("selfcheck 失败")
	return 1
}
