// The service worker, run the way a browser runs it: as one script, with the two constants the server puts in front of
// it, and with a fake of what a worker is given (self, caches, fetch, Request).

import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

const template = readFileSync(new URL('../static/sw.js', import.meta.url), 'utf8');
const ORIGIN = 'https://climate.example';
const FILES = ['/', '/index.html', '/app.js', '/app.css'];

/** A Response, as far as the worker looks at it. */
const reply = (body, status = 200) => ({ status, ok: status >= 200 && status < 300, body });

/** The worker, started. `stores` is the CacheStorage: cache name -> path -> response. */
function boot({ version = 'b1', files = FILES, stores = {}, offline = false, missing = [] } = {}) {
  const log = { opened: [], fetched: [], matched: [], skipWaiting: 0, claimed: 0 };

  async function network(request) {
    const path = new URL(request.url).pathname;
    log.fetched.push({ path, cache: request.cache });
    if (offline) throw new TypeError('Failed to fetch');
    return missing.includes(path) ? reply('', 404) : reply(`network:${path}`);
  }

  const caches = {
    async open(name) {
      log.opened.push(name);
      stores[name] ??= {};
      return {
        // Like the real one: all the files are had first, and when one fails all fail and nothing is kept.
        async addAll(requests) {
          const got = await Promise.all(
            requests.map(async (request) => {
              const response = await network(request);
              if (!response.ok) throw new TypeError(`${request.url}: ${response.status}`);
              return [new URL(request.url).pathname, response];
            }),
          );
          for (const [path, response] of got) stores[name][path] = response;
        },
      };
    },
    async keys() {
      return Object.keys(stores);
    },
    async delete(name) {
      return delete stores[name];
    },
    // Looks in the one cache it is told to, and in no other.
    async match(path, options = {}) {
      log.matched.push({ path, ...options });
      return options.cacheName ? stores[options.cacheName]?.[path] : undefined;
    },
  };

  const handlers = {};
  const self = {
    location: { origin: ORIGIN },
    addEventListener: (type, handler) => {
      handlers[type] = handler;
    },
    skipWaiting: async () => {
      log.skipWaiting += 1;
    },
    clients: {
      claim: async () => {
        log.claimed += 1;
      },
    },
  };
  class Request {
    constructor(path, init = {}) {
      this.url = new URL(path, ORIGIN).href;
      this.method = 'GET';
      this.cache = init.cache ?? 'default';
    }
  }
  const context = vm.createContext({ self, caches, fetch: network, Request, URL });
  const prelude = `const SHELL_VERSION = ${JSON.stringify(version)};\nconst SHELL_FILES = ${JSON.stringify(files)};\n`;
  vm.runInContext(prelude + template, context, { filename: 'sw.js' });

  /** Fires an event and waits for what the worker asked to wait for. `answered` tells whether it took the request. */
  async function dispatch(type, extra = {}) {
    const waits = [];
    let answer;
    handlers[type]({
      ...extra,
      waitUntil: (promise) => waits.push(promise),
      respondWith: (promise) => {
        answer = promise;
      },
    });
    await Promise.all(waits);
    return { answered: answer !== undefined, response: answer === undefined ? undefined : await answer };
  }

  const get = (path, method = 'GET') => dispatch('fetch', { request: { url: ORIGIN + path, method } });
  return { log, stores, dispatch, get, handlers };
}

/** What the cache of the build b1 holds after an install. */
const installed = () => ({
  'climate-shell-b1': {
    '/': reply('kept:/'),
    '/index.html': reply('kept:/index.html'),
    '/app.js': reply('kept:/app.js'),
    '/app.css': reply('kept:/app.css'),
  },
});

describe('the worker', () => {
  it('listens for install, activate and fetch, and for nothing else', () => {
    assert.deepEqual(Object.keys(boot().handlers).sort(), ['activate', 'fetch', 'install']);
  });
});

