# SSO 自动登录 —— 已补齐，无需再操作

SSO 登录流程已完全逆向并实现在 `vehicle_export.py` 的 `sso_login()` 中：

- 防重放签名：`sign = SM3("requestTime,nonce")`
- 密码加密：`SM3(密码) + 8位随机串 + 密码`，再 SM2(ASN.1 DER) 加密；公钥由脚本运行时从登录页自动提取
- userTicket、execution 均自动处理

使用方式：`config.ini` 里 `mode = sso` 并填入用户名（密码建议留空运行时输入），或命令行 `python3 vehicle_export.py --auth sso --month 2026-09`。

细节见《接口分析.md》第六节。本文件留档，无需任何操作。
