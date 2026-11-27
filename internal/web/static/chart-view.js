// The history section on the page: range buttons, three charts, what they say and a table of the data.
// It draws what history.js describes and chart.js lays out. Text goes in with textContent and shapes with
// setAttribute, never as markup (the policy of the page forbids inline styles and scripts).

import { layoutChart, timeTicks, windowIndexAt } from './chart.js';
import { formatDuration, timeFormat } from './format.js';
import { METRICS, RANGES, describeSummary, describeWindow, seriesOf, summarize } from './history.js';

const SVG_NS = 'http://www.w3.org/2000/svg';
const DAY_MS = 86_400_000;
const AXIS_HEIGHT = 20; // room for the time labels under the last chart
const MIN_TICK_GAP = 60; // pixels between two time labels (a label is about 40 wide)
const NARROW = 520; // below this width the charts are a little lower

const round = (value) => String(Math.round(value * 10) / 10);

/**
 * @param {Document} doc
 */
export function createHistoryView(doc) {
  const byId = (id) => {
    const element = doc.getElementById(id);
    if (!element) throw new Error(`the page has no element #${id}`);
    return element;
  };
  const charts = byId('charts');
  const status = byId('history-status');
  const tableBox = byId('table-view');
  const tableHolder = byId('table-holder');
  const rangeButtons = [...doc.querySelectorAll('.range-button')];
  const plots = METRICS.map((metric) => ({
    metric,
    svg: byId(`plot-${metric.key}`),
    readout: byId(`readout-${metric.key}`),
    layout: null,
    series: [],
    cursor: null,
  }));

  let drawn = null; // what was drawn last: {data, unit, width}
  let cursorTime = null; // the moment pointed at, or null
  let tableStale = true;

  const element = (name, attributes, text) => {
    const node = doc.createElementNS(SVG_NS, name);
    for (const [key, value] of Object.entries(attributes)) node.setAttribute(key, String(value));
    if (text !== undefined) node.textContent = text;
    return node;
  };

  // What was written last, so that refreshing the page every second touches it only when something changed.
  const texts = new WeakMap();
  const attributes = new WeakMap();
  const setText = (node, text) => {
    if (texts.get(node) !== text) {
      node.textContent = text;
      texts.set(node, text);
    }
  };
  const setAttribute = (node, name, value) => {
    if (!attributes.has(node)) attributes.set(node, {});
    const known = attributes.get(node);
    if (known[name] !== value) {
      node.setAttribute(name, value);
      known[name] = value;
    }
  };

  function paint(plot, { width, plotHeight, labelled, ticks, tickStep }) {
    const { layout, svg } = plot;
    const height = plotHeight + (labelled ? AXIS_HEIGHT : 0);
    svg.replaceChildren();
    svg.setAttribute('width', String(width));
    svg.setAttribute('height', String(height));
    svg.setAttribute('viewBox', `0 0 ${width} ${height}`);

    const grid = element('g', { class: 'grid' });
    for (const t of ticks) {
      const x = round(layout.x(t));
      grid.append(element('line', { class: 'grid-line', x1: x, x2: x, y1: 0, y2: plotHeight }));
    }
    for (const tick of layout.yTicks) {
      const y = round(tick.y);
      grid.append(element('line', { class: 'grid-line', x1: 0, x2: width, y1: y, y2: y }));
    }
    svg.append(grid);

    const marks = element('g', { class: 'marks' });
    for (const segment of layout.segments) {
      marks.append(element('path', { class: 'band', d: segment.band }));
      marks.append(element('path', { class: 'line', d: segment.line }));
    }
    for (const dot of layout.dots) {
      marks.append(element('circle', { class: 'dot', cx: round(dot.x), cy: round(dot.y), r: 2.5 }));
    }
    svg.append(marks);

    // The value labels sit on the grid lines inside the plot, so the plot can use the whole width of a phone.
    const labels = element('g', { class: 'labels' });
    for (const tick of layout.yTicks) {
      const y = tick.y < 16 ? tick.y + 12 : tick.y - 4;
      labels.append(element('text', { class: 'tick-label', x: 2, y: round(y) }, plot.metric.format(tick.value)));
    }
    if (labelled) {
      for (const t of ticks) {
        const x = layout.x(t);
        const anchor = x < 16 ? 'start' : x > width - 16 ? 'end' : 'middle';
        const text = tickStep >= DAY_MS ? timeFormat.day(t) : timeFormat.clock(t);
        labels.append(element('text', { class: 'tick-label', x: round(x), y: plotHeight + 14, 'text-anchor': anchor }, text));
      }
    }
    svg.append(labels);

    const cursor = element('g', { class: 'cursor', 'data-active': 'false' });
    const line = element('line', { class: 'cursor-line', y1: 0, y2: plotHeight });
    const dot = element('circle', { class: 'cursor-dot', r: 4 });
    cursor.append(line, dot);
    svg.append(cursor);
    plot.cursor = { group: cursor, line, dot };
  }

  function clear(width) {
    const plotHeight = width < NARROW ? 112 : 136;
    plots.forEach((plot, i) => {
      plot.layout = null;
      plot.series = [];
      plot.cursor = null;
      plot.svg.replaceChildren();
      plot.svg.setAttribute('width', String(Math.max(width, 0)));
      plot.svg.setAttribute('height', String(plotHeight + (i === plots.length - 1 ? AXIS_HEIGHT : 0)));
      setText(plot.readout, '');
    });
  }

  function draw(data, unit, width) {
    const plotHeight = width < NARROW ? 112 : 136;
    const offset = -new Date(data.to).getTimezoneOffset();
    const { step: tickStep, ticks } = timeTicks(data.from, data.to, offset, Math.max(2, Math.floor(width / MIN_TICK_GAP)));
    const words = RANGES[data.range]?.words ?? '';
    plots.forEach((plot, i) => {
      plot.series = seriesOf(data.points, plot.metric, unit);
      plot.layout = layoutChart({
        points: plot.series,
        from: data.from,
        to: data.to,
        stepMs: data.stepMs,
        width,
        height: plotHeight,
        minSpan: plot.metric.minSpan(unit),
      });
      paint(plot, { width, plotHeight, labelled: i === plots.length - 1, ticks, tickStep });
      const summary = describeSummary(summarize(plot.series), plot.metric, unit);
      plot.summary = summary;
      plot.svg.setAttribute('aria-label', `${plot.metric.label}, ${words}. ${summary}`);
    });
  }

  function times() {
    return plots[0].series.map((point) => point.t);
  }

  /** Points at the moment t (or at nothing, for null) on every chart at once. */
  function pointAt(t, unit) {
    cursorTime = t;
    const data = drawn?.data;
    if (!data) return;
    const index = t === null ? -1 : windowIndexAt(times(), t, data.stepMs);
    for (const plot of plots) {
      if (!plot.cursor) continue;
      if (t === null) {
        plot.cursor.group.setAttribute('data-active', 'false');
        setText(plot.readout, plot.summary ?? '');
      } else if (index < 0) {
        plot.cursor.group.setAttribute('data-active', 'false');
        setText(plot.readout, `${timeFormat.moment(t, data.stepMs)}: no readings`);
      } else {
        const point = plot.series[index];
        const x = round(plot.layout.x(point.t));
        plot.cursor.line.setAttribute('x1', x);
        plot.cursor.line.setAttribute('x2', x);
        plot.cursor.dot.setAttribute('cx', x);
        plot.cursor.dot.setAttribute('cy', round(plot.layout.y(point.avg)));
        plot.cursor.group.setAttribute('data-active', 'true');
        setText(plot.readout, describeWindow(point, plot.metric, unit, timeFormat.moment(point.t, data.stepMs)));
      }
    }
  }

  function renderTable(data, unit) {
    if (!tableBox.open) {
      tableStale = true;
      return;
    }
    tableStale = false;
    const table = doc.createElement('table');
    const caption = doc.createElement('caption');
    caption.textContent = `One row per ${formatDuration(data.stepMs / 1000)}, newest first. Every value is the average of its window, with the lowest and the highest in brackets.`;
    table.append(caption);
    const head = doc.createElement('tr');
    for (const title of ['Time', ...plots.map((p) => `${p.metric.label} (${p.metric.unitLabel(unit)})`)]) {
      const cell = doc.createElement('th');
      cell.setAttribute('scope', 'col');
      cell.textContent = title;
      head.append(cell);
    }
    table.append(head);
    for (let i = data.points.length - 1; i >= 0; i -= 1) {
      const row = doc.createElement('tr');
      const time = doc.createElement('th');
      time.setAttribute('scope', 'row');
      time.textContent = timeFormat.moment(data.points[i].t, data.stepMs);
      row.append(time);
      for (const plot of plots) {
        const p = plot.series[i];
        const f = plot.metric.format;
        const cell = doc.createElement('td');
        cell.textContent = `${f(p.avg)} (${f(p.min)}–${f(p.max)})`;
        row.append(cell);
      }
      table.append(row);
    }
    tableHolder.replaceChildren(table);
  }

  tableBox.addEventListener('toggle', () => {
    if (tableBox.open && tableStale && drawn) renderTable(drawn.data, drawn.unit);
  });

  // The charts are read with a mouse, a finger or the arrow keys; all three move the same cursor.
  for (const plot of plots) {
    const pointer = (event) => {
      if (!plot.layout || plot.layout.empty) return;
      const box = plot.svg.getBoundingClientRect();
      pointAt(plot.layout.timeAt(event.clientX - box.left), drawn.unit);
    };
    plot.svg.addEventListener('pointerdown', pointer);
    plot.svg.addEventListener('pointermove', pointer);
    plot.svg.addEventListener('pointerleave', (event) => {
      if (event.pointerType === 'mouse') pointAt(null, drawn?.unit);
    });
    plot.svg.addEventListener('pointercancel', () => pointAt(null, drawn?.unit));
    plot.svg.addEventListener('keydown', (event) => {
      const data = drawn?.data;
      if (!data || data.points.length === 0) return;
      const all = times();
      const last = all.length - 1;
      const now = cursorTime === null ? -1 : windowIndexAt(all, cursorTime, data.stepMs);
      let next;
      if (event.key === 'ArrowLeft') next = now < 0 ? last : Math.max(0, now - 1);
      else if (event.key === 'ArrowRight') next = now < 0 ? last : Math.min(last, now + 1);
      else if (event.key === 'Home') next = 0;
      else if (event.key === 'End') next = last;
      else if (event.key === 'Escape') next = null;
      else return;
      event.preventDefault();
      pointAt(next === null ? null : all[next], drawn.unit);
    });
  }
  // A finger that taps somewhere else lets go of the chart.
  doc.addEventListener('pointerdown', (event) => {
    if (!charts.contains(event.target)) pointAt(null, drawn?.unit);
  });

  return {
    /** The width the charts have, in pixels. */
    width: () => Math.floor(charts.clientWidth),

    /** Calls handler when the section changes its width (a turned phone, a resized window). */
    onResize(handler) {
      if (typeof ResizeObserver === 'function') new ResizeObserver(() => handler()).observe(charts);
    },

    /**
     * @param {import('./history.js').HistoryDescription} description
     * @param {string} range the picked range
     * @param {string} unit the key of the pressure unit
     */
    render(description, range, unit) {
      for (const button of rangeButtons) setAttribute(button, 'aria-pressed', String(button.dataset.range === range));
      setAttribute(charts, 'data-dimmed', String(description.dimmed));
      const width = this.width();
      const { data } = description;
      if (!data || width <= 0) {
        if (!drawn || drawn.data !== null) clear(width);
        drawn = { data: null, unit, width };
        return;
      }
      if (drawn && drawn.data === data && drawn.unit === unit && drawn.width === width) return;
      drawn = { data, unit, width };
      draw(data, unit, width);
      renderTable(data, unit);
      pointAt(cursorTime, unit); // keeps the cursor where it was through a refresh, a resize or a change of unit
    },

    /** The sentence under the range buttons. Cheap enough to call every second. */
    renderStatus(description) {
      setText(status, description.message);
      status.hidden = description.message === '';
      setAttribute(status, 'data-state', description.state);
    },

    /** @param {(range: string) => void} handler called with the key of the range the person picked */
    onRangePicked(handler) {
      for (const button of rangeButtons) {
        button.addEventListener('click', () => handler(button.dataset.range));
      }
    },
  };
}
