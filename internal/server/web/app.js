'use strict';
// easy-music web UI. Plain JS, no build step: the server embeds this file.

const $ = (s, el = document) => el.querySelector(s);

// h('div', {class: 'x', onclick: fn}, 'text', child) — builds DOM without
// innerHTML, so track titles can never inject markup.
function h(tag, props, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v == null || v === false) continue;
    if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'class') el.className = v;
    else if (k === 'style') el.style.cssText = v;
    else if (k in el && typeof v !== 'string') el[k] = v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const kid of kids.flat()) {
    if (kid == null || kid === false) continue;
    el.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
  }
  return el;
}

// Inline SVG icons (Material Design paths): emoji and media-control glyphs
// are missing from too many system fonts. Constant strings, so innerHTML is
// safe here.
const ICONS = {
  play: 'M8 5v14l11-7z',
  pause: 'M6 5h4v14H6zM14 5h4v14h-4z',
  prev: 'M6 6h2v12H6zM9.5 12l8.5 6V6z',
  next: 'M16 6h2v12h-2zM6 18l8.5-6L6 6z',
  shuffle: 'M10.6 9.2 5.4 4 4 5.4l5.2 5.2zM14.5 4l2 2L4 18.6 5.4 20 18 7.5l2 2V4zm.3 9.4-1.4 1.4 3.1 3.1-2 2.1H20v-5.5l-2 2z',
  radio: 'M3.2 6.2C2.5 6.4 2 7.2 2 8v12a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2H8.3l8.3-3.3L15.9 1zM7 20a3 3 0 1 1 0-6 3 3 0 0 1 0 6zm13-8h-2v-2h-2v2H4V8h16z',
  video: 'M18 4l2 4h-3l-2-4h-2l2 4h-3l-2-4H8l2 4H7L5 4H4a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2V4z',
  queue: 'M3 6h18v2H3zm0 5h12v2H3zm0 5h12v2H3zm14-1v6l5-3z',
  fullscreen: 'M7 14H5v5h5v-2H7zm-2-4h2V7h3V5H5zm12 7h-3v2h5v-5h-2zM14 5v2h3v3h2V5z',
  person: 'M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8zm0 2c-2.7 0-8 1.3-8 4v2h16v-2c0-2.7-5.3-4-8-4z',
};
function icon(name, size = 20) {
  const span = document.createElement('span');
  span.className = 'svg';
  span.innerHTML = `<svg viewBox="0 0 24 24" width="${size}" height="${size}" fill="currentColor" aria-hidden="true"><path d="${ICONS[name]}"/></svg>`;
  return span;
}
document.querySelectorAll('[data-icon]').forEach(b => b.replaceChildren(icon(b.dataset.icon, b.classList.contains('big') ? 30 : 20)));

const api = {
  async req(method, path, body) {
    const opt = { method, headers: {} };
    if (body instanceof FormData) opt.body = body;
    else if (body !== undefined) { opt.body = JSON.stringify(body); opt.headers['Content-Type'] = 'application/json'; }
    const r = await fetch(path, opt);
    if (!r.ok) {
      let msg = r.statusText;
      try { msg = (await r.json()).error || msg; } catch (_) {}
      throw new Error(msg);
    }
    return r.status === 204 || r.status === 202 && !r.headers.get('Content-Type') ? null : r.json();
  },
  get(p) { return this.req('GET', p); },
  post(p, b) { return this.req('POST', p, b === undefined ? {} : b); },
  put(p) { return this.req('PUT', p); },
  patch(p, b) { return this.req('PATCH', p, b); },
  del(p) { return this.req('DELETE', p); },
};

function fmtTime(s) {
  if (!isFinite(s) || s <= 0) return '0:00';
  s = Math.floor(s);
  const hh = Math.floor(s / 3600), m = Math.floor(s % 3600 / 60), sec = String(s % 60).padStart(2, '0');
  return hh ? `${hh}:${String(m).padStart(2, '0')}:${sec}` : `${m}:${sec}`;
}
function fmtDate(unix) { return unix ? new Date(unix * 1000).toLocaleString() : ''; }

function toast(msg) {
  const t = h('div', { class: 'toast' }, msg);
  document.body.append(t);
  setTimeout(() => t.remove(), 2500);
}
function fail(e) { console.error(e); toast('⚠ ' + e.message); }

let openMenu = null;
function menu(ev, items) {
  ev.preventDefault();
  ev.stopPropagation();
  closeMenu();
  const m = h('div', { class: 'menu' }, items.filter(Boolean).map(([label, fn]) =>
    h('div', { onclick: e => { e.stopPropagation(); closeMenu(); fn(); } }, label)));
  document.body.append(m);
  const r = m.getBoundingClientRect();
  m.style.left = Math.min(ev.clientX, innerWidth - r.width - 8) + 'px';
  m.style.top = Math.min(ev.clientY, innerHeight - r.height - 8) + 'px';
  openMenu = m;
}
function closeMenu() { if (openMenu) { openMenu.remove(); openMenu = null; } }
document.addEventListener('click', closeMenu);

const coverURL = id => id ? `/api/tracks/${id}/cover` : '';
function coverImg(id, cls) {
  return h('img', { class: cls, src: coverURL(id), loading: 'lazy', alt: '', onerror: e => { e.target.style.visibility = 'hidden'; } });
}

// ---------------------------------------------------------------- player

