# AlarmEngine

告警引擎微服务：独立 Web，用于设备告警展示、历史统计与告警保护动作配置。

## 启动

```powershell
go run .
```

默认服务地址为 `http://localhost:8091`，SQLite 数据库默认写入 `data/alarmengine.db`。可用 `-db` 指定其他数据库文件，用 `-addr` 指定监听地址：

```powershell
go run . -addr :8091 -db .\data\alarmengine.db
```

首次启动数据库为空，告警数据由各模块按 [平台告警标准](docs/powerpulse-alarm-standard.md) 经 NATS 发布接入。登录凭据：`admin / 666`。

## 功能模块

- **告警看板**：活动告警看板（级别卡 × 活动中/未确认/未处理 + 今日总数 + 24h 趋势 + 较昨日同期环比 + 筛选 + 分页列表 + 详情抽屉），支持确认/处理结案（活动中与孤儿告警不可关闭）。
- **告警统计**：KPI 卡（总告警数/活跃告警/MTTA/MTTR）、告警级别占比饼图（右侧图例：名称+百分比）、Top 告警排行、多系列趋势折线（全部/系统/软件/设备，日/周/月切换）、24h×7 天时段热力图。
- **历史记录**：按时间/类型/来源/严重程度/处理状态分页查询，点击记录查看告警详情，支持按当前筛选条件导出 CSV。
- **告警引擎**：多监测源配置（级别监测：级别集合，活动告警数 > 0 成立；事件监测：监测类型+设备+事件定点）+ anyone/all 聚合判断 → 多控制源（设备服务 .cmd 下发，规则级冷却时间）。
- **保护记录**：保护动作下发流水（时间 / 规则 / 动作【设备名·服务名=值】/ 触发告警 / 结果 / 说明），状态筛选（待受理/已受理/推定成功/失败/终态未知）；设备名、服务名、告警名由后端补全，内部 ID 仅在悬停提示中保留。
- **软件配置**：软件信息、NATS 客户端（订阅标准告警输入 + 发布保护动作）、日志配置、软件重启，三列瀑布卡片布局，保存后日志与消息总线立即热重载。

## 架构

单体二进制 + 嵌入式 SPA：静态资源经 `//go:embed` 编入二进制，无独立前端服务。技术栈 Go + chi v5 + GORM/SQLite + NATS + Bootstrap/Chart.js（CDN）。

详见 [docs/alarm-engine-design-guide.md](docs/alarm-engine-design-guide.md)。

## NATS 消息契约

- **订阅输入**：`powerpulse.alarm.>`（平台告警标准命名空间，收全部模块；设备/软件/系统告警均由各模块按 [docs/powerpulse-alarm-standard.md](docs/powerpulse-alarm-standard.md) 发布）
- **定义查询（REQ/REP）**：`powerpulse.thingsmodel.query.devices / .query.events / .query.methods`（启动预取，`thingsmodel.model.changed` 增量维护）
- **保护动作（REQ/REP 两段式）**：`powerpulse.thingsmodel.cmd` 下发服务控制；失败终态经 `thingsmodel.cmd.result`（alarm 主题，`request_id` 闭环）回填，成功静默推定

信封复用网关侧 `PP-Message-*` Header 契约；消费侧按 `alarm_id` 幂等去重、按定位键合并 trigger/recover。详见 [docs/thingsmodel-message-contract-design.md](docs/thingsmodel-message-contract-design.md) 与 [docs/alarm-engine-design-guide.md](docs/alarm-engine-design-guide.md) 第十节。

测试工具：[`tools/alarm-sender`](tools/alarm-sender/README.md) 按本标准在总线上模拟各类工况（稳态/风暴/聚合/重复/非法消息等）发布告警，支持长时运行、固定种子复现与发送侧对账。

> 注：系统告警不由本模块检测产生，由其他模块（或平台组件）经上述标准接口发布。
