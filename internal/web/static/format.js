// Formatting of the readings for people. Pure functions: nothing here touches the page or the network.

const PASCALS_PER_MMHG = 133.322387415;

/** The units the pressure can be shown in. The server always sends pascals. */
export const PRESSURE_UNITS = Object.freeze({
  mmhg: Object.freeze({ label: 'mm Hg', fromPascals: (pa) => pa / PASCALS_PER_MMHG }),
  hpa: Object.freeze({ label: 'hPa', fromPascals: (pa) => pa / 100 }),
});

export const DEFAULT_PRESSURE_UNIT = 'mmhg';

/** @returns {boolean} whether value is the key of a pressure unit (and not, say, "constructor") */
export function isPressureUnit(value) {
  return typeof value === 'string' && Object.hasOwn(PRESSURE_UNITS, value);
}

// The interface is English only, so the numbers are too: the same "22.4" on every phone, whatever its region.
const oneDecimal = new Intl.NumberFormat('en-US', {
  minimumFractionDigits: 1,
  maximumFractionDigits: 1,
  useGrouping: false,
});

const NO_VALUE = '–';

function decimal(value) {
  if (!Number.isFinite(value)) return NO_VALUE;
  // A tiny negative number would print as "-0.0".
  return oneDecimal.format(Math.abs(value) < 0.05 ? 0 : value);
}

/** @param {number} celsius */
export function formatTemperature(celsius) {
  return decimal(celsius);
}

/** @param {number} percent relative humidity; a sensor cannot tell a tenth of a percent, so it is a whole number */
export function formatHumidity(percent) {
  return Number.isFinite(percent) ? String(Math.round(percent)) : NO_VALUE;
}

/**
 * @param {number} pascals
 * @param {string} unit a key of PRESSURE_UNITS
 */
export function formatPressure(pascals, unit) {
  if (!isPressureUnit(unit)) throw new RangeError(`unknown pressure unit ${JSON.stringify(unit)}`);
  return decimal(PRESSURE_UNITS[unit].fromPascals(pascals));
}

/** A length of time in its two largest units that matter: 45 s, 8 min, 2 h 5 min, 3 d. */
export function formatDuration(totalSeconds) {
  const seconds = Math.max(0, Math.floor(totalSeconds));
  if (seconds < 60) return `${seconds} s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} min`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) {
    const rest = minutes % 60;
    return rest ? `${hours} h ${rest} min` : `${hours} h`;
  }
  const days = Math.floor(hours / 24);
  const restHours = hours % 24;
  return restHours ? `${days} d ${restHours} h` : `${days} d`;
}

/** How long ago something happened, as part of a sentence: "just now", "12 s ago". */
export function formatAgo(seconds) {
  return seconds < 5 ? 'just now' : `${formatDuration(seconds)} ago`;
}
