// engine.js — 告警引擎（保护动作规则配置：多监测源 anyone/all 聚合 → 多控制源）模块。
// 动作下发记录见 protection.js（保护记录页）。

// ===== 数据加载 =====
async function loadRules() {
  try { state.rules = await RulesAPI.list(); } catch (e) { state.rules = []; reportLoadError(e, '规则列表'); }
}

async function loadDefinitions() {
  try { state.definitions = await DefinitionsAPI.get(); }
  catch (e) {
    // 保留既有快照：一次瞬时失败不应清空已加载的设备/事件/服务。
    if (!state.definitions) state.definitions = null;
  }
}

async function refreshEngine() {
  setSectionLoading('engine', true);
  try {
    await Promise.all([loadRules(), loadDefinitions()]);
    renderRules(); renderDefStatus();
  } finally { setSectionLoading('engine', false); }
}

async function refreshDefinitions() {
  try {
    await DefinitionsAPI.refresh();
    toast('定义同步已触发，稍后自动刷新。');
    setTimeout(async () => { await loadDefinitions(); renderDefStatus(); }, 1500);
  } catch (e) { toast('同步定义失败：' + e.message, 'error'); }
}

// ===== 定义缓存 =====
function definitionDevices() {
  return (state.definitions && state.definitions.devices) || [];
}

function findDevice(deviceId) {
  return definitionDevices().find(d => d.device && d.device.device_id === deviceId);
}

function renderDefStatus() {
  const el = document.getElementById('en-def-status');
  if (!el) return;
  const def = state.definitions;
  if (!def) { el.innerHTML = '<i class="bi bi-exclamation-circle me-1"></i>定义缓存不可用（NATS 未启用或运行时未就绪）'; return; }
  const devices = def.devices || [];
  const events = devices.reduce((n, d) => n + (d.events || []).length, 0);
  const base = `定义缓存：${devices.length} 设备 · ${events} 事件`;
  if (def.syncing) { el.innerHTML = '<i class="bi bi-arrow-repeat me-1"></i>' + base + ' · 同步中…'; return; }
  if (def.lastError) { el.innerHTML = `<i class="bi bi-x-circle me-1" style="color:var(--danger)"></i>${base} · 同步失败：${escapeHtml(def.lastError)}`; return; }
  const synced = def.syncedAt && !String(def.syncedAt).startsWith('0001-');
  el.innerHTML = base + (synced ? ` · 同步于 ${fmtTime(def.syncedAt)}` : ' · 尚未同步');
}

// ===== 规则卡片 =====
// 展示结构：头部（名称/描述/聚合方式/启停）→ 监测源分区 → 控制源分区 → 页脚（冷却/ID/操作）。
// 监测源按条目展示（级别芯片 / 设备·事件），不再压成一行文本，便于多源规则快速扫读。
function monitorItems(monitor) {
  if (monitor.kind === 'event') {
    const typeNames = { device: '设备', software: '软件', system: '系统' };
    const device = findDevice(monitor.deviceId);
    const deviceName = (device && device.device.device_name) || monitor.deviceId || '未选设备';
    const eventName = monitor.eventName || monitor.eventKey || '未选事件';
    return { icon: 'bi-cpu', text: `${typeNames[monitor.type || 'device']} · ${deviceName} · ${eventName}` };
  }
  // level：级别芯片（升序），不区分类型。
  return { icon: 'bi-flag', levels: [...(monitor.levels || [])].sort((a, b) => a - b) };
}

function controlItems(control) {
  const device = findDevice(control.deviceId);
  const deviceName = (device && device.device.device_name) || control.deviceId || '未选设备';
  const value = Number.isFinite(Number(control.value)) ? Number(control.value) : 0;
  return { text: `${deviceName} · ${control.serviceKey || '未选服务'} = ${value}` };
}

