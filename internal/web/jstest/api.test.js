import { afterEach, describe, it, mock } from 'node:test';
import assert from 'node:assert/strict';

import {
  ApiError,
  getLatest,
  getMe,
  getReadings,
  getStats,
  parseLatest,
  parseReadings,
  parseRetryAfter,
  parseStats,
  signOut,
} from '../static/api.js';

afterEach(() => mock.restoreAll());

const BODY = {
  device_id: 'room',
  time: '2026-11-27T10:48:50Z',
  age_seconds: 7,
  stale: false,
  temperature_c: 22.43,
  humidity_percent: 45.2,
  pressure_pa: 101325,
};

const answer = (body, init = {}) =>
  new Response(typeof body === 'string' ? body : JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
    ...init,
  });

/** Replaces fetch with a function that returns what the test gives it, and returns the mock to inspect the calls. */
const serverSays = (reply) => mock.method(globalThis, 'fetch', async () => reply());

const isApiError = (kind, extra = {}) => (err) => {
  assert.ok(err instanceof ApiError, `expected an ApiError, got ${err}`);
  assert.equal(err.kind, kind);
  for (const [key, value] of Object.entries(extra)) assert.equal(err[key], value, key);
  return true;
};

describe('parseRetryAfter', () => {
  it('reads seconds and returns milliseconds', () => {
    assert.equal(parseRetryAfter('20'), 20_000);
    assert.equal(parseRetryAfter('10.2'), 11_000);
  });

  it('keeps the wait within bounds', () => {
    assert.equal(parseRetryAfter('0'), 5_000);
    assert.equal(parseRetryAfter('1'), 5_000);
    assert.equal(parseRetryAfter('9999'), 120_000);
  });

  it('falls back to a default for anything that is not a number of seconds', () => {
    for (const bad of [null, undefined, '', 'soon', '-5', 'Infinity', 'Wed, 21 Oct 2026 07:28:00 GMT']) {
      assert.equal(parseRetryAfter(bad), 30_000, String(bad));
    }
  });
});

describe('parseLatest', () => {
  it('turns the answer of the API into what the page uses', () => {
    assert.deepEqual(parseLatest(BODY), {
      deviceId: 'room',
      time: '2026-11-27T10:48:50Z',
      ageSeconds: 7,
      stale: false,
      temperature: 22.43,
      humidity: 45.2,
      pressure: 101325,
    });
  });

  it('passes the staleness on, and only for a real true', () => {
    assert.equal(parseLatest({ ...BODY, stale: true }).stale, true);
    assert.equal(parseLatest({ ...BODY, stale: 'true' }).stale, false);
    assert.equal(parseLatest({ ...BODY, stale: undefined }).stale, false);
  });

  it('copes with the optional parts missing or odd', () => {
    const latest = parseLatest({ ...BODY, device_id: 7, time: null, age_seconds: undefined });
    assert.equal(latest.deviceId, '');
    assert.equal(latest.time, '');
    assert.equal(latest.ageSeconds, 0);
    assert.equal(parseLatest({ ...BODY, age_seconds: -3 }).ageSeconds, 0);
  });

  it('refuses a reading that is not made of numbers', () => {
    const broken = [
      null,
      undefined,
      {},
      [],
      { ...BODY, temperature_c: undefined },
      { ...BODY, temperature_c: '22.4' },
      { ...BODY, humidity_percent: null },
      { ...BODY, pressure_pa: NaN },
      { ...BODY, pressure_pa: Infinity },
    ];
    for (const body of broken) assert.throws(() => parseLatest(body), isApiError('server'), JSON.stringify(body));
  });
});

