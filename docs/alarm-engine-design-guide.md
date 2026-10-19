# 告警微服务（AlarmEngine）设计指导文档

> 本文档从参考实现 **ThingsModel（物模型软件）** 中提炼可复用的架构设计规范、Web 设计规范、前后端协作规范、技术栈与实现方式，固化为**标准范式**，为告警微服务开发提供设计基线。
> 提炼来源：`ThingsModel/docs/frontend-backend-collaboration.md`、`ThingsModel/docs/web-design-guide.md` 及 `ThingsModel/internal/` 实际实现。

## 一、文档定位

| 关注点 | 说明 |
|--------|------|
| 本文档 | 告警微服务的架构、前后端、Web、数据契约、消息契约与模块设计范式 |
| 范式来源 | ThingsModel 单体二进制 + 嵌入式 SPA + 接口化分层 |
| 设计目标 | 新增模块无需重复决策，直接套用代码骨架与 Checklist |

## 二、架构总览

### 2.1 单体二进制 + 嵌入式前端

```
+-------------------- HTTP Server (chi v5) --------------------+
|  /api/*  -> api.Server -> store (GORM/SQLite)              |
|                         -> runtime (NATS 客户端热重载)       |
|                         -> logging (slog 热重载)            |
|  /*      -> embed.FS   -> 静态 HTML/JS/CSS 单页应用         |
+--------------------------------------------------------------+
```

**核心特征**：前后端同源部署，静态资源经 `//go:embed` 编入二进制，无独立前端服务、无构建工具。

### 2.2 分层职责

| 层 | 目录 | 职责 | 对外暴露 |
|----|------|------|----------|
| 入口 | `main.go` | 装配、信号监听、优雅退出 | - |
| 配置 | `internal/config` | 平台设置类型与校验（持久化 SQLite） | `config.App` |
| 路由 | `internal/web` | chi 路由、静态分发、请求日志 | `Router(...)` |
| API | `internal/api` | REST 处理器（只做 HTTP 编排，校验/状态机委托 domain） | `api.Server` |
| 领域 | `internal/domain` | 规则校验、告警确认/结案状态机（纯函数，可复用可单测） | `ValidateRule` / `HandleReason` |
| 存储 | `internal/store` | GORM 模型与迁移、平台设置 | `store.Open(...)` |
| 运行时 | `internal/runtime` | 组合根：客户端生命周期（`clientManager`）、规则引擎状态与缓存（`ruleEngine`）、摄取内存态（`ingestState`）、定义缓存、保护动作闭环、热重载 | `runtime.Manager` |
| NATS | `internal/natsclient` | 南向订阅 / 北向发布 | 客户端结构体 |
| 平台常量 | `internal/platform` | 告警主题命名空间等平台级常量 | 常量 |
| 日志 | `internal/logging` | slog 热重载 | `logging.Runtime` |
| 前端 | `internal/web/static` | 单页 UI | 嵌入式静态资源 |

> 运行时内部：`Manager` 只保留生命周期与告警摄取编排；NATS 客户端状态（4 把锁）、规则引擎状态（边沿/冷却/规则缓存/设备锁）、摄取去重状态各自成类型并独立持锁，避免单类型堆叠 9 把锁。

## 三、技术栈

| 依赖 | 版本 | 用途 |
|------|------|------|
| Go | 1.23+ | 后端，`log/slog` 日志核心 |
| go-chi/chi/v5 | v5.1.0 | HTTP 路由 + 中间件 |
| gorm.io/gorm | v1.31.x | ORM |
| glebarez/sqlite | v1.11.0 | 纯 Go SQLite（无 CGO） |
| google/uuid | v1.3.0 | 主键/消息 ID |
| nats-io/nats.go | v1.39.x | NATS 客户端 |
| gopkg.in/natefinch/lumberjack.v2 | v2.2.1 | 日志滚动 |
| Bootstrap | 5.3.2 | 栅格/表单/Modal（CDN） |
| Bootstrap Icons | 1.11.3 | 图标（CDN） |
| Chart.js | 4.x | 饼图/柱状图/趋势（CDN） |
| 原生 CSS 变量 + 原生 JS | ES6+ | 无 UI 框架、无构建工具 |

**约束**：不引入额外 UI 框架或构建工具；自定义样式收敛 `app.css`，脚本收敛 `static/js/`。Chart.js 仅用于统计图表，同走 CDN。

