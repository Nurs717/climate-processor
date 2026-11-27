// Checks on the files of the app as they are, without running them: what the page must never contain.

import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const staticDir = new URL('../static/', import.meta.url);
const scripts = readdirSync(staticDir).filter((name) => name.endsWith('.js'));

// What would let text turn into markup or code. The page puts everything in with textContent, and its policy
// (CSP) forbids inline code, so none of this has a place here.
const FORBIDDEN = [
  ['innerHTML', /\.innerHTML\b/],
  ['outerHTML', /\.outerHTML\b/],
  ['insertAdjacentHTML', /\binsertAdjacentHTML\b/],
  ['document.write', /\bdocument\.write(ln)?\s*\(/],
  ['eval', /\beval\s*\(/],
  ['new Function', /\bnew\s+Function\b/],
  ['a string given to setTimeout or setInterval', /\bset(?:Timeout|Interval)\s*\(\s*['"`]/],
];

describe('index.html', () => {
  const html = readFileSync(new URL('index.html', staticDir), 'utf8');
  const ids = new Set([...html.matchAll(/\sid="([^"]+)"/g)].map((match) => match[1]));

  it('has every element the scripts look for by a fixed id', () => {
    // The view tests use a page of their own: this is what keeps it the same as the real one.
    const wanted = new Set();
    for (const name of scripts) {
      const source = readFileSync(new URL(name, staticDir), 'utf8');
      for (const match of source.matchAll(/\b(?:byId|getElementById)\('([^']+)'\)/g)) wanted.add(match[1]);
    }
    assert.ok(wanted.size > 10, `found only ${[...wanted].join(', ')}`);
    for (const id of wanted) assert.ok(ids.has(id), `no element #${id} in index.html`);
  });

  it('has no id twice', () => {
    const all = [...html.matchAll(/\sid="([^"]+)"/g)].map((match) => match[1]);
    assert.equal(all.length, ids.size);
  });

  it('sends the sign-in link to the Telegram sign-in of the server', () => {
    assert.match(html, /<a [^>]*id="sign-in"[^>]*href="\/api\/v1\/auth\/telegram\/login"/);
  });

  it('preloads every script of the app', () => {
    for (const name of scripts) {
      if (name === 'app.js') continue; // it has its own <script> tag
      assert.ok(html.includes(`<link rel="modulepreload" href="/${name}">`), `${name} is not preloaded`);
    }
  });
});

describe('the scripts of the app', () => {
  it('are there', () => {
    assert.ok(scripts.length >= 5, `found only ${scripts.join(', ')} in ${fileURLToPath(staticDir)}`);
  });

  for (const name of scripts) {
    it(`${name} has no way to turn text into markup or code`, () => {
      const source = readFileSync(new URL(name, staticDir), 'utf8');
      for (const [what, pattern] of FORBIDDEN) {
        assert.doesNotMatch(source, pattern, `${name} uses ${what}`);
      }
    });
  }
});
