// The history section: which ranges there are, what the charts and their readouts say, and the state of loading.
// Pure functions: the clock is always passed in.

import { PRESSURE_UNITS, formatDecimal, formatWhole } from './format.js';
import { limitedMessage } from './model.js';

/**
 * The ranges a person can pick. refreshMs is how often a chart asks again: a new window appears no faster than
 * the server's step for the range (a minute for six hours, five minutes for a day, and so on), and every request
 * costs the server something.
 */
export const RANGES = Object.freeze({
  '6h': Object.freeze({ label: '6 h', words: 'last 6 hours', refreshMs: 60_000 }),
  '24h': Object.freeze({ label: '24 h', words: 'last 24 hours', refreshMs: 60_000 }),
  '7d': Object.freeze({ label: '7 d', words: 'last 7 days', refreshMs: 300_000 }),
  '30d': Object.freeze({ label: '30 d', words: 'last 30 days', refreshMs: 300_000 }),
});

export const DEFAULT_RANGE = '24h';

/** @returns {boolean} whether value is the key of a range (and not, say, "constructor") */
export function isRange(value) {
  return typeof value === 'string' && Object.hasOwn(RANGES, value);
}

const identity = (value) => value;

/**
 * What is drawn, one chart each. convert turns what the server sends into the unit that is shown, minSpan is the
 * thinnest value axis in that unit (the smallest change that means something in a room).
 */
export const METRICS = Object.freeze([
  Object.freeze({
    key: 'temperature',
    label: 'Temperature',
    unitLabel: () => '°C',
    convert: () => identity,
    minSpan: () => 1,
    format: formatDecimal,
  }),
  Object.freeze({
    key: 'humidity',
    label: 'Humidity',
    unitLabel: () => '%',
    convert: () => identity,
    minSpan: () => 5,
    format: formatWhole,
  }),
  Object.freeze({
    key: 'pressure',
    label: 'Pressure',
    unitLabel: (unit) => PRESSURE_UNITS[unit].label,
    convert: (unit) => PRESSURE_UNITS[unit].fromPascals,
    minSpan: (unit) => PRESSURE_UNITS[unit].fromPascals(200), // 2 hPa, whatever the unit is
    format: formatDecimal,
  }),
]);

/**
 * The windows of one measurement, in the unit that is shown.
 * @param {{t: number, count: number}[]} points the windows of a /readings answer (see parseReadings)
 */
export function seriesOf(points, metric, unit) {
  const convert = metric.convert(unit);
  return points.map((point) => {
    const stat = point[metric.key];
    return { t: point.t, count: point.count, avg: convert(stat.avg), min: convert(stat.min), max: convert(stat.max) };
  });
}

/**
 * Lowest, average and highest value of a whole series. The average is weighted by the readings in every window:
 * the mean of the means would give a window of two readings the same say as a window of two hundred.
 */
export function summarize(series) {
  if (series.length === 0) return null;
  let min = Infinity;
  let max = -Infinity;
  let sum = 0;
  let count = 0;
  for (const point of series) {
    min = Math.min(min, point.min);
    max = Math.max(max, point.max);
    sum += point.avg * point.count;
    count += point.count;
  }
  const avg = count > 0 ? sum / count : series.reduce((total, point) => total + point.avg, 0) / series.length;
  return { min, avg, max };
}

/** "Low 21.8, avg 22.4, high 23.1 °C": what a chart says when nothing is pointed at. */
export function describeSummary(summary, metric, unit) {
  if (!summary) return '';
  const f = metric.format;
  return `Low ${f(summary.min)}, avg ${f(summary.avg)}, high ${f(summary.max)} ${metric.unitLabel(unit)}`;
}

/** "Nov 27, 14:35: 22.4 °C, low 22.1, high 22.7": what a chart says about the window that is pointed at. */
export function describeWindow(point, metric, unit, moment) {
  const f = metric.format;
  return `${moment}: ${f(point.avg)} ${metric.unitLabel(unit)}, low ${f(point.min)}, high ${f(point.max)}`;
}

