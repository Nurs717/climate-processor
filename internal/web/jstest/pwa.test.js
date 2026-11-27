import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

import { UPDATE_EVERY_MS, startShell, updateDue } from '../static/pwa.js';

const MINUTE = 60_000;

describe('updateDue', () => {
  it('is due when there was no check', () => {
    assert.equal(updateDue(null, 0), true);
  });

  it('is not due soon after a check', () => {
    assert.equal(updateDue(1000, 1000), false);
    assert.equal(updateDue(1000, 1000 + UPDATE_EVERY_MS - 1), false);
  });

  it('is due an hour after a check', () => {
    assert.equal(UPDATE_EVERY_MS, 60 * MINUTE);
    assert.equal(updateDue(1000, 1000 + UPDATE_EVERY_MS), true);
    assert.equal(updateDue(1000, 1000 + 5 * UPDATE_EVERY_MS), true);
  });
});

/** A page with a service worker container. `controller` is what controls the page when it opens. */
function setup({ controller = null, registered = true, updateFails = false } = {}) {
  const listeners = {};
  const registration = {
    updates: 0,
    update() {
      this.updates += 1;
      return updateFails ? Promise.reject(new Error('offline')) : Promise.resolve();
    },
  };
  const container = {
    controller,
    registered: [],
    addEventListener: (type, fn) => {
      listeners[type] = fn;
    },
    register(path) {
      this.registered.push(path);
      return registered ? Promise.resolve(registration) : Promise.reject(new Error('no'));
    },
  };
  let now = 0;
  let loaded = () => {};
  let reloads = 0;
  const shell = startShell(container, {
    now: () => now,
    reload: () => {
      reloads += 1;
    },
    whenLoaded: (run) => {
      loaded = run;
    },
  });
  const settle = () => new Promise((resolve) => setImmediate(resolve));
  return {
    shell,
    container,
    registration,
    reloads: () => reloads,
    advance: (ms) => {
      now += ms;
    },
    /** The page finishes loading, and the registration is answered. */
    async load() {
      loaded();
      await settle();
    },
    /** A worker takes over the page. */
    takeOver: () => listeners.controllerchange(),
    async comeBack() {
      shell.comeBack();
      await settle();
    },
  };
}

describe('startShell', () => {
  it('does nothing, and does not fail, in a browser without service workers', () => {
    const shell = startShell(undefined, {
      now: () => 0,
      reload: () => assert.fail('reloaded'),
      whenLoaded: () => assert.fail('waited for the page'),
    });

    shell.comeBack();
  });

  it('registers the worker once the page has loaded, and not before', async () => {
    const page = setup();
    assert.deepEqual(page.container.registered, []);

    await page.load();

    assert.deepEqual(page.container.registered, ['/sw.js']);
  });

  it('goes on without a worker when it cannot be registered', async () => {
    const page = setup({ registered: false });
    await page.load();
    page.advance(2 * UPDATE_EVERY_MS);

    await page.comeBack();

    assert.equal(page.registration.updates, 0);
    assert.equal(page.reloads(), 0);
  });

  describe('a newer build', () => {
    it('does not replace the page for the first worker of a page that had none', async () => {
      const page = setup({ controller: null });
      await page.load();

      page.takeOver();
      await page.comeBack();

      assert.equal(page.reloads(), 0);
    });

    it('does not replace the page while it is being read', async () => {
      const page = setup({ controller: {} });
      await page.load();

      page.takeOver();

      assert.equal(page.reloads(), 0);
    });

    it('replaces the page when it comes back after another worker has taken over', async () => {
      const page = setup({ controller: {} });
      await page.load();
      page.takeOver();

      await page.comeBack();

      assert.equal(page.reloads(), 1);
    });

    it('asks for the reload once', async () => {
      const page = setup({ controller: {} });
      await page.load();
      page.takeOver();

      await page.comeBack();
      await page.comeBack();

      assert.equal(page.reloads(), 1);
    });

    it('counts the second worker of a visit as an update, though the first one was the first', async () => {
      const page = setup({ controller: null });
      await page.load();
      page.takeOver(); // the first worker, no update
      await page.comeBack();
      assert.equal(page.reloads(), 0);

      page.takeOver(); // the next one is

      await page.comeBack();
      assert.equal(page.reloads(), 1);
    });
  });

  describe('looking for a newer build', () => {
    it('asks when the page comes back, but not more often than once an hour', async () => {
      const page = setup();
      await page.load(); // checked at 0 by the registration itself

      page.advance(30 * MINUTE);
      await page.comeBack();
      assert.equal(page.registration.updates, 0);

      page.advance(30 * MINUTE);
      await page.comeBack();
      assert.equal(page.registration.updates, 1);

      page.advance(MINUTE);
      await page.comeBack();
      assert.equal(page.registration.updates, 1);

      page.advance(60 * MINUTE);
      await page.comeBack();
      assert.equal(page.registration.updates, 2);
    });

    it('does not ask before the worker is registered', async () => {
      const page = setup();
      page.advance(2 * UPDATE_EVERY_MS);

      await page.comeBack();

      assert.equal(page.registration.updates, 0);
    });

    it('does not mind an answer that is an error (it is most likely no network)', async () => {
      const page = setup({ updateFails: true });
      await page.load();
      page.advance(UPDATE_EVERY_MS);

      await page.comeBack(); // an unhandled rejection would fail the run

      assert.equal(page.registration.updates, 1);
    });
  });
});