const audio = $('#audio');
const video = h('video', { playsInline: true, preload: 'auto' });
$('#stage-video').append(video);

const P = {
  queue: [],      // track objects
  idx: -1,
  station: null,  // radio station being played instead of the queue
  mode: '',       // '' | 'radio' (similar to seed) | 'shuffle'
  seed: null,
  videoMode: localStorage.getItem('videoMode') === '1',
  history: [],    // ids played this session, for the mixer's variety rules
  listened: 0,    // seconds actually heard of the current track
  lastPos: null,
  refilling: false,
};
let media = audio;

const cur = () => P.queue[P.idx];
const wantVideo = t => P.videoMode && t && t.hasVideo;

for (const el of [audio, video]) {
  el.addEventListener('timeupdate', () => {
    if (el !== media) return;
    const pos = el.currentTime;
    if (P.lastPos != null && !el.paused) {
      const d = pos - P.lastPos;
      if (d > 0 && d < 2) P.listened += d; // ignore seeks
    }
    P.lastPos = pos;
    updateSeek();
  });
  el.addEventListener('ended', () => { if (el === media) advance('ended'); });
  el.addEventListener('play', () => { if (el === media) updatePlayButton(); });
  el.addEventListener('pause', () => { if (el === media) updatePlayButton(); });
  el.addEventListener('loadedmetadata', () => { if (el === media) updateSeek(); });
  el.addEventListener('error', () => {
    if (el !== media || !el.getAttribute('src')) return;
    toast(P.station ? 'Station unavailable' : 'Cannot play this file');
  });
}

// Count the listen (or the skip) of the track we are leaving.
function finishTrack(reason) {
  const t = cur();
  if (!t || P.station) return;
  const heard = P.listened;
  const dur = t.duration || media.duration || 0;
  P.listened = 0;
  P.lastPos = null;
  if (heard >= Math.min(240, Math.max(dur * 0.5, 20)) || (reason === 'ended' && heard >= 20)) {
    t.plays = (t.plays || 0) + 1;
    api.post(`/api/tracks/${t.id}/played`, { seconds: Math.round(heard) }).catch(fail);
  } else if (reason === 'skip' && heard < 30) {
    api.post(`/api/tracks/${t.id}/skipped`).catch(() => {});
  }
}

function load(t, { autoplay = true, at = 0 } = {}) {
  P.station = null;
  const useVideo = wantVideo(t);
  const el = useVideo ? video : audio;
  const other = useVideo ? audio : video;
  other.pause();
  if (other.getAttribute('src')) { other.removeAttribute('src'); other.load(); }
  media = el;
  media.volume = +$('#volume').value;
  el.src = `/api/tracks/${t.id}/${useVideo ? 'video' : 'stream'}`;
  if (at) el.addEventListener('loadedmetadata', () => { el.currentTime = at; }, { once: true });
  $('#stage').hidden = !useVideo;
  if (autoplay) el.play().catch(() => {});
  if (!P.history.length || P.history[P.history.length - 1] !== t.id) P.history.push(t.id);
  updateNow();
  renderQueue();
  markPlaying();
  refill();
}

function playList(tracks, start = 0, mode = '') {
  if (!tracks.length) return;
  finishTrack('skip');
  P.queue = tracks.slice();
  P.idx = start;
  setMode(mode, tracks[start]);
  load(P.queue[P.idx]);
}

function enqueue(tracks, next = false) {
  if (!P.queue.length || P.idx < 0) { playList(tracks); return; }
  P.queue.splice(next ? P.idx + 1 : P.queue.length, 0, ...tracks);
  renderQueue();
  toast(next ? 'Playing next' : `Added ${tracks.length} to queue`);
}

function advance(reason) {
  if (P.station) return;
  finishTrack(reason);
  if (P.idx < P.queue.length - 1) {
    P.idx++;
    load(cur());
  } else if (P.mode) {
    refill().then(() => { if (P.idx < P.queue.length - 1) { P.idx++; load(cur()); } });
  } else {
    media.pause();
    updatePlayButton();
  }
}

function prev() {
  if (P.station) return;
  if (media.currentTime > 5 || P.idx <= 0) { media.currentTime = 0; return; }
  finishTrack('skip');
  P.idx--;
  load(cur());
}

function jump(i) {
  if (i === P.idx && !P.station) return;
  finishTrack('skip');
  P.idx = i;
  load(cur());
}

function togglePlay() {
  if (!media.getAttribute('src')) {
    if (P.queue.length) load(cur() || P.queue[0]);
    else startShuffle();
    return;
  }
  media.paused ? media.play().catch(fail) : media.pause();
}

// Radio/shuffle: top the queue up from the server's mixer when it runs low.
async function refill() {
  if (!P.mode || P.refilling || P.station) return;
  if (P.queue.length - P.idx - 1 >= 3) return;
  P.refilling = true;
  try {
    const recent = [...new Set([...P.history.slice(-60), ...P.queue.slice(Math.max(0, P.idx - 30)).map(t => t.id)])];
    const picks = await api.post('/api/mix', { seed: P.mode === 'radio' ? P.seed : '', recent, count: 5 });
    P.queue.push(...picks.filter(p => !P.queue.some(q => q.id === p.id)));
    renderQueue();
  } catch (e) { fail(e); } finally { P.refilling = false; }
}