// 冷却展示：整秒不带小数，非整秒保留一位（存储为毫秒）。
function fmtCooldown(ms) {
  const sec = Number(ms) / 1000;
  if (!Number.isFinite(sec) || sec <= 0) return '';
  return Number.isInteger(sec) ? `${sec} 秒` : `${sec.toFixed(1)} 秒`;
}

function renderRuleCard(r) {
  const mode = r.matchMode === 'all' ? '全部满足' : '任一满足';
  const monitors = (r.monitors || []).map(monitorItems);
  const controls = (r.controls || []).map(controlItems);
  const cooldown = r.cooldownMs > 0 ? fmtCooldown(r.cooldownMs) : '';
  const disabled = r.enabled === false;
  const description = (r.description || '').trim();
  const monitorRows = monitors.length
    ? monitors.map(m => {
        if (m.levels) {
          if (!m.levels.length) return '<li><i class="bi bi-flag"></i><span class="rule-hint">级别告警 · 未选级别</span></li>';
          const chips = m.levels.map(l => {
            const meta = levelMeta(l);
            return `<span class="level-chip active" style="background:${meta.color}" title="${escapeHtml(meta.name)}">P${l}</span>`;
          }).join('');
          return `<li><i class="bi bi-flag"></i><span class="rule-levels">${chips}</span><span class="rule-hint">级别告警 · 活动数 &gt; 0 即成立</span></li>`;
        }
        return `<li><i class="bi ${m.icon}"></i><span>${escapeHtml(m.text)}</span></li>`;
      }).join('')
    : '<li class="rule-empty">未配置监测源</li>';
  const controlRows = controls.length
    ? controls.map(c => `<li><i class="bi bi-lightning-charge"></i><span>${escapeHtml(c.text)}</span></li>`).join('')
    : '<li class="rule-empty">未配置控制源</li>';
  return `<div class="model-card rule-card${disabled ? ' rule-card-disabled' : ''}">
    <div class="rule-card-head">
      <div class="model-card-icon"><i class="bi bi-gear"></i></div>
      <div class="rule-card-titles">
        <div class="rule-card-title">${escapeHtml(r.name)}</div>
        ${description ? `<div class="rule-card-sub">${escapeHtml(description)}</div>` : ''}
      </div>
      <div class="rule-card-badges">
        <span class="rule-agg-chip" title="监测源聚合方式">${mode}</span>
        <span class="status-badge ${disabled ? 'disabled' : 'enabled'}">${disabled ? '停用' : '启用'}</span>
      </div>
    </div>
    <div class="rule-section">
      <div class="rule-section-title"><i class="bi bi-broadcast"></i>监测源 <span class="rule-count">${monitors.length}</span></div>
      <ul class="rule-list">${monitorRows}</ul>
    </div>
    <div class="rule-section">
      <div class="rule-section-title"><i class="bi bi-lightning-charge"></i>控制源 <span class="rule-count">${controls.length}</span></div>
      <ul class="rule-list">${controlRows}</ul>
    </div>
    <div class="rule-card-foot">
      <span class="rule-meta" title="同一规则重复下发的最小间隔"><i class="bi bi-hourglass-split me-1"></i>${cooldown ? '冷却 ' + cooldown : '不限冷却'}</span>
      <span class="rule-id" title="${escapeHtml(r.id)}">${escapeHtml(r.id)}</span>
      <span class="ms-auto d-flex gap-1">
        <button class="btn btn-outline-secondary btn-sm" data-rule-id="${escapeHtml(r.id)}" onclick="editRule(this.dataset.ruleId)"><i class="bi bi-pencil me-1"></i>编辑</button>
        <button class="btn btn-outline-danger btn-sm" data-rule-id="${escapeHtml(r.id)}" onclick="deleteRule(this.dataset.ruleId)" title="删除规则"><i class="bi bi-trash"></i></button>
      </span>
    </div>
  </div>`;
}

