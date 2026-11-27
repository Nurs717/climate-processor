// The client of the REST API. It turns every failure into an ApiError with a kind the page can act on.

const DEFAULT_TIMEOUT_MS = 8000;
const MIN_RETRY_MS = 5000;
const MAX_RETRY_MS = 120_000;
const DEFAULT_RETRY_MS = 30_000;

export class ApiError extends Error {
  /**
   * @param {'empty'|'limited'|'network'|'server'} kind
   *   empty: the sensor has not reported yet; limited: too many requests;
   *   network: the server could not be reached; server: it answered with something that is not an answer
   * @param {{status?: number, retryAfterMs?: number}} [details]
   */
  constructor(kind, { status = 0, retryAfterMs = 0 } = {}) {
    super(kind);
    this.name = 'ApiError';
    this.kind = kind;
    this.status = status;
    this.retryAfterMs = retryAfterMs;
  }
}

/** The Retry-After header of a 429 answer, in milliseconds, kept within sensible bounds. */
export function parseRetryAfter(header) {
  const seconds = Number(header);
  if (header === null || header === undefined || header === '' || !Number.isFinite(seconds) || seconds < 0) {
    return DEFAULT_RETRY_MS;
  }
  return Math.min(MAX_RETRY_MS, Math.max(MIN_RETRY_MS, Math.ceil(seconds) * 1000));
}

/**
 * GET path and read the JSON answer.
 * @param {string} path
 * @param {number} timeoutMs
 * @returns {Promise<{status: number, body: unknown}>} for an answer with a 2xx status or 404
 */
async function getJSON(path, timeoutMs) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    let response;
    try {
      response = await fetch(path, {
        signal: controller.signal,
        headers: { Accept: 'application/json' },
        credentials: 'same-origin',
        cache: 'no-store',
      });
    } catch {
      throw new ApiError('network');
    }
    if (response.status === 429) {
      throw new ApiError('limited', {
        status: 429,
        retryAfterMs: parseRetryAfter(response.headers.get('Retry-After')),
      });
    }
    if (response.status === 404) return { status: 404, body: null };
    if (!response.ok) throw new ApiError('server', { status: response.status });
    try {
      return { status: response.status, body: await response.json() };
    } catch {
      throw new ApiError('server', { status: response.status });
    }
  } finally {
    clearTimeout(timer);
  }
}

/**
 * The latest reading in the shape the page uses.
 * @param {unknown} body the JSON of GET /api/v1/latest
 */
export function parseLatest(body) {
  const { temperature_c: temperature, humidity_percent: humidity, pressure_pa: pressure } = body ?? {};
  if (![temperature, humidity, pressure].every(Number.isFinite)) throw new ApiError('server');
  const age = body.age_seconds;
  return {
    deviceId: typeof body.device_id === 'string' ? body.device_id : '',
    time: typeof body.time === 'string' ? body.time : '',
    ageSeconds: Number.isFinite(age) ? Math.max(0, age) : 0,
    stale: body.stale === true,
    temperature,
    humidity,
    pressure,
  };
}

/** GET /api/v1/latest. An ApiError of kind "empty" means the sensor has not reported yet. */
export async function getLatest({ timeoutMs = DEFAULT_TIMEOUT_MS } = {}) {
  const { status, body } = await getJSON('/api/v1/latest', timeoutMs);
  if (status === 404) throw new ApiError('empty', { status });
  return parseLatest(body);
}

/** GET /api/v1/auth/me: the role the server gives this browser. */
export async function getMe({ timeoutMs = DEFAULT_TIMEOUT_MS } = {}) {
  const { status, body } = await getJSON('/api/v1/auth/me', timeoutMs);
  if (status === 404 || typeof body?.role !== 'string') throw new ApiError('server', { status });
  return { role: body.role, signInEnabled: body.sign_in_enabled === true };
}