describe('install', () => {
  it('keeps every file of the shell in a cache named after the build', async () => {
    const stores = {};
    const sw = boot({ version: 'b7', stores });

    await sw.dispatch('install');

    assert.deepEqual(Object.keys(stores), ['climate-shell-b7']);
    assert.deepEqual(Object.keys(stores['climate-shell-b7']).sort(), [...FILES].sort());
  });

  it('takes the files from the server and not from the HTTP cache of the browser', async () => {
    const sw = boot();

    await sw.dispatch('install');

    assert.deepEqual(
      sw.log.fetched.map((f) => f.cache),
      FILES.map(() => 'reload'),
    );
  });

  it('takes over at once, without waiting for the windows of the old worker to close', async () => {
    const sw = boot();

    await sw.dispatch('install');

    assert.equal(sw.log.skipWaiting, 1);
  });

  it('fails as a whole when one file cannot be had: nothing is kept and the old worker stays', async () => {
    const stores = {};
    const sw = boot({ stores, missing: ['/app.css'] });

    await assert.rejects(sw.dispatch('install'));

    assert.deepEqual(stores['climate-shell-b1'], {});
    assert.equal(sw.log.skipWaiting, 0);
  });

  it('fails when the network is down', async () => {
    const sw = boot({ offline: true });

    await assert.rejects(sw.dispatch('install'));

    assert.equal(sw.log.skipWaiting, 0);
  });
});

describe('activate', () => {
  it('throws away the caches of the other builds, and only those', async () => {
    const stores = {
      'climate-shell-b0': { '/': reply('old') },
      ...installed(),
      'something-else': { '/': reply('not ours') },
    };

    await boot({ stores }).dispatch('activate');

    assert.deepEqual(Object.keys(stores).sort(), ['climate-shell-b1', 'something-else']);
  });

  it('takes the pages that are open, so that the first visit works offline too', async () => {
    const sw = boot();

    await sw.dispatch('activate');

    assert.equal(sw.log.claimed, 1);
  });
});

describe('fetch', () => {
  it('answers a file of the shell from the cache, with the network down', async () => {
    const sw = boot({ stores: installed(), offline: true });

    const { answered, response } = await sw.get('/app.js');

    assert.equal(answered, true);
    assert.equal(response.body, 'kept:/app.js');
    assert.equal(sw.log.fetched.length, 0);
  });

  it('answers the page also when the address has a query (the reason of a failed sign-in)', async () => {
    const sw = boot({ stores: installed(), offline: true });

    const { response } = await sw.get('/?signin=expired');

    assert.equal(response.body, 'kept:/');
  });

  it('leaves the API and the sign-in to the browser', async () => {
    const sw = boot({ stores: installed() });
    for (const path of [
      '/api/v1/latest',
      '/api/v1/readings?range=24h',
      '/api/v1/auth/me',
      '/api/v1/auth/telegram/login',
      '/api/v1/auth/telegram/callback?id=1',
    ]) {
      assert.equal((await sw.get(path)).answered, false, `${path} was taken by the worker`);
    }
  });

  it('leaves everything that is not a GET to the browser', async () => {
    const sw = boot({ stores: installed() });
    for (const method of ['POST', 'PUT', 'DELETE']) {
      assert.equal((await sw.get('/app.js', method)).answered, false, method);
    }
  });

  it('leaves other origins alone, whatever the path', async () => {
    const sw = boot({ stores: installed() });

    const { answered } = await sw.dispatch('fetch', { request: { url: 'https://oauth.telegram.org/app.js', method: 'GET' } });

    assert.equal(answered, false);
  });

  it('leaves alone what is not in the shell: the worker itself, and what the app does not have', async () => {
    const sw = boot({ stores: installed() });
    for (const path of ['/sw.js', '/nothing.js', '/app.js/', '/APP.JS']) {
      assert.equal((await sw.get(path)).answered, false, `${path} was taken by the worker`);
    }
  });

  it('asks the network for a file the cache has lost (a browser may clear its caches)', async () => {
    const sw = boot({ stores: { 'climate-shell-b1': {} } });

    const { answered, response } = await sw.get('/app.js');

    assert.equal(answered, true);
    assert.equal(response.body, 'network:/app.js');
  });

  it('fails like the browser would when the file is lost and the network is down', async () => {
    const sw = boot({ stores: { 'climate-shell-b1': {} }, offline: true });

    await assert.rejects(sw.get('/app.js'));
  });

  it('never serves a file of an older build', async () => {
    const sw = boot({ stores: { 'climate-shell-b0': { '/app.js': reply('old') }, 'climate-shell-b1': {} } });

    const { response } = await sw.get('/app.js');

    assert.equal(response.body, 'network:/app.js');
  });

  it('looks a file up by its path alone, whatever the headers of the request say (a server may send Vary)', async () => {
    const sw = boot({ stores: installed() });

    await sw.get('/app.js');

    assert.deepEqual(sw.log.matched, [{ path: '/app.js', cacheName: 'climate-shell-b1', ignoreVary: true }]);
  });

  it('does not open a cache to look in it, which would make an empty one', async () => {
    const sw = boot({ stores: installed() });

    await sw.get('/app.js');

    assert.deepEqual(sw.log.opened, []);
  });
});
