// protection.js — 保护记录模块：保护动作下发流水（.cmd 两段式闭环展示，自告警引擎页拆分）
// 展示口径：面向运维的具体信息（动作、目标、结果、原因）；内部 ID 仅在悬停提示中保留。

async function loadActions() {
  const params = { page: state.actionsPage || 1, pageSize: PAGE_SIZE };
  if (state.actionsFilter) params.status = state.actionsFilter;
  try {
    const res = await ActionsAPI.list(params);
    state.actions = res.list || [];
    state.actionsPageData = res;
    clearLoadError();
  } catch (e) {
    state.actions = [];
    state.actionsPageData = null;
    reportLoadError(e, '保护记录');
  }
}

async function refreshProtection() {
  setSectionLoading('protection', true);
  try {
    await loadActions();
    renderActions();
  } finally { setSectionLoading('protection', false); }
}

function onActionsFilterChange() {
  state.actionsFilter = document.getElementById('pr-filter').value;
  state.actionsPage = 1;
  loadActions().then(renderActions);
}

function gotoActionsPage(page) {
  state.actionsPage = page;
  loadActions().then(renderActions);
}

// 动作目标：优先展示后端补全的设备名/服务名，缺失时回退内部 key。
function actionTarget(a) {
  const device = a.deviceName || a.deviceId || '未知设备';
  const service = a.serviceName || a.serviceKey || '未知服务';
  const value = Number.isFinite(Number(a.value)) ? Number(a.value) : 0;
  return { device, service, value };
}

function renderActions() {
  const filter = document.getElementById('pr-filter');
  if (filter && !filter.options.length) {
    filter.innerHTML = '<option value="">全部状态</option>' + ACTION_STATUSES.map(s => `<option value="${s.value}">${s.name}</option>`).join('');
  }
  const tbody = document.getElementById('pr-tbody');
  const count = document.getElementById('pr-count');
  const page = state.actionsPageData;
  if (count) count.textContent = page ? `${page.total} 条` : '0 条';
  if (!tbody) return;
  if (!state.actions.length) {
    tbody.innerHTML = '<tr><td colspan="6"><div class="empty-state py-4"><i class="bi bi-lightning-charge"></i><div>暂无动作下发记录</div></div></td></tr>';
    renderActionsPagination();
    return;
  }
  tbody.innerHTML = state.actions.map(a => {
    const sm = actionStatusMeta(a.status);
    const t = actionTarget(a);
    const alarm = a.alarmName
      ? escapeHtml(a.alarmName)
      : '<span class="text-muted">—</span>';
    const statusTitle = a.finalizedAt ? `终态时间：${fmtTime(a.finalizedAt)}` : '等待终态';
    return `<tr>
      <td class="small text-muted">${fmtTime(a.dispatchedAt)}</td>
      <td>${escapeHtml(a.ruleName || a.ruleId || '—')}</td>
      <td title="设备 ${escapeHtml(a.deviceId)} · 服务 ${escapeHtml(a.serviceKey)}">
        <i class="bi bi-lightning-charge me-1" style="color:var(--primary)"></i>${escapeHtml(t.device)} · ${escapeHtml(t.service)} = <strong>${escapeHtml(String(t.value))}</strong>
      </td>
      <td class="small">${alarm}</td>
      <td><span class="status-badge ${sm.cls}" title="${escapeHtml(statusTitle)}">${sm.name}</span></td>
      <td class="small text-muted">${escapeHtml(a.message || '—')}</td>
    </tr>`;
  }).join('');
  renderActionsPagination();
}

function renderActionsPagination() {
  const bar = document.getElementById('pr-pagination');
  if (!bar) return;
  const page = state.actionsPageData;
  if (!page || !page.total) { bar.innerHTML = ''; return; }
  bar.innerHTML = paginationHtml(page.total, page.page, page.pageSize, 'gotoActionsPage');
}
