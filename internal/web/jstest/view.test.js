import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

import { createView } from '../static/view.js';

// The smallest stand-in for the page that the view needs. It counts writes, to see what a refresh touches.
class FakeElement {
  constructor(dataset = {}) {
    this.dataset = { ...dataset };
    this.hidden = false;
    this.writes = 0;
    this.text = '';
    this.attributes = {};
    this.listeners = [];
  }

  get textContent() {
    return this.text;
  }

  set textContent(value) {
    this.text = value;
    this.writes += 1;
  }

  setAttribute(name, value) {
    this.attributes[name] = value;
    this.writes += 1;
  }

  addEventListener(type, handler) {
    assert.equal(type, 'click');
    this.listeners.push(handler);
  }

  click() {
    for (const handler of this.listeners) handler();
  }
}

function fakePage() {
  const ids = [
    'board', 'temperature', 'humidity', 'pressure', 'pressure-unit', 'status', 'status-text', 'role',
    'temperature-range', 'humidity-range', 'pressure-range',
  ];
  const elements = Object.fromEntries(ids.map((id) => [id, new FakeElement()]));
  // As in index.html: the role is shown once it is known, and so are the ranges of the last day.
  for (const id of ['role', 'temperature-range', 'humidity-range', 'pressure-range']) elements[id].hidden = true;
  const buttons = [new FakeElement({ unit: 'mmhg' }), new FakeElement({ unit: 'hpa' })];
  const doc = {
    titleWrites: 0,
    _title: '',
    get title() {
      return this._title;
    },
    set title(value) {
      this._title = value;
      this.titleWrites += 1;
    },
    getElementById: (id) => elements[id] ?? null,
    querySelectorAll: (selector) => (selector === '.unit-button' ? buttons : []),
  };
  return { doc, el: elements, buttons };
}

const live = {
  state: 'live',
  message: 'Live, updated 7 s ago',
  dimmed: false,
  values: { temperature: 22.43, humidity: 45.2, pressure: 101325 },
};

const empty = { state: 'empty', message: 'No readings yet.', dimmed: false, values: null };

