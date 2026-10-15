// alarms.js — 告警信息模块

// 今日趋势迷你图实例（模块级，避免重复创建）。
let todayChart = null;

async function loadAlarms() {
  try {
    const res = await AlarmsAPI.list(state.alarmFilters);
    state.alarms = res.list || [];
    state.alarmTotal = res.total || 0;
  } catch (e) {
    state.alarms = [];
    state.alarmTotal = 0;
  }
}

async function loadAlarmSummary() {
  try {
    state.alarmSummary = await AlarmsAPI.summary();
  } catch (e) {
    state.alarmSummary = null;
  }
}

async function refreshAlarms() {
  await Promise.all([loadAlarms(), loadAlarmSummary()]);
  // 结案等操作可能清空当前页：回退一页重查
  if (!state.alarms.length && state.alarmFilters.page > 1) {
    state.alarmFilters.page--;
    await loadAlarms();
  }
  renderAlarms();
}

function gotoAlarmPage(page) {
  state.alarmFilters.page = page;
  refreshAlarms();
}

function renderAlarms() {
  renderAlarmSummary();
  renderAlarmList();
  renderAlarmFilters();
}

// ===== 顶部全局统计：级别卡（活动中/未确认/未处理）+ 今日卡（总数 + 24h 趋势 + 环比） =====
function renderAlarmSummary() {
  const s = state.alarmSummary || { bySeverity: {}, bySource: {}, today: {} };
  const grid = document.getElementById('al-summary-grid');
  if (!grid) return;
  const cards = [];
  [3, 2, 1, 0].forEach(sev => {
    const l = levelMeta(sev);
    const st = (s.bySeverity && s.bySeverity[sev]) || {};
    cards.push(`
      <div class="summary-card severity-summary">
        <div class="summary-icon" style="background:${l.color}1a;color:${l.color}"><i class="bi bi-flag"></i></div>
        <div class="severity-summary-body">
          <div class="severity-summary-head">
            <span class="val" style="color:${l.color}">${st.unhandled || 0}</span>
            <span class="lbl">${l.name}告警</span>
          </div>
          <div class="substats">
            <span title="触发持续中（非孤儿）">活动中 <b>${st.active || 0}</b></span>
            <span title="触发持续中且未人工确认">未确认 <b>${st.unacked || 0}</b></span>
            <span title="未结案">未处理 <b>${st.unhandled || 0}</b></span>
          </div>
        </div>
      </div>`);
  });
  cards.push(todayCardHtml(s.today || {}));
  grid.innerHTML = cards.join('');
  renderTodayChart(s.today || {});
}

function todayCardHtml(t) {
  let pctHtml = '<span class="trend-pct muted">较昨日 —</span>';
  if (t.changePct !== null && t.changePct !== undefined) {
    const up = t.changePct >= 0;
    const icon = up ? 'bi-arrow-up-right' : 'bi-arrow-down-right';
    const cls = up ? 'up' : 'down';
    pctHtml = `<span class="trend-pct ${cls}"><i class="bi ${icon}"></i>${Math.abs(t.changePct)}% 较昨日同期</span>`;
  }
  return `
    <div class="summary-card today-summary">
      <div class="summary-icon" style="background:#4f46e51a;color:#4f46e5"><i class="bi bi-calendar-day"></i></div>
      <div class="today-summary-body">
        <div class="severity-summary-head">
          <span class="val" style="color:#111827">${t.count || 0}</span>
          <span class="lbl">今日告警</span>
          ${pctHtml}
        </div>
        <div class="sparkline-box"><canvas id="al-today-chart"></canvas></div>
      </div>
    </div>`;
}

function renderTodayChart(t) {
  if (typeof Chart === 'undefined' || !t || !t.hours || !t.hours.length) return;
  const canvas = document.getElementById('al-today-chart');
  if (!canvas) return;
  if (todayChart) todayChart.destroy();
  todayChart = new Chart(canvas, {
    type: 'line',
    data: {
      labels: t.hours.map(h => h.label),
      datasets: [{
        data: t.hours.map(h => h.value),
        borderColor: '#4f46e5',
        backgroundColor: 'rgba(79,70,229,.12)',
        fill: true,
        tension: 0.35,
        pointRadius: 0,
        borderWidth: 1.5,
      }],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: { legend: { display: false }, tooltip: { displayColors: false } },
      scales: { x: { display: false }, y: { display: false, beginAtZero: true } },
    },
  });
}

