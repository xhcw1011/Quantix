# Quantix 部署与启动指南

## 目录

- [架构概览](#架构概览)
- [方式一：本地开发模式](#方式一本地开发模式快速测试)
- [方式二：Docker 全栈部署](#方式二docker-全栈部署生产就绪)
- [首次设置：创建管理员](#首次设置创建管理员)
- [日常使用流程](#日常使用流程)
- [环境变量说明](#环境变量说明)
- [端口映射](#端口映射)
- [健康检查](#健康检查)
- [距离真金白银还缺什么](#距离真金白银还缺什么)

---

## 架构概览

```
┌─────────────┐    ┌──────────────┐    ┌──────────────┐
│  React SPA  │───→│  Go API :8080│───→│ TimescaleDB  │
│   :5173     │    │  (REST + WS) │    │   :5432      │
└─────────────┘    └──────┬───────┘    └──────────────┘
                          │
                   ┌──────┼───────┐
                   │      │       │
              ┌────▼──┐ ┌─▼───┐ ┌─▼────────┐
              │ Redis │ │NATS │ │Prometheus │
              │ :6379 │ │:4222│ │  :9091    │
              └───────┘ └─────┘ └─────┬─────┘
                                      │
                                ┌─────▼─────┐
                                │  Grafana  │
                                │   :3000   │
                                └───────────┘
```

**必须启动的服务**（按依赖顺序）：

| 服务 | 必须？ | 用途 |
|------|--------|------|
| **TimescaleDB** | 必须 | K线、订单、用户、持仓等所有数据存储 |
| **Go API** | 必须 | 后端核心：认证、引擎管理、交易路由 |
| **React Frontend** | 推荐 | Web 管理界面（也可纯 API 操作） |
| Redis | 可选 | ticker/orderbook 缓存（当前代码未强制依赖） |
| NATS | 可选 | 内部事件总线（无 NATS 时退化为进程内） |
| Prometheus + Grafana | 可选 | 监控仪表盘 |

---

## 方式一：本地开发模式（快速测试）

最低要求：只需 TimescaleDB + Go API + React。

### 步骤 1：启动数据库

如果你用的是本地 PostgreSQL + TimescaleDB（homebrew 安装）：

```bash
# 确认 PostgreSQL 正在运行
brew services start postgresql@17

# 确认数据库存在
psql -U quantix -d quantix -c "SELECT 1"
```

如果用 Docker 单独跑数据库：

```bash
docker run -d --name quantix-db \
  -e POSTGRES_USER=quantix \
  -e POSTGRES_PASSWORD=quantix_secret \
  -e POSTGRES_DB=quantix \
  -p 5432:5432 \
  timescale/timescaledb:latest-pg16
```

### 步骤 2：准备配置文件

```bash
cd /Users/apexis-backdesk/project/go-workspace/Quantix

# 复制示例配置（如果还没有 config.yaml）
cp config/config.example.yaml config/config.yaml
```

编辑 `config/config.yaml`，确认数据库连接信息正确：

```yaml
database:
  host: localhost
  port: 5432
  user: quantix
  password: quantix_secret
  name: quantix
  ssl_mode: disable
  max_conns: 10
```

### 步骤 3：设置环境变量并启动 API

```bash
# 生成密钥（只需执行一次，记住保存）
export QUANTIX_ENCRYPTION_KEY=$(openssl rand -hex 32)
export QUANTIX_JWT_SECRET=$(openssl rand -hex 32)

# 可选：允许实盘交易（不设置 = 只能 paper 模式）
# export QUANTIX_LIVE_CONFIRM=true

# 启动 API 服务器
go run ./cmd/api -config config/config.yaml
```

你应该看到：

```
{"level":"info","msg":"API server starting","addr":":8080"}
```

### 步骤 4：启动前端

新开一个终端：

```bash
cd /Users/apexis-backdesk/project/go-workspace/Quantix/web
npm install    # 首次运行需要
npm run dev
```

打开浏览器：**http://localhost:5173**

### 步骤 5（可选）：启动 Redis + NATS + 监控

```bash
# 只启动基础设施（不启动 API 和 Web，因为本地跑）
cd deploy
docker compose up -d redis nats prometheus grafana
```

---

## 方式二：Docker 全栈部署（生产就绪）

一键启动所有服务。

### 步骤 1：准备环境文件

```bash
cd /Users/apexis-backdesk/project/go-workspace/Quantix/deploy

# 复制环境模板
cp .env.example .env
```

编辑 `.env`：

```bash
# 必填：加密密钥
QUANTIX_ENCRYPTION_KEY=<你的64位hex，用 openssl rand -hex 32 生成>

# 必填：JWT 签名密钥（至少32字符）
QUANTIX_JWT_SECRET=<你的64位hex，用 openssl rand -hex 32 生成>

# 可选：允许真实交易
# QUANTIX_LIVE_CONFIRM=true

# 前端域名（生产环境改为实际域名）
QUANTIX_CORS_ORIGINS=http://localhost:80,http://localhost:5173
```

### 步骤 2：确保有 config.yaml

```bash
cp ../config/config.example.yaml ../config/config.yaml
# 编辑 config.yaml 设置你的交易所 API Key（如果需要）
```

### 步骤 3：启动

```bash
cd deploy
docker compose up -d --build
```

### 步骤 4：检查服务状态

```bash
docker compose ps
docker compose logs api --tail 20
```

### 访问地址

| 服务 | URL |
|------|-----|
| Web 界面 | http://localhost |
| API 直连 | http://localhost:8080 |
| Swagger 文档 | http://localhost:8080/api/docs/ |
| Grafana | http://localhost:3000 (admin / quantix_grafana) |
| NATS 监控 | http://localhost:8222 |

---

## 首次设置：创建管理员

API 服务器启动后，创建第一个管理员账号：

```bash
# 本地开发模式
export QUANTIX_ENCRYPTION_KEY=<同上>
go run ./cmd/api -config config/config.yaml \
  -create-admin \
  -admin-username admin \
  -admin-password "YourStrongP@ssw0rd!"

# Docker 模式
docker compose exec api quantix-api \
  -create-admin \
  -admin-username admin \
  -admin-password "YourStrongP@ssw0rd!"
```

然后在 Web 界面注册一个普通用户，用管理员账号在 Admin 面板激活它。

---

## 日常使用流程

### Paper Trading（模拟交易）测试流程

1. **登录** → http://localhost:5173（或 :80）
2. **添加交易所凭证** → Credentials 页面
   - Binance Testnet 或 OKX Demo 的 API Key/Secret
   - 勾选 testnet/demo 模式
3. **启动引擎** → Engine 页面
   - 选择凭证、策略（macross / grid / meanreversion）
   - Mode: **paper**
   - 设置参数（初始资金、费率、滑点）
   - 点击 Start
4. **观察** → Dashboard 页面
   - 实时权益曲线（WS 推送）
   - 成交记录、持仓、订单

### Backtest（回测）测试流程

1. **Backtest 页面** → 选策略、交易对、时间范围
2. 提交后异步运行，结果会显示在历史列表中
3. 查看 Sharpe、MaxDD、胜率等指标

### 手动 API 测试

```bash
# 健康检查
curl http://localhost:8080/health

# 登录获取 token
TOKEN=$(curl -s -X POST http://localhost:8080/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"YourStrongP@ssw0rd!"}' \
  | jq -r '.token')

# 查看策略列表
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/strategies

# 查看引擎状态
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/engines
```

---

## 环境变量说明

| 变量 | 必须 | 说明 |
|------|------|------|
| `QUANTIX_ENCRYPTION_KEY` | 是 | AES-256-GCM 密钥，64位 hex (`openssl rand -hex 32`) |
| `QUANTIX_JWT_SECRET` | 是 | JWT 签名密钥，至少 32 字符 (`openssl rand -hex 32`) |
| `QUANTIX_LIVE_CONFIRM` | 否 | 设为 `true` 才能用 Binance/OKX 真实账户下单 |
| `QUANTIX_API_ADDR` | 否 | API 监听地址，默认 `:8080` |
| `QUANTIX_API_CONFIG` | 否 | config.yaml 路径，默认 `config/config.yaml` |
| `QUANTIX_CORS_ORIGINS` | 否 | 逗号分隔的允许 CORS 域名，默认 localhost |
| `QUANTIX_TLS_CERT` | 否 | TLS 证书路径（设置后启用 HTTPS） |
| `QUANTIX_TLS_KEY` | 否 | TLS 私钥路径 |
| `QUANTIX_DATABASE_HOST` | 否 | 覆盖 config.yaml 中的数据库 host |

---

## 端口映射

| 端口 | 服务 | 说明 |
|------|------|------|
| 5432 | TimescaleDB | PostgreSQL 数据库 |
| 8080 | Go API | REST + WebSocket |
| 5173 | Vite Dev | 前端开发服务器（仅开发模式） |
| 80 | Nginx | 前端 + API 反向代理（Docker 模式） |
| 6379 | Redis | 缓存（可选） |
| 4222 | NATS | 消息总线（可选） |
| 9091 | Prometheus | 指标抓取（可选） |
| 3000 | Grafana | 监控仪表盘（可选） |

---

## 健康检查

```bash
# API 健康（含数据库连通性）
curl http://localhost:8080/health
# 返回: {"status":"healthy","database":true,"time":"..."}

# Docker 模式下查看所有服务
docker compose ps
docker compose logs --tail 5
```

---

## 距离真金白银还缺什么

下面按 **优先级** 从高到低列出投入真实资金前必须完成的工作：

### P0 — 必须完成（否则可能亏钱/出 bug）

| # | 项目 | 说明 | 工作量 |
|---|------|------|--------|
| 1 | **关键路径测试** | `internal/api/`（auth、handler）、`internal/live/`（broker 下单流程）、`internal/paper/`（引擎）、`internal/data/`（DB 操作）目前 **零测试**。至少要覆盖：JWT 认证、下单→成交→持仓更新、引擎启停恢复 | 3-5 天 |
| 2 | **Testnet 全流程验证** | 用 Binance Testnet / OKX Demo 跑完整交易周期：开仓→止损/止盈触发→平仓→引擎重启恢复。确认 OMS 持仓数据和交易所一致 | 1-2 天 |
| 3 | **策略参数验证** | 用回测确认你要上线的策略在目标交易对上 Sharpe > 1、MaxDD 可控。用 WFO optimizer 做 out-of-sample 验证 | 2-3 天 |
| 4 | **数据库备份方案** | 配置 pg_dump 定时备份（cron 或 Docker sidecar），测试恢复流程。丢失订单/持仓数据 = 资金风险 | 0.5 天 |

### P1 — 强烈建议（降低运营风险）

| # | 项目 | 说明 |
|---|------|------|
| 5 | **TLS 启用** | 生产环境必须 HTTPS（API 凭证和 JWT 在传输中明文 = 被窃取风险）。方案：Nginx TLS 终止 或设置 `QUANTIX_TLS_CERT/KEY` |
| 6 | **Telegram 告警** | 配置 Telegram bot，接收实时成交、风险告警、引擎停止通知。离开屏幕时也能感知异常 |
| 7 | **资金上限控制** | 初次实盘用 **小资金**（如 $100-500）+ 低杠杆（2-3x），config.yaml 中设置保守的 risk 参数 |
| 8 | **Grafana 仪表盘** | 确认 Prometheus 指标正在采集，Grafana 仪表盘能看到权益曲线、延迟、持仓 |

### P2 — 可以后续迭代

| # | 项目 | 说明 |
|---|------|------|
| 9 | Bybit OrderClient 实现 | 当前 Bybit 仅支持行情数据，不能交易 |
| 10 | 密码找回功能 | 当前忘记密码 = 账号锁死 |
| 11 | 操作审计日志 | 记录谁在何时启停了哪个引擎、修改了什么凭证 |
| 12 | 交易所 API 限频监控 | 避免触发交易所 rate limit 导致下单失败 |

### 推荐的上线路径

```
Paper 模拟 (1-2 周)
    ↓  确认策略盈利、系统稳定
Testnet 实盘 (1 周)
    ↓  确认下单/成交/止损全链路正常
小资金真实交易 ($100-500, 低杠杆)
    ↓  确认滑点、费率、延迟在预期范围
逐步加仓
```

---

## 快速启动命令速查

```bash
# ===== 本地开发（最小化启动） =====
export QUANTIX_ENCRYPTION_KEY=$(openssl rand -hex 32)
export QUANTIX_JWT_SECRET=$(openssl rand -hex 32)

# 终端 1: API
go run ./cmd/api -config config/config.yaml

# 终端 2: 前端
cd web && npm run dev

# 终端 3（可选）: 基础设施
cd deploy && docker compose up -d redis nats prometheus grafana


# ===== Docker 全栈 =====
cd deploy
cp .env.example .env
# 编辑 .env 填入密钥
docker compose up -d --build
# 创建管理员
docker compose exec api quantix-api -create-admin -admin-username admin -admin-password "StrongPass!"
```
