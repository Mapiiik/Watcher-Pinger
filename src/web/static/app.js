'use strict';

// Two trees of the same access points: the network with its devices, and the customers by the access point they are
// connected through. Both are kept up to date by the states the pinger sends; only what is expanded is in the page,
// and a change repaints only the rows it touches.

const STATUS = { UNKNOWN: 0, UP: 1, DOWN: 2, FAILED: 3 };

// English is the source, a translation lists only what it translates and falls back to English for the rest.
const TEXTS = {
  en: {
    network: 'Network',
    customers: 'Customers',
    search: 'Search a name or IP…',
    filterAll: 'All',
    filterOutages: 'Outages',
    filterLoss: 'Losses',
    expandOutages: 'Expand outages',
    expandLosses: 'Expand losses',
    collapseAll: 'Collapse all',
    unassigned: 'Without an access point',
    outageStartsHere: 'outage starts here',
    behindOutage: 'behind an outage',
    neverAnswered: 'no answer yet',
    lastReply: 'last answered {0} ago',
    loss: 'loss {0} %',
    history: 'The last {0} pings: loss {1} %, round trip on average {2} ms, at most {3} ms',
    historyLost: 'The last {0} pings: none answered',
    failed: 'cannot be pinged',
    generated: 'Data from Watcher NMS of {0} ({1})',
    staleTitle: 'The states shown are not current',
    staleText: 'The page has lost the pinger. If its window was closed, start Watcher Pinger again.',
    staleSince: 'Last heard from it at {0}.',
    staleTextHosted: 'The page has lost the server and keeps trying to reach it.',
    reload: 'Reload data',
    reloading: 'Reading…',
    reloaded: 'Read anew at {0}.',
    reloadFailed: 'The data were not read, the old ones stay: {0}',
    signOut: 'Sign out',
    copied: 'Copied: {0}',
    noResults: 'Nothing found.',
    tooMany: 'The first {0} addresses are shown.',
    contact: 'contact of the access point',
    up: 'up',
    down: 'down',
    unknown: 'not pinged',
    kind: { 'radio-unit': 'radio unit', 'routeros-device': 'RouterOS', 'power-supply': 'power supply', customer: '' },
  },
  cs: {
    network: 'Síť',
    customers: 'Zákazníci',
    search: 'Hledat jméno nebo IP…',
    filterAll: 'Vše',
    filterOutages: 'Výpadky',
    filterLoss: 'Ztráty',
    expandOutages: 'Rozbalit výpadky',
    expandLosses: 'Rozbalit ztráty',
    collapseAll: 'Sbalit vše',
    unassigned: 'Bez přístupového bodu',
    outageStartsHere: 'výpadek začíná zde',
    behindOutage: 'za výpadkem',
    neverAnswered: 'zatím neodpověděl',
    lastReply: 'naposledy před {0}',
    loss: 'ztráta {0} %',
    history: 'Posledních {0} pingů: ztráta {1} %, odezva průměrně {2} ms, nejvýš {3} ms',
    historyLost: 'Posledních {0} pingů: žádná odpověď',
    failed: 'nelze pingnout',
    generated: 'Data z Watcher NMS ze dne {0} ({1})',
    staleTitle: 'Zobrazené stavy nejsou aktuální',
    staleText: 'Stránka ztratila spojení s pingerem. Pokud bylo jeho okno zavřené, spusťte Watcher Pinger znovu.',
    staleSince: 'Naposledy se ozval v {0}.',
    staleTextHosted: 'Stránka ztratila spojení se serverem a zkouší se k němu znovu připojit.',
    reload: 'Načíst data znovu',
    reloading: 'Načítám…',
    reloaded: 'Načteno znovu v {0}.',
    reloadFailed: 'Data se nenačetla, zůstávají původní: {0}',
    signOut: 'Odhlásit',
    copied: 'Zkopírováno: {0}',
    noResults: 'Nic nenalezeno.',
    tooMany: 'Zobrazeno prvních {0} adres.',
    contact: 'kontakt přístupového bodu',
    up: 'odpovídá',
    down: 'neodpovídá',
    unknown: 'nepingnuto',
    kind: { 'radio-unit': 'rádiová jednotka', 'routeros-device': 'RouterOS', 'power-supply': 'zdroj', customer: '' },
  },
};

