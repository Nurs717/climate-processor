import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

import {
  bandPath,
  layoutChart,
  linePath,
  linearScale,
  niceStep,
  splitSegments,
  timeTicks,
  valueDomain,
  valueTicks,
  windowIndexAt,
} from '../static/chart.js';

const near = (actual, expected, message) => assert.ok(Math.abs(actual - expected) < 1e-9, `${message ?? ''} ${actual} != ${expected}`);

const MIN = 60_000;
const HOUR = 3_600_000;
const DAY = 86_400_000;

describe('linearScale', () => {
  it('maps the domain onto the range', () => {
    const scale = linearScale(0, 10, 0, 100);
    assert.equal(scale(0), 0);
    assert.equal(scale(5), 50);
    assert.equal(scale(10), 100);
  });

  it('can turn the range upside down, as the value axis of a chart is', () => {
    const scale = linearScale(20, 30, 100, 0);
    assert.equal(scale(20), 100);
    assert.equal(scale(30), 0);
    assert.equal(scale(25), 50);
  });

  it('puts everything in the middle when the domain is a single value', () => {
    assert.equal(linearScale(7, 7, 0, 100)(7), 50);
    assert.equal(linearScale(7, 7, 0, 100)(1000), 50);
  });
});

describe('niceStep', () => {
  it('chooses 1, 2, 5 or 10 times a power of ten', () => {
    assert.equal(niceStep(10, 5), 2);
    assert.equal(niceStep(100, 3), 50);
    assert.equal(niceStep(7, 4), 2);
    assert.equal(niceStep(4, 4), 1);
    assert.equal(niceStep(1, 3), 0.5);
    assert.equal(niceStep(1000, 2), 500);
    assert.equal(niceStep(9, 1), 10);
  });

  it('rounds down at the thresholds 1.5, 3 and 7', () => {
    assert.equal(niceStep(3, 2), 1);
    assert.equal(niceStep(6, 2), 2);
    assert.equal(niceStep(14, 2), 5);
    assert.equal(niceStep(15, 2), 10);
  });

  it('copes with a span that is not positive', () => {
    assert.equal(niceStep(0, 3), 1);
    assert.equal(niceStep(-5, 3), 1);
    assert.equal(niceStep(10, 0), 1);
    assert.equal(niceStep(NaN, 3), 1);
  });
});

describe('valueTicks', () => {
  it('returns round values inside the interval', () => {
    assert.deepEqual(valueTicks(21.6, 23.4, 3), [22, 22.5, 23]);
    assert.deepEqual(valueTicks(0, 100, 3), [0, 50, 100]);
    assert.deepEqual(valueTicks(-3, 3, 3), [-2, 0, 2]);
  });

  it('has no rounding dust in the values', () => {
    assert.deepEqual(valueTicks(0.05, 0.45, 4), [0.1, 0.2, 0.3, 0.4]);
  });
});

describe('valueDomain', () => {
  it('leaves some air around the data', () => {
    const [lo, hi] = valueDomain(20, 30);
    near(lo, 19.2);
    near(hi, 30.8);
  });

  it('is never thinner than the minimum span: a room that does not change must not look like a storm', () => {
    const [lo, hi] = valueDomain(22, 22.2, { minSpan: 1 });
    near(lo, 22.1 - 0.5 * 1.16);
    near(hi, 22.1 + 0.5 * 1.16);
  });

  it('keeps a wide span as it is', () => {
    const [lo, hi] = valueDomain(10, 20, { minSpan: 1, padding: 0 });
    assert.deepEqual([lo, hi], [10, 20]);
  });

  it('copes with a single value', () => {
    const [lo, hi] = valueDomain(22, 22, { minSpan: 2, padding: 0 });
    assert.deepEqual([lo, hi], [21, 23]);
  });
});

