// The service worker of the app. It keeps the files of the app (the shell) on the device, so that an installed app opens
// at once and also opens without a network. It never keeps data: a temperature taken from a cache would be a lie, so
// everything under /api/ goes to the server, as if there were no worker at all.
//
// This file is not served as it is written here. When the server starts, it puts two constants in front of it (see
// web.go): SHELL_VERSION, which is another one for every build, and SHELL_FILES, the paths of the files of the shell.
// A new build therefore gives the browser a new worker. That worker fills a cache of its own, takes over, and then
// throws the caches of the older builds away. Nothing is ever a mix of two builds.

/* global SHELL_VERSION, SHELL_FILES */

const CACHE_PREFIX = 'climate-shell-';
const CACHE = CACHE_PREFIX + SHELL_VERSION;
const SHELL = new Set(SHELL_FILES);

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches
      .open(CACHE)
      // One file that cannot be had fails the whole install, and the worker that is in charge stays in charge.
      // "reload" takes the files from the server and not from the HTTP cache of the browser.
      .then((cache) => cache.addAll(SHELL_FILES.map((path) => new Request(path, { cache: 'reload' }))))
      // Waiting for every window of the app to close would never end for an app that lives on a phone.
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((names) =>
        Promise.all(names.filter((name) => name.startsWith(CACHE_PREFIX) && name !== CACHE).map((name) => caches.delete(name))),
      )
      // The page that was opened before the first worker existed gets it now, and works offline from the first visit.
      .then(() => self.clients.claim()),
  );
});

self.addEventListener('fetch', (event) => {
  const { request } = event;
  if (request.method !== 'GET') return;
  const url = new URL(request.url);
  // Not answering is how a worker lets the browser go to the network by itself. That is what the API, the sign-in
  // (it leaves for Telegram and comes back) and everything on another origin need.
  if (url.origin !== self.location.origin || !SHELL.has(url.pathname)) return;
  event.respondWith(fromShell(url.pathname, request));
});

// The path alone is the key: the page is the same with any query (/?signin=expired is the page too).
async function fromShell(path, request) {
  // Only this build's cache is asked, and it is not opened, which would make an empty one. A file that is missing
  // (the browser is free to clear its caches) comes from the network.
  const kept = await caches.match(path, { cacheName: CACHE, ignoreVary: true });
  return kept ?? fetch(request);
}
