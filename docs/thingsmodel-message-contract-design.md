# ThingsModel 北向消息契约设计（v2）

本文档描述物模型模块（ThingsModel）**北向**与 NATS 之间的消息契约：主题（Subject）设计、消息信封（Header + Data）设计、各主题的消息载体格式定义，以及周期发布、查询与控制的业务机制。北向指 ThingsModel 面向下游业务模块（储能系统、充电桩系统、光伏系统等）提供的**能力与数据接口**。

- 南向（面向网关）契约见 [gateway-message-contract-design.md](gateway-message-contract-design.md)。
- **告警/事件消息遵循平台通用标准**，见 [powerpulse-alarm-standard.md](powerpulse-alarm-standard.md)。
- 客户端配置、连接/重连/降级等**客户端设计**内容，见 [nats-client-design.md](nats-client-design.md)。
- 本版本（v2）**整体替代**旧版"能力域两级主题 + 单例无实例 ID"方案与代码中"{softwareId} 入主题"的现状，差异清单见第 10 节。

## 1. 主题定义

主题前缀固定为 `powerpulse.thingsmodel`（平台前缀 `powerpulse` + 业务模块 `thingsmodel`）。**一个站点仅部署一个 ThingsModel 实例（单例），主题不含实例 ID**，主题为固定常量，下游零配置接入；实例身份在负载中自描述（`thingsmodel_id` / `source.instance`）。告警消息发布到平台统一告警命名空间。

| 能力域 | 能力 | Subject | 方式 | PP-Message-Type |
|--------|------|---------|------|-----------------|
| 数据 | 设备数据发布 | `powerpulse.thingsmodel.data` | PUB/SUB | `data` |
| 告警 | 告警/事件发布 | `powerpulse.alarm.thingsmodel` | PUB/SUB | `alarm` |
| 查询 | 设备清单 | `powerpulse.thingsmodel.query.devices` | REQ/REP | `query` |
| 查询 | 属性定义 | `powerpulse.thingsmodel.query.properties` | REQ/REP | `query` |
| 查询 | 服务定义 | `powerpulse.thingsmodel.query.methods` | REQ/REP | `query` |
| 查询 | 告警定义 | `powerpulse.thingsmodel.query.events` | REQ/REP | `query` |
| 控制 | 服务控制下发 | `powerpulse.thingsmodel.cmd` | REQ/REP | `control` |

```
powerpulse.thingsmodel.data
powerpulse.thingsmodel.query.devices
powerpulse.thingsmodel.query.properties
powerpulse.thingsmodel.query.methods
powerpulse.thingsmodel.query.events
powerpulse.thingsmodel.cmd
powerpulse.alarm.thingsmodel        ← 平台统一告警命名空间（见告警标准）
```

**设计要点**

- **主题为固定常量**：无实例 ID、无业务元数据参与路由；`device_id`、`service_key` 等全部收束到消息载体；
- **查询拆分为四个独立主题**：设备清单 → 属性定义 / 服务定义 / 告警定义各自独立，消费方按需订阅；
- **静态定义用查询拉取，动态数据用推送**：属性值/告警状态/控制终态均为推送，定义均为查询；
- **告警走平台统一标准**：主题结构与语义遵循 [powerpulse-alarm-standard.md](powerpulse-alarm-standard.md)，本模块发布到 `powerpulse.alarm.thingsmodel`；
- **单例假设**：一个站点只有一个 ThingsModel 实例；如将来需多实例，再引入实例标识（演进点，见第 9 节）；
- 本阶段仅使用 **Core NATS**；不引入 JetStream。

## 2. 消息格式

NATS 消息采用 msg.Header + msg.Data 组合：msg.Header 承载元信息，msg.Data 承载业务 JSON。

### 2.1 消息信封

信封与南向网关侧完全一致，复用 `PP-Message-*` 四个 Header：

