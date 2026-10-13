// helpers.js — 通用工具函数

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

  // 按区块触发渲染
  if (key === 'alarms') refreshAlarms();
  if (key === 'history') refreshHistory();
  if (key === 'engine') loadRules().then(renderRules);
  if (key === 'settings') Promise.all([loadPlatformSettings(), loadSystemInfo()]).then(renderSettings);
}

// 简易 toast 提示
function toast(msg, type) {
  if (type === 'error') {
    alert('错误：' + msg);
  } else {
    alert(msg);
  }
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