## 四、核心设计原则

1. **单体二进制**：前后端同源，`//go:embed` 静态资源，单进程部署。
2. **接口化解耦**：跨层调用走 facade 接口，便于测试桩与未启用场景兜底。
3. **热重载优先**：日志与 NATS 客户端配置变更差量重建而非全量重启，HTTP 保持可用。
4. **JSON Blob 灵活 schema**：可扩展字段用 JSON 存储，改 blob 不改表。
5. **回退兜底**：配置缺失、NATS 未启用提供默认值与静默降级。
6. **并发安全收敛在运行时**：API 层无锁，客户端替换与告警摄取用锁保护。
7. **统一响应封装**：`{code, data}` / `{code, msg}`，HTTP 状态码与业务码一致。

## 五、后端设计规范

### 5.1 目录与命名

```
internal/
  api/          api.go + alarms.go + history.go + engine.go + settings.go
  store/        db.go + models.go + settings.go
  runtime/      manager.go
  natsclient/   message.go + client.go
  logging/      logging.go + runtime.go
  config/       config.go
  buildinfo/    buildinfo.go
  web/          router.go + static/
```

### 5.2 路由注册

资源名**复数**，路径参数 `{id}`，GET 列表 / POST upsert / DELETE 删除。

```go
r.Route("/api", func(r chi.Router) {
    r.Get("/settings", srv.GetSettings)
    r.Post("/settings", srv.SaveSettings)
    r.Get("/system-info", srv.GetSystemInfo)
    r.Post("/restart", srv.Restart)
    r.Get("/alarms", srv.ListAlarms)
    r.Get("/alarms/summary", srv.AlarmSummary)
    r.Get("/alarms/{id}", srv.GetAlarm)
    r.Post("/alarms/{id}/acknowledge", srv.AcknowledgeAlarm)
    r.Post("/alarms/{id}/handle", srv.HandleAlarm)
    r.Post("/alarms/batch/acknowledge", srv.BatchAcknowledgeAlarms)
    r.Post("/alarms/batch/handle", srv.BatchHandleAlarms)
    r.Get("/history", srv.ListHistory)
    r.Get("/history/statistics", srv.HistoryStatistics)
    r.Get("/history/daily-distribution", srv.DailyDistribution)
    r.Get("/rules", srv.ListRules)
    r.Post("/rules", srv.SaveRule)
    r.Delete("/rules/{id}", srv.DeleteRule)
})
```

### 5.3 响应封装

`ok(w, data)` 成功；`fail(w, code, msg)` 失败。状态码：400 参数 / 404 不存在 / 409 冲突 / 503 未就绪 / 500 内部错误。

### 5.4 存储模型

- 告警/规则主键 UUID；更新先 `First` 查存在性再 `Save`，保留原 `CreatedAt`。
- `CreatedAt` 标 `json:"-"`；列表 `Order("occurred_at desc")`。
- 可扩展字段用 JSON blob 避免改表。

### 5.5 运行时协作（facade）

```go
type RuntimeFacade interface {
    ReloadBus(config.App) error
    Restart(config.App) bool
}
```

新增能力扩展 facade 接口；调用前判 `s.Runtime == nil`。

## 六、前端设计规范

### 6.1 文件组织

`index.html` + `css/app.css` + `js/{constants,helpers,api,alarms,statistics,history,engine,protection,settings,main}.js`。
加载顺序：constants -> helpers -> api -> 业务模块 -> main（最后）。

### 6.2 全局 state 单一数据源

`state = { alarms, alarmFilters, alarmSummary, selectedAlarmId, alarmBatch, history, historyFilters, statistics, trendGranularity, topExpanded, rules, editingRuleId, settings, systemInfo }`。

`alarmBatch = { active, mode(acknowledge|handle), selected[] }`：告警列表批量模式，点击右上角"批量确认/批量处理"进入（active=true 时列表顶部出现 全选/取消/确认 操作条、每行出现勾选框，行点击即勾选）；勾选仅对当前页可见条目生效，翻页/筛选后自动清理。`topExpanded = { software, device }`：统计页 Top 排行分组展开状态，默认收起（系统告警无子项，不参与）。

### 6.3 区块切换

区块 ID `section-{key}`，侧栏 `nav-{key}`；`switchSection(key)` 切换显隐 + 高亮 + 按需初始化。