function renderRules() {
  const grid = document.getElementById('en-rules-grid');
  const count = document.getElementById('en-rule-count');
  if (count) count.textContent = state.rules.length;
  if (!grid) return;
  if (!state.rules.length) {
    grid.innerHTML = '<div class="empty-state"><i class="bi bi-inboxes"></i><div>暂无规则，点击右上角「新建规则」开始配置。</div></div>';
    return;
  }
  grid.innerHTML = state.rules.map(renderRuleCard).join('');
}

// ===== 规则编辑（草稿 + 动态源卡片）=====
// 草稿只承载监测/控制源与聚合模式；基本信息字段在保存时从 DOM 读取。
function ruleDraft() { return state.ruleDraft; }

function blankMonitor() { return { kind: 'level', type: '', levels: [3] }; }
function blankControl() { return { kind: 'device', deviceId: '', serviceKey: '', value: 0 }; }

function newRule() {
  state.ruleDraft = {
    matchMode: 'anyone',
    monitors: [blankMonitor()],
    controls: [blankControl()],
  };
  renderRuleEditor();
  document.getElementById('ruleModal').querySelector('.modal-title').innerHTML = '<i class="bi bi-gear me-2" style="color:var(--primary)"></i>新建规则';
  bootstrap.Modal.getOrCreateInstance(document.getElementById('ruleModal')).show();
}

function editRule(id) {
  const rule = state.rules.find(r => r.id === id);
  if (!rule) return;
  // 旧模型兼容：无 monitors 但有旧字段时合成事件源（后端启动迁移后通常不会出现）。
  let monitors = rule.monitors && rule.monitors.length
    ? rule.monitors
    : (rule.deviceId ? [{ kind: 'event', deviceId: rule.deviceId, eventKey: rule.eventKey, eventName: rule.eventName }] : [blankMonitor()]);
  let controls = rule.controls && rule.controls.length
    ? rule.controls
    : (rule.targetDeviceId ? [{ kind: 'device', deviceId: rule.targetDeviceId, serviceKey: rule.serviceKey, value: rule.commandValue || 0 }] : [blankControl()]);
  const draftControls = JSON.parse(JSON.stringify(controls)).map(c => {
    // 旧数据/omitempty 遗留：value 缺失或非法时归一为 0，避免编辑时保存被"指令值必须是数字"卡死。
    if (!Number.isFinite(Number(c.value))) c.value = 0;
    else c.value = Number(c.value);
    return c;
  });
  state.ruleDraft = {
    matchMode: rule.matchMode || 'anyone',
    monitors: JSON.parse(JSON.stringify(monitors)),
    controls: draftControls,
  };
  renderRuleEditor(rule);
  document.getElementById('ruleModal').querySelector('.modal-title').innerHTML = '<i class="bi bi-gear me-2" style="color:var(--primary)"></i>编辑规则';
  bootstrap.Modal.getOrCreateInstance(document.getElementById('ruleModal')).show();
}

function renderRuleEditor(rule) {
  rule = rule || {};
  setField('en-id', rule.id || genId('rule'));
  setField('en-version', rule.version || 0);
  setField('en-name', rule.name || '');
  setField('en-desc', rule.description || '');
  // 冷却时间：界面以秒为单位，存储/API 仍为毫秒（cooldownMs）。
  setField('en-cooldownMs', rule.cooldownMs != null ? rule.cooldownMs / 1000 : 10);
  setCheckbox('en-enabled', rule.enabled !== false);
  document.getElementById('en-matchMode').value = ruleDraft().matchMode;
  renderMonitors();
  renderControls();
}

function setField(id, value) { const el = document.getElementById(id); if (el) el.value = value == null ? '' : value; }
function setCheckbox(id, value) { const el = document.getElementById(id); if (el) el.checked = !!value; }

function setDraftMatchMode(mode) { ruleDraft().matchMode = mode; }

function addMonitor() { ruleDraft().monitors.push(blankMonitor()); renderMonitors(); }
function removeMonitor(index) {
  ruleDraft().monitors.splice(index, 1);
  renderMonitors();
}
function addControl() { ruleDraft().controls.push(blankControl()); renderControls(); }
function removeControl(index) {
  ruleDraft().controls.splice(index, 1);
  renderControls();
}

