import { afterEach, describe, it, mock } from 'node:test';
import assert from 'node:assert/strict';

import { ApiError, getLatest, getMe, parseLatest, parseRetryAfter } from '../static/api.js';

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
});
