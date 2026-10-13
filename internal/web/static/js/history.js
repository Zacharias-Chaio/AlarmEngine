// history.js — 历史告警模块

const charts = {};

async function loadHistory() {
  try {
    const res = await HistoryAPI.list(state.historyFilters);
    state.history = res.list || [];
    state.historyTotal = res.total || 0;
  } catch (e) {
    state.history = [];
    state.historyTotal = 0;
  }
}

async function loadStatistics() {
  try {
    state.statistics = await HistoryAPI.statistics({ granularity: state.trendGranularity });
  } catch (e) {
    state.statistics = null;
  }
}

async function refreshHistory() {
  await Promise.all([loadHistory(), loadStatistics()]);
  // 当前页被清空时回退一页重查
  if (!state.history.length && state.historyFilters.page > 1) {
    state.historyFilters.page--;
    await loadHistory();
  }
  renderHistory();
}

function gotoHistoryPage(page) {
  state.historyFilters.page = page;
  refreshHistory();
}

function renderHistory() {
  renderHistoryFilters();
  renderHistoryTable();
  renderStatistics();
}

function renderHistoryFilters() {
  const severity = document.getElementById('hi-filter-severity');
  if (severity && !severity.dataset.inited) {
    severity.innerHTML = '<option value="">全部</option>' + ALARM_LEVELS.map(l => `<option value="${l.value}">${l.name}</option>`).join('');
    severity.dataset.inited = '1';
  }
  if (severity) severity.value = state.historyFilters.severity || '';

  const type = document.getElementById('hi-filter-type');
  if (type && !type.dataset.inited) {
    type.innerHTML = '<option value="">全部</option>' + ALARM_TYPES.map(t => `<option value="${t.value}">${t.name}</option>`).join('');
    type.dataset.inited = '1';
  }
  if (type) type.value = state.historyFilters.type || '';

  const source = document.getElementById('hi-filter-source');
  if (source) {
    // 累积历史查询见过的来源，避免设置筛选后下拉选项塌缩。
    const seen = [...new Set(state.history.map(a => a.source).filter(Boolean))];
    state.historySourceOptions = [...new Set([...state.historySourceOptions, ...seen])]
      .sort((a, b) => a.localeCompare(b, 'zh-CN'));
    source.innerHTML = '<option value="">全部</option>' + state.historySourceOptions.map(s => `<option value="${escapeHtml(s)}">${escapeHtml(s)}</option>`).join('');
    source.value = state.historyFilters.source || '';
  }

  const handled = document.getElementById('hi-filter-handled');
  if (handled) handled.value = state.historyFilters.handled || '';
}

function renderHistoryTable() {
  const tbody = document.getElementById('hi-tbody');
  const count = document.getElementById('hi-count');
  if (count) count.textContent = state.historyTotal + ' 条';
  const pagination = document.getElementById('hi-pagination');
  if (pagination) pagination.innerHTML = paginationHtml(state.historyTotal, state.historyFilters.page || 1, PAGE_SIZE, 'gotoHistoryPage');
  if (!tbody) return;
  if (!state.history.length) {
    tbody.innerHTML = '<tr><td colspan="9" class="empty-state-row">暂无历史告警记录</td></tr>';
    return;
  }
  tbody.innerHTML = state.history.map(a => {
    const lm = levelMeta(a.severity);
    const sm = displayStateMeta(a);
    const tm = typeMeta(a.type);
    const closedAt = a.clearedAt || a.recoveredAt;
    return `<tr class="clickable" onclick="openAlarmDetail('${escapeHtml(a.id)}', false)" title="点击查看告警详情">
      <td><span class="alarm-level ${lm.cls}">${lm.name}</span></td>
      <td><span class="type-badge" style="background:${tm.color}1a;color:${tm.color}"><i class="bi ${tm.icon} me-1"></i>${tm.name}</span></td>
      <td>${escapeHtml(a.source || '—')}</td>
      <td><code>${escapeHtml(a.point || '—')}</code></td>
      <td class="fw-semibold">${escapeHtml(a.name)}</td>
      <td>${escapeHtml(fmtTime(a.occurredAt))}</td>
      <td>${escapeHtml(fmtTime(closedAt))}</td>
      <td>${escapeHtml(fmtDuration(a.durationMs))}</td>
      <td><span class="status-badge ${sm.cls}">${sm.name}</span></td>
    </tr>`;
  }).join('');
}