| Header | 说明 |
|--------|------|
| `PP-Message-ID` | 消息唯一 ID，UUIDv4 |
| `PP-Message-Type` | 消息类型，北向取值见下表 |
| `PP-Message-Version` | 消息版本号，当前 `v1.0` |
| `PP-Message-Timestamp` | 消息时间戳，Unix 毫秒 |

北向 `PP-Message-Type` 取值：

| 取值 | 含义 | 方向 / 方式 |
|------|------|-------------|
| `data` | 设备数据发布（周期 + 遥测驱动） | ThingsModel → 下游（PUB/SUB，.data） |
| `alarm` | 标准化告警/事件消息 | ThingsModel → 下游（PUB/SUB，powerpulse.alarm.thingsmodel） |
| `query` | 查询请求与应答 | 下游 → ThingsModel（REQ/REP，.query.*） |
| `control` | 服务控制请求与受理应答 | 下游 → ThingsModel（REQ/REP，.cmd） |

**REP 关联约定**：REQ/REP 的应答信封**回填请求的 `PP-Message-ID`**，消费方据此关联请求与应答。

### 2.2 通用约定

- 所有业务负载均为 **JSON 对象**，UTF-8 编码；字段统一 **snake_case**；
- REQ/REP 响应统一携带 `ok`（是否成功）与 `message`（失败原因，成功时省略）；
- 时间戳统一 **Unix 毫秒**（int64）；零值时间序列化为 `0`；
- 空值遵循 JSON 约定：`null` 表示无值，`omitempty` 字段在无值时省略；
- **未启用设备不对外**：`Enabled=false` 的设备不参与查询结果与数据/告警推送；
- **标识符体系**：`device_id` 为设备实例唯一标识；条目级 `id`（`property_N` / `method_N` / `event_N`）在设备实例化时生成、设备内稳定，用于紧凑引用与联动对账；`key` 为模板语义标识，跨配置版本稳定。**控制调用（`service_key`）与消息 map 键以 `key` 为准**，`id` 为辅助标识。

## 3. `powerpulse.thingsmodel.data` 设备数据发布

信封 `PP-Message-Type: data`，PUB/SUB。**用途**：向下游发布设备的归一化全量状态（属性值 + 告警实时状态）。

### 3.1 触发模型（纯周期发布）

北向 `.data` **只由周期调度器发布**，不保留遥测驱动的事件式即时发布：

- 南向遥测到达 / 逻辑点位写入只更新内部状态（属性归一化、告警评估与持续时间计时器照常推进），**不触发发布**；
- 每设备独立节拍到期时发布该设备全量快照，变化可见时延 ≤ 一个周期；
- 代价：**短于一个周期的瞬态告警**（触发又恢复落在同一拍内）不出现在北向消息中（内部有记录）；调大周期时需意识到该窗口；
- 收益：发布速率恒定 N/P，无遥测突发洪峰，队列压力可预期。

### 3.2 周期发布机制

- **全局统一周期 `publishInterval`**（配置项，毫秒；`0` = 关闭周期发布，默认 `5000`）；
- **每设备独立节拍、不排队**：每设备一个独立 ticker，相位均匀错开（第 `i` 个设备首拍 = `i × P / N`），各设备发布周期严格等于 P，设备之间不串行、不形成同一瞬间的消息洪峰；
- 设备加入调度时**先立即发布一拍**（初始状态可见），再进入自身相位周期；
- 周期发布走**纯读路径**：不推进告警持续时间计时器，只做状态投影（见 3.4）。

### 3.3 载体定义

