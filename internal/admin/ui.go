// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

// configUIPage is the self-contained configuration GUI served at the admin
// root. It is dependency-free vanilla HTML/CSS/JS and loads its data from the
// /api/config endpoints. Avoid backticks here: this is a Go raw string literal.
const configUIPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Jul.IA — Configuration</title>
<style>
  :root { color-scheme: light dark; }
  * { box-sizing: border-box; }
  body { font-family: system-ui, -apple-system, Segoe UI, Roboto, sans-serif;
         margin: 0; background: #0f1115; color: #e6e6e6; }
  header { padding: 16px 24px; background: #161a22; border-bottom: 1px solid #262b36;
           display: flex; align-items: baseline; gap: 12px; }
  header h1 { font-size: 20px; margin: 0; letter-spacing: 1px; }
  header .brand { color: #5ac8fa; font-weight: 700; }
  header .ver { color: #8a93a2; font-size: 13px; }
  header .path { margin-left: auto; color: #8a93a2; font-size: 13px; }
  header .back { color: #5ac8fa; text-decoration: none; font-size: 13px; }
  header .back:hover { text-decoration: underline; }
  main { max-width: 920px; margin: 0 auto; padding: 24px; }
  .tabs { display: flex; gap: 8px; margin-bottom: 16px; }
  .tabs button { background: #1c212b; color: #cdd3dd; border: 1px solid #2b313d;
                 padding: 8px 16px; border-radius: 6px; cursor: pointer; }
  .tabs button.active { background: #5ac8fa; color: #06121c; border-color: #5ac8fa; }
  .panel { display: none; background: #161a22; border: 1px solid #262b36;
           border-radius: 10px; padding: 20px; }
  .panel.active { display: block; }
  label { display: block; margin: 12px 0 4px; font-size: 13px; color: #aeb6c2; }
  input[type=text], select, textarea {
    width: 100%; background: #0f1115; color: #e6e6e6; border: 1px solid #2b313d;
    border-radius: 6px; padding: 9px 10px; font-size: 14px; font-family: inherit; }
  textarea { min-height: 380px; font-family: ui-monospace, Menlo, Consolas, monospace;
             font-size: 13px; white-space: pre; }
  .row { display: flex; gap: 16px; flex-wrap: wrap; }
  .row > div { flex: 1 1 220px; }
  .check { display: flex; align-items: center; gap: 8px; margin-top: 16px; }
  .check input { width: auto; }
  .actions { margin-top: 20px; display: flex; gap: 10px; align-items: center; }
  button.primary { background: #34c759; color: #04140a; border: none; padding: 10px 18px;
                   border-radius: 6px; font-weight: 600; cursor: pointer; }
  button.ghost { background: transparent; color: #cdd3dd; border: 1px solid #2b313d;
                 padding: 10px 18px; border-radius: 6px; cursor: pointer; }
  .note { color: #8a93a2; font-size: 12px; margin-top: 8px; }
  #status { margin-left: auto; font-size: 13px; min-height: 18px; }
  #status.ok { color: #34c759; } #status.err { color: #ff6b6b; }
  .token { margin: 8px 0 16px; }
</style>
</head>
<body>
<header>
  <a class="back" id="backLink" href="/" style="display:none">&larr; Console</a>
  <h1><span class="brand" id="brand">Jul.IA</span> Configuration</h1>
  <span class="ver" id="ver"></span>
  <span class="path" id="path"></span>
</header>
<main>
  <div class="token" id="tokenBox" style="display:none">
    <label for="token">Admin token (required)</label>
    <input type="text" id="token" placeholder="Bearer token" autocomplete="off"
           onchange="load()" onkeyup="if(event.key==='Enter')load()">
  </div>

  <div class="tabs">
    <button id="tabSettings" class="active" onclick="showTab('settings')">Settings</button>
    <button id="tabRaw" onclick="showTab('raw')">Advanced (raw TOML)</button>
  </div>

  <section id="settings" class="panel active">
    <div class="row">
      <div>
        <label for="logLevel">Log level</label>
        <select id="logLevel">
          <option>debug</option><option>info</option><option>warn</option><option>error</option>
        </select>
      </div>
      <div>
        <label for="logFormat">Log format</label>
        <select id="logFormat"><option>text</option><option>json</option></select>
      </div>
      <div>
        <label for="shutdownTimeout">Shutdown timeout</label>
        <input type="text" id="shutdownTimeout" placeholder="30s">
      </div>
    </div>
    <div class="row">
      <div>
        <label for="adminListen">Admin listen address</label>
        <input type="text" id="adminListen" placeholder="127.0.0.1:9090">
      </div>
      <div>
        <label for="cacheDefaultTTL">Cache default TTL</label>
        <input type="text" id="cacheDefaultTTL" placeholder="60s">
      </div>
      <div>
        <label for="cacheMemoryMaxSize">Cache memory max size</label>
        <input type="text" id="cacheMemoryMaxSize" placeholder="64m">
      </div>
    </div>
    <div class="check">
      <input type="checkbox" id="cacheEnabled">
      <label for="cacheEnabled" style="margin:0">Enable response cache</label>
    </div>
    <div class="actions">
      <button class="primary" onclick="saveSettings()">Save settings</button>
      <button class="ghost" onclick="triggerReload()">Reload</button>
      <span id="status"></span>
    </div>
    <p class="note">Saving here rewrites the whole config file; comments and formatting
       in the original file are not preserved. Use the Advanced tab to keep them.</p>
  </section>

  <section id="raw" class="panel">
    <label for="rawText">server.toml</label>
    <textarea id="rawText" spellcheck="false"></textarea>
    <div class="actions">
      <button class="primary" onclick="saveRaw()">Save raw TOML</button>
      <button class="ghost" onclick="load()">Revert</button>
      <span id="status2"></span>
    </div>
    <p class="note">Changes are validated before saving; an invalid file is rejected
       and the running configuration is kept.</p>
  </section>
</main>
<script>
  var state = {};
  function token() { return document.getElementById('token').value.trim(); }
  function headers(json) {
    var h = {};
    if (json) h['Content-Type'] = 'application/json';
    var t = token();
    if (t) h['Authorization'] = 'Bearer ' + t;
    return h;
  }
  function setStatus(id, msg, ok) {
    var el = document.getElementById(id);
    el.textContent = msg; el.className = ok ? 'ok' : 'err';
    if (ok) setTimeout(function () { el.textContent = ''; el.className = ''; }, 4000);
  }
  function showTab(name) {
    ['settings', 'raw'].forEach(function (n) {
      document.getElementById(n).classList.toggle('active', n === name);
      document.getElementById('tab' + n.charAt(0).toUpperCase() + n.slice(1))
        .classList.toggle('active', n === name);
    });
  }
  function load() {
    fetch('api/config', { headers: headers(false) })
      .then(function (r) { if (!r.ok) throw new Error('HTTP ' + r.status); return r.json(); })
      .then(function (d) {
        state = d;
        if (d.product) {
          document.getElementById('brand').textContent = d.product;
          document.title = d.product + ' \u2014 Configuration';
        }
        document.getElementById('ver').textContent = d.version || '';
        document.getElementById('path').textContent = d.path || '';
        document.getElementById('tokenBox').style.display = d.authRequired ? 'block' : 'none';
        document.getElementById('backLink').style.display = d.consoleEnabled ? 'inline' : 'none';
        if (d.settings) {
          document.getElementById('logLevel').value = d.settings.log_level || 'info';
          document.getElementById('logFormat').value = d.settings.log_format || 'text';
          document.getElementById('shutdownTimeout').value = d.settings.shutdown_timeout || '';
          document.getElementById('adminListen').value = d.settings.admin_listen || '';
          document.getElementById('cacheDefaultTTL').value = d.settings.cache_default_ttl || '';
          document.getElementById('cacheMemoryMaxSize').value = d.settings.cache_memory_max_size || '';
          document.getElementById('cacheEnabled').checked = !!d.settings.cache_enabled;
        }
        if (typeof d.raw === 'string') document.getElementById('rawText').value = d.raw;
      })
      .catch(function (e) {
        // A 401 means a token is required but absent/incorrect. The token box is
        // hidden until the first successful load, so reveal it here to let the
        // user authenticate and retry; otherwise there is no way to enter it.
        if (/\b401\b/.test(e.message)) {
          document.getElementById('tokenBox').style.display = 'block';
          setStatus('status', 'Authentication required: enter the admin token to continue.', false);
          return;
        }
        setStatus('status', 'Load failed: ' + e.message, false);
      });
  }
  function saveSettings() {
    var body = {
      log_level: document.getElementById('logLevel').value,
      log_format: document.getElementById('logFormat').value,
      shutdown_timeout: document.getElementById('shutdownTimeout').value,
      admin_listen: document.getElementById('adminListen').value,
      cache_default_ttl: document.getElementById('cacheDefaultTTL').value,
      cache_memory_max_size: document.getElementById('cacheMemoryMaxSize').value,
      cache_enabled: document.getElementById('cacheEnabled').checked
    };
    fetch('api/config/settings', { method: 'POST', headers: headers(true), body: JSON.stringify(body) })
      .then(readResult).then(function () {
        setStatus('status', 'Saved and reloaded.', true); load();
      }).catch(function (e) { setStatus('status', e.message, false); });
  }
  function saveRaw() {
    fetch('api/config/raw', { method: 'POST', headers: headers(false),
      body: document.getElementById('rawText').value })
      .then(readResult).then(function () {
        setStatus('status2', 'Saved and reloaded.', true);
      }).catch(function (e) { setStatus('status2', e.message, false); });
  }
  function triggerReload() {
    fetch('reload', { method: 'POST', headers: headers(false) })
      .then(readResult).then(function () { setStatus('status', 'Reload triggered.', true); })
      .catch(function (e) { setStatus('status', e.message, false); });
  }
  function readResult(r) {
    return r.json().catch(function () { return {}; }).then(function (d) {
      if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
      return d;
    });
  }
  load();
</script>
</body>
</html>
`