// ===== 告警列表 =====
function renderAlarmList() {
  const tbody = document.getElementById('al-tbody');
  const count = document.getElementById('al-count');
  if (count) count.textContent = state.alarmTotal + ' 条';
  const pagination = document.getElementById('al-pagination');
  if (pagination) pagination.innerHTML = paginationHtml(state.alarmTotal, state.alarmFilters.page || 1, PAGE_SIZE, 'gotoAlarmPage');
  renderAlarmBatchUi();
  if (!tbody) return;
  const batch = state.alarmBatch.active;
  const colCount = batch ? 11 : 10; // 批量模式多一列勾选框
  if (!state.alarms.length) {
    tbody.innerHTML = `<tr><td colspan="${colCount}" class="empty-state-row">暂无告警</td></tr>`;
    return;
  }
  // 批量模式勾选仅针对当前页可见条目：翻页/筛选后自动清理不可见勾选
  if (batch) {
    const pageIds = new Set(state.alarms.map(a => a.id));
    state.alarmBatch.selected = state.alarmBatch.selected.filter(id => pageIds.has(id));
  }
  tbody.innerHTML = state.alarms.map(a => {
    const lm = levelMeta(a.severity);
    const sm = displayStateMeta(a);
    const tm = typeMeta(a.type);
    const tags = alarmTagList(a);
    const handleTitle = a.handled ? '已处理' : (a.orphan ? '孤儿告警（触发源失联）不可关闭' : (canHandleAlarm(a) ? '处理并关闭，进入历史告警' : (a.kind === 'event' ? '一次性事件需先确认才能处理' : '活动中的告警需等待自动恢复后才能处理')));
    const checked = batch && state.alarmBatch.selected.includes(a.id);
    // 批量模式：行点击切换勾选；普通模式：行点击打开详情
    const rowAttrs = batch
      ? `class="batch-row" onclick="toggleAlarmBatchRow('${escapeHtml(a.id)}')"`
      : `class="clickable" onclick="openAlarmDetail('${escapeHtml(a.id)}')"`;
    const checkTd = batch
      ? `<td class="batch-check-col" onclick="event.stopPropagation()"><input type="checkbox" class="form-check-input al-batch-check" value="${escapeHtml(a.id)}" ${checked ? 'checked' : ''} onchange="setAlarmBatchCheck('${escapeHtml(a.id)}', this.checked)" title="勾选该告警"></td>`
      : '';
    return `<tr ${rowAttrs}>
      ${checkTd}
      <td>${escapeHtml(fmtTime(a.occurredAt))}</td>
      <td><span class="alarm-level ${lm.cls}">${lm.name}</span></td>
      <td><span class="type-badge" style="background:${tm.color}1a;color:${tm.color}"><i class="bi ${tm.icon} me-1"></i>${tm.name}</span></td>
      <td class="fw-semibold">${escapeHtml(a.name)}</td>
      <td>${renderAlarmTags(tags, a.count)}</td>
      <td>${fmtValue(a)}</td>
      <td>${fmtThreshold(a)}</td>
      <td><span class="status-badge ${sm.cls}">${sm.name}</span></td>
      <td>${escapeHtml(fmtDuration(a.durationMs))}</td>
      <td onclick="event.stopPropagation()">
        <button class="btn btn-outline-primary btn-sm" onclick="event.stopPropagation();acknowledgeAlarm('${escapeHtml(a.id)}')" ${a.acknowledged ? 'disabled' : ''} title="${a.acknowledged ? '已确认' : '确认告警，进入处理阶段'}"><i class="bi bi-check-circle"></i></button>
        <button class="btn btn-outline-success btn-sm" onclick="event.stopPropagation();handleAlarm('${escapeHtml(a.id)}')" ${canHandleAlarm(a) ? '' : 'disabled'} title="${handleTitle}"><i class="bi bi-check2-all"></i></button>
      </td>
    </tr>`;
  }).join('');
}

// ===== 告警列表批量操作（批量确认 / 批量处理） =====
// 进入批量模式：仅此时显示全选/取消/确认操作条与每行勾选框；mode 决定"确认"执行的动作。
function enterAlarmBatchMode(mode) {
  if (!state.alarms.length) {
    toast('当前列表暂无告警，无需批量操作');
    return;
  }
  state.alarmBatch = { active: true, mode, selected: [] };
  renderAlarmList();
}

