package ui

const indexHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>t3-bridge</title>
<style>
  body { background: #0b0e14; color: #cdd6f4; font: 13px/1.6 ui-monospace, SFMono-Regular, Menlo, monospace; margin: 0; }
  header { padding: 10px 16px; border-bottom: 1px solid #1e2030; display: flex; gap: 12px; align-items: baseline; position: sticky; top: 0; background: #0b0e14; }
  h1 { font-size: 14px; margin: 0; font-weight: 600; }
  #status { font-size: 12px; color: #7f849c; }
  #status.live { color: #a6e3a1; }
  #log { padding: 8px 16px 24px; }
  .row { white-space: pre-wrap; word-break: break-word; padding: 1px 0; }
  .lvl { display: inline-block; width: 5ch; }
  .DEBUG { color: #585b70; } .INFO { color: #89b4fa; } .WARN { color: #f9e2af; } .ERROR { color: #f38ba8; }
  .row.DEBUG-row { opacity: .55; }
  .t { color: #585b70; margin-right: 8px; }
  .attr { color: #94e2d5; margin-left: 8px; }
  .attr b { color: #7f849c; font-weight: 400; }
</style>
</head>
<body>
<header><h1>t3-bridge</h1><span id="status">connecting…</span></header>
<div id="log"></div>
<script>
  const log = document.getElementById('log');
  const status = document.getElementById('status');
  const esc = s => String(s).replace(/[&<>"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));
  function add(e) {
    const div = document.createElement('div');
    div.className = 'row ' + e.level + '-row';
    let attrs = '';
    for (const [k, v] of Object.entries(e.attrs || {})) {
      attrs += '<span class="attr"><b>' + esc(k) + '=</b>' + esc(v) + '</span>';
    }
    div.innerHTML =
      '<span class="t">' + new Date(e.t).toLocaleTimeString() + '</span>' +
      '<span class="lvl ' + esc(e.level) + '">' + esc(e.level) + '</span> ' +
      esc(e.msg) + attrs;
    log.prepend(div);
    while (log.childElementCount > 1000) log.lastChild.remove();
  }
  const es = new EventSource('events');
  es.onopen = () => { status.textContent = 'live'; status.className = 'live'; };
  es.onmessage = m => add(JSON.parse(m.data));
  es.onerror = () => { status.textContent = 'disconnected, retrying…'; status.className = ''; };
</script>
</body>
</html>
`
