# Quantix 单机部署商业化计划

## 目标

将 Quantix 从开发者工具转变为可售卖的单机部署产品，具备：
- 5 分钟一键安装
- License 授权保护（防盗版）
- 自动更新
- AI 策略核心价值锁定

---

## Phase 1: 打包与部署（预计 3-4 天）

### 1.1 预编译发布

**目标：** 用户无需安装 Go，下载即用。

- [ ] 使用 `goreleaser` 配置多平台交叉编译
  - linux/amd64, linux/arm64（VPS 主力）
  - darwin/amd64, darwin/arm64（Mac 本地开发）
- [ ] 构建产物：`quantix-api` 单二进制 + `web/` 前端静态文件
- [ ] 使用 `garble` 混淆编译（防反编译提取逻辑）
  - `garble -literals -seed=random build -o quantix-api ./cmd/api`
- [ ] GitHub Releases / 私有下载站分发

### 1.2 Docker 一键部署

**目标：** `docker compose up -d` 起全栈。

- [ ] 优化 `deploy/docker-compose.yml`
  ```yaml
  services:
    quantix-api:
      image: quantix/quantix:latest
      volumes:
        - ./config:/app/config
        - ./data/logs:/app/logs
        - ./data/license:/app/license
      environment:
        - QUANTIX_ENCRYPTION_KEY=${ENCRYPTION_KEY}
        - QUANTIX_LICENSE_FILE=/app/license/license.key
      depends_on: [postgres, redis]
    
    quantix-web:
      image: quantix/quantix-web:latest
      ports: ["443:443"]
    
    postgres:
      image: timescale/timescaledb:latest-pg17
      volumes: ["./data/pg:/var/lib/postgresql/data"]
    
    redis:
      image: redis:7-alpine
      volumes: ["./data/redis:/data"]
  ```
- [ ] `install.sh` 一键安装脚本
  ```bash
  # 检查 Docker 环境
  # 生成随机密钥
  # 拉取镜像
  # 创建目录结构
  # 首次初始化（创建管理员）
  # 启动服务
  ```
- [ ] `update.sh` 一键更新脚本
  ```bash
  # 拉取新镜像
  # 备份数据库
  # 停止服务
  # 启动新版本（自动 migrate）
  ```

### 1.3 首次运行向导

**目标：** 打开浏览器，引导式完成所有配置。

- [ ] `/setup` 页面（仅在未初始化时显示）
  - Step 1: 创建管理员账户
  - Step 2: 输入 License Key
  - Step 3: 配置交易所 API Key（支持 Binance/OKX）
  - Step 4: 选择策略和交易对
  - Step 5: 设置风控参数
  - Step 6: 启动引擎
- [ ] 后端 `/api/setup` endpoint（仅首次可用）
- [ ] 完成后自动禁用 setup 路由

---

## Phase 2: License 授权系统（预计 4-5 天）

### 2.1 License Key 格式

**结构：** RSA-2048 签名的 JSON payload

```json
{
  "license_id": "LIC-20260402-001",
  "customer_id": "cust_abc123",
  "customer_name": "张三",
  "plan": "pro",                    // "starter" | "pro" | "enterprise"
  "max_engines": 3,                 // 最多同时运行的引擎数
  "max_symbols": 5,                 // 最多交易对数
  "features": ["ai_strategy", "staged_tp", "grid"],  // 解锁的功能
  "issued_at": "2026-04-02T00:00:00Z",
  "expires_at": "2027-04-02T00:00:00Z",
  "machine_id": "",                 // 首次激活时绑定
  "signature": "base64..."          // RSA-2048 签名
}
```

**Plan 分级：**

| Plan | 价格/月 | 引擎数 | 交易对 | 功能 |
|------|---------|--------|--------|------|
| Starter | $49 | 1 | 1 | 基础策略 (MACross, MeanReversion) |
| Pro | $149 | 3 | 5 | AI 策略 + Staged TP + Grid |
| Enterprise | $399 | 10 | 不限 | 全功能 + 优先支持 + 自定义 prompt |

### 2.2 License 验证流程

```
启动时:
  1. 读取 license.key 文件
  2. RSA 公钥验证签名（公钥编译进二进制）
  3. 检查过期时间
  4. 检查 machine_id 绑定（首次自动绑定，后续必须匹配）
  5. 检查 plan 权限 → 限制引擎数/交易对/功能
  6. 联网验证（可选，见 2.3）
  7. 通过 → 正常启动；失败 → 仅显示 License 页面

运行中:
  8. 每 24h 在线心跳（后台静默）
  9. 心跳失败 → 离线宽限期 7 天
  10. 宽限期过 → 停止引擎（不退出，显示续费提示）
```

### 2.3 License Server（你的服务端）

**极简 API（可用 Cloudflare Workers / Vercel Edge 部署）：**

```
POST /api/license/activate
  - 首次激活，绑定 machine_id
  - 返回激活确认

POST /api/license/heartbeat
  - 每日心跳
  - 检测多机使用（同一 key 不同 machine_id）
  - 返回: valid / expired / revoked / multi_use_detected

POST /api/license/deactivate
  - 用户换机器时解绑旧 machine_id
```

**数据库只需 1 张表：**
```sql
CREATE TABLE licenses (
  id TEXT PRIMARY KEY,
  customer_id TEXT NOT NULL,
  plan TEXT NOT NULL,
  machine_id TEXT,
  activated_at TIMESTAMPTZ,
  last_heartbeat TIMESTAMPTZ,
  expires_at TIMESTAMPTZ NOT NULL,
  is_revoked BOOLEAN DEFAULT false
);
```