function renderStatistics() {
  const stats = state.statistics;
  if (!stats) return;
  renderSeverityChart(stats.severity);
  renderSourceChart(stats.sources);
  renderTrendChart(stats.trend);
  renderTopList(stats.top);
}

function renderChart(id, type, config) {
  if (typeof Chart === 'undefined') return;
  const canvas = document.getElementById(id);
  if (!canvas) return;
  if (charts[id]) { charts[id].destroy(); }
  charts[id] = new Chart(canvas, {
    type: type,
    data: config,
    options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { position: 'bottom' } } },
  });
}

function renderSeverityChart(severity) {
  renderChart('hi-chart-severity', 'doughnut', {
    labels: severity.map(x => x.name),
    datasets: [{
      data: severity.map(x => x.value),
      backgroundColor: ['#3b82f6', '#f59e0b', '#ea580c', '#dc2626'],
      borderWidth: 2,
      borderColor: '#fff',
    }],
  });
}

function renderSourceChart(sources) {
  renderChart('hi-chart-source', 'bar', {
    labels: sources.map(x => x.name),
    datasets: [{
      label: '告警数',
      data: sources.map(x => x.value),
      backgroundColor: '#4f46e5',
      borderRadius: 6,
    }],
  });
}

function renderTrendChart(trend) {
  renderChart('hi-chart-trend', 'line', {
    labels: trend.map(x => x.label),
    datasets: [{
      label: '告警趋势',
      data: trend.map(x => x.value),
      borderColor: '#4f46e5',
      backgroundColor: 'rgba(79,70,229,.15)',
      fill: true,
      tension: 0.3,
      pointRadius: 3,
    }],
  });
}

function renderTopList(top) {
  const list = document.getElementById('hi-top-list');
  if (!list) return;
  if (!top || !top.length) {
    list.innerHTML = '<div class="empty-state"><i class="bi bi-inboxes"></i><div>暂无排行数据</div></div>';
    return;
  }
  list.innerHTML = top.map((g, i) => {
    const children = (g.children && g.children.length)
      ? g.children.map(c => `<li class="top-child"><span class="rank">·</span><span class="name">${escapeHtml(c.name)}</span><span class="num">${c.value}</span></li>`).join('')
      : '';
    return `<li class="top-group"><span class="rank">${i + 1}</span><span class="name">${escapeHtml(g.name)}</span><span class="num">${g.value}</span></li>${children}`;
  }).join('');
}

function setTrendGranularity(g) {
  state.trendGranularity = g;
  document.querySelectorAll('[data-g]').forEach(b => b.classList.toggle('active', b.dataset.g === g));
  loadStatistics().then(renderStatistics);
}

function applyHistoryFilters() {
  const start = document.getElementById('hi-filter-start').value;
  const end = document.getElementById('hi-filter-end').value;
  state.historyFilters.start = start ? new Date(start).toISOString() : '';
  state.historyFilters.end = end ? new Date(end).toISOString() : '';
  state.historyFilters.type = document.getElementById('hi-filter-type').value;
  state.historyFilters.source = document.getElementById('hi-filter-source').value;
  state.historyFilters.severity = document.getElementById('hi-filter-severity').value;
  state.historyFilters.handled = document.getElementById('hi-filter-handled').value;
  state.historyFilters.page = 1; // 筛选变更后回到第一页
  refreshHistory();
}

function resetHistoryFilters() {
  state.historyFilters = { start: '', end: '', type: '', source: '', severity: '', handled: '', page: 1 };
  ['hi-filter-start', 'hi-filter-end', 'hi-filter-handled'].forEach(id => {
    const el = document.getElementById(id);
    if (el) el.value = '';
  });
  refreshHistory();
}