describe('getLatest', () => {
  it('asks the API the way the page needs it', async () => {
    const fetchMock = serverSays(() => answer(BODY));
    await getLatest();

    assert.equal(fetchMock.mock.callCount(), 1);
    const [path, init] = fetchMock.mock.calls[0].arguments;
    assert.equal(path, '/api/v1/latest');
    assert.equal(init.credentials, 'same-origin');
    assert.equal(init.cache, 'no-store');
    assert.equal(init.headers.Accept, 'application/json');
    assert.ok(init.signal instanceof AbortSignal);
  });

  it('returns the reading', async () => {
    serverSays(() => answer(BODY));
    assert.equal((await getLatest()).temperature, 22.43);
  });

  it('says "empty" when the sensor has not reported yet (404)', async () => {
    serverSays(() => answer({ error: { code: 'not_found' } }, { status: 404 }));
    await assert.rejects(getLatest(), isApiError('empty', { status: 404 }));
  });

  it('says "limited" for 429, with the wait the server asked for', async () => {
    serverSays(() => answer({ error: { code: 'rate_limited' } }, { status: 429, headers: { 'Retry-After': '20' } }));
    await assert.rejects(getLatest(), isApiError('limited', { status: 429, retryAfterMs: 20_000 }));
  });

  it('uses a default wait when a 429 comes without Retry-After', async () => {
    serverSays(() => answer('{}', { status: 429 }));
    await assert.rejects(getLatest(), isApiError('limited', { retryAfterMs: 30_000 }));
  });

  it('says "server" for any other failure', async () => {
    for (const status of [400, 401, 403, 500, 502, 503]) {
      serverSays(() => answer('{}', { status }));
      await assert.rejects(getLatest(), isApiError('server', { status }), String(status));
      mock.restoreAll();
    }
  });

  it('says "server" for an answer that is not JSON, or not a reading', async () => {
    serverSays(() => answer('<html>bad gateway</html>'));
    await assert.rejects(getLatest(), isApiError('server'));
    mock.restoreAll();

    serverSays(() => answer('null'));
    await assert.rejects(getLatest(), isApiError('server'));
    mock.restoreAll();

    serverSays(() => answer({ ...BODY, temperature_c: 'warm' }));
    await assert.rejects(getLatest(), isApiError('server'));
  });

  it('does not leave its timeout running once it has an answer, or a failure', async () => {
    serverSays(() => answer(BODY));
    const clear = mock.method(globalThis, 'clearTimeout');
    await getLatest();
    assert.equal(clear.mock.callCount(), 1);
    mock.restoreAll();

    serverSays(() => answer('{}', { status: 500 }));
    const clearAgain = mock.method(globalThis, 'clearTimeout');
    await assert.rejects(getLatest(), isApiError('server'));
    assert.equal(clearAgain.mock.callCount(), 1);
  });

  it('says "network" when the server cannot be reached', async () => {
    mock.method(globalThis, 'fetch', async () => {
      throw new TypeError('Failed to fetch');
    });
    await assert.rejects(getLatest(), isApiError('network'));
  });

  it('gives up after the timeout and says "network"', async () => {
    mock.method(
      globalThis,
      'fetch',
      (_path, init) =>
        new Promise((_resolve, reject) => {
          init.signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
        }),
    );
    const started = Date.now();
    await assert.rejects(getLatest({ timeoutMs: 30 }), isApiError('network'));
    assert.ok(Date.now() - started < 2000, 'it waited far longer than the timeout');
  });
});

