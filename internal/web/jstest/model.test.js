import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

import { POLL_MS, describe as describeState, initialState, nextDelay, onFailure, onLatest } from '../static/model.js';

const SECOND = 1000;

const reading = (overrides = {}) => ({
  temperature: 22.43,
  humidity: 45.2,
  pressure: 101325,
  ageSeconds: 7,
  stale: false,
  ...overrides,
});

const VALUES = { temperature: 22.43, humidity: 45.2, pressure: 101325 };
const failure = (kind, retryAfterMs = 0) => ({ kind, retryAfterMs });

describe('before anything has happened', () => {
  it('is loading, with nothing to show', () => {
    assert.deepEqual(describeState(initialState, 0), {
      state: 'loading',
      message: 'Connecting to the sensor…',
      dimmed: false,
      values: null,
    });
  });

  it('is not changed by what happens to it', () => {
    const before = structuredClone(initialState);
    onFailure(initialState, failure('network'), 1000);
    onLatest(initialState, reading(), 1000);
    assert.deepEqual(initialState, before);
    assert.ok(Object.isFrozen(initialState));
  });
});

describe('a fresh reading', () => {
  it('is live, and says how old it is', () => {
    const state = onLatest(initialState, reading({ ageSeconds: 7 }), 1000);
    assert.deepEqual(describeState(state, 1000), {
      state: 'live',
      message: 'Live, updated 7 s ago',
      dimmed: false,
      values: VALUES,
    });
  });

  it('gets older by itself as time passes', () => {
    const state = onLatest(initialState, reading({ ageSeconds: 7 }), 1000);
    assert.equal(describeState(state, 1000 + 10 * SECOND).message, 'Live, updated 17 s ago');
    assert.equal(describeState(state, 1000 + 53 * SECOND).message, 'Live, updated 1 min ago');
  });

  it('says "just now" when it has just come', () => {
    const state = onLatest(initialState, reading({ ageSeconds: 0 }), 5000);
    assert.equal(describeState(state, 5000).message, 'Live, updated just now');
  });

  it('does not get younger if the clock steps back', () => {
    const state = onLatest(initialState, reading({ ageSeconds: 7 }), 5000);
    assert.equal(describeState(state, 1000).message, 'Live, updated 7 s ago');
  });
});

describe('an old reading (the server says it is stale)', () => {
  it('is shown dimmed, with a sentence about the sensor', () => {
    const state = onLatest(initialState, reading({ ageSeconds: 520, stale: true }), 0);
    assert.deepEqual(describeState(state, 0), {
      state: 'stale',
      message: 'Last reading 8 min ago. The sensor may be offline.',
      dimmed: true,
      values: VALUES,
    });
  });
});

describe('the server cannot be reached', () => {
  it('keeps the last reading, dimmed, and keeps counting its age', () => {
    const live = onLatest(initialState, reading({ ageSeconds: 7 }), 0);
    const state = onFailure(live, failure('network'), 15 * SECOND);

    assert.deepEqual(describeState(state, 15 * SECOND), {
      state: 'offline',
      message: "Can't reach the server. Last reading 22 s ago. Retrying…",
      dimmed: true,
      values: VALUES,
    });
    assert.equal(
      describeState(state, 75 * SECOND).message,
      "Can't reach the server. Last reading 1 min ago. Retrying…",
    );
  });

  it('has no reading to keep when it never got one', () => {
    const state = onFailure(initialState, failure('network'), 0);
    assert.deepEqual(describeState(state, 0), {
      state: 'offline',
      message: "Can't reach the server. Retrying…",
      dimmed: false,
      values: null,
    });
  });

  it('treats an answer that is not an answer the same way', () => {
    const state = onFailure(onLatest(initialState, reading(), 0), failure('server'), 1000);
    assert.equal(describeState(state, 1000).state, 'offline');
  });

  it('recovers with the next reading', () => {
    const down = onFailure(onLatest(initialState, reading(), 0), failure('network'), 1000);
    const up = onLatest(down, reading({ temperature: 23.1, ageSeconds: 2 }), 16 * SECOND);
    const shown = describeState(up, 16 * SECOND);
    assert.equal(shown.state, 'live');
    assert.equal(shown.dimmed, false);
    assert.equal(shown.values.temperature, 23.1);
  });
});