function setMode(mode, seedTrack) {
  P.mode = mode;
  P.seed = mode === 'radio' && seedTrack ? seedTrack.id : null;
  $('#btn-radio').classList.toggle('on', mode === 'radio');
  $('#btn-shuffle').classList.toggle('on', mode === 'shuffle');
  $('#btn-radio').title = mode === 'radio' && seedTrack
    ? `Radio mode: similar to “${seedTrack.title}” (click to stop)`
    : 'Radio mode: keep playing similar music from here';
}

function startRadio(t) {
  // The seed plays first, then the mixer takes over.
  playList([t], 0, 'radio');
}

async function startShuffle() {
  finishTrack('skip');
  P.queue = [];
  P.idx = -1;
  setMode('shuffle');
  await refill();
  if (P.queue.length) { P.idx = 0; load(cur()); }
}

function toggleRadio() {
  if (P.mode === 'radio') { setMode(''); return; }
  const t = cur();
  if (!t || P.station) { toast('Play a track first, then turn on radio mode'); return; }
  // Upcoming queue entries stay; the radio continues after them.
  setMode('radio', t);
  refill();
  toast(`Radio: more like “${t.title}”`);
}

function toggleVideo() {
  P.videoMode = !P.videoMode;
  localStorage.setItem('videoMode', P.videoMode ? '1' : '0');
  const t = cur();
  if (t && !P.station && t.hasVideo) {
    // Same track, other element; keep position and play state.
    const at = media.currentTime, playing = !media.paused;
    const listened = P.listened;
    load(t, { autoplay: playing, at });
    P.listened = listened;
  }
  updateNow();
}

async function playStation(st) {
  finishTrack('skip');
  video.pause();
  $('#stage').hidden = true;
  media = audio;
  P.station = st;
  // HLS can't be relayed byte-for-byte; let the browser fetch it directly.
  audio.src = /\.m3u8(\?|$)/i.test(st.url) ? st.url : `/api/stations/${st.id}/stream`;
  audio.play().catch(fail);
  api.post(`/api/stations/${st.id}/played`).catch(() => {});
  updateNow();
  markPlaying();
}

function updateNow() {
  const t = cur(), st = P.station;
  $('#now-title').textContent = st ? st.name : t ? t.title : 'Nothing playing';
  $('#now-artist').textContent = st ? 'Internet radio' : t ? [t.artist, t.album].filter(Boolean).join(' — ') : '';
  $('#now-cover').src = !st && t ? coverURL(t.id) : '';
  $('#now-cover').style.visibility = '';
  $('#now-fav').textContent = !st && t && t.favorite ? '♥' : '♡';
  $('#now-fav').classList.toggle('on', !!(!st && t && t.favorite));
  $('#btn-video').disabled = !!st || !(t && t.hasVideo);
  $('#btn-video').classList.toggle('on', P.videoMode);
  $('#btn-video').title = t && t.hasVideo ? (P.videoMode ? 'Hide music video' : 'Show music video') : 'No music video for this track';
  document.title = st ? `${st.name} · easy-music` : t ? `${t.title} – ${t.artist}` : 'easy-music';
  if ('mediaSession' in navigator) {
    navigator.mediaSession.metadata = new MediaMetadata({
      title: st ? st.name : t ? t.title : '',
      artist: st ? 'Internet radio' : t ? t.artist : '',
      album: t && !st ? t.album || '' : '',
      artwork: t && !st ? [{ src: coverURL(t.id), sizes: '600x600', type: 'image/jpeg' }] : [],
    });
  }
  updatePlayButton();
}

function updatePlayButton() {
  $('#btn-play').replaceChildren(icon(media.paused ? 'play' : 'pause', 30));
}

function updateSeek() {
  const d = P.station ? 0 : media.duration;
  $('#time-cur').textContent = fmtTime(media.currentTime);
  $('#time-dur').textContent = P.station ? 'live' : fmtTime(d);
  if (!seeking) $('#seek').value = d ? Math.round(media.currentTime / d * 1000) : 0;
}

let seeking = false;
$('#seek').addEventListener('input', () => { seeking = true; });
$('#seek').addEventListener('change', e => {
  seeking = false;
  if (media.duration && !P.station) media.currentTime = e.target.value / 1000 * media.duration;
});
$('#volume').value = localStorage.getItem('volume') || 1;
$('#volume').addEventListener('input', e => {
  audio.volume = video.volume = +e.target.value;
  localStorage.setItem('volume', e.target.value);
});
audio.volume = video.volume = +$('#volume').value;

$('#btn-play').onclick = togglePlay;
$('#btn-next').onclick = () => advance('skip');
$('#btn-prev').onclick = prev;
$('#btn-radio').onclick = toggleRadio;
$('#btn-shuffle').onclick = () => P.mode === 'shuffle' ? setMode('') : startShuffle();
$('#btn-video').onclick = toggleVideo;
$('#btn-queue').onclick = () => { $('#queue').hidden = !$('#queue').hidden; renderQueue(); };
$('#queue-close').onclick = () => { $('#queue').hidden = true; };
$('#queue-clear').onclick = () => { P.queue = cur() ? [cur()] : []; P.idx = P.queue.length ? 0 : -1; renderQueue(); };
$('#stage-hide').onclick = toggleVideo;
$('#stage-full').onclick = () => video.requestFullscreen && video.requestFullscreen();
$('#now-fav').onclick = () => { const t = cur(); if (t && !P.station) toggleFavorite(t); };