// The first language of the browser's preferences that there is a translation for, English if none.
const LANGUAGE = [...(navigator.languages || []), navigator.language || '']
  .map((tag) => tag.toLowerCase().split('-')[0])
  .find((code) => Object.hasOwn(TEXTS, code)) ?? 'en';
const T = { ...TEXTS.en, ...TEXTS[LANGUAGE] };

function text(key, ...args) {
  return args.reduce((s, arg, i) => s.replace(`{${i}}`, arg), T[key] ?? key);
}

// What each tree lists of an access point.
const VIEWS = {
  network: (node) => node.devices,
  customers: (node) => node.customers,
};

const MAX_RESULTS = 300;
const STORAGE_KEY = 'watcher-pinger';

// The page among others open on the same pinger: each watches what it shows.
const VIEWER = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;

const treeEl = document.getElementById('tree');
const resultsEl = document.getElementById('results');
const searchEl = document.getElementById('search');
const expandEl = document.getElementById('expand-filtered');

const nodes = [];            // access points, a parent before its children
const roots = [];
const states = new Map();    // ip -> [status, rtt ms, lost, last reply, since]
const rowsByIp = new Map();  // ip -> rows showing it, dropped once out of the page
const headers = new Set();   // nodes whose header is in the page
let view = 'network';
let searchIps = [];
let allIps = { network: [], customers: [] };

// --- the model ---------------------------------------------------------------------------------------------------

function normalize(value) {
  return value.normalize('NFD').replace(/[̀-ͯ]/g, '').toLowerCase();
}

function makeNode(id, name, parent) {
  const node = {
    id, name, key: normalize(name), parent, children: [], devices: [], customers: [],
    open: { network: false, customers: false }, total: { network: 0, customers: 0 },
    el: null, head: null, body: null, counts: null, out: false, behind: false,
  };
  (parent ? parent.children : roots).push(node);
  nodes.push(node);
  return node;
}

function addHost(node, host) {
  const entry = { ...host, node, key: normalize(host.name) };
  (host.kind === 'customer' ? node.customers : node.devices).push(entry);
}

function buildModel(book) {
  nodes.length = 0;
  roots.length = 0;
  rowsByIp.clear();
  headers.clear();
  const byId = new Map();
  for (const ap of book.accessPoints) {
    const node = makeNode(ap.id, ap.name, ap.parentId ? byId.get(ap.parentId) : null);
    byId.set(ap.id, node);
    ap.hosts.forEach((host) => addHost(node, host));
  }
  if (book.unassigned.length) {
    const node = makeNode('', text('unassigned'), null);
    book.unassigned.forEach((host) => addHost(node, host));
  }

  // what a tree shows of a subtree never changes, only the states do
  for (let i = nodes.length - 1; i >= 0; i--) {
    const node = nodes[i];
    node.total.network += node.devices.length;
    node.total.customers += node.customers.length;
    if (node.parent) {
      node.parent.total.network += node.total.network;
      node.parent.total.customers += node.total.customers;
    }
  }
  for (const name of Object.keys(VIEWS)) {
    allIps[name] = [...new Set(nodes.flatMap((n) => VIEWS[name](n).map((h) => h.ip)))];
  }
}

function statusOf(ip) {
  const state = states.get(ip);
  return state ? state[0] : STATUS.UNKNOWN;
}

