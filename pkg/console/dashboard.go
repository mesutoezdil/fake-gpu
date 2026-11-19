package console

// page is the single-page console served at "/". It polls the API,
// keeps a short client-side history per card for the sparklines, and sends
// fault actions back. No build step and no external assets.
const page = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>fake-gpu console</title>
<style>
  :root {
    --ink: #0c0d0b; --panel: #121410; --line: #262a22; --soft: #1a1d17;
    --text: #e9ecdf; --dim: #8a917c; --lime: #c8f135; --amber: #ffb224;
    --red: #ff5a47; --cyan: #5fd3c9;
    --mono: ui-monospace, "SF Mono", "JetBrains Mono", Menlo, Consolas, monospace;
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { background: var(--ink); color: var(--text); font: 13px/1.4 var(--mono);
         padding: 28px 32px 40px; max-width: 1480px; margin: 0 auto; }
  header { display: flex; align-items: baseline; gap: 14px; }
  .mark { font-size: 26px; font-weight: 700; letter-spacing: -1px; }
  .mark b { font-weight: 700; }
  .mark .l { color: var(--lime); } .mark .a { color: var(--amber); }
  .mark .c { color: var(--cyan); } .mark .r { color: var(--red); }
  .tag { color: var(--ink); background: var(--amber); padding: 1px 7px; font-size: 11px;
         font-weight: 700; letter-spacing: 1px; text-transform: uppercase; }
  .versions { margin-left: auto; color: var(--dim); font-size: 12px; }
  .summary { display: grid; grid-template-columns: repeat(4, 1fr); margin: 22px 0 26px;
             border: 1px solid var(--line); background: var(--panel); }
  .summary div { padding: 14px 18px; border-right: 1px solid var(--line); }
  .summary div:last-child { border-right: 0; }
  .summary span { display: block; color: var(--dim); font-size: 11px; letter-spacing: 1px;
                  text-transform: uppercase; }
  .summary strong { display: block; font-size: 24px; font-weight: 600; margin-top: 2px; }
  .summary small { color: var(--dim); font-size: 12px; font-weight: 400; }
  .controls { display: flex; align-items: center; gap: 12px; margin-bottom: 18px; color: var(--dim); }
  select { background: var(--panel); color: var(--text); border: 1px solid var(--line);
           font: inherit; padding: 5px 8px; cursor: pointer; }
  select:hover, select:focus { border-color: var(--lime); outline: 0; }
  .tray-head { display: flex; align-items: center; gap: 12px; padding: 8px 0;
               border-bottom: 2px solid var(--text); margin-top: 22px; }
  .tray-head h2 { font-size: 13px; font-weight: 700; letter-spacing: 2px; text-transform: uppercase; }
  .tray-head span { color: var(--dim); }
  .tray-head select { margin-left: auto; }
  .card { display: grid; grid-template-columns: 34px minmax(190px, 1.3fr) 124px repeat(4, minmax(120px, 1fr));
          gap: 0 18px; align-items: center; padding: 12px 0 10px; border-bottom: 1px solid var(--line); }
  .card.off { opacity: .55; }
  .slot { font-size: 22px; font-weight: 700; color: var(--dim); }
  .who strong { display: block; font-weight: 600; }
  .who small { color: var(--dim); font-size: 11px; }
  .state { font-size: 11px; font-weight: 700; letter-spacing: 1px; text-transform: uppercase; }
  .state::before { content: ""; display: inline-block; width: 8px; height: 8px; margin-right: 7px;
                   background: currentColor; }
  .state.healthy { color: var(--lime); }
  .state.crashed { color: var(--red); }
  .state.overheating, .state.ecc_error, .state.throttling, .state.page_retired,
  .state.xgmi_link_down { color: var(--amber); }
  .gauge label { display: flex; justify-content: space-between; color: var(--dim); font-size: 10px;
                 letter-spacing: 1px; text-transform: uppercase; }
  .gauge label b { color: var(--text); font-size: 13px; font-weight: 600; letter-spacing: 0; text-transform: none; }
  .gauge label b.hot { color: var(--amber); }
  .gauge label b.crit { color: var(--red); }
  .track { height: 5px; background: var(--soft); margin-top: 4px; }
  .track i { display: block; height: 100%; background: var(--lime); transition: width .6s; }
  .track i.mid { background: var(--amber); }
  .track i.high { background: var(--red); }
  svg.spark { display: block; width: 100%; height: 18px; margin-top: 2px; }
  svg.spark polyline { fill: none; stroke: var(--cyan); stroke-width: 1.5; vector-effect: non-scaling-stroke; }
  .acts { grid-column: 2 / -1; display: flex; flex-wrap: wrap; gap: 4px 6px; margin-top: 9px;
          align-items: center; color: var(--dim); font-size: 10px; letter-spacing: 1px; text-transform: uppercase; }
  .acts em { font-style: normal; margin: 0 2px 0 8px; }
  .acts em:first-child { margin-left: 0; }
  button { font: 11px var(--mono); color: var(--dim); background: none; border: 1px solid var(--line);
           padding: 2px 8px; cursor: pointer; }
  button:hover { color: var(--text); border-color: var(--text); }
  button.on { color: var(--ink); background: var(--amber); border-color: var(--amber); }
  button.on.bad { background: var(--red); border-color: var(--red); }
  button.fix:hover { color: var(--ink); background: var(--lime); border-color: var(--lime); }
  footer { color: var(--dim); font-size: 11px; margin-top: 22px; max-width: 86ch; }
  @media (max-width: 1100px) {
    .card { grid-template-columns: 34px 1fr 124px; row-gap: 10px; }
    .gauge { grid-column: span 3; }
    .acts { grid-column: 1 / -1; }
    .summary { grid-template-columns: repeat(2, 1fr); }
  }
</style>
</head>
<body>
<header>
  <div class="mark" title="fake-gpu"><b class="l">F</b><b class="a">@</b>k<b class="c">&pound;</b> <b class="a">@</b><b class="r">&#8357;</b><b class="l">&#272;</b></div>
  <span class="tag">simulated</span>
  <div class="versions" id="versions"></div>
</header>

<section class="summary">
  <div><span>Cards</span><strong id="sum-cards">-</strong></div>
  <div><span>Fleet draw</span><strong id="sum-power">-</strong></div>
  <div><span>Hottest</span><strong id="sum-hot">-</strong></div>
  <div><span>Not healthy</span><strong id="sum-faults">-</strong></div>
</section>

<div class="controls">
  <span>fleet profile</span>
  <select id="fleet" onchange="switchFleet(this.value)"><option value="">loading</option></select>
  <span id="layout"></span>
</div>

<main id="rack"></main>

<footer>
  Cards in one tray are always the same model, as on a real Instinct baseboard; change a tray
  with the selector in its header. Sparklines show the last two minutes of edge temperature.
  Every button calls the same HTTP API that fakegpuctl uses.
</footer>

<script>
var API = window.location.origin;
var profiles = [];
var trail = {};          // card index -> recent temperatures
var HISTORY_POINTS = 60;   // one sample every 2 s

function esc(v) {
  return String(v).replace(/[&<>"]/g, function (c) {
    return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c];
  });
}
function level(pct) { return pct < 50 ? '' : pct < 80 ? 'mid' : 'high'; }