```golang
// NormalizedData 设备数据发布载体（周期与遥测驱动共用）。
type NormalizedData struct {
    ThingsModelID   string                        `json:"thingsmodel_id"` // 实例标识（software.id）
    DeviceID        string                        `json:"device_id"`
    DeviceName      string                        `json:"device_name"`
    TemplateCode    string                        `json:"template_code"`
    TemplateVersion string                        `json:"template_version"`
    DataStatus      string                        `json:"data_status"` // available / degraded / unavailable
    ConfigRevision  int64                         `json:"config_revision"` // 设备配置版本号：变化=定义已变更，应重查定义
    Properties      map[string]NormalizedProperty `json:"properties"` // 属性 key → 归一化值
    Events          map[string]NormalizedEvent    `json:"events"`     // 告警 key → 实时状态
    Seq             uint64                        `json:"seq"`        // 发布序列号（设备内单调递增）：跳号=有丢失，等下一拍自愈
    Timestamp       int64                         `json:"timestamp"`
}

// NormalizedProperty 归一化后的单个属性值。
type NormalizedProperty struct {
    ID        string `json:"id,omitempty"` // 实例条目 ID（property_N）
    Name      string `json:"name"`
    Unit      string `json:"unit"`
    Value     any    `json:"value"`
    Timestamp int64  `json:"timestamp"` // 最近一次来源时间，Unix 毫秒；0 表示无时间来源
    Quality   string `json:"quality"`
}

// NormalizedEvent 单条告警的实时状态（定义见 .query.events）。
type NormalizedEvent struct {
    ID        string `json:"id,omitempty"` // 实例条目 ID（event_N）
    Name      string `json:"name"`
    Level     int    `json:"level"`
    Status    string `json:"status"` // active / pending / inactive / unavailable / unbound
    Active    bool   `json:"active"`
    Value     any    `json:"value"`      // 监测点位当前归一化值（无数据为 null）
    Point     string `json:"point"`      // 监测属性 key
    PointName string `json:"point_name"` // 监测属性名称
    Timestamp int64  `json:"timestamp"`
}
```

`quality` 语义：`good` 正常、`default` 逻辑点位默认值、`unbound` 未绑定、`unavailable` 缺少来源、`invalid` 解析/运算非法、`stale` 来源超时（超过 `staleAfter`）、`unmapped` 状态值未匹配。

### 3.4 断流投影（B 方案）

数据断流（属性值龄超 `staleAfter`）后，周期发布对告警状态做**投影迁移**：

- 已 `active` 的告警在发布载体中投影为 `unavailable`（判断依据已失效，状态不可信）；
- 投影**不改变**内部评估状态，**不产生**告警消息（"不可信" ≠ "恢复"，不发 recover）；
- 数据恢复后重新评估：条件不满足 → `active → inactive`，此刻发 recover（见第 4 节）；条件仍满足 → 回到 `active`，按沿规则可能重新发 trigger；
- 属性 `stale` 质量照常出现在 `properties` 中，下游可独立感知断流。

## 4. 告警与事件发布（平台标准）

告警/事件消息的**主题、信封、消息结构、字段语义、节流与幂等约定**全部遵循平台通用标准 [powerpulse-alarm-standard.md](powerpulse-alarm-standard.md)；本模块发布到 `powerpulse.alarm.thingsmodel`，`source.module = "thingsmodel"`。本节只约定 ThingsModel 的**生产规则**。

### 4.1 设备告警沿触发规则

沿检测基于"**上次发布状态 → 本次发布状态**"的迁移，在周期发布路径执行（发布边界即检测边界）：

| 状态迁移 | 动作 |
|----------|------|
| 非 active → active | 发 `kind=trigger`（`category=device`，含点位、触发值、规则） |
| active → inactive | 发 `kind=recover` |
| active → unavailable（断流投影） | **不发消息**（仅体现在 .data） |
| unavailable → active（数据恢复且条件仍满足） | 发 `kind=trigger`（下游重新武装） |
| 其他迁移（pending 内部流转等） | 不发消息 |

实时状态（含 pending）始终随 `.data` 周期/即时发布，沿消息只关注 trigger/recover 两个业务关注点。

### 4.2 控制终态（成功静默）

服务控制的执行终态采用**仅失败发布**策略（成功静默，避免提示性消息刷屏）：