describe('timeTicks', () => {
  const ASTANA = 300;

  it('puts the ticks on whole hours of the local clock', () => {
    const from = Date.UTC(2026, 10, 27, 3, 0); // 08:00 in Astana
    const { step, ticks } = timeTicks(from, from + DAY, ASTANA, 8);
    assert.equal(step, 6 * HOUR);
    // 12:00, 18:00, 00:00 and 06:00 in Astana
    assert.deepEqual(ticks, [Date.UTC(2026, 10, 27, 7), Date.UTC(2026, 10, 27, 13), Date.UTC(2026, 10, 27, 19), Date.UTC(2026, 10, 28, 1)]);
  });

  it('puts the ticks of a long range on midnights of the local clock', () => {
    const from = Date.UTC(2026, 10, 20, 3, 0);
    const { step, ticks } = timeTicks(from, from + 7 * DAY, ASTANA, 8);
    assert.equal(step, DAY);
    assert.ok(ticks.length >= 6);
    for (const t of ticks) assert.equal((t + ASTANA * MIN) % DAY, 0, new Date(t).toISOString());
  });

  it('works west of Greenwich too', () => {
    const from = Date.UTC(2026, 10, 27, 3, 0);
    const { ticks } = timeTicks(from, from + DAY, -300, 8);
    for (const t of ticks) assert.equal((t - 300 * MIN) % (6 * HOUR), 0, new Date(t).toISOString());
  });

  it('includes a moment that is exactly the start or the end of the range', () => {
    const from = Date.UTC(2026, 10, 27, 0, 0);
    const { step, ticks } = timeTicks(from, from + 6 * HOUR, 0, 8);
    assert.equal(step, HOUR);
    assert.equal(ticks[0], from);
    assert.equal(ticks[ticks.length - 1], from + 6 * HOUR);
    assert.equal(ticks.length, 7);
  });

  it('keeps every tick inside the range and has fewer than the maximum', () => {
    for (const span of [6 * HOUR, DAY, 7 * DAY, 30 * DAY]) {
      for (const maxTicks of [2, 4, 8]) {
        const from = Date.UTC(2026, 10, 1, 1, 17);
        const { ticks } = timeTicks(from, from + span, ASTANA, maxTicks);
        assert.ok(ticks.length <= maxTicks, `${span} ${maxTicks}: ${ticks.length}`);
        for (const t of ticks) assert.ok(t >= from && t <= from + span);
        for (let i = 1; i < ticks.length; i += 1) assert.ok(ticks[i] > ticks[i - 1]);
      }
    }
  });

  it('chooses a coarser step when there is less room', () => {
    const from = Date.UTC(2026, 10, 1);
    const wide = timeTicks(from, from + DAY, 0, 12).step;
    const narrow = timeTicks(from, from + DAY, 0, 3).step;
    assert.ok(narrow > wide);
  });

  it('does not run out of steps for a very long range', () => {
    const from = Date.UTC(2026, 0, 1);
    const { step } = timeTicks(from, from + 400 * DAY, 0, 2);
    assert.equal(step, 30 * DAY);
  });
});

describe('windowIndexAt', () => {
  const times = [0, 10, 20, 50];

  it('finds the window that contains the moment', () => {
    assert.equal(windowIndexAt(times, 0, 10), 0);
    assert.equal(windowIndexAt(times, 9.9, 10), 0);
    assert.equal(windowIndexAt(times, 10, 10), 1);
    assert.equal(windowIndexAt(times, 29.9, 10), 2);
    assert.equal(windowIndexAt(times, 55, 10), 3);
  });

  it('says -1 for a gap, before the first window and after the last', () => {
    assert.equal(windowIndexAt(times, 30, 10), -1);
    assert.equal(windowIndexAt(times, 49.9, 10), -1);
    assert.equal(windowIndexAt(times, -1, 10), -1);
    assert.equal(windowIndexAt(times, 60, 10), -1);
  });

  it('says -1 when there are no windows', () => {
    assert.equal(windowIndexAt([], 5, 10), -1);
  });
});

describe('splitSegments', () => {
  const at = (...ts) => ts.map((t) => ({ t }));

  it('cuts the points where they are further apart than the gap', () => {
    assert.deepEqual(splitSegments(at(0, 1, 2, 10, 11), 3), [at(0, 1, 2), at(10, 11)]);
  });

  it('keeps a gap of exactly the maximum inside one segment', () => {
    assert.deepEqual(splitSegments(at(0, 3, 6), 3), [at(0, 3, 6)]);
  });

  it('gives a single point its own segment', () => {
    assert.deepEqual(splitSegments(at(0, 10, 20), 3), [at(0), at(10), at(20)]);
  });

  it('gives nothing for nothing', () => {
    assert.deepEqual(splitSegments([], 3), []);
  });
});