function setMonitorKind(index, kind) {
  const monitor = ruleDraft().monitors[index];
  if (monitor.kind === kind) return;
  monitor.kind = kind;
  // 两种监测源互斥：切换时清空另一形态的专属字段，避免脏字段落库。
  if (kind === 'level') {
    monitor.type = '';
    monitor.deviceId = '';
    monitor.eventKey = '';
    monitor.eventName = '';
    // 保留已选级别；仅从未选过时补默认 P3（不能用固定 [3] 覆盖用户选择）。
    if (!(monitor.levels || []).length) monitor.levels = [3];
  } else {
    monitor.type = 'device';
    monitor.deviceId = '';
    monitor.eventKey = '';
    monitor.eventName = '';
  }
  renderMonitors();
}

// 事件监测的监测类型（v1 仅设备；软件/系统为预留项）。
// TODO(预留)：软件/系统事件监测尚未实现——启用时需同步放开后端 Type != device 校验，
// 并在 monitorHolds 中按 category=software/system 匹配对应告警负载；当前三处互为锁死，不可达。
function setMonitorType(index, type) {
  const monitor = ruleDraft().monitors[index];
  if (monitor.type === type) return;
  monitor.type = type;
  renderMonitors();
}

function toggleMonitorLevel(index, level) {
  const monitor = ruleDraft().monitors[index];
  const pos = monitor.levels.indexOf(level);
  if (pos >= 0) monitor.levels.splice(pos, 1);
  else monitor.levels.push(level);
  renderMonitors();
}

function setMonitorDevice(index, deviceId) {
  const monitor = ruleDraft().monitors[index];
  monitor.deviceId = deviceId;
  monitor.eventKey = '';
  monitor.eventName = '';
  renderMonitors();
}

function setMonitorEvent(index, eventKey) {
  const monitor = ruleDraft().monitors[index];
  monitor.eventKey = eventKey;
  const device = findDevice(monitor.deviceId);
  const event = device && (device.events || []).find(e => e.key === eventKey);
  monitor.eventName = event ? (event.name || eventKey) : '';
}

function setControlDevice(index, deviceId) {
  const control = ruleDraft().controls[index];
  control.deviceId = deviceId;
  control.serviceKey = '';
  renderControls();
}

function setControlField(index, field, value) {
  ruleDraft().controls[index][field] = value;
  // 服务名切换后重渲染，刷新"工程值范围/合法值"提示，避免按旧范围填值。
  if (field === 'serviceKey') renderControls();
}

// ===== 下拉选项构造 =====
// 设备下拉带"请选择"占位项：避免唯一设备被浏览器默认选中却不触发 change 事件，
// 导致后续服务/事件下拉因草稿 deviceId 为空而显示"无定义"（单选项下拉陷阱）。
function deviceOptionsHTML(selected, placeholder) {
  const devices = definitionDevices();
  if (!devices.length) return '<option value="">（定义缓存为空，请先同步定义）</option>';
  const prompt = placeholder || '请选择设备';
  const options = devices.map(d =>
    `<option value="${escapeHtml(d.device.device_id)}"${d.device.device_id === selected ? ' selected' : ''}>${escapeHtml(d.device.device_name || d.device.device_id)}</option>`
  ).join('');
  return `<option value="">${prompt}</option>` + options;
}

function eventOptionsHTML(deviceId, selected) {
  const device = findDevice(deviceId);
  const events = (device && device.events) || [];
  if (!events.length) return '<option value="">（该设备无告警定义）</option>';
  const options = events.map(e => {
    const label = `${e.name || e.key}（${levelMeta(e.level).name} · ${e.type} ${e.threshold}${e.time ? ' / ' + e.time + 'ms' : ''}）`;
    return `<option value="${escapeHtml(e.key)}"${e.key === selected ? ' selected' : ''}>${escapeHtml(label)}</option>`;
  }).join('');
  // 占位项：避免首选项被浏览器默认选中却不触发 change，导致草稿 eventKey 为空（单选项下拉陷阱）。
  return '<option value="">请选择告警事件</option>' + options;
}

