# OnlyOffice fnOS Connector

在浏览器中直接编辑 NAS 上的 Office 文档。支持 DOCX、XLSX、PPTX、PDF 在线编辑；支持 DOC、ODT、RTF、TXT、XLS、ODS、CSV、PPT、ODP 转换为对应的 OOXML 格式；DJVU、OXPS、EPUB、FB2 支持只读查看。

本项目基于 [tf4fun/onlyoffice-fnos](https://github.com/tf4fun/onlyoffice-fnos) 持续开发，现作为独立项目维护；不计划开发原生 fnOS 应用。

> ⚠️ **早期开发阶段**
> 
> 本应用仍处于非常早期的开发阶段，可能存在各种意料之外的 BUG。不同设备、不同客户端版本也可能产生不同的结果。**不建议在生产环境中使用**。

## 功能特性

- **在线编辑**: 直接在浏览器中编辑 DOCX、XLSX、PPTX、PDF 文档（PDF 编辑要求 ONLYOFFICE Docs / Document Server 8.1 或更高版本）
- **格式转换**: 支持 DOC、ODT、RTF、TXT 转换为 DOCX，XLS、ODS、CSV 转换为 XLSX，以及 PPT、ODP 转换为 PPTX
- **文本导入选项**: CSV 转换可选择编码和分隔符；TXT 转换可选择编码
- **文档查看**: 支持 DJVU、OXPS、EPUB、FB2 等格式的只读预览
- **JWT 安全**: 支持 JWT 签名验证，确保文档传输安全
- **fnOS 集成**: 面向飞牛 NAS（fnOS）适配的应用连接器

## 支持的文件格式

| 类型 | 可编辑 | 可转换 | 仅查看 |
|------|--------|--------|--------|
| 文档 | docx, pdf | doc, odt, rtf, txt | djvu, oxps, epub, fb2 |
| 表格 | xlsx | xls, ods, csv | - |
| 演示 | pptx | ppt, odp | - |

> ⚠️ **生产安全要求**：生产部署必须启用 JWT，并确保 connector 与 ONLYOFFICE Docs / Document Server 使用完全一致的密钥。

## 安装部署

### 方式一：WatchCow + Docker Compose（推荐）

最灵活的部署方式，可随时调整配置和存储卷挂载。该方式通过 [WatchCow](https://github.com/tf4fun/watchcow) 集成 fnOS 文件管理器右键菜单。

进入 docker 目录，复制 `.env.example` 为 `.env` 并配置：

```bash
cd docker
cp .env.example .env
```

编辑 `.env` 文件：

```bash
# 外网域名后缀，用于判断是否走 HTTPS
# 匹配 *.example.com 和 example.com
EXTERNAL_DOMAIN=.your-domain.com

# JWT 密钥，用于 Document Server 安全通信
JWT_SECRET=your-secret-key-change-me
```

启动所有服务：

```bash
docker compose up -d
```

> ⚠️ **注意**：请根据你机器上的存储卷路径，修改 `compose.yaml` 中 `onlyoffice-connector` 的 volumes 挂载。默认挂载为 `/vol00:/vol00`、`/vol1:/vol1`、`/vol2:/vol2`，请按实际情况调整。

这会启动三个容器：
- `onlyoffice-nginx`: 反向代理入口 (端口 9080)
- `onlyoffice-connector`: 连接器服务（镜像：`lanceloo/onlyoffice-fnos:latest`）
- `onlyoffice-doc-svr`: ONLYOFFICE Docs（Document Server）

部署完成后，按 WatchCow 的说明完成集成即可使用文件管理器右键菜单。

### 方式二：FPK 安装包

前往本项目的 GitHub Releases 页面下载 `.fpk` 安装包，在 fnOS 应用中心选择「手动安装」上传即可。FPK 包提供自身的 fnOS 应用集成，无需 WatchCow。

> ⚠️ **FPK 包的局限性**
> 
> FPK 包本质上仍基于 Docker，只是提供安装引导流程。存在以下限制：
> 
> - **存储卷固定**：安装时会发现当时存在的 `/vol*` 存储卷；后续增加或减少的存储卷不会自动更新
> - **无法重建容器**：fnOS 目前不支持重建应用容器以更新配置
> - **灵活性较低**：相比方式一，配置调整不够灵活
> 
> 如需更高的灵活性，建议使用方式一。

## 使用

部署完成后，在 fnOS 文件管理器中右键点击 Office 文档，选择「使用 OnlyOffice 打开」即可在浏览器中编辑。

## 配置说明

`.env` 文件中的配置项：

| 环境变量 | 说明 |
|---------|------|
| `EXTERNAL_DOMAIN` | 外网域名后缀，用于判断 HTTPS |
| `JWT_SECRET` | JWT 密钥，用于 Document Server 安全通信 |

## 项目结构

```
.
├── cmd/server/          # 主程序入口
├── docker/              # Docker 部署配置
│   ├── compose.yaml     # Docker Compose 编排文件
│   └── .env.example     # 环境变量示例
├── internal/
│   ├── config/          # 配置管理
│   ├── editor/          # 编辑器配置生成
│   ├── file/            # 文件服务
│   ├── format/          # 格式管理
│   ├── jwt/             # JWT 签名验证
│   └── server/          # HTTP 服务器
├── web/
│   ├── static/          # 静态资源
│   └── templates/       # HTML 模板
└── fpk_assets/          # fnOS 应用包资源
```

## 开发

```bash
# 编译
go build -o onlyoffice-connector ./cmd/server

# 运行测试
go test ./...
```

## 许可证

MIT License

### 第三方组件与商标

[ONLYOFFICE Docs（Document Server）](https://github.com/ONLYOFFICE/DocumentServer) 是独立发布的第三方组件；其开源版本及不同发行版的许可证、商用条款和使用条件请以官方项目说明为准。ONLYOFFICE、fnOS 及相关名称和标识属于各自权利人的商标或品牌。

## 致谢

- [tf4fun/onlyoffice-fnos](https://github.com/tf4fun/onlyoffice-fnos) — 项目基础与开源贡献
- [OnlyOffice Document Server](https://github.com/ONLYOFFICE/DocumentServer)
- [飞牛 NAS (fnOS)](https://www.fnnas.com/)
