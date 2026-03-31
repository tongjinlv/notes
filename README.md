# 本地网页笔记 · Local Notes

一个对新手友好的本地网页笔记工具：

- 浏览器里写 Markdown，自动保存
- 粘贴或拖拽上传图片，和笔记放一起
- 支持公开页展示勾选为「公开」的笔记
- 支持 GitHub / Gitee OAuth 登录（多用户隔离）

适合个人知识库、小团队内网笔记、或者把数据同步到 Git 仓库做版本管理。

---

## 1 分钟理解它怎么工作

- 程序启动后提供一个本地网页（比如 `http://127.0.0.1:8787`）
- 你在网页里编辑笔记，内容写到本机磁盘
- 每位登录用户都有自己的目录：`users/<provider>/<login>/...`
- 每篇笔记是一个文件夹，默认目录结构是 `YYYYMM/<noteId>/note.md`

示例（以 GitHub 用户 `alice` 为例）：

```text
notes-vault/
├── users/
│   └── github/
│       └── alice/
│           └── 202603/
│               └── n_xxxxxxxx/
│                   ├── note.md
│                   └── image-*.png
└── .notes-sidebar-order.json
```

---

## 你将获得什么功能

- Markdown 编辑 + 预览
- 侧栏搜索（标题/正文）
- 拖拽排序（顺序保存在 `.notes-sidebar-order.json`）
- 图片粘贴上传
- 明暗主题切换
- 公开笔记列表与详情页
- Windows / Linux 服务安装（可选）

---

## 运行前准备

### 必需

- Go 1.21+（仅从源码运行/构建时需要）

### 登录方式（二选一或都配）

- GitHub OAuth
- Gitee OAuth

> 不配置 OAuth 也能启动程序，但前端会提示你先配置登录，无法正常使用笔记功能。

---

## 快速启动（开发机最简单）

```bash
git clone https://github.com/你的ID/你的仓库.git
cd 你的仓库
go run .
```

默认读取当前目录下 `notes-config.json`。  
启动后看控制台日志里的地址，浏览器打开即可。

---

## 配置文件说明（`notes-config.json`）

最小示例（仅演示结构）：

```json
{
  "listen": ":8787",
  "data": "notes-vault",
  "githubOAuth": {
    "clientId": "xxx",
    "clientSecret": "xxx",
    "callbackUrl": "http://127.0.0.1:8787/auth/github/callback",
    "cookieSecret": "请替换成足够长的随机字符串",
    "allowedLogins": []
  },
  "giteeOAuth": {
    "clientId": "xxx",
    "clientSecret": "xxx",
    "callbackUrl": "http://127.0.0.1:8787/auth/gitee/callback",
    "cookieSecret": "请替换成足够长的随机字符串",
    "allowedLogins": []
  }
}
```

字段说明：

- `listen`：监听地址
  - `:8787` 表示本机/局域网都可访问
  - `127.0.0.1:8787` 表示仅本机访问（更安全）
- `data`：数据目录
  - 推荐写相对路径 `notes-vault`
  - 也可写绝对路径
- `callbackUrl`：OAuth 回调地址，必须和平台应用设置完全一致
- `cookieSecret`：用于签名会话，建议 32+ 长随机串
- `allowedLogins`：可选白名单，不填通常表示不限制（按服务端实现）

---

## OAuth 登录详细配置（小白版）

下面是最容易踩坑的部分，按步骤做就行。

### A. GitHub 登录配置

1. 打开 GitHub → Settings → Developer settings → OAuth Apps → New OAuth App  
2. 填写：
   - Application name：随意（如 `Local Notes`）
   - Homepage URL：`http://127.0.0.1:8787`
   - Authorization callback URL：`http://127.0.0.1:8787/auth/github/callback`
3. 创建后拿到：
   - `Client ID`
   - `Client Secret`
4. 填到 `notes-config.json` 的 `githubOAuth`。

### B. Gitee 登录配置

1. 打开 Gitee 开放平台，创建第三方应用  
2. 回调地址填：`http://127.0.0.1:8787/auth/gitee/callback`
3. 获取 `clientId` / `clientSecret`
4. 填到 `notes-config.json` 的 `giteeOAuth`。

### C. 启动与验证

1. 保存配置后重启程序
2. 打开首页
3. 点击 GitHub 或 Gitee 登录按钮
4. 授权完成后自动回到笔记页

### 常见报错排查

- **“回调地址不匹配”**
  - 检查平台配置与 `callbackUrl` 是否逐字一致（协议、域名、端口、路径都要一致）
- **“服务未配置 OAuth”**
  - `clientId/clientSecret/callbackUrl/cookieSecret` 是否有空值
- **登录后仍未进入**
  - 看控制台日志是否有 OAuth 配置无效提示
  - 检查浏览器是否拦截了第三方跳转/弹窗

---

## 构建与服务部署

### 从源码构建

```bash
go build -o notes .
```

Windows 可用：

- `build.bat build`：编译 `notes.exe`
- `build.bat run`：直接运行

### 命令行参数

| 参数 | 说明 |
| ---- | ---- |
| `-config` | 配置文件路径；默认 `<可执行文件目录>/notes-config.json`（`go run` 时为当前目录） |
| `-service` | `install` / `uninstall` / `start` / `stop` / `restart` |
| `-svc-name` | 服务名，默认 `LocalNotes` |

---

## API 摘要

| 方法 | 路径 | 说明 |
| ---- | ---- | ---- |
| GET | `/api/notes` | 获取笔记列表 |
| POST | `/api/notes` | 新建笔记 |
| PUT | `/api/notes/:id` | 更新笔记 |
| DELETE | `/api/notes/:id` | 删除笔记 |
| POST | `/api/media` | 上传图片 |
| GET | `/api/vault/*` | 读取笔记附件 |
| GET | `/api/public/posts` | 公开笔记列表 |
| GET | `/api/public/post` | 公开笔记详情 |

---

## 安全建议（务必看）

- 能用 `127.0.0.1` 就不要监听全网卡
- 不要直接裸露到公网
- `clientSecret` 和 `cookieSecret` 不要提交到 Git
- 若部署在服务器，建议再加一层反向代理与访问控制

---

## 技术栈

- Go + Gin
- YAML front matter（`note.md` 元数据）
- 纯 HTML/CSS/JS 前端（embed 打包）

---

## 许可证

MIT License
