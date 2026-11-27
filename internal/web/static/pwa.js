// The app as a program on the device: registers the service worker (sw.js) and takes the updates it brings.
//
// A new build gives the browser a new worker, and the worker takes over by itself. The page that is open keeps the files
// it has loaded, and it is replaced by the new one when the person comes back to it: never while it is being read.

export const UPDATE_EVERY_MS = 3_600_000;

/**
 * Whether to ask the server for a newer worker. The browser asks by itself whenever a page is opened; a page that
 * stays open (an installed app does) asks when it comes to the front, and not more often than this.
 * @param {number|null} lastCheck when the last check was made, or null if there was none
 * @param {number} now on the same clock, in milliseconds
 */
export function updateDue(lastCheck, now) {
  return lastCheck === null || now - lastCheck >= UPDATE_EVERY_MS;
}

/**
 * @param {{register: Function, addEventListener: Function, controller: unknown}|undefined} container
 *   navigator.serviceWorker, which is missing in a browser without workers and on a page that is not served over HTTPS:
 *   the app works without a worker, it only does not open offline
 * @param {object} env
 * @param {() => number} env.now milliseconds on a clock that only moves forward
 * @param {() => void} env.reload loads the page again
 * @param {(run: () => void) => void} env.whenLoaded calls run once the page has loaded: the worker has no business with
 *   the files the first view is waiting for
 */
export function startShell(container, { now, reload, whenLoaded }) {
  if (!container) return { comeBack() {} };

  let registration = null;
  let checkedAt = null;
  let replaced = false; // a newer worker has taken over from the one that served this page
  let reloading = false;
  // The first worker takes over a page that had none: that is not an update. The next one is.
  let controlled = Boolean(container.controller);

  container.addEventListener('controllerchange', () => {
    if (controlled) replaced = true;
    controlled = true;
  });

  whenLoaded(() => {
    container.register('/sw.js').then(
      (found) => {
        registration = found;
        checkedAt = now();
      },
      () => {
        // The app works without it.
      },
    );
  });

  return {
    /** The page is back in front: it is replaced when there is a newer build, and a check is made now and then. */
    comeBack() {
      if (replaced) {
        if (!reloading) {
          reloading = true;
          reload();
        }
        return;
      }
      if (registration && updateDue(checkedAt, now())) {
        checkedAt = now();
        registration.update().catch(() => {
          // Offline, most likely. The browser asks again when the page is opened.
        });
      }
    },
  };
}
