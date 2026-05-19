import './style.css';
import {
  Login, Logout, Me, Search,
  AddJob, ListJobs, PauseJob, ResumeJob, CancelJob, RemoveJob, ClearFinishedJobs,
  PreviewJobName,
  PickFolder, OpenOutputFolder,
  ListPresets, GetPreset, SavePreset, DeletePreset, SetPresetAutoPull,
  ListPresetsDetailed, RunPresetNow,
  GetWindowSettings, SaveWindowSettings,
  GetAppSettings, SaveAppSettings,
  ManifestKeys, OpenManifestFolder, DeleteManifest, RebuildManifest,
  TagSuggest, CheckUser, SuggestUsers, BlockedTagCount,
  PostComments,
  TestNetwork,
} from '../wailsjs/go/main/GUI';
import { EventsOn, BrowserOpenURL } from '../wailsjs/runtime/runtime';

const LS_SHOW_AUTHOR     = 'jrdl:showAuthor';
const LS_SHOW_RATING     = 'jrdl:showRating';
const LS_SHOW_PAGE_RANGE = 'jrdl:showPageRange';
const LS_OUTDIR          = 'jrdl:outdir';
const LS_FILENAME_FORMAT = 'jrdl:filenameFormat';
const LS_PREVIEW_BATCH   = 'jrdl:previewBatch';
const LS_LAST_PRESET     = 'jrdl:lastPreset';
const LS_MANUAL_SELECT   = 'jrdl:manualSelect';

const lsBool = (key, def) => {
  const v = localStorage.getItem(key);
  return v == null ? def : v === '1';
};
const lsStr = (key, def) => localStorage.getItem(key) ?? def;
const lsInt = (key, def) => {
  const n = parseInt(localStorage.getItem(key) ?? '', 10);
  return Number.isFinite(n) && n > 0 ? n : def;
};

// ----- State -----
const state = {
  user: '',
  tags: [],
  excludeTags: [],
  sort: 'rating',
  kinds: [],               // empty = any; subset of ['image','gif','video']
  showAuthor: lsBool(LS_SHOW_AUTHOR, false),
  showRating: lsBool(LS_SHOW_RATING, false),
  showPageRange: lsBool(LS_SHOW_PAGE_RANGE, false),
  filenameFormat: lsStr(LS_FILENAME_FORMAT, 'id'),
  previewBatch: lsInt(LS_PREVIEW_BATCH, 25),  // posts to fetch per Найти / Загрузить ещё click
  settingsOpen: false,
  searching: false,
  count: null,
  exhausted: false,         // set when doSearch concludes the API has nothing more to give for the current filters
  page: 1,
  results: [],
  jobs: [],
  formInputs: {
    'f-use-blocked': true,                  // blocked-tags filter is on by default
    'f-outdir': lsStr(LS_OUTDIR, ''),       // remember last-used download folder
  },
  presets: [],
  currentPreset: '',
  currentPresetAutoPull: false,
  blockedCount: 0,
  postModal: null,   // { post, comments, loading, error } when the right-click preview is open
  windowSettings: { width: 1180, height: 820, maximized: false },
  appSettings: { manifestMode: 'per-folder', autoPullIntervalHours: 24, autostart: false, startMinimized: false, minimizeToTrayOnClose: false, hideRemovedPosts: true, socks5Enabled: false, socks5Addr: '', onionBaseURL: '', recoverDmcaViaOnion: false },
  // Last network-test result, shown next to the "Проверить" button until the
  // user changes any field. {state:'idle'|'testing'|'ok'|'error', text:'...'}.
  networkTest: { state: 'idle' },
  downloaded: { outDir: '', keys: new Set() },  // attr IDs in the current outdir's .manifest.json
  selectedPosts: new Set(),  // postId strings the user manually picked for selective download
  manualSelect: lsBool(LS_MANUAL_SELECT, false),  // master toggle for the per-tile checkbox UI
  queueOpen: false,                                // job-queue modal visibility
  presetsManagerOpen: false,                       // preset-manager modal visibility
  presetViews: [],                                 // PresetView[] from ListPresetsDetailed, populated when modal opens
};

// Preset-manager countdown tick handle. setInterval id, or 0 when no tick
// is running. Started in openPresetsManager, stopped in closePresetsManager.
let _presetsTickTimer = 0;
// Bookkeeping so the 60th tick can refresh the backend payload.
let _presetsTickCount = 0;

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];

