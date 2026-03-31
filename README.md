<img width="1912" height="932" alt="image" src="https://github.com/user-attachments/assets/8d464e91-c477-4265-a7aa-49134e55fe23" />

# 本地网页笔记 · Local Notes

轻量、单二进制的 **本地 Markdown 笔记** 应用：内置 Web UI，数据以 `YYYYMM/<笔记ID>/note.md` 目录结构存放在本机（例如 `202603/n_xxx`，兼容旧版 `YYYY/MM/<id>` 与 `YYYY/MM/DD/<id>`），支持图片粘贴与拖拽上传、明暗主题、侧栏搜索与自定义排序。

**English:** A small self-hosted note app: single Go binary, embedded web UI, Markdown files on disk, image upload, dark/light theme.

---

## 功能概览

- 浏览器中编辑与预览 Markdown（简易渲染）
- 笔记按侧栏列表管理；支持搜索标题与正文
- 新建笔记可插在「当前选中项之前」或列表顶部；顺序持久化在仓库根目录 `.notes-sidebar-order.json`
- 图片：粘贴截图或拖入编辑器，随笔记保存在同一目录
- 主题切换（本地存储）
- 可选安装为 **Windows / Linux 系统服务**（[kardianos/service](https://github.com/kardianos/service)）
- 前端静态资源嵌入二进制，部署只需一个可执行文件

## 环境要求

- [Go](https://go.dev/dl/) **1.21+**（从源码构建时）

## 快速开始

```bash
git clone https://github.com/你的ID/你的仓库.git
cd 你的仓库
go run .
```

在可执行文件同目录放置 `notes-config.json`（可参考 `notes-config.example.json`），其中 `listen` 控制监听地址。浏览器访问控制台输出的 URL。

### 从源码构建

```bash
go build -o notes .
# 写入版本号（可选）
go build -ldflags "-X=main.version=1.0.0" -o notes .
```

Windows 下可使用仓库中的 `build.bat`：

- `build.bat build` — 编译生成 `notes.exe`（可通过环境变量 `APP_VERSION` 指定版本字符串）
- `build.bat run` — `go run .`（配置见当前目录 `notes-config.json`，可用 `-config` 指定路径）

### 命令行参数

| 参数 | 说明 |
|------|------|
| `-config` | 配置文件路径；省略则为 `<可执行文件目录>/notes-config.json`（`go run` 时为当前目录） |
| `-service` | `install` / `uninstall` / `start` / `stop` / `restart`（安装服务通常需要管理员权限） |
| `-svc-name` | 服务内部名称，默认为 `LocalNotes`，与 `install` / `uninstall` 等需一致 |

监听地址与仓库路径仅在 **`notes-config.json`** 中配置：`listen`（如 `:8787`、`127.0.0.1:8787`）、`data`（仓库目录，空则默认可执行文件旁的 `notes-vault`；可为旧版 `notes-data.json` 路径）。安装为 Windows 服务时仅写入 `-config=...`，修改监听或仓库后改 JSON 并重启服务即可。

### 数据目录结构

```
notes-vault/
├── .notes-sidebar-order.json   # 侧栏顺序（自动生成）
└── 202603/
    └── n_xxxxxxxx/
        ├── note.md       # YAML 头 + Markdown 正文
        └── image-*.png   # 附件图片（示例）
```

## HTTP API（摘要）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/notes` | 列表（顺序由侧栏顺序文件与规则合并） |
| POST | `/api/notes` | 新建；JSON 可含 `beforeId`，表示插在该 id 之前 |
| PUT | `/api/notes/:id` | 更新标题与正文 |
| DELETE | `/api/notes/:id` | 删除笔记目录 |
| POST | `/api/media` | 表单：`note`（笔记 id）、`file`（图片文件） |
| GET | `/api/vault/*` | 按仓库相对路径读取资源（用于笔记内图片等） |

## 技术栈

- 后端：[Go](https://go.dev/)、[Gin](https://github.com/gin-gonic/gin)
- 服务封装：[kardianos/service](https://github.com/kardianos/service)
- 笔记元数据：YAML front matter（[gopkg.in/yaml.v3](https://github.com/go-yaml/yaml)）
- 前端：原生 HTML / CSS / JS，通过 `embed` 打包

## 安全说明

- 默认监听所有网卡时，局域网内可达；请在防火墙或反向代理上按需限制访问。
- 本仓库定位为**本地/受信网络**使用，未内置多用户认证与审计；勿直接暴露于公网而不加防护。

## 开源许可

本项目基于 [MIT License](LICENSE) 开源。

## 贡献

欢迎 Issue 与 Pull Request。提交前请确保 `go build ./...` 可通过。
