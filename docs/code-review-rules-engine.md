# 保护动作规则引擎 · 代码走查报告

> 范围：多监测源告警保护动作引擎（新建/未提交代码），重点为 `internal/runtime/engine.go`、`internal/runtime/manager.go`、`internal/api/engine.go`、`internal/store/{models,db}.go`、`internal/web/static/js/engine.js`。
> 方法：逐文件阅读 + 三路并行审查（运行时 / API 存储 / 前端）+ 对 2 个 Critical 用真实模型做临时测试复现（测试文件已删除）。
> 结论：主链路结构完整、锁与去重基本正确，但有 **4 个 Critical**，其中 2 个已复现、2 个由代码路径确认。

## 一、Critical

### C1. 新建"停用"规则被 GORM 默认值改写为"启用"（已复现）
- **位置**：`internal/api/engine.go:136`（`s.DB.Create(&rule)`）+ `internal/store/models.go:192`（`Enabled bool gorm:"default:true"`）
- **证据**：实测 `Create(&AlarmRule{Enabled:false})` 后，落库与结构体均为 `Enabled=true`。GORM 对带 `default` tag 的零值字段在 struct 形式 Create 时会用默认值替换。
- **影响**：前端取消"启用"后新建 → 规则实际启用，`evaluateRules` 只查 `enabled=true` → 该规则立即参与匹配并下发保护动作，与用户意图相反。
- **修复**：Create 也走显式列 map（`enabled`、`version:0` 显式写），或去掉 `default:true` 并在 API 层补默认；补"enabled=false 新建"回归测试。
- 状态：**已验证复现**。

### C2. 旧单源规则迁移产物不可用（已复现）
- **位置**：`internal/store/db.go:49-65`；旧 schema（`git show HEAD:internal/store/models.go`）中 `AlarmRule` 只有 `device_id/property_key/operator/threshold/duration_ms/action_type/target_device_id/command/command_value`，**没有 `event_key`/`service_key` 列**。
- **证据**：模拟旧行（device_id、target_device_id、command_value 有值，event_key/service_key 为 NULL）迁移后得到
  `monitors=[{Kind:event, DeviceID:bms-001, EventKey:""}]`、`controls=[{Kind:device, DeviceID:pcs-001, ServiceKey:""}]`。
- **影响**：
  1. `monitorHolds` 要求 `msg.Device.EventKey == monitor.EventKey`，`EventKey=""` 时永不命中 → 旧规则静默失效（也不会报错）；
  2. 反过来，若消息缺 `event_key` 则会异常命中；
  3. 控制源 `serviceKey=""`，命中后向 `.cmd` 下发空服务名；
  4. 旧触发条件（property_key/operator/threshold/duration_ms）与 command 静默丢失，不可恢复（迁移后 monitors 非空，不会重试）；
  5. 迁移失败分支 `return` 无日志，线上不可观测。
- **修复**：旧行不要伪造成 monitor —— 置 `enabled=false` + `Warn` 输出 id/旧字段提示人工重建；如要自动映射，`command→serviceKey` 至少需人工确认；补真实旧 schema 的迁移测试（现有 `store/db_test.go` 用新列构造"旧数据"，掩盖了该问题）。

### C3. level 监测源计数被"永不恢复"的告警永久占住
- **位置**：`internal/runtime/engine.go:490-501`（`state='active'` 无条件计数）+ `internal/runtime/manager.go:445-451`（`kind=event` 也以 active 落库）+ `internal/api/alarms.go:264-266`（一次性事件无恢复沿，只能人工确认后结案）
- **证据（代码路径）**：`kind=event` 的告警不会自动恢复；引擎自身回填失败时发布的 `thingsmodel.cmd.result` 正是这样的 event 告警（level=2）。level 计数不过滤 kind / orphan。
- **影响**：任意一条 event 告警（尤其控制失败告警）→ `count>0` 恒成立 → 该 level 规则一旦成立，`ruleHoldState` 永不下降 → **后续真实告警再无极值上升沿，保护动作永久不再下发**。orphan 告警（定义已删除、永不恢复）同样污染。
- **修复**：level 计数加 `kind='trigger'` 且 `orphan=0`；孤儿状态变化（reconcile/tombstone/merge）后刷新该规则边沿。

### C4. 重启后首条消息把存量活动告警判成上升沿 → 重复下发
- **位置**：`internal/runtime/engine.go:431-455` + `internal/runtime/manager.go:80`（`ruleHoldState` 仅内存）
- **证据（代码路径）**：重启后 map 为空；首条消息评估时 `holds=true`、`previous=false` → 上升沿成立；`lastDispatch` 同样为空 → 冷却放行。
- **影响**：重启前已命中的 level 条件（存量活动告警）会在重启后再次触发全部控制源，与 `engine.go:431` 注释"重启不预评估（避免开机对存量活动告警重复动作）"**行为相反**。
- **修复**：启动时执行一次"只写边沿不下发"的 seed 评估；或 `ruleHoldState` 初始化时对存量规则做一次 `monitorHolds` 采样。