function escape(s) {
  return String(s ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
}

// ----- Render -----
// render() coalesces calls through requestAnimationFrame so a burst of
// job:update events (one per downloaded picture) collapses to at most one
// DOM rebuild per ~16ms frame — eliminates flicker and CPU thrash during
// active downloads. Callers can pass {immediate: true} to force a sync
// render when the user just did something and expects instant feedback.
let _renderScheduled = false;
let _pendingOpts = {};
let _deferTimer = 0;
function render(opts = {}) {
  // Merge opts in case multiple callers queue up before the frame fires.
  // skipCapture is sticky: any caller that asks to skip wins, since the
  // captureInputs side-effect would clobber freshly-mutated state.
  if (opts.skipCapture) _pendingOpts.skipCapture = true;
  if (opts.immediate) {
    _pendingOpts = { ..._pendingOpts, ...opts };
    return doRender();
  }
  if (_renderScheduled) return;
  _renderScheduled = true;
  requestAnimationFrame(() => {
    _renderScheduled = false;
    doRender();
  });
}

// bgRender — debounced variant for background-driven renders (the
// job:update flood during mass downloads, ~60 events/sec). The rAF-
// coalesced render() still fires up to 60 times/sec, and each fire
// rebuilds #app.innerHTML wholesale — which is visible as flicker on
// hovers, chip rows, and the tag autocomplete root, even though almost
// nothing in the shell actually depends on per-Fetch progress.
//
// Debouncing to 250ms collapses bursts to ≤4 renders/sec — still feels
// "live" inside the queue modal (the only place per-pic counters are
// shown), and inside-folder green check-marks have their own 1Hz
// throttle so they're unaffected. Final state transitions still arrive
// promptly because Go emits job:update on done/canceled/error too —
// the 250ms debounce just spaces them out instead of dropping any.
let _bgRenderTimer = 0;
function bgRender(opts = {}) {
  if (opts.skipCapture) _pendingOpts.skipCapture = true;
  if (_bgRenderTimer) return;
  _bgRenderTimer = setTimeout(() => {
    _bgRenderTimer = 0;
    render();
  }, 250);
}

// shouldDeferRender returns true only while an autocomplete dropdown is
// actually visible — wiping #app innerHTML mid-selection would destroy
// the dropdown before the user can click an item. We do NOT defer just
// because an input is focused: that would block re-renders forever while
// the user is typing (and break tag-pick → chip-appears flow, since after
// pick the input keeps focus).
function shouldDeferRender() {
  return !!document.querySelector('.autocomplete');
}

function doRender() {
  if (!_pendingOpts.immediate && shouldDeferRender()) {
    // Re-poll every 250ms; as soon as the dropdown closes, render with
    // the accumulated state. _pendingOpts is intentionally not cleared so
    // skipCapture (and any other sticky flag) survives until the flush.
    if (!_deferTimer) {
      _deferTimer = setTimeout(() => { _deferTimer = 0; render(); }, 250);
    }
    return;
  }
  const opts = _pendingOpts;
  _pendingOpts = {};
  // Save focused element + caret position so re-rendering doesn't yank
  // the cursor out from under the user. Most rebuilds happen on background
  // job:update events while the user is actively editing a filter field.
  const focusedId = document.activeElement?.id || '';
  let focusedSel = null;
  if (focusedId) {
    const a = document.activeElement;
    if (a && (a.tagName === 'INPUT' || a.tagName === 'TEXTAREA')) {
      try { focusedSel = { start: a.selectionStart, end: a.selectionEnd }; } catch {}
    }
  }
  if (!opts.skipCapture) captureInputs();
  // Preserve scroll positions across the full innerHTML rebuild, so loading
  // more results (or any background event) doesn't yank the user back to the
  // top of any scrollable pane. Includes modal-internal scrolls (Settings
  // dialog, Queue table) — otherwise opening Settings and waiting for a
  // job:update would scroll the modal back to top.
  const scrollSelectors = [
    '.main',
    '.preview-scroll',
    '.post-overlay-pics',
    '.comments-list',
    '.modal.wide',           // Settings modal (overflow-y:auto on the modal itself)
    '.modal.queue-modal',    // Queue modal outer
    '.queue-table-wrap',     // Queue table scroll container
  ];
  const prevScrolls = Object.fromEntries(
    scrollSelectors.map(s => [s, $(s)?.scrollTop ?? 0])
  );
  document.querySelector('#app').innerHTML = `
    <div class="shell">
      ${renderTopbar()}
      <div class="main">
        <div class="split">
          ${renderPreviewCard()}
          ${renderFiltersCard()}
        </div>
      </div>
      ${state.settingsOpen ? renderSettingsModal() : ''}
      ${state.queueOpen ? renderQueueModal() : ''}
      ${state.presetsManagerOpen ? renderPresetsManagerModal() : ''}
      ${state.postModal ? renderPostPreview() : ''}
    </div>
  `;
  restoreInputs();
  wireEvents();
  // Re-apply focus + caret to the same element id (if it still exists in
  // the rebuilt DOM). Without this, every background re-render kicks the
  // cursor out of whatever field the user was typing in.
  if (focusedId) {
    const el = document.getElementById(focusedId);
    if (el && typeof el.focus === 'function') {
      el.focus();
      if (focusedSel && typeof el.setSelectionRange === 'function') {
        try { el.setSelectionRange(focusedSel.start, focusedSel.end); } catch {}
      }
    }
  }
  for (const sel of scrollSelectors) {
    const el = $(sel);
    if (el) el.scrollTop = prevScrolls[sel];
  }
}

// Toast lifecycle is intentionally separate from the main render() loop.
// The shell does innerHTML rebuilds on every job:update event (one per
// downloaded picture) which would re-trigger the CSS animation and reset
// the auto-dismiss timer — making it look like a new toast on every
// progress tick. Instead we keep a single persistent <div> in document.body
// and mutate it imperatively.
let _toastTimer = 0;

function showToast(kind, text) {
  clearTimeout(_toastTimer);
  let el = document.getElementById('toast-elem');
  const isNew = !el;
  if (!el) {
    el = document.createElement('div');
    el.id = 'toast-elem';
    document.body.appendChild(el);
  }
  el.className = `toast ${kind}`;
  el.setAttribute('role', 'status');
  el.innerHTML = `
    <span class="toast-text">${escape(text)}</span>
    ${kind === 'error' ? `<button class="toast-close" title="Закрыть">✕</button>` : ''}
  `;
  el.querySelector('.toast-close')?.addEventListener('click', hideToast);
  // Replay the entry animation only when the element is brand-new — updating
  // the same toast shouldn't make it bounce in again.
  if (isNew) {
    el.classList.add('toast-enter');
    requestAnimationFrame(() => el.classList.remove('toast-enter'));
  }
  if (kind === 'success') {
    _toastTimer = setTimeout(hideToast, 4000);
  }
}

function hideToast() {
  clearTimeout(_toastTimer);
  document.getElementById('toast-elem')?.remove();
}

function renderTopbar() {
  const active = countActive();
  const total = state.jobs.length;
  const queueBadge = total > 0
    ? `<span class="topbar-badge ${active > 0 ? 'active' : ''}">${total}</span>`
    : '';
  return `
    <div class="topbar">
      <div class="title">Joyreactor Downloader</div>
      <button class="icon-btn gear" id="btn-settings" title="Настройки скачивания">⚙</button>
      <button class="topbar-btn" id="btn-presets-manager" title="Менеджер пресетов: расписания автовыгрузок, быстрый запуск, удаление">
        📑 Пресеты
      </button>
      <button class="topbar-btn" id="btn-queue" title="Очередь задач">
        📋 Очередь${queueBadge}
      </button>
      <div class="spacer"></div>
      <div class="auth">
        ${state.user
          ? `<span>вошёл как <span class="user">${escape(state.user)}</span></span>
             <button class="btn" id="btn-logout">Выйти</button>`
          : `<span>гость</span>
             <button class="btn" id="btn-login">Войти</button>`}
      </div>
    </div>`;
}

// renderPresetBar — second sticky row right below the topbar. Holds the
// preset selector + its save/delete buttons, the output directory input,
// and the «Добавить в очередь» action. Outdir lives here (not in a bottom
// bar) because it's now a per-preset field — visually grouped with the
// preset controls so the "this folder belongs to this preset" mental
// model is obvious at a glance.
// renderPresetSection — preset selector + save/delete, lives at the top of
// the filters card so all "what to download" choices stack vertically in one
// column. Was a separate top bar previously.
function renderPresetSection() {
  const opts = ['<option value="">— пресет —</option>']
    .concat(state.presets.map(n => `<option value="${escape(n)}" ${n === state.currentPreset ? 'selected' : ''}>${escape(n)}</option>`))
    .join('');
  const autoPullDisabled = state.currentPreset ? '' : 'disabled';
  const autoPullChecked = state.currentPresetAutoPull ? 'checked' : '';
  const interval = state.appSettings.autoPullIntervalHours || 24;
  const autoPullTitle = state.currentPreset
    ? `Раз в ${interval} ч сам докачивать новые картинки этого пресета в его папку (интервал меняется в Настройках)`
    : 'Сначала выбери пресет — авто-обновление работает только для сохранённых';
  return `
    <div class="field">
      <label>Пресет</label>
      <div class="preset-control">
        <select id="preset-sel" title="Выбрать пресет фильтров">${opts}</select>
        <button class="btn small" id="btn-preset-save" title="Сохранить текущие фильтры как пресет">Сохранить как…</button>
        <button class="btn small" id="btn-preset-delete" ${state.currentPreset ? '' : 'disabled'}>Удалить</button>
      </div>
      <label class="preset-autopull" title="${autoPullTitle}">
        <input type="checkbox" id="preset-autopull" ${autoPullChecked} ${autoPullDisabled}>
        🔄 Авто-обновлять этот пресет
      </label>
    </div>`;
}

// renderOutdirSection — output directory input + picker + open-in-explorer.
// Sits right after the preset so the preset/folder pair stays grouped.
function renderOutdirSection() {
  const hasOutdir = !!(state.formInputs['f-outdir'] || '').trim();
  const hint = state.currentPreset
    ? `привязана к пресету «${escape(state.currentPreset)}»`
    : 'без пресета — папка сохраняется в localStorage';
  return `
    <div class="field">
      <label>Папка для скачивания</label>
      <div class="outdir">
        <input type="text" id="f-outdir" placeholder="Папка…">
        <button class="btn" id="btn-pick">Выбрать…</button>
        <button class="icon-btn" id="btn-open-outdir" title="Открыть папку в проводнике" ${hasOutdir ? '' : 'disabled'}>📂</button>
      </div>
      <div class="field-hint">${hint}</div>
    </div>`;
}

function renderFiltersCard() {
  return `
    <div class="card">
      <h2>Поиск</h2>
      ${renderPresetSection()}
      ${renderOutdirSection()}
      <div class="field">
        <label>Запрос</label>
        <input type="text" id="f-query" placeholder="свободный текст…">
      </div>
      <div class="field">
        <label>Теги (Enter — добавить)</label>
        <div class="chips" id="chips-tags">
          ${state.tags.map((t, i) => chip(t, 'tag', i)).join('')}
          <input type="text" id="f-tag-input" placeholder="+ новый тег" autocomplete="off">
        </div>
      </div>
      <div class="field">
        <label>Исключить теги</label>
        <div class="chips" id="chips-excl">
          ${state.excludeTags.map((t, i) => chip(t, 'excl', i)).join('')}
          <input type="text" id="f-excl-input" placeholder="+ исключить" autocomplete="off">
        </div>
      </div>
      ${state.showAuthor ? `
        <div class="field has-ac">
          <label>Автор поста</label>
          <input type="text" id="f-user" placeholder="username" autocomplete="off">
          <div class="field-hint" id="user-hint"></div>
        </div>` : ''}
      ${state.showRating ? `
        <div class="row">
          <div class="field">
            <label>Рейтинг от</label>
            <input type="number" id="f-min-rating" placeholder="-∞">
          </div>
          <div class="field">
            <label>Рейтинг до</label>
            <input type="number" id="f-max-rating" placeholder="+∞">
          </div>
        </div>` : ''}
      <div class="field">
        <label>Сортировка${state.showPageRange ? ' · диапазон страниц' : ''}</label>
        <div class="sort-row">
          <div class="segmented" id="seg-sort">
            <button data-v="rating" class="${state.sort === 'rating' ? 'active' : ''}">рейтинг</button>
            <button data-v="date"   class="${state.sort === 'date'   ? 'active' : ''}">дата</button>
          </div>
          ${state.showPageRange ? `
            <div class="page-range" title="JR отдаёт ~10 постов на страницу. Пусто/0 = без ограничения с этой стороны.">
              <input type="number" id="f-page-from" placeholder="с" min="1" aria-label="С какой страницы">
              <span class="page-range-sep">—</span>
              <input type="number" id="f-page-to"   placeholder="по" min="0" aria-label="По какую страницу">
              <span class="muted">стр.</span>
            </div>` : ''}
        </div>
      </div>
      <div class="field">
        <label>Флаги</label>
        <div class="toggles">
          <label><input type="checkbox" id="f-nsfw"> Показывать NSFW</label>
          <label><input type="checkbox" id="f-only-nsfw"> Только NSFW</label>
          <label><input type="checkbox" id="f-unsafe"> Показывать unsafe</label>
          <label class="${state.user ? '' : 'disabled'}">
            <input type="checkbox" id="f-favorite" ${state.user ? '' : 'disabled'}> Только избранное
          </label>
          <label class="full ${state.user ? '' : 'disabled'}" title="${state.user ? `Исключить ${state.blockedCount} тег(а/ов) из твоего профиля` : 'нужен логин'}">
            <input type="checkbox" id="f-use-blocked" ${state.user ? '' : 'disabled'}>
            Учитывать заблокированные теги${state.user && state.blockedCount > 0 ? ` <span class="muted">(${state.blockedCount})</span>` : ''}
          </label>
        </div>
      </div>
      <div class="actions-row search-actions">
        <button class="btn primary big" id="btn-search" ${state.searching ? 'disabled' : ''}>
          ${state.searching ? 'Ищу…' : '🔍 Найти'}
        </button>
        ${renderAddButton()}
        ${renderFoundCount()}
      </div>
    </div>`;
}

// renderFoundCount — JR's `Query.search` doesn't accept a media-type
// argument, so res.PostPager.Count is always the unfiltered total. When
// the local "тип медиа" toggle is active that number can wildly overshoot
// what the user will actually see, so flag the label explicitly.
function renderFoundCount() {
  if (state.count === null) return '';
  // JR's GraphQL Query.search caps PostPager.Count at 1000 for wide
  // searches — the field tops out at exactly 1000 even when the true
  // total is larger. Empirically verified, no schema docs say so.
  // Show "+" when we hit that ceiling to make it clear the number is
  // a floor, not the exact total.
  const totalStr = `${state.count}${state.count >= 1000 ? '+' : ''}`;
  if (state.kinds.length > 0) {
    return `<span class="muted" title="JoyReactor не умеет фильтровать по типу медиа на сервере — это общее число постов в поиске. Тип отбирается уже в приложении, см. «Превью» ниже.">
      Всего по поиску: <strong class="ink">${totalStr}</strong>
      <span class="muted-inline">(тип фильтрую локально)</span>
    </span>`;
  }
  return `<span class="muted">Найдено: <strong class="ink">${totalStr}</strong></span>`;
}

// renderAddButton — the "＋ Добавить в очередь" action sits next to "Найти"
// in the same row so the two primary buttons read side-by-side. In manual
// mode the label switches to "Добавить выбранные (N)" with a "Сброс" sibling.
function renderAddButton() {
  const selectedN = state.manualSelect ? state.selectedPosts.size : 0;
  if (selectedN > 0) {
    return `
      <button class="btn primary big" id="btn-add" title="Скачать только выбранные посты">＋ Добавить выбранные (${selectedN})</button>
      <button class="btn small" id="btn-clear-selection" title="Снять выделение со всех плиток">Сброс</button>`;
  }
  return `<button class="btn primary big" id="btn-add">＋ Добавить в очередь</button>`;
}

function renderSettingsModal() {
  const kindChecked = k => state.kinds.includes(k) ? 'checked' : '';
  return `
    <div class="modal-backdrop" id="settings-backdrop">
      <div class="modal wide" id="settings-modal">
        <div class="modal-header">
          <h3>Настройки скачивания</h3>
          <button class="icon-btn" id="btn-settings-close" title="Закрыть">✕</button>
        </div>

        <div class="settings-section">
          <h4>Окно</h4>
          <div class="row">
            <div class="field">
              <label>Ширина (px)</label>
              <input type="number" id="s-win-w" min="600" max="9999" value="${state.windowSettings.width}" ${state.windowSettings.maximized ? 'disabled' : ''}>
            </div>
            <div class="field">
              <label>Высота (px)</label>
              <input type="number" id="s-win-h" min="400" max="9999" value="${state.windowSettings.height}" ${state.windowSettings.maximized ? 'disabled' : ''}>
            </div>
          </div>
          <div class="toggles">
            <label>
              <input type="checkbox" id="s-win-max" ${state.windowSettings.maximized ? 'checked' : ''}>
              Развернуть на весь доступный экран (с панелью задач)
            </label>
          </div>
          <div class="field-hint">При включении окно растянется на всё рабочее пространство, но не перекроет панель задач Windows. Ширина/высота сохранятся для случая, когда галку снимут.</div>
        </div>

        <div class="settings-section">
          <h4>Автозапуск и фон</h4>
          <div class="toggles">
            <label>
              <input type="checkbox" id="s-autostart" ${state.appSettings.autostart ? 'checked' : ''}>
              Запускать при старте системы
            </label>
            <label>
              <input type="checkbox" id="s-start-min" ${state.appSettings.startMinimized ? 'checked' : ''}>
              Открывать свёрнутым в трей
            </label>
            <label>
              <input type="checkbox" id="s-min-on-close" ${state.appSettings.minimizeToTrayOnClose ? 'checked' : ''}>
              При закрытии окна (✕) сворачивать в трей вместо выхода
            </label>
          </div>
          <div class="field-hint">
            Иконка в системном трее позволяет приложению работать в фоне:
            планировщик авто-обновления пресетов тикает только пока процесс
            запущен. Чтобы полностью выйти — правый клик по иконке в трее →
            «Выход».
          </div>
        </div>

        <div class="settings-section">
          <h4>Превью</h4>
          <div class="toggles">
            <label>
              <input type="checkbox" id="s-hide-removed" ${state.appSettings.hideRemovedPosts !== false ? 'checked' : ''}>
              Скрывать удалённые посты
            </label>
          </div>
          <div class="field-hint">
            Посты, удалённые с JR по жалобе на копирайт. Картинки физически
            недоступны — остаётся только превьюшка. Когда галка снята, такие
            посты показываются в гриде с серой ватермаркой «🚫 удалено», но
            скачать их всё равно нельзя.
          </div>
        </div>

        <div class="settings-section">
          <h4>Авто-обновление пресетов</h4>
          <div class="field">
            <label>Глобальный интервал между авто-обновлениями (часы)</label>
            <input type="number" id="s-autopull-interval" min="1" max="720"
                   value="${state.appSettings.autoPullIntervalHours || 24}">
            <div class="field-hint">
              Если в выпадайке пресетов отмечен чекбокс «🔄 Авто», такой
              пресет раз в N часов сам докачивает новые картинки в свою
              папку. Минимум 1 час. По умолчанию 24 (раз в сутки).
              Глобальный интервал общий для всех «автоматических» пресетов.
            </div>
          </div>
        </div>

        <div class="settings-section">
          <h4>Манифест дедупликации</h4>
          <div class="manifest-mode">
            <label>
              <input type="radio" name="manifest-mode" value="per-folder" ${state.appSettings.manifestMode === 'per-folder' ? 'checked' : ''}>
              <span>
                <strong>В каждой папке</strong>
                <code class="muted">&lt;outDir&gt;/.manifest.json</code>
              </span>
            </label>
            <label>
              <input type="radio" name="manifest-mode" value="global" ${state.appSettings.manifestMode === 'global' ? 'checked' : ''}>
              <span>
                <strong>Общий для всех папок</strong>
                <code class="muted">%APPDATA%/joyreactorDownloader/manifest.json</code>
              </span>
            </label>
          </div>
          <div class="field-hint">
            В режиме «общий» одна и та же картинка не качается повторно, даже
            если выбрана другая папка для скачивания. Удобно если ты не
            хочешь дубликатов на диске между разными подборками. Переключение
            влияет только на новые задачи — уже запущенные продолжают со своим
            манифестом.
          </div>
          <div class="actions-row manifest-actions">
            <button class="btn small" id="btn-open-manifest" title="Открыть папку, в которой лежит активный манифест">
              📂 Открыть папку манифеста
            </button>
            <button class="btn small danger" id="btn-delete-manifest" title="Удалить файл манифеста — все картинки снова будут считаться нескачанными">
              🗑️ Удалить манифест
            </button>
            <button class="btn small danger" id="btn-rebuild-manifest" title="Просканировать указанную папку с подпапками и сделать так, чтобы манифест содержал ровно те attribute id, что есть на диске. Записи о файлах, которых нет в этой папке, будут удалены.">
              🔄 Полностью пересобрать по файлам из директории…
            </button>
          </div>
        </div>

        <div class="settings-section">
          <h4>Сеть</h4>
          <div class="toggles">
            <label>
              <input type="checkbox" id="s-socks5-enabled" ${state.appSettings.socks5Enabled ? 'checked' : ''}>
              Использовать SOCKS5-прокси для GraphQL (Tor)
            </label>
          </div>
          <div class="row">
            <div class="field">
              <label>SOCKS5 адрес (host:port)</label>
              <input type="text" id="s-socks5-addr" placeholder="127.0.0.1:9150"
                     value="${escape(state.appSettings.socks5Addr || '')}">
              <div class="field-hint">
                Tor Browser — <code>127.0.0.1:9150</code>, отдельный tor daemon — <code>127.0.0.1:9050</code>.
              </div>
            </div>
            <div class="field">
              <label>.onion-зеркало JR (базовый URL)</label>
              <input type="text" id="s-onion-base" placeholder="http://reactorccdnf...onion"
                     value="${escape(state.appSettings.onionBaseURL || '')}">
              <div class="field-hint">
                Используется для восстановления удалённых постов через
                HTML-скрапинг <code>&lt;onion&gt;/post/&lt;id&gt;</code>.
              </div>
            </div>
          </div>
          <div class="toggles">
            <label class="${state.appSettings.socks5Enabled && state.appSettings.onionBaseURL ? '' : 'disabled'}"
                   title="${state.appSettings.socks5Enabled && state.appSettings.onionBaseURL ? '' : 'нужны SOCKS5 + URL зеркала'}">
              <input type="checkbox" id="s-recover-dmca"
                     ${state.appSettings.recoverDmcaViaOnion ? 'checked' : ''}
                     ${state.appSettings.socks5Enabled && state.appSettings.onionBaseURL ? '' : 'disabled'}>
              Восстанавливать удалённые посты через .onion-зеркало
            </label>
          </div>
          <div class="actions-row">
            <button class="btn small" id="btn-fill-onion" title="Подставить адрес .onion-зеркала JR (нужен SOCKS5+Tor)">
              🧅 Подставить .onion
            </button>
            <button class="btn small" id="btn-test-network" title="Проверить связь с настроенным эндпоинтом">
              ${state.networkTest.state === 'testing' ? '⏳ Проверяю…' : '🔌 Проверить'}
            </button>
            ${renderNetworkTestResult()}
          </div>
          <div class="field-hint">
            Скачивание файлов всегда идёт напрямую — прокси нужен только для
            метаданных (GraphQL) и для HTML-скрапинга зеркала. Удалённые по
            DMCA посты восстанавливаются так: зеркало хранит метаданные,
            оттуда берём <code>attribute.id</code> картинок, сами файлы
            живут на основном CDN. Прокси-клиент мы не поставляем — поставь
            его сам (например, Tor Browser или <code>tor</code> daemon).
            После смены сетевых настроек — перезапусти приложение.
          </div>
        </div>

        <div class="settings-section">
          <h4>Какие фильтры показывать</h4>
          <div class="toggles">
            <label>
              <input type="checkbox" id="s-show-author" ${state.showAuthor ? 'checked' : ''}>
              Поиск по автору
            </label>
            <label>
              <input type="checkbox" id="s-show-rating" ${state.showRating ? 'checked' : ''}>
              Поиск по рейтингу
            </label>
            <label>
              <input type="checkbox" id="s-show-page-range" ${state.showPageRange ? 'checked' : ''}>
              Диапазон страниц поиска
            </label>
          </div>
        </div>

        <div class="settings-section">
          <h4>Превью</h4>
          <div class="field">
            <label>Постов за один клик «Найти» / «Загрузить ещё»</label>
            <input type="number" id="s-preview-batch" min="1" max="500" value="${state.previewBatch}">
            <div class="field-hint">JR отдаёт ~10 постов за страницу — мы автоматически дотягиваем нужное количество, делая столько запросов, сколько надо.</div>
          </div>
        </div>

        <div class="settings-section">
          <h4>Тип медиа</h4>
          <div class="kind-checks">
            <label><input type="checkbox" id="s-kind-image" ${kindChecked('image')}> Картинки</label>
            <label><input type="checkbox" id="s-kind-gif"   ${kindChecked('gif')}> GIF</label>
            <label><input type="checkbox" id="s-kind-video" ${kindChecked('video')}> Видео</label>
          </div>
          <div class="field-hint">Ничего не отмечено — качаем все типы.</div>
        </div>

        <div class="settings-section">
          <h4>Ограничения</h4>
          <div class="row">
            <div class="field">
              <label>Мин. ширина (px)</label>
              <input type="number" id="f-min-width" placeholder="0">
            </div>
            <div class="field">
              <label>Мин. высота (px)</label>
              <input type="number" id="f-min-height" placeholder="0">
            </div>
          </div>
          <div class="row">
            <div class="field">
              <label>Дата с</label>
              <input type="date" id="f-from">
            </div>
            <div class="field">
              <label>Дата по</label>
              <input type="date" id="f-to">
            </div>
          </div>
          <div class="row">
            <div class="field">
              <label>Лимит файлов</label>
              <input type="number" id="f-limit" placeholder="0 = без лимита">
            </div>
            <div class="field">
              <label>Параллельных загрузок</label>
              <input type="number" id="f-workers" value="4" min="1" max="16">
            </div>
          </div>
        </div>

        <div class="settings-section">
          <h4>Формат имени файла</h4>
          <div class="filename-fmt">
            <label>
              <input type="radio" name="filename-fmt" value="id" ${state.filenameFormat === 'id' ? 'checked' : ''}>
              <span>
                <strong>По ID</strong>
                <code class="muted">12345_67890.jpg</code>
              </span>
            </label>
            <label>
              <input type="radio" name="filename-fmt" value="tags" ${state.filenameFormat === 'tags' ? 'checked' : ''}>
              <span>
                <strong>По тегам (в скобках)</strong>
                <code class="muted">[art][cat][dog]_12345_67890.jpg</code>
              </span>
            </label>
            <label>
              <input type="radio" name="filename-fmt" value="joysave" ${state.filenameFormat === 'joysave' ? 'checked' : ''}>
              <span>
                <strong>JoySave-совместимый</strong>
                <code class="muted">12345_0_000067890__art-cat-dog.jpeg</code>
              </span>
            </label>
          </div>
          <div class="field-hint">«По тегам»: теги в квадратных скобках, сортируются алфавитно (одинаковый пост → одинаковое имя), берётся до 6 шт., спецсимволы (<code>/ \ : * ? " &lt; &gt; |</code>) → <code>-</code>, скобки внутри тега → круглые. «JoySave-совместимый» 1:1 повторяет имена, которые пишет <a href="https://github.com/corax4/JoySave" target="_blank" rel="noopener">JoySave</a> (<code>&lt;postId&gt;_0_&lt;attrId padded to 9&gt;__&lt;до 4 тегов через -&gt;.ext</code>): пробелы в имени тега → <code>-</code>, спецсимволы → <code>@</code>, теги в исходном порядке без сортировки — удобно если папка делится между двумя инструментами.</div>
        </div>

        <div class="actions">
          <button class="btn primary" id="btn-settings-done">Готово</button>
        </div>
      </div>
    </div>`;
}

function renderQueueModal() {
  const anyFinished = state.jobs.some(j => isFinished(j.state));
  const active = countActive();
  const subtitle = state.jobs.length === 0
    ? ''
    : `<span class="muted">· ${state.jobs.length}${active > 0 ? ` · ${active} активны` : ''}</span>`;
  const body = state.jobs.length === 0
    ? `<div class="queue-empty">Очередь пуста. Добавь задачу через «＋ Добавить в очередь» вверху.</div>`
    : `<div class="queue-table-wrap">
        <table class="queue-table">
          <thead>
            <tr>
              <th class="col-no">№</th>
              <th class="col-state">Статус</th>
              <th class="col-name">Имя</th>
              <th class="col-folder">Папка</th>
              <th class="col-progress">Прогресс</th>
              <th class="col-stats">Файлы</th>
              <th class="col-actions"></th>
            </tr>
          </thead>
          <tbody>
            ${state.jobs.map(renderJobRow).join('')}
          </tbody>
        </table>
      </div>`;
  return `
    <div class="modal-backdrop" id="queue-backdrop">
      <div class="modal queue-modal">
        <div class="modal-header">
          <h3>Очередь ${subtitle}</h3>
          <div class="queue-modal-actions">
            ${anyFinished ? `<button class="btn small" id="btn-clear-finished">Очистить готовые</button>` : ''}
            <button class="icon-btn" id="btn-queue-close" title="Закрыть (Esc)">✕</button>
          </div>
        </div>
        ${body}
      </div>
    </div>`;
}

function renderJobRow(j, i) {
  const total = j.saved + j.skipped + j.failed;
  const determinate = j.limit > 0;
  let pct;
  if (j.state === 'done') {
    pct = 100;                                                   // a finished bar always reads "full"
  } else if (determinate) {
    pct = Math.min(100, Math.round((total / j.limit) * 100));
  } else {
    pct = 30;                                                    // placeholder for indeterminate running jobs
  }
  const indeterm = !determinate && j.state === 'running';
  const folder = j.outDir;
  const folderShort = folder.length > 32 ? '…' + folder.slice(-30) : folder;
  return `
    <tr class="job-row job-state-${j.state}" data-id="${escape(j.id)}">
      <td class="col-no">${i + 1}</td>
      <td class="col-state">
        <span class="job-badge job-state-${j.state}">${jobStateLabel(j.state)}</span>
      </td>
      <td class="col-name" title="${escape(j.name)}">
        <div class="job-name-cell">${escape(j.name)}</div>
        ${j.error ? `<div class="job-error-inline" title="${escape(j.error)}">${escape(j.error)}</div>` : ''}
      </td>
      <td class="col-folder" title="${escape(folder)}">${escape(folderShort)}</td>
      <td class="col-progress">
        <div class="progress-bar mini ${indeterm ? 'indeterminate' : ''} ${j.state === 'paused' ? 'paused' : ''} ${j.state === 'error' ? 'error' : ''}">
          <div class="fill" style="width: ${pct}%"></div>
        </div>
      </td>
      <td class="col-stats">
        ${renderJobStats(j, determinate)}
      </td>
      <td class="col-actions">${jobButtons(j)}</td>
    </tr>`;
}

// renderJobStats — files-column markup. Earlier `saved/skipped` slash-format
// read as a fraction ("9/0 из 9" looked like "9 of 0 of 9"). New format
// shows non-zero counts only, with explicit icons + tooltips, so a clean
// run reads simply "9 из 9" instead of "9/0 из 9".
function renderJobStats(j, determinate) {
  const parts = [];
  if (j.saved   > 0) parts.push(`<span class="stat saved"   title="скачано: ${j.saved}">${j.saved}<span class="stat-icon">✓</span></span>`);
  if (j.skipped > 0) parts.push(`<span class="stat skipped" title="пропущено (есть в манифесте): ${j.skipped}">${j.skipped}<span class="stat-icon">⊘</span></span>`);
  if (j.failed  > 0) parts.push(`<span class="stat failed"  title="ошибок: ${j.failed}">${j.failed}<span class="stat-icon">✖</span></span>`);
  const body = parts.length ? parts.join(' ') : `<span class="stat muted">0</span>`;
  const totalSuffix = determinate ? ` <span class="muted">из ${j.limit}</span>` : '';
  return body + totalSuffix;
}

function jobButtons(j) {
  const btn = (act, label, title, cls = '') =>
    `<button class="icon-btn ${cls}" data-act="${act}" data-id="${escape(j.id)}" title="${title}">${label}</button>`;
  if (j.state === 'running')  return btn('pause',  '⏸', 'Пауза') + btn('cancel', '⏹', 'Отменить', 'danger');
  if (j.state === 'paused')   return btn('resume', '▶', 'Продолжить') + btn('cancel', '⏹', 'Отменить', 'danger');
  return btn('open', '📂', 'Открыть папку') + btn('remove', '✕', 'Убрать из списка');
}

// renderPresetsManagerModal — the «🎛️ Пресеты» modal. One row per saved
// preset showing summary + AutoPull + last/next countdown + actions. Live
// countdown is updated in-place by tickPresetsCountdown (every 1s) reading
// data-next-due-sec on each row, so we don't need to rebuild the DOM each
// tick (saves flicker and a captureInputs round-trip).
function renderPresetsManagerModal() {
  const intervalH = state.appSettings?.autoPullIntervalHours || 24;
  const rows = state.presetViews.length === 0
    ? `<div class="presets-empty">Нет сохранённых пресетов. Заведи первый в форме фильтров: настрой фильтры и нажми «Сохранить как…».</div>`
    : `<div class="presets-list">
         ${state.presetViews.map(renderPresetRow).join('')}
       </div>`;
  return `
    <div class="modal-backdrop" id="presets-backdrop">
      <div class="modal wide presets-modal">
        <div class="modal-header">
          <h3>Пресеты <span class="muted" title="Глобальный интервал автовыгрузки — меняется в ⚙ → Авто-обновление">· интервал автовыгрузки: ${intervalH} ч</span></h3>
          <div class="queue-modal-actions">
            <button class="icon-btn" id="btn-presets-close" title="Закрыть (Esc)">✕</button>
          </div>
        </div>
        ${rows}
      </div>
    </div>`;
}

// renderPresetRow — single row in the preset-manager modal.
//   - data-name carries the preset name so the wired click handlers can
//     find it without each button repeating the id.
//   - data-next-due-sec on the .preset-next-due cell is the source of truth
//     for the live countdown; tickPresetsCountdown decrements it in place.
//   - Run/Load/Folder buttons gracefully disable when OutDir is empty,
//     mirroring the scheduler's "skip if no outDir" guard.
function renderPresetRow(v) {
  const autoPullChecked = v.autoPull ? 'checked' : '';
  const folderDisabled = v.hasOutDir ? '' : 'disabled';
  const folderTitle = v.hasOutDir
    ? `Открыть «${v.outDir}» в проводнике`
    : 'У пресета не задана папка';
  const runTitle = v.hasOutDir
    ? 'Запустить пресет сейчас вручную (отметит как только что выгруженный — следующий авто-запуск через полный интервал)'
    : 'Нельзя запустить — у пресета не задана папка';
  const last = v.lastAutoPullAt
    ? `<span class="preset-last" title="${escape(v.lastAutoPullAt)}">⏱ ${formatRelativePast(v.lastAutoPullAt)}</span>`
    : `<span class="preset-last muted" title="Ещё не выгружался">⏱ ни разу</span>`;
  const next = v.autoPull
    ? `<span class="preset-next-due" data-next-due-sec="${v.nextDueSec}" title="Расчёт от ⏱ + ${v.intervalH} ч (глобальный интервал)">→ ${formatNextDue(v.nextDueSec)}</span>`
    : `<span class="preset-next-due muted">→ автовыгрузка выключена</span>`;
  const outdirShort = v.outDir
    ? (v.outDir.length > 38 ? '…' + v.outDir.slice(-36) : v.outDir)
    : '<span class="muted">папка не задана</span>';
  return `
    <div class="preset-row" data-name="${escape(v.name)}">
      <div class="preset-head">
        <div class="preset-name" title="${escape(v.name)}">${escape(v.name)}</div>
        <div class="preset-actions">
          <button class="icon-btn" data-act="run"    title="${runTitle}" ${folderDisabled}>▶</button>
          <button class="icon-btn" data-act="folder" title="${folderTitle}" ${folderDisabled}>📂</button>
          <button class="icon-btn" data-act="load"   title="Загрузить в форму для редактирования">📋</button>
          <button class="icon-btn danger" data-act="delete" title="Удалить пресет">🗑</button>
        </div>
      </div>
      <div class="preset-summary">${escape(v.summary)}</div>
      <div class="preset-folder" title="${escape(v.outDir)}">${v.outDir ? escape(outdirShort) : outdirShort}</div>
      <div class="preset-schedule">
        <label class="preset-autopull-toggle" title="Включить периодическую автовыгрузку этого пресета по глобальному интервалу">
          <input type="checkbox" data-act="autopull" ${autoPullChecked}>
          Авто-обновление
        </label>
        ${last}
        ${next}
      </div>
    </div>`;
}

// formatRelativePast — turns an ISO timestamp into a "Nмин назад" /
// "Nч назад" / "Nдн назад" string. Best-effort: an unparseable input
// just returns "—".
function formatRelativePast(iso) {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return '—';
  const sec = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (sec < 10)        return 'только что';
  if (sec < 60)        return `${sec}с назад`;
  if (sec < 3600)      return `${Math.floor(sec / 60)} мин назад`;
  if (sec < 86400)     return `${Math.floor(sec / 3600)} ч назад`;
  return `${Math.floor(sec / 86400)} дн назад`;
}

// formatNextDue — "следующая через" in human form. ≤0 ⇒ "готова к запуску".
function formatNextDue(sec) {
  if (sec <= 0) return 'готова к запуску';
  if (sec < 60)        return `через ${sec}с`;
  if (sec < 3600)      return `через ${Math.floor(sec / 60)} мин`;
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (m === 0) return `через ${h} ч`;
  return `через ${h} ч ${m} мин`;
}

function jobStateLabel(s) {
  switch (s) {
    case 'running':  return '⏵ идёт';
    case 'paused':   return '⏸ пауза';
    case 'done':     return '✓ готово';
    case 'error':    return '✖ ошибка';
    case 'canceled': return '⊘ отменено';
    default:         return s;
  }
}

function isFinished(s) {
  return s === 'done' || s === 'error' || s === 'canceled';
}

function renderPreviewCard() {
  const hasResults = state.results.length > 0;
  // hasMore is "are there more API pages worth fetching?". state.exhausted is
  // set when the doSearch loop has good reason to stop: JR returned an
  // empty page, the user-defined PageTo was reached, or count was hit.
  // Without that flag the button kept hovering even when a hard media-type
  // filter dropped 99% of results but the raw count was still high.
  const hasMore = !state.exhausted && state.count != null && state.results.length < state.count;
  const header = hasResults
    ? `Превью: ${state.results.length}${state.count != null ? ` из ${state.count}${state.count >= 1000 ? '+' : ''}` : ''}`
    : 'Превью';
  let body;
  if (hasResults) {
    body = `<div class="results-grid">${state.results.map(renderTile).join('')}</div>`;
  } else if (state.searching) {
    body = `<div class="preview-empty">Ищу…</div>`;
  } else {
    body = `<div class="preview-empty">Настрой фильтры справа и нажми <strong>«Найти»</strong>, чтобы увидеть превью.</div>`;
  }
  return `
    <div class="card results preview-card">
      <div class="results-header">
        <h2>${header}</h2>
        <label class="manual-select-toggle" title="Показать чекбоксы на плитках, чтобы скачать только выбранные">
          <input type="checkbox" id="s-manual-select" ${state.manualSelect ? 'checked' : ''}>
          <span>Ручное скачивание${state.manualSelect && state.selectedPosts.size > 0 ? ` <span class="muted">(выбрано ${state.selectedPosts.size})</span>` : ''}</span>
        </label>
      </div>
      <div class="preview-scroll">
        ${body}
        ${hasMore ? `
          <div class="actions-row preview-more">
            <button class="btn" id="btn-more" ${state.searching ? 'disabled' : ''}>
              ${state.searching ? 'Загружаю…' : 'Загрузить ещё'}
            </button>
          </div>` : ''}
      </div>
    </div>`;
}

function renderTile(p) {
  const thumb = p.thumbnailUrl || (p.pictures?.[0]?.url) || '';
  const ratingStr = (Math.round(p.rating * 10) / 10).toFixed(1);
  const picCount = (p.pictures || []).length;
  const tags = (p.tags || []).slice(0, 4).map(t => escape(t)).join(', ');
  const kind = postKindLabel(p);
  const have = tileDownloadState(p);
  const selected = state.manualSelect && state.selectedPosts.has(p.postId);
  const fullyDownloaded = have === 'full';
  const haveBadge = have === 'full'
    ? `<div class="tile-badge tile-have" title="все картинки поста уже скачаны в текущую папку">✓</div>`
    : have === 'partial'
      ? `<div class="tile-badge tile-have partial" title="часть картинок поста уже скачаны">✓</div>`
      : '';
  const countBadge = picCount > 1
    ? `<div class="tile-badge tile-count" title="${picCount} картинок в посте">${picCount}</div>`
    : '';
  const nsfwBadge = p.nsfw ? `<div class="tile-badge tile-nsfw">NSFW</div>` : '';
  const kindBadge = kind  ? `<div class="tile-badge tile-kind">${kind}</div>`  : '';
  // tile-select appears only when "Ручное скачивание" is on AND the post
  // isn't already fully downloaded. Also hidden for removed posts — there's
  // nothing to download. The left-corner green check already signals
  // "downloaded", so hiding the right-corner checkbox keeps the UI uncluttered.
  // stopPropagation in the handler keeps the tile body's open-preview click
  // from firing.
  const selectBtn = (state.manualSelect && !fullyDownloaded && !p.removed)
    ? `<button class="tile-select ${selected ? 'on' : ''}"
              data-post-id="${escape(p.postId)}"
              title="${selected ? 'Убрать из выбора' : 'Добавить в выбранные'}"
              aria-pressed="${selected ? 'true' : 'false'}">${selected ? '✓' : ''}</button>`
    : '';
  const removedOverlay = p.removed
    ? `<div class="tile-removed" title="Пост удалён по жалобе на копирайт — картинки недоступны">
         <div class="tile-removed-icon">🚫</div>
         <div class="tile-removed-text">удалено<br>по копирайту</div>
       </div>`
    : '';
  return `
    <div class="tile ${selected ? 'selected' : ''} ${p.removed ? 'is-removed' : ''}" data-post="${p.postNum}" data-post-id="${escape(p.postId)}" title="${escape(tags)}">
      ${thumb ? `<img src="${escape(thumb)}" loading="lazy" alt="" draggable="false">` : '<div class="tile-empty">нет превью</div>'}
      ${removedOverlay}
      <div class="tile-corner left">
        ${haveBadge}
        ${countBadge}
      </div>
      <div class="tile-corner right">
        ${selectBtn}
        ${nsfwBadge}
        ${kindBadge}
      </div>
      <div class="tile-meta">
        <span class="tile-rating">★ ${ratingStr}</span>
        <span class="tile-author">${escape(p.author || '')}</span>
      </div>
    </div>`;
}

// tileDownloadState — '' / 'partial' / 'full' depending on how many of the
// post's pictures are in the current outdir's manifest. "full" is solid green
// (definitively already have it), "partial" is faded green (got some, not all).
function tileDownloadState(p) {
  const set = state.downloaded.keys;
  const pics = p.pictures || [];
  if (set.size === 0 || pics.length === 0) return '';
  let n = 0;
  for (const pic of pics) if (set.has(pic.attrId)) n++;
  if (n === 0) return '';
  return n === pics.length ? 'full' : 'partial';
}

// postKindLabel reports the strongest media kind in the post for the corner
// badge. Returns '' only for posts with no pictures at all — every other
// post gets IMG/GIF/VIDEO so the user can tell at a glance what's inside.
function postKindLabel(p) {
  const pics = p.pictures || [];
  if (pics.length === 0) return '';
  if (pics.some(x => x.kind === 'video')) return 'VIDEO';
  if (pics.some(x => x.kind === 'gif')) return 'GIF';
  return 'IMG';
}

function renderPostPreview() {
  const m = state.postModal;
  const p = m.post;
  const ratingStr = (Math.round(p.rating * 10) / 10).toFixed(1);
  // Tag pills in the overlay: click to toggle membership in state.tags.
  // Active (already-in-search) pills get the accent colour; clicking them
  // removes the tag from the chip list.
  const activeTags = new Set(state.tags || []);
  const tagsLine = (p.tags || []).map(t => {
    const isActive = activeTags.has(t);
    const cls = isActive ? 'post-overlay-tag active' : 'post-overlay-tag';
    const title = isActive ? `убрать «${t}» из поиска` : `добавить «${t}» в поиск`;
    return `<span class="${cls}" data-tag="${escape(t)}" title="${escape(title)}">${escape(t)}</span>`;
  }).join('');

  const pics = (p.pictures || []).length
    ? p.pictures.map(pic => mediaTile(pic, 'post-overlay-pic', { postId: p.postId, tags: p.tags })).join('')
    : '<div class="post-overlay-empty">В посте нет картинок</div>';

  let commentsBody;
  if (m.loading)      commentsBody = `<div class="comments-empty">Загружаю комментарии…</div>`;
  else if (m.error)   commentsBody = `<div class="comments-empty err">${escape(m.error)}</div>`;
  else if (!m.comments || m.comments.length === 0) commentsBody = `<div class="comments-empty">Комментариев нет.</div>`;
  else                commentsBody = m.comments.map(renderComment).join('');

  const commentsHeader = m.loading
    ? 'Комментарии'
    : `Комментарии · ${m.comments ? m.comments.length : 0}`;

  return `
    <div class="post-overlay" id="post-overlay-backdrop">
      <div class="post-overlay-inner">
        <button class="icon-btn post-overlay-close" id="btn-post-close" title="Закрыть (Esc)">✕</button>
        <div class="post-overlay-pics">
          <div class="post-overlay-meta">
            <div class="post-overlay-meta-info">
              <span class="tile-rating">★ ${ratingStr}</span>
              ${p.author ? `<span class="post-overlay-author">@${escape(p.author)}</span>` : ''}
              ${p.nsfw ? `<span class="tile-badge tile-nsfw">NSFW</span>` : ''}
            </div>
            <div class="post-overlay-meta-actions">
              ${tileDownloadState(p) === 'full'
                ? `<button class="btn small" id="btn-post-save" disabled title="все картинки этого поста уже в папке">✓ Скачано</button>`
                : `<button class="btn small primary" id="btn-post-save" title="Скачать только этот пост в текущую папку">＋ Сохранить</button>`}
              <button class="btn small" id="btn-post-open">Открыть на сайте</button>
            </div>
          </div>
          ${tagsLine ? `<div class="post-overlay-tags">${tagsLine}</div>` : ''}
          <div class="post-overlay-pics-list">${pics}</div>
        </div>
        <div class="post-overlay-comments">
          <div class="comments-header">${commentsHeader}</div>
          <div class="comments-list">${commentsBody}</div>
        </div>
      </div>
    </div>`;
}

function renderComment(c) {
  const indent = Math.min(c.level || 0, 8);
  const ratingCls = c.rating > 0 ? 'pos' : c.rating < 0 ? 'neg' : '';
  const ratingStr = c.rating === 0 ? '0' : (c.rating > 0 ? '+' : '') + c.rating.toFixed(1).replace(/\.0$/, '');
  return `
    <div class="comment" style="margin-left: ${indent * 14}px">
      <div class="comment-meta">
        <span class="comment-author">${escape(c.author || 'аноним')}</span>
        <span class="comment-rating ${ratingCls}">${ratingStr}</span>
      </div>
      <div class="comment-body">${renderCommentBody(c)}</div>
    </div>`;
}

// renderCommentBody turns JR's comment.text (HTML with &attribute_insert_N&
// placeholders) plus the attribute list into safe markup. Each placeholder
// is swapped for the matching attribute's image; text segments are sanitized
// (whitelist of <a>/<br>/<b>/<i>/<em>/<strong>) so links stay clickable but
// raw <p>/<div>/<script>/etc. don't leak through.
function renderCommentBody(c) {
  const text = c.text || '';
  const pics = c.pictures || [];

  // Lookup: insertId -> picture. Fall back to positional order if multiple
  // attrs share insertId 0 (rare/legacy).
  const byInsert = new Map();
  pics.forEach((p, i) => {
    const key = p.insertId || i + 1;
    if (!byInsert.has(key)) byInsert.set(key, p);
  });
  const usedInserts = new Set();

  // Split on the placeholder, keep the captured insert id.
  const parts = text.split(/&attribute_insert_(\d+)&/);
  let html = '';
  for (let i = 0; i < parts.length; i++) {
    if (i % 2 === 0) {
      const segment = sanitizeCommentHTML(parts[i]);
      // Skip purely-empty interstitials (whitespace or just <br>s between
      // attribute placeholders) — without this the comment renders with
      // blank gaps between every embedded image.
      if (segmentHasContent(parts[i], segment)) {
        html += `<div class="comment-text">${segment}</div>`;
      }
    } else {
      const id = parseInt(parts[i], 10);
      usedInserts.add(id);
      const pic = byInsert.get(id);
      if (pic) html += commentPicMarkup(pic);
      else      html += `<div class="comment-text muted">[вложение #${id} не получено]</div>`;
    }
  }

  // Append any attributes the text didn't reference (legacy comments where
  // the placeholder is missing). Without this we'd drop their images on the floor.
  for (const pic of pics) {
    const key = pic.insertId || 0;
    if (key && usedInserts.has(key)) continue;
    if (!key) html += commentPicMarkup(pic);
  }
  return html || `<div class="comment-text muted">(пусто)</div>`;
}

function commentPicMarkup(pic) {
  return mediaTile(pic, 'comment-pic post-overlay-pic');
}

// mediaTile renders one picture/video for the overlay or a comment body.
// MP4/WEBM go into a <video> with native controls — we deliberately don't
// wire BrowserOpenURL on the wrapper for videos so play/pause/seek aren't
// hijacked. Images keep click-to-open in browser.
function mediaTile(pic, cssClass, meta) {
  // JR's CDN refuses to serve newer media without a joyreactor.cc Referer,
  // and the WebView sends its own (wails://) origin. Route every src through
  // our Go-side /proxy, which adds the right Referer. data-url stays the
  // direct CDN url so "open in browser" hands the user the canonical link.
  //
  // meta carries the parent post's id + tags so the proxy can build a
  // "Save image as" filename matching state.filenameFormat. Comments are
  // currently rendered without meta; the proxy then falls back to the URL
  // segment as the filename hint.
  const imgUrl   = pic.url || '';
  const videoUrl = pic.videoUrl || '';
  const hint     = namingHint(pic, meta);
  const imgSrc   = proxyURL(imgUrl,   hint);
  const videoSrc = proxyURL(videoUrl, hint);

  // Matches joyreactor.cc — every <video> renders without controls,
  // autoplay-looping muted like a giant gif. Click toggles play/pause; the ↗
  // button opens the original in the system browser for sound/seek.
  // "real video" (kind === 'video') and "gif-as-video" (kind === 'gif' with a
  // transcoded videoUrl) only differ in which URL we feed to <video src>.
  const isRealVideo  = pic.kind === 'video';
  const isGifAsVideo = !isRealVideo && !!videoUrl;

  if (isRealVideo || isGifAsVideo) {
    const src     = isGifAsVideo ? videoSrc : imgSrc;
    const openUrl = isGifAsVideo ? videoUrl : imgUrl;
    // For gif-as-video we stash the original .gif on the wrapper so a 404 on
    // the webm transcode can degrade gracefully to the gif instead of an
    // error message. (Our webm URL guesses the post's primary tag-slug; when
    // wrong, JR returns 404 and the <video> emits an error event.)
    const fallbackAttrs = isGifAsVideo
      ? ` data-gif-fallback-src="${escape(imgSrc)}" data-gif-fallback-url="${escape(imgUrl)}"`
      : '';
    return `
      <div class="${cssClass} is-video is-gif-video"${fallbackAttrs}>
        <video src="${escape(src)}" autoplay loop muted playsinline preload="metadata"></video>
        <button class="media-open-btn" data-url="${escape(openUrl)}" title="Открыть оригинал в браузере">↗</button>
      </div>`;
  }
  return `
    <div class="${cssClass}" data-url="${escape(imgUrl)}" title="Открыть оригинал в браузере">
      <img src="${escape(imgSrc)}" alt="" loading="lazy">
    </div>`;
}

// proxyURL wraps a joyreactor.cc CDN URL so the WebView fetches it via our
// Go-side proxy (which adds the Referer JR requires). Empty in → empty out.
//
// When `hint` is supplied (pid/aid/type/tags), the proxy uses it together
// with state.filenameFormat to set Content-Disposition. That's what makes
// WebView2's "Save image as" suggest the same filename the downloader
// would write to disk.
function proxyURL(jrUrl, hint) {
  if (!jrUrl) return '';
  if (!hint || !hint.pid || !hint.aid || !hint.type) {
    return '/proxy?url=' + encodeURIComponent(jrUrl);
  }
  const p = new URLSearchParams();
  p.set('url', jrUrl);
  p.set('pid', hint.pid);
  p.set('aid', hint.aid);
  p.set('type', hint.type);
  p.set('fmt', state.filenameFormat || 'id');
  for (const t of hint.tags || []) p.append('tag', t);
  return '/proxy?' + p.toString();
}

// namingHint — packs the data the Go-side proxy needs to reconstruct the
// downloader's filename. Returns null when we don't have a parent post
// (e.g. when mediaTile is called from a comment without context).
function namingHint(pic, meta) {
  if (!meta || !meta.postId || !pic.attrId || !pic.type) return null;
  return {
    pid:  meta.postId,
    aid:  pic.attrId,
    type: pic.type,
    tags: meta.tags || [],
  };
}

// sanitizeCommentHTML parses raw JR comment HTML into a fresh inert document
// (so no scripts/images execute) and walks it, keeping only a small tag
// whitelist. Everything else collapses to its text content; attributes are
// dropped except for the href on whitelisted links. Anchors keep their URL
// in a data-url attribute so wireEvents can route the click through
// BrowserOpenURL instead of letting the WebView navigate away from the app.
const COMMENT_TAG_WHITELIST = new Set(['A', 'BR', 'B', 'I', 'EM', 'STRONG']);

function sanitizeCommentHTML(raw) {
  if (!raw) return '';
  const doc = new DOMParser().parseFromString('<div>' + String(raw) + '</div>', 'text/html');
  const wrap = doc.body && doc.body.firstChild;
  if (!wrap) return '';
  return sanitizeChildren(wrap);
}

function sanitizeNode(node) {
  if (node.nodeType === 3 /* TEXT_NODE */) {
    return escape(node.nodeValue || '');
  }
  if (node.nodeType !== 1 /* ELEMENT_NODE */) return '';
  const tag = node.tagName;
  if (!COMMENT_TAG_WHITELIST.has(tag)) return sanitizeChildren(node);
  if (tag === 'BR') return '<br>';
  if (tag === 'A') {
    const href = node.getAttribute('href') || '';
    if (/^https?:\/\//i.test(href)) {
      return '<a class="comment-link" data-url="' + escape(href) + '">' + sanitizeChildren(node) + '</a>';
    }
    return sanitizeChildren(node);
  }
  const t = tag.toLowerCase();
  return '<' + t + '>' + sanitizeChildren(node) + '</' + t + '>';
}

function sanitizeChildren(node) {
  let out = '';
  for (const c of node.childNodes) out += sanitizeNode(c);
  return out;
}

// segmentHasContent reports whether a between-placeholders text segment is
// worth rendering. Empty strings, pure whitespace, or HTML containing only
// <br>s should not produce an empty bubble — that just adds vertical gaps.
function segmentHasContent(raw, sanitized) {
  if (raw.replace(/<[^>]+>/g, '').trim() !== '') return true;
  return /<a\s/i.test(sanitized);
}

function countActive() {
  return state.jobs.filter(j => j.state === 'running' || j.state === 'paused').length;
}

function chip(text, kind, i) {
  return `<span class="chip">${escape(text)}<button data-kind="${kind}" data-i="${i}" title="убрать">×</button></span>`;
}

// renderNetworkTestResult — small status pill next to the "Проверить" button.
function renderNetworkTestResult() {
  const t = state.networkTest;
  if (t.state === 'idle' || t.state === 'testing') return '';
  const cls = t.state === 'ok' ? 'ok' : 'err';
  return `<span class="net-test-result ${cls}">${escape(t.text || '')}</span>`;
}

const DEFAULT_ONION_BASE_URL = 'http://reactorccdnf36aqvq34zbfzqyrcrpg3eyhilauovitrvmcjovsujmid.onion';

// ----- Input preservation across re-renders -----
// All known input ids — only those present in the current DOM are captured/restored.
const inputIds = ['f-query','f-user','f-min-rating','f-max-rating','f-nsfw','f-only-nsfw','f-unsafe','f-favorite','f-use-blocked','f-min-width','f-min-height','f-from','f-to','f-limit','f-workers','f-page-from','f-page-to','f-outdir'];

function captureInputs() {
  for (const id of inputIds) {
    const el = $('#' + id);
    if (!el) continue;
    state.formInputs[id] = (el.type === 'checkbox') ? el.checked : el.value;
  }
}

function restoreInputs() {
  for (const id of inputIds) {
    const el = $('#' + id);
    const v = state.formInputs[id];
    if (!el || v === undefined) continue;
    if (el.type === 'checkbox') el.checked = !!v;
    else el.value = v;
  }
}

// ----- Event wiring -----
function wireEvents() {
  $('#btn-login')?.addEventListener('click', openLogin);
  $('#btn-logout')?.addEventListener('click', doLogout);
  $('#btn-search')?.addEventListener('click', () => doSearch(true));
  $('#btn-more')?.addEventListener('click', () => doSearch(false));
  $('#btn-add')?.addEventListener('click', doAddJob);
  $('#btn-pick')?.addEventListener('click', doPickFolder);
  $('#btn-open-outdir')?.addEventListener('click', doOpenCurrentOutdir);
  $('#f-outdir')?.addEventListener('change', async e => {
    const v = e.target.value.trim();
    state.formInputs['f-outdir'] = v;
    if (v) localStorage.setItem(LS_OUTDIR, v);
    syncOutdirToPreset();
    await refreshDownloadedKeys();
    render({ skipCapture: true });
  });
  $('#btn-clear-finished')?.addEventListener('click', doClearFinished);
  $('#btn-settings')?.addEventListener('click', openSettings);
  $('#btn-settings-close')?.addEventListener('click', closeSettings);
  $('#btn-settings-done')?.addEventListener('click', closeSettings);
  $('#settings-backdrop')?.addEventListener('click', e => {
    if (e.target.id === 'settings-backdrop') closeSettings();
  });

  $('#btn-queue')?.addEventListener('click', openQueue);
  $('#btn-queue-close')?.addEventListener('click', closeQueue);
  $('#queue-backdrop')?.addEventListener('click', e => {
    if (e.target.id === 'queue-backdrop') closeQueue();
  });

  $('#btn-presets-manager')?.addEventListener('click', openPresetsManager);
  $('#btn-presets-close')?.addEventListener('click', closePresetsManager);
  $('#presets-backdrop')?.addEventListener('click', e => {
    if (e.target.id === 'presets-backdrop') closePresetsManager();
  });
  // Per-row delegated handler: every action button carries data-act and lives
  // inside [data-name="<preset>"]. One listener covers run/folder/load/delete
  // + the autopull toggle so we don't query-select N×4 buttons per render.
  $('#presets-backdrop')?.addEventListener('click', async e => {
    const row = e.target.closest('.preset-row');
    if (!row) return;
    const btn = e.target.closest('[data-act]');
    if (!btn) return;
    const name = row.getAttribute('data-name');
    const act = btn.getAttribute('data-act');
    if (!name || !act) return;
    if (act === 'autopull') return; // handled by 'change' listener below
    e.preventDefault();
    await onPresetAction(name, act);
  });
  $('#presets-backdrop')?.addEventListener('change', async e => {
    if (!e.target.matches('input[data-act="autopull"]')) return;
    const row = e.target.closest('.preset-row');
    const name = row?.getAttribute('data-name');
    if (!name) return;
    const on = e.target.checked;
    const err = await SetPresetAutoPull(name, on);
    if (err) {
      showToast('error', `Не удалось переключить авто-обновление: ${err}`);
      e.target.checked = !on;
      return;
    }
    // Refresh local state.presetViews so next/last cells reflect the new
    // autopull status (which controls whether the countdown is shown).
    // Also keep the form's autopull checkbox in sync if this preset is the
    // currently selected one.
    if (state.currentPreset === name) state.currentPresetAutoPull = on;
    await refreshPresetViews();
    render();
  });


  $('#preset-sel')?.addEventListener('change', e => applyPreset(e.target.value));
  $('#btn-preset-save')?.addEventListener('click', doSavePreset);
  $('#btn-preset-delete')?.addEventListener('click', doDeletePreset);
  $('#preset-autopull')?.addEventListener('change', async e => {
    if (!state.currentPreset) return;
    const on = e.target.checked;
    const err = await SetPresetAutoPull(state.currentPreset, on);
    if (err) {
      showToast('error', `Не удалось переключить авто-обновление: ${err}`);
      e.target.checked = !on;
      return;
    }
    state.currentPresetAutoPull = on;
    const intH = state.appSettings.autoPullIntervalHours || 24;
    showToast('success', on
      ? `Пресет «${state.currentPreset}» будет обновляться раз в ${intH} ч`
      : `Авто-обновление пресета «${state.currentPreset}» выключено`);
  });

  $('#s-show-author')?.addEventListener('change', e => {
    state.showAuthor = e.target.checked;
    localStorage.setItem(LS_SHOW_AUTHOR, e.target.checked ? '1' : '0');
    captureInputs();
    render();
  });
  $('#s-show-rating')?.addEventListener('change', e => {
    state.showRating = e.target.checked;
    localStorage.setItem(LS_SHOW_RATING, e.target.checked ? '1' : '0');
    captureInputs();
    render();
  });
  $('#s-show-page-range')?.addEventListener('change', e => {
    state.showPageRange = e.target.checked;
    localStorage.setItem(LS_SHOW_PAGE_RANGE, e.target.checked ? '1' : '0');
    captureInputs();
    render();
  });
  $('#s-preview-batch')?.addEventListener('change', e => {
    const n = parseInt(e.target.value, 10);
    if (Number.isFinite(n) && n > 0) {
      state.previewBatch = Math.min(n, 500);
      localStorage.setItem(LS_PREVIEW_BATCH, String(state.previewBatch));
    }
  });

  const winW = $('#s-win-w'), winH = $('#s-win-h'), winMax = $('#s-win-max');
  const pushWinSettings = () => {
    SaveWindowSettings({
      width:     state.windowSettings.width,
      height:    state.windowSettings.height,
      maximized: state.windowSettings.maximized,
    });
  };
  winW?.addEventListener('change', e => {
    const n = parseInt(e.target.value, 10);
    if (Number.isFinite(n) && n >= 600) {
      state.windowSettings.width = n;
      pushWinSettings();
    }
  });
  winH?.addEventListener('change', e => {
    const n = parseInt(e.target.value, 10);
    if (Number.isFinite(n) && n >= 400) {
      state.windowSettings.height = n;
      pushWinSettings();
    }
  });
  winMax?.addEventListener('change', e => {
    state.windowSettings.maximized = e.target.checked;
    pushWinSettings();
    // Re-render so the W/H inputs get disabled/enabled to match.
    captureInputs();
    render();
  });

  for (const k of ['image','gif','video']) {
    $('#s-kind-' + k)?.addEventListener('change', e => {
      const has = state.kinds.includes(k);
      if (e.target.checked && !has) state.kinds.push(k);
      else if (!e.target.checked && has) state.kinds = state.kinds.filter(x => x !== k);
    });
  }

  $$('input[name="filename-fmt"]').forEach(r => {
    r.addEventListener('change', e => {
      state.filenameFormat = e.target.value;
      localStorage.setItem(LS_FILENAME_FORMAT, e.target.value);
    });
  });

  // Helper — push the full state.appSettings to Go. Sending the full
  // struct (not just one field) keeps the other AppSettings keys from
  // being reset to their Go zero-values on a partial save.
  const pushAppSettings = async () => {
    const err = await SaveAppSettings({ ...state.appSettings });
    if (err) showToast('error', `Не удалось сохранить настройки: ${err}`);
    return err || '';
  };

  $$('input[name="manifest-mode"]').forEach(r => {
    r.addEventListener('change', async e => {
      const mode = e.target.value;
      state.appSettings.manifestMode = mode;
      if (await pushAppSettings()) return;
      // The set of "already downloaded" tiles depends on which manifest
      // is consulted — refresh the badge cache after switching modes so
      // the grid updates immediately.
      await refreshDownloadedKeys();
      render({ skipCapture: true });
    });
  });

  $('#s-autostart')?.addEventListener('change', async e => {
    state.appSettings.autostart = e.target.checked;
    await pushAppSettings();
  });
  $('#s-start-min')?.addEventListener('change', async e => {
    state.appSettings.startMinimized = e.target.checked;
    await pushAppSettings();
  });
  $('#s-min-on-close')?.addEventListener('change', async e => {
    state.appSettings.minimizeToTrayOnClose = e.target.checked;
    await pushAppSettings();
  });
  $('#s-hide-removed')?.addEventListener('change', async e => {
    state.appSettings.hideRemovedPosts = e.target.checked;
    await pushAppSettings();
  });
  $('#s-socks5-enabled')?.addEventListener('change', async e => {
    state.appSettings.socks5Enabled = e.target.checked;
    // Pre-fill the address on first enable so the user has a sane default to
    // start from. Leave it alone on subsequent toggles in case they already
    // customized it.
    if (e.target.checked && !state.appSettings.socks5Addr) {
      state.appSettings.socks5Addr = '127.0.0.1:9150';
    }
    state.networkTest = { state: 'idle' };
    await pushAppSettings();
    render({ skipCapture: true });
  });
  // Save on blur (or Enter) rather than every keystroke — typing into
  // host:port shouldn't trigger 50 disk writes.
  const persistOnCommit = (id, key) => {
    const commit = e => {
      const next = e.target.value.trim();
      if (state.appSettings[key] === next) return;
      state.appSettings[key] = next;
      state.networkTest = { state: 'idle' };
      pushAppSettings();
      render({ skipCapture: true });
    };
    $('#' + id)?.addEventListener('blur', commit);
    $('#' + id)?.addEventListener('keydown', e => { if (e.key === 'Enter') e.target.blur(); });
  };
  persistOnCommit('s-socks5-addr', 'socks5Addr');
  persistOnCommit('s-onion-base', 'onionBaseURL');

  $('#s-recover-dmca')?.addEventListener('change', async e => {
    state.appSettings.recoverDmcaViaOnion = e.target.checked;
    await pushAppSettings();
  });

  $('#btn-fill-onion')?.addEventListener('click', async () => {
    state.appSettings.onionBaseURL = DEFAULT_ONION_BASE_URL;
    state.networkTest = { state: 'idle' };
    await pushAppSettings();
    render({ skipCapture: true });
  });

  $('#btn-test-network')?.addEventListener('click', async () => {
    if (state.networkTest.state === 'testing') return;
    state.networkTest = { state: 'testing' };
    render({ skipCapture: true });
    try {
      const r = await TestNetwork(
        !!state.appSettings.socks5Enabled,
        state.appSettings.socks5Addr || '',
        state.appSettings.onionBaseURL || '',
      );
      if (r.ok) {
        state.networkTest = { state: 'ok', text: `OK · ${r.latencyMs} мс · ${r.address}` };
      } else {
        state.networkTest = { state: 'err', text: r.error || 'неизвестная ошибка' };
      }
    } catch (e) {
      state.networkTest = { state: 'err', text: String(e?.message || e) };
    }
    render({ skipCapture: true });
  });
  $('#s-autopull-interval')?.addEventListener('change', async e => {
    const n = parseInt(e.target.value, 10);
    if (!Number.isFinite(n) || n < 1) {
      e.target.value = state.appSettings.autoPullIntervalHours || 24;
      return;
    }
    state.appSettings.autoPullIntervalHours = n;
    await pushAppSettings();
  });

  $('#btn-open-manifest')?.addEventListener('click', async () => {
    const outDir = state.formInputs['f-outdir'] || '';
    const err = await OpenManifestFolder(outDir);
    if (err) showToast('error', err);
  });

  $('#btn-rebuild-manifest')?.addEventListener('click', async () => {
    const dir = await PickFolder();
    if (!dir) return;
    const warning = state.appSettings.manifestMode === 'global'
      ? `Полностью пересобрать общий манифест по содержимому «${dir}»? Записи о файлах из других папок будут удалены — в режиме «общий» это особенно опасно.`
      : `Полностью пересобрать манифест по содержимому «${dir}»? Записи о файлах, которых нет в этой папке, будут удалены.`;
    showConfirm(warning, async () => {
      const btn = $('#btn-rebuild-manifest');
      const original = btn.innerHTML;
      btn.disabled = true;
      btn.innerHTML = '⏳ Сканирую…';
      try {
        const res = await RebuildManifest(dir);
        if (res?.error) {
          showToast('error', res.error);
          return;
        }
        const added = res?.added ?? 0;
        const removed = res?.removed ?? 0;
        const scanned = res?.scanned ?? 0;
        const inspected = res?.inspected ?? 0;
        showToast('success', `Обошёл ${inspected} файлов, подошло ${scanned}, добавлено ${added}, удалено ${removed}`);
        await refreshDownloadedKeys();
        render({ skipCapture: true });
      } finally {
        btn.disabled = false;
        btn.innerHTML = original;
      }
    });
  });

  $('#btn-delete-manifest')?.addEventListener('click', () => {
    const outDir = state.formInputs['f-outdir'] || '';
    const where = state.appSettings.manifestMode === 'global'
      ? 'общий манифест в %APPDATA%'
      : `манифест папки «${outDir || '(не выбрана)'}»`;
    showConfirm(`Удалить ${where}? Все картинки оттуда снова будут считаться нескачанными.`, async () => {
      const err = await DeleteManifest(outDir);
      if (err) {
        showToast('error', err);
        return;
      }
      showToast('success', 'Манифест удалён');
      await refreshDownloadedKeys();
      render({ skipCapture: true });
    });
  });

  attachAutocomplete($('#f-tag-input'), TagSuggest, name => {
    if (!state.tags.includes(name)) state.tags.push(name);
    captureInputs();
    render();
  });
  attachAutocomplete($('#f-excl-input'), TagSuggest, name => {
    if (!state.excludeTags.includes(name)) state.excludeTags.push(name);
    captureInputs();
    render();
  });
  if (state.showAuthor) {
    attachAutocomplete(
      $('#f-user'),
      async mask => (await SuggestUsers(mask) || []).map(n => ({ name: n })),
      name => {
        const el = $('#f-user');
        if (el) { el.value = name; state.formInputs['f-user'] = name; el.dispatchEvent(new Event('input')); }
      },
      { clearOnPick: false, minMaskLen: 1 },
    );
    attachUserValidation($('#f-user'), $('#user-hint'));
  }

  $$('#chips-tags .chip button, #chips-excl .chip button').forEach(b => {
    b.addEventListener('click', () => {
      const kind = b.dataset.kind === 'tag' ? 'tags' : 'excludeTags';
      state[kind].splice(parseInt(b.dataset.i, 10), 1);
      captureInputs();
      render();
    });
  });

  $$('#seg-sort button').forEach(b => b.addEventListener('click', () => { state.sort = b.dataset.v; captureInputs(); render(); }));

  $$('.tile[data-post]').forEach(t => {
    t.addEventListener('click', e => {
      // The tile-select checkbox handles its own clicks (with stopPropagation).
      // Anywhere else on the tile opens the post-preview overlay; from there
      // the «Открыть на сайте» button takes the user to the real post URL.
      // Suppress this click if a drag-select gesture just finished — the
      // user wasn't trying to open a preview.
      if (dragSuppressClick) {
        dragSuppressClick = false;
        return;
      }
      if (e.target.closest('.tile-select')) return;
      const n = t.dataset.post;
      const post = state.results.find(p => String(p.postNum) === n);
      if (post) openPostPreview(post);
    });
  });
  attachGridDragSelect();
  $$('.tile-select[data-post-id]').forEach(btn => {
    btn.addEventListener('click', e => {
      e.stopPropagation();
      const id = btn.dataset.postId;
      if (!id) return;
      if (state.selectedPosts.has(id)) state.selectedPosts.delete(id);
      else                              state.selectedPosts.add(id);
      render({ skipCapture: true });
    });
  });
  $('#btn-clear-selection')?.addEventListener('click', () => {
    state.selectedPosts.clear();
    render({ skipCapture: true });
  });
  $('#s-manual-select')?.addEventListener('change', e => {
    state.manualSelect = e.target.checked;
    localStorage.setItem(LS_MANUAL_SELECT, e.target.checked ? '1' : '0');
    // Turning manual mode off shouldn't leave a stale selection running the
    // next "Добавить в очередь" through the selected-items code path.
    if (!state.manualSelect) state.selectedPosts.clear();
    render({ skipCapture: true });
  });

  // Post-preview overlay (right-click on a tile)
  $('#btn-post-close')?.addEventListener('click', closePostPreview);
  $('#post-overlay-backdrop')?.addEventListener('click', e => {
    if (e.target.id === 'post-overlay-backdrop') closePostPreview();
  });
  $('#btn-post-open')?.addEventListener('click', () => {
    const n = state.postModal?.post?.postNum;
    if (n) BrowserOpenURL(`https://joyreactor.cc/post/${n}`);
  });
  $('#btn-post-save')?.addEventListener('click', () => doSaveCurrentPost());
  // Tag pills inside the post overlay: click to toggle membership in
  // state.tags. Active pills (already in the chip list) get removed;
  // inactive ones get added. Re-renders the shell so both the chip and
  // the pill flip immediately.
  $$('.post-overlay-tag[data-tag]').forEach(el => {
    el.addEventListener('click', e => {
      e.stopPropagation();
      const name = el.dataset.tag;
      if (!name) return;
      const i = state.tags.indexOf(name);
      if (i >= 0) state.tags.splice(i, 1);
      else        state.tags.push(name);
      render({ skipCapture: true });
    });
  });
  $$('.post-overlay-pic[data-url]').forEach(el => {
    el.addEventListener('click', () => {
      const u = el.dataset.url;
      if (u) BrowserOpenURL(u);
    });
  });
  $$('.media-open-btn[data-url]').forEach(b => {
    b.addEventListener('click', e => {
      e.stopPropagation();
      const u = b.dataset.url;
      if (u) BrowserOpenURL(u);
    });
  });
  // Comment links: send through the system browser instead of letting the
  // WebView navigate away from the app shell.
  $$('.comment-link[data-url]').forEach(a => {
    a.addEventListener('click', e => {
      e.preventDefault();
      e.stopPropagation();
      const u = a.dataset.url;
      if (u) BrowserOpenURL(u);
    });
  });
  // Gif-as-video has no native controls. Click toggles play/pause so users can
  // freeze an interesting frame the way they would on joyreactor.cc itself.
  $$('.is-gif-video video').forEach(v => {
    v.addEventListener('click', () => {
      if (v.paused) v.play(); else v.pause();
    });
  });
  // Surface load failures inline — when the URL pattern is wrong or the
  // codec isn't supported, this is the only way for the user to see what
  // happened (no devtools in the shipped WebView). For gif-as-video the
  // wrapper carries the original .gif as a fallback; on error we swap to
  // <img> so the user still sees the animation.
  $$('.is-video video').forEach(v => {
    v.addEventListener('error', () => {
      const wrap = v.parentElement;
      if (!wrap) return;
      const fallbackSrc = wrap.dataset.gifFallbackSrc;
      const fallbackUrl = wrap.dataset.gifFallbackUrl;
      if (fallbackSrc) {
        wrap.classList.remove('is-video', 'is-gif-video');
        wrap.innerHTML = `<img src="${escape(fallbackSrc)}" alt="" loading="lazy">`;
        if (fallbackUrl) {
          wrap.dataset.url = fallbackUrl;
          wrap.style.cursor = 'pointer';
          wrap.title = 'Открыть оригинал в браузере';
          wrap.addEventListener('click', () => BrowserOpenURL(fallbackUrl));
        }
        return;
      }
      const failedUrl = v.currentSrc || v.src;
      const errCode = v.error ? v.error.code : '?';
      wrap.innerHTML = `
        <div class="video-error">
          Не удалось загрузить видео (code ${escape(errCode)}).
          <div class="video-error-url" title="нажми, чтобы открыть в браузере">${escape(failedUrl)}</div>
        </div>`;
      wrap.querySelector('.video-error-url')?.addEventListener('click', () => {
        if (failedUrl) BrowserOpenURL(failedUrl);
      });
    });
  });

  $$('.job-row .col-actions button[data-act]').forEach(b => {
    b.addEventListener('click', () => {
      const id = b.dataset.id, act = b.dataset.act;
      if (act === 'pause')   PauseJob(id);
      if (act === 'resume')  ResumeJob(id);
      if (act === 'cancel')  CancelJob(id);
      if (act === 'remove')  doRemoveJob(id);
      if (act === 'open')    doOpenJobFolder(id);
    });
  });
}

// ----- Drag-select (rubber band marquee) -----
// In manual-select mode, holding LMB on the grid and dragging draws a
// rectangle; every preview tile it touches is added to state.selectedPosts
// (fully-downloaded ones are skipped). Tiles get the .selected class live
// during the drag; the rest of the UI (Add-selected button label, count
// indicators) syncs on mouseup via a full render.

let dragState = null;        // { startX, startY, curX, curY, dragging, initial: Set }
let marqueeEl = null;
let dragSuppressClick = false;

function attachGridDragSelect() {
  const grid = $('.results-grid');
  if (!grid) return;
  grid.addEventListener('mousedown', onGridMouseDown);
}

function onGridMouseDown(e) {
  if (!state.manualSelect) return;
  if (e.button !== 0) return;
  // The checkbox handles its own clicks — don't hijack as drag-start.
  if (e.target.closest('.tile-select')) return;
  // Suppress the browser's native "drag this image as a file" gesture — it
  // would attach an image ghost to the cursor and steal our marquee. Safe
  // here because we already filtered out the checkbox.
  e.preventDefault();
  dragState = {
    startX: e.clientX, startY: e.clientY,
    curX:   e.clientX, curY:   e.clientY,
    dragging: false,
    initial: new Set(state.selectedPosts),    // tiles already selected when drag began stay selected if dragged-off
  };
  document.addEventListener('mousemove', onDragMove);
  document.addEventListener('mouseup',   onDragEnd, { once: true });
}

function onDragMove(e) {
  if (!dragState) return;
  dragState.curX = e.clientX;
  dragState.curY = e.clientY;
  if (!dragState.dragging) {
    const dx = e.clientX - dragState.startX;
    const dy = e.clientY - dragState.startY;
    if (Math.hypot(dx, dy) < 6) return;       // movement threshold — distinguishes click from drag
    dragState.dragging = true;
    createMarquee();
    // Make text non-selectable during drag so it doesn't fight the marquee.
    document.body.classList.add('dragging-select');
  }
  updateMarquee();
  applyDragSelectionLive();
}

function onDragEnd() {
  document.removeEventListener('mousemove', onDragMove);
  const wasDragging = dragState?.dragging === true;
  destroyMarquee();
  document.body.classList.remove('dragging-select');
  dragState = null;
  if (wasDragging) {
    dragSuppressClick = true;
    // Re-render so the preset-bar action button label updates to reflect
    // the new selection count. Click on a tile right after drag-end is
    // ignored (see dragSuppressClick check in the tile handler).
    render({ skipCapture: true });
  }
}

function createMarquee() {
  if (marqueeEl) return;
  marqueeEl = document.createElement('div');
  marqueeEl.className = 'marquee';
  document.body.appendChild(marqueeEl);
}

function destroyMarquee() {
  marqueeEl?.remove();
  marqueeEl = null;
}

function updateMarquee() {
  if (!marqueeEl || !dragState) return;
  const x = Math.min(dragState.startX, dragState.curX);
  const y = Math.min(dragState.startY, dragState.curY);
  const w = Math.abs(dragState.curX - dragState.startX);
  const h = Math.abs(dragState.curY - dragState.startY);
  marqueeEl.style.left   = x + 'px';
  marqueeEl.style.top    = y + 'px';
  marqueeEl.style.width  = w + 'px';
  marqueeEl.style.height = h + 'px';
}

// applyDragSelectionLive — mutates state.selectedPosts AND toggles .selected
// classes on tiles directly, avoiding a full render on every mousemove. The
// preset-bar's "Добавить выбранные (N)" label is stale until drag-end, which
// is fine — the live tile highlight is the immediate visual feedback.
function applyDragSelectionLive() {
  if (!dragState || !dragState.dragging) return;
  const x  = Math.min(dragState.startX, dragState.curX);
  const y  = Math.min(dragState.startY, dragState.curY);
  const x2 = Math.max(dragState.startX, dragState.curX);
  const y2 = Math.max(dragState.startY, dragState.curY);
  for (const t of document.querySelectorAll('.tile[data-post-id]')) {
    const id = t.dataset.postId;
    if (!id) continue;
    const r = t.getBoundingClientRect();
    const intersects = !(r.right < x || r.left > x2 || r.bottom < y || r.top > y2);
    if (intersects) {
      const p = state.results.find(p => p.postId === id);
      if (p && tileDownloadState(p) !== 'full') state.selectedPosts.add(id);
    } else if (!dragState.initial.has(id)) {
      state.selectedPosts.delete(id);          // removed from marquee since drag started — uncheck
    }
    const shouldSelect = state.selectedPosts.has(id);
    t.classList.toggle('selected', shouldSelect);
    const btn = t.querySelector('.tile-select');
    if (btn) {
      btn.classList.toggle('on', shouldSelect);
      btn.textContent = shouldSelect ? '✓' : '';
    }
  }
}

function openSettings() {
  captureInputs();
  state.settingsOpen = true;
  render();
}

function closeSettings() {
  captureInputs();
  state.settingsOpen = false;
  render();
}

function openQueue() {
  captureInputs();
  state.queueOpen = true;
  render();
}

function closeQueue() {
  captureInputs();
  state.queueOpen = false;
  render();
}

// openPresetsManager — fetch the latest preset detail payload, open the
// modal, and start the 1-second countdown tick. The tick decrements the
// data-next-due-sec attribute on each row in place (cheap, no re-render),
// and every 60 ticks (≈1 min) re-pulls the backend payload so a scheduler
// run that fires while the modal is open is reflected without manual
// reload.
async function openPresetsManager() {
  captureInputs();
  await refreshPresetViews();
  state.presetsManagerOpen = true;
  render();
  startPresetsTick();
}

function closePresetsManager() {
  stopPresetsTick();
  captureInputs();
  state.presetsManagerOpen = false;
  render();
}

async function refreshPresetViews() {
  try {
    state.presetViews = (await ListPresetsDetailed()) || [];
  } catch {
    state.presetViews = [];
  }
}

function startPresetsTick() {
  stopPresetsTick();
  _presetsTickCount = 0;
  _presetsTickTimer = setInterval(tickPresetsCountdown, 1000);
}

function stopPresetsTick() {
  if (_presetsTickTimer) {
    clearInterval(_presetsTickTimer);
    _presetsTickTimer = 0;
  }
}

// tickPresetsCountdown — runs every second while the preset modal is open.
// For each .preset-next-due cell, read the source-of-truth attribute,
// decrement, rewrite the human label. After 60 ticks (≈1 min) also re-fetch
// the backend payload so the modal stays in sync with scheduler runs that
// happened in the background (LastAutoPullAt would otherwise stay frozen
// on whatever value was loaded when the modal opened).
async function tickPresetsCountdown() {
  if (!state.presetsManagerOpen) {
    stopPresetsTick();
    return;
  }
  _presetsTickCount++;
  if (_presetsTickCount >= 60) {
    _presetsTickCount = 0;
    await refreshPresetViews();
    render();
    return;
  }
  for (const el of document.querySelectorAll('.preset-next-due[data-next-due-sec]')) {
    const cur = parseInt(el.getAttribute('data-next-due-sec'), 10);
    if (!Number.isFinite(cur)) continue;
    const next = cur - 1;
    el.setAttribute('data-next-due-sec', String(next));
    el.textContent = '→ ' + formatNextDue(next);
  }
}

async function openPostPreview(post) {
  captureInputs();
  state.postModal = { post, comments: null, loading: true, error: null };
  render({ skipCapture: true });
  let seq = ++openPostPreview.seq;
  try {
    const res = await PostComments(post.postId);
    if (seq !== openPostPreview.seq || !state.postModal) return;
    if (res.error) state.postModal.error = res.error;
    else           state.postModal.comments = res.comments || [];
  } catch (e) {
    if (seq !== openPostPreview.seq || !state.postModal) return;
    state.postModal.error = String(e);
  } finally {
    if (state.postModal) {
      state.postModal.loading = false;
      render({ skipCapture: true });
    }
  }
}
openPostPreview.seq = 0;

function closePostPreview() {
  state.postModal = null;
  openPostPreview.seq++;
  render({ skipCapture: true });
}

// ----- Actions -----
function collectInput(page) {
  const num = sel => {
    const v = $(sel)?.value;
    if (v === '' || v == null) return null;
    const n = parseInt(v, 10);
    return Number.isFinite(n) ? n : null;
  };
  const username = state.showAuthor ? (state.formInputs['f-user'] || $('#f-user')?.value || '') : '';
  const minRating = state.showRating ? (num('#f-min-rating') ?? parseNullableStored('f-min-rating')) : null;
  const maxRating = state.showRating ? (num('#f-max-rating') ?? parseNullableStored('f-max-rating')) : null;
  return {
    query: $('#f-query')?.value || '',
    tags: state.tags,
    excludeTags: state.excludeTags,
    username,
    minRating,
    maxRating,
    sort: state.sort,
    showNsfw: $('#f-nsfw')?.checked || false,
    onlyNsfw: $('#f-only-nsfw')?.checked || false,
    showUnsafe: $('#f-unsafe')?.checked || false,
    // Auth-gated filters are visually disabled when logged out, but their
    // backing state.formInputs values can survive a logout and still read
    // .checked === true. Force them off without a session.
    onlyFavorite: state.user ? ($('#f-favorite')?.checked || false) : false,
    useBlockedTags: state.user ? ($('#f-use-blocked')?.checked || false) : false,
    page,
  };
}

// parseNullableStored reads an int from state.formInputs (used when an input
// element is currently hidden but the value is still preserved in state).
function parseNullableStored(id) {
  const v = state.formInputs[id];
  if (v === '' || v == null) return null;
  const n = parseInt(v, 10);
  return Number.isFinite(n) ? n : null;
}

function parseStoredInt(id, def = 0) {
  return parseNullableStored(id) ?? def;
}

function dedupPosts(arr) {
  const seen = new Set();
  const out = [];
  for (const p of arr) {
    const k = p.postId || p.postNum;
    if (seen.has(k)) continue;
    seen.add(k);
    out.push(p);
  }
  return out;
}

// filterByKinds drops posts where none of the pictures match the Settings
// media-kinds toggle. Empty state.kinds means "any" — no-op pass-through.
function filterByKinds(posts) {
  if (!state.kinds.length) return posts;
  const want = new Set(state.kinds);
  return posts.filter(p => (p.pictures || []).some(pic => want.has(pic.kind)));
}

function sortResults(arr) {
  if (state.sort === 'date') {
    return [...arr].sort((a, b) => new Date(b.createdAt) - new Date(a.createdAt));
  }
  // 'rating' (default)
  return [...arr].sort((a, b) => (b.rating ?? 0) - (a.rating ?? 0));
}

async function doSearch(reset) {
  captureInputs();
  state.searching = true;
  hideToast();
  // Honour the user's PageFrom/PageTo for the preview too — start at
  // PageFrom on a fresh search so what's shown lines up with what would
  // actually be downloaded. The range is only active when the user has
  // turned on "Диапазон страниц поиска" in Settings; otherwise the
  // stale inputs (if hidden but still in formInputs) are ignored.
  const pageFrom = state.showPageRange ? parseStoredInt('f-page-from', 0) : 0;
  const pageTo   = state.showPageRange ? parseStoredInt('f-page-to',   0) : 0;
  if (reset) {
    state.results = [];
    state.count = null;
    state.exhausted = false;
    state.page = Math.max(0, pageFrom - 1);   // next page = pageFrom
    // The previously-selected post ids would no longer match any visible
    // tile, so clear them to avoid invisible selection state.
    state.selectedPosts.clear();
  }
  render();

  const target  = Math.max(1, state.previewBatch || 25);
  const startLen = state.results.length;
  const maxPages = 30; // safety cap: avoid runaway loops on weird API responses
  let   fetched  = 0;

  try {
    while (state.results.length - startLen < target && fetched < maxPages) {
      const nextPage = state.page + 1;
      if (pageTo > 0 && nextPage > pageTo) {
        // User-defined upper bound reached.
        state.page = nextPage - 1;
        state.exhausted = true;
        break;
      }
      const res = await Search(collectInput(nextPage));
      if (res.error) {
        showToast('error', res.error);
        break;
      }
      if (res.count != null) state.count = res.count;
      const rawPosts = res.posts || [];
      if (rawPosts.length === 0) {
        // End of results — bump page so the next click doesn't re-query the same one.
        state.page = nextPage;
        state.exhausted = true;
        break;
      }
      // Apply the Settings → "Тип медиа" filter to the preview itself: drop
      // posts whose pictures don't include any of the selected kinds. Without
      // this, only the download phase honoured the toggle and previews
      // showed everything regardless.
      const posts = filterByKinds(rawPosts);
      // Server-side sort isn't reliable across paginated calls (e.g. sortByRating
      // shuffles between pages). Dedup by postId — JR sometimes overlaps pages —
      // then re-sort client-side by the user's chosen mode.
      state.results = sortResults(dedupPosts(state.results.concat(posts)));
      state.page = nextPage;
      fetched++;
      // Mid-loop render so the user sees the grid filling in instead of a long blank.
      render({ skipCapture: true });
      // If we've hit JR's total count, stop early — more API calls would just return [].
      if (state.count != null && state.results.length >= state.count) {
        state.exhausted = true;
        break;
      }
    }
  } catch (e) {
    showToast('error', String(e));
  } finally {
    state.searching = false;
    render();
  }
}

async function doAddJob() {
  captureInputs();
  const outDir = $('#f-outdir').value.trim();
  if (!outDir) {
    showToast('error', 'Укажи папку для скачивания.');
    render();
    return;
  }
  // If the user is in manual-select mode and has picked specific tiles, build
  // a SelectedItems payload from the previews currently in state. The backend
  // bypasses Search and downloads exactly these pictures. When manual mode is
  // off, ignore any leftover selection — it's invisible to the user.
  //
  // Skip fully-downloaded posts as a final guard — the UI already disables
  // the checkbox on them, but this catches the case where a parallel job
  // finished between selection and Add click.
  const selectedItems = state.manualSelect && state.selectedPosts.size > 0
    ? state.results
        .filter(p => state.selectedPosts.has(p.postId) && tileDownloadState(p) !== 'full')
        .map(p => collectSelectedItem(p))
    : null;
  if (selectedItems && selectedItems.length === 0) {
    showToast('error', 'Выбранные посты уже скачаны в эту папку.');
    render({ skipCapture: true });
    return;
  }
  const input = {
    ...collectInput(1),
    mediaKinds: [...state.kinds],
    minWidth:  parseStoredInt('f-min-width', 0),
    minHeight: parseStoredInt('f-min-height', 0),
    dateFrom:  state.formInputs['f-from'] || '',
    dateTo:    state.formInputs['f-to'] || '',
    limit:     parseStoredInt('f-limit', 0),
    workers:   parseStoredInt('f-workers', 4) || 4,
    pageFrom:  state.showPageRange ? parseStoredInt('f-page-from', 0) : 0,
    pageTo:    state.showPageRange ? parseStoredInt('f-page-to',   0) : 0,
    filenameFormat: state.filenameFormat || 'id',
    outDir,
    ...(selectedItems ? { selectedItems } : {}),
  };
  hideToast();
  // Empty name → backend uses defaultJobName(in) which builds a label from
  // the filters (#tag, @user, ≥rating, …) or "Выбранные ×N" in manual mode.
  const res = await AddJob('', input);
  if (res.error) {
    showToast('error', res.error);
  } else {
    const n = selectedItems ? selectedItems.length : null;
    showToast('success', (n ? `${n} пост(а/ов) добавлено в очередь` : 'Задача добавлена в очередь') + ' — открой «📋 Очередь» наверху, чтобы посмотреть прогресс');
    localStorage.setItem(LS_OUTDIR, outDir);
    if (selectedItems) state.selectedPosts.clear();
  }
  render({ skipCapture: true });
}

// collectSelectedItem packs a Preview into the SelectedItem shape the Go
// backend expects (postId, tags, and the picture URLs we already received
// from Search — no second round-trip needed).
function collectSelectedItem(p) {
  return {
    postId: p.postId,
    tags: p.tags || [],
    pictures: (p.pictures || []).map(pic => ({
      attrId: pic.attrId,
      url:    pic.url,
      type:   pic.type,
    })),
  };
}

// doSaveCurrentPost enqueues just the post currently shown in the right-
// click overlay. Same backend path as the bulk-selection AddJob, but with
// a single item — keeps the "this one specifically" UX flow distinct from
// "everything matching the filters".
async function doSaveCurrentPost() {
  const post = state.postModal?.post;
  if (!post) return;
  captureInputs();
  const outDir = (state.formInputs['f-outdir'] || '').trim();
  if (!outDir) {
    showToast('error', 'Сначала укажи папку для скачивания в шапке.');
    render({ skipCapture: true });
    return;
  }
  const input = {
    ...collectInput(1),
    mediaKinds: [...state.kinds],
    minWidth:  parseStoredInt('f-min-width', 0),
    minHeight: parseStoredInt('f-min-height', 0),
    dateFrom:  state.formInputs['f-from'] || '',
    dateTo:    state.formInputs['f-to'] || '',
    limit:     parseStoredInt('f-limit', 0),
    workers:   parseStoredInt('f-workers', 4) || 4,
    filenameFormat: state.filenameFormat || 'id',
    outDir,
    selectedItems: [collectSelectedItem(post)],
  };
  const res = await AddJob('', input);
  if (res.error) {
    showToast('error', res.error);
  } else {
    showToast('success', `Пост #${post.postNum} добавлен в очередь`);
  }
  render({ skipCapture: true });
}

async function doClearFinished() {
  await ClearFinishedJobs();
}

async function doRemoveJob(id) {
  const err = await RemoveJob(id);
  if (err) {
    showToast('error', err);
    render();
  }
}

async function doOpenJobFolder(id) {
  const j = state.jobs.find(x => x.id === id);
  if (!j) return;
  const err = await OpenOutputFolder(j.outDir);
  if (err) {
    showToast('error', `не открылась: ${err}`);
    render();
  }
}

async function doOpenCurrentOutdir() {
  captureInputs();
  const path = (state.formInputs['f-outdir'] || '').trim();
  if (!path) {
    showToast('error', 'Папка не задана.');
    render({ skipCapture: true });
    return;
  }
  const err = await OpenOutputFolder(path);
  if (err) {
    showToast('error', `не открылась: ${err}`);
    render({ skipCapture: true });
  }
}

async function doPickFolder() {
  captureInputs();
  const path = await PickFolder();
  if (path) {
    state.formInputs['f-outdir'] = path;
    localStorage.setItem(LS_OUTDIR, path);
    syncOutdirToPreset();
    await refreshDownloadedKeys();
    render({ skipCapture: true });
  }
}

function openLogin() {
  const modal = document.createElement('div');
  modal.className = 'modal-backdrop';
  modal.innerHTML = `
    <div class="modal">
      <h3>Вход на Joyreactor</h3>
      <div class="field">
        <label>Имя пользователя</label>
        <input type="text" id="m-name" autocomplete="username">
      </div>
      <div class="field">
        <label>Пароль</label>
        <input type="password" id="m-pass" autocomplete="current-password">
      </div>
      <div id="m-err" style="color: #fca5a5; font-size: 13px; min-height: 18px;"></div>
      <div class="actions">
        <button class="btn" id="m-cancel">Отмена</button>
        <button class="btn primary" id="m-go">Войти</button>
      </div>
    </div>`;
  document.body.appendChild(modal);
  const close = () => modal.remove();
  modal.querySelector('#m-name').focus();
  modal.querySelector('#m-cancel').addEventListener('click', close);
  modal.addEventListener('keydown', e => { if (e.key === 'Escape') close(); });
  const submit = async () => {
    const name = modal.querySelector('#m-name').value.trim();
    const pass = modal.querySelector('#m-pass').value;
    if (!name || !pass) return;
    modal.querySelector('#m-go').disabled = true;
    try {
      const res = await Login(name, pass);
      if (res.success) {
        state.user = res.username;
        showToast('success', `Добро пожаловать, ${res.username}!`);
        close();
        render();
      } else {
        modal.querySelector('#m-err').textContent = res.error || 'не удалось войти';
        modal.querySelector('#m-go').disabled = false;
      }
    } catch (e) {
      modal.querySelector('#m-err').textContent = String(e);
      modal.querySelector('#m-go').disabled = false;
    }
  };
  modal.querySelector('#m-go').addEventListener('click', submit);
  modal.querySelector('#m-pass').addEventListener('keydown', e => {
    if (e.key === 'Enter') submit();
  });
}

async function doLogout() {
  await Logout();
  state.user = '';
  showToast('success', 'Вышли');
  render();
}

// ----- Presets -----
async function refreshPresets() {
  try { state.presets = (await ListPresets()) || []; }
  catch { state.presets = []; }
}

async function applyPreset(name) {
  if (!name) {
    state.currentPreset = '';
    state.currentPresetAutoPull = false;
    localStorage.removeItem(LS_LAST_PRESET);
    render();
    return;
  }
  const p = await GetPreset(name);
  if (!p) {
    showToast('error', `Пресет «${name}» не найден`);
    render();
    return;
  }
  state.currentPreset = name;
  state.currentPresetAutoPull = !!p.autoPull;
  localStorage.setItem(LS_LAST_PRESET, name);
  // Switching presets implies the user is moving to a different topic — drop
  // any pending tile selection so it doesn't leak across contexts.
  state.selectedPosts.clear();
  state.tags = p.tags || [];
  state.excludeTags = p.excludeTags || [];
  state.sort = p.sort || 'rating';
  state.kinds = sanitizeKinds(p.mediaKinds);
  // Output dir is part of the preset — when loading, swap to the preset's
  // dir. Empty preset.outDir falls back to the last-typed value so users
  // upgrading from the old single-dir setup don't lose their folder.
  const outDir = p.outDir || state.formInputs['f-outdir'] || '';
  state.formInputs = {
    ...state.formInputs,
    'f-query': p.query || '',
    'f-user': p.username || '',
    'f-min-rating': p.minRating != null ? String(p.minRating) : '',
    'f-max-rating': p.maxRating != null ? String(p.maxRating) : '',
    'f-nsfw': !!p.showNsfw,
    'f-only-nsfw': !!p.onlyNsfw,
    'f-unsafe': !!p.showUnsafe,
    'f-favorite': !!p.onlyFavorite,
    'f-min-width': p.minWidth ? String(p.minWidth) : '',
    'f-min-height': p.minHeight ? String(p.minHeight) : '',
    'f-from': p.dateFrom || '',
    'f-to': p.dateTo || '',
    'f-limit': p.limit ? String(p.limit) : '',
    'f-workers': p.workers ? String(p.workers) : '4',
    'f-page-from': p.pageFrom ? String(p.pageFrom) : '',
    'f-page-to':   p.pageTo   ? String(p.pageTo)   : '',
    'f-outdir': outDir,
  };
  if (outDir) localStorage.setItem(LS_OUTDIR, outDir);
  await refreshDownloadedKeys();
  showToast('success', `Загружен пресет «${name}»`);
  render({ skipCapture: true });
}

function sanitizeKinds(arr) {
  const ok = new Set(['image','gif','video']);
  return Array.isArray(arr) ? arr.filter(k => ok.has(k)) : [];
}

function collectPreset() {
  captureInputs();
  const num = id => {
    const v = state.formInputs[id];
    if (v === '' || v == null) return null;
    const n = parseInt(v, 10);
    return Number.isFinite(n) ? n : null;
  };
  return {
    query: state.formInputs['f-query'] || '',
    tags: [...state.tags],
    excludeTags: [...state.excludeTags],
    username: state.formInputs['f-user'] || '',
    minRating: num('f-min-rating'),
    maxRating: num('f-max-rating'),
    sort: state.sort,
    showNsfw: !!state.formInputs['f-nsfw'],
    onlyNsfw: !!state.formInputs['f-only-nsfw'],
    showUnsafe: !!state.formInputs['f-unsafe'],
    onlyFavorite: !!state.formInputs['f-favorite'],
    mediaKinds: [...state.kinds],
    minWidth: num('f-min-width') || 0,
    minHeight: num('f-min-height') || 0,
    dateFrom: state.formInputs['f-from'] || '',
    dateTo: state.formInputs['f-to'] || '',
    limit: num('f-limit') || 0,
    workers: num('f-workers') || 4,
    pageFrom: num('f-page-from') || 0,
    pageTo: num('f-page-to') || 0,
    outDir: state.formInputs['f-outdir'] || '',
  };
}

// refreshDownloadedKeys reloads the manifest for the current outdir so
// already-downloaded posts get the green check on preview tiles. Cheap to
// call — manifest reads are local file I/O, no network. Side-effect: any
// post that just became fully-downloaded is removed from state.selectedPosts
// so it doesn't quietly stay "selected" with no actionable work.
async function refreshDownloadedKeys() {
  const outDir = state.formInputs['f-outdir'] || '';
  if (!outDir) {
    state.downloaded = { outDir: '', keys: new Set() };
    return;
  }
  try {
    const keys = (await ManifestKeys(outDir)) || [];
    state.downloaded = { outDir, keys: new Set(keys) };
  } catch {
    state.downloaded = { outDir, keys: new Set() };
  }
  cleanupFullyDownloadedSelection();
}

function cleanupFullyDownloadedSelection() {
  if (state.selectedPosts.size === 0) return;
  for (const id of Array.from(state.selectedPosts)) {
    const p = state.results.find(r => r.postId === id);
    if (p && tileDownloadState(p) === 'full') state.selectedPosts.delete(id);
  }
}

// maybeRefreshDownloadedKeys throttles refreshes to ~1Hz so live job:update
// events don't pummel the disk with manifest reads on every progress tick.
// Schedules a re-render after the refresh completes so newly-downloaded
// posts get their check marks without waiting for the next event.
let _lastManifestRefresh = 0;
function maybeRefreshDownloadedKeys() {
  const now = Date.now();
  if (now - _lastManifestRefresh < 1000) return;
  _lastManifestRefresh = now;
  refreshDownloadedKeys().then(() => updateTileDownloadedBadges());
}

// updateTileDownloadedBadges — surgical refresh of just the .tile-have
// green-check badges on existing tiles. Called from the 1Hz manifest
// poll during active downloads. Avoids the full #app innerHTML rebuild
// that render() would do, which is what was causing visible judder on
// inputs / hovers / autocomplete / scroll while pictures were landing
// in the active folder.
function updateTileDownloadedBadges() {
  const grid = document.querySelector('.results-grid');
  if (!grid) return;
  for (const tile of grid.querySelectorAll('.tile[data-post-id]')) {
    const id = tile.getAttribute('data-post-id');
    const p = state.results.find(x => x.postId === id);
    if (!p) continue;
    const want = tileDownloadState(p); // '' | 'partial' | 'full'
    const leftCorner = tile.querySelector('.tile-corner.left');
    if (!leftCorner) continue;
    let have = leftCorner.querySelector('.tile-badge.tile-have');
    if (want === '') {
      if (have) have.remove();
      continue;
    }
    const wantClass = want === 'full'
      ? 'tile-badge tile-have'
      : 'tile-badge tile-have partial';
    const wantTitle = want === 'full'
      ? 'все картинки поста уже скачаны в текущую папку'
      : 'часть картинок поста уже скачаны';
    if (have) {
      if (have.className !== wantClass) have.className = wantClass;
      if (have.title     !== wantTitle) have.title     = wantTitle;
    } else {
      have = document.createElement('div');
      have.className   = wantClass;
      have.title       = wantTitle;
      have.textContent = '✓';
      leftCorner.prepend(have);
    }
  }
}

// syncOutdirToPreset persists the current outdir field back into the selected
// preset. Called whenever the user edits/picks a folder while a preset is
// active so the binding stays "hard" — no implicit divergence between the
// preset and what's in the input.
//
// We reload the stored preset and only mutate outDir to avoid silently
// saving the user's other in-flight edits (those still go through the
// explicit "Save as…" flow).
async function syncOutdirToPreset() {
  if (!state.currentPreset) return;
  const existing = await GetPreset(state.currentPreset);
  if (!existing) return;
  existing.outDir = state.formInputs['f-outdir'] || '';
  await SavePreset(state.currentPreset, existing);
}

async function doSavePreset() {
  showPrompt('Сохранить пресет', 'Имя пресета', state.currentPreset || '', async name => {
    name = name.trim();
    if (!name) return false;
    const err = await SavePreset(name, collectPreset());
    if (err) return { error: err };
    state.currentPreset = name;
    localStorage.setItem(LS_LAST_PRESET, name);
    await refreshPresets();
    showToast('success', `Пресет «${name}» сохранён`);
    render();
    return true;
  });
}

async function doDeletePreset() {
  const name = state.currentPreset;
  if (!name) return;
  showConfirm(`Удалить пресет «${name}»?`, async () => {
    const err = await DeletePreset(name);
    if (err) showToast('error', err);
    else {
      state.currentPreset = '';
      localStorage.removeItem(LS_LAST_PRESET);
      await refreshPresets();
      showToast('success', `Пресет «${name}» удалён`);
    }
    render();
  });
}

// onPresetAction — dispatcher for action buttons in the preset-manager
// modal rows. Each branch does one thing and routes through the same
// confirmation/toast UX as the inline preset controls would.
async function onPresetAction(name, act) {
  switch (act) {
    case 'run': {
      const err = await RunPresetNow(name);
      if (err) {
        showToast('error', `Не удалось запустить «${name}»: ${err}`);
        return;
      }
      showToast('success', `Запущено: «${name}»`);
      // The MarkAutoPullStarted side-effect just changed LastAutoPullAt;
      // refresh views so the row's last/next cells reflect that.
      await refreshPresetViews();
      render();
      return;
    }
    case 'folder': {
      const v = state.presetViews.find(x => x.name === name);
      if (!v || !v.outDir) return;
      const err = await OpenOutputFolder(v.outDir);
      if (err) showToast('error', err);
      return;
    }
    case 'load': {
      // Mirror selecting the preset in the form dropdown — load fields and
      // close the modal so the user lands on the editable filters card.
      //
      // Order matters: stop the tick and flip the modal flag BEFORE
      // applyPreset's render fires, but do NOT call closePresetsManager()
      // (it would captureInputs() and read stale DOM values for the form
      // fields behind the still-rendered modal, clobbering the f-outdir
      // we just loaded). applyPreset's own render({skipCapture:true})
      // will then rebuild the shell without the modal and push the
      // freshly-loaded values into the DOM via restoreInputs.
      stopPresetsTick();
      state.presetsManagerOpen = false;
      await applyPreset(name);
      showToast('success', `Пресет «${name}» загружен в форму`);
      return;
    }
    case 'delete': {
      showConfirm(`Удалить пресет «${name}»?`, async () => {
        const err = await DeletePreset(name);
        if (err) {
          showToast('error', err);
          return;
        }
        // Cascade: if the deleted preset was the one selected in the form,
        // clear that selection too so the form doesn't keep a dangling name.
        if (state.currentPreset === name) {
          state.currentPreset = '';
          state.currentPresetAutoPull = false;
          localStorage.removeItem(LS_LAST_PRESET);
        }
        await refreshPresets();
        await refreshPresetViews();
        render();
        showToast('success', `Пресет «${name}» удалён`);
      });
      return;
    }
  }
}

// ----- Modal helpers -----
function showPrompt(title, label, defaultValue, onSubmit) {
  const m = document.createElement('div');
  m.className = 'modal-backdrop';
  m.innerHTML = `
    <div class="modal">
      <h3>${escape(title)}</h3>
      <div class="field">
        <label>${escape(label)}</label>
        <input type="text" id="p-input" value="${escape(defaultValue)}">
      </div>
      <div id="p-err" style="color: #fca5a5; font-size: 13px; min-height: 18px;"></div>
      <div class="actions">
        <button class="btn" id="p-cancel">Отмена</button>
        <button class="btn primary" id="p-ok">Ок</button>
      </div>
    </div>`;
  document.body.appendChild(m);
  const input = m.querySelector('#p-input');
  input.focus(); input.select();
  const close = () => m.remove();
  m.querySelector('#p-cancel').addEventListener('click', close);
  const submit = async () => {
    m.querySelector('#p-ok').disabled = true;
    try {
      const r = await onSubmit(input.value);
      if (r === true) { close(); return; }
      if (r && r.error) m.querySelector('#p-err').textContent = r.error;
      m.querySelector('#p-ok').disabled = false;
    } catch (e) {
      m.querySelector('#p-err').textContent = String(e);
      m.querySelector('#p-ok').disabled = false;
    }
  };
  m.querySelector('#p-ok').addEventListener('click', submit);
  input.addEventListener('keydown', e => {
    if (e.key === 'Enter') submit();
    if (e.key === 'Escape') close();
  });
}

function showConfirm(text, onYes) {
  const m = document.createElement('div');
  m.className = 'modal-backdrop';
  m.innerHTML = `
    <div class="modal">
      <h3>${escape(text)}</h3>
      <div class="actions">
        <button class="btn" id="c-no">Отмена</button>
        <button class="btn danger" id="c-yes">Удалить</button>
      </div>
    </div>`;
  document.body.appendChild(m);
  m.querySelector('#c-yes').focus();
  const close = () => m.remove();
  m.querySelector('#c-no').addEventListener('click', close);
  m.querySelector('#c-yes').addEventListener('click', async () => { close(); await onYes(); });
  m.addEventListener('keydown', e => { if (e.key === 'Escape') close(); });
}

// ----- Generic autocomplete -----
function attachAutocomplete(input, lookup, onPick, opts = {}) {
  if (!input) return;
  const clearOnPick = opts.clearOnPick !== false;
  const minMaskLen = opts.minMaskLen ?? 1;

  let timer = 0;
  let items = [];
  let active = -1;
  let dropdown = null;
  let lastMask = '';
  let reqSeq = 0;

  const close = () => {
    if (dropdown) { dropdown.remove(); dropdown = null; }
    items = []; active = -1;
  };
  const updateActive = () => {
    if (!dropdown) return;
    [...dropdown.querySelectorAll('.ac-item')].forEach((el, i) => {
      el.classList.toggle('active', i === active);
      if (i === active) el.scrollIntoView({ block: 'nearest' });
    });
  };
  const open = (suggestions) => {
    close();
    if (!suggestions || suggestions.length === 0) return;
    items = suggestions; active = 0;
    dropdown = document.createElement('div');
    dropdown.className = 'autocomplete';
    dropdown.innerHTML = items.map((s, i) => `
      <div class="ac-item ${i === 0 ? 'active' : ''}" data-i="${i}">
        <span class="ac-name">${escape(s.name)}</span>
        <span class="ac-meta">
          ${s.nsfw ? '<span class="ac-nsfw">NSFW</span>' : ''}
          ${s.count != null ? `<span class="ac-count">${formatCount(s.count)}</span>` : ''}
        </span>
      </div>`).join('');
    input.parentElement.appendChild(dropdown);
    [...dropdown.querySelectorAll('.ac-item')].forEach(el => {
      el.addEventListener('mousedown', e => { e.preventDefault(); pick(items[parseInt(el.dataset.i, 10)]); });
    });
  };
  const pick = (item) => {
    if (!item) return;
    onPick(item.name);
    if (clearOnPick) input.value = '';
    lastMask = ''; close(); input.focus();
  };
  input.addEventListener('input', e => {
    clearTimeout(timer);
    const mask = e.target.value.trim();
    if (!mask || mask.length < minMaskLen) { close(); lastMask = ''; return; }
    if (mask === lastMask) return;
    lastMask = mask;
    const my = ++reqSeq;
    timer = setTimeout(async () => {
      try {
        const res = await lookup(mask);
        if (my !== reqSeq) return;
        open(res || []);
      } catch {}
    }, 200);
  });
  input.addEventListener('keydown', e => {
    if (e.key === 'ArrowDown') { e.preventDefault(); if (items.length) { active = (active + 1) % items.length; updateActive(); } }
    else if (e.key === 'ArrowUp') { e.preventDefault(); if (items.length) { active = (active - 1 + items.length) % items.length; updateActive(); } }
    else if (e.key === 'Enter') {
      e.preventDefault();
      if (active >= 0 && items[active]) pick(items[active]);
      else { const v = input.value.trim(); if (v) { onPick(v); input.value = ''; lastMask = ''; close(); } }
    } else if (e.key === 'Escape') close();
  });
  input.addEventListener('blur', () => setTimeout(close, 120));
}

function formatCount(n) {
  if (!n) return '0';
  if (n >= 1000) return Math.round(n / 100) / 10 + 'k';
  return String(n);
}

function attachUserValidation(input, hint) {
  if (!input || !hint) return;
  let timer = 0, seq = 0;
  const update = (cls, text) => { hint.className = 'field-hint ' + (cls || ''); hint.textContent = text || ''; };
  input.addEventListener('input', () => {
    clearTimeout(timer);
    const name = input.value.trim();
    if (!name) { update('', ''); return; }
    update('pending', '…');
    const my = ++seq;
    timer = setTimeout(async () => {
      try {
        const r = await CheckUser(name);
        if (my !== seq) return;
        if (r && r.found) update('ok', `✓ ${r.username} · ${r.postNum} постов`);
        else update('err', 'не найден');
      } catch { if (my === seq) update('err', 'ошибка проверки'); }
    }, 350);
  });
}

// ----- Events -----
EventsOn('job:update', j => {
  const prev = state.jobs.find(x => x.id === j.id);
  const stateChanged = !prev || prev.state !== j.state;
  const i = state.jobs.findIndex(x => x.id === j.id);
  if (i >= 0) state.jobs[i] = j;
  else state.jobs.push(j);
  // Picture just landed in the folder the user is previewing → refresh
  // the manifest so tile checks turn green. Surgical: only the .tile-have
  // badges in the existing grid get patched — no full shell rebuild.
  const cur = state.formInputs['f-outdir'] || '';
  if (cur && j.outDir === cur) {
    if (isFinished(j.state)) {
      _lastManifestRefresh = 0;            // force, ignore throttle
      refreshDownloadedKeys().then(() => updateTileDownloadedBadges());
    } else {
      maybeRefreshDownloadedKeys();
    }
  }
  // Decide whether the visible UI needs a re-render.
  //   - stateChanged (running → done/canceled/error, add, remove) flips
  //     the topbar queue badge and any queue-table row state — render
  //     immediately for snappy feedback.
  //   - queueOpen + only counter tick (saved/skipped/failed bumped while
  //     state stays 'running') → bgRender (250ms debounce). The queue
  //     modal is the only surface that shows those counters live.
  //   - otherwise (queue closed, only progress tick) → DO NOTHING. The
  //     state.jobs[i] mutation above is enough; openQueue() does its
  //     own render() and picks up whatever has accumulated.
  if (stateChanged) {
    render();
  } else if (state.queueOpen) {
    bgRender();
  }
});

EventsOn('job:removed', id => {
  state.jobs = state.jobs.filter(j => j.id !== id);
  render();
});

EventsOn('auth:blocked-tags', n => {
  state.blockedCount = n || 0;
  render();
});

// ----- Boot -----
document.addEventListener('keydown', e => {
  if (e.key !== 'Escape') return;
  if (state.postModal)           { closePostPreview();     return; }
  if (state.queueOpen)           { closeQueue();           return; }
  if (state.presetsManagerOpen)  { closePresetsManager();  return; }
  if (state.settingsOpen)        { closeSettings();        return; }
});

// When the user comes back from File Explorer (e.g. after deleting files
// or the manifest manually), re-read the manifest so green checkmarks
// reflect the actual on-disk state. visibilitychange is the belt-and-
// suspenders for Win11 virtual-desktop switches where 'focus' is patchy.
async function refreshOnReturn() {
  if (!(state.formInputs['f-outdir'] || '').trim()) return;
  await refreshDownloadedKeys();
  render({ skipCapture: true });
}
window.addEventListener('focus', refreshOnReturn);
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible') refreshOnReturn();
});