describe('the sensor has not reported yet (empty)', () => {
  it('says so and shows no values', () => {
    const state = onFailure(initialState, failure('empty'), 0);
    assert.deepEqual(describeState(state, 0), {
      state: 'empty',
      message: 'No readings yet. Waiting for the sensor to report.',
      dimmed: false,
      values: null,
    });
  });

  it('forgets an older reading: the server now says there is none', () => {
    const state = onFailure(onLatest(initialState, reading(), 0), failure('empty'), 1000);
    assert.equal(describeState(state, 1000).values, null);
  });

  it('ends with the first reading', () => {
    const state = onLatest(onFailure(initialState, failure('empty'), 0), reading(), 15 * SECOND);
    assert.equal(describeState(state, 15 * SECOND).state, 'live');
  });
});

describe('too many requests (limited)', () => {
  it('counts down the wait the server asked for', () => {
    const state = onFailure(onLatest(initialState, reading(), 0), failure('limited', 20 * SECOND), 1000);
    assert.deepEqual(describeState(state, 1000), {
      state: 'limited',
      message: 'Too many requests. Trying again in 20 s.',
      dimmed: true,
      values: VALUES,
    });
    assert.equal(describeState(state, 1000 + 4500).message, 'Too many requests. Trying again in 16 s.');
  });

  it('stops counting when the time is up', () => {
    const state = onFailure(initialState, failure('limited', 20 * SECOND), 0);
    assert.equal(describeState(state, 20 * SECOND).message, 'Too many requests. Trying again…');
    assert.equal(describeState(state, 99 * SECOND).message, 'Too many requests. Trying again…');
  });

  it('has no wait to count when the server named none', () => {
    const state = onFailure(initialState, failure('limited'), 1000);
    assert.equal(describeState(state, 1000).message, 'Too many requests. Trying again…');
  });

  it('without a reading shows no values and does not dim', () => {
    const shown = describeState(onFailure(initialState, failure('limited', 9 * SECOND), 0), 0);
    assert.equal(shown.state, 'limited');
    assert.equal(shown.dimmed, false);
    assert.equal(shown.values, null);
  });

  it('says how long in minutes when the wait is long', () => {
    const state = onFailure(initialState, failure('limited', 120 * SECOND), 0);
    assert.equal(describeState(state, 0).message, 'Too many requests. Trying again in 2 min.');
  });
});

describe('nextDelay', () => {
  it('polls at the normal pace while all is well, and when the sensor is silent', () => {
    assert.equal(POLL_MS, 15_000);
    assert.equal(nextDelay(null, 0), POLL_MS);
    assert.equal(nextDelay(failure('empty'), 1), POLL_MS);
    assert.equal(nextDelay(failure('empty'), 4), POLL_MS); // an empty sensor is not a failing server: no backoff
  });

  it('waits as long as the server asked after a 429', () => {
    assert.equal(nextDelay(failure('limited', 20_000), 1), 20_000);
    assert.equal(nextDelay(failure('limited', 20_000), 5), 20_000);
  });

  it('falls back to the normal pace after a 429 without a wait', () => {
    assert.equal(nextDelay(failure('limited'), 1), POLL_MS);
  });

  it('backs off after failures in a row, up to a minute', () => {
    const delays = [1, 2, 3, 4, 5, 10, 100].map((n) => nextDelay(failure('network'), n));
    assert.deepEqual(delays, [15_000, 30_000, 60_000, 60_000, 60_000, 60_000, 60_000]);
  });

  it('does not wait less than the normal pace after a failure, whatever the count says', () => {
    assert.equal(nextDelay(failure('server'), 0), POLL_MS);
    assert.equal(nextDelay(failure('server'), -3), POLL_MS);
  });
});