## 二、High

| # | 位置 | 问题 | 影响/触发 | 建议 |
|---|------|------|-----------|------|
| H1 | `runtime/engine.go:543-571`、`284-287` | `request_id` 在 REQ/REP 返回后才落库，窗口内到达的 `cmd.result` 失败按 request_id 查不到记录 | 失败被吞 → 记录 accepted → 35s 后误报"推定成功"（窗口 = 整个 REQ/REP 往返） | 预生成 request_id 并在发布前落库（拆 Publish/Await） |
| H2 | `runtime/engine.go:284-295` | `First` 只查 pending/accepted，推定成功后的迟到失败永远走不到允许覆盖 success 的更新分支，注释与实现矛盾 | 控制实际失败却显示推定成功 | `First` 条件加 `success`（并保留现有的条件更新互斥） |
| H3 | `runtime/engine.go:459-505` | `all` 聚合下 ≥2 个 event 源永不成立（event 源只看当前这一条消息） | UI 允许配置，保存成功但规则永不触发；无提示 | 保存时拒绝 `all + 多 event 源`，或 event 源改查 DB 活性 |
| H4 | `store/models.go:128`、`engine.js:66/363/399` | `RuleControl.Value` 带 `json:"value,omitempty"`，value=0 在回读时被省略 → 前端 `undefined` | 归零/停机类规则（setPower=0）编辑时输入框空白、卡片显示 `undefined`；不改该字段直接保存必报"指令值必须是数字"，保存被卡死 | 去掉 omitempty（或 `*float64`）；前端 `value: Number.isFinite(Number(c.value)) ? ... : 0` |
| H5 | `engine.js:409-426` | 乐观锁 409 后 Modal 保留、version 仍旧快照，重试必然再 409；引擎页无"刷新规则"入口 | 双开标签页 / 并发编辑后陷入死循环，只能整页刷新 | 捕获冲突后 `loadRules()` 回写 `en-version` 并提示复核 |
| H6 | `engine.js:97-98` | 内联 `onclick="editRule('${escapeHtml(r.id)}')"`：`&#39;` 在属性解析阶段还原为 `'`，可闭合 JS 字符串 | API 无 id 字符集校验且 `/api` 无鉴权 → 写入恶意 id 后运维点击卡片触发存储型 XSS | 改 `data-id` + 事件委托；API 层校验 id 字符集 |
| H7 | `engine.js:174-181`、`api/engine.go:62-75` | 事件→级别切换不清 deviceId/eventKey/eventName，且 levels 被强制重置为 [3]；后端只清 Type 不裁剪混淆字段 | 用户已选 P0+P2 被静默改成 P3；脏字段落 JSON blob | 切换时互相清理；levels 仅在为空时补默认；后端按 kind 白名单裁剪 |
| H8 | `runtime/engine.go:320-322` | 墓碑后零消息评估可触发 level 规则下发，`ActionRecord.AlarmID` 为空 | 保护记录"触发告警"列空白，动作来源不可追溯 | 该次评估只刷新边沿、禁止下发；或携带墓碑上下文 |

## 三、Medium（按子系统）

**运行时**
- 冷却命中时边沿已消耗、控制通道 nil 时冷却也已消耗（`engine.go:447-451`、`537-542`）：冷却期内真实新沿被丢弃，需下一次下降→上升才再触发（与文档语义一致，但需明确）。
- 启动清扫只有一次且窗口 95s（`engine.go:230-246`）：重启前 <95s 的 pending/accepted 永久滞留"已受理"。
- reload 与在跑的 bootstrap 交叉（`engine.go:143-180`、`manager.go:170-185`）：旧 client 被 Close，拉取失败仅 Warn → 写入"有设备无事件"快照且 LastError 空，新 bootstrap 被 singleflight 丢弃。
- `setAll` 全量覆盖会丢 bootstrap 期间新增设备、让已删设备回流（`engine.go:84-98`）。
- 单设备事件/服务拉取失败静默降级，仍打"已同步"（`engine.go:182-196`）。
- 合并路径不复位 orphan（`manager.go:408-428`）：源已恢复仍显示孤儿且不可结案，并继续污染 level 源。
- `event_id` 有/无混用 → 同一事件两条活动行，旧行永不恢复（`manager.go:532-543`）。
- 合并后 `ActionRecord.AlarmID` 指向不存在的行（`engine.go:544`）。
- `markSeen` 早于字段校验占位（`manager.go:317-331`）：非法消息吞掉 alarm_id，修正后重投被幂等丢弃。
- 下发前不校验 definitions 的 `methods`（bound/存在性），违反契约 §6（`engine.go:537-554`）。
- 多规则/多控制源并发下发无按设备串行化，冲突命令顺序不确定（`engine.go:508-516`）。
- 每条消息全表查启用规则 + 每 level 源一次 COUNT（`engine.go:433-437`、`490-501`），风暴下放大 SQLite 压力。