function gauge(label, value, pct, cls) {
  return '<div class="gauge"><label>' + label + '<b class="' + (cls || '') + '">' + value + '</b></label>' +
    '<div class="track"><i class="' + level(pct) + '" style="width:' + Math.max(0, Math.min(100, pct)) + '%"></i></div></div>';
}

function spark(points) {
  if (points.length < 2) return '<svg class="spark"></svg>';
  var lo = Math.min.apply(null, points), hi = Math.max.apply(null, points);
  var span = Math.max(hi - lo, 4);
  var step = 100 / (points.length - 1);   // the line always spans the full width
  var coords = points.map(function (t, i) {
    return (i * step).toFixed(1) + ',' + (16 - (t - lo) / span * 14).toFixed(1);
  }).join(' ');
  return '<svg class="spark" viewBox="0 0 100 18" preserveAspectRatio="none"><polyline points="' + coords + '"/></svg>';
}

function act(name, label, index, on, cls) {
  return '<button class="' + (on ? 'on ' : '') + (cls || '') + '" onclick="fire(\'' + name + '\',' + index + ')">' + label + '</button>';
}

function card(g) {
  var seen = trail[g.index] || (trail[g.index] = []);
  seen.push(g.temperature_c);
  if (seen.length > HISTORY_POINTS) seen.shift();

  var cap = g.power_cap_w || 1;
  var memPct = g.memory_total_mb ? g.memory_used_mb / g.memory_total_mb * 100 : 0;
  var tempCls = g.temperature_c >= 100 ? 'crit' : g.temperature_c >= 80 ? 'hot' : '';
  var is = function (s) { return g.status === s; };

  return '<div class="card' + (is('crashed') ? ' off' : '') + '">' +
    '<div class="slot">' + g.index + '</div>' +
    '<div class="who"><strong>' + esc(g.name) + '</strong><small>' + esc(g.pci_bdf) + ' &middot; numa ' + g.numa_node +
      ' &middot; ' + esc(g.partition || 'n/a') + '</small></div>' +
    '<div class="state ' + esc(g.status) + '">' + esc(g.status.replace(/_/g, ' ')) + '</div>' +
    gauge('load', g.utilization_pct + ' %', g.utilization_pct) +
    gauge('power', g.power_w + ' / ' + g.power_cap_w + ' W', g.power_w / cap * 100) +
    '<div class="gauge"><label>edge<b class="' + tempCls + '">' + g.temperature_c + ' C</b></label>' + spark(seen) + '</div>' +
    gauge('vram', (g.memory_used_mb / 1024).toFixed(1) + ' / ' + (g.memory_total_mb / 1024).toFixed(0) + ' GB', memPct) +
    '<div class="acts"><em>load</em>' +
      act('busy', 'busy', g.index, g.target_utilization_pct > 0) +
      act('idle', 'idle', g.index, false) +
      '<em>fault</em>' +
      act('throttle', 'throttle', g.index, is('throttling')) +
      act('overheat', 'overheat', g.index, is('overheating')) +
      act('ecc-error', 'ecc ' + g.ecc_errors, g.index, is('ecc_error')) +
      act('retire-page', 'retired ' + g.retired_pages, g.index, is('page_retired')) +
      act('xgmi-down', 'xgmi', g.index, is('xgmi_link_down')) +
      act('crash', 'crash', g.index, is('crashed'), 'bad') +
      '<em></em>' + act('recover', 'recover', g.index, false, 'fix') +
    '</div></div>';
}

