# aistrat 趋势腿修复:5m 累积分 + 高周期确认

**日期**: 2026-06-30
**分支基线**: `feat/trend-cut-aistrat`（叠在 trend-cut `9ca15d7` + hysteresis `802ca77` 之上）
**状态**: 设计待评审

## 1. 背景与动机

aistrat 是 fade（均值回归）+ trend（突破）的混合体,但在**趋势行情里反复亏**。实测归因(最近 6 天,见会话记录):
- 多头净 −1120 / 空头净 +635;catastrophic_stop 16 次里 12 次是 LONG;ETH 06-23~25 跌 −9%(1727→1567)。
- 那笔 −45 空单是镜像:fade 06-29 的反弹被碾。

**根因在入场端的两点:**
1. **regime 分类器只认"干净趋势"**:`detectRegime()` 的效率比门控 `efficiency < TrendEfficiencyMin → RANGE` 会把**阶梯式趋势**(有涨有跌、效率低)误判成 RANGE。06-24 −9% 下跌全程记录为 `regime=RANGE`。
2. **保护性的逆势打分被绑死在 TREND regime 上**:counter-trend clamp(signal.go:303)`if regime==StrongTrend||Expansion` 才生效;**RANGE 里没有任何逆势惩罚**,两边自由 fade。被误判成 RANGE 的阶梯趋势正好掉进"零保护"桶 → 自由抄底/抄顶 → 放血。
3. 补充:老的 GPT 打分器(会"顺势高分、逆势 ≤0.25")**已从 live 路径删除**(signal.go:285 "GPT removed"),现在跑纯技术置信度,丢了那套智能。

## 2. 设计目标

把"是否趋势 / 该不该 fade"从**二元 regime 门控**改成**连续的趋势对齐评分**,并据此:
- (防守)按趋势强度**连续地**压低逆势 fade 的入场置信度——不依赖判错的 RANGE/trend 标签;
- (进攻)在趋势被**累积分 + 高周期确认**坐实后,**开一个顺势的趋势跟随仓**。

## 3. 核心设计

### 3.1 5m 累积分(只攒分,自己不下单)

新增一个**有符号**的趋势分 `trendScore`(+ 看多累积,− 看空累积),与现有 `accumLong/accumShort`(服务 fade 信号)**分开**,单一职责。机制沿用现有 accum 的"衰减 + 上限"模式。

每根 **primary(5m)bar** 更新一次(在 `generateSignal` 内,`barCount++` 之后):

```
body  = close - open                      // 有符号
delta = clamp(body/ATR, -PerBarCap, +PerBarCap)   // 强度加权,单 bar 截顶
trendScore = clamp(trendScore*Decay + delta, -ScoreMax, +ScoreMax)
```

- **强度加权**(已确认):大阳/大阴比小碎步权重高(`body/ATR`)。
- **衰减**:`Decay`(如 0.9)让分数 recency-weighted;震荡里有涨有跌、净值自然趋近 0;趋势里才单向累加。
- **单 bar 截顶 `PerBarCap`**:防一根插针 bar 独霸分数。
- **总上限 `ScoreMax`**:防溢出(呼应历史"溢出准确率仅 20%"的教训)。

纯函数:`updateTrendScore(prev, body, atr, decay, perBarCap, scoreMax) float64`。

### 3.2 下单门控(分够 + 高周期同向确认)

```
htfDir = 高周期方向(复用现成信号,不新造):
         ConfirmTF=="15m" → s.lastTrendDir   (detectRegime 已在 15m 上算 2h 净移动方向)
         ConfirmTF=="1h"  → s.lastHourlyDir   (hysteresis 后的 sticky 1h 方向)
若 trendScore >= +Threshold 且 htfDir == +1  → 触发 LONG 趋势入场
若 trendScore <= -Threshold 且 htfDir == -1  → 触发 SHORT 趋势入场
```

- **"同向即可"**(已确认):高周期方向与累积分符号一致就行,不要求更强,避免重新引入滞后。
- 触发后走现有 `openTrend()`(趋势仓、staged TP、trailing),不新造出场逻辑。
- `Threshold` = 0 时整个特性关闭(默认)。
- 入场后设**冷却**(`EntryCooldownBars`):防止分数在阈值附近抖动导致连续重复入场(阶梯趋势的 whipsaw 防护)。

