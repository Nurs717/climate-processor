import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

import { createLoop } from '../static/loop.js';

// A clock and timers that only move when the test says so.
function fakeWorld() {
  const world = {
    time: 0,
    seen: true,
    timers: new Map(),
    nextId: 1,
    now: () => world.time,
    visible: () => world.seen,
    setTimer(fn, ms) {
      const id = world.nextId;
      world.nextId += 1;
      world.timers.set(id, { fn, at: world.time + ms });
      return id;
    },
    clearTimer(id) {
      world.timers.delete(id);
    },
    /** Moves time forward and runs the timers that are due, in order. */
    async advance(ms) {
      const end = world.time + ms;
      for (;;) {
        const due = [...world.timers.entries()].filter(([, t]) => t.at <= end).sort((a, b) => a[1].at - b[1].at)[0];
        if (!due) break;
        world.timers.delete(due[0]);
        world.time = due[1].at;
        due[1].fn();
        await flush();
      }
      world.time = end;
    },
    get waiting() {
      return [...world.timers.values()].map((t) => t.at - world.time);
    },
  };
  return world;
}

const flush = () => new Promise((resolve) => setImmediate(resolve));

const loopIn = (world, run, options = {}) =>
  createLoop(run, { visible: world.visible, now: world.now, setTimer: world.setTimer, clearTimer: world.clearTimer, ...options });

