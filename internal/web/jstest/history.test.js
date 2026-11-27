import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

import {
  DEFAULT_RANGE,
  METRICS,
  RANGES,
  STATS_REFRESH_MS,
  describeDayRange,
  describeHistory,
  describeSummary,
  describeWindow,
  historyDelay,
  initialHistory,
  isRange,
  onHistory,
  onHistoryFailure,
  pickRange,
  seriesOf,
  statsDelay,
  summarize,
} from '../static/history.js';

const [temperature, humidity, pressure] = METRICS;

const window = (t, count, temp, hum, press) => ({ t, count, temperature: temp, humidity: hum, pressure: press });
const stat = (avg, min = avg, max = avg) => ({ avg, min, max });
const data = (points = [window(0, 10, stat(22), stat(40), stat(101325))]) => ({ from: 0, to: 60_000, stepMs: 60_000, points });

describe('the ranges', () => {
  it('are the four the page offers, with the default among them', () => {
    assert.deepEqual(Object.keys(RANGES), ['6h', '24h', '7d', '30d']);
    assert.ok(isRange(DEFAULT_RANGE));
    assert.equal(initialHistory.range, DEFAULT_RANGE);
  });

  it('refresh no faster than the server makes a new window', () => {
    for (const range of Object.values(RANGES)) assert.ok(range.refreshMs >= 60_000);
    assert.ok(RANGES['7d'].refreshMs > RANGES['24h'].refreshMs);
  });

  it('know a range from anything else, "constructor" included', () => {
    assert.equal(isRange('7d'), true);
    for (const bad of ['1h', '', null, undefined, 7, 'constructor', '__proto__', 'toString', '24H']) {
      assert.equal(isRange(bad), false, String(bad));
    }
  });
});

describe('METRICS', () => {
  it('are temperature, humidity and pressure, in the order of the board', () => {
    assert.deepEqual(METRICS.map((m) => m.key), ['temperature', 'humidity', 'pressure']);
  });

  it('name their unit, the pressure by the unit that is shown', () => {
    assert.equal(temperature.unitLabel('hpa'), '°C');
    assert.equal(humidity.unitLabel('hpa'), '%');
    assert.equal(pressure.unitLabel('hpa'), 'hPa');
    assert.equal(pressure.unitLabel('mmhg'), 'mm Hg');
  });

  it('call two hectopascals the thinnest pressure axis, whatever the unit is', () => {
    assert.equal(pressure.minSpan('hpa'), 2);
    assert.ok(Math.abs(pressure.minSpan('mmhg') - 1.5) < 0.001);
    assert.equal(temperature.minSpan('hpa'), 1);
    assert.equal(humidity.minSpan('hpa'), 5);
  });

  it('format the temperature with a decimal and the humidity as a whole number', () => {
    assert.equal(temperature.format(22.44), '22.4');
    assert.equal(humidity.format(42.4), '42');
    assert.equal(pressure.format(760), '760.0');
  });
});

describe('seriesOf', () => {
  const points = [window(1000, 3, stat(22, 21, 23), stat(40, 38, 44), stat(101325, 101300, 101400))];

  it('takes one measurement of every window', () => {
    assert.deepEqual(seriesOf(points, temperature, 'mmhg'), [{ t: 1000, count: 3, avg: 22, min: 21, max: 23 }]);
    assert.deepEqual(seriesOf(points, humidity, 'mmhg'), [{ t: 1000, count: 3, avg: 40, min: 38, max: 44 }]);
  });

  it('converts the pressure to the unit that is shown, all three values', () => {
    const [hpa] = seriesOf(points, pressure, 'hpa');
    assert.deepEqual([hpa.avg, hpa.min, hpa.max], [1013.25, 1013, 1014]);
    const [mm] = seriesOf(points, pressure, 'mmhg');
    assert.ok(Math.abs(mm.avg - 760) < 0.001);
    assert.ok(mm.min < mm.avg && mm.avg < mm.max);
  });

  it('has nothing for nothing', () => {
    assert.deepEqual(seriesOf([], temperature, 'hpa'), []);
  });
});

