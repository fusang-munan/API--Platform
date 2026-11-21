'use strict';

const byId = (id) => document.getElementById(id);
const form = byId('login');
const notice = byId('notice');
const error = byId('error');
const saved = byId('saved');
const result = byId('result');
let socket;
let csrf = '';
let session;
let closed = false;
let busy = true;

function showError(message) {
  error.textContent = message;
  error.hidden = !message;
}

function showNotice(message) {
  notice.textContent = message;
  notice.hidden = !message;
}

function renderControls() {
  byId('fields').disabled = busy || closed;
  byId('close').disabled = busy || closed;
  byId('refresh').disabled = busy || closed || !session?.can_refresh;
  byId('crawl').disabled = busy || closed;
  byId('change').disabled = busy || closed;
}

function renderMode() {
  const useToken = form.elements.mode.value === 'token';
  byId('tokenFields').hidden = byId('tokenFields').disabled = !useToken;
  byId('passwordFields').hidden = byId('passwordFields').disabled = useToken;
  byId('password').value = '';
  byId('token').value = '';
  byId('code').value = '';
}

function timeText(value) {
  return value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '北辰未返回';
}

function renderSession(value) {
  session = value;
  saved.hidden = false;
  byId('account').textContent = session.account || '北辰未返回账号';
  byId('expiry').textContent = timeText(session.expires_at);
  byId('verified').textContent = timeText(session.last_verified_at);
  if (session.expired) {
    byId('renewal').textContent = session.can_refresh ? '会话已到期，可以刷新会话。' : '会话已到期，请重新登录。';
  } else {
    byId('renewal').textContent = session.can_refresh
      ? '到期后，爬虫会尝试自动续期。'
      : '北辰未提供续期凭证。到期后请重新登录。';
  }
}

function renderCrawl(report) {
  result.hidden = false;
  const failures = report.failures || [];
  byId('counts').textContent = `平台 ${report.platform_count} 个 · 模型 ${report.model_count} 个 · 失败 ${failures.length} 项`;
  const list = byId('failures');
  list.replaceChildren();
  for (const failure of failures) {
    const item = document.createElement('li');
    item.textContent = [failure.platform, failure.path, failure.reason].filter(Boolean).join(' · ');
    list.append(item);
  }
  list.hidden = failures.length === 0;
}

function send(action, login) {
  if (busy || closed || socket?.readyState !== WebSocket.OPEN || !csrf) return;
  const messages = { login: '正在验证北辰登录…', refresh: '正在续期…', crawl: '正在读取北辰渠道页…', close: '正在关闭…' };
  showError('');
  showNotice(messages[action]);
  busy = true;
  renderControls();
  socket.send(JSON.stringify({ action, csrf, login }));
  csrf = '';
}

function finish() {
  closed = true;
  form.reset();
  form.hidden = true;
  saved.hidden = true;
  result.hidden = true;
  showError('');
  showNotice('本机服务已关闭，可以关闭此标签页。');
  renderControls();
}

form.addEventListener('change', (event) => {
  if (event.target.name === 'mode') renderMode();
});
form.addEventListener('submit', (event) => {
  event.preventDefault();
  const fields = new FormData(form);
  const mode = fields.get('mode');
  const login = mode === 'password'
    ? { mode, email: fields.get('email'), password: fields.get('password'), code: fields.get('code'), remember: fields.has('remember') }
    : { mode, token: fields.get('token') };
  byId('password').value = '';
  byId('token').value = '';
  byId('code').value = '';
  send('login', login);
});
byId('refresh').addEventListener('click', () => send('refresh'));
byId('crawl').addEventListener('click', () => send('crawl'));
byId('close').addEventListener('click', () => send('close'));
byId('change').addEventListener('click', () => {
  form.hidden = false;
  form.querySelector('input[name="mode"]:checked').focus();
});
window.addEventListener('pagehide', () => {
  form.reset();
  socket?.close();
});

let launchToken = location.hash.slice(1);
history.replaceState(null, '', location.pathname);
if (!/^[0-9a-f]{64}$/.test(launchToken)) {
  showNotice('');
  showError('请从终端运行北辰登录命令来打开窗口。');
} else {
  socket = new WebSocket(`ws://${location.host}/session`);
  socket.addEventListener('open', () => {
    socket.send(JSON.stringify({ action: 'open', csrf: launchToken }));
    launchToken = '';
  });
  socket.addEventListener('message', (event) => {
    const reply = JSON.parse(event.data);
    csrf = reply.csrf || '';
    busy = false;
    if (reply.closed) {
      finish();
      window.close();
      return;
    }
    showError(reply.error || '');
    showNotice(reply.notice || '');
    if (reply.session) renderSession(reply.session);
    if (reply.notice === '已保存，本机爬虫可直接使用' || !session && csrf) {
      form.hidden = Boolean(session);
      result.hidden = true;
      if (session) {
        form.reset();
        renderMode();
      }
    }
    if (reply.crawl) renderCrawl(reply.crawl);
    renderControls();
  });
  socket.addEventListener('close', () => {
    if (!closed) {
      finish();
      showError('窗口连接已结束。需要继续时，请重新运行北辰登录命令。');
    }
  });
}