if ('mediaSession' in navigator) {
  navigator.mediaSession.setActionHandler('play', togglePlay);
  navigator.mediaSession.setActionHandler('pause', togglePlay);
  navigator.mediaSession.setActionHandler('nexttrack', () => advance('skip'));
  navigator.mediaSession.setActionHandler('previoustrack', prev);
}

document.addEventListener('keydown', e => {
  if (e.target.matches('input, textarea, select')) return;
  if (e.key === ' ') { e.preventDefault(); togglePlay(); }
  else if (e.key === 'n') advance('skip');
  else if (e.key === 'p') prev();
  else if (e.key === 'v' && !$('#btn-video').disabled) toggleVideo();
  else if (e.key === 'ArrowRight' && !P.station) media.currentTime += 10;
  else if (e.key === 'ArrowLeft' && !P.station) media.currentTime -= 10;
});

// Behind idlegate, a fully buffered song makes no requests; tell the server
// we're still listening so it isn't put to sleep mid-album. Paused or
// stopped: no heartbeat, and the server may sleep as intended.
setInterval(() => {
  if (!media.paused) fetch('/api/now-playing', { method: 'POST' }).catch(() => {});
}, 4 * 60 * 1000);

function renderQueue() {
  if ($('#queue').hidden) return;
  const ol = $('#queue-list');
  ol.replaceChildren(...P.queue.map((t, i) => h('li', {
    class: i === P.idx && !P.station ? 'current' : i < P.idx ? 'played' : '',
    onclick: () => jump(i),
  },
  h('div', { class: 't' }, h('div', { class: 'ellipsis' }, t.title), h('div', { class: 'ellipsis muted small' }, t.artist)),
  i > P.idx ? h('button', { title: 'Remove', onclick: e => { e.stopPropagation(); P.queue.splice(i, 1); renderQueue(); } }, '✕') : null)));
  if (P.mode) ol.append(h('li', { class: 'muted small' }, P.mode === 'radio' ? 'Radio continues with similar music…' : 'Shuffle continues…'));
  const c = $('li.current', ol);
  if (c) c.scrollIntoView({ block: 'nearest' });
}

function markPlaying() {
  const id = !P.station && cur() ? cur().id : null;
  document.querySelectorAll('[data-track]').forEach(el => el.classList.toggle('playing', el.dataset.track === id));
  document.querySelectorAll('[data-station]').forEach(el => el.classList.toggle('playing', !!P.station && el.dataset.station === P.station.id));
}

async function toggleFavorite(t) {
  try {
    t.favorite = !t.favorite;
    await (t.favorite ? api.put(`/api/tracks/${t.id}/favorite`) : api.del(`/api/tracks/${t.id}/favorite`));
    for (const q of P.queue) if (q.id === t.id) q.favorite = t.favorite;
    document.querySelectorAll(`[data-fav="${t.id}"]`).forEach(b => { b.textContent = t.favorite ? '♥' : '♡'; b.classList.toggle('on', t.favorite); });
    updateNow();
  } catch (e) { t.favorite = !t.favorite; fail(e); }
}

// ---------------------------------------------------------------- playlists (sidebar)

let playlists = [];
async function loadPlaylists() {
  playlists = await api.get('/api/playlists');
  $('#playlists').replaceChildren(...playlists.map(p => h('a', { href: `#/playlist/${p.id}`, 'data-nav': true }, p.name)));
  highlightNav();
}

async function newPlaylist(tracks = []) {
  const name = prompt('Playlist name');
  if (!name) return null;
  const p = await api.post('/api/playlists', { name, tracks: tracks.map(t => t.id) });
  await loadPlaylists();
  toast(`Created “${p.name}”`);
  return p;
}
$('#new-playlist').onclick = () => newPlaylist().catch(fail);

function addToPlaylistMenu(ev, tracks) {
  menu(ev, [
    ...playlists.map(p => [p.name, () => api.post(`/api/playlists/${p.id}/tracks`, { tracks: tracks.map(t => t.id) })
      .then(() => { toast(`Added to “${p.name}”`); loadPlaylists(); }).catch(fail)]),
    ['+ New playlist…', () => newPlaylist(tracks).catch(fail)],
  ]);
}

// ---------------------------------------------------------------- router

const view = $('#view');
let viewToken = 0; // stale async renders check this and bail
let poller = null;

function parseHash() {
  const [path, qs] = (location.hash.slice(1) || '/tracks').split('?');
  return { parts: path.split('/').filter(Boolean), q: new URLSearchParams(qs || '') };
}

function highlightNav() {
  const href = location.hash || '#/tracks';
  document.querySelectorAll('[data-nav]').forEach(a => a.classList.toggle('active', a.getAttribute('href') === href));
}

async function route() {
  const token = ++viewToken;
  clearInterval(poller);
  poller = null;
  highlightNav();
  const { parts, q } = parseHash();
  try {
    switch (parts[0]) {
      case 'albums': await renderAlbums(token, q); break;
      case 'artists': await renderArtists(token, q); break;
      case 'playlist': await renderPlaylist(token, parts[1]); break;
      case 'radio': await renderRadio(token); break;
      case 'import': await renderImport(token); break;
      case 'stats': await renderStats(token); break;
      default: await renderTracks(token, q);
    }
    markPlaying();
  } catch (e) {
    if (token === viewToken) view.replaceChildren(h('p', { class: 'error' }, e.message));
  }
}
window.addEventListener('hashchange', route);

// ---------------------------------------------------------------- track table