- `thingsmodel.cmd.result`：**仅执行失败时发布**（category=software、kind=event、level=2），携带 `request_id` 供调用方闭环，不聚合（标准第 7 节"闭环类必达"）；执行成功**不发布任何消息**；
- 成功回执路径：调用方在受理（REP accepted）后未收到失败告警，且目标属性值变化随后体现在 `.data` 推送中——**以数据变化作为成功回执**；
- `thingsmodel.cmd.frequent_failure`：同 `(device_id, service_key)` 在固定窗口（10s）内失败达阈值（5）时发布一条聚合告警（`count` = 达阈值时计数），每窗口最多一条。

### 4.3 模型定义变更提示

设备或其定义发生变更时，发布一条**提示性事件**（不做周期性对账）：

- 事件码 `thingsmodel.model.changed`（category=software、kind=event、level=0）；
- `context`：`device_id`、`action`（created / updated / deleted / disabled）；
- 触发时机：设备创建、配置保存（`config_revision` 递增）、删除、启停切换；
- 下游收到后按需重查 `query.devices` 与对应定义主题；`deleted` / `disabled` 即**墓碑**，下游应移除缓存。

### 4.4 事件码登记

ThingsModel 的事件码已在标准文档第 6 节登记：`thingsmodel.cmd.result`、`thingsmodel.cmd.frequent_failure`、`thingsmodel.model.changed`。新增事件码须先登记再使用。

## 5. 查询主题（REQ/REP）

四个查询主题共享同一交互模式：REQ 携带参数 → REP 携带 `ok/message` 与结果。REP 信封回填请求 `PP-Message-ID`（见 2.1）。`device_id` 参数必填（除设备清单查询）；设备不存在时返回 `ok=false`。

### 5.1 `powerpulse.thingsmodel.query.devices` — 设备清单查询

**用途**：查询物模型配置了哪些设备（不含属性/服务/告警定义，定义见 5.2~5.4）。

```golang
// DevicesQueryRequest 设备清单查询请求（无参数）。
type DevicesQueryRequest struct{}

// DevicesQueryResponse 设备清单查询响应。
type DevicesQueryResponse struct {
    OK      bool            `json:"ok"`
    Message string          `json:"message,omitempty"`
    Devices []DeviceSummary `json:"devices"`
}

// DeviceSummary 设备实例概要。
type DeviceSummary struct {
    DeviceID        string `json:"device_id"`
    DeviceName      string `json:"device_name"`
    TemplateCode    string `json:"template_code"`
    TemplateVersion string `json:"template_version,omitempty"`
    Description     string `json:"description,omitempty"`
    Enabled         bool   `json:"enabled"`
    DataStatus      string `json:"data_status,omitempty"` // available / degraded / unavailable
    PropertyCount   int    `json:"property_count"`
    MethodCount     int    `json:"method_count"`
    EventCount      int    `json:"event_count"`
}
```

### 5.2 `powerpulse.thingsmodel.query.properties` — 属性定义查询

**用途**：查询指定设备的属性定义（key/名称/单位/类型/聚合方式等）。

```golang
// PropertiesQueryRequest 属性定义查询请求。
type PropertiesQueryRequest struct {
    DeviceID string `json:"device_id"`
}

// PropertiesQueryResponse 属性定义查询响应。
type PropertiesQueryResponse struct {
    OK         bool                `json:"ok"`
    Message    string               `json:"message,omitempty"`
    DeviceID   string               `json:"device_id"`
    Properties []PropertyDefinition `json:"properties"`
}

// PropertyDefinition 单个属性的定义。
type PropertyDefinition struct {
    ID           string       `json:"id"`                     // 实例条目 ID（property_N，设备实例化时生成）
    Key          string       `json:"key"`
    Name         string       `json:"name"`
    Unit         string       `json:"unit,omitempty"`
    Type         string       `json:"type"`                   // status | number
    Mode         string       `json:"mode"`                   // physical | logical
    Method       string       `json:"method"`                 // ept / sum / avg / min / max / and / or / not
    Default      any          `json:"default,omitempty"`
    Descriptions []StatusDesc `json:"descriptions,omitempty"` // type=status 时：状态值（enum/key/name/value）
}
```