describe('linePath and bandPath', () => {
  const segment = [
    { t: 0, avg: 1, min: 0, max: 2 },
    { t: 10, avg: 3, min: 2, max: 4 },
  ];
  const id = (value) => value;

  it('draws the line through the averages', () => {
    assert.equal(linePath(segment, id, id), 'M0 1L10 3');
  });

  it('draws the band along the highest values, back along the lowest, and closes it', () => {
    assert.equal(bandPath(segment, id, id), 'M0 2L10 4L10 2L0 0Z');
  });

  it('rounds the coordinates to a tenth: the path of a month is not a megabyte', () => {
    assert.equal(linePath([{ t: 1.23456, avg: 9.87654 }], id, id), 'M1.2 9.9');
  });
});

describe('layoutChart', () => {
  const base = { from: 0, to: 100_000, stepMs: 10_000, width: 200, height: 100 };
  const point = (t, avg, min = avg, max = avg) => ({ t, avg, min, max });

  it('has nothing to draw without points, and still knows where a moment is', () => {
    const chart = layoutChart({ ...base, points: [] });
    assert.equal(chart.empty, true);
    assert.deepEqual([chart.segments, chart.dots, chart.yTicks], [[], [], []]);
    assert.equal(chart.timeAt(100), 50_000);
  });

  it('puts a higher value higher up, inside the plot', () => {
    const chart = layoutChart({ ...base, points: [point(0, 20), point(10_000, 25), point(20_000, 30)], bottom: 6, top: 8 });
    const [y20, y25, y30] = [20, 25, 30].map(chart.y);
    assert.ok(y20 > y25 && y25 > y30);
    assert.ok(y30 >= 8 && y20 <= 94);
    assert.equal(chart.empty, false);
    assert.equal(chart.segments.length, 1);
  });

  it('maps time to the width of the plot, and the other way round', () => {
    const chart = layoutChart({ ...base, points: [point(0, 1), point(10_000, 2)] });
    assert.equal(chart.x(0), 0);
    assert.equal(chart.x(50_000), 100);
    assert.equal(chart.x(100_000), 200);
    assert.equal(chart.timeAt(100), 50_000);
  });

  it('keeps what lies outside the range at the edge: the first window may start before the range does', () => {
    const chart = layoutChart({ ...base, points: [point(-5000, 1), point(5000, 2)] });
    assert.equal(chart.x(-5000), 0);
    assert.equal(chart.x(500_000), 200);
    assert.equal(chart.timeAt(-50), 0);
    assert.equal(chart.timeAt(999), 100_000);
  });

  it('leaves a gap where the readings are missing, and draws a lone window as a dot', () => {
    const chart = layoutChart({
      ...base,
      points: [point(0, 1), point(10_000, 2), point(20_000, 3), point(70_000, 4), point(90_000, 5)],
    });
    assert.equal(chart.segments.length, 1, 'the first three are joined');
    assert.equal(chart.dots.length, 2, 'the two windows after the gap are alone, with another gap between them');
  });

  it('spans the band from the lowest to the highest value of a window', () => {
    const chart = layoutChart({ ...base, points: [point(0, 22, 21, 24), point(10_000, 22, 21, 24)] });
    const [lo, hi] = chart.domain;
    assert.ok(lo < 21 && hi > 24);
    assert.match(chart.segments[0].band, /^M0 .*Z$/);
  });

  it('does not stretch noise: a flat series keeps the minimum span', () => {
    const flat = layoutChart({ ...base, minSpan: 1, points: [point(0, 22.0), point(10_000, 22.1)] });
    const [lo, hi] = flat.domain;
    assert.ok(hi - lo >= 1);
    const spread = Math.abs(flat.y(22.0) - flat.y(22.1));
    assert.ok(spread < 15, `a tenth of a degree moves the line by ${spread} px`);
  });

  it('gives value labels that sit inside the domain, at the height of their value', () => {
    const chart = layoutChart({ ...base, points: [point(0, 20), point(10_000, 30)], yTickCount: 3 });
    assert.ok(chart.yTicks.length >= 2);
    for (const tick of chart.yTicks) {
      assert.ok(tick.value >= chart.domain[0] && tick.value <= chart.domain[1]);
      near(tick.y, chart.y(tick.value));
    }
  });
});
