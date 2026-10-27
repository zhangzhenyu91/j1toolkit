// 编译期接口兼容检查：主装配按各应用接口注入本包实现，签名漂移在此暴露。
package integrations

import (
	"shade/internal/apps/safeday"
	"shade/internal/apps/treehazard"
	"shade/internal/apps/worklog"
	"shade/internal/config"
)

var (
	// 对象存储：worklog 与 safeday / treehazard 同一 COSStore 全兼容。
	_ worklog.ObjectStore    = (*COSStore)(nil)
	_ safeday.ObjectStore    = (*COSStore)(nil)
	_ treehazard.ObjectStore = (*COSStore)(nil)
	// 地理服务与 Dify 适配。
	_ worklog.GeoService     = (*AMapService)(nil)
	_ worklog.DifyClient     = (*PhotoVerifier)(nil)
	_ safeday.WorkflowRunner = (*StreamRunner)(nil)
	// 配置热读取接口互认（Provider 与两应用 ConfigReader 同签名；库内 config.Store 可直插）。
	_ safeday.ConfigReader = (Provider)(nil)
	_ worklog.ConfigReader = (Provider)(nil)
	_ Provider             = (*config.Store)(nil)
	_ Provider             = (*MapProvider)(nil)
	_ Provider             = (*EnvProvider)(nil)
	_ Provider             = (*ChainProvider)(nil)
	_ Provider             = FuncProvider(nil)
)