// Sums every subtree and decides where an outage starts: an access point is out when all its devices are down, and
// everything below an access point that is out is behind the outage.
function recount() {
  for (const node of nodes) {
    node.counts = { network: [0, 0, 0, 0], customers: [0, 0, 0, 0] };
    node.highLoss = { network: 0, customers: 0 };
    node.anyLoss = { network: 0, customers: 0 };
  }
  for (let i = nodes.length - 1; i >= 0; i--) {
    const node = nodes[i];
    for (const [name, hosts] of [['network', node.devices], ['customers', node.customers]]) {
      for (const host of hosts) {
        node.counts[name][statusOf(host.ip)]++;
        if (losesMuch(host.ip)) node.highLoss[name]++;
        if (losesAny(host.ip)) node.anyLoss[name]++;
      }
    }
    node.out = node.devices.length > 0 && node.devices.every((host) => statusOf(host.ip) === STATUS.DOWN);
    if (node.parent) {
      for (let s = 0; s < 4; s++) {
        node.parent.counts.network[s] += node.counts.network[s];
        node.parent.counts.customers[s] += node.counts.customers[s];
      }
      for (const name of Object.keys(VIEWS)) {
        node.parent.highLoss[name] += node.highLoss[name];
        node.parent.anyLoss[name] += node.anyLoss[name];
      }
    }
  }
  let changed = false;
  for (const node of nodes) {
    const behind = node.parent ? node.parent.out || node.parent.behind : false;
    if (behind !== node.behind) {
      node.behind = behind;
      changed = true;
    }
  }
  return changed;
}

function downIn(node, name = view) {
  return node.counts[name][STATUS.DOWN];
}

// The share of the last pings lost, in per cent.
function lossOf(ip) {
  const state = states.get(ip);
  return state && state[5] ? Math.round(((state[5] - state[6]) / state[5]) * 100) : 0;
}

// An address that answers but loses more than this is shown among the outages.
const HIGH_LOSS = 20;

function losesMuch(ip) {
  return statusOf(ip) === STATUS.UP && lossOf(ip) > HIGH_LOSS;
}

// An address that answers but loses anything is shown among the losses, where a poor link shows.
function losesAny(ip) {
  return statusOf(ip) === STATUS.UP && lossOf(ip) > 0;
}

// Whether a subtree has anything to show among the outages.
function troubled(node, name = view) {
  return downIn(node, name) > 0 || node.highLoss[name] > 0 || node.out;
}

// Whether a subtree has anything to show among the losses.
function losing(node, name = view) {
  return node.anyLoss[name] > 0;
}

function shown(node) {
  return node.total[view] > 0;
}

// --- rendering ---------------------------------------------------------------------------------------------------

function el(tag, className, content) {
  const element = document.createElement(tag);
  if (className) element.className = className;
  if (content !== undefined) element.textContent = content;
  return element;
}

function nodeElement(node) {
  node.el = el('div', 'ap');
  node.head = el('div', 'ap-head');
  node.head.append(el('span', 'arrow'), el('span', 'dot'), el('span', 'name', node.name), el('span', 'badges'),
    el('span', 'note'));
  node.head.addEventListener('click', () => toggle(node));
  node.el.append(node.head);
  headers.add(node);

  if (node.open[view]) renderBody(node);
  paintHead(node);
  return node.el;
}

function renderBody(node) {
  node.body = el('div', 'ap-body');
  for (const host of VIEWS[view](node)) node.body.append(hostRow(host));
  for (const child of node.children) {
    if (shown(child)) node.body.append(nodeElement(child));
  }
  node.el.append(node.body);
}

function hostRow(host, withPath = false) {
  const row = el('div', 'host');
  const name = el('span', 'name');
  if (host.contact) {
    const plus = el('span', 'contact', '+');
    plus.title = text('contact');
    name.append(plus);
  }
  name.append(host.name);
  const ip = el('span', 'ip', host.ip);
  ip.addEventListener('click', (event) => {
    event.stopPropagation();
    copy(host.ip);
  });
  const info = el('span', 'info');
  row.append(el('span', 'dot'), name, ip, el('span', 'kind', kindOf(host)), info);
  if (withPath) {
    row.append(el('span', 'path', pathOf(host.node)));
    row.addEventListener('click', () => reveal(host.node, host.kind === 'customer' ? 'customers' : 'network', host));
  }
  row.host = host;
  row.info = info;

  if (!rowsByIp.has(host.ip)) rowsByIp.set(host.ip, new Set());
  rowsByIp.get(host.ip).add(row);
  paintRow(row);
  return row;
}