const SORTS = { title: 'Title', artist: 'Artist', album: 'Album', plays: 'Plays', duration: 'Time' };

function trackTable(tracks, { sort, order, onSort, playlist } = {}) {
  const th = (key, label, cls) => h('th', {
    class: cls,
    onclick: onSort && key ? () => onSort(key, sort === key && order !== 'desc' ? 'desc' : 'asc') : null,
  }, label, sort === key ? (order === 'desc' ? ' ↓' : ' ↑') : '');
  const rows = tracks.map((t, i) => {
    const fav = h('button', { 'data-fav': t.id, class: t.favorite ? 'on' : '', title: 'Favorite', onclick: e => { e.stopPropagation(); toggleFavorite(t); } }, t.favorite ? '♥' : '♡');
    const actions = [
      ['Play next', () => enqueue([t], true)],
      ['Add to queue', () => enqueue([t])],
      ['Start radio from this track', () => startRadio(t)],
      ['Add to playlist…', () => setTimeout(() => addToPlaylistMenu(lastMenuEvent, [t]))],
      playlist && ['Remove from playlist', () => playlist.remove(i)],
      playlist && i > 0 && ['Move up', () => playlist.move(i, -1)],
      playlist && i < tracks.length - 1 && ['Move down', () => playlist.move(i, 1)],
      t.source && ['Open original page', () => window.open(t.source, '_blank', 'noopener')],
      ['Show album', () => { location.hash = `#/tracks?album=${t.albumId}&title=${encodeURIComponent(t.album || 'Album')}`; }],
    ];
    const playable = tracks.filter(x => !x.missing);
    return h('tr', {
      'data-track': t.id, class: t.missing ? 'missing' : '',
      ondblclick: () => !t.missing && playList(playable, playable.indexOf(t)),
    },
    h('td', { class: 'num' }, i + 1),
    h('td', { title: t.file || '' }, t.title, t.hasVideo ? h('span', { class: 'badge', title: 'Has a music video' }, 'VIDEO') : null,
      t.source ? h('span', { class: 'badge', title: t.source }, 'IMPORT') : null),
    h('td', null, t.missing ? '' : h('a', { href: `#/tracks?q=${encodeURIComponent(t.artist)}` }, t.artist)),
    h('td', { class: 'album' }, t.album ? h('a', { href: `#/tracks?album=${t.albumId}&title=${encodeURIComponent(t.album)}` }, t.album) : ''),
    h('td', { class: 'plays', title: t.lastPlayed ? 'Last played ' + fmtDate(t.lastPlayed) : '' }, t.plays || ''),
    h('td', { class: 'dur' }, t.duration ? fmtTime(t.duration) : ''),
    h('td', { class: 'acts' },
      t.missing ? (playlist ? h('button', { onclick: () => playlist.remove(i) }, '✕') : null) : [
        h('button', { title: 'Play', onclick: () => playList(playable, playable.indexOf(t)) }, icon('play', 16)),
        h('button', { title: 'Start radio from this track', onclick: () => startRadio(t) }, icon('radio', 16)),
        fav,
        h('button', { title: 'More', onclick: e => { lastMenuEvent = e; menu(e, actions); } }, '⋯'),
      ]));
  });
  return h('table', { class: 'tracks' },
    h('thead', null, h('tr', null, th(null, '#', 'num'), th('title', 'Title'), th('artist', 'Artist'), th('album', 'Album', 'album'),
      th('plays', 'Plays', 'plays'), th('duration', 'Time', 'dur'), th(null, '', 'acts'))),
    h('tbody', null, rows));
}
let lastMenuEvent = null;

function searchBox(value, onSearch) {
  let timer;
  return h('input', {
    type: 'search', placeholder: 'Search title, artist, album, genre…', value: value || '',
    oninput: e => { clearTimeout(timer); timer = setTimeout(() => onSearch(e.target.value), 250); },
  });
}

function setQuery(q, changes) {
  for (const [k, v] of Object.entries(changes)) v ? q.set(k, v) : q.delete(k);
  const { parts } = parseHash();
  history.replaceState(null, '', `#/${parts.join('/') || 'tracks'}?${q}`);
}

async function renderTracks(token, q) {
  const PAGE = 300;
  let tracks = [], total = 0;
  const filterTitles = { favorites: 'Favorites', video: 'Music videos', imported: 'Imported', unplayed: 'Never played' };
  const title = q.get('title') || filterTitles[q.get('filter')] ||
    (q.get('sort') === 'added' ? 'Recently added' : q.get('sort') === 'plays' ? 'Most played' : 'Tracks');

  const fetchPage = async offset => {
    const p = new URLSearchParams(q);
    p.delete('title');
    p.set('offset', offset);
    p.set('limit', PAGE);
    return api.get('/api/tracks?' + p);
  };
  const first = await fetchPage(0);
  if (token !== viewToken) return;
  tracks = first.tracks;
  total = first.total;

  const tableWrap = h('div');
  const count = h('span', { class: 'muted' });
  const more = h('button', { onclick: async () => {
    const page = await fetchPage(tracks.length);
    tracks = tracks.concat(page.tracks);
    draw();
  } }, 'Load more');
  const draw = () => {
    count.textContent = `${total} tracks`;
    tableWrap.replaceChildren(tracks.length ? trackTable(tracks, {
      sort: q.get('sort'), order: q.get('order'),
      onSort: (sort, order) => { setQuery(q, { sort, order }); route(); },
    }) : h('div', { class: 'empty' }, total === 0 && !q.toString() ? 'No music indexed yet — is MUSIC_DIRS mounted? A scan may still be running.' : 'Nothing matches.'));
    more.hidden = tracks.length >= total;
    markPlaying();
  };
  view.replaceChildren(
    h('div', { class: 'toolbar' },
      h('h1', null, title),
      searchBox(q.get('q'), v => { setQuery(q, { q: v }); route(); }),
      h('button', { class: 'primary', onclick: () => playList(tracks) }, 'Play all'),
      h('button', { onclick: async () => {
        const p = new URLSearchParams(q); p.delete('title'); p.set('sort', 'random'); p.set('limit', 500);
        playList((await api.get('/api/tracks?' + p)).tracks);
      } }, 'Shuffle these'),
      h('button', { onclick: e => addToPlaylistMenu(e, tracks) }, '+ Add all to playlist'),
      count),
    tableWrap, more);
  draw();
}

