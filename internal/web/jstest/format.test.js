import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

import {
  DEFAULT_PRESSURE_UNIT,
  PRESSURE_UNITS,
  createTimeFormat,
  formatAgo,
  formatDecimal,
  formatDuration,
  formatHumidity,
  formatPressure,
  formatTemperature,
  formatWhole,
  isPressureUnit,
} from '../static/format.js';

describe('formatTemperature', () => {
  it('shows one decimal', () => {
    assert.equal(formatTemperature(22.43), '22.4');
    assert.equal(formatTemperature(22), '22.0');
    assert.equal(formatTemperature(-5.25), '-5.3');
  });

  it('does not group digits or follow the locale of the phone', () => {
    assert.equal(formatTemperature(1234.5), '1234.5');
  });

  it('never prints a negative zero', () => {
    assert.equal(formatTemperature(-0.04), '0.0');
    assert.equal(formatTemperature(-0), '0.0');
    assert.equal(formatTemperature(-0.06), '-0.1');
  });

  it('shows a dash for something that is not a number', () => {
    for (const bad of [NaN, Infinity, -Infinity, undefined, null, '22.4']) {
      assert.equal(formatTemperature(bad), '–', String(bad));
    }
  });
});

describe('formatHumidity', () => {
  it('is a whole number', () => {
    assert.equal(formatHumidity(45.2), '45');
    assert.equal(formatHumidity(45.5), '46');
    assert.equal(formatHumidity(0), '0');
    assert.equal(formatHumidity(100), '100');
  });

  it('shows a dash for something that is not a number', () => {
    assert.equal(formatHumidity(NaN), '–');
    assert.equal(formatHumidity(undefined), '–');
  });
});

describe('formatPressure', () => {
  it('converts pascals to mm Hg and hPa', () => {
    assert.equal(formatPressure(101325, 'mmhg'), '760.0'); // standard atmosphere
    assert.equal(formatPressure(101325, 'hpa'), '1013.3');
    assert.equal(formatPressure(98000, 'hpa'), '980.0');
  });

  it('rejects a unit it does not know, including names that every object has', () => {
    for (const bad of ['kpa', '', 'MMHG', 'constructor', '__proto__', 'toString', undefined, null, 5]) {
      assert.throws(() => formatPressure(101325, bad), RangeError, String(bad));
    }
  });

  it('shows a dash for something that is not a number', () => {
    assert.equal(formatPressure(NaN, 'hpa'), '–');
  });
});

describe('pressure units', () => {
  it('recognises exactly its own keys', () => {
    assert.equal(isPressureUnit('mmhg'), true);
    assert.equal(isPressureUnit('hpa'), true);
    for (const bad of ['MMHG', 'pa', 'constructor', '__proto__', 'hasOwnProperty', '', null, undefined, 1, {}]) {
      assert.equal(isPressureUnit(bad), false, String(bad));
    }
  });

  it('defaults to a unit it knows, with a label', () => {
    assert.equal(isPressureUnit(DEFAULT_PRESSURE_UNIT), true);
    assert.equal(PRESSURE_UNITS.mmhg.label, 'mm Hg');
    assert.equal(PRESSURE_UNITS.hpa.label, 'hPa');
  });
});

describe('formatDuration', () => {
  it('uses the two largest units that matter', () => {
    const cases = [
      [0, '0 s'],
      [59.9, '59 s'],
      [60, '1 min'],
      [3599, '59 min'],
      [3600, '1 h'],
      [7500, '2 h 5 min'],
      [86_399, '23 h 59 min'],
      [86_400, '1 d'],
      [93_600, '1 d 2 h'],
    ];
    for (const [seconds, text] of cases) assert.equal(formatDuration(seconds), text, String(seconds));
  });

  it('does not go below zero', () => {
    assert.equal(formatDuration(-12), '0 s');
  });
});

describe('formatAgo', () => {
  it('says "just now" for the first seconds', () => {
    assert.equal(formatAgo(0), 'just now');
    assert.equal(formatAgo(4.9), 'just now');
  });

  it('says how long ago after that', () => {
    assert.equal(formatAgo(5), '5 s ago');
    assert.equal(formatAgo(90), '1 min ago');
  });
});

describe('formatDecimal', () => {
  it('writes one decimal, with a point, whatever the region of the phone', () => {
    assert.equal(formatDecimal(22.44), '22.4');
    assert.equal(formatDecimal(22.45), '22.5');
    assert.equal(formatDecimal(1013), '1013.0');
    assert.equal(formatDecimal(-3.26), '-3.3');
    assert.equal(formatDecimal(12345.67), '12345.7');
  });

  it('does not write "-0.0"', () => {
    assert.equal(formatDecimal(-0.04), '0.0');
    assert.equal(formatDecimal(-0), '0.0');
    assert.equal(formatDecimal(-0.06), '-0.1');
  });

  it('writes a dash for what is not a number', () => {
    for (const bad of [NaN, Infinity, undefined, null, '22']) assert.equal(formatDecimal(bad), '–', String(bad));
  });
});

describe('formatWhole', () => {
  it('rounds to a whole number', () => {
    assert.equal(formatWhole(41.5), '42');
    assert.equal(formatWhole(41.4), '41');
    assert.equal(formatWhole(0), '0');
  });

  it('writes a dash for what is not a number', () => {
    for (const bad of [NaN, Infinity, undefined, null, '22']) assert.equal(formatWhole(bad), '–', String(bad));
  });
});

describe('createTimeFormat', () => {
  const t = Date.UTC(2026, 10, 27, 9, 5); // 14:05 in Astana, 04:05 in New York

  it('writes the clock in 24 hours, in the time zone it is given', () => {
    assert.equal(createTimeFormat('UTC').clock(t), '09:05');
    assert.equal(createTimeFormat('Asia/Almaty').clock(t), '14:05');
    assert.equal(createTimeFormat('America/New_York').clock(t), '04:05');
  });

  it('writes midnight as 00:00, not 24:00', () => {
    assert.equal(createTimeFormat('UTC').clock(Date.UTC(2026, 10, 27, 0, 0)), '00:00');
  });

  it('writes the day with the month in letters: the same on every phone', () => {
    assert.equal(createTimeFormat('UTC').day(t), 'Nov 27');
    assert.equal(createTimeFormat('Asia/Almaty').day(Date.UTC(2026, 10, 27, 20, 0)), 'Nov 28');
  });

  it('writes a moment as the day and the clock, or only the day for a window that is a day long', () => {
    const format = createTimeFormat('UTC');
    assert.equal(format.moment(t), 'Nov 27, 09:05');
    assert.equal(format.moment(t, 60_000), 'Nov 27, 09:05');
    assert.equal(format.moment(t, 3 * 3_600_000), 'Nov 27, 09:05');
    assert.equal(format.moment(t, 86_400_000), 'Nov 27');
    assert.equal(format.moment(t, 2 * 86_400_000), 'Nov 27');
  });
});
