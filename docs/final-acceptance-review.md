# AlarmEngine 最终验收走查报告

> 评审：软件架构师 + 验收测试工程师（双视角）· 主会话交叉核对
> 范围：全部 Go 代码（16 文件 / ~4.9k 行）、前端静态资源（12 文件）、tools/alarm-sender、docs
> 结论：**框架总体合理（B+），但存在 5 项 P0 阻断，当前状态不建议直接上生产。**

## 一、结论速览

| 维度 | 评级 | 判断 |
|------|------|------|
| 分层与依赖方向 | B+ | 无环、单向清晰；仅 config→logging 一条边值得反演 |
| 接口抽象 | B | RuntimeFacade / definitions 取舍合理；Manager 职责过载（6 类职责、9 把锁） |
| 业务逻辑正确性 | **C** | 发现并复现"边沿被无关消息重置 → 重复下发"（P0-3），其余主链路设计成熟 |
| 错误处理/数据一致性 | B− | 写路径（条件更新 + RowsAffected）扎实；读路径与配置应用有"半成功"语义 |
| 前端可维护性 | C+ | 无模块化、46 处内联 onclick（含 XSS 面）、关键样式依赖公网 CDN |
| 工程化/可验证性 | **D** | `*_test.go` 被 .gitignore 忽略 → 38 个测试 0 个入库；总覆盖率 23.8% |

## 二、验证方式

- 双专家独立通读 + 主会话交叉核对；架构师做了 go vet / go test / gofmt / 静态引用 / git 跟踪 5 项机器验证
- 主会话**临时用例复现 P0-3**（复现后已删除临时文件，结论：动作记录 2 条，期望 1 条）
- 主会话复核 P0-1（XSS 模式）、P1-8（饼图配色）、A1（.gitignore）为代码事实

## 三、P0 阻断项（交付前必修）

### P0-1 存储型 XSS：外部总线可写 alarm_id，直达能下发设备控制的控制台
- 位置：`internal/web/static/js/alarms.js:155/156/158/172/173`、`js/history.js:79`
- 机制：`onclick="openAlarmDetail('${escapeHtml(a.id)}')"`。`escapeHtml` 把 `'` 转成 `&#39;`，HTML 属性解析阶段会还原为 `'`，可闭合 JS 字符串。`alarm_id` 来自 NATS 消息体且只校验非空（`runtime/manager.go`），任何能发 `powerpulse.alarm.>` 的模块都可注入。
- 影响：执行环境是能调用 `POST /api/rules`、`POST /api/alarms/{id}/handle` 的运维控制台。
- 修复：① 前端全部改 `data-* + 事件委托`（与规则 ID 的 H6 修法一致）；② 后端摄取时校验 `alarm_id` 字符集（`^[A-Za-z0-9_-]{1,128}$`），非法丢弃并 Warn；③ 顺带排查 `statistics.js:304/339`。

### P0-2 /api 零鉴权 + 凭据硬编码并明文回显
- 位置：`internal/web/router.go:28-53`、`js/constants.js:4-5`、`index.html:31`、`README.md:17`
- 现状：登录只是前端 `sessionStorage` 标记 + JS 字符串比对；`LOGIN_PASS='666'` 下发到每个浏览器且登录失败页主动提示凭据；所有变更接口（含 `.cmd` 下发、restart）无服务端鉴权，`curl` 直接可用。
- 影响：这是一个能下达设备物理控制指令的服务。
- 修复（二选一）：(a) 加 Token/Basic Auth 中间件 + 删除凭据回显；(b) 若本期不做，README 顶部固定部署硬约束并由部署评审签字。不得"默认不做也不声明"。

### P0-3 边沿状态被无关消息重置 → 条件未解除却重复下发（已复现）
- 位置：`internal/runtime/engine.go` `evaluateRulesInternal`（每条消息无条件覆写 `ruleHoldState[rule.ID]`）
- 机制：event 源只匹配"当前这一条消息"。任何不匹配的告警都会把 `holds` 写成 false；随后同事件重触发（前一条 trigger 仍 active、条件从未解除）又被判为上升沿 → 再次下发。`cooldownMs=0`（合法配置）时等于指令风暴；>0 时只是限速，语义仍错。
- 复现：我的临时用例——首次命中下发 1 条 → 插入无关设备告警 → 同条件重触发 → **动作记录 2 条（期望 1 条）**，测试失败。
- 修复：边沿状态按**监测源粒度**保存（`map[ruleID]map[monitorKey]bool`），或 event 源改为查 DB 活性（`state='active' AND device/event` 存在即成立）；补"触发 → 无关告警 → 重触发"回归用例。

