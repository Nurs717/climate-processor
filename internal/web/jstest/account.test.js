import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

import {
  ME_REFRESH_MS,
  SIGN_OUT_PROBLEM,
  accountDelay,
  describeAccount,
  initialAccount,
  onMe,
  onSignOutFailed,
  onSignOutStarted,
  onSignedOut,
  readSignIn,
  withNotice,
  withoutSignIn,
} from '../static/account.js';

const owner = { role: 'owner', signInEnabled: true };
const visitor = { role: 'demo', signInEnabled: true };

describe('readSignIn', () => {
  it('has nothing to say about an address without a reason', () => {
    for (const search of ['', '?', '?range=7d', '?signinx=1']) {
      assert.deepEqual(readSignIn(search), { present: false, message: null }, search);
    }
  });

  it('has a sentence for every reason the server sends', () => {
    // The names are the ones of internal/handler/rest/auth.go.
    const reasons = ['cancelled', 'not_owner', 'expired', 'busy', 'unavailable', 'failed'];
    const messages = reasons.map((reason) => {
      const { present, message } = readSignIn(`?signin=${reason}`);
      assert.equal(present, true, reason);
      assert.equal(typeof message, 'string', reason);
      assert.ok(message.length > 10, reason);
      return message;
    });
    assert.equal(new Set(messages).size, reasons.length, 'every reason says something of its own');
  });

  it('says what happened and what to do, in a sentence of its own, without apologising', () => {
    for (const reason of ['cancelled', 'not_owner', 'expired', 'busy', 'unavailable', 'failed']) {
      const { message } = readSignIn(`?signin=${reason}`);
      assert.match(message, /^[A-Z].*\.$/, reason);
      assert.doesNotMatch(message, /sorry|oops|unfortunately/i, reason);
    }
  });

  it('knows the address is carrying a reason even when it does not know the reason', () => {
    // Anybody can write an address: such a reason is taken off the page without a word, and never repeated.
    for (const reason of ['lol', '', 'CANCELLED', ' cancelled', '<script>alert(1)</script>']) {
      assert.deepEqual(readSignIn(`?signin=${encodeURIComponent(reason)}`), { present: true, message: null }, JSON.stringify(reason));
    }
  });

  it('does not take a word of Object.prototype for a reason', () => {
    for (const reason of ['constructor', 'toString', '__proto__', 'hasOwnProperty', 'valueOf']) {
      assert.deepEqual(readSignIn(`?signin=${reason}`), { present: true, message: null }, reason);
    }
  });

  it('reads the first reason when the address has several', () => {
    assert.equal(readSignIn('?signin=busy&signin=failed').message, readSignIn('?signin=busy').message);
  });

  it('finds the reason among other parameters', () => {
    assert.equal(readSignIn('?a=1&signin=cancelled&b=2').message, readSignIn('?signin=cancelled').message);
  });
});

describe('withoutSignIn', () => {
  it('takes the reason off the query string and leaves nothing behind when it was the only parameter', () => {
    assert.equal(withoutSignIn('?signin=cancelled'), '');
    assert.equal(withoutSignIn(''), '');
    assert.equal(withoutSignIn('?'), '');
  });

  it('keeps the other parameters', () => {
    assert.equal(withoutSignIn('?a=1&signin=cancelled&b=2'), '?a=1&b=2');
    assert.equal(withoutSignIn('?x=1'), '?x=1');
  });

  it('takes every copy of the reason', () => {
    assert.equal(withoutSignIn('?signin=busy&signin=failed&a=1'), '?a=1');
  });
});