### 5.3 `powerpulse.thingsmodel.query.methods` — 服务定义查询

**用途**：查询指定设备支持哪些控制（服务）、参数约束如何构造。控制调用方**必须先查此主题**再构造 `.cmd` 请求。

```golang
// MethodsQueryRequest 服务定义查询请求。
type MethodsQueryRequest struct {
    DeviceID string `json:"device_id"`
}

// MethodsQueryResponse 服务定义查询响应。
type MethodsQueryResponse struct {
    OK      bool               `json:"ok"`
    Message string             `json:"message,omitempty"`
    DeviceID string             `json:"device_id"`
    Methods []MethodDefinition `json:"methods"`
}

// MethodDefinition 单个服务（控制能力）的定义。
type MethodDefinition struct {
    ID          string       `json:"id"`                     // 实例条目 ID（method_N，设备实例化时生成）
    Key         string       `json:"key"`
    Name        string       `json:"name"`
    Type         string       `json:"type"`                  // status | number
    Bound        bool         `json:"bound"`                 // 是否已绑定目标点位（false 不可控制）
    TargetMode   string       `json:"target_mode,omitempty"` // physical | logical（bound 时给出）
    Validation   *Validation  `json:"validation,omitempty"`  // type=number：工程值范围
    Descriptions []StatusDesc `json:"descriptions,omitempty"` // type=status：合法工程值枚举
}
```

### 5.4 `powerpulse.thingsmodel.query.events` — 告警定义查询

**用途**：查询指定设备配置了哪些告警、可能产生哪些告警（规则定义）。告警**实时状态**不走查询，由 `.data`（全量快照）与平台告警主题（沿消息）推送承载。

```golang
// EventsQueryRequest 告警定义查询请求。
type EventsQueryRequest struct {
    DeviceID string `json:"device_id"`
}

// EventsQueryResponse 告警定义查询响应。
type EventsQueryResponse struct {
    OK      bool              `json:"ok"`
    Message string            `json:"message,omitempty"`
    DeviceID string            `json:"device_id"`
    Events  []EventDefinition `json:"events"`
}

// EventDefinition 单条告警的定义。
type EventDefinition struct {
    ID                  string  `json:"id"`                     // 实例条目 ID（event_N，设备实例化时生成）
    Key                 string  `json:"key"`
    Name                string  `json:"name"`
    Description         string  `json:"description,omitempty"`
    Level               int     `json:"level"` // 0 提示 / 1 一般 / 2 严重 / 3 紧急
    Type                string  `json:"type"`  // equal | upper | lower
    Threshold           float64 `json:"threshold"`
    Time                int     `json:"time"` // 持续时间（毫秒）
    BindingProperty     string  `json:"binding_property"`      // 监测属性 key（空=未绑定）
    BindingPropertyName string  `json:"binding_property_name,omitempty"`
}
```

## 6. `powerpulse.thingsmodel.cmd` 服务控制下发

信封 `PP-Message-Type: control`，REQ/REP。**用途**：北向消费者向指定设备的一个服务下发控制命令。

```golang
// ControlRequest 服务控制下发请求。
type ControlRequest struct {
    DeviceID   string  `json:"device_id"`   // 目标设备实例 ID
    ServiceKey string  `json:"service_key"` // 服务标识符（来自 .query.methods）
    Value      float64 `json:"value"`       // 工程值（number 为数值；status 为状态工程值）
}

// ControlAck 服务控制受理应答（REQ/REP 同步返回）。
type ControlAck struct {
    RequestID  string `json:"request_id"`  // 原请求 PP-Message-ID
    DeviceID   string `json:"device_id"`
    ServiceKey string `json:"service_key"`
    Status     string `json:"status"` // accepted / failure
    Message    string `json:"message,omitempty"`
}
```

