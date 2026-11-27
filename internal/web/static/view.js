// The page: it only writes what describe() says and reads nothing back. Text goes in with textContent, never as HTML.

import { PRESSURE_UNITS, formatHumidity, formatPressure, formatTemperature } from './format.js';
import { METRICS, describeDayRange } from './history.js';

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
    account: byId('account'),
    role: byId('role'),
    signIn: byId('sign-in'),
    signOut: byId('sign-out'),
    alert: byId('alert'),
    alertText: byId('alert-text'),
    alertClose: byId('alert-close'),
  };
  const dayRanges = Object.fromEntries(METRICS.map((metric) => [metric.key, byId(`${metric.key}-range`)]));
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

  const setHidden = (element, hidden) => {
    if (element.hidden !== hidden) element.hidden = hidden;
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

    /**
     * The "24 h range 21.8–23.1" under every number.
     * @param {{temperature: object, humidity: object, pressure: object}|null} stats the aggregate of the last 24 hours
     * @param {string} unit the key of the pressure unit
     */
    renderDayRange(stats, unit) {
      for (const metric of METRICS) {
        const element = dayRanges[metric.key];
        const text = stats ? describeDayRange(stats[metric.key], metric, unit) : '';
        setText(element, text);
        if (element.hidden !== (text === '')) element.hidden = text === '';
      }
    },

    /**
     * The role in the corner with its button, and the sentence about a sign-in that did not work.
     * @param {import('./account.js').AccountDescription} account
     */
    renderAccount(account) {
      const hadFocus = doc.activeElement === els.signOut;
      setHidden(els.account, !account.visible);
      setText(els.role, account.label);
      setAttribute(els.role, 'data-role', account.role);
      setHidden(els.signIn, !account.signIn);
      setHidden(els.signOut, account.signOut === 'hidden');
      setText(els.signOut, account.signOut === 'busy' ? 'Signing out…' : 'Sign out');
      // Not "disabled": a button that is disabled while it has the focus drops it.
      setAttribute(els.signOut, 'aria-disabled', String(account.signOut === 'busy'));
      setHidden(els.alert, account.notice === null);
      setText(els.alertText, account.notice ?? '');
      // The button the person has just pressed is gone: the focus goes to what took its place.
      if (hadFocus && account.signOut === 'hidden' && account.signIn) els.signIn.focus();
    },

    /** @param {() => void} handler called when the person presses "Sign out" */
    onSignOut(handler) {
      els.signOut.addEventListener('click', () => handler());
    },

    /** @param {() => void} handler called when the person dismisses the sentence about a problem */
    onAlertDismissed(handler) {
      els.alertClose.addEventListener('click', () => handler());
    },

    /** @param {(unit: string) => void} handler called with the key of the unit the person picked */
    onUnitPicked(handler) {
      for (const button of unitButtons) {
        button.addEventListener('click', () => handler(button.dataset.unit));
      }
    },
  };
}