describe('the account', () => {
  it('starts without knowing who is looking', () => {
    assert.deepEqual(describeAccount(initialAccount), {
      visible: false, role: 'demo', label: '', signIn: false, signOut: 'hidden', notice: null,
    });
  });

  it('offers the owner a way out and nothing else', () => {
    assert.deepEqual(describeAccount(onMe(initialAccount, owner)), {
      visible: true, role: 'owner', label: 'Signed in', signIn: false, signOut: 'ready', notice: null,
    });
  });

  it('offers a visitor the way in, where the server has one', () => {
    assert.deepEqual(describeAccount(onMe(initialAccount, visitor)), {
      visible: true, role: 'demo', label: 'Demo view', signIn: true, signOut: 'hidden', notice: null,
    });
    const without = describeAccount(onMe(initialAccount, { role: 'demo', signInEnabled: false }));
    assert.equal(without.signIn, false);
    assert.equal(without.visible, true);
  });

  it('treats every role but the owner as a visitor', () => {
    for (const role of ['demo', 'member', '', undefined]) {
      const d = describeAccount(onMe(initialAccount, { role, signInEnabled: true }));
      assert.deepEqual([d.role, d.label, d.signIn, d.signOut], ['demo', 'Demo view', true, 'hidden'], String(role));
    }
  });

  it('shows the sentence even before the role is known', () => {
    const d = describeAccount(withNotice(initialAccount, 'Sign-in was cancelled.'));
    assert.equal(d.notice, 'Sign-in was cancelled.');
    assert.equal(d.visible, false);
  });

  it('shows the sentence to a visitor, and to the owner', () => {
    for (const me of [visitor, owner]) {
      assert.equal(describeAccount(withNotice(onMe(initialAccount, me), 'Sign-in was cancelled.')).notice, 'Sign-in was cancelled.');
    }
  });

  it('takes the sentence away', () => {
    const state = withNotice(onMe(initialAccount, visitor), 'Sign-in was cancelled.');
    assert.equal(describeAccount(withNotice(state, null)).notice, null);
  });

  it('keeps the role when a sentence comes and goes, and the sentence when the role is asked again', () => {
    const state = withNotice(onMe(initialAccount, visitor), 'x');
    assert.equal(state.me, visitor);
    assert.equal(onMe(state, owner).notice, 'x');
  });

  it('does not change the state it is given', () => {
    const frozen = Object.freeze({ me: Object.freeze({ ...owner }), signingOut: false, notice: null });
    for (const step of [
      (s) => onMe(s, visitor),
      (s) => withNotice(s, 'x'),
      onSignOutStarted,
      onSignOutFailed,
      onSignedOut,
    ]) {
      assert.doesNotThrow(() => step(frozen));
    }
    assert.equal(frozen.signingOut, false);
  });
});

describe('signing out', () => {
  const signedIn = onMe(initialAccount, owner);

  it('shows that the server is being asked, and clears an old sentence', () => {
    const d = describeAccount(onSignOutStarted(withNotice(signedIn, SIGN_OUT_PROBLEM)));
    assert.equal(d.signOut, 'busy');
    assert.equal(d.notice, null);
    assert.equal(d.role, 'owner'); // still the owner: nothing has happened yet
  });

  it('says so when it did not work, and lets the person try again', () => {
    const d = describeAccount(onSignOutFailed(onSignOutStarted(signedIn)));
    assert.equal(d.notice, SIGN_OUT_PROBLEM);
    assert.equal(d.signOut, 'ready');
    assert.equal(d.role, 'owner'); // the session is alive on the server
  });

  it("makes the owner a visitor when it worked, with the way back in, before /auth/me is asked again", () => {
    const d = describeAccount(onSignedOut(onSignOutStarted(signedIn)));
    assert.deepEqual(d, { visible: true, role: 'demo', label: 'Demo view', signIn: true, signOut: 'hidden', notice: null });
  });

  it('lets the person sign out again when they have signed in again', () => {
    assert.equal(onSignedOut(onSignOutStarted(signedIn)).signingOut, false);
  });

  it('keeps the answer of the server about whether signing in is possible', () => {
    const state = onMe(initialAccount, { role: 'owner', signInEnabled: false });
    assert.equal(describeAccount(onSignedOut(state)).signIn, false);
  });

  it('does not invent a role when none was known', () => {
    assert.equal(onSignedOut(initialAccount).me, null);
  });

  it('says what the problem is in the words of the page', () => {
    assert.equal(SIGN_OUT_PROBLEM, "Couldn't sign out. Try again.");
  });
});

describe('accountDelay', () => {
  it('asks again every few minutes when all is well', () => {
    assert.equal(accountDelay(null), ME_REFRESH_MS);
    assert.equal(ME_REFRESH_MS, 300_000);
  });

  it('asks again sooner after a failure, and waits as long as the server says when it is busy', () => {
    assert.equal(accountDelay({ kind: 'network' }), 60_000);
    assert.equal(accountDelay({ kind: 'server', status: 500 }), 60_000);
    assert.equal(accountDelay({ kind: 'limited', retryAfterMs: 20_000 }), 20_000);
    assert.equal(accountDelay({ kind: 'limited' }), 60_000);
    assert.equal(accountDelay({ kind: 'limited', retryAfterMs: 0 }), 60_000);
  });
});