function options(current) {
  return profiles.map(function (p) {
    return '<option value="' + esc(p.slug) + '"' + (p.slug === current ? ' selected' : '') + '>' +
      esc(p.name) + ' / ' + p.memory_gb + ' GB / ' + p.tdp_w + ' W</option>';
  }).join('');
}

function tray(t, gpus) {
  return '<div class="tray-head"><h2>tray ' + String.fromCharCode(65 + t.index) + '</h2>' +
    '<span>cards ' + gpus[0].index + ' to ' + gpus[gpus.length - 1].index + '</span>' +
    '<select title="change every card in this tray" onchange="switchTray(' + gpus[0].index + ', this.value)">' +
    options(t.profile_slug) + '</select></div>' + gpus.map(card).join('');
}

async function refresh() {
  try {
    var res = await Promise.all([fetch(API + '/api/gpus'), fetch(API + '/api/trays')]);
    var data = await res[0].json();
    var trays = await res[1].json();
    var gpus = data.gpus;

    document.getElementById('versions').textContent =
      'amdgpu ' + data.system.amdgpu_version + '   rocm ' + data.system.rocm_version;

    var draw = 0, budget = 0, hot = gpus[0], faults = 0;
    gpus.forEach(function (g) {
      draw += g.power_w; budget += g.power_cap_w;
      if (g.temperature_c > hot.temperature_c) hot = g;
      if (g.status !== 'healthy') faults++;
    });
    document.getElementById('sum-cards').textContent = gpus.length;
    document.getElementById('sum-power').innerHTML = draw + ' W <small>of ' + budget + ' W</small>';
    document.getElementById('sum-hot').innerHTML = hot.temperature_c + ' C <small>card ' + hot.index + '</small>';
    document.getElementById('sum-faults').textContent = faults;

    var same = trays.length > 0 && trays.every(function (t) { return t.profile_slug === trays[0].profile_slug; });
    var fleet = document.getElementById('fleet');
    if (document.activeElement !== fleet) {
      fleet.innerHTML = '<option value="">' + (same ? 'select profile' : 'mixed fleet') + '</option>' + options(same ? trays[0].profile_slug : '');
    }
    document.getElementById('layout').textContent = trays.length + (trays.length === 1 ? ' tray' : ' trays');

    if (!document.querySelector('.tray-head select:focus')) {
      document.getElementById('rack').innerHTML = trays.map(function (t) {
        return tray(t, gpus.filter(function (g) { return g.index >= t.start_gpu && g.index <= t.end_gpu; }));
      }).join('');
    }
    document.body.dataset.ready = '1';
  } catch (e) { console.error('refresh failed', e); }
}

async function post(path) { await fetch(API + path, { method: 'POST' }); refresh(); }
function fire(name, index) { post('/api/actions/' + name + '?gpu=' + index); }
function switchFleet(slug) { if (slug) { trail = {}; post('/api/profiles/switch?profile=' + slug); } }
function switchTray(index, slug) { if (slug) { trail = {}; post('/api/profiles/switch-tray?gpu=' + index + '&profile=' + slug); } }

(async function () {
  try { profiles = await (await fetch(API + '/api/profiles')).json() || []; } catch (e) { profiles = []; }
  refresh();
  setInterval(refresh, 2000);
})();
</script>
</body>
</html>`