// 退出批量模式：隐藏操作条与勾选框，清空选择。
function exitAlarmBatchMode() {
  state.alarmBatch = { active: false, mode: '', selected: [] };
  renderAlarmList();
}

// 同步批量模式 UI：入口按钮/操作条/表头勾选列的显隐，模式文案与已选计数。
function renderAlarmBatchUi() {
  const b = state.alarmBatch;
  const entry = document.getElementById('al-batch-entry');
  const bar = document.getElementById('al-batch-bar');
  const th = document.getElementById('al-batch-th');
  if (entry) entry.classList.toggle('d-none', b.active);
  if (bar) bar.classList.toggle('d-none', !b.active);
  if (th) th.classList.toggle('d-none', !b.active);
  if (b.active) {
    const modeEl = document.getElementById('al-batch-mode');
    if (modeEl) {
      modeEl.innerHTML = b.mode === 'handle'
        ? '<i class="bi bi-check2-all me-1"></i>批量处理'
        : '<i class="bi bi-check2-square me-1"></i>批量确认';
    }
  }
  updateAlarmBatchCount();
}

function updateAlarmBatchCount() {
  const el = document.getElementById('al-batch-selected-count');
  if (el) el.textContent = '已选 ' + state.alarmBatch.selected.length + ' 项';
}

// 勾选/取消勾选单条（勾选框 onchange）。
function setAlarmBatchCheck(id, checked) {
  const selected = state.alarmBatch.selected.filter(x => x !== id);
  if (checked) selected.push(id);
  state.alarmBatch.selected = selected;
  updateAlarmBatchCount();
}

// 批量模式下点击行任意位置切换该行勾选（点击勾选框本身除外，避免二次切换）。
function toggleAlarmBatchRow(id) {
  if (window.event && window.event.target && window.event.target.type === 'checkbox') return;
  setAlarmBatchCheck(id, !state.alarmBatch.selected.includes(id));
  syncAlarmBatchChecks();
}

// 全选/清空：当前页已全部勾选时再点一次即清空，否则勾选当前页全部。
function toggleAlarmSelectAll() {
  const b = state.alarmBatch;
  const ids = state.alarms.map(a => a.id);
  if (!ids.length) return;
  b.selected = b.selected.length === ids.length ? [] : ids;
  syncAlarmBatchChecks();
  updateAlarmBatchCount();
}

// 将勾选状态同步到当前页勾选框（不重建表格）。
function syncAlarmBatchChecks() {
  const selected = state.alarmBatch.selected;
  document.querySelectorAll('.al-batch-check').forEach(cb => {
    cb.checked = selected.includes(cb.value);
  });
}

// 执行批量操作：按模式调用批量确认/批量处理接口，完成后退出批量模式并刷新列表。
async function confirmAlarmBatch() {
  const b = state.alarmBatch;
  if (!b.active) return;
  if (!b.selected.length) {
    toast('请先勾选要操作的告警');
    return;
  }
  const action = b.mode === 'handle' ? '批量处理' : '批量确认';
  if (b.mode === 'handle') {
    if (!confirm(`确认批量处理选中的 ${b.selected.length} 条告警并关闭？处理后进入历史告警。`)) return;
  }
  try {
    const res = b.mode === 'handle'
      ? await AlarmsAPI.batchHandle(b.selected.slice())
      : await AlarmsAPI.batchAcknowledge(b.selected.slice());
    let msg = `${action}完成：成功 ${res.ok || 0} 条`;
    if (res.skipped) msg += `，跳过 ${res.skipped} 条`;
    toast(msg);
    exitAlarmBatchMode();
    await refreshAlarms();
  } catch (e) {
    toast(action + '失败：' + e.message, 'error');
  }
}

function renderAlarmTags(tags, count) {
  let html = tags.map(t => `<span class="tag-pill">${escapeHtml(t)}</span>`).join('');
  if (count && count > 1) html += `<span class="tag-pill tag-count" title="节流聚合消息，窗口内合并 ${count} 条">×${count}</span>`;
  return html || '<span class="text-muted">—</span>';
}

// 当前值/阈值（列表单元格）：统一口径见 alarmValueText/alarmThresholdText，无值时灰色占位
function fmtValue(a) {
  const text = alarmValueText(a);
  return text ? escapeHtml(text) : '<span class="text-muted">—</span>';
}