// What an address belongs to, with the manufacturer where it is known.
function kindOf(host) {
  const kind = T.kind[host.kind] ?? '';
  return host.manufacturer ? `${kind} (${host.manufacturer})` : kind;
}

function pathOf(node) {
  const names = [];
  for (let n = node; n; n = n.parent) names.unshift(n.name);
  return names.join(' › ');
}

function ago(seconds) {
  const s = Math.max(0, Math.round(Date.now() / 1000 - seconds));
  if (s < 90) return `${s} s`;
  if (s < 5400) return `${Math.round(s / 60)} min`;
  if (s < 172800) return `${Math.round(s / 3600)} h`;
  return `${Math.round(s / 86400)} d`;
}

function ms(value) {
  return value < 1 ? '<1' : String(Math.round(value));
}

// A row says how the last ping went and, over the last ones, how many were lost; the round trip times over them are
// in its tooltip.
function paintRow(row) {
  const host = row.host;
  const [status, rtt, , lastReply, , sent, received, average, longest] =
    states.get(host.ip) || [STATUS.UNKNOWN, 0, 0, 0, 0, 0, 0, 0, 0];
  const loss = lossOf(host.ip);
  const behind = status === STATUS.DOWN && (host.node.behind || (host.kind === 'customer' && host.node.out));
  row.className = `host s${status}`
    + (status === STATUS.UP && loss > 0 ? ' lossy' : '')
    + (losesMuch(host.ip) ? ' high-loss' : '')
    + (behind ? ' behind' : '');

  let info = '';
  if (status === STATUS.UP) info = `${ms(rtt)} ms${loss > 0 ? ` (${text('loss', loss)})` : ''}`;
  else if (status === STATUS.DOWN) info = lastReply ? text('lastReply', ago(lastReply)) : text('neverAnswered');
  else if (status === STATUS.FAILED) info = text('failed');
  row.info.textContent = info;

  if (!sent) row.title = '';
  else if (!received) row.title = text('historyLost', sent);
  else row.title = text('history', sent, loss, ms(average), ms(longest));
}

// The header of an access point counts what its tree shows of the subtree, and in both trees says whether the network
// is down there: a customer behind an outage is no fault of their own.
function paintHead(node) {
  const head = node.head;
  const counts = node.counts[view];
  const down = counts[STATUS.DOWN];
  head.className = 'ap-head'
    + (node.out ? ' out' : down > 0 ? ' some-down' : counts[STATUS.UP] > 0 ? ' all-up' : '')
    + (node.behind ? ' behind' : '');
  node.el.classList.toggle('clean', !troubled(node));
  node.el.classList.toggle('no-loss', !losing(node));

  head.children[0].textContent = node.open[view] ? '▾' : '▸';
  head.children[3].replaceChildren(...badge(counts));
  head.children[4].textContent = node.out && !node.behind ? text('outageStartsHere')
    : node.behind ? text('behindOutage') : '';
}

function badge(counts) {
  const total = counts[0] + counts[1] + counts[2] + counts[3];
  const parts = [document.createTextNode(`${counts[STATUS.UP]}/${total}`)];
  if (counts[STATUS.DOWN]) {
    parts.push(document.createTextNode(' '), el('span', 'down', `↓${counts[STATUS.DOWN]}`));
  }
  return parts;
}

function toggle(node) {
  node.open[view] = !node.open[view];
  if (node.open[view]) {
    renderBody(node);
  } else if (node.body) {
    node.body.remove();
    node.body = null;
  }
  paintHead(node);
  save();
  scheduleWatch();
}

function renderTree() {
  headers.clear();
  treeEl.replaceChildren(...roots.filter(shown).map(nodeElement));
  for (const button of document.querySelectorAll('#views button')) {
    button.classList.toggle('active', button.dataset.view === view);
  }
  paintTotals();
}

function switchView(name) {
  view = name;
  renderTree();
  save();
  scheduleWatch();
}