// ---------------------------------------------------------------- albums / artists

function card(coverTrack, name, sub, onclick, placeholder = '♪') {
  const art = h('div', { class: 'art' }, placeholder);
  if (coverTrack) {
    const img = new Image();
    img.onload = () => { art.style.backgroundImage = `url("${img.src}")`; art.textContent = ''; };
    img.src = coverURL(coverTrack);
  }
  return h('div', { class: 'card', onclick }, art, h('div', { class: 'name ellipsis', title: name }, name), h('div', { class: 'muted small ellipsis' }, sub));
}

async function renderAlbums(token, q) {
  const p = new URLSearchParams(q);
  const albums = await api.get('/api/albums?' + p);
  if (token !== viewToken) return;
  const sortSel = h('select', { onchange: e => { const [s, o] = e.target.value.split(':'); setQuery(q, { sort: s, order: o }); route(); } },
    [['', 'Library order'], ['name:asc', 'Name'], ['artist:asc', 'Artist'], ['year:desc', 'Newest'], ['added:desc', 'Recently added'], ['plays:desc', 'Most played']]
      .map(([v, l]) => h('option', { value: v, selected: v === `${q.get('sort')}:${q.get('order')}` }, l)));
  view.replaceChildren(
    h('div', { class: 'toolbar' }, h('h1', null, 'Albums'), searchBox(q.get('q'), v => { setQuery(q, { q: v }); route(); }), sortSel,
      h('span', { class: 'muted' }, `${albums.length} albums`)),
    albums.length ? h('div', { class: 'grid' }, albums.map(a => card(a.coverTrack, a.album, [a.artist, a.year].filter(Boolean).join(' · '),
      () => { location.hash = `#/tracks?album=${a.id}&title=${encodeURIComponent(a.album)}`; })))
      : h('div', { class: 'empty' }, 'No albums.'));
}

async function renderArtists(token, q) {
  const artists = await api.get('/api/artists?' + new URLSearchParams(q));
  if (token !== viewToken) return;
  const sortSel = h('select', { onchange: e => { setQuery(q, { sort: e.target.value }); route(); } },
    [['', 'Name'], ['plays', 'Most played'], ['tracks', 'Most tracks']].map(([v, l]) => h('option', { value: v, selected: v === (q.get('sort') || '') }, l)));
  view.replaceChildren(
    h('div', { class: 'toolbar' }, h('h1', null, 'Artists'), searchBox(q.get('q'), v => { setQuery(q, { q: v }); route(); }), sortSel,
      h('span', { class: 'muted' }, `${artists.length} artists`)),
    h('div', { class: 'grid' }, artists.map(a => card(a.coverTrack, a.name, `${a.tracks} tracks${a.plays ? ` · ${a.plays} plays` : ''}`,
      () => { location.hash = `#/tracks?artist=${a.id}&title=${encodeURIComponent(a.name)}`; }, icon('person', 56)))));
}

// ---------------------------------------------------------------- playlist

async function renderPlaylist(token, id) {
  const pl = await api.get(`/api/playlists/${id}`);
  if (token !== viewToken) return;
  const save = async ids => {
    await api.patch(`/api/playlists/${id}`, { tracks: ids });
    loadPlaylists();
    route();
  };
  const ids = pl.tracks.map(t => t.id);
  const ctl = {
    remove: i => { ids.splice(i, 1); save(ids).catch(fail); },
    move: (i, d) => { [ids[i], ids[i + d]] = [ids[i + d], ids[i]]; save(ids).catch(fail); },
  };
  const urlIn = h('input', { type: 'url', placeholder: 'Paste a YouTube URL to download it into this playlist (audio + video)' });
  const importForm = h('form', { class: 'panel-form', onsubmit: async e => {
    e.preventDefault();
    try {
      await api.post('/api/downloads', { url: urlIn.value, playlistId: id });
      urlIn.value = '';
      toast('Importing… it will appear here when done');
      watchImports(token, () => route());
    } catch (err) { fail(err); }
  } }, urlIn, h('button', { class: 'primary' }, 'Import'));
  const dur = pl.tracks.reduce((s, t) => s + (t.duration || 0), 0);
  view.replaceChildren(
    h('div', { class: 'toolbar' },
      h('h1', null, pl.name),
      h('span', { class: 'muted' }, `${pl.tracks.length} tracks · ${fmtTime(dur)}`),
      h('button', { class: 'primary', onclick: () => playList(pl.tracks.filter(t => !t.missing)) }, 'Play'),
      h('button', { onclick: () => { const ts = pl.tracks.filter(t => !t.missing).sort(() => Math.random() - .5); playList(ts); } }, 'Shuffle'),
      h('button', { onclick: async () => {
        const name = prompt('Rename playlist', pl.name);
        if (name) { await api.patch(`/api/playlists/${id}`, { name }).catch(fail); loadPlaylists(); route(); }
      } }, 'Rename'),
      h('button', { onclick: async () => {
        if (!confirm(`Delete playlist “${pl.name}”? The tracks themselves stay.`)) return;
        await api.del(`/api/playlists/${id}`).catch(fail);
        await loadPlaylists();
        location.hash = '#/tracks';
      } }, 'Delete')),
    importStatus.available ? importForm : null,
    pl.tracks.length ? trackTable(pl.tracks, { playlist: ctl }) : h('div', { class: 'empty' }, 'Empty. Add tracks with ⋯ → Add to playlist, or import a URL above.'));
}