### P0-4 单行 JSON 损坏 → 保护动作全引擎静默停摆
- 位置：`internal/store/models.go` `scanJSONList` 直接返回错误；`runtime/engine.go` `Find` 失败即 `return`；`api/engine.go` 列表 500。
- 影响：任一条规则的 `monitors/controls` 非法（历史残留/手改库/磁盘问题）→ 所有规则均不再评估（保护动作全部停止），仅一条 Warn；引擎页"暂无规则"，运维无法区分"没配"与"坏了"。
- 修复：逐行容错（坏行停用 + 软件告警上板），或至少把"规则加载失败"提升为可观测事件。

### P0-5 测试不入库（工程化硬伤）
- 位置：`.gitignore:37` `*_test.go`
- 影响：`git ls-files` 显示 39 个受控文件中 **0 个测试**；38 个用例（含前轮 C1-C4/H1/H2 回归）只存在于工作区，`git clean`/新克隆/CI 全部拿不到 → 前一轮"已验证"结论不可复现。
- 修复：删除该行并提交测试；CI 增加 `go vet` + `go test -race` + `gofmt -l` + 覆盖率下限（建议从 45% 起，逐期抬到 65%）。

## 四、P1 高（对外可见 / 数据可信度）

| # | 位置 | 问题 |
|---|------|------|
| P1-1 | `api/history.go:360,370` | 日/周趋势 `Truncate(24h)` 按 UTC 零点截断，东八区"日"桶实为 08:00→次日 08:00，与"今日统计"/热力图/分布图（均本地零点）口径差 8 小时；周桶也未做日历周对齐 |
| P1-2 | `js/statistics.js:81` + `api/history.go:256-260` | 级别占比饼图配色按**数组下标**取固定色（`['#3b82f6',…]`），而后端只返回存在的级别 → 仅存在 P3 时"紧急"显示为蓝色；同文件 `DIST_LEVEL_COLORS` 按 level 键取色才是对的 |
| P1-3 | `runtime/manager.go:431-445` | 合并触发不复位 `orphan`。设计指南明确"recover 或**重触发**时自愈"，实际重触发后仍显示孤儿且不可结案 → 与"永不恢复"形成死结 |
| P1-4 | `api/alarms.go:290-309` | `software/system + kind=trigger` 的告警无恢复沿、不可结案、无墓碑路径 → 永久卡死，还会污染 level 计数 |
| P1-5 | `api/settings.go:34-49` + `runtime/manager.go` | 保存设置"半提交"：DB 已落库后日志/总线失败返回 500/503，用户以为没保存；且总线失败不触发既有退避重试，`m.settings` 与 `SettingsStore` 分叉 |
| P1-6 | `js/alarms.js:11-23`、`statistics.js:8-14`、`engine.js:6`、`protection.js:6` | 接口失败一律吞异常置空 → 显示"暂无告警/暂无规则"，与"真的没有"不可区分（对告警看板是事故放大路径） |
| P1-7 | `index.html:7-10` + `router.go:55-57` | 关键样式/图表走公网 CDN（离线现场退化为无样式）；embed.FS 无 ModTime，无 ETag/Last-Modified/版本串 → 升级后可能长期跑旧 JS |
| P1-8 | `api/engine.go` ListActions + `manager.go:266` | 保护记录无分页（硬截 50 条、无 total），`retention` 只清已结案告警、`action_records` 永不清理 → 复盘证据留存不足 + 表无限增长 |
| P1-9 | `helpers.js:19-34` | 看板/统计无自动刷新（无轮询/SSE），"实时监控"可能展示几十分钟前状态 |

## 五、P2 中（架构 / 一致性 / 可维护性）