function methodOptionsHTML(deviceId, selected) {
  const device = findDevice(deviceId);
  const methods = (device && device.methods) || [];
  if (!methods.length) return '<option value="">（该设备无服务定义）</option>';
  const options = methods.map(m =>
    `<option value="${escapeHtml(m.key)}"${m.key === selected ? ' selected' : ''}${m.bound ? '' : ' disabled'}>${escapeHtml(m.name || m.key)}${m.bound ? '' : '（未绑定，不可控）'}</option>`
  ).join('');
  // 占位项：同事件下拉，避免草稿 serviceKey 为空导致保存校验失败。
  return '<option value="">请选择服务名</option>' + options;
}

function methodValueHint(deviceId, serviceKey) {
  const device = findDevice(deviceId);
  const method = device && (device.methods || []).find(m => m.key === serviceKey);
  if (!method) return '';
  if (method.type === 'number' && method.validation) {
    const min = method.validation.min != null ? method.validation.min : '−∞';
    const max = method.validation.max != null ? method.validation.max : '+∞';
    return `工程值范围：${min} ~ ${max}`;
  }
  if (method.type === 'status' && Array.isArray(method.descriptions)) {
    return '合法值：' + method.descriptions.map(d => `${d.name}(${d.value})`).join('、');
  }
  return '';
}

// ===== 源卡片渲染 =====
function monitorCardHTML(monitor, index) {
  const kindSelect = `<select class="form-select form-select-sm" style="width:auto" onchange="setMonitorKind(${index}, this.value)">
      <option value="level"${monitor.kind === 'level' ? ' selected' : ''}>告警级别</option>
      <option value="event"${monitor.kind === 'event' ? ' selected' : ''}>告警事件</option>
    </select>`;
  let body = '';
  if (monitor.kind === 'event') {
    // 监测类型：v1 仅设备可用，软件/系统预留（禁用占位）。
    const typeSelect = `<select class="form-select form-select-sm" onchange="setMonitorType(${index}, this.value)">
        <option value="device"${!monitor.type || monitor.type === 'device' ? ' selected' : ''}>设备</option>
        <option value="software" disabled title="暂不支持">软件（暂不支持）</option>
        <option value="system" disabled title="暂不支持">系统（暂不支持）</option>
      </select>`;
    body = `
      <div class="row g-2">
        <div class="col-md-3">
          <label class="form-label small fw-semibold">监测类型 <span class="text-danger">*</span></label>
          ${typeSelect}
        </div>
        <div class="col-md-4">
          <label class="form-label small fw-semibold">触发设备 <span class="text-danger">*</span></label>
          <select class="form-select form-select-sm" onchange="setMonitorDevice(${index}, this.value)">${deviceOptionsHTML(monitor.deviceId, '请选择触发设备')}</select>
        </div>
        <div class="col-md-5">
          <label class="form-label small fw-semibold">告警事件 <span class="text-danger">*</span></label>
          <select class="form-select form-select-sm" onchange="setMonitorEvent(${index}, this.value)">${eventOptionsHTML(monitor.deviceId, monitor.eventKey)}</select>
        </div>
      </div>`;
  } else {
    const chips = ALARM_LEVELS.map(l => {
      const active = (monitor.levels || []).includes(l.value);
      const style = active ? `background:${l.color};color:#fff` : '';
      return `<button type="button" class="level-chip${active ? ' active' : ''}" style="${style}" onclick="toggleMonitorLevel(${index}, ${l.value})">P${l.value} ${l.name}</button>`;
    }).join(' ');
    body = `
      <div class="row g-2">
        <div class="col-12">
          <label class="form-label small fw-semibold">告警级别 <span class="text-danger">*</span>（活动告警数 &gt; 0 即成立）</label>
          <div class="d-flex flex-wrap gap-2 align-items-center" style="padding-top:2px">${chips}</div>
        </div>
      </div>`;
  }
  return `<div class="rule-source-card">
    <div class="rule-source-head">
      <span class="rule-source-title me-3">监测源 #${index + 1}</span>
      ${kindSelect}
      <button type="button" class="btn btn-outline-danger btn-sm ms-auto" onclick="removeMonitor(${index})" title="删除该监测源"><i class="bi bi-trash"></i></button>
    </div>
    ${body}
  </div>`;
}