describe('createLoop', () => {
  it('runs at once, and again after the time the task asks for', async () => {
    const world = fakeWorld();
    const calls = [];
    const loop = loopIn(world, () => {
      calls.push(world.time);
      return 1000;
    });

    loop.start();
    await flush();
    assert.deepEqual(calls, [0]);
    await world.advance(999);
    assert.deepEqual(calls, [0]);
    await world.advance(1);
    assert.deepEqual(calls, [0, 1000]);
    await world.advance(2000);
    assert.deepEqual(calls, [0, 1000, 2000, 3000]);
  });

  it('takes the delay from every round: a struggling server gets more time', async () => {
    const world = fakeWorld();
    const delays = [1000, 5000, 100];
    const calls = [];
    const loop = loopIn(world, () => {
      calls.push(world.time);
      return delays.shift() ?? 100;
    });
    loop.start();
    await flush();
    await world.advance(1000);
    await world.advance(5000);
    await world.advance(100);
    assert.deepEqual(calls, [0, 1000, 6000, 6100]);
  });

  it('starts the next wait when the round ends, not when it began', async () => {
    const world = fakeWorld();
    let finish;
    const loop = loopIn(world, () => new Promise((resolve) => (finish = resolve)));
    loop.start();
    await flush();
    world.time = 3000; // the request took three seconds
    finish(1000);
    await flush();
    assert.deepEqual(world.waiting, [1000]);
  });

  it('does not start a second round while one is under way', async () => {
    const world = fakeWorld();
    let runs = 0;
    let finish;
    const loop = loopIn(world, () => {
      runs += 1;
      return new Promise((resolve) => (finish = resolve));
    });
    loop.start();
    loop.start();
    loop.resume();
    await flush();
    assert.equal(runs, 1);
    finish(1000);
    await flush();
    assert.equal(world.waiting.length, 1);
  });

  it('waits the fallback time when the task throws, and keeps going', async () => {
    const world = fakeWorld();
    let runs = 0;
    const loop = loopIn(
      world,
      () => {
        runs += 1;
        throw new Error('bug');
      },
      { fallbackMs: 7000 },
    );
    loop.start();
    await flush();
    assert.deepEqual(world.waiting, [7000]);
    await world.advance(7000);
    assert.equal(runs, 2);
  });

  it('waits the fallback time for a delay that is not a number', async () => {
    for (const bad of [undefined, NaN, -5, 'soon']) {
      const world = fakeWorld();
      const loop = loopIn(world, () => bad, { fallbackMs: 4000 });
      loop.start();
      await flush();
      assert.deepEqual(world.waiting, [4000], String(bad));
    }
  });

  describe('a hidden page', () => {
    it('does not set a timer when a round ends while the page is hidden', async () => {
      const world = fakeWorld();
      let finish;
      const loop = loopIn(world, () => new Promise((resolve) => (finish = resolve)));
      loop.start();
      await flush();
      world.seen = false;
      finish(1000);
      await flush();
      assert.deepEqual(world.waiting, []);
    });

    it('stops the timer on pause', async () => {
      const world = fakeWorld();
      let runs = 0;
      const loop = loopIn(world, () => {
        runs += 1;
        return 1000;
      });
      loop.start();
      await flush();
      loop.pause();
      assert.deepEqual(world.waiting, []);
      await world.advance(10_000);
      assert.equal(runs, 1);
    });

    it('asks again at once on return when the last answer is older than staleAfterMs', async () => {
      const world = fakeWorld();
      let runs = 0;
      const loop = loopIn(world, () => {
        runs += 1;
        return 15_000;
      }, { staleAfterMs: 5000 });
      loop.start();
      await flush();
      loop.pause();
      world.time = 6000;
      loop.resume();
      await flush();
      assert.equal(runs, 2);
    });

    it('waits for the rest of the delay on return when the last answer is still fresh', async () => {
      const world = fakeWorld();
      let runs = 0;
      const loop = loopIn(world, () => {
        runs += 1;
        return 15_000;
      }, { staleAfterMs: 5000 });
      loop.start();
      await flush();
      loop.pause();
      world.time = 3000;
      loop.resume();
      await flush();
      assert.equal(runs, 1);
      assert.deepEqual(world.waiting, [12_000]);
    });

    it('can take the age that counts as old from a function: it changes with the range', async () => {
      const world = fakeWorld();
      let old = 60_000;
      let runs = 0;
      const loop = loopIn(world, () => {
        runs += 1;
        return 60_000;
      }, { staleAfterMs: () => old });
      loop.start();
      await flush();
      loop.pause();
      world.time = 100_000;
      old = 300_000;
      loop.resume();
      await flush();
      assert.equal(runs, 1, 'a hundred seconds is not old for a week');
      assert.deepEqual(world.waiting, [0], 'the delay is over: the next round is due');
    });

    it('does nothing on return while a round is under way', async () => {
      const world = fakeWorld();
      let runs = 0;
      const loop = loopIn(world, () => {
        runs += 1;
        return new Promise(() => {});
      });
      loop.start();
      await flush();
      loop.pause();
      loop.resume();
      await flush();
      assert.equal(runs, 1);
      assert.deepEqual(world.waiting, []);
    });
  });

  describe('restart', () => {
    it('runs at once, even in the middle of a round, and drops the old round', async () => {
      const world = fakeWorld();
      const finishers = [];
      const loop = loopIn(world, () => new Promise((resolve) => finishers.push(resolve)));
      loop.start();
      await flush();
      loop.restart();
      await flush();
      assert.equal(finishers.length, 2);

      finishers[0](1000); // the answer that is no longer wanted
      await flush();
      assert.deepEqual(world.waiting, [], 'the old round does not schedule anything');
      assert.equal(finishers.length, 2);

      finishers[1](2000);
      await flush();
      assert.deepEqual(world.waiting, [2000]);
    });

    it('cancels the wait that was running', async () => {
      const world = fakeWorld();
      let runs = 0;
      const loop = loopIn(world, () => {
        runs += 1;
        return 5000;
      });
      loop.start();
      await flush();
      await world.advance(3000);
      loop.restart();
      await flush();
      assert.equal(runs, 2);
      assert.deepEqual(world.waiting, [5000]);
    });

    it('lets start run again after a restarted round has ended', async () => {
      const world = fakeWorld();
      let runs = 0;
      const loop = loopIn(world, () => {
        runs += 1;
        return 5000;
      });
      loop.restart();
      await flush();
      loop.pause();
      loop.start();
      await flush();
      assert.equal(runs, 2);
    });
  });
});