**运行时**
- `Manager` 承担 6 类职责 + 9 把锁（595 行）+ `engine.go` 665 行；锁序不变式未文档化（当前无死锁）。建议同包内机械拆分 `clientManager / ruleEngine / ingest`，包注释写死锁序。
- `Stop` 不取消 ctx，仅 `wg.Wait()`；传入非取消 ctx 时永久阻塞。建议 `NewManager` 内 `WithCancel`。
- 未受跟踪的 goroutine：`RefreshDefinitions` 裸 `go`、推定成功 `time.AfterFunc`；退出期可能访问已关闭 DB。
- `definitions` 双锁 + 三标志，不变式靠时序侥幸（`markError`/`setAll` 绕过 stateMu 改 Syncing/LastError）。
- reload 与在跑 bootstrap 交叉：旧 client 被 Drain → 该轮拉取全失败 → 写入"有设备无事件"快照且 `LastError` 为空 → 规则编辑器下拉静默变空 30s。
- 热路径：每条消息全表查启用规则 + 每 level 源一次 COUNT；风暴下 SQLite 单写者被放大。建议规则内存缓存（保存/删除失效）+ level 计数一次 `GROUP BY severity`。
- `durationRefreshLoop` 每 5s 全量 UPDATE active 行（值不变也写）→ WAL 持续写放大；建议查询时派生。
- 合并触发路径我新增的 `resolveTriggerAlarm` 会二次定位查询（C8）：可让 `upsertAlarm` 返回落库 ID 复用。
- GORM 日志写 stdout + Colorful，绕开 slog/lumberjack → 文件日志缺 SQL 慢查询。
- 500 响应直出 `err.Error()`（12+ 处）；`err == gorm.ErrRecordNotFound` 未用 `errors.Is`；202 手写 envelope 绕过 ok/fail；`RefreshDefinitions` 用 503 表达"未启用"。
- 领域逻辑（确认/结案状态机、规则校验约 200 行）住在 HTTP 层，无法复用与独立单测。
- `software.id/name` 死配置面：只被校验、无消费方、前端只读 → 校验失败用户无法修复。
- 主题命名空间 4 份、级别名 3 份、类型名 2 份硬编码（`powerpulse.alarm.` 等）。
- `SettingsStore.Save` 先写库再写内存副本，两段无锁 → 并发保存可让内存与 DB 分叉。
- 无 `/healthz`/`/readyz`：NATS 断连、定义同步失败、SQLite 锁等待均不可机器读。
- 无构建/发布脚本；`buildinfo.Version` 恒为 `v0.0.0`（前端侧栏直接展示）。
- 测试用字面量手搓 Manager/Server（构造函数副作用过宽）；`web/config/logging` 三包零测试。
- `tools/alarm-sender` 复制信封组装与模块名解析，与运行时契约实现两份。

**API/前端边界**
- 看板摘要口径打架：`active` 排除孤儿，`unacked` 包含孤儿（可出"活动中 0 / 未确认 2"）。
- 热力图 `counts[row.Type][i]++` 对未登记 type 是 nil slice 越界 panic（recoverer 兜成 500）。
- 看板列表/摘要/批量操作 0 覆盖（批量"部分失败"承诺无验证）。
- `recoverAlarm` 不检查 RowsAffected；`markSeen` 在字段校验前占位（非法消息吞 alarm_id，修正后重投被幂等丢弃）；孤儿对账只在 `model.changed updated`，启动全量同步与"同步定义"按钮不对账。
- 下发前不校验 definitions 的 `methods.bound/存在性`（违反契约 §6）；多控制源无按设备串行化（冲突指令顺序不定）。
- 保护记录无 total/分页；CSV 公式注入（`history.js:166` 未转义 `= + - @` 开头）；`window.event` 依赖（Firefox/Safari 无）。
- `config.Validate`、`store.Open`、`web.NewRouter` 均 0 覆盖（路由表 `/alarms/summary` vs `/alarms/{id}` 优先级无快照测试）。

## 六、P3 低（死代码 / 重复 / 优化）

**死代码（确认 0 引用）**
- `api/history.go` `resolvedAt()`；`store/models.go` `AlarmLevelInfo/Minor/Major/Critical`；`natsclient` `EventCodeFrequentFail`（有常量无处理）；`logging.New()`；前端 `state.editingRuleId`（只写不读）；`Alarm.Unit`（无写入方但被读）；`setMonitorType` + 软件/系统禁用项 + 后端 `Type != device` 拒绝分支（三处预留互相锁死、不可达）。
- `app.css` 死类：`model-card-top/-title/-sub/-desc/-actions`、`min-w-0`（rule-card 重命名残留）。

