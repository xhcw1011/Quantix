# aistrat 多周期数据可配置化设计

**日期**: 2026-08-04
**分支基线**: `worktree-research+grid-no-vol-decline-gauge`（叠在本 session 的多lot记账修复 + TrendCutR/RangeEntryConf/TrendMaxRegimeAge 之上）
**状态**: 设计已确认 → 待写实现计划

## 1. 背景与动机

本 session 反复验证了网格腿（range regime）的残余亏损主因是"假 range"入场——`detectRegime()` 判成 Range 开了网格，随后价格走出真趋势，被 `TrendCutR` 早砍（41笔 -2809.8，是 grid_tp 全部盈利 +915.8 的 3 倍）。用户提出一个假设：更长的时间框架下，趋势发生频率更低、区间震荡期更长，网格可能表现更好。

尝试验证这个假设时发现两层障碍：

1. **表层**：直接把 `-interval` 换成 `4h` 会触发一个按 5m 波动率尺度写死的极端波动率闸门（`atr/price > 0.05`），4h bar 中位数振幅就有 1.77%，闸门几乎把所有 bar 都拦住，导致 0 笔交易——测不出任何可解读的结果。

2. **深层、更根本**：即使避开上面的闸门，`detectRegime()`/`hourlyTrendDir()`/`detectHourlyMode()`/`TrendExhaustPct`/MTF打分 这 5 处活跃代码全部**硬编码字符串 `"15m"`** 去取 context bar 数据，不看 `PrimaryInterval` 也不受任何配置项影响。换 `-interval` 只改变了策略"多久做一次决策"，`regime` 判定本身用的窗口纹丝不动。想要真正测试"regime 判定用更粗周期"这个变量，必须先让这份 15m 依赖变成可配置的。

## 2. 设计目标

把 5 处硬编码 `"15m"` 中的 3 处真正在用的**用途**（1 处死代码 `gpt.go` 和 1 处无关的 `PrimaryInterval` 字符串比较不在范围内）抽成 3 个独立可配置的 interval 开关，让后续可以干净地做"只改 regime 判定周期、其余不变"这类单变量实验，不预设哪个周期组合更好。

**不做的事**：不在这次设计里改任何默认行为、不预判 4h/12h 是否更优、不碰极端波动率闸门（那是另一个独立问题，如果后续要在粗周期上测试需要单独处理，本次只解决"能不能干净地配置周期"这一层）。

## 3. 核心设计

### 3.1 三个新配置字段，按用途分组

```go
// RegimeInterval selects which interval's bars detectRegime() reads for
// its trend-direction/efficiency-ratio classification. "" (zero value)
// and "15m" are equivalent — both mean "use 15m", the long-standing
// hardcoded default. Falls back to primary-interval bars if this
// interval's data isn't loaded (same fallback detectRegime() already had).
RegimeInterval string

// HTFInterval selects which interval's bars hourlyTrendDir() (used by
// RangeTrendFilter + TrendCutR) and detectHourlyMode() (trailing 3-tier
// selection) read. "" / "15m" = current default behavior.
HTFInterval string

// EntryFilterInterval selects which interval's bars the TrendExhaustPct
// distance-from-4h-extreme check and the MTF confidence score read.
// "" / "15m" = current default behavior.
EntryFilterInterval string
```

三个字段互相独立，可以任意组合（比如 `RegimeInterval="4h"` 但 `HTFInterval`/`EntryFilterInterval` 留在 `"15m"`），不会互相牵连。

### 3.2 解析 helper（空值兜底 + 复用）

```go
// resolveInterval returns iv, or "15m" if iv is empty — the long-standing
// default before these fields existed. Centralizes the zero-value fallback
// so every call site (and any Config{} literal built outside DefaultConfig())
// behaves consistently.
func resolveInterval(iv string) string {
	if iv == "" {
		return "15m"
	}
	return iv
}
```

三个 call-site 分组各自用一个薄 wrapper 取数据 + 打校验日志（见 3.3），例如：

```go
func (s *AIStrategy) regimeBars() []exchange.Kline {
	iv := resolveInterval(s.cfg.RegimeInterval)
	bars := s.barsForInterval(iv)
	if len(bars) == 0 && iv != "15m" {
		s.log.Warn("AI: RegimeInterval configured but no data loaded — falling back",
			zap.String("configured", iv))
	}
	return bars
}
```

`detectRegime()` 内的 `s.barsForInterval("15m")` 替换成 `s.regimeBars()`；`hourlyTrendDir()`/`detectHourlyMode()` 替换成对应的 `s.htfBars()`；`TrendExhaustPct` 检查（signal.go:351）和 MTF 打分（signal.go:435）替换成 `s.entryFilterBars()`。

### 3.3 安全校验：配置了但数据没到位时要出声

现状的隐患：如果配置了 `RegimeInterval="4h"` 但忘了在 `-context-intervals` 里同步传 `4h`，策略会静默拿到空 slice、走原有 fallback（退回主周期数据），测出来的"4h效果"其实根本没用 4h 数据——跟这次挖出的 HTF 闸门"配了但从没触发过"是同一类坑，之前是靠人工核对交易笔数才发现的。

设计上加一条防护：3.2 的每个 wrapper 只要"配置的不是默认值 `15m`"且"取到的数据是空的"，就打一条 WARN 日志明确指出配置和实际生效状态不一致。不做硬失败（保持现有 graceful-degradation 行为不变，只是现在会被看见）。

### 3.4 改动范围

**Go 代码**：
- `internal/strategy/aistrat/config.go`：新增 3 个字段 + params 解析 + DefaultConfig 默认值（都是 `"15m"`）
- `internal/strategy/aistrat/helpers.go`：新增 `resolveInterval`、`regimeBars`、`htfBars`、`entryFilterBars`；`detectRegime()`/`hourlyTrendDir()`/`detectHourlyMode()` 内部改用新 helper
- `internal/strategy/aistrat/signal.go`：`TrendExhaustPct` 检查、MTF 打分两处改用 `entryFilterBars()`
- 不动：`gpt.go`（死代码，超出范围）、`helpers.go:537`（`PrimaryInterval == "15m"` 字符串比较，跟取 context 数据无关）

### 3.5 测试计划

- **单测**：`resolveInterval` 空值兜底；三个 wrapper 在"字段为空/字段为15m/字段为其他值但数据存在/字段为其他值但数据缺失(触发WARN)"四种情况下的行为
- **回归**：默认配置跑一次 ETH 标准回测（Jan25-May9），确认跟当前基线**字节对字节一致**（这是本 session 每次改动后的标准验证方式）
- **新能力验证**：跑一次 `RegimeInterval="4h"`（`-context-intervals` 同步加 `4h`），确认 regime 分布/交易笔数相比基线确实发生变化——这是第一次能证明"配置真的生效了"，而不是像 HTF 闸门那次事后才发现测了个寂寞

## 4. 后续（不在本次范围）

如果 3.5 的新能力验证通过、且未来要真正测试粗周期 regime 判定，还需要：
- 处理 `atr/price > 0.05` 极端波动率闸门在粗周期下的失效问题（这是本次发现但未解决的独立问题）
- 视情况处理其他按 5m 尺度校准的阈值（ATR 倍数类参数——trailing 距离、trend_cut 阈值等——本次不动，用户已明确这是"独立 session 的工作量"）

这些留到真正要做粗周期实验时再处理，不在这次"让配置可实验"的范围内。