/** "24 h range 21.8–23.1" under a number of the board, from the stat of /stats?range=24h (in the sent unit). */
export function describeDayRange(stat, metric, unit) {
  if (!stat) return '';
  const convert = metric.convert(unit);
  return `24 h range ${metric.format(convert(stat.min))}–${metric.format(convert(stat.max))}`;
}

/** How often the numbers under the board (the range of the last 24 hours) are asked for again. */
export const STATS_REFRESH_MS = 300_000;
const STATS_RETRY_MS = 60_000;

/** How long to wait before asking for the 24-hour range again; those numbers are an extra, so they ask quietly. */
export function statsDelay(failure) {
  if (!failure) return STATS_REFRESH_MS;
  return failure.kind === 'limited' ? failure.retryAfterMs || STATS_RETRY_MS : STATS_RETRY_MS;
}

/**
 * @typedef {{range: string, data: object|null, loading: boolean, error: {kind: 'limited'|'unreachable'}|null,
 *            retryAt: number}} HistoryState
 */

/** @type {HistoryState} */
export const initialHistory = Object.freeze({ range: DEFAULT_RANGE, data: null, loading: true, error: null, retryAt: 0 });

/** The person picked a range. What is on screen stays (dimmed) until the new series arrives. */
export function pickRange(state, range) {
  return { ...state, range, loading: true, error: null, retryAt: 0 };
}

/** A series arrived. An answer for a range that is no longer the picked one is forgotten. */
export function onHistory(state, range, data) {
  if (range !== state.range) return state;
  return { ...state, data: { ...data, range }, loading: false, error: null, retryAt: 0 };
}

/** A request failed. The series on screen stays: an old chart with a warning beats an empty one. */
export function onHistoryFailure(state, range, failure, now) {
  if (range !== state.range) return state;
  if (failure.kind === 'limited') {
    return { ...state, loading: false, error: { kind: 'limited' }, retryAt: now + (failure.retryAfterMs ?? 0) };
  }
  return { ...state, loading: false, error: { kind: 'unreachable' }, retryAt: 0 };
}

/** How long to wait before the chart asks again. */
export function historyDelay(range, failure) {
  const base = RANGES[range].refreshMs;
  return failure?.kind === 'limited' ? failure.retryAfterMs || base : base;
}

/**
 * @typedef {object} HistoryDescription
 * @property {'loading'|'ready'|'empty'|'offline'|'limited'} state
 * @property {string} message a sentence for the status line under the range buttons; empty when there is none
 * @property {boolean} dimmed the charts show something old or something of another range
 * @property {object|null} data what to draw (its range is data.range)
 */

/**
 * What the section shows at the moment now.
 * @param {HistoryState} state
 * @returns {HistoryDescription}
 */
export function describeHistory(state, now) {
  const { data, error, loading } = state;
  const shown = data ?? null;
  const old = shown !== null;
  if (error?.kind === 'limited') {
    return { state: 'limited', message: limitedMessage(state, now), dimmed: old, data: shown };
  }
  if (error) {
    // What is on screen may be another range than the picked one: then it is not a refresh that failed.
    const message = !old
      ? "Can't load the history. Retrying…"
      : shown.range === state.range
        ? "Can't refresh the history. Retrying…"
        : `Can't load the ${RANGES[state.range].words}. Retrying…`;
    return { state: 'offline', message, dimmed: old, data: shown };
  }
  if (loading || !shown) {
    return { state: 'loading', message: old ? 'Loading…' : 'Loading the history…', dimmed: old, data: shown };
  }
  if (shown.points.length === 0) {
    return { state: 'empty', message: 'No readings in this range yet.', dimmed: false, data: shown };
  }
  return { state: 'ready', message: '', dimmed: false, data: shown };
}