// Opens the way to an access point in the given tree, or to one of its addresses, shows it and lets it flash. What is
// looked for is shown whatever its state, so the filter goes back to everything.
function reveal(node, name, host = null) {
  view = name;
  for (let n = node; n; n = n.parent) n.open[view] = true;
  if (filter !== 'all') setFilter('all');
  searchEl.value = '';
  showSearch('');
  renderTree();
  save();
  scheduleWatch();

  const row = host && [...(rowsByIp.get(host.ip) || [])].find((r) => r.host === host && treeEl.contains(r));
  flash(row || node.head);
}

// The flash is an attribute, as a repaint sets the classes of a row anew.
function flash(element) {
  element.scrollIntoView({ block: 'center' });
  element.removeAttribute('data-flash');
  void element.offsetWidth;
  element.setAttribute('data-flash', '');
  setTimeout(() => element.removeAttribute('data-flash'), 1600);
}

// --- updates -----------------------------------------------------------------------------------------------------

const dirty = new Set();
let repaintPending = false;

function scheduleRepaint() {
  if (repaintPending) return;
  repaintPending = true;
  requestAnimationFrame(repaint);
}

function repaint() {
  repaintPending = false;
  const behindChanged = recount();
  const ips = behindChanged ? rowsByIp.keys() : dirty;
  for (const ip of ips) {
    const rows = rowsByIp.get(ip);
    if (!rows) continue;
    for (const row of rows) {
      if (row.isConnected) paintRow(row);
      else rows.delete(row);
    }
  }
  dirty.clear();
  for (const node of headers) {
    if (node.head.isConnected) paintHead(node);
    else headers.delete(node);
  }
  paintTotals();
}

function tally(name) {
  const totals = [0, 0, 0, 0];
  for (const ip of allIps[name]) totals[statusOf(ip)]++;
  return totals;
}

function paintTotals() {
  for (const button of document.querySelectorAll('#views button')) {
    const down = tally(button.dataset.view)[STATUS.DOWN];
    button.replaceChildren(text(button.dataset.view), ...(down ? [' ', el('span', 'down', `↓${down}`)] : []));
  }
  const totals = tally(view);
  document.getElementById('totals').replaceChildren(
    el('span', 'up', `● ${totals[STATUS.UP]} ${text('up')}`),
    el('span', 'down', `● ${totals[STATUS.DOWN]} ${text('down')}`),
    el('span', '', `○ ${totals[STATUS.UNKNOWN]} ${text('unknown')}`),
  );
}

// What is shown is watched, pinged most often: the addresses of the access points expanded in the tab open and those
// found. The pinger keeps the other devices, which tell where an outage starts, more often than the other customers.
let watchTimer = null;

function scheduleWatch() {
  clearTimeout(watchTimer);
  watchTimer = setTimeout(sendWatch, 300);
}

function sendWatch() {
  const ips = new Set(searchIps);
  for (const node of headers) {
    if (node.open[view] && node.head.isConnected) VIEWS[view](node).forEach((host) => ips.add(host.ip));
  }
  fetch(`api/watch?viewer=${VIEWER}`, { method: 'POST', body: JSON.stringify([...ips]) }).catch(() => {});
}

// The pinger sends something every two seconds at least. Without it for longer, or with the connection lost, what the
// page shows is old, and the page is covered so that nobody takes it for the state of the network.
const STALE_AFTER = 6000;
let lastMessage = Date.now();
let connected = true;

function checkStale() {
  const stale = !connected || Date.now() - lastMessage > STALE_AFTER;
  const overlay = document.getElementById('stale');
  if (stale && overlay.hidden) {
    document.getElementById('stale-since').textContent =
      text('staleSince', new Date(lastMessage).toLocaleTimeString());
  }
  overlay.hidden = !stale;
}

// The stream of the states. The browser opens it again by itself after a lost connection, but not after a refusal: a
// session run out sends the page to sign in again, a server shutting down is tried again a little later.
let source = null;
let opened = false;