### 6.4 API 客户端

统一 `apiGet/apiPost/apiDelete`，不直接 `fetch`；成功返回 `data`，失败 `throw`；`try/catch` 处理。

## 七、Web 设计规范

### 7.1 设计令牌（:root 变量，禁止硬编码）

`--primary #4f46e5 / --primary-light #eef2ff / --success #059669 / --warning #d97706 / --danger #dc2626 / --muted-color #6b7280 / --border-color #e5e7eb / --bg #f8fafc`。

### 7.2 布局

Sidebar(232px) + Main；`.main-inner` 最大宽 1200px 居中。

### 7.3 核心组件

section-header / landing-toolbar / count-pill / summary-card / data-panel / filter-bar / table / alarm-level 徽标 / status-badge / empty-state / drawer（右侧详情抽屉）。

### 7.4 告警语义配色

级别 0 提示（浅蓝）1 一般（琥珀）2 严重（浅红）3 紧急（深红）；状态 活动中 红 / 已恢复 蓝 / 孤儿 灰 / 已处理 绿；确认与否通过操作按钮禁用区分，不构成独立状态。

### 7.5 响应式

<=880px 侧栏收 64px 图标条、图表堆叠、表格横向滚动。

## 八、模块设计

| 区块 | key | 图标 | 内容 |
|------|-----|------|------|
| 告警看板 | alarms | bi-grid-1x2 | 级别卡（活动中/未确认/未处理）+ 今日卡（总数/24h 趋势/环比）+ 筛选区 + 分页列表（标签列，右上角批量确认/批量处理：进入后列表顶部 全选/取消/确认 + 行勾选框）+ 详情抽屉 |
| 告警统计 | statistics | bi-graph-up | KPI 卡（总数/活跃/MTTA/MTTR）+ 级别占比饼图（右侧图例：名称+百分比）+ Top 排行（软件/设备分组点击展开收起，列表区滚动）+ 多系列趋势（全部/系统/软件/设备）+ 告警分布图（24h）（散点：X=时间 Y=级别数量，右上角可选日期）+ 24h×7d 时段热力图 |
| 历史记录 | history | bi-clock-history | 时间/类型/来源/级别/处理状态筛选 + 分页列表（行点击查看详情）+ CSV 导出 |
| 告警引擎 | engine | bi-gear | 定义缓存状态条（N 设备 · M 事件 · 同步时间 + 手动同步按钮）+ 保护动作规则 CRUD（三区表单：基本信息【启用/名称/描述/冷却】+ 监测源配置【级别监测：级别多选（不区分类型）；事件监测：监测类型（设备可用，软件/系统预留）+设备+事件；anyone/all 聚合】+ 控制源配置【设备：设备名+服务名+值/状态】；多源动态增删） |
| 保护记录 | protection | bi-shield-check | 动作下发记录流水：时间/规则/动作（设备名·服务名=值）/触发告警/结果/说明，状态筛选（待受理/已受理/推定成功/失败/终态未知）；设备名、服务名、告警名由后端补全（定义缓存 + 告警表），内部 ID 仅在悬停提示中保留 |
| 软件配置 | settings | bi-sliders | 软件信息 + NATS（订阅主题/物模型前缀/REQ 超时）+ 数据保留 + 日志配置 + 软件重启（三列瀑布卡片） |

## 九、数据契约