(async () => {
  try { state.user = await Me(); } catch {}
  try {
    const ws = await GetWindowSettings();
    if (ws) state.windowSettings = {
      width:     ws.width  || state.windowSettings.width,
      height:    ws.height || state.windowSettings.height,
      maximized: !!ws.maximized,
    };
  } catch {}
  try {
    const app = await GetAppSettings();
    if (app) {
      if (app.manifestMode) state.appSettings.manifestMode = app.manifestMode;
      if (app.autoPullIntervalHours) state.appSettings.autoPullIntervalHours = app.autoPullIntervalHours;
      state.appSettings.autostart = !!app.autostart;
      state.appSettings.startMinimized = !!app.startMinimized;
      state.appSettings.minimizeToTrayOnClose = !!app.minimizeToTrayOnClose;
      // Absent (legacy settings.json) ⇒ stay with the default true.
      state.appSettings.hideRemovedPosts = app.hideRemovedPosts == null ? true : !!app.hideRemovedPosts;
      state.appSettings.socks5Enabled = !!app.socks5Enabled;
      state.appSettings.socks5Addr = app.socks5Addr || '';
      state.appSettings.onionBaseURL = app.onionBaseURL || '';
      state.appSettings.recoverDmcaViaOnion = !!app.recoverDmcaViaOnion;
    }
  } catch {}
  await refreshPresets();
  try { state.blockedCount = (await BlockedTagCount()) || 0; } catch {}
  try { state.jobs = (await ListJobs()) || []; } catch {}
  // Auto-restore the last preset the user touched — applyPreset itself
  // re-saves it to localStorage, so this is idempotent. Falls back silently
  // if the preset was deleted while the app was closed.
  const lastPreset = lsStr(LS_LAST_PRESET, '');
  if (lastPreset && state.presets.includes(lastPreset)) {
    await applyPreset(lastPreset);          // also calls refreshDownloadedKeys
  } else {
    await refreshDownloadedKeys();           // ad-hoc mode: still mark via LS_OUTDIR
    render();
  }
})();
