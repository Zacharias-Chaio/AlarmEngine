# alarm-sender — 标准告警模拟发送工具

经 NATS 总线按 [平台告警标准](../../docs/powerpulse-alarm-standard.md) 发布标准告警/事件消息（`PP-Message-*` 信封 + `AlarmMessage` 负载），用于模拟各种工况、支撑长期测试与发送侧对账。消息结构直接复用引擎的 `internal/natsclient` 契约定义，与真实发布方保持单一事实来源。

## 前置条件

- NATS 服务（≥ 2.2，需支持消息头），默认 `nats://127.0.0.1:4222`；
- AlarmEngine 已启动，且在「软件配置」中启用 NATS 客户端（订阅 `powerpulse.alarm.>`）。

## 快速开始

```powershell
# 查看全部工况
go run ./tools/alarm-sender -list

# 5 分钟稳态冒烟
go run ./tools/alarm-sender -duration 5m

# 只看不发（不连接 NATS）
go run ./tools/alarm-sender -dry-run -v -duration 10s
```

## 参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-url` | `nats://127.0.0.1:4222` | NATS 服务地址 |
| `-subject` | `powerpulse.alarm.thingsmodel` | 目标主题（标准命名空间） |
| `-module` | 取主题二级命名空间 | `source.module`（与主题须一致，见标准 §2） |
| `-instance` | 同 module | `source.instance` |
| `-scenario` | `normal` | 工况，逗号分隔可组合；`list` 查看全部 |
| `-duration` | `0` | 运行时长（如 `30m`、`24h`），0 表示持续运行直到 Ctrl+C |
| `-interval` | `1s` | 发送节拍基础间隔 |
| `-jitter` | `0.2` | 节拍随机抖动比例（0~1），避免机械等间隔 |
| `-burst` | `100` | storm 工况每轮突发条数 |
| `-hold` | `30s` | 设备告警触发→恢复的基准时长（随机 0.5~2 倍抖动） |
| `-report` | `30s` | 统计报告间隔 |
| `-flush` | `5s` | NATS Flush 间隔（探测发送故障） |
| `-seed` | `0` | 随机种子，固定后序列可复现；0 按当前时间 |
| `-devices` | `8` | 模拟设备数量 |
| `-v` | 关 | 打印每条发送消息 |
| `-dry-run` | 关 | 仅构造并打印消息，不连接 NATS |
| `-log` | 空 | 发送日志（JSON Lines，每条一行），供与引擎侧对账 |

## 工况

| 工况 | 说明 | 覆盖的引擎消费路径 |
|------|------|--------------------|
| `normal` | 稳态综合：设备触发/恢复为主，夹杂软件与系统事件，含平静期 | 常规入库 + 自动恢复 |
| `device` | 设备告警：trigger→保持→recover 配对，覆盖 upper/lower/equal 规则与 L0~L3 | 落库、按定位键合并、恢复闭环 |
| `software` | 标准登记事件码：`cmd.result` / `cmd.frequent_failure`（含 count）/ `model.changed` | 一次性事件入库 |
| `system` | 平台级系统事件（事件码标准预留，此处为模拟样例） | system 类入库 |
| `storm` | 告警风暴：每轮 `-burst` 条触发沿（少量全新定位键 + 大量重复定位键） | 高并发落库与合并 |
| `aggregate` | 节流聚合：同定位键重复触发携带 count，偶发恢复后再聚合 | count 累加合并 |
| `duplicate` | 原样重发已发送消息（同 `alarm_id`） | 幂等去重 |
| `orphan-recover` | 无匹配触发沿的 recover | 容错忽略（日志可见） |
| `invalid` | 缺信封头/错类型/坏时间戳/坏负载/未知 category/未知 kind/缺明细/级别越界 | 信封与负载校验、按提示降级 |
| `mismatch` | 主题与 `source.module` 不一致 | 以负载为准 + 告警日志 |
| `mixed` | 上述全部按权重轮流出现（风暴自动降规模） | 全路径 |

## 典型用法

```powershell
# 24 小时长测：固定种子可复现，发送日志落盘对账
go run ./tools/alarm-sender -duration 24h -seed 42 -report 1m -log .\logs\sender.jsonl

# 告警风暴压测：每 3 秒突发 500 条
go run ./tools/alarm-sender -scenario storm -burst 500 -interval 3s -duration 10m -v

# 幂等与容错专项：重复 + 孤儿恢复 + 非法消息 + 模块不一致
go run ./tools/alarm-sender -scenario duplicate,orphan-recover,invalid,mismatch -duration 10m

# 全工况混合（mixed 不能与其他工况组合）
go run ./tools/alarm-sender -scenario mixed -duration 1h

# 加大设备规模、缩短恢复周期
go run ./tools/alarm-sender -devices 32 -hold 5s -interval 200ms
```

## 输出说明

- `[INIT]` 启动横幅：总线、主题、来源、工况、节拍、时长、种子；
- `[MSG]`/`[DRY]`（`-v` 或 dry-run）每条消息一行：主题、类别/生命周期、级别、alarm_id 前 8 位、明细；
- `[STAT]` 周期统计：总量、周期增量与速率、失败数，按类别/生命周期/级别/工况分维计数；
- `[WARN]` 发布失败、连接断开/重连、Flush 失败；
- `[DONE]` 终期汇总；若退出时有已触发未恢复的告警，会提示条数（这些告警在引擎侧保持活动状态，属预期行为）。

## 与引擎侧对账

1. 发送端开启 `-log sender.jsonl`，每条消息记录 `sent_at`、`subject`、`alarm_id`、`scenario` 与完整负载；
2. 引擎侧「历史记录」按时间/类型/来源筛选或导出 CSV，与发送日志按 `alarm_id` 核对；
3. 预期规则：`duplicate` 工况的重发被幂等丢弃（不新增记录）；`orphan-recover`/`invalid`/`mismatch` 在引擎日志有对应告警但不入库（`invalid` 中级别越界按 L0 降级入库）；`aggregate` 的重复触发合并进同一条活动告警且 count 累加；`device` 的 recover 使对应活动告警关闭并计算持续时间。

## 注意事项

- 软件/系统事件为一次性 `event`（无恢复沿），长期运行会持续累积历史记录，属引擎设计行为，适合观察统计报表；
- `system` 事件码在标准 §6 中预留，本工具使用模拟样例码，不代表已登记事件码；
- 断线自动重连（无限重试），重连期间发布失败计入 `失败` 计数；
- 退出请用 Ctrl+C（SIGINT/SIGTERM），工具会 Drain 连接并输出终期汇总。