// ---------------------------------------------------------------- radio

async function renderRadio(token) {
  const stations = await api.get('/api/stations');
  if (token !== viewToken) return;
  const urlIn = h('input', { type: 'url', placeholder: 'Stream URL or link to a .pls / .m3u', required: true });
  const nameIn = h('input', { type: 'text', placeholder: 'Name (optional)', style: 'flex:0 1 200px;min-width:120px' });
  const fileIn = h('input', { type: 'file', accept: '.pls,.m3u,.m3u8', hidden: true, onchange: async () => {
    const fd = new FormData();
    fd.append('file', fileIn.files[0]);
    try { const added = await api.req('POST', '/api/stations', fd); toast(`Added ${added.length} station(s)`); route(); } catch (e) { fail(e); }
  } });
  view.replaceChildren(
    h('div', { class: 'toolbar' }, h('h1', null, 'Radio')),
    h('form', { class: 'panel-form', onsubmit: async e => {
      e.preventDefault();
      try { await api.post('/api/stations', { url: urlIn.value, name: nameIn.value }); route(); } catch (err) { fail(err); }
    } }, urlIn, nameIn, h('button', { class: 'primary' }, 'Add station'), h('button', { type: 'button', onclick: () => fileIn.click() }, 'Upload .pls / .m3u'), fileIn),
    stations.length ? h('div', { class: 'list' }, stations.map(st => h('div', { class: 'row', 'data-station': st.id },
      h('button', { class: 'icon', title: 'Play', onclick: () => playStation(st) }, icon('play')),
      h('div', { class: 't' }, h('div', { class: 'ellipsis' }, st.name), h('div', { class: 'muted small ellipsis' }, st.url)),
      h('span', { class: 'muted small' }, st.plays ? `${st.plays} plays` : ''),
      h('button', { class: 'icon' + (st.favorite ? ' on' : ''), title: 'Favorite', onclick: async () => {
        await api.patch(`/api/stations/${st.id}`, { favorite: !st.favorite }).catch(fail); route();
      } }, st.favorite ? '★' : '☆'),
      h('button', { onclick: async () => {
        const name = prompt('Station name', st.name);
        if (name) { await api.patch(`/api/stations/${st.id}`, { name }).catch(fail); route(); }
      } }, 'Rename'),
      h('button', { onclick: async () => { if (confirm(`Remove “${st.name}”?`)) { await api.del(`/api/stations/${st.id}`).catch(fail); route(); } } }, '✕'))))
      : h('div', { class: 'empty' }, 'No stations yet. Paste a stream URL, a link to a .pls file, or upload one.'));
}

// ---------------------------------------------------------------- import

let importStatus = { available: false };

// Poll while imports are active and this view is open. Polling only then
// matters behind idlegate: a page that polled forever would keep the server
// awake for as long as a tab is open.
function watchImports(token, onChange) {
  clearInterval(poller);
  let lastDone = null;
  poller = setInterval(async () => {
    if (token !== viewToken) { clearInterval(poller); return; }
    if (document.hidden) return;
    const res = await api.get('/api/downloads').catch(() => null);
    if (!res) return;
    const active = res.jobs.some(j => j.status === 'queued' || j.status === 'running');
    const done = res.jobs.filter(j => j.status === 'done' || j.status === 'failed').length;
    if (lastDone !== null && done !== lastDone) onChange(res);
    lastDone = done;
    if (!active) { clearInterval(poller); poller = null; onChange(res); }
  }, 2000);
}