### 两段式模型

```
下游 ──REQ(.cmd)──► ThingsModel ──校验/受理──► REP {request_id, accepted|failure}
                      │
                      ├─ 同步 failure：设备/服务不存在、未绑定、参数越界 —— 调用方当场可知，不发告警
                      │
                      └─ accepted：复用 InvokeMethod 管道
                            ├─ logical：写缓存→即时终态
                            └─ physical：南向 .cmd 下发网关，等待 cmdAck
                                      │
                                      ▼ 终态（仅失败发布，成功静默）
                        powerpulse.alarm.thingsmodel
                        (category=software, kind=event, code=thingsmodel.cmd.result)
                        context: {request_id, device_id, service_key, service_id, value, message}
```

- **REP 只表达受理**（accepted/failure），不表达执行终态；
- **终态仅失败经平台告警主题**（`thingsmodel.cmd.result`，level=2），调用方以 `request_id` 闭环；成功静默，以 `.data` 属性值变化为成功回执；
- 北向 `.cmd` 与既有 HTTP `POST /api/runtime/devices/{id}/methods/{key}` 共享同一 `InvokeMethod` 实现（校验、logical/physical 分流、命令记录），行为一致；
- 终态失败同时受高频失败聚合节流保护（聚合告警不替代单条终态）；
- **重复请求不去重**：同一控制重复下发会重复执行，重试语义由调用方自行保证；
- **并发下发**：同设备同服务并发请求按到达顺序执行，各自独立产生终态；
- **执行超时**：受理后网关回报等待上限为 **30 秒**（常量 `defaultCmdAckTimeout`，本期不可配置），超时按失败终态发布。

## 7. 下游接入引导（bootstrap）

新订阅者（或重启的消费方）错过了历史沿消息，必须按以下顺序建立基线，增量消息才有意义：

1. **查清单与定义**：`query.devices` + 按需 `query.properties` / `query.methods` / `query.events`，建立设备与定义缓存；
2. **建全量状态基线**：订阅 `.data`，等待一个完整发布周期，取每设备最新一拍（含当前 active 告警、属性值与质量）；
3. **接增量**：订阅 `powerpulse.alarm.thingsmodel`，此后沿消息叠加在基线之上；
4. **维护缓存**：收到 `thingsmodel.model.changed` 或发现 `config_revision` 变化 → 重查对应定义；`deleted` / `disabled` → 移除缓存；发现 `seq` 跳号 → 等下一拍自愈（周期快照幂等）。

注：步骤 2 与 3 顺序可互换——先订告警再等基线更稳妥，最多多收几条与基线重复的沿消息，按定位键幂等合并即可。

## 8. 配置与常量

| 项 | 位置 | 默认 | 说明 |
|----|------|------|------|
| `publisher.publishInterval` | 配置项 | `5000` | 周期发布间隔（毫秒），`0` 关闭周期发布 |
| 告警节流窗口 | 常量 | `10s` | `thingsmodel.cmd.frequent_failure` 聚合窗口 |
| 告警节流阈值 | 常量 | `5` | 窗口内失败达阈值触发聚合告警 |

## 9. 实现切分（最终）

