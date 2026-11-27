// The arithmetic of a time-series chart: scales, ticks, gaps, paths. Pure functions: no page, no clock.
//
// A point is {t, avg, min, max}: t is the start of a window in milliseconds, the rest are values in the unit that
// is shown. The line follows avg; the band behind it spans min to max, so a window with a spike is not hidden.

const MINUTE = 60_000;
const HOUR = 3_600_000;
const DAY = 86_400_000;

/** Maps [d0, d1] to [r0, r1]. An empty domain maps everything to the middle of the range. */
export function linearScale(d0, d1, r0, r1) {
  const span = d1 - d0;
  if (span === 0) return () => (r0 + r1) / 2;
  return (value) => r0 + ((value - d0) / span) * (r1 - r0);
}

/** A round step (1, 2 or 5 times a power of ten) that fits about count times into span. */
export function niceStep(span, count) {
  if (!(span > 0) || !(count > 0)) return 1;
  const raw = span / count;
  const power = 10 ** Math.floor(Math.log10(raw));
  const f = raw / power;
  return (f <= 1.5 ? 1 : f <= 3 ? 2 : f <= 7 ? 5 : 10) * power;
}

/** Round values inside [lo, hi], about count of them, in ascending order. */
export function valueTicks(lo, hi, count) {
  const step = niceStep(hi - lo, count);
  const ticks = [];
  for (let k = Math.ceil(lo / step); k * step <= hi + step * 1e-9; k += 1) {
    ticks.push(Number((k * step).toPrecision(12)));
  }
  return ticks;
}

/**
 * The values the axis covers: the data with some air around it, and never thinner than minSpan. Without a
 * minimum a room whose temperature does not move would fill the chart with the noise of the sensor.
 */
export function valueDomain(min, max, { minSpan = 0, padding = 0.08 } = {}) {
  let lo = min;
  let hi = max;
  if (hi - lo < minSpan) {
    const middle = (lo + hi) / 2;
    lo = middle - minSpan / 2;
    hi = middle + minSpan / 2;
  }
  const air = (hi - lo) * padding;
  return [lo - air, hi + air];
}

const TICK_STEPS = [
  10 * MINUTE, 15 * MINUTE, 30 * MINUTE, HOUR, 2 * HOUR, 3 * HOUR, 6 * HOUR, 12 * HOUR,
  DAY, 2 * DAY, 5 * DAY, 10 * DAY, 30 * DAY,
];

/**
 * Round moments (whole hours, whole days of the local clock) inside [from, to], fewer than maxTicks of them.
 * @param {number} offsetMinutes the local time minus UTC, in minutes (Astana: 300)
 * @returns {{step: number, ticks: number[]}} step in milliseconds
 */
export function timeTicks(from, to, offsetMinutes, maxTicks) {
  const span = to - from;
  const step = TICK_STEPS.find((candidate) => span / candidate < maxTicks) ?? TICK_STEPS[TICK_STEPS.length - 1];
  const shift = offsetMinutes * MINUTE;
  const ticks = [];
  for (let t = Math.ceil((from + shift) / step) * step - shift; t <= to; t += step) ticks.push(t);
  return { step, ticks };
}

/**
 * The window that contains the moment t, or -1 when no reading covers it (a gap, or outside the range).
 * @param {number[]} times start of every window, ascending
 */
export function windowIndexAt(times, t, stepMs) {
  let lo = 0;
  let hi = times.length - 1;
  let found = -1;
  while (lo <= hi) {
    const middle = (lo + hi) >> 1;
    if (times[middle] <= t) {
      found = middle;
      lo = middle + 1;
    } else {
      hi = middle - 1;
    }
  }
  return found >= 0 && t < times[found] + stepMs ? found : -1;
}

/** Cuts the points where two neighbours are further apart than maxGapMs: a gap in the data stays a gap. */
export function splitSegments(points, maxGapMs) {
  const segments = [];
  let current = [];
  for (const point of points) {
    if (current.length > 0 && point.t - current[current.length - 1].t > maxGapMs) {
      segments.push(current);
      current = [];
    }
    current.push(point);
  }
  if (current.length > 0) segments.push(current);
  return segments;
}

const coordinate = (value) => String(Math.round(value * 10) / 10);

/** The line through the averages of a segment, as the d attribute of an SVG path. */
export function linePath(segment, x, y) {
  return segment.map((p, i) => `${i === 0 ? 'M' : 'L'}${coordinate(x(p.t))} ${coordinate(y(p.avg))}`).join('');
}

/** The band between the lowest and the highest value of every window: along max, back along min, closed. */
export function bandPath(segment, x, y) {
  const upper = segment.map((p, i) => `${i === 0 ? 'M' : 'L'}${coordinate(x(p.t))} ${coordinate(y(p.max))}`).join('');
  const lower = [...segment].reverse().map((p) => `L${coordinate(x(p.t))} ${coordinate(y(p.min))}`).join('');
  return `${upper}${lower}Z`;
}

/**
 * Everything one chart draws, as numbers and strings.
 * @param {object} o
 * @param {{t: number, avg: number, min: number, max: number}[]} o.points ascending by t
 * @param {number} o.from start of the range, in milliseconds
 * @param {number} o.to end of the range
 * @param {number} o.stepMs length of a window
 * @param {number} o.width width of the plot in pixels
 * @param {number} o.height height of the plot in pixels
 * @param {number} [o.minSpan] the thinnest value axis, in the unit that is shown
 * @param {number} [o.yTickCount] about how many value labels
 */
export function layoutChart({ points, from, to, stepMs, width, height, minSpan = 0, top = 8, bottom = 6, yTickCount = 3 }) {
  const scaleX = linearScale(from, to, 0, width);
  // The first window may start before the range does: it is drawn at the edge, not outside the plot.
  const x = (t) => scaleX(Math.min(Math.max(t, from), to));
  const timeAt = (px) => from + (Math.min(Math.max(px, 0), width) / width) * (to - from);

  if (points.length === 0) {
    return { empty: true, x, y: () => height / 2, timeAt, domain: null, yTicks: [], segments: [], dots: [] };
  }

  let min = Infinity;
  let max = -Infinity;
  for (const point of points) {
    min = Math.min(min, point.min);
    max = Math.max(max, point.max);
  }
  const domain = valueDomain(min, max, { minSpan });
  const y = linearScale(domain[0], domain[1], height - bottom, top);

  const segments = [];
  const dots = [];
  for (const segment of splitSegments(points, stepMs * 1.5)) {
    if (segment.length === 1) {
      dots.push({ x: x(segment[0].t), y: y(segment[0].avg) });
    } else {
      segments.push({ line: linePath(segment, x, y), band: bandPath(segment, x, y) });
    }
  }

  const yTicks = valueTicks(domain[0], domain[1], yTickCount).map((value) => ({ value, y: y(value) }));
  return { empty: false, x, y, timeAt, domain, yTicks, segments, dots };
}
