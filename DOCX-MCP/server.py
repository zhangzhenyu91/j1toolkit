#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
docx-mcp 的 HTTP 传输启动封装。

官方默认启动方式是 stdio（仅供本机 MCP 客户端使用），
Dify 远程调用需要 HTTP 传输，本脚本以 Streamable HTTP（默认）
或 SSE 模式启动 docx-mcp。

环境变量：
  MCP_TRANSPORT  传输协议：streamable-http（默认）| sse
  MCP_HOST       监听地址，默认 0.0.0.0
  MCP_PORT       监听端口，默认 8000
  MCP_PATH       端点路径，streamable-http 默认 /mcp/，sse 通常为 /sse/

文件路径约定：
  进程工作目录为 /app（Docker 内），项目的 docs/ 挂载在 /app/docs，
  因此 Dify 调用工具时 file_path 传 "docs/2025.07.29.docx" 这类相对路径，
  文件就会写入网站项目的 docs/ 目录，网站的记录检测会自动识别。
"""

import inspect
import os

from final_complete_server import mcp

TRANSPORT = os.environ.get("MCP_TRANSPORT", "streamable-http")
HOST = os.environ.get("MCP_HOST", "0.0.0.0")
PORT = int(os.environ.get("MCP_PORT", "8000"))
PATH = os.environ.get("MCP_PATH", "/mcp/")

if __name__ == "__main__":
    kwargs = {"transport": TRANSPORT, "host": HOST, "port": PORT}
    # 兼容不同 fastmcp 版本：仅当 run() 支持 path 参数时传入
    if "path" in inspect.signature(mcp.run).parameters:
        kwargs["path"] = PATH
    print(f"docx-mcp 启动中: transport={TRANSPORT} {HOST}:{PORT}{PATH}", flush=True)
    mcp.run(**kwargs)