describe('summarize', () => {
  it('finds the lowest, the average and the highest', () => {
    const series = [
      { t: 0, count: 1, avg: 20, min: 19, max: 21 },
      { t: 1, count: 1, avg: 24, min: 22, max: 27 },
    ];
    assert.deepEqual(summarize(series), { min: 19, avg: 22, max: 27 });
  });

  it('weighs the average by the readings of every window: two readings do not weigh as much as two hundred', () => {
    const series = [
      { t: 0, count: 2, avg: 30, min: 30, max: 30 },
      { t: 1, count: 198, avg: 20, min: 20, max: 20 },
    ];
    assert.equal(summarize(series).avg, 20.1);
  });

  it('falls back to the plain mean when the counts are useless', () => {
    const series = [
      { t: 0, count: 0, avg: 10, min: 10, max: 10 },
      { t: 1, count: 0, avg: 20, min: 20, max: 20 },
    ];
    assert.equal(summarize(series).avg, 15);
  });

  it('says null for nothing', () => {
    assert.equal(summarize([]), null);
  });
});

describe('what the charts say', () => {
  it('describes a series by its lowest, average and highest value', () => {
    assert.equal(describeSummary({ min: 21.84, avg: 22.4, max: 23.06 }, temperature, 'mmhg'), 'Low 21.8, avg 22.4, high 23.1 °C');
    assert.equal(describeSummary({ min: 36, avg: 41.6, max: 48 }, humidity, 'mmhg'), 'Low 36, avg 42, high 48 %');
    assert.equal(describeSummary({ min: 1009, avg: 1013.25, max: 1017 }, pressure, 'hpa'), 'Low 1009.0, avg 1013.3, high 1017.0 hPa');
    assert.equal(describeSummary(null, temperature, 'hpa'), '');
  });

  it('describes the window that is pointed at', () => {
    const point = { t: 0, count: 5, avg: 22.44, min: 22.1, max: 22.74 };
    assert.equal(describeWindow(point, temperature, 'mmhg', 'Nov 27, 14:35'), 'Nov 27, 14:35: 22.4 °C, low 22.1, high 22.7');
  });

  it('describes the range of the last day in the unit that is shown', () => {
    const stats = stat(101325, 100900, 101700);
    assert.equal(describeDayRange(stat(22.4, 21.84, 23.06), temperature, 'mmhg'), '24 h range 21.8–23.1');
    assert.equal(describeDayRange(stats, pressure, 'hpa'), '24 h range 1009.0–1017.0');
    assert.equal(describeDayRange(null, temperature, 'hpa'), '');
  });
});

