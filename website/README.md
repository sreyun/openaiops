# AIOps · 营销网站

上传本目录到服务器后，用下面任一方式启动，**用户访问信息会永久写入本地 SQLite**，管理后台可查。

## 一、上传后怎么用（推荐）

### 方式 A · Docker（适合 VPS）

把整个 `website/` 目录上传到服务器后：

```bash
cd website
docker compose up -d --build
```

- 站点：`http://服务器IP:8090/`
- 后台：`http://服务器IP:8090/ethan.html`
- 默认密码：`aiops2026`（请改）
- 数据库文件：`website/data/website.db`（卷持久化，删容器不丢）
- 基础镜像默认走华为云 SWR（`alpine`）+ 阿里云 apk；海外可在 `.env` 写 `BASE_REGISTRY=docker.io`

改密码 / 镜像源示例：

```bash
# Linux
WEBSITE_ADMIN_PASSWORD='你的强密码' docker compose up -d

# 或写到同目录 .env
echo 'WEBSITE_ADMIN_PASSWORD=你的强密码' > .env
# 海外构建时取消下一行注释：
# echo 'BASE_REGISTRY=docker.io' >> .env
docker compose up -d
```
### 方式 B · 本机双击启动（Windows）

双击 `start.bat` → 打开 http://127.0.0.1:8090/ethan.html

### 方式 C · 跟 AIOps 开发栈一起起

```bash
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d --build aiops-website
```

## 二、数据在哪看

1. 打开管理后台登录  
2. 「数据总览 / 访问分析 / 联系表单 / 订阅用户」  
3. 每条记录含 **IP、设备、来源、登记信息** 等  
4. 「设置」可导出 JSON、导入旧浏览器 localStorage、清空库  

数据库路径：`data/website.db`（勿提交到 Git）。

## 三、注意

| 方式 | 能否永久保存访问信息 |
|------|----------------------|
| `serve.py` / Docker / start.bat | ✅ 可以 |
| GitHub Pages / 纯静态托管 | ❌ 不行（没有后端） |
| 直接双击打开 HTML 文件 | ❌ 不行（没有 API） |

## 四、API 摘要

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/visit` | 页面访问上报 |
| POST | `/api/contact` | 联系表单 |
| POST | `/api/subscribe` | 订阅 |
| POST | `/api/admin/login` | 后台登录 |
| GET | `/api/admin/visits` | 访问列表 |
| GET | `/api/admin/contacts` | 联系列表 |
| GET | `/api/admin/subscribers` | 订阅列表 |
| GET | `/api/admin/export` | 全量导出 |

## 五、目录

```
website/
├── start.bat / start.sh     # 一键启动
├── docker-compose.yml       # 独立 Docker 部署
├── Dockerfile
├── serve.py                 # 静态站 + SQLite API
├── data/website.db          # 自动生成的数据库
├── ethan.html               # 管理后台
└── *.html / css / js / assets
```