function listen() {
  source = new EventSource(`api/events?viewer=${VIEWER}`);
  // What the page shows is watched only while its stream is open, so it is told again with every new one. A stream
  // opened again may be that of a pinger started again, with other data, which are read again for that reason.
  source.onopen = () => {
    if (opened) queueBook();
    opened = true;
    sendWatch();
  };
  source.onerror = () => {
    connected = false;
    checkStale();
    if (source.readyState === EventSource.CLOSED) {
      source = null;
      checkSession().finally(() => setTimeout(() => { if (!source) listen(); }, 3000));
    }
  };
  source.onmessage = (event) => {
    connected = true;
    lastMessage = Date.now();
    for (const [ip, state] of Object.entries(JSON.parse(event.data))) {
      states.set(ip, state);
      dirty.add(ip);
    }
    scheduleRepaint();
    checkStale();
  };
  // new data, read on this page or another one, or fetched by the server
  source.addEventListener('book', () => {
    lastMessage = Date.now();
    queueBook();
  });
}

async function checkSession() {
  try {
    const response = await fetch('api/session');
    if (response.status === 401) {
      const { signIn } = await response.json();
      location.assign(`${signIn}?return=${encodeURIComponent(location.pathname + location.search)}`);
    }
  } catch (e) {
    // the pinger does not answer, the page stays covered
  }
}

// --- search ------------------------------------------------------------------------------------------------------

let searchTimer = null;
searchEl.addEventListener('input', () => {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => showSearch(searchEl.value), 120);
});

// Searches both trees: the access points by name, the addresses by name or IP.
function showSearch(query) {
  const q = normalize(query.trim());
  if (!q) {
    resultsEl.hidden = true;
    treeEl.hidden = false;
    resultsEl.replaceChildren();
    searchIps = [];
    scheduleWatch();
    return;
  }

  const rows = [];
  for (const node of nodes) {
    if (node.key.includes(q)) {
      const row = el('div', 'result-ap');
      row.append(el('span', 'name', node.name), el('span', 'path', node.parent ? pathOf(node.parent) : ''));
      row.addEventListener('click', () => reveal(node, shown(node) ? view : 'customers'));
      rows.push(row);
    }
  }
  const hosts = [];
  for (const node of nodes) {
    for (const host of [...node.devices, ...node.customers]) {
      if (host.ip.includes(q) || host.key.includes(q)) hosts.push(host);
    }
  }
  hosts.slice(0, MAX_RESULTS).forEach((host) => rows.push(hostRow(host, true)));
  if (!rows.length) rows.push(el('div', 'message', text('noResults')));
  if (hosts.length > MAX_RESULTS) rows.push(el('div', 'message', text('tooMany', MAX_RESULTS)));

  searchIps = hosts.slice(0, MAX_RESULTS).map((host) => host.ip);
  resultsEl.replaceChildren(...rows);
  resultsEl.hidden = false;
  treeEl.hidden = true;
  scheduleWatch();
}

// --- the rest ----------------------------------------------------------------------------------------------------

function save() {
  try {
    const open = {};
    for (const name of Object.keys(VIEWS)) open[name] = nodes.filter((n) => n.open[name]).map((n) => n.id);
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ view, open, filter }));
  } catch (e) {
    // a browser keeping nothing just starts on the network, collapsed
  }
}

function restore() {
  try {
    const saved = JSON.parse(localStorage.getItem(STORAGE_KEY) || '{}');
    if (Object.hasOwn(VIEWS, saved.view)) view = saved.view;
    for (const name of Object.keys(VIEWS)) {
      const open = new Set(saved.open?.[name] || []);
      nodes.forEach((n) => { n.open[name] = open.has(n.id); });
    }
    if (Object.hasOwn(FILTERS, saved.filter)) filter = saved.filter;
  } catch (e) {
    // nothing kept
  }
  setFilter(filter);
}

// What the tree shows: everything, the outages, or the addresses that answer but lose anything.
const FILTERS = { all: '', outages: 'only-outages', loss: 'only-loss' };
let filter = 'all';

