# ADR-0001：告警摄取采用 Core NATS（at-most-once）

- 状态：已接受
- 日期：2026-10-16
- 关联：docs/thingsmodel-message-contract-design.md §11、docs/alarm-engine-design-guide.md §10

## 背景

告警引擎的唯一常驻订阅是 `powerpulse.alarm.>`（Core NATS，无 JetStream）。
本决策显式声明其投递语义及已知后果，避免后续排障时的预期错位。

## 决策

告警沿消息摄取为 **at-most-once**：

- 引擎进程不在线（重启/崩溃/断连）期间发布的沿消息**不会补投**；
  `ReconnectBufSize` 只缓冲本进程发布侧（.cmd 请求），与订阅侧无关。
- 保护动作管道同样继承该语义：错过的 trigger 沿不会触发补下发。

## 后果与缓解

| 后果 | 缓解 |
|------|------|
| 停机期间 trigger/recover 丢失，活动告警状态漂移 | 1) 断流后契约 §4.1 重触发沿（重新武装）自愈；2) `model.changed` 墓碑自动恢复；3) 定义对账解除/标记孤儿；4) 二期候选：启动时一次性订阅 `.data` 对账活动状态 |
| 停机期间 cmd.result 丢失，动作终态不明 | 启动清扫器将超窗 pending/accepted 记为 unknown（终态未知），而非永久中间态 |
| 引擎侧动作记录跨进程不可靠 | ActionRecord 全部落库；推定成功窗口（35s）为进程内定时器，重启后由清扫器接管 |

## 未来演进

若业务要求摄取不丢沿（exactly-once 需求），迁移点收敛在 `internal/natsclient`：
将 `Subscribe` 替换为 JetStream durable consumer，摄取主链（幂等/定位键/闭环）无需改动。
