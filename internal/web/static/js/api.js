// api.js — API 客户端封装

const API_BASE = '/api';

// 通用请求封装：成功返回 data，失败 throw Error(msg)
async function apiReq(method, path, body) {
  const opt = { method, headers: {} };
  if (body !== undefined) {
    opt.headers['Content-Type'] = 'application/json';
    opt.body = JSON.stringify(body);
  }
  const res = await fetch(API_BASE + path, opt);
  const payload = await res.json().catch(() => ({ code: res.status, msg: '响应解析失败' }));
  if (!res.ok || payload.code !== 0) {
    throw new Error(payload.msg || ('HTTP ' + res.status));
  }
  return payload.data;
}

const apiGet    = (path)         => apiReq('GET', path);
const apiPost   = (path, body)   => apiReq('POST', path, body);
const apiDelete = (path)         => apiReq('DELETE', path);

// 查询串构造
function queryString(params) {
  if (!params) return '';
  const qs = Object.entries(params)
    .filter(([, v]) => v !== undefined && v !== null && v !== '')
    .map(([k, v]) => k + '=' + encodeURIComponent(v))
    .join('&');
  return qs ? '?' + qs : '';
}

// ===== 告警信息 API =====
const AlarmsAPI = {
  list:            (params) => apiGet('/alarms' + queryString(params)),
  summary:         ()       => apiGet('/alarms/summary'),
  get:             (id)     => apiGet('/alarms/' + encodeURIComponent(id)),
  acknowledge:     (id)     => apiPost('/alarms/' + encodeURIComponent(id) + '/acknowledge'),
  handle:          (id)     => apiPost('/alarms/' + encodeURIComponent(id) + '/handle'),
  batchAcknowledge:(ids)    => apiPost('/alarms/batch/acknowledge', { ids }),
  batchHandle:     (ids)    => apiPost('/alarms/batch/handle', { ids }),
};

// ===== 历史告警 API =====
const HistoryAPI = {
  list:       (params) => apiGet('/history' + queryString(params)),
  statistics: (params) => apiGet('/history/statistics' + queryString(params)),
};

// ===== 告警引擎 API =====
const RulesAPI = {
  list:   ()       => apiGet('/rules'),
  save:   (rule)   => apiPost('/rules', rule),
  remove: (id)     => apiDelete('/rules/' + encodeURIComponent(id)),
};

// ===== 软件配置 API =====
const SettingsAPI = {
  get:     ()         => apiGet('/settings'),
  save:   (settings)  => apiPost('/settings', settings),
  system:  ()         => apiGet('/system-info'),
  restart: ()         => apiPost('/restart'),
};
