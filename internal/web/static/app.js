// Entry point: polls the latest reading, keeps the page up to date and remembers the pressure unit.

import { getLatest, getMe } from './api.js';
import { DEFAULT_PRESSURE_UNIT, isPressureUnit } from './format.js';
import { describe, initialState, nextDelay, onFailure, onLatest } from './model.js';
import { createView } from './view.js';

const UNIT_KEY = 'climate.pressureUnit';
const MIN_GAP_MS = 5000; // a tab that comes back to the front does not ask again within this time

// The storage can be missing or throw (private windows, blocked site data): the page works without it.
function loadUnit() {
  try {
    const stored = localStorage.getItem(UNIT_KEY);
    return isPressureUnit(stored) ? stored : DEFAULT_PRESSURE_UNIT;
  } catch {
    return DEFAULT_PRESSURE_UNIT;
  }
}

function saveUnit(unit) {
  try {
    localStorage.setItem(UNIT_KEY, unit);
  } catch {
    // The choice then lasts until the page is closed.
  }
}

const view = createView(document);
let state = initialState;
let unit = loadUnit();
let failures = 0;
let delay = 0;
let lastAttempt = -Infinity;
let timer = 0;
let busy = false;

const visible = () => document.visibilityState === 'visible';
const render = () => view.render(describe(state, performance.now()), unit);

function schedule(ms) {
  clearTimeout(timer);
  if (visible()) timer = setTimeout(poll, ms);
}

async function poll() {
  if (busy) return;
  busy = true;
  clearTimeout(timer);
  lastAttempt = performance.now();
  let failure = null;
  try {
    state = onLatest(state, await getLatest(), performance.now());
    failures = 0;
  } catch (err) {
    failure = err;
    failures += 1;
    state = onFailure(state, err, performance.now());
  } finally {
    busy = false;
  }
  delay = nextDelay(failure, failures);
  render();
  schedule(delay);
}

view.onUnitPicked((picked) => {
  if (!isPressureUnit(picked) || picked === unit) return;
  unit = picked;
  saveUnit(unit);
  render();
});

// A hidden tab asks for nothing: the dashboard is not being read, and every request costs the server something.
document.addEventListener('visibilitychange', () => {
  if (!visible()) {
    clearTimeout(timer);
    return;
  }
  render();
  if (performance.now() - lastAttempt >= MIN_GAP_MS) poll();
  else schedule(delay);
});

// The age of the reading grows by the second, so the sentence about it is refreshed by the second.
setInterval(() => {
  if (visible()) render();
}, 1000);

// Who is looking: the role is shown in the corner. Not knowing it is no reason to fail.
getMe()
  .then((me) => view.showRole(me))
  .catch(() => {});

render();
poll();