describe('getMe', () => {
  it('returns the role and whether signing in is possible', async () => {
    serverSays(() => answer({ role: 'owner', user_id: 'tg:1', sign_in_enabled: true }));
    assert.deepEqual(await getMe(), { role: 'owner', signInEnabled: true });
  });

  it('treats a missing sign_in_enabled as off, and so is anything but a real true', async () => {
    for (const flag of [undefined, null, false, 'yes', 1]) {
      serverSays(() => answer({ role: 'demo', sign_in_enabled: flag }));
      assert.deepEqual(await getMe(), { role: 'demo', signInEnabled: false }, String(flag));
      mock.restoreAll();
    }
  });

  it('refuses an answer without a role', async () => {
    for (const body of [{}, { role: 7 }, null]) {
      serverSays(() => answer(body));
      await assert.rejects(getMe(), isApiError('server'), JSON.stringify(body));
      mock.restoreAll();
    }
  });

  it('refuses a 404, and passes a 429 on', async () => {
    serverSays(() => answer('{}', { status: 404 }));
    await assert.rejects(getMe(), isApiError('server', { status: 404 }));
    mock.restoreAll();

    serverSays(() => answer('{}', { status: 429, headers: { 'Retry-After': '9' } }));
    await assert.rejects(getMe(), isApiError('limited', { retryAfterMs: 9_000 }));
  });

  it('asks again after a 401: the session has ended, its cookie is gone, and the browser is a visitor now', async () => {
    const replies = [
      () => answer({ error: { code: 'unauthorized' } }, { status: 401 }),
      () => answer({ role: 'demo', sign_in_enabled: true }),
    ];
    const fetchMock = serverSays(() => replies.shift()());

    assert.deepEqual(await getMe(), { role: 'demo', signInEnabled: true });
    assert.equal(fetchMock.mock.callCount(), 2);
  });

  it('asks only once more: a second 401 is an answer', async () => {
    const fetchMock = serverSays(() => answer('{}', { status: 401 }));

    await assert.rejects(getMe(), isApiError('server', { status: 401 }));
    assert.equal(fetchMock.mock.callCount(), 2);
  });

  it('does not ask again after any other failure', async () => {
    for (const status of [403, 500, 503]) {
      const fetchMock = serverSays(() => answer('{}', { status }));
      await assert.rejects(getMe(), isApiError('server', { status }), String(status));
      assert.equal(fetchMock.mock.callCount(), 1, String(status));
      mock.restoreAll();
    }
    const fetchMock = mock.method(globalThis, 'fetch', async () => {
      throw new TypeError('Failed to fetch');
    });
    await assert.rejects(getMe(), isApiError('network'));
    assert.equal(fetchMock.mock.callCount(), 1);
  });
});

describe('signOut', () => {
  it('posts to the logout route of the server, with the cookie', async () => {
    const fetchMock = serverSays(() => new Response(null, { status: 204 }));

    assert.equal(await signOut(), undefined);

    const [path, init] = fetchMock.mock.calls[0].arguments;
    assert.equal(path, '/api/v1/auth/logout');
    assert.equal(init.method, 'POST');
    assert.equal(init.credentials, 'same-origin');
    assert.equal(init.cache, 'no-store');
    assert.ok(init.signal instanceof AbortSignal);
    assert.equal(fetchMock.mock.callCount(), 1);
  });

  it('does not need a body in the answer, and takes any 2xx for done', async () => {
    for (const status of [200, 204]) {
      serverSays(() => new Response(status === 200 ? 'not json' : null, { status }));
      await assert.doesNotReject(signOut(), String(status));
      mock.restoreAll();
    }
  });

  it('fails when the server cannot end the session, so that the person can try again', async () => {
    for (const status of [500, 503, 401]) {
      serverSays(() => answer({ error: { code: 'unavailable' } }, { status }));
      await assert.rejects(signOut(), isApiError('server', { status }), String(status));
      mock.restoreAll();
    }
  });

  it('fails when the server has no sign-in at all (a 404 is not "done")', async () => {
    serverSays(() => answer('{}', { status: 404 }));
    await assert.rejects(signOut(), isApiError('server', { status: 404 }));
  });

  it('tells a busy server from an unreachable one', async () => {
    serverSays(() => answer('{}', { status: 429, headers: { 'Retry-After': '12' } }));
    await assert.rejects(signOut(), isApiError('limited', { retryAfterMs: 12_000 }));
    mock.restoreAll();

    mock.method(globalThis, 'fetch', async () => {
      throw new TypeError('Failed to fetch');
    });
    await assert.rejects(signOut(), isApiError('network'));
  });

  it('gives up when the server does not answer in time', async () => {
    mock.method(
      globalThis,
      'fetch',
      (_path, init) =>
        new Promise((_resolve, reject) => {
          init.signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
        }),
    );
    await assert.rejects(signOut({ timeoutMs: 20 }), isApiError('network'));
  });
});

const READINGS = {
  from: '2026-11-26T10:00:00Z',
  to: '2026-11-27T10:00:01Z',
  step_seconds: 300,
  points: [
    {
      time: '2026-11-27T09:55:00Z',
      count: 60,
      temperature_c: { avg: 22.4, min: 22.1, max: 22.7 },
      humidity_percent: { avg: 45, min: 44, max: 46 },
      pressure_pa: { avg: 101325, min: 101300, max: 101350 },
    },
    {
      time: '2026-11-27T09:50:00Z',
      count: 58,
      temperature_c: { avg: 22.3, min: 22, max: 22.6 },
      humidity_percent: { avg: 45.5, min: 45, max: 46 },
      pressure_pa: { avg: 101320, min: 101300, max: 101340 },
    },
  ],
};

