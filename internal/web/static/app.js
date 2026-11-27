// Entry point: keeps the board and the history up to date and remembers the pressure unit and the range.

import {
  ME_REFRESH_MS,
  accountDelay,
  describeAccount,
  initialAccount,
  onMe,
  onSignOutFailed,
  onSignOutStarted,
  onSignedOut,
  readSignIn,
  withNotice,
  withoutSignIn,
} from './account.js';
import { getLatest, getMe, getReadings, getStats, signOut } from './api.js';
import { createHistoryView } from './chart-view.js';
import { DEFAULT_PRESSURE_UNIT, isPressureUnit } from './format.js';
import {
  RANGES,
  DEFAULT_RANGE,
  describeHistory,
  historyDelay,
  initialHistory,
  isRange,
  onHistory,
  onHistoryFailure,
  pickRange,
  statsDelay,
} from './history.js';
import { createLoop } from './loop.js';
import { describe, initialState, nextDelay, onFailure, onLatest } from './model.js';
import { startShell } from './pwa.js';
import { createView } from './view.js';

const UNIT_KEY = 'climate.pressureUnit';
const RANGE_KEY = 'climate.range';
const MIN_GAP_MS = 5000; // a tab that comes back to the front does not ask for the latest reading again within this time
const DAY_RANGE = '24h'; // the range of the numbers under the board

// The storage can be missing or throw (private windows, blocked site data): the page works without it.
function load(key, accepts, fallback) {
  try {
    const stored = localStorage.getItem(key);
    return accepts(stored) ? stored : fallback;
  } catch {
    return fallback;
  }
}

function save(key, value) {
  try {
    localStorage.setItem(key, value);
  } catch {
    // The choice then lasts until the page is closed.
  }
}

const view = createView(document);
const historyView = createHistoryView(document);
let unit = load(UNIT_KEY, isPressureUnit, DEFAULT_PRESSURE_UNIT);
let state = initialState;
let history = { ...initialHistory, range: load(RANGE_KEY, isRange, DEFAULT_RANGE) };
let dayStats = null;
let account = initialAccount;
let failures = 0;
let historyRequest = null; // the AbortController of the request for the history that is under way

const visible = () => document.visibilityState === 'visible';

function renderBoard() {
  view.render(describe(state, performance.now()), unit);
  view.renderDayRange(dayStats, unit);
}

function renderAccount() {
  view.renderAccount(describeAccount(account));
}

function renderHistory() {
  const description = describeHistory(history, performance.now());
  historyView.render(description, history.range, unit);
  historyView.renderStatus(description);
}

// The latest reading: every 15 seconds, and later when the server is struggling.
const latestLoop = createLoop(
  async () => {
    let failure = null;
    try {
      state = onLatest(state, await getLatest(), performance.now());
      failures = 0;
    } catch (err) {
      failure = err;
      failures += 1;
      state = onFailure(state, err, performance.now());
    }
    renderBoard();
    return nextDelay(failure, failures);
  },
  { visible, staleAfterMs: MIN_GAP_MS },
);

// The history of the picked range. A request whose range is no longer the picked one is given up.
const historyLoop = createLoop(
  async () => {
    historyRequest?.abort();
    const controller = new AbortController();
    historyRequest = controller;
    const range = history.range;
    let failure = null;
    try {
      history = onHistory(history, range, await getReadings(range, { signal: controller.signal }));
    } catch (err) {
      if (controller !== historyRequest) return 0; // given up on purpose, the next round is already under way
      failure = err;
      history = onHistoryFailure(history, range, err, performance.now());
    }
    if (controller !== historyRequest) return 0;
    historyRequest = null;
    renderHistory();
    return historyDelay(range, failure);
  },
  { visible, staleAfterMs: () => RANGES[history.range].refreshMs },
);

// The lowest and the highest value of the last day, under the numbers of the board. They are an extra: when they
// cannot be loaded the board simply has no second line.
const statsLoop = createLoop(
  async () => {
    try {
      dayStats = await getStats(DAY_RANGE);
      renderBoard();
      return statsDelay(null);
    } catch (err) {
      return statsDelay(err);
    }
  },
  { visible, staleAfterMs: statsDelay(null) },
);

// Who is looking: the role is shown in the corner. The session of the owner ends by itself after a day, and a page that
// stays open (an installed app does) would go on saying "Signed in", so it asks again now and then. Not knowing the role
// is no reason to fail: the corner stays empty.
const accountLoop = createLoop(
  async () => {
    try {
      account = onMe(account, await getMe());
      renderAccount();
      return accountDelay(null);
    } catch (err) {
      return accountDelay(err);
    }
  },
  { visible, staleAfterMs: ME_REFRESH_MS },
);

const loops = [latestLoop, historyLoop, statsLoop, accountLoop];

// The files of the app are kept on the device by the service worker, which is how an installed app opens without a
// network and how a new build reaches it.
const shell = startShell(navigator.serviceWorker, {
  now: () => performance.now(),
  reload: () => location.reload(),
  whenLoaded: (run) => {
    if (document.readyState === 'complete') run();
    else window.addEventListener('load', run, { once: true });
  },
});

view.onUnitPicked((picked) => {
  if (!isPressureUnit(picked) || picked === unit) return;
  unit = picked;
  save(UNIT_KEY, unit);
  renderBoard();
  renderHistory();
});

historyView.onRangePicked((picked) => {
  if (!isRange(picked) || picked === history.range) return;
  history = pickRange(history, picked);
  save(RANGE_KEY, picked);
  renderHistory();
  historyLoop.restart();
});

historyView.onResize(renderHistory);

view.onSignOut(async () => {
  if (account.signingOut) return; // the button only looks pressed while the server is being asked
  account = onSignOutStarted(account);
  renderAccount();
  let done = false;
  try {
    await signOut();
    done = true;
  } catch {
    // The session is still alive on the server: the sentence says so, and the button works again.
  }
  account = done ? onSignedOut(account) : onSignOutFailed(account);
  renderAccount();
  if (done) accountLoop.restart(); // ask what the server says now
});

view.onAlertDismissed(() => {
  account = withNotice(account, null);
  renderAccount();
});

// A hidden tab asks for nothing: the dashboard is not being read, and every request costs the server something.
document.addEventListener('visibilitychange', () => {
  if (!visible()) {
    for (const loop of loops) loop.pause();
    return;
  }
  shell.comeBack();
  renderBoard();
  renderHistory();
  for (const loop of loops) loop.resume();
});

// The connection is back (a phone coming out of a tunnel): ask at once, instead of waiting out the pause that followed
// the failures.
window.addEventListener('online', () => {
  if (visible()) for (const loop of loops) loop.start();
});

// The age of the reading grows by the second, so the sentences about it are refreshed by the second.
setInterval(() => {
  if (!visible()) return;
  renderBoard();
  historyView.renderStatus(describeHistory(history, performance.now()));
}, 1000);

// A sign-in that failed sends the browser home as /?signin=<reason>. The sentence is shown once, and the reason is taken
// off the address, so that a reload or a link that is passed on does not repeat it. (The name "history" is the state of
// the charts in this file, hence window.history.)
const signIn = readSignIn(location.search);
if (signIn.present) {
  account = withNotice(account, signIn.message);
  try {
    window.history.replaceState(null, '', location.pathname + withoutSignIn(location.search) + location.hash);
  } catch {
    // The address keeps the reason. The sentence is on the page all the same.
  }
}

renderBoard();
renderHistory();
renderAccount();
for (const loop of loops) loop.start();