**API / 存储**
- 单行 JSON 损坏会同时打挂规则列表与全部规则评估（`models.go:174-183`、`runtime/engine.go:433-437`）—— fail-stop 全引擎。
- 冷却无上限，`time.Duration` 溢出后 fail-open（`api/engine.go:108-111`、`runtime/engine.go:528`）。
- Create 路径忽略 `version>0`，已删除规则会被"复活"为 version 0（`api/engine.go:131-140`）。
- 并发创建同 ID 返回 500（唯一约束）而非 409（`api/engine.go:113-140`）。
- 版本自增非原子：`nextVersion` 取自 First 的旧读（`api/engine.go:116-128`），极端交错下版本原地踏步，旧版本写入仍被接受。
- 控制源不校验设备/服务存在性与取值域，定义缓存未用于保存校验（`api/engine.go:98-107`）。
- 内部错误信息直出响应体（`api/engine.go` 多处 `fail(500, err.Error())`）。
- `kind` 混淆字段静默接受（`api/engine.go:62-92`）。
- ID/名称无长度与字符集约束；含 `/` 的 ID 无法删除（`api/engine.go:41-48`、`159-161`）。
- `/api` 全部变更接口无鉴权（`router.go:28-53`）—— 若端口不只在反代后可达，等于设备物理控制面裸奔（部署备注）。

**前端**
- 换服务名后"工程值范围/合法值"提示不更新（`engine.js:222` vs `340-364`）。
- 定义刷新完成后已打开的 Modal 不重建下拉，必须关闭重开（`engine.js:13-24`）。
- 秒→毫秒无上限/空值静默变 0（`engine.js:409-410`），`1e400` 等极端值可让冷却静默清零。
- 保存按钮无 disabled/在途标志（`engine.js:377-426`）：双击更新路径一成功一 409，新建路径可能撞主键 500。
- 未校验 `method.bound` 与定义存在性（`engine.js:384-406`），可保存注定失败的控制源。
- `loadRules` 吞异常置 `[]`（`engine.js:6`）：抖动后显示"暂无规则"且无提示。
- `new bootstrap.Modal(...)` 每次新建不复用（`engine.js:120/141/421`）：快速连点可残留 backdrop。

## 四、Low（摘要）
- `ruleHoldState`/`lastDispatch` 不清理已删规则；停用后再启用时 previous 残留 true 不再触发（`engine.go:444-446`、`531`）。
- `time.AfterFunc` 不受 WaitGroup 跟踪；停机窗口内可能访问已关闭 DB（`engine.go:569`）。
- `ack.Status` 非 accepted/failure 一律按受理成功（`engine.go:563-570`）；应答 request_id 不匹配只 Warn（`client.go:142/208`）。
- `recoverAlarm` 不检查 RowsAffected、不判新旧（`manager.go:570-577`）。
- `EventCodeFrequentFailure` 已定义但无处理（`message.go:33`）。
- `toggleMonitorLevel` 未兜底 `levels`（`engine.js:193`）；编辑时 id 空串会退化为新增（`engine.js:146`）。
- `toast()` 实为 `alert()`（`helpers.js:37-43`）：阻塞、双前缀、成功无反馈。
- `internal/store/models.go` 未 gofmt。

## 五、已确认无问题
- 三处内存 map 读写均在锁内，锁序单向，无死锁；`goTracked`/WaitGroup 无误用，Stop 先 Drain 再 Wait。
- 无 double-finalize：success 只从 accepted 升级、failure 优先、重复 cmd.result 由 RowsAffected 早退。
- go 1.23 循环变量捕获安全。
- SQL 全参数化（GORM），无注入面。
- Valuer/Scanner 对称，空列表返回 `[]`（前端不踩 null）。
- `ruleUpdateMap` 列完整；DeleteRule 404 语义正确。
- 前端 XSS 其余字段均 escapeHtml（仅 H6 的 onclick 拼 id 例外）。
- 单选项下拉占位修复有效；小数秒换算（0.5→500ms）正确。