**重复代码**
- 级别主色 5 份（constants.js / statistics.js×2 / app.css / history.go）——P1-2 即其直接产物。
- 过滤下拉初始化 alarms.js 与 history.js 近乎逐字重复；Chart 实例管理两套（todayChart vs charts 注册表）；规则校验前后端各一份且已漂移（前端多 bound/范围校验，后端多 ID 字符集）；`fmtCooldown` 与后端上限魔数各写一份。

**可优化**
- 24h 趋势 O(n×24)、热力图 O(7n) → 一次遍历直接落桶；分桶逻辑重复 4 次可抽公共函数。
- 补复合索引 `(handled, occurred_at)`、`(handled, cleared_at)`。
- `ruleHoldState`/`lastDispatch` 只增不减，规则删除时清理。
- 硬编码颜色改 CSS 变量；`toast()` 实为 `alert()`（阻塞、双前缀）；`start/end` 非法参数静默忽略（分布图同类参数返回 400，口径不一致）。
- `fs.Sub` 错误被丢弃（embed 路径错写会静默空站点）；进入 HTTP goroutine 的 `fatal()` 用 `os.Exit` 跳过全部 defer。

## 七、文档承诺 vs 实现

| 承诺 | 实际 | 结论 |
|------|------|------|
| "孤儿……recover 或**重触发**时自愈" | 合并不复位 orphan | **不符**（P1-3） |
| "重启播种边沿，不重复动作" | 已实现且有对照测试 | ✅ |
| "推定成功后迟到失败可覆盖" | 已实现且有测试 | ✅ |
| "墓碑重算只刷边沿不下发" | 已实现（evaluateRulesQuiet） | ✅ |
| "冷却 0=不限、上限 24h" | 前后端双校验 | ✅ |
| "旧规则停用并告警、不伪造源" | 已实现（含修复与幂等测试） | ✅ |
| "value=0 不省略" | 已实现且有回读测试 | ✅ |
| README 登录凭据 admin/666 | 与服务端无关 | **声明即风险**（P0-2） |

## 八、测试覆盖现状（实测）

| 包 | 覆盖率 | 说明 |
|----|--------|------|
| runtime | 44.8% | 核心沿/冷却/墓碑/闭环有守护 |
| store | 40.0% | 迁移与 julianday 有测；`Open` 0% |
| api | 29.1% | 仅 `apply*` 内部函数，HTTP handler 几乎全 0 |
| natsclient | 13.8% | 仅信封；`client.go` 整文件 0%（`.cmd` 唯一出口无测） |
| config / logging / web | 0% | — |
| **总计** | **23.8%** | 不足支撑"生产就绪" |

## 九、建议修复顺序

1. **P0**：P0-3（重复下发）→ P0-1（XSS）→ P0-5（测试入库）→ P0-4（JSON 容错）→ P0-2（鉴权/声明）。
2. **P1**：P1-1（时区）→ P1-2（饼图配色）→ P1-3（孤儿自愈）→ P1-5（配置半提交）→ P1-6（静默失败）→ P1-8（保护记录分页/保留）→ P1-4 → P1-7 → P1-9。
3. **P2** 按子系统批量：运行时锁/生命周期 → 热路径查询与写放大 → API 一致性与领域外提 → 前端数据层与调色板收敛。
4. **P3** 随手清（死代码一次提交可完成）。

## 十、验收建议

- **A 阶段（阻断）**：修完 P0 五项 + P1-1/2/3/5/6 后复评。
- **B 阶段（回归网）**：history 全量（含时区）、natsclient 契约、API handler 与批量操作、生命周期与清扫窗口、config/store.Open、路由表快照、`go test -race`。
- **C 阶段（人工）**：alarm-sender 六种工况（storm/aggregate/duplicate/orphan/mismatch/invalid）；真实 `.cmd` 失败与推定成功双路径；重启一致性（kill → 重启 → 不重复下发/清扫 unknown）；Chrome+Firefox 双浏览器；≥4h 浸泡。
- **D 阶段（清理）**：死代码、调色板收敛、过滤下拉/Chart/规则校验单一来源、toast 与 CSS 变量。

---

## 十一、本轮修复状态（P0-2 / P0-5 经确认暂不处理）

### 已修复

