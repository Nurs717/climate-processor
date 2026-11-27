import { afterEach, describe, it } from 'node:test';
import assert from 'node:assert/strict';

import { createHistoryView } from '../static/chart-view.js';
import { timeFormat } from '../static/format.js';

// The smallest stand-in for the page that the history view needs. It keeps what was put in and counts the redraws.
class FakeNode {
  constructor(tag = 'div', dataset = {}) {
    this.tag = tag;
    this.dataset = { ...dataset };
    this.attributes = {};
    this.children = [];
    this.listeners = {};
    this.parent = null;
    this.hidden = false;
    this.open = false;
    this.clientWidth = 0;
    this.redraws = 0;
    this.text = '';
    this.box = { left: 0 };
  }

  get textContent() {
    return this.text;
  }

  set textContent(value) {
    this.text = String(value);
  }

  setAttribute(name, value) {
    this.attributes[name] = String(value);
  }

  append(...nodes) {
    for (const node of nodes) {
      node.parent = this;
      this.children.push(node);
    }
  }

  replaceChildren(...nodes) {
    this.redraws += 1;
    this.children = [];
    this.append(...nodes);
  }

  contains(node) {
    for (let at = node; at; at = at.parent) if (at === this) return true;
    return false;
  }

  getBoundingClientRect() {
    return this.box;
  }

  addEventListener(type, handler) {
    (this.listeners[type] ??= []).push(handler);
  }

  dispatch(type, event = {}) {
    const e = { target: this, preventDefault() { this.prevented = true; }, ...event };
    for (const handler of this.listeners[type] ?? []) handler(e);
    return e;
  }

  find(test, found = []) {
    for (const child of this.children) {
      if (test(child)) found.push(child);
      child.find(test, found);
    }
    return found;
  }

  withClass(name) {
    return this.find((n) => n.attributes.class === name);
  }
}

const METRIC_KEYS = ['temperature', 'humidity', 'pressure'];

function fakePage({ width = 500 } = {}) {
  const ids = {};
  const node = (id, tag) => (ids[id] = new FakeNode(tag));
  const charts = node('charts');
  charts.clientWidth = width;
  node('history-status');
  node('table-view', 'details');
  node('table-holder');
  for (const key of METRIC_KEYS) {
    node(`plot-${key}`, 'svg').parent = charts; // a chart is inside the charts, so a tap on it is a tap on the charts
    node(`readout-${key}`, 'span');
  }
  const buttons = ['6h', '24h', '7d', '30d'].map((range) => new FakeNode('button', { range }));
  const doc = new FakeNode('document');
  Object.assign(doc, {
    getElementById: (id) => ids[id] ?? null,
    querySelectorAll: (selector) => (selector === '.range-button' ? buttons : []),
    createElement: (tag) => new FakeNode(tag),
    createElementNS: (_ns, tag) => new FakeNode(tag),
  });
  return {
    doc,
    charts,
    buttons,
    status: ids['history-status'],
    table: ids['table-view'],
    holder: ids['table-holder'],
    plot: (key) => ids[`plot-${key}`],
    readout: (key) => ids[`readout-${key}`],
  };
}

const MIN = 60_000;
const stat = (avg, min = avg - 0.5, max = avg + 0.5) => ({ avg, min, max });
const window = (minute, temp) => ({
  t: minute * MIN,
  count: 12,
  temperature: stat(temp),
  humidity: stat(40 + minute),
  pressure: stat(101325 + minute * 10),
});

// Windows of a minute at minutes 0, 1, 2 and 6, 7, 8 of a ten-minute range: a gap of three minutes in the middle.
const range = (points = [0, 1, 2, 6, 7, 8].map((minute) => window(minute, 20 + minute))) => ({
  from: 0,
  to: 10 * MIN,
  stepMs: MIN,
  range: '24h',
  points,
});

const ready = (data) => ({ state: 'ready', message: '', dimmed: false, data });
const loading = { state: 'loading', message: 'Loading the history…', dimmed: false, data: null };

// A pointer at x pixels from the left of a chart that is 500 pixels wide: 600000 ms / 500 px.
const at = (minute) => ({ clientX: (minute * MIN * 500) / (10 * MIN) });