## 六、测试缺口（建议补）
1. "存量活动告警"场景：重启后首次评估、event/orphan 参与 level 计数（现有用例全部从空库起步，抓不到 C3/C4）。
2. 真实旧 schema 迁移用例（现有 `db_test` 用新列构造旧数据，掩盖 C2）。
3. `enabled:false` 新建用例（抓 C1）、并发/乐观锁竞态用例。
4. `cmd.result` 早于 REP 返回的用例；推定成功后迟到失败的用例。
5. `go test -race`；DeleteRule/ListActions/GetDefinitions/RefreshDefinitions 覆盖。

## 七、建议修复顺序
1. **C1**（停用规则被启用）→ **C3**（level 计数污染）→ **C2**（迁移）→ **C4**（重启重复下发）。
2. **H1/H2**（闭环误报）→ **H3**（all 模式）→ **H4/H5**（前端阻断性）。
3. **H6**（XSS）→ **H7** → 其余 Medium 批量。
4. 同步补第六节测试。

---

## 八、修复状态（本次已落地）

| 项 | 状态 | 修复要点 |
|----|------|----------|
| C1 停用规则被启用 | 已修复 | 新建走显式列 map（`ruleCreateMap`），不再触发 GORM default 覆盖；测试 `TestSaveRuleDisabledCreate` |
| C2 旧规则迁移不可用 | 已修复 | 旧规则不再伪造监测源，改为停用 + `slog.Warn`（含旧字段提示人工重建）；新增 `repairBrokenRules` 停用历史上已错误迁移的规则；测试 `TestMigrateLegacyRules`（含幂等与修复） |
| C3 level 被不可恢复告警污染 | 已修复 | level 计数限定 `kind='trigger' AND orphan=0`；测试 `TestLevelMonitorExcludesEventAndOrphan` |
| C4 重启重复下发 | 已修复 | 启动期 `seedRuleHoldState()` 只写边沿不下发（订阅启动前执行）；测试 `TestSeedPreventsRestartDuplicateDispatch`（含未播种对照） |
| H1 request_id 落库晚于 REQ/REP | 已修复 | 引擎预生成 request_id → 先落记录 → 发布（`ControlRequest.RequestID` 走请求头不进负载）；终态改为条件更新，避免覆盖窗口内已闭环的失败 |
| H2 推定成功后迟到失败被丢 | 已修复 | `closeActionLoop` 的匹配条件加入 `success`；测试 `TestLateCmdResultOverridesPresumedSuccess` |
| H3 all + 多事件源永不成立 | 已修复 | 保存时 400 拒绝；前端同样前置校验；测试 `TestSaveRuleRejectsAllModeMultiEvent` |
| H4 value=0 回读丢失 | 已修复 | 去掉 `RuleControl.Value` 的 `omitempty`（旧数据前端归一为 0）；`TestSaveRuleMultiSourceLifecycle` 与前端 VM 验证覆盖 |
| H5 409 后 Modal 无法自愈 | 已修复 | 保存失败后自动 `loadRules` 回写 `en-version`，提示复核后重试 |
| H6 内联 onclick 拼 id XSS | 已修复 | 改 `data-rule-id` + `this.dataset.ruleId`；API 增加 ID 字符集校验（字母数字 `_` `-`，1-128）；测试 `TestSaveRuleIDCharset` |
| H7 kind 切换残留脏字段 | 已修复 | 前端切换时互相清理并保留已选级别；后端按 kind 白名单裁剪字段；测试 `TestSaveRuleTrimsKindFields` |
| H8 墓碑重算可触发下发 | 已修复 | 墓碑后改用 `evaluateRulesQuiet`（只刷新边沿、不下发）；测试 `TestTombstoneEvaluationDoesNotDispatch` |
| M 冷却无上限溢出 | 已修复 | API 与前端均限制 0~86400000ms（24h）；测试 `TestSaveRuleCooldownBounds` |
| M 版本自增非原子 | 已修复 | 更新走 `gorm.Expr("version + 1")`，响应版本 = 提交版本 + 1 |
| M 已删规则被"复活" | 已修复 | `version>0` 且记录不存在 → 404 |
| M 并发同 ID 创建 500 | 已修复 | 唯一约束冲突 → 409 |
| M 前端杂项 | 已修复 | 保存防重复提交；定义缓存瞬时失败不清空既有快照；换服务名刷新工程值提示；保存前校验事件/服务仍在定义缓存且 bound |
| M 代码风格 | 已修复 | 本次涉及文件 `gofmt` 通过 |

未在本轮修复（保留在第三/四节，建议后续迭代）：定义缓存与 reload 的交叉竞态、`setAll` 增量覆盖、合并不复位 orphan、`event_id`/`event_key` 混用、`markSeen` 早占位、下发前未校验 `query.methods`、按设备串行化、`/api` 鉴权、错误信息直出等。
