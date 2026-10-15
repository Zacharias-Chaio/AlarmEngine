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
| API | `internal/api` | REST 处理器 | `api.Server` |
| 存储 | `internal/store` | GORM 模型与迁移、平台设置 | `store.Open(...)` |
| 运行时 | `internal/runtime` | NATS 客户端管理、标准告警摄取（幂等/定位键恢复/聚合）、热重载 | `runtime.Manager` |
| NATS | `internal/natsclient` | 南向订阅 / 北向发布 | 客户端结构体 |
| 日志 | `internal/logging` | slog 热重载 | `logging.Runtime` |
| 前端 | `internal/web/static` | 单页 UI | 嵌入式静态资源 |

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
    r.Get("/history", srv.ListHistory)
    r.Get("/history/statistics", srv.HistoryStatistics)
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

`index.html` + `css/app.css` + `js/{constants,helpers,api,alarms,statistics,history,engine,settings,main}.js`。
加载顺序：constants -> helpers -> api -> 业务模块 -> main（最后）。

### 6.2 全局 state 单一数据源

`state = { alarms, alarmFilters, alarmSummary, selectedAlarmId, history, historyFilters, statistics, trendGranularity, rules, editingRuleId, settings, systemInfo }`。

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
| 告警看板 | alarms | bi-grid-1x2 | 级别卡（活动中/未确认/未处理）+ 今日卡（总数/24h 趋势/环比）+ 筛选区 + 分页列表（标签列）+ 详情抽屉 |
| 告警统计 | statistics | bi-graph-up | KPI 卡（总数/活跃/MTTA/MTTR）+ 级别占比饼图（右侧图例：名称+百分比）+ Top 排行 + 多系列趋势（全部/系统/软件/设备）+ 24h×7d 时段热力图 |
| 历史记录 | history | bi-clock-history | 时间/类型/来源/级别/处理状态筛选 + 分页列表（行点击查看详情）+ CSV 导出 |
| 告警引擎 | engine | bi-gear | 保护动作规则 CRUD（条件 + 动作） |
| 软件配置 | settings | bi-sliders | 软件信息 + NATS 南向北向 + 日志配置 + 软件重启（三列瀑布卡片） |

## 九、数据契约

- 全链路 camelCase；DOM ID 前缀 `al-`(告警看板)/`sa-`(告警统计)/`hi-`(历史记录)/`en-`(引擎)/`setting-`(设置)。
- 列表接口（/alarms、/history）统一分页：`page`(1 起) + `pageSize`(默认 15，上限 100)，响应 `{list,total,page,pageSize}`；按时间倒序，前端分页条复用 `paginationHtml`。
- 告警级别：0 提示 1 一般 2 严重 3 紧急（与标准 level 一致）。
- 告警展示状态（三态派生）：活动中（触发持续中，含未确认/已确认）→ 已恢复（自动恢复沿）→ 已处理（人工结案，进历史）；孤儿 = 触发源因配置变更/设备离线等失联，叠加于活动中。
- 确认（Acknowledged）是正交的操作维度（未确认/已确认），不是独立状态；顶部统计的"未确认"口径即该维度。
- 操作约束：确认 = 操作员认领进入处理阶段；处理 = 仅"已恢复"告警可结案，活动中/孤儿不可关闭；一次性事件（kind=event）确认后可视为即时恢复直接结案。
- 告警标签（自动派生）：device = 事件规则 key + 监测点；software/system = 事件码 + 组件；聚合消息（count>1）追加 ×N。
- 今日统计口径：今日 0 点至今全部告警；24h 逐小时趋势；环比 = 今日截至当前 vs 昨日同期。

## 十、消息契约（NATS）

南向输入遵循 [powerpulse-alarm-standard.md](powerpulse-alarm-standard.md)：订阅 `powerpulse.alarm.>`，信封 `PP-Message-*`（Type=alarm）；消费侧以 `alarm_id` 幂等去重，trigger 按定位键合并（device: module+device_id+event_id/event_key；software/system: module+code+component），recover 按同定位键恢复；`count` 聚合计数累加。北向输出 `powerpulse.alarm.{softwareId}.action`。

## 十一、实现 Checklist

- [ ] store 模型 + AutoMigrate
- [ ] api 模块 List/Save/Delete + ok/fail + 状态码
- [ ] 运行时能力经 facade 暴露
- [ ] 日志经模块 logger
- [ ] 前端 js 模块 + section/nav + state + apiGet/apiPost/apiDelete
- [ ] 视觉遵循令牌与组件清单，图表走 Chart.js
