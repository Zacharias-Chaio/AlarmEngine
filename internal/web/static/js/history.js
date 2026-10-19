// history.js — 历史记录模块（筛选 + 分页列表 + CSV 导出；详情查看复用告警抽屉）

async function loadHistory() {
  try {
    const res = await HistoryAPI.list(state.historyFilters);
    state.history = res.list || [];
    state.historyTotal = res.total || 0;
  } catch (e) {
    state.history = [];
    state.historyTotal = 0;
    reportLoadError(e, '历史记录');
  }
}

async function refreshHistory() {
  setSectionLoading('history', true);
  try {
  await loadHistory();
  // 当前页被清空时回退一页重查
  if (!state.history.length && state.historyFilters.page > 1) {
    state.historyFilters.page--;
    await loadHistory();
  }
  renderHistory();
  } finally { setSectionLoading('history', false); }
}

function gotoHistoryPage(page) {
  state.historyFilters.page = page;
  refreshHistory();
}

function renderHistory() {
  renderHistoryFilters();
  renderHistoryTable();
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
    return `<tr class="clickable" data-alarm-id="${escapeHtml(a.id)}" onclick="openAlarmDetail(event, this.dataset.alarmId, false)" title="点击查看告警详情">
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
  ['hi-filter-start', 'hi-filter-end', 'hi-filter-type', 'hi-filter-source', 'hi-filter-severity', 'hi-filter-handled'].forEach(id => {
    const el = document.getElementById(id);
    if (el) el.value = '';
  });
  refreshHistory();
}

// ===== CSV 导出：按当前筛选条件导出全部匹配记录（跨页拉取） =====

// 导出上限页数（pageSize=100 × 50 页 = 5000 条），防止异常数据量拖垮浏览器。
const EXPORT_MAX_PAGES = 50;
const EXPORT_PAGE_SIZE = 100;

async function exportHistory() {
  const btn = document.getElementById('hi-export');
  const original = btn ? btn.innerHTML : '';
  if (btn) { btn.disabled = true; btn.innerHTML = '<span class="spinner-border spinner-border-sm me-1"></span>导出中'; }
  try {
    const filters = { ...state.historyFilters };
    const rows = [];
    for (let page = 1; page <= EXPORT_MAX_PAGES; page++) {
      filters.page = page;
      filters.pageSize = EXPORT_PAGE_SIZE;
      const res = await HistoryAPI.list(filters);
      rows.push(...(res.list || []));
      if (rows.length >= (res.total || 0)) break;
    }
    if (!rows.length) {
      toast('当前筛选条件下没有可导出的记录');
      return;
    }
    downloadHistoryCsv(rows);
    toast(`已导出 ${rows.length} 条历史告警记录`);
  } catch (e) {
    toast('导出失败：' + e.message, 'error');
  } finally {
    if (btn) { btn.disabled = false; btn.innerHTML = original; }
  }
}

function downloadHistoryCsv(rows) {
  const header = ['告警时间', '告警等级', '告警类型', '告警来源', '告警点位', '告警名称', '当前值', '告警阈值', '告警状态', '持续时长', '恢复时间', '处理时间'];
  const lines = rows.map(a => [
    fmtTime(a.occurredAt),
    levelMeta(a.severity).name,
    typeMeta(a.type).name,
    a.source || '',
    a.point || '',
    a.name || '',
    alarmValueText(a),
    alarmThresholdText(a),
    displayStateMeta(a).name,
    fmtDuration(a.durationMs),
    fmtTime(a.recoveredAt),
    fmtTime(a.clearedAt),
  ]);
  // 带 UTF-8 BOM（\ufeff），Excel 直接打开中文不乱码
  // 公式注入防护：以 = + - @ 开头的单元格加前导单引号，避免 Excel 当公式执行。
  const csvCell = (c) => {
    let text = String(c == null ? '' : c).replace(/"/g, '""');
    if (/^[=+\-@]/.test(text)) text = "'" + text;
    return '"' + text + '"';
  };
  const csv = [header, ...lines]
    .map(cols => cols.map(csvCell).join(','))
    .join('\r\n');
  const blob = new Blob(['\ufeff' + csv], { type: 'text/csv;charset=utf-8' });
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  const ts = new Date();
  const pad = n => String(n).padStart(2, '0');
  link.href = url;
  link.download = `历史告警_${ts.getFullYear()}${pad(ts.getMonth() + 1)}${pad(ts.getDate())}_${pad(ts.getHours())}${pad(ts.getMinutes())}${pad(ts.getSeconds())}.csv`;
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}