describe('parseReadings', () => {
  it('turns the answer into windows of milliseconds, oldest first', () => {
    const readings = parseReadings(READINGS);
    assert.equal(readings.from, Date.parse('2026-11-26T10:00:00Z'));
    assert.equal(readings.to, Date.parse('2026-11-27T10:00:01Z'));
    assert.equal(readings.stepMs, 300_000);
    assert.deepEqual(readings.points.map((p) => p.t), [Date.parse('2026-11-27T09:50:00Z'), Date.parse('2026-11-27T09:55:00Z')]);
    assert.deepEqual(readings.points[1], {
      t: Date.parse('2026-11-27T09:55:00Z'),
      count: 60,
      temperature: { avg: 22.4, min: 22.1, max: 22.7 },
      humidity: { avg: 45, min: 44, max: 46 },
      pressure: { avg: 101325, min: 101300, max: 101350 },
    });
  });

  it('accepts a range without readings', () => {
    assert.deepEqual(parseReadings({ ...READINGS, points: [] }).points, []);
  });

  it('counts a window with an odd count as one reading', () => {
    const body = { ...READINGS, points: [{ ...READINGS.points[0], count: undefined }, { ...READINGS.points[1], count: 0 }] };
    assert.deepEqual(parseReadings(body).points.map((p) => p.count), [1, 1]);
  });

  it('refuses an answer that is not a series', () => {
    const bad = [
      null,
      {},
      'text',
      { ...READINGS, points: null },
      { ...READINGS, step_seconds: 0 },
      { ...READINGS, step_seconds: '300' },
      { ...READINGS, from: 'yesterday' },
      { ...READINGS, to: undefined },
      { ...READINGS, points: [{ ...READINGS.points[0], time: 'soon' }] },
      { ...READINGS, points: [{ ...READINGS.points[0], temperature_c: { avg: 22, min: 21 } }] },
      { ...READINGS, points: [{ ...READINGS.points[0], humidity_percent: null }] },
      { ...READINGS, points: [{ ...READINGS.points[0], pressure_pa: { avg: NaN, min: 1, max: 2 } }] },
      { ...READINGS, points: [null] },
    ];
    for (const body of bad) assert.throws(() => parseReadings(body), isApiError('server'), JSON.stringify(body)?.slice(0, 60));
  });
});

describe('parseStats', () => {
  const STATS = {
    from: '2026-11-26T10:00:00Z',
    to: '2026-11-27T10:00:01Z',
    count: 17000,
    temperature_c: { avg: 22.4, min: 21.8, max: 23.1 },
    humidity_percent: { avg: 42, min: 36, max: 48 },
    pressure_pa: { avg: 101325, min: 100900, max: 101700 },
  };

  it('gives the three measurements', () => {
    assert.deepEqual(parseStats(STATS), {
      count: 17000,
      temperature: { avg: 22.4, min: 21.8, max: 23.1 },
      humidity: { avg: 42, min: 36, max: 48 },
      pressure: { avg: 101325, min: 100900, max: 101700 },
    });
  });

  it('gives null for a range without readings', () => {
    assert.equal(parseStats({ from: STATS.from, to: STATS.to, count: 0 }), null);
  });

  it('refuses what is not an aggregate', () => {
    for (const bad of [null, {}, { count: -1 }, { count: 'many' }, { ...STATS, temperature_c: undefined }, { ...STATS, pressure_pa: { avg: 1 } }]) {
      assert.throws(() => parseStats(bad), isApiError('server'), JSON.stringify(bad));
    }
  });
});