describe('the state of the history', () => {
  const now = 100_000;
  const loaded = onHistory(initialHistory, '24h', data());

  it('starts loading, with nothing to show', () => {
    assert.equal(initialHistory.loading, true);
    assert.equal(initialHistory.data, null);
    assert.deepEqual(describeHistory(initialHistory, now), { state: 'loading', message: 'Loading the history…', dimmed: false, data: null });
  });

  it('shows a series once it has arrived, and says nothing about it', () => {
    const description = describeHistory(loaded, now);
    assert.equal(description.state, 'ready');
    assert.equal(description.message, '');
    assert.equal(description.dimmed, false);
    assert.equal(description.data.range, '24h');
    assert.equal(description.data.points.length, 1);
  });

  it('forgets an answer for a range that is no longer the picked one', () => {
    const picked = pickRange(loaded, '7d');
    assert.equal(onHistory(picked, '24h', data()), picked);
    assert.equal(onHistoryFailure(picked, '24h', { kind: 'network' }, now), picked);
  });

  it('keeps what is on screen, dimmed, while another range loads', () => {
    const picked = pickRange(loaded, '7d');
    const description = describeHistory(picked, now);
    assert.equal(picked.range, '7d');
    assert.equal(description.state, 'loading');
    assert.equal(description.message, 'Loading…');
    assert.equal(description.dimmed, true);
    assert.equal(description.data.range, '24h');
    assert.equal(describeHistory(onHistory(picked, '7d', data()), now).data.range, '7d');
  });

  it('forgets an old error when another range is picked', () => {
    const failed = onHistoryFailure(loaded, '24h', { kind: 'network' }, now);
    assert.equal(describeHistory(pickRange(failed, '6h'), now).state, 'loading');
  });

  it('says so when there are no readings in the range', () => {
    const description = describeHistory(onHistory(initialHistory, '24h', data([])), now);
    assert.equal(description.state, 'empty');
    assert.equal(description.message, 'No readings in this range yet.');
    assert.equal(description.dimmed, false);
  });

  it('keeps the old series, dimmed, when a refresh fails', () => {
    const description = describeHistory(onHistoryFailure(loaded, '24h', { kind: 'network' }, now), now);
    assert.equal(description.state, 'offline');
    assert.equal(description.message, "Can't refresh the history. Retrying…");
    assert.equal(description.dimmed, true);
    assert.equal(description.data, loaded.data);
  });

  it('names the range that cannot be loaded when the series on screen belongs to another one', () => {
    const picked = pickRange(loaded, '7d');
    const description = describeHistory(onHistoryFailure(picked, '7d', { kind: 'server' }, now), now);
    assert.equal(description.state, 'offline');
    assert.equal(description.message, "Can't load the last 7 days. Retrying…");
    assert.equal(description.dimmed, true);
    assert.equal(description.data.range, '24h');
  });

  it('says it cannot load when there is no series to keep', () => {
    const description = describeHistory(onHistoryFailure(initialHistory, '24h', { kind: 'server' }, now), now);
    assert.equal(description.state, 'offline');
    assert.equal(description.message, "Can't load the history. Retrying…");
    assert.equal(description.dimmed, false);
    assert.equal(description.data, null);
  });

  it('counts down the wait the server asked for', () => {
    const limited = onHistoryFailure(loaded, '24h', { kind: 'limited', retryAfterMs: 20_000 }, now);
    assert.equal(describeHistory(limited, now).message, 'Too many requests. Trying again in 20 s.');
    assert.equal(describeHistory(limited, now + 12_000).message, 'Too many requests. Trying again in 8 s.');
    assert.equal(describeHistory(limited, now + 20_000).message, 'Too many requests. Trying again…');
    assert.equal(describeHistory(limited, now).state, 'limited');
    assert.equal(describeHistory(limited, now).dimmed, true);
  });

  it('recovers when the next answer arrives', () => {
    const failed = onHistoryFailure(loaded, '24h', { kind: 'limited', retryAfterMs: 20_000 }, now);
    const recovered = onHistory(failed, '24h', data());
    assert.equal(describeHistory(recovered, now).state, 'ready');
    assert.equal(recovered.error, null);
    assert.equal(recovered.retryAt, 0);
  });

  it('does not change the state it is given', () => {
    const before = JSON.stringify(loaded);
    pickRange(loaded, '7d');
    onHistoryFailure(loaded, '24h', { kind: 'network' }, now);
    onHistory(loaded, '24h', data());
    assert.equal(JSON.stringify(loaded), before);
    assert.throws(() => {
      'use strict';
      initialHistory.range = '7d';
    }, TypeError);
  });
});

describe('historyDelay', () => {
  it('is the refresh time of the range when all is well, or when the server cannot be reached', () => {
    assert.equal(historyDelay('24h', null), 60_000);
    assert.equal(historyDelay('7d', null), 300_000);
    assert.equal(historyDelay('6h', { kind: 'network' }), 60_000);
  });

  it('is the wait the server asked for after a 429', () => {
    assert.equal(historyDelay('7d', { kind: 'limited', retryAfterMs: 20_000 }), 20_000);
    assert.equal(historyDelay('7d', { kind: 'limited', retryAfterMs: 0 }), 300_000);
  });
});

describe('statsDelay', () => {
  it('asks every five minutes, and a minute later after a failure', () => {
    assert.equal(STATS_REFRESH_MS, 300_000);
    assert.equal(statsDelay(null), 300_000);
    assert.equal(statsDelay({ kind: 'network' }), 60_000);
    assert.equal(statsDelay({ kind: 'server' }), 60_000);
  });

  it('waits what the server asked for after a 429', () => {
    assert.equal(statsDelay({ kind: 'limited', retryAfterMs: 45_000 }), 45_000);
    assert.equal(statsDelay({ kind: 'limited' }), 60_000);
  });
});
