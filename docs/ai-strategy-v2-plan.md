# AI Strategy v2 — 系统性重构方案

## 一、问题清单

### P0 — 必须修（导致资金损失）

#### 1. 重启后丢失持仓状态
- **现象**：引擎重启后策略不知道交易所还有未平仓位，继续开新单 → 幽灵仓位
- **根因**：策略 `longPos`/`shortPos` 是纯内存状态，不持久化到 DB。`recoverFromDB` 只恢复 PENDING/OPEN 订单，不恢复 FILLED 的已有持仓
- **影响**：每次重启都可能产生幽灵持仓，已发生 3 次

#### 2. 手动操作后系统不同步
- **现象**：用户在币安手动平仓/开仓，系统不知道 → 策略状态和交易所不一致
- **根因**：User Data Stream 只监听自己下的订单（通过 clientOrderID 匹配），手动操作的订单没有 clientOrderID 匹配不上
- **影响**：手动平仓后系统继续管理已不存在的持仓，或在已有持仓上重复开单

#### 3. Equity 计算在合约模式下不准
- **现象**：开仓后 equity 虚低（保证金锁定被当成亏损），触发 daily limit / 熔断
- **根因**：`cash + unrealizedPnL` 在合约保证金模式下不等于真实 equity。虽然已加了 WS ACCOUNT_UPDATE 推送，但首次推送前的窗口期仍有问题
- **影响**：已触发 3 次虚假熔断/daily limit

---

### P1 — 应该修（影响盈利能力）

#### 4. 低位追空 / 高位追多
- **现象**：价格已大跌后 GPT 仍给 SELL 高 confidence → 在低位开空
- **根因**：GPT 只看当前快照（RSI/MACD/BB），不考虑"价格已经从哪跌到哪"，缺少趋势幅度判断
- **方案**：给 GPT 增加 "近 1h 涨跌幅" 和 "距今日高低点距离" 指标

#### 5. GPT 信号偏向单边
- **现象**：GPT 连续多次只给 SELL 不给 BUY（或反过来），导致只做单边
- **根因**：RSI 超买时 GPT 拒绝给 BUY，但震荡市里 RSI 长期偏高是正常的
- **已做**：改了 prompt + swing boost。但 boost 规则太简单（距离阈值固定），需要更智能

#### 6. 限价单超时后交易所残留
- **现象**：策略本地超时取消了 posState，但交易所限价单可能还在挂着
- **根因**：`ctx.CancelOrder(orderID)` 取消的是 OMS 订单，不一定成功取消了交易所订单
- **方案**：超时时先取消交易所订单，确认取消成功后再清 posState

---

### P2 — 优化项（提升稳定性）

#### 7. 多空双开间距控制不精准
- **现象**：多空仓位距离太近变成自我对冲
- **方案**：间距应该基于 ATR 而不是固定百分比

#### 8. 策略配置散落在 API 请求里
- **现象**：每次启动引擎都要传一大堆 params，容易漏
- **方案**：策略配置持久化到 DB 或 config file，启动时只传 strategy_id

#### 9. 没有交易记录仪表盘
- **现象**：只能看日志分析交易，没有结构化的交易历史视图
- **方案**：API 加交易统计端点（胜率、盈亏比、累计 PnL 曲线）

---

## 二、架构设计

### 核心原则：交易所是唯一真相源（Single Source of Truth）

```
当前（有问题的）:
  策略内存状态 ←→ OMS ←→ 交易所
  三者经常不同步

改为:
  交易所持仓 → 同步到引擎 → 策略读取引擎状态
  策略只做决策，不维护持仓状态
```

### 2.1 持仓同步器（PositionSyncer）

新增组件，负责：
- 启动时从交易所查询当前持仓
- 运行时通过 User Data Stream 实时更新
- 提供统一的 `GetPositions()` 接口
- 检测外部操作（手动平仓等）并通知策略

```go
type PositionSyncer struct {
    positions map[string]*ExchangePosition  // symbol+side → position
    mu        sync.RWMutex
    onChange  func(event PositionChangeEvent) // 通知策略
}

type ExchangePosition struct {
    Symbol       string
    Side         string  // "LONG" or "SHORT"
    Qty          float64
    EntryPrice   float64
    UnrealizedPnL float64
    UpdatedAt    time.Time
}

type PositionChangeEvent struct {
    Type     string // "opened", "closed", "modified", "external"
    Position ExchangePosition
}
```

### 2.2 策略状态持久化

策略的 posState 保存到 DB：

