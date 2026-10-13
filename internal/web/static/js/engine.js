// engine.js — 告警引擎（保护动作规则）模块

async function loadRules() {
  try {
    state.rules = await RulesAPI.list();
  } catch (e) {
    state.rules = [];
  }
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
  grid.innerHTML = state.rules.map(r => {
    const lm = levelMeta(r.severity);
    const op = operatorMeta(r.operator);
    const at = actionTypeMeta(r.actionType);
    const condition = `${r.deviceId || '任意设备'}.${r.propertyKey || '—'} ${op.name} ${r.threshold}${r.durationMs ? '（持续 ' + r.durationMs + 'ms）' : ''}`;
    const action = at.name + (r.targetDeviceId ? ` → ${r.targetDeviceId}.${r.command || '—'} = ${r.commandValue}` : '');
    return `<div class="model-card">
      <div class="model-card-top">
        <div class="model-card-icon"><i class="bi bi-gear"></i></div>
        <div class="min-w-0">
          <div class="model-card-title">${escapeHtml(r.name)} <span class="alarm-level ${lm.cls}">${lm.name}</span></div>
          <div class="model-card-sub">${escapeHtml(r.id)}</div>
        </div>
        <span class="status-badge ${r.enabled ? 'enabled' : 'disabled'} ms-auto">${r.enabled ? '启用' : '停用'}</span>
      </div>
      <div class="model-card-desc">${escapeHtml(r.description || '—')}</div>
      <div class="rule-brief">
        <div><i class="bi bi-sliders me-1"></i>条件：<code>${escapeHtml(condition)}</code></div>
        <div><i class="bi bi-lightning-charge me-1"></i>动作：<code>${escapeHtml(action)}</code></div>
      </div>
      <div class="model-card-actions">
        <button class="btn btn-outline-secondary btn-sm" onclick="editRule('${escapeHtml(r.id)}')"><i class="bi bi-pencil me-1"></i>编辑</button>
        <button class="btn btn-outline-danger btn-sm" onclick="deleteRule('${escapeHtml(r.id)}')"><i class="bi bi-trash"></i></button>
      </div>
    </div>`;
  }).join('');
}

function newRule() {
  state.editingRuleId = null;
  resetRuleForm();
  document.getElementById('ruleModal').querySelector('.modal-title').innerHTML = '<i class="bi bi-gear me-2" style="color:var(--primary)"></i>新建规则';
  new bootstrap.Modal(document.getElementById('ruleModal')).show();
}

function editRule(id) {
  const rule = state.rules.find(r => r.id === id);
  if (!rule) return;
  state.editingRuleId = id;
  fillRuleForm(rule);
  document.getElementById('ruleModal').querySelector('.modal-title').innerHTML = '<i class="bi bi-gear me-2" style="color:var(--primary)"></i>编辑规则';
  new bootstrap.Modal(document.getElementById('ruleModal')).show();
}

function resetRuleForm() {
  const fields = {
    'en-id': genId('rule'),
    'en-name': '',
    'en-desc': '',
    'en-enabled': true,
    'en-deviceId': '',
    'en-propertyKey': '',
    'en-threshold': 0,
    'en-durationMs': 0,
    'en-targetDeviceId': '',
    'en-command': '',
    'en-commandValue': 0,
  };
  Object.entries(fields).forEach(([id, value]) => {
    const el = document.getElementById(id);
    if (!el) return;
    if (el.type === 'checkbox') el.checked = value;
    else el.value = value;
  });
  fillSelectOptions();
}

function fillSelectOptions() {
  const severity = document.getElementById('en-severity');
  if (severity && !severity.options.length) {
    severity.innerHTML = ALARM_LEVELS.map(l => `<option value="${l.value}">${l.name}（P${l.value}）</option>`).join('');
  }
  const operator = document.getElementById('en-operator');
  if (operator && !operator.options.length) {
    operator.innerHTML = RULE_OPERATORS.map(o => `<option value="${o.value}">${o.name}</option>`).join('');
  }
  const actionType = document.getElementById('en-actionType');
  if (actionType && !actionType.options.length) {
    actionType.innerHTML = ACTION_TYPES.map(a => `<option value="${a.value}">${a.name}</option>`).join('');
  }
}

function fillRuleForm(rule) {
  resetRuleForm();
  const map = {
    'en-id': rule.id,
    'en-name': rule.name,
    'en-desc': rule.description,
    'en-enabled': !!rule.enabled,
    'en-severity': rule.severity,
    'en-deviceId': rule.deviceId,
    'en-propertyKey': rule.propertyKey,
    'en-operator': rule.operator,
    'en-threshold': rule.threshold,
    'en-durationMs': rule.durationMs,
    'en-actionType': rule.actionType,
    'en-targetDeviceId': rule.targetDeviceId,
    'en-command': rule.command,
    'en-commandValue': rule.commandValue,
  };
  Object.entries(map).forEach(([id, value]) => {
    const el = document.getElementById(id);
    if (!el) return;
    if (el.type === 'checkbox') el.checked = value;
    else el.value = value;
  });
}

async function saveRule() {
  try {
    const payload = {
      id: document.getElementById('en-id').value.trim(),
      name: document.getElementById('en-name').value.trim(),
      description: document.getElementById('en-desc').value.trim(),
      enabled: document.getElementById('en-enabled').checked,
      severity: Number(document.getElementById('en-severity').value),
      deviceId: document.getElementById('en-deviceId').value.trim(),
      propertyKey: document.getElementById('en-propertyKey').value.trim(),
      operator: document.getElementById('en-operator').value,
      threshold: Number(document.getElementById('en-threshold').value),
      durationMs: Number(document.getElementById('en-durationMs').value) || 0,
      actionType: document.getElementById('en-actionType').value,
      targetDeviceId: document.getElementById('en-targetDeviceId').value.trim(),
      command: document.getElementById('en-command').value.trim(),
      commandValue: Number(document.getElementById('en-commandValue').value) || 0,
    };
    if (!payload.name) throw new Error('规则名称不能为空');
    await RulesAPI.save(payload);
    bootstrap.Modal.getInstance(document.getElementById('ruleModal')).hide();
    await loadRules();
    renderRules();
  } catch (e) {
    toast('保存规则失败：' + e.message, 'error');
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
