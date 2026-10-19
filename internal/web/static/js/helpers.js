// helpers.js — 通用工具函数

// 区块加载态：请求期间降低透明度并禁用交互，避免用户把"加载中"误认为"没有数据"。
function setSectionLoading(key, loading) {
  const el = document.getElementById('section-' + key);
  if (el) el.classList.toggle('is-loading', !!loading);
}
// 读取 CSS 变量（图表等 Canvas 能力无法直接使用 var()，统一在此转为具体颜色）。
function cssVar(name, fallback) {
  const value = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return value || fallback || '';
}
// HTML 转义，防止 XSS
function escapeHtml(s) {
  return String(s == null ? '' : s).replace(/[&<>"']/g, c => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
  }[c]));
}

// 时间格式化
function fmtTime(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (isNaN(d.getTime())) return String(iso);
  return d.toLocaleString('zh-CN', { hour12: false });
}

// 区块切换：隐藏所有 section，显示目标 section，同步侧栏 active
function switchSection(key) {
  document.querySelectorAll('.app-section').forEach(s => s.classList.add('d-none'));
  const target = document.getElementById('section-' + key);
  if (target) target.classList.remove('d-none');
  document.querySelectorAll('.sidebar-item').forEach(i => i.classList.remove('active'));
  const nav = document.getElementById('nav-' + key);
  if (nav) nav.classList.add('active');
  clearLoadError();

  // 告警看板自动刷新：仅在看板页且页面可见时轮询（切走/隐藏即停，避免无谓请求）。
  if (key === 'alarms') startAlarmsPolling(); else stopAlarmsPolling();

  // 按区块触发渲染
  if (key === 'alarms') refreshAlarms();
  if (key === 'statistics') refreshStatistics();
  if (key === 'history') refreshHistory();
  if (key === 'engine') refreshEngine();
  if (key === 'protection') refreshProtection();
  if (key === 'settings') Promise.all([loadPlatformSettings(), loadSystemInfo()]).then(renderSettings);
}

// 看板轮询：10 秒一次（页面隐藏时跳过），切换区块时由 switchSection 启停。
let alarmsPollTimer = null;
function startAlarmsPolling() {
  stopAlarmsPolling();
  alarmsPollTimer = setInterval(() => {
    if (document.visibilityState === 'visible' && !state.alarmBatch.active) refreshAlarms();
  }, 10000);
}
function stopAlarmsPolling() {
  if (alarmsPollTimer) {
    clearInterval(alarmsPollTimer);
    alarmsPollTimer = null;
  }
}

// 数据加载失败提示：接口故障与"真的没有数据"必须可区分（看板显示空列表会误导运维）。
function reportLoadError(err, module) {
  const el = document.getElementById('global-error');
  if (!el) return;
  const name = module || '数据加载';
  const detail = err && err.message ? err.message : String(err);
  el.innerHTML = '<i class="bi bi-exclamation-triangle me-1"></i>' + escapeHtml(name) + '失败：' + escapeHtml(detail) +
    '（当前显示为空，可能并非真实为空）' +
    '<button type="button" class="btn-close btn-sm ms-2" aria-label="关闭" onclick="clearLoadError()"></button>';
  el.classList.remove('d-none');
}
function clearLoadError() {
  const el = document.getElementById('global-error');
  if (el) el.classList.add('d-none');
}

// 非阻塞 toast 提示（替代 alert：不打断操作、可堆叠、自动消失）。
let toastTimer = null;
function toast(msg, type) {
  let box = document.getElementById('toast-box');
  if (!box) {
    box = document.createElement('div');
    box.id = 'toast-box';
    document.body.appendChild(box);
  }
  const item = document.createElement('div');
  item.className = 'app-toast ' + (type === 'error' ? 'app-toast-error' : 'app-toast-info');
  item.textContent = msg;
  box.appendChild(item);
  setTimeout(() => item.remove(), 4000);
  if (toastTimer) clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { box.innerHTML = ''; toastTimer = null; }, 4500);
}

// 持续时间格式化（ms）
function fmtDuration(ms) {
  if (ms == null || isNaN(ms) || ms < 0) return '—';
  const s = Math.floor(ms / 1000);
  if (s < 60) return s + ' 秒';
  const m = Math.floor(s / 60);
  if (m < 60) return m + ' 分 ' + (s % 60) + ' 秒';
  const h = Math.floor(m / 60);
  if (h < 24) return h + ' 时 ' + (m % 60) + ' 分';
  const d = Math.floor(h / 24);
  return d + ' 天 ' + (h % 24) + ' 时';
}

// 生成 UUID（前端生成，用于新建资源）
function genId(prefix) {
  return (prefix || 'id') + '-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 8);
}

// 分页条渲染：total 总数 / page 当前页(1起) / pageSize 每页 / gotoFn 跳页函数名。
// 单页时不渲染；页码窗口为当前页 ±2，首尾页固定显示，间隔以省略号折叠。
function paginationHtml(total, page, pageSize, gotoFn) {
  const pages = Math.max(1, Math.ceil(total / pageSize));
  if (pages <= 1) return '';
  const items = [];
  const add = (p, label, disabled, active) => {
    const inner = (disabled || active)
      ? `<span class="page-link">${label}</span>`
      : `<a class="page-link" href="javascript:void(0)" onclick="${gotoFn}(${p})">${label}</a>`;
    items.push(`<li class="page-item${disabled ? ' disabled' : ''}${active ? ' active' : ''}">${inner}</li>`);
  };
  add(page - 1, '&laquo;', page <= 1, false);
  let last = 0;
  for (let p = 1; p <= pages; p++) {
    if (p === 1 || p === pages || Math.abs(p - page) <= 2) {
      if (last && p - last > 1) items.push('<li class="page-item disabled"><span class="page-link">…</span></li>');
      add(p, p, false, p === page);
      last = p;
    }
  }
  add(page + 1, '&raquo;', page >= pages, false);
  return `<ul class="pagination pagination-sm mb-0">${items.join('')}</ul>`;
}