function setFilter(name) {
  filter = name;
  for (const className of Object.values(FILTERS)) {
    if (className) document.body.classList.toggle(className, className === FILTERS[name]);
  }
  for (const button of document.querySelectorAll('#filters button')) {
    button.classList.toggle('active', button.dataset.filter === name);
  }
  expandEl.textContent = text(name === 'loss' ? 'expandLosses' : 'expandOutages');
}

let toastTimer = null;
function copy(value) {
  navigator.clipboard?.writeText(value).then(() => {
    const toast = document.getElementById('toast');
    toast.textContent = text('copied', value);
    toast.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { toast.hidden = true; }, 1500);
  }).catch(() => {});
}

for (const button of document.querySelectorAll('#views button')) {
  button.addEventListener('click', () => switchView(button.dataset.view));
}

for (const button of document.querySelectorAll('#filters button')) {
  button.addEventListener('click', () => {
    setFilter(button.dataset.filter);
    save();
  });
}

// opens what the filter shows; the outages may spread over most of the tree, so they are shown without what answers
expandEl.addEventListener('click', () => {
  if (filter === 'all') setFilter('outages');
  for (const node of nodes) node.open[view] = filter === 'loss' ? losing(node) : troubled(node);
  renderTree();
  save();
  scheduleWatch();
});

document.getElementById('collapse-all').addEventListener('click', () => {
  nodes.forEach((n) => { n.open[view] = false; });
  renderTree();
  save();
  scheduleWatch();
});

// times like "5 min ago" grow old without any change being sent
setInterval(() => {
  for (const rows of rowsByIp.values()) {
    for (const row of rows) {
      if (row.isConnected && statusOf(row.host.ip) === STATUS.DOWN) paintRow(row);
    }
  }
}, 15000);

// Reads the data and builds the trees. Data read anew keep what was expanded and what is searched for.
async function readBook(first) {
  const response = await fetch('api/book');
  const { book, dataFile, canReload, hosted } = await response.json();
  document.getElementById('generated').textContent =
    text('generated', new Date(book.generated).toLocaleString(), dataFile);
  document.getElementById('reload').hidden = !canReload;
  document.querySelector('#stale [data-text="staleText"]').textContent =
    text(hosted ? 'staleTextHosted' : 'staleText');

  const open = Object.fromEntries(Object.keys(VIEWS).map(
    (name) => [name, new Set(nodes.filter((n) => n.open[name]).map((n) => n.id))]));
  buildModel(book);
  if (first) {
    restore();
  } else {
    for (const name of Object.keys(VIEWS)) nodes.forEach((n) => { n.open[name] = open[name].has(n.id); });
  }
  recount();
  renderTree();
  if (searchEl.value.trim()) showSearch(searchEl.value);
  else scheduleWatch();
}

let bookQueue = Promise.resolve();

function queueBook() {
  bookQueue = bookQueue.then(() => readBook(false)).catch(() => {});
}

document.getElementById('reload').addEventListener('click', async (event) => {
  const button = event.currentTarget;
  const result = document.getElementById('reload-result');
  button.disabled = true;
  result.className = '';
  result.textContent = text('reloading');
  try {
    const response = await fetch('api/reload', { method: 'POST' });
    const answer = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(answer.error || response.statusText);
    // the stream tells every page open, this one too, to read the new data
    result.textContent = text('reloaded', new Date().toLocaleTimeString());
  } catch (e) {
    result.className = 'failed';
    result.textContent = text('reloadFailed', e.message);
  } finally {
    button.disabled = false;
  }
});

async function showSession() {
  const session = await fetch('api/session').then((r) => r.json()).catch(() => ({}));
  if (!session.user) return;
  document.getElementById('user').textContent = session.user;
  const signOut = document.getElementById('sign-out');
  if (session.signOut) signOut.href = session.signOut;
  else signOut.remove();
  document.getElementById('session').hidden = false;
}

async function start() {
  document.querySelectorAll('[data-text]').forEach((element) => { element.textContent = text(element.dataset.text); });
  searchEl.placeholder = text('search');
  document.documentElement.lang = LANGUAGE;

  await readBook(true);
  showSession();
  listen();
  setInterval(checkStale, 1000);
}

start();
