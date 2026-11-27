// What the page knows and what it says about it. Pure functions: the clock is always passed in.
//
// The state holds the last reading and the last failure. describe() turns it into what the page shows right now,
// so the same state reads differently as time passes (the age of the reading grows by itself).

import { formatAgo, formatDuration } from './format.js';

export const POLL_MS = 15_000;
const MAX_BACKOFF_MS = 60_000;

/**
 * @typedef {{temperature: number, humidity: number, pressure: number, ageSeconds: number, stale: boolean}} Reading
 * @typedef {{kind: 'empty'|'limited'|'unreachable'}} Problem
 * @typedef {{latest: Reading|null, receivedAt: number, error: Problem|null, retryAt: number}} State
 */

/** @type {State} */
export const initialState = Object.freeze({ latest: null, receivedAt: 0, error: null, retryAt: 0 });

/**
 * A new reading arrived.
 * @param {State} _state
 * @param {Reading} latest
 * @param {number} now milliseconds on a clock that only moves forward
 * @returns {State}
 */
export function onLatest(_state, latest, now) {
  return { latest, receivedAt: now, error: null, retryAt: 0 };
}

/**
 * A request failed. The last reading stays: an old value with a warning beats an empty page.
 * @param {State} state
 * @param {{kind: string, retryAfterMs?: number}} failure an ApiError
 * @param {number} now
 * @returns {State}
 */
export function onFailure(state, failure, now) {
  switch (failure.kind) {
    case 'empty':
      return { latest: null, receivedAt: 0, error: { kind: 'empty' }, retryAt: 0 };
    case 'limited':
      return { ...state, error: { kind: 'limited' }, retryAt: now + (failure.retryAfterMs ?? 0) };
    default:
      return { ...state, error: { kind: 'unreachable' }, retryAt: 0 };
  }
}

/**
 * How long to wait before the next request.
 * @param {{kind: string, retryAfterMs?: number}|null} failure the failure of the last request, if it failed
 * @param {number} failures how many requests in a row have failed, this one included
 */
export function nextDelay(failure, failures) {
  if (!failure || failure.kind === 'empty') return POLL_MS;
  if (failure.kind === 'limited') return failure.retryAfterMs || POLL_MS;
  return Math.min(POLL_MS * 2 ** Math.max(0, failures - 1), MAX_BACKOFF_MS);
}

/**
 * @typedef {object} Description
 * @property {'loading'|'live'|'stale'|'empty'|'offline'|'limited'} state
 * @property {string} message a sentence for the status line
 * @property {boolean} dimmed the numbers are old or cannot be refreshed
 * @property {{temperature: number, humidity: number, pressure: number}|null} values
 */

/**
 * What to show at the moment now.
 * @param {State} state
 * @param {number} now
 * @returns {Description}
 */
export function describe(state, now) {
  const { latest, error } = state;
  if (!latest) return describeWithoutReading(error, state, now);

  const age = latest.ageSeconds + Math.max(0, now - state.receivedAt) / 1000;
  const values = { temperature: latest.temperature, humidity: latest.humidity, pressure: latest.pressure };
  if (error?.kind === 'limited') {
    return { state: 'limited', message: limitedMessage(state, now), dimmed: true, values };
  }
  if (error) {
    return {
      state: 'offline',
      message: `Can't reach the server. Last reading ${formatAgo(age)}. Retrying…`,
      dimmed: true,
      values,
    };
  }
  if (latest.stale) {
    return {
      state: 'stale',
      message: `Last reading ${formatAgo(age)}. The sensor may be offline.`,
      dimmed: true,
      values,
    };
  }
  return { state: 'live', message: `Live, updated ${formatAgo(age)}`, dimmed: false, values };
}

function describeWithoutReading(error, state, now) {
  switch (error?.kind) {
    case undefined:
      return { state: 'loading', message: 'Connecting to the sensor…', dimmed: false, values: null };
    case 'empty':
      return { state: 'empty', message: 'No readings yet. Waiting for the sensor to report.', dimmed: false, values: null };
    case 'limited':
      return { state: 'limited', message: limitedMessage(state, now), dimmed: false, values: null };
    default:
      return { state: 'offline', message: "Can't reach the server. Retrying…", dimmed: false, values: null };
  }
}

/** The sentence for a 429: how long is left of the wait the server asked for. */
export function limitedMessage(state, now) {
  const wait = Math.ceil((state.retryAt - now) / 1000);
  return wait > 0
    ? `Too many requests. Trying again in ${formatDuration(wait)}.`
    : 'Too many requests. Trying again…';
}