describe('getReadings', () => {
  it('asks for the range, and for nothing else: the server chooses the step', async () => {
    const fetch = serverSays(() => answer(READINGS));
    await getReadings('7d');

    const [path, init] = fetch.mock.calls[0].arguments;
    assert.equal(path, '/api/v1/readings?range=7d');
    assert.equal(init.cache, 'no-store');
    assert.equal(init.credentials, 'same-origin');
    assert.equal(init.headers.Accept, 'application/json');
  });

  it('escapes the range', async () => {
    const fetch = serverSays(() => answer(READINGS));
    await getReadings('7d&step=1s');
    assert.equal(fetch.mock.calls[0].arguments[0], '/api/v1/readings?range=7d%26step%3D1s');
  });

  it('returns the series', async () => {
    serverSays(() => answer(READINGS));
    const readings = await getReadings('24h');
    assert.equal(readings.points.length, 2);
    assert.equal(readings.stepMs, 300_000);
  });

  it('says "limited" for 429, with the wait the server asked for', async () => {
    serverSays(() => answer('', { status: 429, headers: { 'Retry-After': '20' } }));
    await assert.rejects(getReadings('24h'), isApiError('limited', { retryAfterMs: 20_000, status: 429 }));
  });

  it('says "server" for a 404, for any other failure and for an answer that is not a series', async () => {
    for (const status of [404, 400, 500, 503]) {
      serverSays(() => answer('{}', { status }));
      await assert.rejects(getReadings('24h'), isApiError('server'), String(status));
      mock.restoreAll();
    }
    serverSays(() => answer({ nope: true }));
    await assert.rejects(getReadings('24h'), isApiError('server'));
  });

  it('says "network" when the server cannot be reached', async () => {
    mock.method(globalThis, 'fetch', async () => {
      throw new TypeError('Failed to fetch');
    });
    await assert.rejects(getReadings('24h'), isApiError('network'));
  });

  it('gives up when the caller does: the request is aborted, and it ends as "network"', async () => {
    let seen;
    mock.method(globalThis, 'fetch', (_path, { signal }) => {
      seen = signal;
      return new Promise((_resolve, reject) => signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError'))));
    });
    const controller = new AbortController();
    const result = getReadings('24h', { signal: controller.signal });
    controller.abort();

    await assert.rejects(result, isApiError('network'));
    assert.equal(seen.aborted, true);
  });

  it('does not even ask when the caller has already given up', async () => {
    mock.method(globalThis, 'fetch', (_path, { signal }) =>
      signal.aborted ? Promise.reject(new DOMException('Aborted', 'AbortError')) : Promise.resolve(answer(READINGS)),
    );
    const controller = new AbortController();
    controller.abort();
    await assert.rejects(getReadings('24h', { signal: controller.signal }), isApiError('network'));
  });

  it('stops listening to the signal of the caller when it is done, and stops its timer', async () => {
    serverSays(() => answer(READINGS));
    const controller = new AbortController();
    const removed = mock.method(controller.signal, 'removeEventListener');
    const cleared = mock.method(globalThis, 'clearTimeout');
    await getReadings('24h', { signal: controller.signal });

    assert.equal(removed.mock.calls.length, 1);
    assert.equal(removed.mock.calls[0].arguments[0], 'abort');
    assert.equal(cleared.mock.calls.length, 1);
  });
});

describe('getStats', () => {
  it('asks for the range', async () => {
    const fetch = serverSays(() =>
      answer({ count: 1, temperature_c: { avg: 1, min: 1, max: 1 }, humidity_percent: { avg: 1, min: 1, max: 1 }, pressure_pa: { avg: 1, min: 1, max: 1 } }),
    );
    const stats = await getStats('24h');
    assert.equal(fetch.mock.calls[0].arguments[0], '/api/v1/stats?range=24h');
    assert.equal(stats.count, 1);
  });

  it('escapes the range', async () => {
    const fetch = serverSays(() => answer({ count: 0 }));
    await getStats('24h&x=1');
    assert.equal(fetch.mock.calls[0].arguments[0], '/api/v1/stats?range=24h%26x%3D1');
  });

  it('returns null for a range without readings', async () => {
    serverSays(() => answer({ count: 0 }));
    assert.equal(await getStats('24h'), null);
  });

  it('says "limited" for 429, and "server" for a 404', async () => {
    serverSays(() => answer('', { status: 429 }));
    await assert.rejects(getStats('24h'), isApiError('limited', { retryAfterMs: 30_000 }));
    mock.restoreAll();
    serverSays(() => answer('{}', { status: 404 }));
    await assert.rejects(getStats('24h'), isApiError('server'));
  });
});
