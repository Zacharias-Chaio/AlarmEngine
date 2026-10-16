// statistics.js — 告警统计模块（KPI 卡 + 级别占比 + 多系列趋势 + 时段热力图）

const charts = {};

// 趋势系列配色：全部 / 系统 / 软件 / 设备（全部=靛蓝、软件=橙，避免同为紫系难以区分）
const TREND_COLORS = { total: '#4f46e5', system: '#0ea5e9', software: '#f59e0b', device: '#059669' };

async function loadStatistics() {
  try {
    state.statistics = await HistoryAPI.statistics({ granularity: state.trendGranularity });
  } catch (e) {
    state.statistics = null;
  }
}

async function refreshStatistics() {
  await Promise.all([loadStatistics(), loadDailyDistribution()]);
  renderStatistics();
}

function renderStatistics() {
  const stats = state.statistics;
  if (!stats) return;
  renderKpiCards(stats.overview || {});
  renderSeverityChart(stats.severity);
  renderTrendChart(stats.trend);
  renderTopList(stats.top);
  renderDailyDistribution();
  renderHeatmap(stats.heatmap);
}

// ===== 组件1：核心指标卡片（KPI） =====

function renderKpiCards(o) {
  const grid = document.getElementById('sa-kpi-grid');
  if (!grid) return;
  const cards = [
    { lbl: '总告警数', val: o.total || 0, icon: 'bi-bell', color: '#4f46e5' },
    { lbl: '活跃告警', val: o.active || 0, icon: 'bi-exclamation-triangle', color: '#dc2626' },
    { lbl: 'MTTA 平均确认', val: o.mttaMs != null ? fmtDuration(o.mttaMs) : '—', icon: 'bi-person-check', color: '#0ea5e9', small: true },
    { lbl: 'MTTR 平均恢复', val: o.mttrMs != null ? fmtDuration(o.mttrMs) : '—', icon: 'bi-wrench-adjustable', color: '#059669', small: true },
  ];
  grid.innerHTML = cards.map(c => `
    <div class="summary-card">
      <div class="summary-icon" style="background:${c.color}1a;color:${c.color}"><i class="bi ${c.icon}"></i></div>
      <div>
        <div class="val" style="color:${c.color}${c.small ? ';font-size:18px' : ''}">${c.val}</div>
        <div class="lbl">${c.lbl}</div>
      </div>
    </div>`).join('');
}

// ===== 组件3：级别占比环形图 =====

function renderChart(id, type, config, plugins, options) {
  if (typeof Chart === 'undefined') return;
  const canvas = document.getElementById(id);
  if (!canvas) return;
  if (charts[id]) { charts[id].destroy(); }
  charts[id] = new Chart(canvas, {
    type: type,
    data: config,
    plugins: plugins || [],
    options: Object.assign(
      { responsive: true, maintainAspectRatio: false, plugins: { legend: { position: 'bottom' } } },
      options || {}
    ),
  });
}

// ===== 组件3：告警级别占比饼图（右侧图例：名称 + 百分比） =====

function renderSeverityChart(severity) {
  const rows = severity || [];
  const data = rows.map(x => x.value);
  const total = data.reduce((sum, v) => sum + v, 0) || 1;
  renderChart('sa-chart-severity', 'pie', {
    labels: rows.map(x => x.name),
    datasets: [{
      data: data,
      backgroundColor: ['#3b82f6', '#f59e0b', '#ea580c', '#dc2626'],
      borderWidth: 2,
      borderColor: '#fff',
    }],
  }, [], {
    plugins: {
      legend: {
        position: 'right',
        labels: {
          // 图例标记用圆点而非长方块。
          // 注意：圆点大小由字号决定；boxWidth 只影响间距，pointStyleWidth 会把点拉成椭圆，均勿用于调大小
          usePointStyle: true,
          pointStyle: 'circle',
          boxWidth: 10,
          // 图例项显示"名称 百分比"，圆点与扇区一一对应
          generateLabels(chart) {
            const ds = chart.data.datasets[0];
            return (chart.data.labels || []).map((label, i) => ({
              text: `${label}  ${Math.round((ds.data[i] / total) * 1000) / 10}%`,
              fillStyle: ds.backgroundColor[i],
              strokeStyle: '#fff',
              lineWidth: 1,
              usePointStyle: true,
              pointStyle: 'circle',
              hidden: false,
              index: i,
            }));
          },
        },
      },
    },
  });
}

// ===== 组件4：告警趋势折线图（全部/系统/软件/设备 多系列） =====

// 隐藏系列的图例样式：不使用内置删除线，改为灰度（色线与文字同时变灰）。
const GRAY = '#9ca3af';
const TEXT_DARK = '#374151';