### 2.4 本地验证实现

```go
// internal/license/license.go

type License struct {
    LicenseID    string   `json:"license_id"`
    CustomerID   string   `json:"customer_id"`
    Plan         string   `json:"plan"`
    MaxEngines   int      `json:"max_engines"`
    MaxSymbols   int      `json:"max_symbols"`
    Features     []string `json:"features"`
    IssuedAt     time.Time `json:"issued_at"`
    ExpiresAt    time.Time `json:"expires_at"`
    MachineID    string   `json:"machine_id"`
}

type Validator struct {
    pubKey     *rsa.PublicKey   // 编译进二进制
    license    *License
    graceUntil time.Time        // 离线宽限截止
}

func (v *Validator) Validate(licenseFile string) error { ... }
func (v *Validator) HasFeature(f string) bool { ... }
func (v *Validator) CheckEngineLimit(current int) error { ... }
func (v *Validator) StartHeartbeat(ctx context.Context) { ... }
```

### 2.5 防绕过措施

**编译层面：**
- `garble -literals` 混淆所有字符串常量（包括公钥、API URL）
- 关键验证逻辑分散在多个包中（不是单一 `license.Check()` 可 patch）
- 引擎启动、下单、GPT 调用等多个热路径都检查 license 状态

**运行层面：**
- License 验证不是单一 `if` 判断，而是影响程序行为：
  ```go
  // 在 engine.Run() 中
  if !license.HasFeature("ai_strategy") {
      // 不是 return error，而是降级为基础策略
      strat = fallbackStrategy()
  }
  ```
- machine_id 使用多个硬件特征组合 hash（CPU ID + MAC + hostname + disk serial）
- 心跳检查结果缓存在加密的本地文件中（不只是内存）

---

## Phase 3: AI 策略锁定（预计 2-3 天）

### 3.1 Prompt 服务端化（最强保护）

**核心思路：** GPT 的 system prompt（策略的灵魂）不编译在二进制中，启动时从你的服务器拉取。

```
quantix-api 启动
  → 验证 license
  → POST https://api.quantix.io/v1/prompts?plan=pro
    Header: X-License-Key: LIC-xxx
    Header: X-Machine-ID: abc123
  → 返回加密的 prompt 模板
  → 内存解密使用（不落盘）
```

**好处：**
- 即使二进制被 crack，没有 prompt 就没有 AI 策略
- 可以按 plan 下发不同质量的 prompt
- 更新 prompt 不需要用户重新部署
- 你可以 A/B 测试不同 prompt 版本

**实现：**
- [ ] 提取 `aistrat.go` 中的 GPT system prompt 为动态模板
- [ ] 添加 `internal/license/prompt_fetcher.go`
- [ ] Prompt 使用 AES-256 加密传输，key 从 license 派生
- [ ] 本地缓存加密 prompt（离线宽限期内可用）
- [ ] 缓存过期 = prompt 失效 = AI 策略降级

### 3.2 功能分级

| 功能 | Starter | Pro | Enterprise |
|------|---------|-----|------------|
| MACross / MeanReversion | YES | YES | YES |
| AI Strategy (GPT) | NO | YES | YES |
| Staged TP (exchange-native) | NO | YES | YES |
| Grid Trading | NO | YES | YES |
| Multi-timeframe | NO | YES | YES |
| Custom Prompt | NO | NO | YES |
| Backtest | 基础 | 完整 | 完整 |
| API Access | NO | YES | YES |

---

## Phase 4: 运营基础设施（预计 2-3 天）

### 4.1 License 管理后台

你自己的管理后台（简单的 Web 界面）：
- 生成 / 吊销 License
- 查看激活状态和心跳记录
- 客户管理
- 收入统计

### 4.2 自动更新检查

```go
// quantix-api 启动时 & 每 24h 检查
GET https://api.quantix.io/v1/version/latest
→ { "version": "1.2.0", "changelog": "...", "download_url": "..." }

// 前端显示更新提示，用户手动触发更新
```

### 4.3 错误上报（可选，用户同意后）

- 匿名 crash report（panic stack trace）
- 策略性能统计（帮你优化 prompt）
- 用户可在 Settings 中关闭

---

## 工作量估算

| Phase | 内容 | 预估 |
|-------|------|------|
| **Phase 1** | 打包部署 + Docker + 安装脚本 + 首次向导 | 3-4 天 |
| **Phase 2** | License 系统（本地验证 + Server + 防绕过） | 4-5 天 |
| **Phase 3** | Prompt 服务端化 + 功能分级 | 2-3 天 |
| **Phase 4** | 管理后台 + 自动更新 + 错误上报 | 2-3 天 |
| **总计** | | **11-15 天** |

### 优先级排序

```
Week 1: Phase 1 (部署) + Phase 2 前半 (本地 License 验证)
Week 2: Phase 2 后半 (Server + 防绕过) + Phase 3 (Prompt 锁定)
Week 3: Phase 4 (运营) + 测试 + 文档
```

---

## 关键决策点

1. **License Server 用什么部署？** 推荐 Cloudflare Workers（免费额度大，全球 CDN，零运维）
2. **支付集成？** Stripe / LemonSqueezy（自动发 license key）
3. **Prompt 更新频率？** 建议按周迭代，每次更新自动同步到所有用户
4. **免费试用？** 建议 7 天 Starter 试用（无需信用卡），体验基础策略
5. **退款政策？** 建议 14 天无条件退款（降低购买摩擦）