describe('createHistoryView', () => {
  it('fails at once, and says which element is missing, when the page is not the one it expects', () => {
    const page = fakePage();
    page.doc.getElementById = (id) => (id === 'plot-humidity' ? null : { addEventListener() {} });
    assert.throws(() => createHistoryView(page.doc), /#plot-humidity/);
  });
});

describe('render', () => {
  it('draws a line and a band for the windows of every chart, and says what it shows', () => {
    const page = fakePage();
    createHistoryView(page.doc).render(ready(range()), '24h', 'mmhg');

    for (const key of METRIC_KEYS) {
      const svg = page.plot(key);
      assert.equal(svg.attributes.width, '500');
      assert.equal(svg.attributes.viewBox, `0 0 500 ${svg.attributes.height}`);
      assert.equal(svg.withClass('line').length, 2, `${key}: two lines, one on each side of the gap`);
      assert.equal(svg.withClass('band').length, 2);
      assert.match(svg.attributes['aria-label'], new RegExp(`^${key[0].toUpperCase()}${key.slice(1)}, last 24 hours\\. Low `));
    }
    assert.match(page.plot('temperature').attributes['aria-label'], /Low 19\.5, avg 24\.0, high 28\.5 °C$/);
    assert.match(page.readout('temperature').text, /^Low 19\.5, avg 24\.0, high 28\.5 °C$/);
  });

  it('writes the pressure in the unit that is shown', () => {
    const page = fakePage();
    createHistoryView(page.doc).render(ready(range()), '24h', 'hpa');
    assert.match(page.readout('pressure').text, / hPa$/);
  });

  it('puts the time under the last chart only, and the values on the grid lines of every chart', () => {
    const page = fakePage();
    createHistoryView(page.doc).render(ready(range()), '24h', 'mmhg');

    const labels = (key) => page.plot(key).withClass('tick-label').map((n) => n.text);
    assert.ok(labels('pressure').some((text) => /^\d\d:\d\d$/.test(text)), labels('pressure').join());
    for (const key of ['temperature', 'humidity']) {
      assert.ok(labels(key).length >= 2);
      assert.ok(labels(key).every((text) => !/^\d\d:\d\d$/.test(text)), `${key}: ${labels(key)}`);
    }
    assert.ok(Number(page.plot('pressure').attributes.height) > Number(page.plot('humidity').attributes.height));
  });

  it('writes a value label under its grid line when the line is too near the top of the chart for the label to fit above it', () => {
    const page = fakePage();
    const flat = (minute, value) => ({ ...window(minute, value), temperature: stat(value, value, value) });
    createHistoryView(page.doc).render(ready(range([flat(0, 20), flat(1, 22), flat(2, 24)])), '24h', 'mmhg');

    const svg = page.plot('temperature');
    const lines = svg.withClass('grid-line').filter((n) => n.attributes.y1 === n.attributes.y2 && n.attributes.x1 === '0');
    const labels = svg.withClass('tick-label').filter((n) => n.attributes.x === '2');
    assert.deepEqual(labels.map((n) => n.text), ['20.0', '22.0', '24.0']);
    assert.equal(lines.length, 3);
    const [bottom, , top] = labels.map((n, i) => ({ label: Number(n.attributes.y), line: Number(lines[i].attributes.y1) }));
    assert.ok(bottom.label < bottom.line, 'there is room above the lower lines');
    assert.ok(top.line < 16, 'the test needs a line near the top');
    assert.ok(top.label > top.line, 'the label of the line at the top goes under it');
  });

  it('makes the charts lower on a narrow screen', () => {
    const narrow = fakePage({ width: 360 });
    createHistoryView(narrow.doc).render(ready(range()), '24h', 'mmhg');
    const wide = fakePage({ width: 800 });
    createHistoryView(wide.doc).render(ready(range()), '24h', 'mmhg');
    assert.ok(Number(narrow.plot('temperature').attributes.height) < Number(wide.plot('temperature').attributes.height));
  });

  it('draws a lone window as a dot', () => {
    const page = fakePage();
    createHistoryView(page.doc).render(ready(range([window(0, 20), window(5, 21), window(9, 22)])), '24h', 'mmhg');
    assert.equal(page.plot('temperature').withClass('line').length, 0);
    assert.equal(page.plot('temperature').withClass('dot').length, 3);
  });

  it('marks the picked range as pressed, and only that one', () => {
    const page = fakePage();
    const view = createHistoryView(page.doc);
    view.render(loading, '7d', 'mmhg');
    assert.deepEqual(page.buttons.map((b) => b.attributes['aria-pressed']), ['false', 'false', 'true', 'false']);
    view.render(loading, '6h', 'mmhg');
    assert.deepEqual(page.buttons.map((b) => b.attributes['aria-pressed']), ['true', 'false', 'false', 'false']);
  });

  it('dims the charts when what they show is old', () => {
    const page = fakePage();
    const view = createHistoryView(page.doc);
    view.render({ ...ready(range()), dimmed: true }, '24h', 'mmhg');
    assert.equal(page.charts.attributes['data-dimmed'], 'true');
    view.render(ready(range()), '24h', 'mmhg');
    assert.equal(page.charts.attributes['data-dimmed'], 'false');
  });

  it('keeps the room of the charts while there is nothing to draw, and says nothing in them', () => {
    const page = fakePage();
    const view = createHistoryView(page.doc);
    view.render(loading, '24h', 'mmhg');

    for (const key of METRIC_KEYS) {
      assert.equal(page.plot(key).attributes.width, '500');
      assert.equal(page.plot(key).children.length, 0);
      assert.equal(page.readout(key).text, '');
    }
    assert.equal(page.plot('temperature').attributes.height, '112');
    assert.equal(page.plot('pressure').attributes.height, '132', 'the last chart has room for the time');
  });

  it('empties the charts when the data goes away', () => {
    const page = fakePage();
    const view = createHistoryView(page.doc);
    view.render(ready(range()), '24h', 'mmhg');
    view.render(loading, '24h', 'mmhg');
    assert.equal(page.plot('temperature').children.length, 0);
    assert.equal(page.readout('temperature').text, '');
  });

  it('draws nothing in a chart that has no width yet (a hidden page), and draws once it has one', () => {
    const page = fakePage({ width: 0 });
    const view = createHistoryView(page.doc);
    view.render(ready(range()), '24h', 'mmhg');
    assert.equal(page.plot('temperature').children.length, 0);
    page.charts.clientWidth = 500;
    view.render(ready(range()), '24h', 'mmhg');
    assert.equal(page.plot('temperature').withClass('line').length, 2);
  });

  it('does not draw again what is drawn already: the page is refreshed every second', () => {
    const page = fakePage();
    const view = createHistoryView(page.doc);
    const data = range();
    view.render(ready(data), '24h', 'mmhg');
    const before = page.plot('temperature').redraws;
    view.render(ready(data), '24h', 'mmhg');
    view.render({ ...ready(data) }, '24h', 'mmhg');
    assert.equal(page.plot('temperature').redraws, before);
  });

  it('draws again for new data, another unit or another width', () => {
    const page = fakePage();
    const view = createHistoryView(page.doc);
    const data = range();
    view.render(ready(data), '24h', 'mmhg');
    const redrawsBefore = page.plot('temperature').redraws;

    view.render(ready(range()), '24h', 'mmhg');
    assert.equal(page.plot('temperature').redraws, redrawsBefore + 1, 'new data');
    const redraws = page.plot('temperature').redraws;
    const same = range();
    view.render(ready(same), '24h', 'mmhg');
    view.render(ready(same), '24h', 'hpa');
    assert.equal(page.plot('temperature').redraws, redraws + 2);
    page.charts.clientWidth = 400;
    view.render(ready(same), '24h', 'hpa');
    assert.equal(page.plot('temperature').redraws, redraws + 3);
    assert.equal(page.plot('temperature').attributes.width, '400');
  });

  it('puts text in as text: markup in a message stays markup', () => {
    const page = fakePage();
    createHistoryView(page.doc).renderStatus({ state: 'offline', message: '<img src=x onerror=alert(1)>', dimmed: false, data: null });
    assert.equal(page.status.text, '<img src=x onerror=alert(1)>');
  });
});

describe('renderStatus', () => {
  it('writes the sentence and the state, and shows the line only when there is a sentence', () => {
    const page = fakePage();
    const view = createHistoryView(page.doc);
    view.renderStatus({ state: 'offline', message: "Can't load the history. Retrying…", dimmed: false, data: null });
    assert.equal(page.status.text, "Can't load the history. Retrying…");
    assert.equal(page.status.attributes['data-state'], 'offline');
    assert.equal(page.status.hidden, false);

    view.renderStatus(ready(range()));
    assert.equal(page.status.text, '');
    assert.equal(page.status.hidden, true);
    assert.equal(page.status.attributes['data-state'], 'ready');
  });
});

describe('the cursor', () => {
  const setup = () => {
    const page = fakePage();
    const view = createHistoryView(page.doc);
    view.render(ready(range()), '24h', 'mmhg');
    const cursors = () => METRIC_KEYS.map((key) => page.plot(key).withClass('cursor')[0].attributes['data-active']);
    return { page, view, cursors };
  };

  it('is hidden until something is pointed at', () => {
    const { cursors } = setup();
    assert.deepEqual(cursors(), ['false', 'false', 'false']);
  });

  it('moves on all three charts at once, and every chart says what its window holds', () => {
    const { page, cursors } = setup();
    page.plot('humidity').dispatch('pointermove', at(1.5));

    assert.deepEqual(cursors(), ['true', 'true', 'true']);
    const moment = timeFormat.moment(MIN, MIN);
    assert.equal(page.readout('temperature').text, `${moment}: 21.0 °C, low 20.5, high 21.5`);
    assert.equal(page.readout('humidity').text, `${moment}: 41 %, low 41, high 42`);
    assert.match(page.readout('pressure').text, new RegExp(`^${moment}: 7\\d\\d\\.\\d mm Hg, low `));
    const x = page.plot('temperature').withClass('cursor-line')[0].attributes.x1;
    assert.equal(x, '50', 'the window starts at a minute: 50 px of 500');
    assert.equal(page.plot('pressure').withClass('cursor-line')[0].attributes.x1, x);
  });

  it('sits on the average of the window', () => {
    const { page } = setup();
    page.plot('temperature').dispatch('pointerdown', at(7.5));
    const dot = page.plot('temperature').withClass('cursor-dot')[0];
    assert.equal(dot.attributes.cx, '350');
    const y = Number(dot.attributes.cy);
    assert.ok(y > 0 && y < 112);
  });

  it('says there are no readings, and hides the dot, in a gap', () => {
    const { page, cursors } = setup();
    page.plot('temperature').dispatch('pointermove', at(4));
    assert.deepEqual(cursors(), ['false', 'false', 'false']);
    assert.equal(page.readout('temperature').text, `${timeFormat.moment(4 * MIN, MIN)}: no readings`);
  });

  it('lets go when the mouse leaves, and goes back to what the chart says as a whole', () => {
    const { page, cursors } = setup();
    page.plot('temperature').dispatch('pointermove', at(1.5));
    page.plot('temperature').dispatch('pointerleave', { pointerType: 'mouse' });
    assert.deepEqual(cursors(), ['false', 'false', 'false']);
    assert.match(page.readout('temperature').text, /^Low 19\.5, avg 24\.0, high 28\.5 °C$/);
  });

  it('stays where a finger left it: a finger that lifts has not let go of the chart', () => {
    const { page, cursors } = setup();
    page.plot('temperature').dispatch('pointerdown', at(1.5));
    page.plot('temperature').dispatch('pointerleave', { pointerType: 'touch' });
    assert.deepEqual(cursors(), ['true', 'true', 'true']);
  });

  it('lets go when the browser takes the touch for a scroll', () => {
    const { page, cursors } = setup();
    page.plot('temperature').dispatch('pointerdown', at(1.5));
    page.plot('temperature').dispatch('pointercancel');
    assert.deepEqual(cursors(), ['false', 'false', 'false']);
  });

  it('lets go when a finger taps somewhere else, and not when it taps on the charts', () => {
    const { page, cursors } = setup();
    page.plot('temperature').dispatch('pointerdown', at(1.5));
    page.doc.dispatch('pointerdown', { target: page.plot('humidity') });
    assert.deepEqual(cursors(), ['true', 'true', 'true']);
    page.doc.dispatch('pointerdown', { target: new FakeNode('p') });
    assert.deepEqual(cursors(), ['false', 'false', 'false']);
  });

  it('is kept through a refresh of the data, a change of unit and a resize', () => {
    const { page, view, cursors } = setup();
    page.plot('temperature').dispatch('pointermove', at(7.5));
    const moment = timeFormat.moment(7 * MIN, MIN);

    view.render(ready(range()), '24h', 'mmhg');
    assert.deepEqual(cursors(), ['true', 'true', 'true']);
    assert.match(page.readout('temperature').text, new RegExp(`^${moment}: 27\\.0 °C`));

    view.render(ready(range()), '24h', 'hpa');
    assert.match(page.readout('pressure').text, new RegExp(`^${moment}: 10\\d\\d\\.\\d hPa`));

    page.charts.clientWidth = 250;
    view.render(ready(range()), '24h', 'hpa');
    assert.equal(page.plot('temperature').withClass('cursor-line')[0].attributes.x1, '175');
  });

  it('ignores the pointer while there is nothing drawn', () => {
    const page = fakePage();
    createHistoryView(page.doc).render(loading, '24h', 'mmhg');
    assert.doesNotThrow(() => page.plot('temperature').dispatch('pointermove', at(1)));
    assert.equal(page.readout('temperature').text, '');
  });

  it('ignores the pointer over a range without readings', () => {
    const page = fakePage();
    createHistoryView(page.doc).render({ ...ready(range([])), state: 'empty' }, '24h', 'mmhg');
    assert.doesNotThrow(() => page.plot('temperature').dispatch('pointermove', at(1)));
    assert.equal(page.readout('temperature').text, '');
  });

  describe('with the keyboard', () => {
    const press = (page, key) => page.plot('humidity').dispatch('keydown', { key });
    const humidity = (page) => page.readout('humidity').text;
    const minute = (m) => timeFormat.moment(m * MIN, MIN);

    it('starts at the newest window with the left arrow and at the end with End', () => {
      const { page } = setup();
      press(page, 'ArrowLeft');
      assert.match(humidity(page), new RegExp(`^${minute(8)}: 48 %`));
    });

    it('goes from window to window, and not across the gap, because there is no window there', () => {
      const { page } = setup();
      press(page, 'Home');
      assert.match(humidity(page), new RegExp(`^${minute(0)}: 40 %`));
      press(page, 'ArrowRight');
      press(page, 'ArrowRight');
      assert.match(humidity(page), new RegExp(`^${minute(2)}: 42 %`));
      press(page, 'ArrowRight');
      assert.match(humidity(page), new RegExp(`^${minute(6)}: 46 %`));
      press(page, 'ArrowLeft');
      assert.match(humidity(page), new RegExp(`^${minute(2)}: 42 %`));
    });

    it('stops at both ends', () => {
      const { page } = setup();
      press(page, 'End');
      press(page, 'ArrowRight');
      assert.match(humidity(page), new RegExp(`^${minute(8)}: 48 %`));
      press(page, 'Home');
      press(page, 'ArrowLeft');
      assert.match(humidity(page), new RegExp(`^${minute(0)}: 40 %`));
    });

    it('lets go with Escape', () => {
      const { page, cursors } = setup();
      press(page, 'End');
      press(page, 'Escape');
      assert.deepEqual(cursors(), ['false', 'false', 'false']);
      assert.match(humidity(page), /^Low /);
    });

    it('takes over the keys it uses, and no others', () => {
      const { page } = setup();
      for (const key of ['ArrowLeft', 'ArrowRight', 'Home', 'End', 'Escape']) assert.equal(press(page, key).prevented, true, key);
      for (const key of ['Tab', 'ArrowUp', 'a', 'Enter']) assert.equal(press(page, key).prevented, undefined, key);
    });

    it('does nothing while there is nothing drawn', () => {
      const page = fakePage();
      createHistoryView(page.doc).render(loading, '24h', 'mmhg');
      assert.equal(press(page, 'End').prevented, undefined);
    });
  });
});

describe('the table', () => {
  it('is built only when it is opened: a month of windows is not a page of rows nobody reads', () => {
    const page = fakePage();
    createHistoryView(page.doc).render(ready(range()), '24h', 'mmhg');
    assert.equal(page.holder.children.length, 0);
  });

  it('is built when it is opened, newest row first, with the lowest and the highest value of every window', () => {
    const page = fakePage();
    createHistoryView(page.doc).render(ready(range()), '24h', 'mmhg');
    page.table.open = true;
    page.table.dispatch('toggle');

    const [table] = page.holder.children;
    const rows = table.children.filter((n) => n.tag === 'tr');
    assert.equal(rows.length, 1 + 6);
    assert.deepEqual(rows[0].children.map((c) => c.text), ['Time', 'Temperature (°C)', 'Humidity (%)', 'Pressure (mm Hg)']);
    assert.equal(rows[1].children[0].text, timeFormat.moment(8 * MIN, MIN));
    assert.equal(rows[1].children[1].text, '28.0 (27.5–28.5)');
    assert.equal(rows[1].children[2].text, '48 (48–49)');
    assert.equal(rows[6].children[0].text, timeFormat.moment(0, MIN));
    assert.equal(table.children[0].text, 'One row per 1 min, newest first. Every value is the average of its window, with the lowest and the highest in brackets.');
  });

  it('says to which row and column a cell belongs', () => {
    const page = fakePage();
    createHistoryView(page.doc).render(ready(range()), '24h', 'mmhg');
    page.table.open = true;
    page.table.dispatch('toggle');
    const rows = page.holder.children[0].children.filter((n) => n.tag === 'tr');
    assert.ok(rows[0].children.every((c) => c.tag === 'th' && c.attributes.scope === 'col'));
    assert.equal(rows[1].children[0].tag, 'th');
    assert.equal(rows[1].children[0].attributes.scope, 'row');
    assert.equal(rows[1].children[1].tag, 'td');
  });

  it('follows the data and the unit while it is open', () => {
    const page = fakePage();
    const view = createHistoryView(page.doc);
    view.render(ready(range()), '24h', 'mmhg');
    page.table.open = true;
    page.table.dispatch('toggle');
    view.render(ready(range()), '24h', 'hpa');
    const header = page.holder.children[0].children.filter((n) => n.tag === 'tr')[0];
    assert.equal(header.children[3].text, 'Pressure (hPa)');
  });

  it('is brought up to date when it is opened after the data changed', () => {
    const page = fakePage();
    const view = createHistoryView(page.doc);
    view.render(ready(range()), '24h', 'mmhg');
    view.render(ready(range()), '24h', 'hpa');
    page.table.open = true;
    page.table.dispatch('toggle');
    const header = page.holder.children[0].children.filter((n) => n.tag === 'tr')[0];
    assert.equal(header.children[3].text, 'Pressure (hPa)');
  });
});

describe('onRangePicked', () => {
  it('tells which range was picked', () => {
    const page = fakePage();
    const picked = [];
    createHistoryView(page.doc).onRangePicked((range) => picked.push(range));
    page.buttons[2].dispatch('click');
    page.buttons[0].dispatch('click');
    assert.deepEqual(picked, ['7d', '6h']);
  });
});

describe('onResize', () => {
  const original = globalThis.ResizeObserver;
  afterEach(() => {
    if (original === undefined) delete globalThis.ResizeObserver;
    else globalThis.ResizeObserver = original;
  });

  it('watches the charts and calls the handler when they change', () => {
    const watched = [];
    let notify;
    globalThis.ResizeObserver = class {
      constructor(callback) {
        notify = callback;
      }

      observe(target) {
        watched.push(target);
      }
    };
    const page = fakePage();
    let calls = 0;
    createHistoryView(page.doc).onResize(() => (calls += 1));
    assert.deepEqual(watched, [page.charts]);
    notify([]);
    assert.equal(calls, 1);
  });

  it('is no reason to fail where there is no ResizeObserver', () => {
    delete globalThis.ResizeObserver;
    assert.doesNotThrow(() => createHistoryView(fakePage().doc).onResize(() => {}));
  });
});
