# OCR 模型来源与校验

PP-OCRv4 mobile（与嗖嗖搜题 Sdcb.PaddleOCR 所用 ChineseV4 同族），取自 RapidOCR 官方发行：

- 来源：`rapidocr_onnxruntime-1.4.4-py3-none-any.whl`（PyPI）内 `rapidocr_onnxruntime/models/`
- ppocr_keys.txt：rec.onnx 模型 metadata（`character` 字段）内嵌字典，经 `../tools/extract_keys.cjs` 提取（6623 字）

| 文件 | 用途 | SHA256 |
|---|---|---|
| det.onnx | DBNet 文本检测 | d2a7720d45a54257208b1e13e36a8479894cb74155a5efe29462512d42f49da9 |
| rec.onnx | CTC 文字识别 | 48fc40f24f6d2a207a2b1091d3437eb3cc3eb6b676dc3ef9c37384005483683b |
| cls.onnx | 0/180° 方向分类（当前管线未启用，保留备用） | e47acedf663230f8863ff1ab0e64dd2d82b838fceb5957146dab185a89d6215c |
| ppocr_keys.txt | 识别字典 | 28b2362ad4ab2dc38769aa72feb535e3a9ddb3fd2a7585a05920e6393b1dc7f7 |

更换模型后重跑 `node electron/quizsearch/tools/extract_keys.cjs` 重新提取字典，并更新本表。