| # | 文件 | 内容 |
|---|------|------|
| 1 | `internal/natsclient/message.go` | 契约结构：查询 REQ/REP 系、`ControlRequest/ControlAck`、`AlarmMessage` 系（按平台标准）；`NormalizedData` 扩展（`config_revision` / `seq`、`NormalizedEvent` 的 `id/point/point_name/value`）；信封 type 常量（query/control/alarm） |
| 2 | `internal/natsclient/north.go` | 主题修订：`.data` 去 ID → `powerpulse.thingsmodel.data`；告警主题 → `powerpulse.alarm.thingsmodel`；**队列隔离**：data 队列保持丢最旧，alarm 走独立不丢队列（队满阻塞 + 告警日志，绝不丢弃）；`PublishAlarm(AlarmMessage)` 结构化改造 |
| 3 | `internal/natsclient/northapi.go`（新） | 四个查询主题 + `.cmd` 的 REQ/REP responder；响应不经发布队列，在订阅回调内直接 reply；REP 信封回填请求 `PP-Message-ID` |
| 4 | `internal/runtime/registry.go` | **去掉 `Ingest` / `WriteLogical` 的扇出返回**（调度器为唯一发布源，遥测仍推进内部评估）；每设备 `seq` 计数；周期快照纯读（含 B 方案 unavailable 投影）；告警沿检测（`lastPublished` 状态）；设备清单/属性/服务/告警定义查询读取方法 |
| 5 | `internal/runtime/manager.go` | 周期调度器（每设备独立节拍、相位错开、设备加入先发一拍、`Apply`/`Remove` 同步设备集）；`.cmd` → `InvokeMethod` 接线；`finalizeCommand` 挂钩发布 `thingsmodel.cmd.result`（仅失败）；高频失败聚合节流器；`thingsmodel.model.changed` 发布挂钩（创建/保存/删除/启停） |
| 6 | `internal/config/config.go` | `publisher.publishInterval` 字段、默认值（5000）与校验（0=关闭） |
| 7 | `internal/web/static/js/settings.js` 等 | 北向配置 UI 增加 `publishInterval` |
| 8 | `internal/natsclient/message_test.go` 等 | 契约序列化、沿检测（拍边界）、B 投影、seq、节流、队列隔离行为测试 |

## 10. 与既有方案的差异（替代记录）

| 方面 | 旧契约文档（v1，已废弃） | 代码现状 | v2（本版） |
|------|--------------------------|----------|------------|
| 主题模式 | 能力域两级（`device.query` 等），单例无实例 ID | `powerpulse.thingsmodel.{softwareId}.data/.alarm`，实例 ID 入主题 | 模块命名空间固定主题（`powerpulse.thingsmodel.*`），**无实例 ID**；实例身份在负载 |
| 告警 | `alarm.status` 增量状态包（模块私有） | `.alarm` 主题预留、无调用方 | **平台通用标准**（独立文档），`powerpulse.alarm.thingsmodel`，category/kind/source 标准化 |
| 查询 | `device.query`/`alarm.query`/`service.query` 三主题 | 无 | `.query.devices/properties/methods/events` 四主题；告警查询为**定义**查询，实时状态由推送承载 |
| 控制终态 | `controlAck` 独立类型 + `service.status` 主题 | 无 | 仅失败经平台告警主题 `thingsmodel.cmd.result` 发布，成功静默（`.data` 值变化为回执） |
| 周期发布 | 无 | 仅遥测驱动（事件式即时发布） | **纯周期发布**：每设备独立节拍（相位错开、不排队），遥测只更新内部状态 |
| 断流告警 | 未定义 | 冻结（静默） | B 方案：stale 投影 unavailable，不发 recover |
| 节流 | 无 | 无 | 终态必达 + 高频失败聚合 |
| 消息可靠性 | 无 | 单队列丢最旧 | data 丢最旧 + **alarm 独立不丢队列**；`seq` 跳号检测丢失 |
| 保留 | PP-Message 信封、snake_case、`ok/message`、两段式控制、未启用设备不对外 | — | 同左 |

## 11. 演进点（本期不实现，仅记录）

- **消息持久化**：当前仅 Core NATS，无持久化/重放；站点级长断网期间的沿消息不可补发，恢复后由周期拍自愈。**未来引入 JetStream**；
- **控制通道安全与优先级**：暂不考虑；未来按需引入 NATS 账号/ACL 隔离或应用层校验；
- **cmdAck 超时配置化**：当前固定 30 秒常量，未来开放为配置项；
- **查询响应分页**：暂不考虑大规模响应，未来按需引入分页/过滤。