async function renderImport(token) {
  const res = await api.get('/api/downloads');
  importStatus.available = res.available;
  if (token !== viewToken) return;
  const urlIn = h('input', { type: 'url', placeholder: 'https://www.youtube.com/watch?v=…', required: true });
  const plSel = h('select', null, h('option', { value: '' }, 'No playlist'), playlists.map(p => h('option', { value: p.id }, p.name)));
  const jobs = h('div', { class: 'list' });
  const drawJobs = list => jobs.replaceChildren(...(list.length ? list.map(j => h('div', { class: 'row' },
    h('div', { class: 't' },
      h('div', { class: 'ellipsis' }, j.title || j.url),
      h('div', { class: 'muted small ellipsis' }, j.url),
      j.error ? h('div', { class: 'small error' }, j.error) : null),
    j.status === 'running' ? h('progress', { max: 100, value: j.progress }) : h('span', { class: 'muted small' }, j.status),
    j.status === 'done' && j.trackId ? h('button', { onclick: async () => playList([await api.get(`/api/tracks/${j.trackId}`)]) }, 'Play') : null,
    j.status === 'failed' ? h('button', { onclick: () => api.post(`/api/downloads/${j.id}/retry`).then(refresh).catch(fail) }, 'Retry') : null,
    h('button', { title: j.status === 'running' ? 'Cancel' : 'Remove from list (files stay)', onclick: () => api.del(`/api/downloads/${j.id}`).then(refresh).catch(fail) }, '✕')))
    : [h('div', { class: 'empty' }, 'No imports yet.')]));
  const refresh = async () => {
    const r = await api.get('/api/downloads');
    drawJobs(r.jobs);
    if (r.jobs.some(j => j.status === 'queued' || j.status === 'running')) live();
  };
  // Live progress needs a faster redraw than "something finished".
  const live = () => {
    clearInterval(poller);
    poller = setInterval(async () => {
      if (token !== viewToken) { clearInterval(poller); return; }
      if (document.hidden) return;
      const r = await api.get('/api/downloads').catch(() => null);
      if (!r) return;
      drawJobs(r.jobs);
      if (!r.jobs.some(j => j.status === 'queued' || j.status === 'running')) { clearInterval(poller); poller = null; }
    }, 1500);
  };
  view.replaceChildren(
    h('div', { class: 'toolbar' }, h('h1', null, 'Import from URL')),
    res.available ? h('form', { class: 'panel-form', onsubmit: async e => {
      e.preventDefault();
      try {
        await api.post('/api/downloads', { url: urlIn.value, playlistId: plSel.value });
        urlIn.value = '';
        await refresh();
      } catch (err) { fail(err); }
    } }, urlIn, plSel, h('button', { class: 'primary' }, 'Download')) :
      h('p', { class: 'error' }, 'Imports are unavailable: the server needs a writable DOWNLOAD_DIR and yt-dlp (both are in the Docker image).'),
    h('p', { class: 'muted small' }, 'Downloads the video (up to 1080p) and its audio into your library as one track with a music video. ' +
      'If the server is stopped mid-download (e.g. idlegate putting it to sleep), the download resumes the next time it starts.'),
    jobs);
  drawJobs(res.jobs);
  if (res.jobs.some(j => j.status === 'queued' || j.status === 'running')) live();
}

// ---------------------------------------------------------------- stats

async function renderStats(token) {
  const s = await api.get('/api/stats');
  if (token !== viewToken) return;
  const tile = (v, l) => h('div', { class: 'tile' }, h('div', { class: 'v' }, v), h('div', { class: 'muted small' }, l));
  const hours = s.seconds / 3600;
  view.replaceChildren(
    h('div', { class: 'toolbar' }, h('h1', null, 'Stats')),
    h('div', { class: 'stat-tiles' },
      tile(s.tracks, 'tracks'), tile(s.plays, 'plays'), tile(hours >= 1 ? hours.toFixed(1) + ' h' : Math.round(s.seconds / 60) + ' min', 'listened'), tile(s.favorites, 'favorites')),
    h('div', { class: 'cols' },
      h('div', null, h('h3', null, 'Top tracks'), s.topTracks.length ? trackTable(s.topTracks) : h('div', { class: 'empty' }, 'Play something!')),
      h('div', null, h('h3', null, 'Top artists'), h('div', { class: 'list' }, s.topArtists.map(a => h('a', { class: 'row', href: `#/tracks?artist=${a.id}&title=${encodeURIComponent(a.name)}` },
        h('div', { class: 't ellipsis' }, a.name), h('span', { class: 'muted' }, `${a.plays} plays`)))))),
    h('h3', null, 'Recently played'),
    h('div', { class: 'list' }, (s.recent || []).map(r => h('div', { class: 'row', 'data-track': r.id },
      h('button', { class: 'icon', onclick: () => playList([r]) }, icon('play')),
      h('div', { class: 't' }, h('div', { class: 'ellipsis' }, r.title), h('div', { class: 'muted small ellipsis' }, r.artist)),
      h('span', { class: 'muted small' }, fmtDate(r.at))))));
}

// ---------------------------------------------------------------- status

async function refreshStatus() {
  try {
    const st = await api.get('/api/status');
    importStatus.available = st.imports;
    $('#nav-import').hidden = false;
    const el = $('#scan-status');
    const hc = st.hotCache.max ? `SSD cache: ${st.hotCache.tracks} tracks, ${(st.hotCache.used / 2 ** 30).toFixed(1)}/${(st.hotCache.max / 2 ** 30).toFixed(0)} GB` : '';
    el.replaceChildren(
      h('div', null, st.scanning ? `Scanning… ${st.scanned} files` : `${st.tracks} tracks`),
      hc ? h('div', null, hc) : null,
      st.scanning ? null : h('a', { href: '#', onclick: e => { e.preventDefault(); api.post('/api/scan').then(() => setTimeout(refreshStatus, 500)).catch(fail); } }, 'Rescan'));
    // Poll only while a scan runs (see watchImports for why).
    if (st.scanning) setTimeout(refreshStatus, 3000);
    else if (wasScanning) route();
    wasScanning = st.scanning;
  } catch (e) { console.error(e); }
}
let wasScanning = false;

$('#now-cover').addEventListener('error', e => { e.target.style.visibility = 'hidden'; });
loadPlaylists().catch(fail);
updateNow();
refreshStatus().finally(route);
