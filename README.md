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
- **告警引擎**：保护动作规则 CRUD（触发条件 + 保护动作）。
- **软件配置**：软件信息、NATS 客户端（订阅标准告警输入 + 发布保护动作）、日志配置、软件重启，三列瀑布卡片布局，保存后日志与消息总线立即热重载。

## 架构

单体二进制 + 嵌入式 SPA：静态资源经 `//go:embed` 编入二进制，无独立前端服务。技术栈 Go + chi v5 + GORM/SQLite + NATS + Bootstrap/Chart.js（CDN）。

详见 [docs/alarm-engine-design-guide.md](docs/alarm-engine-design-guide.md)。

## NATS 消息契约

- 南向输入主题：`powerpulse.alarm.>`（平台告警标准命名空间，收全部模块；设备/软件/系统告警均由各模块按 [docs/powerpulse-alarm-standard.md](docs/powerpulse-alarm-standard.md) 发布）
- 北向输出主题：`powerpulse.alarm.{softwareId}.action`（保护动作下发）

信封复用网关侧 `PP-Message-*` Header 契约；消费侧按 `alarm_id` 幂等去重、按定位键合并 trigger/recover。

> 注：系统告警不由本模块检测产生，由其他模块（或平台组件）经上述标准接口发布。