| 项 | 修复内容 |
|----|----------|
| **P0-3 重复下发** | 事件监测源改为按"源告警是否仍活动"判定（DB 活性），无关消息不再重置边沿；恢复即下降沿、下次触发重新武装。`all` 模式因此允许多事件源（原拒绝逻辑删除）。回归用例 `TestUnrelatedMessageDoesNotResetEdge`（复现：动作记录从 2 条降为 1 条） |
| **P0-1 XSS** | 前端所有内联 onclick 拼数据改为 `data-*` + `this.dataset.*`（alarms/history/statistics/engine）；后端摄取增加 `alarm_id` 字符集白名单（`[A-Za-z0-9_.:-]{1,128}`），非法消息丢弃并告警 |
| **P0-4 单行 JSON 打挂全引擎** | `scanJSONList` 改为容错：单行损坏降级为空列表 + `slog.Warn`，其余规则继续评估 |
| **P1-1 时区错位** | 趋势日/周桶改用本地零点（`localDayStart`），周桶按周一对齐；测试 `TestTrendBucketsUseLocalMidnight` |
| **P1-2 饼图配色错配** | 饼图配色按级别名映射（`ALARM_LEVELS`），不再按数组下标 |
| **P1-3 孤儿不自愈** | 合并触发时复位 `orphan=false` 并记日志；测试 `TestMergeResetsOrphan` |
| **P1-4 软件/系统 trigger 卡死** | 非设备类 trigger 确认后允许人工结案；测试 `TestHandleSoftwareTriggerAfterAck` |
| **P1-5 配置半提交** | 重载失败时仍保存新配置并自动进入退避重试；`SettingsStore.Save` 加锁串行化 |
| **P1-6 静默失败** | 全局错误横幅（`#global-error`）+ 各模块加载失败上报；空数据与接口故障可区分 |
| **P1-7 缓存失效** | 静态资源 `Cache-Control: no-store`；`fs.Sub` 错误启动即 panic |
| **P1-8 保护记录** | `/api/actions` 改为统一分页（total/page/pageSize，默认 50/页）；前端保护记录加分页条；`retention` 同时清理终态动作记录 |
| **P1-9 自动刷新** | 告警看板 10s 轮询（页面隐藏/批量模式暂停，切走即停） |
| **P2 批量** | 摘要 `unacked` 排除孤儿；合并触发复用 upsert 返回的真实告警 ID（去掉二次定位查询）；下发前校验服务存在/已绑定；控制按设备串行化；`AfterFunc` 关闭后不再写库（`closed` 标志）；全量同步后对账孤儿；热力图单次遍历 O(n)；`errors.Is`、内部错误不再直出、202/412 语义、`alarm_id` 校验、retention 覆盖动作记录、`markSeen` 移到校验之后、软件配置校验移除、平台命名空间收敛到 `internal/platform`、GORM 日志桥接 slog、健康检查 `GET /api/health`、死代码（`resolvedAt`/级别常量/`logging.New`/`editingRuleId`）清理、死 CSS 清理、CSV 公式注入防护、`window.event` 移除、toast 改为非阻塞组件 |
| **测试** | 新增：时区桶、热力图索引、未知类型容错、protected 记录分页、`config.Validate`、路由兜底与 no-store、`store.Open` 真实文件库、`toFloat`、孤儿自愈、软件 trigger 结案、事件源边沿回归 |

### 未处理（大改动/高风险，待确认）

> 以下项属于结构性重构或部署决策，改动面大、回归风险高，已按要求留待确认：

1. **C1** `Manager` 拆分（6 类职责、9 把锁、~690 行 → clientManager / ruleEngine / ingest 组合根）
2. **C4** `definitions` 双锁收敛为单锁状态机（并发不变式目前靠时序侥幸）
3. **C6** 规则集内存缓存（每条消息全表查规则 + 每 level 源一次 COUNT；风暴工况性能）
4. **C7** 告警时长改为查询时派生（去掉每 5s 全量 UPDATE 的写放大）
5. **C13** 领域层抽取（告警状态机与规则校验移出 HTTP 层，便于复用与单测）
6. **C19** `NewManager` 依赖注入化（消除测试手搓结构体，补 web/config/logging 包级测试）
7. **C18** 构建/发布脚本（Makefile/Dockerfile/CI 与版本注入；当前 `buildinfo.Version` 恒为 `v0.0.0`）
8. **C20** `tools/alarm-sender` 复用 natsclient 信封实现（导出 `ToMsg`/`ModuleFromSubject`）
9. **B5-CDN** 第三方静态资源 vendoring（离线现场可用性；需要引入本地 vendor 目录）
10. **P0-2** 鉴权（经确认暂不处理；建议 README 写明"仅可部署于受信内网"）

