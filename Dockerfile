# Shade 壹匣后端镜像（Go 服务 + Vue 网页端同端口托管；Go/Vue 重写口径，旧 Node 服务端不进镜像）
# 构建：GitHub Actions 发版产线（仓根 .github/workflows/release.yml，上下文为仓根、Dockerfile 即本文件），
#       推 tag release-vX.Y.Z：GitHub Actions 原生构建 amd64/arm64 推 Docker Hub；
#       CNB 国内重建推腾讯云 TCR + CNB 制品库（见 .cnb.yml）；
#       本地手动构建：docker build -t j1toolkit:dev .
# 多架构：Go 由 TARGETARCH 交叉编译（CGO_ENABLED=0 无原生依赖，amd64/arm64 同一产线）。

# ---------- Go 编译阶段（交叉编译，构建机原生工具链） ----------
FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm AS gobuild
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src
COPY go/go.mod go/go.sum ./go/
RUN cd go && go mod download
COPY go/ ./go/
RUN cd go && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/shade ./cmd/shade

# ---------- web 构建阶段（npm ci 严格按 lockfile，产出 dist/） ----------
FROM --platform=$BUILDPLATFORM node:24-bookworm-slim AS webbuild
WORKDIR /src/web
# CNB 国内构建机走 npmmirror 更快；可在构建参数覆盖（--build-arg NPM_REGISTRY=...）
ARG NPM_REGISTRY=https://registry.npmmirror.com
COPY web/package.json web/package-lock.json ./
RUN npm config set registry "$NPM_REGISTRY" \
 && npm ci
COPY web/ ./
RUN npm run build

# ---------- 运行阶段（Debian slim：LibreOffice + 中文字体 + tzdata，无 Node） ----------
FROM debian:bookworm-slim
ENV TZ=Asia/Shanghai \
    PORT=3000 \
    SHADE_WEB_DIR=/app/web/dist \
    SHADE_LEGACY_PUBLIC_DIR=/app/server/public \
    WORKLOG_ASSETS_DIR=assets/worklog
# LibreOffice：安全日记录多文件含非 PDF 时转 PDF 再合并（safeday 调 soffice）；
# fonts-noto-cjk：转换中文文档排版所需；tzdata：TZ=Asia/Shanghai 生效（打卡/日志时间戳依赖）；
# curl：健康检查探针；ca-certificates：Go 的 HTTPS/WSS 系统证书链验证
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      libreoffice-writer libreoffice-calc libreoffice-impress \
      fonts-noto-cjk tzdata curl ca-certificates \
 && update-ca-certificates \
 && rm -rf /var/lib/apt/lists/*
# 数据/素材相对路径（./data/…、assets/…）以 /app/server 为基准：
# server/assets → ./assets（字体 assets/fonts、模板 assets/worklog、assets/safeday/…）
WORKDIR /app/server
COPY --from=gobuild /out/shade /usr/local/bin/shade
COPY server/assets ./assets
COPY server/public/sgcc-captcha.html ./public/sgcc-captcha.html
# KVM 设备一键接入素材（install.sh / fileshare.py / S99fileshare / j1-theme.css / extras 两文件）：
# 由 /kvm-device/ 同端口公开托管，安装命令 curl -fsSL https://<域名>/kvm-device/install.sh | sh
COPY server/public/kvm-device ./public/kvm-device
# Vue 网页端构建产物：SHADE_WEB_DIR 指向（safeday.Renderer.TemplatePath=assets/safeday/… 同口径）
COPY --from=webbuild /src/web/dist /app/web/dist
# 可写数据目录（安全日记录、树障/交跨收件箱、缩略图缓存等，见 go 各 data/ 写入点）：
# 运行时必须挂卷持久化，如 -v /opt/j1toolkit/data:/app/server/data
RUN mkdir -p /app/server/data
VOLUME ["/app/server/data"]
EXPOSE 3000
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s \
  CMD curl -fsS "http://127.0.0.1:${PORT}/healthz" || exit 1
# 与 go/cmd/shade 装配同口径（监听 0.0.0.0:$PORT，单端口托管 API + /healthz + 网页端）；
# 全部配置从环境变量/运行配置读取
CMD ["shade"]