function renderMonitors() {
  const box = document.getElementById('en-monitors');
  if (!box) return;
  const monitors = ruleDraft().monitors;
  if (!monitors.length) {
    box.innerHTML = '<div class="text-muted small py-2">尚未配置监测源，点击「添加监测源」。</div>';
    return;
  }
  box.innerHTML = monitors.map(monitorCardHTML).join('');
}

function controlCardHTML(control, index) {
  const hint = methodValueHint(control.deviceId, control.serviceKey);
  return `<div class="rule-source-card">
    <div class="rule-source-head">
      <span class="rule-source-title me-3">控制源 #${index + 1}</span>
      <label class="form-label small fw-semibold mb-0 me-2">通知类型</label>
      <select class="form-select form-select-sm" style="width:auto" onchange="setControlField(${index}, 'kind', this.value)">
        <option value="device" selected>设备</option>
        <option value="sms" disabled title="暂不支持">短信（暂不支持）</option>
        <option value="email" disabled title="暂不支持">邮件（暂不支持）</option>
      </select>
      <button type="button" class="btn btn-outline-danger btn-sm ms-auto" onclick="removeControl(${index})" title="删除该控制源"><i class="bi bi-trash"></i></button>
    </div>
    <div class="row g-2">
      <div class="col-md-4">
        <label class="form-label small fw-semibold">设备名 <span class="text-danger">*</span></label>
        <select class="form-select form-select-sm" onchange="setControlDevice(${index}, this.value)">${deviceOptionsHTML(control.deviceId, '请选择设备名')}</select>
      </div>
      <div class="col-md-4">
        <label class="form-label small fw-semibold">服务名 <span class="text-danger">*</span></label>
        <select class="form-select form-select-sm" onchange="setControlField(${index}, 'serviceKey', this.value)">${methodOptionsHTML(control.deviceId, control.serviceKey)}</select>
      </div>
      <div class="col-md-4">
        <label class="form-label small fw-semibold">值/状态 <span class="text-danger">*</span></label>
        <input type="number" step="any" class="form-control form-control-sm" value="${Number.isFinite(Number(control.value)) ? Number(control.value) : 0}" onchange="setControlField(${index}, 'value', Number(this.value))">
        ${hint ? `<div class="form-text">${escapeHtml(hint)}</div>` : ''}
      </div>
    </div>
  </div>`;
}

function renderControls() {
  const box = document.getElementById('en-controls');
  if (!box) return;
  const controls = ruleDraft().controls;
  if (!controls.length) {
    box.innerHTML = '<div class="text-muted small py-2">尚未配置控制源，点击「添加控制源」。</div>';
    return;
  }
  box.innerHTML = controls.map(controlCardHTML).join('');
}

// ===== 保存 =====
// 保存在途标志：防止双击/重复触发导致"一成功一 409/500"的假象。
let ruleSaving = false;