- 全链路 camelCase；DOM ID 前缀 `al-`(告警看板)/`sa-`(告警统计)/`hi-`(历史记录)/`en-`(引擎)/`pr-`(保护记录)/`setting-`(设置)。
- 列表接口（/alarms、/history）统一分页：`page`(1 起) + `pageSize`(默认 15，上限 100)，响应 `{list,total,page,pageSize}`；按时间倒序，前端分页条复用 `paginationHtml`。
- 告警级别：0 提示 1 一般 2 严重 3 紧急（与标准 level 一致）。
- 告警展示状态（三态派生）：活动中（触发持续中，含未确认/已确认）→ 已恢复（自动恢复沿）→ 已处理（人工结案，进历史）；孤儿 = 触发源因配置变更/设备离线等失联，叠加于活动中。
- 确认（Acknowledged）是正交的操作维度（未确认/已确认），不是独立状态；顶部统计的"未确认"口径即该维度。
- 操作约束：确认 = 操作员认领进入处理阶段；处理 = 仅"已恢复"告警可结案，活动中/孤儿不可关闭；一次性事件（kind=event）确认后可视为即时恢复直接结案。
- 批量操作：`POST /alarms/batch/{acknowledge|handle}`，请求 `{"ids":[...]}`（服务端去重，单次上限 200），响应 `{ok, skipped, results:[{id, ok, reason}]}`；逐条执行且规则与单条一致，不可操作条目跳过并带原因，单条失败不影响其余。
- 告警标签（自动派生）：device = 事件规则 key + 监测点；software/system = 事件码 + 组件；聚合消息（count>1）追加 ×N。
- 今日统计口径：今日 0 点至今全部告警；24h 逐小时趋势；环比 = 今日截至当前 vs 昨日同期。
- 告警分布图（24h）：`GET /history/daily-distribution?date=YYYY-MM-DD`（缺省今日，非法 400），响应 `{date, series:[{level, name, points:[{label:"08:00", value}]×24}]}`；前端散点 x=小时 0-23、y=数量、颜色=级别（与级别配色一致），0 计数不画点，日期切换仅重查本图；热力图行日期可点击，联动切换本图日期并高亮当前选中行（后端 heatmap.dates 提供完整 YYYY-MM-DD）。
- 保护动作规则（多监测源 → 多控制源）：
  - 监测源（`monitors[]`，按 `matchMode` 聚合）：`kind=level`（级别集合，`kind='trigger' AND orphan=0` 的 active 告警计数 > 0 即成立，不区分类型——一次性事件与孤儿无恢复沿，计入会让规则永久成立、边沿不再上升）；`kind=event`（监测类型 `type`：v1 仅 device 可用，software/system 预留；按"该设备/事件的活动告警是否存在"判定，恢复后边沿下降、下次触发重新武装；无关告警消息不影响边沿）；
  - 聚合判断：`anyone`（任一源成立，默认）/ `all`（全部源成立；事件源按 DB 活性判定，可与多个事件源/级别源组合）；
  - 控制源（`controls[]`，规则成立时全部执行）：`kind=device`（deviceId+serviceKey+value，经 .cmd 两段式下发，各自独立 ActionRecord 闭环；下发前按定义缓存校验服务存在且已绑定，多控制源按目标设备串行执行）；sms/email 预留；`value` 的 0 是合法工程值（归零/停机），序列化不省略；
  - `cooldownMs` 规则级：条件重复成立（上升沿）的最低下发间隔，0=不限、上限 24h（防止 duration 溢出导致冷却失效）；评估时机 trigger/event 落库后、recover 后、墓碑清除后（墓碑重算只刷新边沿、不下发），仅上升沿执行、下降沿不反向动作；
  - 重启播种：启动时按存量活动告警预置一次边沿状态（只写状态不下发），首个状态变化不会把重启前已成立的条件误判为上升沿而重复动作；
  - 兼容：旧单源字段保留为兼容列。旧版本规则表没有 `event_key`/`service_key`，无法可靠合成新模型，启动时**停用并告警**（不伪造监测源/控制源），需人工按新模型重建；历史错误迁移产物（event 源缺 eventKey / 控制源缺 serviceKey）同样自动停用。
- 动作记录：`requestId`（.cmd 请求 PP-Message-ID，与 cmd.result 闭环；引擎先预生成并落记录再发布，接住 REQ/REP 窗口内的失败终态）、状态机 pending→accepted→(failure|success 推定)；推定成功后迟到的 failure 仍可覆盖（宁可错报失败，不可错报成功）、`GET /api/actions?status=&page=&pageSize=`（分页，默认 50 条/页）；列表响应补全 `deviceName/serviceName/alarmName` 供前端展示可读信息；滞留中间态由启动清扫 + 每 60s 周期清扫置 unknown。
- 定义缓存：`GET /api/definitions`（设备+事件+服务定义快照，规则编辑器下拉数据源）、`POST /api/definitions/refresh`（手动同步，202 异步；未启用 NATS 时 412）；局部设备拉取失败时如实写入 `lastError`（不再显示"已同步"却给出空事件/服务）。
- 设置保存：响应 `{settings, runtimeReload: ok|pending, message}`——消息总线重连失败时配置已落库并进入自动重试，明确告知"已保存待生效"。
- 告警展示：`valueInvalid=true` 表示触发值无法解析，前端显示"—"而非误导性的 0；历史查询 `start/end` 非法时返回 400。
- 健康检查：`GET /api/health` → `{db, runtime:{natsEnabled,natsConnected,definitionsSyncedAt,definitionsLastError,lastIngestAt}}`，用于部署探针与发现"进程在但没在收消息"的静默故障。