function renderTrendChart(series) {
  if (!series || !series.length) return;
  renderChart('sa-chart-trend', 'line', {
    labels: series[0].points.map(p => p.label),
    datasets: series.map(s => ({
      label: s.name,
      data: s.points.map(p => p.value),
      borderColor: TREND_COLORS[s.key] || '#6b7280',
      borderWidth: 1.5, // 线条粗细（默认 3，调细）
      backgroundColor: (TREND_COLORS[s.key] || '#6b7280') + '24',
      fill: s.key === 'total',
      tension: 0.3,
      pointRadius: 3,
    })),
  }, [], {
    scales: {
      // 只保留横向网格线：关闭 x 轴纵向网格；横向网格用浅灰（与面板边框同色）
      x: { grid: { display: false } },
      y: { grid: { color: '#e5e7eb' } },
    },
    plugins: {
      legend: {
        position: 'bottom',
        labels: {
          // 图例标记用圆点而非长方块。
          // 注意：圆点大小由字号决定；boxWidth 只影响间距，pointStyleWidth 会把点拉成椭圆，均勿用于调大小
          usePointStyle: true,
          pointStyle: 'circle',
          boxWidth: 10,
          // hidden 恒为 false 以禁用内置删除线；灰度由 color/fontColor 表达
          generateLabels(chart) {
            return chart.data.datasets.map((ds, i) => {
              const hidden = !chart.isDatasetVisible(i);
              const color = hidden ? GRAY : ds.borderColor;
              return {
                text: ds.label,
                strokeStyle: color,
                fillStyle: color,
                fontColor: hidden ? GRAY : TEXT_DARK,
                lineWidth: 2,
                usePointStyle: true,
                pointStyle: 'circle',
                hidden: false,
                datasetIndex: i,
                index: i,
              };
            });
          },
        },
      },
    },
  });
}

// ===== 组件5：告警分布图（24h）（时间序列散点：X=时间，Y=级别数量，颜色=级别；右上角可选日期） =====

// 级别散点配色：与级别占比饼图/ALARM_LEVELS 保持一致（提示蓝/一般琥珀/严重橙/紧急红）
const DIST_LEVEL_COLORS = { 0: '#3b82f6', 1: '#f59e0b', 2: '#ea580c', 3: '#dc2626' };

// 本地日期字符串（YYYY-MM-DD），供日期选择器默认值。
function localDateStr(d) {
  const dt = d || new Date();
  const m = String(dt.getMonth() + 1).padStart(2, '0');
  const day = String(dt.getDate()).padStart(2, '0');
  return dt.getFullYear() + '-' + m + '-' + day;
}

function currentDistDate() {
  return state.distDate || localDateStr();
}

async function loadDailyDistribution() {
  try {
    state.distStats = await HistoryAPI.dailyDistribution({ date: currentDistDate() });
  } catch (e) {
    state.distStats = null;
  }
}

// 日期选择：仅刷新分布图（清空输入回退为今日）。
function setDistDate(value) {
  state.distDate = value || localDateStr();
  loadDailyDistribution().then(renderDailyDistribution);
}

function renderDailyDistribution() {
  const input = document.getElementById('sa-dist-date');
  if (input && !input.value) input.value = currentDistDate(); // 首次渲染回填默认日期
  const dist = state.distStats;
  const series = (dist && dist.series) || [];
  renderChart('sa-chart-dist', 'scatter', {
    datasets: series.map(s => ({
      label: s.name,
      // 散点坐标：x=小时(0-23)，y=该小时该级别告警数；0 计数不画点避免贴轴噪点
      data: s.points.map((p, h) => ({ x: h, y: p.value })).filter(p => p.y > 0),
      backgroundColor: DIST_LEVEL_COLORS[s.level] || '#6b7280',
      pointRadius: 5,
      pointHoverRadius: 7,
    })),
  }, [], {
    scales: {
      // 与趋势图同款：只保留横向网格线
      x: {
        min: -0.5, max: 23.5,
        grid: { display: false },
        ticks: { stepSize: 1, callback: v => String(v).padStart(2, '0') + ':00' },
      },
      y: {
        beginAtZero: true,
        grid: { color: '#e5e7eb' },
        ticks: { precision: 0 }, // 计数为整数
      },
    },
    plugins: {
      legend: {
        position: 'bottom',
        labels: {
          // 圆点图例（与饼图/趋势图一致；大小由字号决定，勿用 pointStyleWidth）
          usePointStyle: true,
          pointStyle: 'circle',
          boxWidth: 10,
          // 与告警趋势图行为一致：hidden 恒为 false 禁用内置删除线，点击隐藏后圆点与文字同时变灰
          generateLabels(chart) {
            return chart.data.datasets.map((ds, i) => {
              const hidden = !chart.isDatasetVisible(i);
              const color = hidden ? GRAY : ds.backgroundColor;
              return {
                text: ds.label,
                strokeStyle: color,
                fillStyle: color,
                fontColor: hidden ? GRAY : TEXT_DARK,
                lineWidth: 2,
                usePointStyle: true,
                pointStyle: 'circle',
                hidden: false,
                datasetIndex: i,
                index: i,
              };
            });
          },
        },
      },
      tooltip: {
        callbacks: {
          title: items => items.length ? String(Math.round(items[0].parsed.x)).padStart(2, '0') + ':00' : '',
          label: item => ` ${item.dataset.label}：${item.parsed.y} 条`,
        },
      },
    },
  });
}