function fmtThreshold(a) {
  const text = alarmThresholdText(a);
  return text ? escapeHtml(text) : '<span class="text-muted">—</span>';
}

// ===== 筛选区 =====
function renderAlarmFilters() {
  const severity = document.getElementById('al-filter-severity');
  if (severity && !severity.dataset.inited) {
    severity.innerHTML = '<option value="">全部</option>' + ALARM_LEVELS.map(l => `<option value="${l.value}">${l.name}</option>`).join('');
    severity.dataset.inited = '1';
  }
  if (severity) severity.value = state.alarmFilters.severity || '';

  const type = document.getElementById('al-filter-type');
  if (type && !type.dataset.inited) {
    type.innerHTML = '<option value="">全部</option>' + ALARM_TYPES.map(t => `<option value="${t.value}">${t.name}</option>`).join('');
    type.dataset.inited = '1';
  }
  if (type) type.value = state.alarmFilters.type || '';

  const stateEl = document.getElementById('al-filter-state');
  if (stateEl && !stateEl.dataset.inited) {
    stateEl.innerHTML = '<option value="">全部</option>' + ALARM_STATE_FILTERS.map(s => `<option value="${s.key}">${s.name}</option>`).join('');
    stateEl.dataset.inited = '1';
  }
  if (stateEl) stateEl.value = state.alarmFilters.state || '';

  // 来源选项取自全局统计（全部未结案来源），不随当前列表筛选塌缩
  const source = document.getElementById('al-filter-source');
  if (source) {
    const s = state.alarmSummary;
    const sources = s && s.bySource ? Object.keys(s.bySource).filter(Boolean).sort((a, b) => a.localeCompare(b, 'zh-CN')) : [];
    source.innerHTML = '<option value="">全部</option>' + sources.map(x => `<option value="${escapeHtml(x)}">${escapeHtml(x)}</option>`).join('');
    source.value = state.alarmFilters.source || '';
  }

  const sort = document.getElementById('al-filter-sort');
  if (sort) sort.value = state.alarmFilters.sort || 'time';
}

function applyAlarmFilters() {
  state.alarmFilters.severity = document.getElementById('al-filter-severity').value;
  state.alarmFilters.type = document.getElementById('al-filter-type').value;
  state.alarmFilters.state = document.getElementById('al-filter-state').value;
  state.alarmFilters.source = document.getElementById('al-filter-source').value;
  state.alarmFilters.sort = document.getElementById('al-filter-sort').value;
  state.alarmFilters.page = 1; // 筛选变更后回到第一页
  refreshAlarms();
}

function resetAlarmFilters() {
  state.alarmFilters = { severity: '', type: '', source: '', state: '', sort: 'time', page: 1 };
  refreshAlarms();
}

