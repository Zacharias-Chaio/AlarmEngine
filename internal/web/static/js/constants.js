// constants.js — 常量定义与全局 state（单一数据源）

// 登录凭据（前端静态校验）
const LOGIN_USER = 'admin';
const LOGIN_PASS = '666';

// 告警级别（摘要卡按 紧急→严重→一般→提示 展示；紧急深红、严重橙、一般琥珀、提示蓝）
const ALARM_LEVELS = [
  { value: 0, name: '提示', cls: 'alarm-level-0', color: '#3b82f6' },
  { value: 1, name: '一般', cls: 'alarm-level-1', color: '#f59e0b' },
  { value: 2, name: '严重', cls: 'alarm-level-2', color: '#ea580c' },
  { value: 3, name: '紧急', cls: 'alarm-level-3', color: '#dc2626' },
];

// 告警类型（筛选顺序：系统 / 软件 / 设备；系统告警由外部模块按标准发布）
const ALARM_TYPES = [
  { value: 'system', name: '系统告警', icon: 'bi-cpu', color: '#0ea5e9' },
  { value: 'software', name: '软件告警', icon: 'bi-boxes', color: '#8b5cf6' },
  { value: 'device', name: '设备告警', icon: 'bi-hdd-network', color: '#4f46e5' },
];

// 告警展示状态（由 state + orphan + handled 派生，三态收束）：
// 活动中=触发持续中（含未确认/已确认）；已恢复=自动恢复未结案；孤儿=触发源失联（配置变更/设备离线等）。
// 确认（Acknowledged）是正交的操作维度（未确认/已确认），不构成独立状态。
const ALARM_STATES = [
  { key: 'active', name: '活动中', cls: 'state-active' },
  { key: 'recovered', name: '已恢复', cls: 'state-recovered' },
  { key: 'orphan', name: '孤儿', cls: 'state-orphan' },
  { key: 'handled', name: '已处理', cls: 'state-handled' },
];

// 列表/筛选用展示状态（不含"已处理"，已结案告警进入历史）
const ALARM_STATE_FILTERS = ALARM_STATES.filter(s => s.key !== 'handled');

// 派生展示状态：handled > orphan > cleared(已恢复) > active(活动中)
function displayStateKey(alarm) {
  if (alarm.handled) return 'handled';
  if (alarm.orphan) return 'orphan';
  if (alarm.state === 'cleared') return 'recovered';
  return 'active';
}
function displayStateMeta(alarm) {
  return ALARM_STATES.find(s => s.key === displayStateKey(alarm)) || ALARM_STATES[0];
}

// 是否允许"处理"结案：仅已恢复（自动恢复且非孤儿）；一次性事件（kind=event）确认后可视为即时恢复直接结案
function canHandleAlarm(alarm) {
  if (alarm.handled) return false;
  if (alarm.orphan) return false;
  if (alarm.state === 'cleared') return true;
  if (alarm.kind === 'event' && alarm.acknowledged) return true;
  return false;
}

// 告警标签（自动派生）：device=事件规则key+监测点；software/system=事件码+组件；聚合计数追加 ×N
function alarmTagList(alarm) {
  const tags = [];
  if (alarm.type === 'device') {
    if (alarm.eventKey) tags.push(alarm.eventKey);
    if (alarm.point) tags.push(alarm.point);
  } else {
    if (alarm.code) tags.push(alarm.code);
    if (alarm.component) tags.push(alarm.component);
  }
  return tags;
}

// 当前值/阈值展示文本（统一口径）：仅设备告警有值与判定阈值，拼接单位；其余返回空串。
function alarmValueText(alarm) {
  if (alarm.type !== 'device' || alarm.value == null) return '';
  return String(alarm.value) + (alarm.unit ? ' ' + alarm.unit : '');
}
function alarmThresholdText(alarm) {
  if (alarm.type !== 'device' || alarm.threshold == null) return '';
  return String(alarm.threshold) + (alarm.unit ? ' ' + alarm.unit : '');
}

// 规则操作符
const RULE_OPERATORS = [
  { value: 'gt', name: '大于 (>)' },
  { value: 'gte', name: '大于等于 (>=)' },
  { value: 'lt', name: '小于 (<)' },
  { value: 'lte', name: '小于等于 (<=)' },
  { value: 'eq', name: '等于 (=)' },
];

// 动作类型
const ACTION_TYPES = [
  { value: 'protection', name: '保护动作' },
  { value: 'notify', name: '通知' },
];

// 列表分页：每页固定 15 条
const PAGE_SIZE = 15;

// 全局 state（唯一数据源）
const state = {
  alarms: [],
  alarmFilters: { severity: '', type: '', source: '', state: '', sort: 'time', page: 1 },
  alarmTotal: 0,
  alarmSummary: null,
  selectedAlarmId: null,

  history: [],
  historyFilters: { start: '', end: '', type: '', source: '', severity: '', handled: '', page: 1 },
  historyTotal: 0,
  historySourceOptions: [], // 历史页来源下拉累积选项（避免筛选后塌缩）
  statistics: null,
  trendGranularity: 'day',

  rules: [],
  editingRuleId: null,

  settings: null,
  systemInfo: null,
};

// 工具：按值查找元数据
function levelMeta(severity) {
  return ALARM_LEVELS.find(l => l.value === Number(severity)) || { value: severity, name: String(severity), cls: 'alarm-level-0', color: '#9ca3af' };
}

function typeMeta(type) {
  return ALARM_TYPES.find(t => t.value === type) || { value: type, name: String(type), icon: 'bi-question-circle', color: '#9ca3af' };
}
function operatorMeta(op) {
  return RULE_OPERATORS.find(o => o.value === op) || { value: op, name: String(op) };
}
function actionTypeMeta(type) {
  return ACTION_TYPES.find(a => a.value === type) || { value: type, name: String(type) };
}