纯函数:`trendEntryDir(trendScore, threshold, htfDir) int`(返回 +1/-1/0)。

### 3.3 连续逆势对齐惩罚(取代 regime 硬 clamp)

对 fade 入场的 `longConf/shortConf`,按趋势分**成比例**压低逆势那边:

```
若 side 与 trendScore 反向:
    penaltyFactor = clamp(1 - |trendScore|/FullPenaltyScore, 0, 1)
    conf *= penaltyFactor
```

- `|trendScore|` 越大(趋势越坐实)→ 逆势那边压得越狠,到 `FullPenaltyScore` 归零。
- **永远在线、平滑、不看 regime 标签**——这是堵 06-24 那个洞的核心。
- 在真震荡里 `trendScore≈0` → penaltyFactor≈1 → 不影响低吸,**range 命脉不动**。

纯函数:`trendAlignPenalty(rawConf, sideSign, trendScore, fullPenaltyScore) float64`。

## 4. 与现有机制的关系

| 机制 | 周期/触发 | 这版的关系 |
|---|---|---|
| 现有 1h-EMA 硬 block(signal.go:368)+ regime clamp | 1h / regime | **新评分启用时,连续惩罚是主力**;旧的两个先保留(互补安全),不在本次删除 |
| hysteresis(sticky 1h-dir,`802ca77`) | 1h | 可作为 §3.2 的 `htfDir=1h` 来源之一;互补 |
| trend_cut(出场早砍,`9ca15d7`) | 出场 | 不变,互补——本设计减少坏入场 + 增加好入场,trend_cut 兜已开仓 |
| −3R catastrophic | 出场 | 不变 |

三层仍是:**入场对齐评分(本设计,防+攻)+ trend_cut(砍漏进来的)+ −3R(兜底)**。

## 5. 配置(全部默认"关")

| param | 默认 | 含义 |
|---|---|---|
| `TrendScoreThreshold` | 0 | 0=整特性关;>0=入场所需累积分 |
| `TrendScoreDecay` | 0.9 | 每 bar 衰减 |
| `TrendScorePerBarCap` | 1.0 | 单 bar delta 截顶(ATR 单位) |
| `TrendScoreMax` | 5.0 | 累积分上限 |
| `TrendScoreConfirmTF` | "15m" | 确认周期 |
| `TrendAlignFullPenaltyScore` | 3.0 | 逆势惩罚归零所需分 |
| `TrendEntryCooldownBars` | 12 | 趋势入场冷却 |

灰度:默认关 → 单引擎(建议先 user4)设 param 开 → 观察。

## 6. 测试(TDD)

三个纯函数各配表驱动测试(沿用 `manage_test.go` 风格):
- `updateTrendScore`:衰减、强度加权、单 bar 截顶、总上限、震荡净值≈0。
- `trendEntryDir`:阈值边界、同向/反向 htfDir、关闭(threshold=0)。
- `trendAlignPenalty`:逆势按分压低、顺势不压、震荡(score≈0)不压、归零点。

接线(generateSignal 内的更新/惩罚/触发)是薄胶水,纯函数覆盖核心逻辑。

## 7. 风险与诚实提醒

- **历史坑**:老 accum"光累积就下单"准确率仅 20%。本设计靠 **15m/1h 确认门 + 冷却**补;但累积分仍需**灰度实测**,不能只信回测([[feedback_backtest_distrust]])。
- **阶梯趋势仍难**:分数虽平滑,阶梯行情里可能在阈值附近反复触发 → 冷却 + 高周期确认缓解,但需观察实际 whipsaw。
- **趋势入场是市价(taker)**:增手续费;可接受(趋势仓不该挂限价等)。
- **不删旧机制**:本次只"叠加 + 默认关",不动现有 regime/clamp/1h-block,降低 live 风险;验证后再谈精简。

## 8. 交付

- 一个 commit:三个纯函数 + 测试 + 接线 + config,默认关。
- 在 `feat/trend-cut-aistrat` 上叠加。
- 部署沿用既有流程(cross-build、备份、scp、install+restart、验证),param 单引擎开。