async function saveRule() {
  if (ruleSaving) return;
  ruleSaving = true;
  try {
    const draft = ruleDraft();
    const name = document.getElementById('en-name').value.trim();
    if (!name) throw new Error('规则名称不能为空');
    if (!draft.monitors.length) throw new Error('至少配置一个监测源');
    for (const monitor of draft.monitors) {
      if (monitor.kind === 'level' && !(monitor.levels || []).length) throw new Error('级别监测源必须选择告警级别');
      if (monitor.kind === 'event') {
        // 监测类型：v1 仅设备（软件/系统下拉已禁用，防御性兜底）。
        if (monitor.type && monitor.type !== 'device') throw new Error('事件监测暂仅支持设备类型（软件/系统预留）');
        if (!monitor.deviceId || !monitor.eventKey) throw new Error('事件监测源必须选择触发设备与告警事件');
        // 定义缓存存在时校验事件仍有效（设备被重配置后旧 eventKey 可能已消失）。
        const device = findDevice(monitor.deviceId);
        if (device && !(device.events || []).some(e => e.key === monitor.eventKey)) {
          throw new Error('所选告警事件已不在定义缓存中，请重新选择');
        }
      }
    }
    // all 聚合要求所有源在同一条消息上同时成立；多个事件源逻辑上永不成立，提前拦截。
    if (draft.matchMode === 'all' && draft.monitors.filter(m => m.kind === 'event').length > 1) {
      throw new Error('全部满足（all）模式下最多允许一个事件监测源');
    }
    if (!draft.controls.length) throw new Error('至少配置一个控制源');
    for (const control of draft.controls) {
      if (!control.deviceId || !control.serviceKey) throw new Error('设备控制源必须选择设备名与服务名');
      if (!Number.isFinite(Number(control.value))) throw new Error('指令值必须是数字');
      const device = findDevice(control.deviceId);
      if (device) {
        const method = (device.methods || []).find(m => m.key === control.serviceKey);
        if (!method) throw new Error('所选控制服务已不在定义缓存中，请重新选择');
        if (!method.bound) throw new Error('所选服务未绑定目标点位，不可控制');
        if (method.type === 'number' && method.validation) {
          const value = Number(control.value);
          if (method.validation.min != null && value < method.validation.min) throw new Error(`指令值低于服务范围（最小 ${method.validation.min}）`);
          if (method.validation.max != null && value > method.validation.max) throw new Error(`指令值高于服务范围（最大 ${method.validation.max}）`);
        }
      }
    }
    // 冷却时间：秒 → 毫秒（存储/API 单位），校验有限值与上限（API 侧上限 24h）。
    const cooldownSec = Number(document.getElementById('en-cooldownMs').value);
    if (!Number.isFinite(cooldownSec) || cooldownSec < 0) throw new Error('冷却时间必须是非负数字（秒）');
    if (cooldownSec > 86400) throw new Error('冷却时间不能超过 86400 秒（24 小时）');
    const payload = {
      id: document.getElementById('en-id').value.trim(),
      version: Number(document.getElementById('en-version').value) || 0,
      name,
      description: document.getElementById('en-desc').value.trim(),
      enabled: document.getElementById('en-enabled').checked,
      matchMode: draft.matchMode,
      monitors: draft.monitors,
      controls: draft.controls,
      cooldownMs: Math.round(cooldownSec * 1000),
    };
    await RulesAPI.save(payload);
    bootstrap.Modal.getInstance(document.getElementById('ruleModal')).hide();
    await loadRules();
    renderRules();
  } catch (e) {
    // 乐观锁冲突/并发修改：载入最新版本号回填，避免 Modal 陷入"永远 409"。
    const ruleId = document.getElementById('en-id').value.trim();
    try {
      await loadRules();
      const latest = state.rules.find(r => r.id === ruleId);
      if (latest) setField('en-version', latest.version || 0);
      renderRules();
    } catch (_) { /* 刷新失败不覆盖原始错误 */ }
    toast('保存规则失败：' + e.message, 'error');
  } finally {
    ruleSaving = false;
  }
}

async function deleteRule(id) {
  if (!confirm('确认删除该规则？')) return;
  try {
    await RulesAPI.remove(id);
    await loadRules();
    renderRules();
  } catch (e) {
    toast('删除规则失败：' + e.message, 'error');
  }
}

