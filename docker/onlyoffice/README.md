# ONLYOFFICE Docs 对接与品牌替换（用户自装容器）

主仓 `docker/docker-compose.yml` 只含 Shade 服务端本身；ONLYOFFICE 编辑器容器由部署者按本说明自装，
加入 `j1toolkit` 容器网络即可与 Shade 按容器名互通。对接原理与环境变量见 `开发指南.md` 第十八节。

## 一、启动 ONLYOFFICE 容器（参考片段）

```yaml
# 部署者自行维护的 compose（或并入自己的总 compose），不随主仓 docker-compose.yml 下发
networks:
  j1toolkit:
    external: true

services:
  onlyoffice:
    image: onlyoffice/documentserver:10.0 # 锁版本，不用 latest
    container_name: onlyoffice # Shade 侧 OO_INTERNAL_URL 默认按此容器名互通
    restart: always
    networks:
      - j1toolkit
    ports:
      - '9980:80' # 浏览器须可达（经反代上 HTTPS，见下）
    environment:
      JWT_ENABLED: 'true'
      JWT_SECRET: ${OO_JWT_SECRET} # 与 Shade .env 的 OO_JWT_SECRET 同值
      JWT_HEADER: Authorization
    volumes:
      - ./onlyoffice/data:/var/lib/onlyoffice
      - ./onlyoffice/logs:/var/log/onlyoffice
      - ./onlyoffice/fonts:/usr/share/fonts/truetype/custom:ro # 缺中文字体时放 ttf 进此目录后重启
      # —— 品牌替换（Shade 壹匣）：下列容器内路径以 10.0 实测为准，先跑「二、路径发现」再取消注释 ——
      # - <本仓>/docker/onlyoffice/brand/header-logo.png:<容器内 header logo 路径>:ro
      # - <本仓>/docker/onlyoffice/brand/loader-logo.png:<容器内加载页 logo 路径>:ro
      # - <本仓>/docker/onlyoffice/brand/about-logo.png:<容器内关于框 logo 路径>:ro
      # - <本仓>/docker/onlyoffice/brand/favicon.ico:<容器内 favicon 路径>:ro
```

配套 Shade `.env`：`OO_PUBLIC_URL`（浏览器可达的 HTTPS 地址，如反代 `https://office.j1net.com → 127.0.0.1:9980`）、
`OO_JWT_SECRET`（同上同值）；`OO_INTERNAL_URL` / `OO_CALLBACK_BASE` 有默认值，一般无需填（`OO_CALLBACK_BASE`
缺省按 `http://j1toolkit:<PORT>` 推导——Shade compose 容器名 + 内外同端口，`PORT` 缺省 3000，仅当 Shade 不以
`j1toolkit` 容器名入同一网络时才需显式填写）。

## 二、品牌资源路径发现（每次升级 OnlyOffice 版本后复核一次）

容器首启后枚举各编辑器应用的图片资源目录，找到 logo / 加载图 / 关于框图 / favicon 的实际文件名：

```bash
docker exec onlyoffice find /var/www/onlyoffice/documentserver/web-apps -maxdepth 6 \
  -iname "*logo*" -o -iname "*favicon*" | sort
```

通常涉及 `apps/common/main/resources/img/` 与各编辑器（word/cell/slide）`main/resources/img/` 下的
header logo（深浅色各一）、加载页大图、关于框图。把确认过的目标逐个写成上面的只读挂载，重启容器生效。

素材规格（`brand/` 目录已按此生成）：

| 文件                          | 尺寸          | 用途                        |
| ----------------------------- | ------------- | --------------------------- |
| `header-logo.png`             | 172×40        | 编辑器左上标（浅色 header） |
| `header-logo-dark.png`        | 172×40        | 同上（深色 header，白字）   |
| `loader-logo.png`             | 432×160       | 文档加载页                  |
| `about-logo.png`              | 432×70        | 关于对话框                  |
| `favicon.png` / `favicon.ico` | 64×64 / 32×32 | 页签图标                    |

素材改动后重新生成（依赖 server/node_modules，须先 `cd server && npm i`）：

```bash
node docker/onlyoffice/brand/render.js
```

## 三、插件安装（自托管社区版：插件市场列表有限，AI 等插件手动装）

自托管社区版的「插件 → 插件管理器」市场列表不全（AI 插件不在其中，2026-10 实测），手动安装：

```bash
# 插件源码仓：github.com/ONLYOFFICE/onlyoffice.github.io，插件在 sdkjs-plugins/content/<名>/
# 以 AI 插件为例（下载仓 zip 后取该目录，或 git clone 后拷出）：
docker cp ./ai onlyoffice-documentserver:/var/www/onlyoffice/documentserver/sdkjs-plugins/ai
docker exec onlyoffice-documentserver supervisorctl restart all   # 或 docker restart 容器
```

装好后编辑器「插件」标签页出现对应图标。AI 插件还需在其设置面板里配置服务商
（OpenAI 兼容接口 + API Key；内网可指 Ollama 等自建推理，官方 FAQ：
onlyoffice.com/blog/2025/03/faq-how-to-use-ai-in-onlyoffice）。

注意：插件落在容器内目录，**重建容器即丢**——插件目录应随部署包留存（同 `docker/glkvm/jump.html` 的教训），
或挂卷 `./onlyoffice/plugins:/var/www/onlyoffice/documentserver/sdkjs-plugins` 持久化。

## 四、法务备注

ONLYOFFICE Docs 社区版为 AGPL v3；官方把界面白标（`customization.logo/customer` 等配置项）列为
Developer Edition 付费能力，社区版上替换静态资源属灰色用法——班组内部自用、不二次分发风险低；
若未来对外销售或分发，请购买官方授权。请勿移除源码与文档中的 ONLYOFFICE 版权声明。
