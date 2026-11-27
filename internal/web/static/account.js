// Who is looking, and what the page offers them: the owner signs in and out, and a sign-in that did not work is
// explained. Pure functions, like model.js and history.js.
//
// The state holds what /auth/me said and the sentence about a problem, if there is one. describeAccount() turns it
// into what the header shows.

/** The owner's session ends on its own; a page left open learns about it from time to time. */
export const ME_REFRESH_MS = 300_000;
const ME_RETRY_MS = 60_000;

// The reasons the server puts after /?signin= when it sends the browser home after a sign-in that failed
// (internal/handler/rest/auth.go; a Go test checks that every reason has a sentence here). A sentence says what
// happened and what to do, without an apology.
const SIGN_IN_PROBLEMS = Object.freeze({
  cancelled: 'Sign-in was cancelled.',
  not_owner: "This Telegram account isn't the owner's. Try the owner's account.",
  expired: 'The sign-in expired, or it was started in another browser. Start it again.',
  busy: 'Too many sign-ins are in progress. Try again in a few minutes.',
  unavailable: 'Sign-in is temporarily unavailable. Try again in a minute.',
  failed: "Telegram didn't confirm the sign-in. Try again.",
});

export const SIGN_OUT_PROBLEM = "Couldn't sign out. Try again.";

/**
 * @typedef {{role: string, signInEnabled: boolean}} Me the answer of /auth/me
 * @typedef {{me: Me|null, signingOut: boolean, notice: string|null}} Account
 */

/** @type {Account} */
export const initialAccount = Object.freeze({ me: null, signingOut: false, notice: null });

/**
 * What the address says about a sign-in that has just ended.
 * @param {string} search location.search
 * @returns {{present: boolean, message: string|null}} present: the address carries a reason (it is to be taken off,
 *   whatever it says); message: the sentence for a reason the page knows, and null for anything else
 */
export function readSignIn(search) {
  const reason = new URLSearchParams(search).get('signin');
  if (reason === null) return { present: false, message: null };
  // The reason comes from the address, which anybody can write: only the reasons of the list count ("constructor" does not).
  return { present: true, message: Object.hasOwn(SIGN_IN_PROBLEMS, reason) ? SIGN_IN_PROBLEMS[reason] : null };
}

/** The query string without the sign-in reason, so that a reload does not show the sentence again. */
export function withoutSignIn(search) {
  const params = new URLSearchParams(search);
  params.delete('signin');
  const rest = params.toString();
  return rest ? `?${rest}` : '';
}

/**
 * @param {Account} state
 * @param {Me} me
 * @returns {Account}
 */
export function onMe(state, me) {
  return { ...state, me };
}

/** The sentence to show, or null to take it away. */
export function withNotice(state, notice) {
  return { ...state, notice };
}

export function onSignOutStarted(state) {
  return { ...state, signingOut: true, notice: null };
}

export function onSignOutFailed(state) {
  return { ...state, signingOut: false, notice: SIGN_OUT_PROBLEM };
}

/** The server has ended the session: the browser is a visitor now, whatever /auth/me says next. */
export function onSignedOut(state) {
  return { ...state, signingOut: false, notice: null, me: state.me && { ...state.me, role: 'demo' } };
}

/**
 * @typedef {object} AccountDescription
 * @property {boolean} visible the role and its buttons are shown at all (the role is not known before /auth/me answers)
 * @property {'owner'|'demo'} role
 * @property {string} label
 * @property {boolean} signIn the link to sign in
 * @property {'hidden'|'ready'|'busy'} signOut the button to sign out
 * @property {string|null} notice
 */

/**
 * What the header shows. Only the owner can sign in, and nothing but the owner has a session to end: a visitor of the
 * demo gets the link only where the server has a sign-in to offer.
 * @param {Account} state
 * @returns {AccountDescription}
 */
export function describeAccount(state) {
  const { me, signingOut, notice } = state;
  if (!me) return { visible: false, role: 'demo', label: '', signIn: false, signOut: 'hidden', notice };
  if (me.role === 'owner') {
    return { visible: true, role: 'owner', label: 'Signed in', signIn: false, signOut: signingOut ? 'busy' : 'ready', notice };
  }
  return { visible: true, role: 'demo', label: 'Demo view', signIn: me.signInEnabled, signOut: 'hidden', notice };
}

/**
 * How long to wait before asking /auth/me again.
 * @param {{kind: string, retryAfterMs?: number}|null} failure the failure of the last request, if it failed
 */
export function accountDelay(failure) {
  if (!failure) return ME_REFRESH_MS;
  if (failure.kind === 'limited') return failure.retryAfterMs || ME_RETRY_MS;
  return ME_RETRY_MS;
}