// ===== 告警详情抽屉 =====
// showActions：是否显示确认/处理操作按钮（告警信息列表 true；历史告警仅查看，false）。
async function openAlarmDetail(id, showActions = true) {
  // 防御：来自按钮的点击不打开详情（确认/处理操作不应触发详情弹层）
  if (window.event && window.event.target && window.event.target.closest && window.event.target.closest('button')) return;
  try {
    const alarm = await AlarmsAPI.get(id);
    state.selectedAlarmId = id;
    const lm = levelMeta(alarm.severity);
    const sm = displayStateMeta(alarm);
    const tm = typeMeta(alarm.type);
    const title = document.getElementById('al-drawer-title');
    const sub = document.getElementById('al-drawer-sub');
    const body = document.getElementById('al-drawer-body');
    if (title) title.textContent = alarm.name;
    if (sub) sub.innerHTML = `<span class="alarm-level ${lm.cls} me-2">${lm.name}</span><span class="status-badge ${sm.cls} me-2">${sm.name}</span>${escapeHtml(alarm.id)}`;
    if (body) {
      body.innerHTML = `
        <dl class="m-0">
          ${detailRow('告警类型', '<span class="type-badge" style="background:' + tm.color + '1a;color:' + tm.color + '"><i class="bi ' + tm.icon + ' me-1"></i>' + tm.name + '</span>')}
          ${detailRow('生命周期', alarm.kind === 'event' ? '一次性事件' : '状态型告警')}
          ${detailRow('告警来源', escapeHtml(alarm.source || '—'))}
          ${detailRow('来源模块', escapeHtml((alarm.module || '—') + (alarm.instance ? ' / ' + alarm.instance : '')))}
          ${alarm.type === 'device' ? detailRow('告警点位', escapeHtml(alarm.point || '—')) : ''}
          ${alarm.type === 'device' ? detailRow('设备', escapeHtml((alarm.deviceName || '—') + (alarm.deviceId ? ' (' + alarm.deviceId + ')' : ''))) : ''}
          ${alarm.type === 'software' ? detailRow('软件实例', escapeHtml(alarm.softwareId || '—')) : ''}
          ${alarm.type !== 'device' ? detailRow('事件码', escapeHtml(alarm.code || '—')) : ''}
          ${alarm.type !== 'device' ? detailRow('组件', escapeHtml(alarm.component || '—')) : ''}
          ${detailRow('告警标签', renderAlarmTags(alarmTagList(alarm), alarm.count))}
          ${detailRow('告警消息', escapeHtml(alarm.message || '—'))}
          ${detailRow('当前值', escapeHtml(alarmValueText(alarm)) || '—')}
          ${detailRow('告警阈值', escapeHtml(alarmThresholdText(alarm)) || '—')}
          ${detailRow('发生时间', fmtTime(alarm.occurredAt))}
          ${detailRow('持续时间', fmtDuration(alarm.durationMs))}
          ${detailRow('确认时间', fmtTime(alarm.acknowledgedAt))}
          ${detailRow('恢复时间', fmtTime(alarm.recoveredAt))}
          ${detailRow('处理时间', fmtTime(alarm.clearedAt))}
        </dl>
        ${showActions ? `
        <div class="drawer-actions">
          <button class="btn btn-outline-primary" onclick="acknowledgeAlarm('${escapeHtml(alarm.id)}')" ${alarm.acknowledged || alarm.handled ? 'disabled' : ''}><i class="bi bi-check-circle me-1"></i>确认告警</button>
          <button class="btn btn-success" onclick="handleAlarm('${escapeHtml(alarm.id)}')" ${canHandleAlarm(alarm) ? '' : 'disabled'}><i class="bi bi-check2-all me-1"></i>处理结案</button>
        </div>` : ''}
        ${alarm.orphan ? '<div class="alert alert-warning py-2 small mb-0 mt-3"><i class="bi bi-slash-circle me-1"></i>孤儿告警：触发源因配置变更或设备离线等原因失联，恢复来源监测并收到恢复消息后才可处理结案。</div>' : ''}
        ${showActions && canHandleAlarm(alarm) && alarm.state === 'active' && alarm.kind === 'event' ? '<div class="alert alert-info py-2 small mb-0 mt-3"><i class="bi bi-info-circle me-1"></i>一次性事件告警：已确认，可直接处理结案（视为即时恢复）。</div>' : ''}`;
    }
    document.getElementById('al-drawer').classList.add('open');
    document.getElementById('al-drawer-backdrop').classList.remove('d-none');
  } catch (e) {
    toast('加载告警详情失败：' + e.message, 'error');
  }
}

function detailRow(label, valueHtml) {
  return `<div class="detail-row"><dt>${escapeHtml(label)}</dt><dd>${valueHtml}</dd></div>`;
}

function closeAlarmDetail() {
  document.getElementById('al-drawer').classList.remove('open');
  document.getElementById('al-drawer-backdrop').classList.add('d-none');
  state.selectedAlarmId = null;
}

// 详情抽屉可能从"告警信息"或"历史告警"区块打开：操作后刷新当前所在区块。
function refreshCurrentSection() {
  const historyEl = document.getElementById('section-history');
  const inHistory = historyEl && !historyEl.classList.contains('d-none');
  return inHistory ? refreshHistory() : refreshAlarms();
}

async function acknowledgeAlarm(id) {
  try {
    await AlarmsAPI.acknowledge(id);
    await refreshCurrentSection();
    // 仅当详情抽屉正打开且展示的是该告警时，原地刷新抽屉内容（不新弹）
    const drawer = document.getElementById('al-drawer');
    if (state.selectedAlarmId === id && drawer && drawer.classList.contains('open')) await openAlarmDetail(id);
  } catch (e) {
    toast('确认告警失败：' + e.message, 'error');
  }
}

async function handleAlarm(id) {
  if (!confirm('确认处理该告警并关闭？处理后进入历史告警。')) return;
  try {
    await AlarmsAPI.handle(id);
    closeAlarmDetail();
    await refreshCurrentSection();
  } catch (e) {
    toast('处理告警失败：' + e.message, 'error');
  }
}