## 十、消息契约（NATS，告警沿驱动设计）

依据 [thingsmodel-message-contract-design.md](thingsmodel-message-contract-design.md)（v2）与 [powerpulse-alarm-standard.md](powerpulse-alarm-standard.md)：

- **常驻订阅**：`powerpulse.alarm.>`（唯一订阅，不订阅 `.data`）。消费侧以 `alarm_id` 幂等去重，trigger 按定位键合并（device: module+device_id+event_id/event_key；software/system: module+code+component），recover 按同定位键恢复；`count` 语义：设备告警=重触发次数（断流后重新武装），软件事件=节流聚合计数。
- **规则评估（多源）**：告警状态变化（trigger/event 落库后、recover 后、墓碑清除后）触发全量规则评估——event 源按"源告警是否仍活动"判定（`state='active' AND type/device/event 匹配`，恢复即下降沿、无关消息不影响边沿）、level 源按 `state='active' AND kind='trigger' AND orphan=0 AND severity IN (...)` 计数 > 0 成立，按 `anyone`/`all` 聚合；仅下降→上升沿且冷却通过时并发下发全部 device 控制源（按设备串行）；启动时播种存量边沿（只写状态不下发），避免重启首条消息重复动作。
- **定义预取（REQ/REP）**：启动/热重载后 `query.devices` → 每启用设备 `query.events` + `query.methods` 建缓存；`thingsmodel.model.changed` 到达后增量维护（updated→重查+孤儿对账；deleted/disabled→墓碑：活动告警自动恢复+移除缓存）；同步失败 30s 自动重试，支持手动触发（POST /api/definitions/refresh）。
- **保护动作（REQ/REP 两段式）**：设备 trigger 按 `(device_id, event_key)` 匹配规则（含断流后重触发的重新武装，受规则冷却时间约束）→ `powerpulse.thingsmodel.cmd` 下发 `ControlRequest` → REP 受理（同步 failure 当场可知，不重试：契约重复请求不去重，at-most-once）→ 终态：失败经 `thingsmodel.cmd.result`（alarm 主题，context.request_id）回填动作记录并上告警看板；成功静默，受理后 35s（cmdAck 30s+余量）无失败终态即**推定成功**。
- **内部信号不上板**：`thingsmodel.model.changed` 仅作缓存维护信号；`thingsmodel.cmd.result` 上板（保护动作失败是重要运维信息）。
- **孤儿判定**（不订阅 .data 的取舍）：设备删除/停用（墓碑）→ 活动告警自动恢复结案；事件定义移除（updated 对账差集）→ 置孤儿，定义恢复解除；数据断流不可见（契约 B 方案不发消息），接受盲区，recover 到达或重触发时自愈。
- **已知取舍**：引擎停机期间错过的沿消息不可补（Core NATS 无重放，ADR：[docs/adr/0001-core-nats-at-most-once-ingestion.md](adr/0001-core-nats-at-most-once-ingestion.md)）；动作成功为推定而非 `.data` 值确认。缓解：启动清扫器把超窗 pending/accepted 动作记为 unknown（终态未知）；二者为二期候选（启动时一次性 `.data` 对账）。
- **数据保留**：已结案告警与终态动作记录（failure/success/unknown）按保留天数每日清理（配置 `retention.days`，0=永久保留；pending/accepted 中间态由启动清扫器处理，不在此清理）。
- **并发正确性**：动作终态（失败/推定成功）与告警人工操作（确认/结案）均为条件列更新 + RowsAffected 校验，消除行级 lost-update；规则保存带版本号乐观锁（409 冲突）。

## 十一、实现 Checklist

- [ ] store 模型 + AutoMigrate
- [ ] api 模块 List/Save/Delete + ok/fail + 状态码
- [ ] 运行时能力经 facade 暴露
- [ ] 日志经模块 logger
- [ ] 前端 js 模块 + section/nav + state + apiGet/apiPost/apiDelete
- [ ] 视觉遵循令牌与组件清单，图表走 Chart.js
