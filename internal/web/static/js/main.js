// main.js — 启动入口

const AUTH_KEY = 'ae_auth';

function init() {
  bindLogin();
  if (sessionStorage.getItem(AUTH_KEY) === '1') {
    enterApp();
    return;
  }
  document.getElementById('login-user').focus();
}

function bindLogin() {
  document.getElementById('login-btn').addEventListener('click', doLogin);
  ['login-user', 'login-pass'].forEach(id => {
    document.getElementById(id).addEventListener('keydown', e => { if (e.key === 'Enter') doLogin(); });
  });
}

function doLogin() {
  const u = document.getElementById('login-user').value.trim();
  const p = document.getElementById('login-pass').value.trim();
  if (u === LOGIN_USER && p === LOGIN_PASS) {
    sessionStorage.setItem(AUTH_KEY, '1');
    enterApp();
  } else {
    document.getElementById('login-err').classList.remove('d-none');
  }
}

function enterApp() {
  document.getElementById('login-err').classList.add('d-none');
  document.getElementById('login-overlay').classList.add('d-none');
  document.getElementById('app-shell').classList.remove('d-none');
  loadSidebarVersion();
  switchSection('alarms');
}

// 动态加载侧栏软件版本号
async function loadSidebarVersion() {
  try {
    const info = await SettingsAPI.system();
    const el = document.getElementById('sidebar-version');
    if (el && info && info.alarmEngineVersion) {
      el.textContent = info.alarmEngineVersion;
    }
  } catch (_) {
    // 请求失败时保留默认占位版本
  }
}

document.addEventListener('DOMContentLoaded', init);