describe('createView', () => {
  it('fails at once, and says which element is missing, when the page is not the one it expects', () => {
    const page = fakePage();
    delete page.el.status;
    assert.throws(() => createView(page.doc), /#status/);
  });
});

describe('render', () => {
  it('writes the readings, in the unit asked for', () => {
    const { doc, el } = fakePage();
    createView(doc).render(live, 'mmhg');

    assert.equal(el.temperature.text, '22.4');
    assert.equal(el.humidity.text, '45');
    assert.equal(el.pressure.text, '760.0');
    assert.equal(el['pressure-unit'].text, 'mm Hg');
  });

  it('converts the pressure when the unit changes', () => {
    const { doc, el } = fakePage();
    const view = createView(doc);
    view.render(live, 'mmhg');
    view.render(live, 'hpa');

    assert.equal(el.pressure.text, '1013.3');
    assert.equal(el['pressure-unit'].text, 'hPa');
  });

  it('writes the state, the sentence and whether the numbers are dimmed', () => {
    const { doc, el } = fakePage();
    createView(doc).render({ ...live, state: 'stale', message: 'Last reading 8 min ago.', dimmed: true }, 'mmhg');

    assert.equal(el.status.attributes['data-state'], 'stale');
    assert.equal(el['status-text'].text, 'Last reading 8 min ago.');
    assert.equal(el.board.attributes['data-dimmed'], 'true');
    assert.equal(el.board.attributes['data-empty'], 'false');
  });

  it('shows dashes, and marks the board empty, when there are no values', () => {
    const { doc, el } = fakePage();
    createView(doc).render(empty, 'mmhg');

    assert.deepEqual([el.temperature.text, el.humidity.text, el.pressure.text], ['–', '–', '–']);
    assert.equal(el['pressure-unit'].text, 'mm Hg');
    assert.equal(el.board.attributes['data-empty'], 'true');
  });

  it('marks the unit that is in use as pressed, and only that one', () => {
    const { doc, buttons } = fakePage();
    const view = createView(doc);

    view.render(live, 'mmhg');
    assert.deepEqual(buttons.map((b) => b.attributes['aria-pressed']), ['true', 'false']);
    view.render(live, 'hpa');
    assert.deepEqual(buttons.map((b) => b.attributes['aria-pressed']), ['false', 'true']);
  });

  it('puts the temperature in the title of the tab, and takes it away when there is none', () => {
    const { doc } = fakePage();
    const view = createView(doc);

    view.render(live, 'mmhg');
    assert.equal(doc.title, '22.4 °C – Climate');
    view.render(empty, 'mmhg');
    assert.equal(doc.title, 'Climate');
  });

  it('touches nothing when nothing changed, so a refresh every second costs nothing', () => {
    const { doc, el, buttons } = fakePage();
    const view = createView(doc);
    view.render(live, 'mmhg');

    const all = [...Object.values(el), ...buttons];
    const before = all.map((e) => e.writes);
    const titleWrites = doc.titleWrites;
    view.render({ ...live }, 'mmhg');
    view.render({ ...live, values: { ...live.values } }, 'mmhg');

    assert.deepEqual(all.map((e) => e.writes), before);
    assert.equal(doc.titleWrites, titleWrites);
  });

  it('writes again as soon as something does change', () => {
    const { doc, el } = fakePage();
    const view = createView(doc);
    view.render(live, 'mmhg');
    const writes = el['status-text'].writes;
    view.render({ ...live, message: 'Live, updated 8 s ago' }, 'mmhg');

    assert.equal(el['status-text'].text, 'Live, updated 8 s ago');
    assert.equal(el['status-text'].writes, writes + 1);
  });

  it('puts text in as text: markup in a sentence stays markup', () => {
    const { doc, el } = fakePage();
    createView(doc).render({ ...live, message: '<img src=x onerror=alert(1)>' }, 'mmhg');
    // The stand-in only has textContent. A view that used innerHTML would leave this empty.
    assert.equal(el['status-text'].text, '<img src=x onerror=alert(1)>');
  });
});

describe('renderDayRange', () => {
  // What /stats?range=24h gives, in the units of the server (pascals).
  const day = {
    count: 17280,
    temperature: { avg: 22.4, min: 21.84, max: 23.06 },
    humidity: { avg: 42, min: 36.2, max: 48.4 },
    pressure: { avg: 101325, min: 100900, max: 101700 },
  };

  it('is not there before the numbers arrive', () => {
    const { el } = fakePage();
    assert.deepEqual([el['temperature-range'].hidden, el['humidity-range'].hidden, el['pressure-range'].hidden], [true, true, true]);
  });

  it('writes the lowest and the highest value of the day under every number, in the unit asked for', () => {
    const { doc, el } = fakePage();
    const view = createView(doc);
    view.renderDayRange(day, 'hpa');

    assert.equal(el['temperature-range'].text, '24 h range 21.8–23.1');
    assert.equal(el['humidity-range'].text, '24 h range 36–48');
    assert.equal(el['pressure-range'].text, '24 h range 1009.0–1017.0');
    assert.deepEqual([el['temperature-range'].hidden, el['humidity-range'].hidden, el['pressure-range'].hidden], [false, false, false]);

    view.renderDayRange(day, 'mmhg');
    assert.equal(el['pressure-range'].text, '24 h range 756.8–762.8');
  });

  it('hides the lines again when there are no numbers (a day without readings)', () => {
    const { doc, el } = fakePage();
    const view = createView(doc);
    view.renderDayRange(day, 'mmhg');
    view.renderDayRange(null, 'mmhg');

    assert.deepEqual([el['temperature-range'].text, el['humidity-range'].text, el['pressure-range'].text], ['', '', '']);
    assert.deepEqual([el['temperature-range'].hidden, el['humidity-range'].hidden, el['pressure-range'].hidden], [true, true, true]);
  });

  it('touches nothing when nothing changed', () => {
    const { doc, el } = fakePage();
    const view = createView(doc);
    view.renderDayRange(day, 'mmhg');
    const lines = [el['temperature-range'], el['humidity-range'], el['pressure-range']];
    const before = lines.map((e) => e.writes);
    view.renderDayRange({ ...day }, 'mmhg');
    assert.deepEqual(lines.map((e) => e.writes), before);
  });
});

describe('showRole', () => {
  it('names the owner', () => {
    const { doc, el } = fakePage();
    createView(doc).showRole({ role: 'owner' });

    assert.equal(el.role.textContent, 'Signed in as owner');
    assert.equal(el.role.dataset.role, 'owner');
    assert.equal(el.role.hidden, false);
  });

  it('treats everyone else as a visitor of the demo', () => {
    const { doc, el } = fakePage();
    for (const role of ['demo', 'member', undefined]) {
      createView(doc).showRole({ role });
      assert.equal(el.role.textContent, 'Demo view, read-only', String(role));
      assert.equal(el.role.dataset.role, 'demo');
    }
  });
});

describe('onUnitPicked', () => {
  it('tells which unit was picked', () => {
    const { doc, buttons } = fakePage();
    const picked = [];
    createView(doc).onUnitPicked((unit) => picked.push(unit));

    buttons[1].click();
    buttons[0].click();
    assert.deepEqual(picked, ['hpa', 'mmhg']);
  });
});
