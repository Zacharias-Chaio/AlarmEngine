// settings.js — 软件配置模块

const SETTINGS_CARDS = [
  { icon: 'journal-text', title: '日志配置', subtitle: '终端与文件日志输出', fields: [
    { path: 'logger.level', label: '日志级别', type: 'select', options: [['debug', 'Debug'], ['info', 'Info'], ['warn', 'Warn'], ['error', 'Error']] },
    { path: 'logger.console', label: '终端输出', type: 'boolean' },
    { path: 'logger.file', label: '文件输出', type: 'boolean' },
    { path: 'logger.fileConfig.compress', label: '压缩归档', type: 'boolean' },
    { path: 'logger.fileConfig.filename', label: '日志文件路径' },
    { path: 'logger.fileConfig.maxSize', label: '单文件上限 (MB)', type: 'number', min: 1 },
    { path: 'logger.fileConfig.maxBackups', label: '历史文件份数', type: 'number', min: 0 },
    { path: 'logger.fileConfig.maxAge', label: '保留天数', type: 'number', min: 0 },
  ] },
  { icon: 'broadcast-pin', title: 'NATS 客户端', subtitle: '单实例：订阅告警输入 + 查询物模型定义 + 下发控制', fields: [
    { path: 'nats.enabled', label: '启用连接', type: 'boolean' },
    { path: 'nats.retryOnFailedConnect', label: '失败后重试', type: 'boolean' },
    { path: 'nats.url', label: '服务地址' },
    { path: 'nats.name', label: '连接名称' },
    { path: 'nats.alarmSubject', label: '告警订阅主题', hint: '平台告警标准命名空间，默认 powerpulse.alarm.>（收全部模块）' },
    { path: 'nats.thingsmodelPrefix', label: '物模型主题前缀', hint: '用于 query.* 定义查询与 .cmd 控制下发，默认 powerpulse.thingsmodel' },
    { path: 'nats.requestTimeout', label: 'REQ 超时 (ms)', type: 'number', min: 0 },
    { path: 'nats.connectTimeout', label: '连接超时 (ms)', type: 'number', min: 0 },
    { path: 'nats.reconnectWait', label: '重连间隔 (ms)', type: 'number', min: 0 },
    { path: 'nats.maxReconnects', label: '最大重连次数', type: 'number', min: -1 },
    { path: 'nats.reconnectBufSize', label: '断连缓冲 (bytes)', type: 'number', min: 0 },
    { path: 'nats.pingInterval', label: '心跳间隔 (ms)', type: 'number', min: 0 },
    { path: 'nats.maxPingsOut', label: '最大未响应心跳数', type: 'number', min: 0 },
  ] },
  { icon: 'archive', title: '数据保留', subtitle: '历史告警清理策略', fields: [
    { path: 'retention.days', label: '保留天数', type: 'number', min: 0, hint: '已结案告警保留天数；0 = 永久保留（每日清理一次）' },
  ] },
];

function settingsFieldID(path) { return 'setting-' + path.replaceAll('.', '-'); }
function settingsGet(object, path) { return path.split('.').reduce((value, key) => value && value[key], object); }
function settingsSet(object, path, value) {
  const keys = path.split('.');
  const last = keys.pop();
  const target = keys.reduce((value, key) => value[key] || (value[key] = {}), object);
  target[last] = value;
}

async function loadPlatformSettings() {
  try {
    state.settings = await SettingsAPI.get();
  } catch (error) {
    state.settings = null;
    toast(error.message, 'error');
  }
}

async function loadSystemInfo() {
  try {
    state.systemInfo = await SettingsAPI.system();
  } catch (error) {
    state.systemInfo = null;
  }
}

function renderSettingsField(field, settings) {
  const id = settingsFieldID(field.path);
  const value = settingsGet(settings, field.path);
  const hint = field.hint ? `<div class="form-text">${escapeHtml(field.hint)}</div>` : '';
  if (field.type === 'boolean') return `<div class="settings-field"><div class="settings-switch"><label for="${id}">${escapeHtml(field.label)}</label><div class="form-check form-switch m-0"><input id="${id}" class="form-check-input" type="checkbox"${value ? ' checked' : ''}></div></div>${hint}</div>`;
  if (field.type === 'select') return `<div class="settings-field"><label for="${id}">${escapeHtml(field.label)}</label><select id="${id}" class="form-select">${field.options.map(([optionValue, optionLabel]) => `<option value="${escapeHtml(optionValue)}"${optionValue === value ? ' selected' : ''}>${escapeHtml(optionLabel)}</option>`).join('')}</select>${hint}</div>`;
  const type = field.type === 'number' ? 'number' : 'text';
  const min = field.min !== undefined ? ` min="${field.min}"` : '';
  return `<div class="settings-field"><label for="${id}">${escapeHtml(field.label)}</label><input id="${id}" type="${type}" class="form-control" value="${escapeHtml(value)}"${min}>${hint}</div>`;
}