---

## 十二、C 组小项修复状态（本轮）

| # | 项 | 状态 |
|---|----|------|
| 1 | `/api/history` start/end 非法参数 | 已修复：返回 400（与分布图口径一致） |
| 2 | 24h 今日趋势 O(n×24) | 已修复：单次遍历按小时索引落桶 O(n) |
| 3 | 分桶逻辑重复 | 已优化：今日趋势与分布图均为单次遍历；两者桶形状不同（24 槽 vs 4×24），未强行抽象 |
| 4 | 复合索引 | 已加：`idx_alarm_handled_occurred`、`idx_alarm_handled_cleared` |
| 5 | 规则删除不清理内存态 | 已修复：删除规则时 `ResetRuleState` 清理 `ruleHoldState`/`lastDispatch` |
| 6 | 清扫只跑一次、95s 窗口外滞留 | 已修复：启动清扫 + 每 60s 周期清扫 |
| 7 | event_id/event_key 混用产生重复行 | 已修复：定位键兼容 `event_id = ? OR (event_id = '' AND event_key = ?)` |
| 8 | reload/bootstrap 交叉、setAll 丢增量、局部失败静默 | 已修复：客户端换代后丢弃本轮结果；`setAll` 保留拉取窗口内新增设备；局部失败如实写 `lastError` |
| 9 | 单设备拉取失败仍显示"已同步" | 已修复：局部失败上报"部分设备定义拉取失败（N 台）" |
| 10 | 冷却命中即消耗边沿 | 保持现状（语义已注释）：若改为"不消耗边沿"，条件持续成立时会按冷却周期反复下发，风险更高 |
| 11 | toFloat 值不可区分 | 已修复：新增 `valueInvalid`（解析失败=true，前端显示"—"；用零值语义避开 GORM 默认值陷阱） |
| 12 | `EventCodeFrequentFail` 无处理 | 已修复：显式分支按普通软件事件上板并注明无额外处理 |
| 13 | `Alarm.Unit` 死字段 | 已修复：模型字段与前端单位分支删除 |
| 14 | 硬编码颜色 | 已修复：`--text-color` 变量 + `cssVar()` 读取，徽标/图表改用变量 |
| 15 | 无统一 loading 态 | 已修复：区块 `.is-loading` 加载态（各 refresh 函数包裹） |
| 16 | 设置保存"已保存待生效"不明确 | 已修复：响应 `{settings, runtimeReload: ok|pending, message}`，前端提示待生效 |
| 17 | 预留分支散落 | 已修复：集中为前端/后端两处 TODO 注释，说明放开条件与依赖 |

---

## 十三、结构性重构完成状态（本轮，经确认全部处理）

| 项 | 状态 | 说明 |
|----|------|------|
| C1 Manager 拆分 | 已完成（机械拆分） | `Manager` 只剩 ctx/db/log/defs/wg/closed；NATS 客户端生命周期独立为 `clientManager`（`client.go`）、规则状态与缓存为 `ruleEngine`（`rules.go`）、摄取去重为 `ingestState`（`ingest.go`），三者嵌入组合、各自持锁。规则评估方法仍保留在 Manager 上（通过嵌入状态访问），方法级迁移留作后续可选项 |
| C4 definitions 单锁 | 已完成 | 去掉 `stateMu` 与双锁，单锁 + `bootstrapping/retrying` 显式状态；`Syncing` 只由 begin/end/markError 在锁内写 |
| C6 规则集缓存 | 已完成 | `ruleEngine.ruleCache`：评估/播种按缓存读取；规则保存/删除经 `InvalidateRules`/`ResetRuleState` 失效 |
| C7 时长派生 | 已完成 | 删除每 5s 全量 UPDATE；读取时 `deriveDuration` 派生（活动=now-发生，已恢复=恢复-发生）；"按持续时长"排序改为 SQL 派生表达式 |
| C13 领域层抽取 | 已完成 | 新增 `internal/domain`：`ValidateRule`（规则校验与规范化）与 `AcknowledgeReason/HandleReason/RereadSkipReason`（告警状态机），API 只做 HTTP/DB 编排；domain 包有独立单测 |

验证：`gofmt` / `go vet` / `go build` / `go test ./...`（domain 新增 3 个测试）全绿；端到端冒烟（health / 设置保存结果 / 历史 400 / 动作分页）通过。