// ===== 组件2：Top 告警排行（软件/设备分组点击展开收起，系统告警无子项不需要） =====

// 展开/收起分组：状态存 state.topExpanded，切换后原地重绘（不重新请求）。
function toggleTopGroup(key) {
  state.topExpanded[key] = !state.topExpanded[key];
  renderTopList(state.statistics && state.statistics.top);
}

function renderTopList(top) {
  const list = document.getElementById('sa-top-list');
  if (!list) return;
  if (!top || !top.length) {
    list.innerHTML = '<div class="empty-state"><i class="bi bi-inboxes"></i><div>暂无排行数据</div></div>';
    return;
  }
  list.innerHTML = top.map((g, i) => {
    const hasChildren = !!(g.children && g.children.length);
    const expanded = hasChildren && !!state.topExpanded[g.key];
    // 收起时显示子项数量提示，帮助判断是否展开
    const childCount = hasChildren ? `<span class="top-child-count">${g.children.length} 项</span>` : '';
    const caret = hasChildren ? `<i class="bi bi-chevron-${expanded ? 'down' : 'right'} top-caret"></i>` : '';
    const head = hasChildren
      ? `<li class="top-group top-toggle" onclick="toggleTopGroup('${escapeHtml(g.key)}')" title="点击${expanded ? '收起' : '展开'}子项"><span class="rank">${i + 1}</span>${caret}<span class="name">${escapeHtml(g.name)}</span>${childCount}<span class="num">${g.value}</span></li>`
      : `<li class="top-group"><span class="rank">${i + 1}</span><span class="name">${escapeHtml(g.name)}</span><span class="num">${g.value}</span></li>`;
    const children = (hasChildren && expanded)
      ? g.children.map(c => `<li class="top-child"><span class="rank">·</span><span class="name">${escapeHtml(c.name)}</span><span class="num">${c.value}</span></li>`).join('')
      : '';
    return head + children;
  }).join('');
}

// ===== 组件5：告警时段热力图（24h × 7 天，深色代表密集） =====

function renderHeatmap(heatmap) {
  const box = document.getElementById('sa-heatmap');
  if (!box) return;
  if (!heatmap || !heatmap.days || !heatmap.days.length) {
    box.innerHTML = '<div class="empty-state"><i class="bi bi-inboxes"></i><div>暂无时段数据</div></div>';
    return;
  }
  const grid = heatmap.grid || [];
  let max = 0;
  grid.forEach(row => row.forEach(v => { if (v > max) max = v; }));
  const cellStyle = v => {
    if (!v) return 'background:#f1f5f9';
    const alpha = 0.15 + 0.85 * (max ? v / max : 0);
    return `background:rgba(220,38,38,${alpha.toFixed(3)})`;
  };
  const rows = heatmap.days.map((day, d) => {
    const cells = Array.from({ length: 24 }, (_, h) => {
      const v = (grid[d] && grid[d][h]) || 0;
      return `<div class="heatmap-cell" style="${cellStyle(v)}" title="${day} ${String(h).padStart(2, '0')}:00 — ${v} 条"></div>`;
    }).join('');
    return `<div class="heatmap-row"><span class="heatmap-day">${escapeHtml(day)}</span>${cells}</div>`;
  }).join('');
  const hours = Array.from({ length: 24 }, (_, h) =>
    `<span class="heatmap-hour">${h % 3 === 0 ? String(h).padStart(2, '0') : ''}</span>`).join('');
  const swatches = [0.15, 0.35, 0.55, 0.75, 1].map(a =>
    `<span class="swatch" style="background:rgba(220,38,38,${a})"></span>`).join('');
  box.innerHTML = `
    <div class="heatmap">
      ${rows}
      <div class="heatmap-hours">${hours}</div>
      <div class="heatmap-legend"><span>少</span><span class="swatches">${swatches}</span><span>多</span></div>
    </div>`;
}

// ===== 趋势粒度切换 =====

function setTrendGranularity(g) {
  state.trendGranularity = g;
  document.querySelectorAll('[data-g]').forEach(b => b.classList.toggle('active', b.dataset.g === g));
  loadStatistics().then(renderStatistics);
}