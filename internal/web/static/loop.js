// A task that repeats: it runs, says how long to wait, and runs again. A hidden tab asks for nothing.
// The clock and the timers are passed in, so that the tests can drive them.

/**
 * @param {() => Promise<number>|number} run does one round of the work and returns the milliseconds to wait before
 *   the next one. It handles its own errors; if it throws anyway, the loop waits fallbackMs.
 * @param {object} [options]
 * @param {() => boolean} [options.visible] whether the page is being looked at
 * @param {() => number} [options.now] milliseconds on a clock that only moves forward
 * @param {number|(() => number)} [options.staleAfterMs] when a tab that comes back to the front asks again at once
 *   (it waits for the rest of the delay when the last round is younger than this)
 * @param {number} [options.fallbackMs]
 * @param {typeof setTimeout} [options.setTimer]
 * @param {typeof clearTimeout} [options.clearTimer]
 */
export function createLoop(run, options = {}) {
  const {
    visible = () => true,
    now = () => performance.now(),
    staleAfterMs = 0,
    fallbackMs = 60_000,
    setTimer = setTimeout,
    clearTimer = clearTimeout,
  } = options;

  let timer = 0;
  let delay = 0;
  let lastStart = -Infinity;
  let generation = 0; // a round that is no longer the latest one does not get to schedule the next
  let pending = false;

  const stale = () => (typeof staleAfterMs === 'function' ? staleAfterMs() : staleAfterMs);

  function schedule(ms) {
    clearTimer(timer);
    timer = 0;
    if (visible()) timer = setTimer(tick, ms);
  }

  async function tick() {
    clearTimer(timer);
    timer = 0;
    const id = (generation += 1);
    pending = true;
    lastStart = now();
    let next;
    try {
      next = await run();
    } catch {
      next = fallbackMs;
    }
    if (id !== generation) return;
    pending = false;
    delay = Number.isFinite(next) && next >= 0 ? next : fallbackMs;
    schedule(delay);
  }

  return {
    /** Runs now, unless a round is already under way. */
    start() {
      if (!pending) tick();
    },

    /** Runs now. A round that is under way is given up: its answer is no longer wanted (run decides that itself). */
    restart() {
      tick();
    },

    /** The page is hidden. */
    pause() {
      clearTimer(timer);
      timer = 0;
    },

    /** The page is back: asks again if the last answer is old, otherwise waits for the rest of the delay. */
    resume() {
      if (pending) return; // the round that is under way schedules the next one itself
      const elapsed = now() - lastStart;
      if (elapsed >= stale()) tick();
      else schedule(Math.max(0, delay - elapsed));
    },
  };
}
