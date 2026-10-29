// Package ocr 是 PP-OCRv4 推理管线的 Go 移植（源：desktop/electron/quizsearch/
// ocr-pipeline.cjs + ocr-worker.cjs，onnxruntime-node 纯 JS 管线），供题库搜题
// quizsearch 调用。流程与嗖嗖搜题所用 Sdcb.PaddleOCR 同族（PP-OCRv4 mobile）：
// det（DBNet 文本检测）→ 行裁剪 → rec（CTC 识别）。cls（0/180 度方向分类）省略：
// 截屏文字不存在倒置（原软件亦关闭 Enable180Classification）。检测后处理为轴对齐
// 简化版：位图膨胀 → 连通域 → 外接矩形 → unclip 外扩，对屏幕水平排布文字与原多边形
// 方案等效，免去轮廓提取/多边形裁剪。
//
// 分层：
//   - 纯函数层（本包）：前处理（BGRA→RGB 平面/双线性 resize/NCHW 归一化）、det 后处理
//     （阈值/膨胀/连通域/unclip/框得分/坐标映射）、rec CTC 解码、词表与常量、帧 ID
//     epoch 编码——全部与 JS 实现逐值对拍（见 *_test.go 金标，Node 离线跑出）；
//   - 会话层（session.go 接口）：det/rec 推理会话抽象，ORT 实现见子包 ort
//     （yalue/onnxruntime_go 运行时加载现有 ONNX Runtime 1.20.1 共享库），测试用 fake；
//   - Engine（engine.go）：常驻 goroutine worker（模型常驻内存、界面不被 CPU 密集计算
//     阻塞）+ epoch 语义（停止/重启作废旧帧结果，id 编码同 JS：epoch*1e6 + seq*2 + 框别）；
//   - Selftest（selftest.go）：分步环境自检（运行时/det 会话/det 零张量推理/rec 会话/
//     rec 零张量推理/字典/golden PNG 识别），每步单独报告 + AVX2 指令集标志，语义对齐
//     worker_diag.cjs「每步都有输出：在哪一步断/报什么错」。
//
// 数值口径与 JS 的差异点均已消除：JS Math.round 为向 +∞ 取整（floor(x+0.5)），本包
// jsRound 同口径；resize 权重 wy 取自钳位前的 y0（JS 既有行为），逐值对拍保持一致。
package ocr