```sql
CREATE TABLE strategy_positions (
    user_id     INT NOT NULL,
    engine_id   TEXT NOT NULL,
    side        TEXT NOT NULL,  -- 'LONG' or 'SHORT'
    mode        TEXT NOT NULL,  -- 'trend' or 'range'
    entry_price FLOAT NOT NULL,
    init_qty    FLOAT NOT NULL,
    remain_qty  FLOAT NOT NULL,
    stop_loss   FLOAT,
    take_profit FLOAT,
    trailing    FLOAT,
    peak_price  FLOAT,
    r_value     FLOAT,
    tp1_hit     BOOLEAN DEFAULT FALSE,
    bars_held   INT DEFAULT 0,
    created_at  TIMESTAMPTZ DEFAULT NOW(),
    updated_at  TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (user_id, engine_id, side)
);
```

启动时：加载 strategy_positions → 初始化 posState
每次变更：更新 strategy_positions
关闭时：保存最终状态

### 2.3 策略重构 — 分离决策和执行

```
当前（一个大文件 800+ 行）:
  aistrat.go = GPT调用 + 指标计算 + 入场 + 出场 + 仓位管理 + 风控

改为:
  signal.go    — GPT 调用 + 信号解析
  indicator.go — 指标计算 + 市场上下文构建
  entry.go     — 入场逻辑（trend/range 模式选择）
  exit.go      — 出场逻辑（止损/止盈/trailing/reversal）
  position.go  — 持仓状态管理 + 持久化
  strategy.go  — 主循环（OnBar/OnFill），组合上述模块
```

### 2.4 入场改进

```
当前:
  GPT 给方向 → 策略直接下单

改为:
  GPT 给方向 → 检查是否合理:
    1. 距今日高/低点位置（不在极端位追单）
    2. 近 N 根 bar 涨跌幅（不在大幅移动后追单）
    3. 与已有持仓的间距（不自我对冲）
    4. 每日已交易次数（防过度交易）
  → 通过所有检查 → 下限价单
```

### 2.5 交易所同步流程

```
启动:
  1. 查交易所余额 → 设置 equity
  2. 查交易所持仓 → 初始化 PositionSyncer
  3. 查交易所挂单 → 取消非本引擎的挂单（或跟踪）
  4. 加载 DB strategy_positions → 初始化策略 posState
  5. 对比交易所持仓 vs 策略 posState → 不一致则以交易所为准
  6. 启动 User Data Stream → 实时同步

运行中:
  - ACCOUNT_UPDATE → 更新 equity + PositionSyncer
  - ORDER_TRADE_UPDATE → 更新 OMS + 策略 posState
  - 外部操作检测 → 通过 PositionSyncer.onChange 通知策略

关闭:
  - 保存 strategy_positions 到 DB
  - 不平仓（保持交易所持仓）
  - 下次启动时恢复
```

---

## 三、实施计划

### Phase 1: 持仓同步（解决 P0-1, P0-2）
- 新增 PositionSyncer
- 启动时查交易所持仓
- User Data Stream 感知所有持仓变化（包括手动操作）
- 策略通过 PositionSyncer 读持仓，不自己维护

### Phase 2: 状态持久化（解决 P0-1 彻底）
- 新增 strategy_positions 表
- 策略 posState 变更时同步写 DB
- 启动时从 DB 恢复 posState

### Phase 3: Equity 修复（解决 P0-3）
- 启动时查交易所余额，不用 SyncBalance
- ACCOUNT_UPDATE 实时更新 equity
- 去掉所有本地 equity 计算

### Phase 4: 入场质量（解决 P1-4, P1-5）
- GPT context 增加趋势幅度指标
- 入场前检查价格位置合理性
- 限价单偏移量基于 ATR 动态调整

### Phase 5: 代码重构（解决可维护性）
- 拆分策略文件
- 统一错误处理
- 增加结构化交易日志

---

## 四、预期效果

| 问题 | 当前 | v2 |
|------|------|-----|
| 重启丢持仓 | 每次重启都丢 | 从交易所+DB 双重恢复 |
| 手动操作不同步 | 完全不感知 | User Data Stream 实时感知 |
| 虚假熔断 | 已发生 3 次 | 用交易所 equity，不本地算 |
| 幽灵仓位 | 已发生 3 次 | 启动时交易所对账 |
| 低位追空 | 经常发生 | 入场前检查价格位置 |
| 策略代码 | 850 行单文件 | 6 个模块，各 100-150 行 |
