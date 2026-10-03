# Shade 壹匣后端镜像（后端 API + 网页端同端口托管）
# 构建：阿里云容器镜像服务「代码变更自动构建」（上下文为仓根、Dockerfile 即本文件），
#       推 tag release-vX.Y.Z 自动构建镜像 :X.Y.Z；本地手动构建：docker build -t j1toolkit:dev .

# ---------- 依赖阶段：严格按 lockfile 安装生产依赖 ----------
FROM node:20-bookworm-slim AS deps
WORKDIR /app/server
# ACR 国内构建机走 npmmirror 更快；可在构建参数覆盖（--build-arg NPM_REGISTRY=...）
ARG NPM_REGISTRY=https://registry.npmmirror.com
COPY server/package.json server/package-lock.json ./
RUN npm config set registry "$NPM_REGISTRY" \
 && npm ci --omit=dev

# ---------- 运行阶段 ----------
FROM node:20-bookworm-slim
ENV NODE_ENV=production \
    TZ=Asia/Shanghai \
    PORT=3000
# LibreOffice：安全日记录多文件含非 PDF 时转 PDF 再合并（src/safeday/convert.js 调 soffice）；
# fonts-noto-cjk：转换中文文档排版所需；tzdata：TZ=Asia/Shanghai 生效（打卡/日志时间戳依赖）
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      libreoffice-writer libreoffice-calc libreoffice-impress \
      fonts-noto-cjk tzdata curl \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /app/server
COPY --from=deps /app/server/node_modules ./node_modules
COPY server/package.json ./
COPY server/src ./src
COPY server/public ./public
COPY server/assets ./assets
# 可写数据目录（安全日记录等，见 src 各 data/ 写入点）：运行时必须挂卷持久化，
# 如 -v /opt/j1toolkit/data:/app/server/data
RUN mkdir -p /app/server/data
VOLUME ["/app/server/data"]
EXPOSE 3000
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s \
  CMD curl -fsS "http://127.0.0.1:${PORT}/healthz" || exit 1
# 与 package.json 的 start 脚本同口径（node src/index.js）；全部配置从环境变量读取
CMD ["node", "src/index.js"]
