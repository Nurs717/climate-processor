// The page: it only writes what describe() says and reads nothing back. Text goes in with textContent, never as HTML.

import { PRESSURE_UNITS, formatHumidity, formatPressure, formatTemperature } from './format.js';

const NO_VALUE = '–';

/**
 * @param {Document} doc
 */
export function createView(doc) {
  const byId = (id) => {
    const element = doc.getElementById(id);
    if (!element) throw new Error(`the page has no element #${id}`);
    return element;
  };
  const els = {
    board: byId('board'),
    temperature: byId('temperature'),
    humidity: byId('humidity'),
    pressure: byId('pressure'),
    pressureUnit: byId('pressure-unit'),
    status: byId('status'),
    statusText: byId('status-text'),
    role: byId('role'),
  };
  const unitButtons = [...doc.querySelectorAll('.unit-button')];

  // What was written last, so that the once-a-second refresh touches the page only when something changed.
  const texts = new WeakMap();
  const attributes = new WeakMap();
  const setText = (element, text) => {
    if (texts.get(element) !== text) {
      element.textContent = text;
      texts.set(element, text);
    }
  };
  const setAttribute = (element, name, value) => {
    if (!attributes.has(element)) attributes.set(element, {});
    const known = attributes.get(element);
    if (known[name] !== value) {
      element.setAttribute(name, value);
      known[name] = value;
    }
  };

  return {
    /**
     * @param {import('./model.js').Description} model
     * @param {string} unit the key of the pressure unit
     */
    render(model, unit) {
      const { values } = model;
      setText(els.temperature, values ? formatTemperature(values.temperature) : NO_VALUE);
      setText(els.humidity, values ? formatHumidity(values.humidity) : NO_VALUE);
      setText(els.pressure, values ? formatPressure(values.pressure, unit) : NO_VALUE);
      setText(els.pressureUnit, PRESSURE_UNITS[unit].label);
      setAttribute(els.board, 'data-dimmed', String(model.dimmed));
      setAttribute(els.board, 'data-empty', String(!values));
      setAttribute(els.status, 'data-state', model.state);
      setText(els.statusText, model.message);
      for (const button of unitButtons) {
        setAttribute(button, 'aria-pressed', String(button.dataset.unit === unit));
      }
      // The temperature in the tab, so that the dashboard can be read without switching to it.
      const title = values ? `${formatTemperature(values.temperature)} °C – Climate` : 'Climate';
      if (doc.title !== title) doc.title = title;
    },

    /** @param {{role: string}} me the answer of /api/v1/auth/me */
    showRole(me) {
      const owner = me.role === 'owner';
      els.role.textContent = owner ? 'Signed in as owner' : 'Demo view, read-only';
      els.role.dataset.role = owner ? 'owner' : 'demo';
      els.role.hidden = false;
    },

    /** @param {(unit: string) => void} handler called with the key of the unit the person picked */
    onUnitPicked(handler) {
      for (const button of unitButtons) {
        button.addEventListener('click', () => handler(button.dataset.unit));
      }
    },
  };
}