function settingsCard(icon, title, subtitle, body) {
  return `<article class="settings-card"><div class="settings-card-head"><i class="bi bi-${icon}"></i><div><div class="settings-card-title">${escapeHtml(title)}</div><div class="settings-card-sub">${escapeHtml(subtitle)}</div></div></div>${body}</article>`;
}

function softwareInfoCard() {
  const info = state.systemInfo;
  return settingsCard('info-circle', '软件信息', '当前运行环境', `<dl class="settings-static">
    <div><dt>操作系统</dt><dd>${escapeHtml(info ? info.operatingSystem : '不可用')}</dd></div>
    <div><dt>系统时间</dt><dd>${escapeHtml(info ? new Date(info.systemTime).toLocaleString() : '不可用')}</dd></div>
    <div><dt>AlarmEngine 版本</dt><dd>${escapeHtml(info ? info.alarmEngineVersion : '不可用')}</dd></div>
    <div><dt>软件 ID</dt><dd>${escapeHtml(state.settings ? state.settings.software.id : '—')}</dd></div>
    <div><dt>软件名称</dt><dd>${escapeHtml(state.settings ? state.settings.software.name : '—')}</dd></div>
  </dl>`);
}

function restartCard() {
  return settingsCard('arrow-repeat', '软件重启', '运行时维护操作', `<div class="settings-restart"><p>重建 NATS 客户端连接并清空运行缓存，HTTP 服务保持可用。</p><button id="software-restart" class="btn btn-outline-danger w-100" type="button" onclick="restartSoftware()"><i class="bi bi-arrow-clockwise me-1"></i>软件重启</button></div>`);
}

function renderSettings() {
  const target = document.getElementById('settings-cards');
  const save = document.getElementById('settings-save');
  if (!target) return;
  if (!state.settings) {
    target.innerHTML = '<div class="empty-state"><i class="bi bi-exclamation-circle"></i><div>无法读取平台设置。</div></div>';
    if (save) save.disabled = true;
    return;
  }
  if (save) save.disabled = false;
  const cards = SETTINGS_CARDS.map(card => settingsCard(card.icon, card.title, card.subtitle, `<div class="settings-fields">${card.fields.map(field => renderSettingsField(field, state.settings)).join('')}</div>`));
  // 三列流式瀑布布局：卡片平铺输出，列排布交给 CSS columns。
  target.innerHTML = [softwareInfoCard(), ...cards, restartCard()].join('');
}

async function saveSettings() {
  try {
    if (!state.settings) throw new Error('平台设置尚未加载');
    const settings = JSON.parse(JSON.stringify(state.settings));
    SETTINGS_CARDS.forEach(card => card.fields.forEach(field => {
      const input = document.getElementById(settingsFieldID(field.path));
      let value = field.type === 'boolean' ? input.checked : input.value.trim();
      if (field.type === 'number') {
        value = Number(value);
        if (!Number.isFinite(value)) throw new Error(`${field.label} 必须是数字`);
      }
      settingsSet(settings, field.path, value);
    }));
    const result = await SettingsAPI.save(settings);
    state.settings = result.settings || result;
    renderSettings();
    if (result.runtimeReload === 'pending') {
      toast(result.message || '设置已保存；消息总线重连失败，已进入自动重试。');
    } else {
      toast('设置已保存，日志与 NATS 客户端已分别热加载。');
    }
  } catch (error) {
    toast('保存设置失败：' + error.message, 'error');
  }
}

async function restartSoftware() {
  if (!confirm('确认软件重启？消息订阅会短暂中断，接收缓存将被清空。')) return;
  const button = document.getElementById('software-restart');
  button.disabled = true;
  button.innerHTML = '<span class="spinner-border spinner-border-sm me-1" aria-hidden="true"></span>正在重启';
  try {
    await SettingsAPI.restart();
    toast('运行时正在重启。');
    setTimeout(() => { button.disabled = false; button.innerHTML = '<i class="bi bi-arrow-clockwise me-1"></i>软件重启'; }, 800);
  } catch (error) {
    button.disabled = false;
    button.innerHTML = '<i class="bi bi-arrow-clockwise me-1"></i>软件重启';
    toast('软件重启失败：' + error.message, 'error');
  }
}
