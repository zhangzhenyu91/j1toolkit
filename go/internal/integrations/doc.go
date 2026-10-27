// Package integrations 外部服务适配层：配置热读取、Dify 工作流客户端、腾讯云 COS V5 对象存储、
// 高德「地点 + 天气」地理服务。协议对齐 server/src 真实实现（utils/dify.js、worklog/cos.js、
// worklog/dify.js、worklog/geo.js、safeday/dify.js；COS 签名对齐 cos-nodejs-sdk-v5 util.getAuth）。
//
// 装配约定（供 main 接线）：
//
//	cfg := integrations.NewRuntimeProvider(configStore) // 库内运行时配置优先、env 兜底，每次调用热读取
//	cos := &integrations.COSStore{Cfg: cfg}             // 满足 worklog.ObjectStore / safeday.ObjectStore
//	geo := &integrations.AMapService{Cfg: cfg}          // 满足 worklog.GeoService
//	dify := &integrations.WorkflowClient{Cfg: cfg, APIKeyKey: "DIFY_WORKLOG_API_KEY"}
//	photo := integrations.NewPhotoVerifier(dify)         // 满足 worklog.DifyClient
//	stream := &integrations.StreamRunner{Client: &integrations.WorkflowClient{
//		Cfg: cfg, APIKeyKey: "DIFY_SAFEDAY_API_KEY"}}    // 满足 safeday.WorkflowRunner
//
// 可选事件写入：三个客户端均可注入 Recorder（*logging.Logger 直接满足），外部调用成功/失败
// 写中文结构化事件（category=external，异常带 error_class 分类、落库前脱敏不泄密）；nil 默认不写。
//
// 全部密钥经 Provider 热读取（或构造字段注入），不硬编码；测试以 httptest 假服务验证，
// 不接触真实外部服务。瞬时传输故障的有界重试集中在 RetryPolicy 一处（WorkflowClient 默认
// 3 次总尝试），调用方不得再叠加重试，避免多层重试连乘；接线到 worklog.recognize 这类自带
// 业务重试的调用点时，建议把 Retry.MaxAttempts 设为 1～2 收住总预算。
package integrations
