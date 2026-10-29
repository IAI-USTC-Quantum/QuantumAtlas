// Main-origin only. The server, not localStorage, grants child access.
const LOCK_NAME = 'qatlas-web-session-v1';
const AUTH_KEY = 'pocketbase_auth';
const BOOTSTRAP_PATH = '/pursuit-session/';
const OPERATION_TIMEOUT_MS = 15000;

async function withSessionLock(action) {
  if (!globalThis.navigator?.locks?.request || !globalThis.AbortController) {
    throw new Error('Shared sessions require browser Web Locks support.');
  }
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), OPERATION_TIMEOUT_MS);
  try {
    return await navigator.locks.request(
      LOCK_NAME,
      { mode: 'exclusive', signal: controller.signal },
      async () => {
        controller.signal.throwIfAborted();
        return action(controller.signal);
      },
    );
  } finally {
    clearTimeout(timer);
  }
}

async function post(path, signal, token) {
  const headers = { 'Content-Type': 'application/json' };
  if (token) headers.Authorization = `Bearer ${token}`;
  const response = await fetch(path, {
    method: 'POST',
    mode: 'same-origin',
    credentials: 'same-origin',
    redirect: 'error',
    cache: 'no-store',
    headers,
    body: '{}',
    signal,
  });
  // Never consume or display an upstream body (it could contain credentials).
  if (response.status !== 204) {
    throw new Error('Shared session service did not confirm the operation.');
  }
  signal.throwIfAborted();
}

const GUARD_KEY = 'qatlas_shared_auth_guard_v1';

function authGuard() {
  const raw = window.localStorage.getItem(GUARD_KEY);
  if (!raw) return { epoch: 'initial', blocked: false };
  try {
    const state = JSON.parse(raw);
    if (typeof state?.epoch === 'string' && typeof state?.blocked === 'boolean') return state;
  } catch { /* Corruption must not revive a canceled login. */ }
  return { epoch: 'invalid', blocked: true };
}

function setAuthGuard(blocked) {
  window.localStorage.setItem(GUARD_KEY, JSON.stringify({ epoch: crypto.randomUUID(), blocked }));
}

// Covers PB authentication work AND its commit, not just the shared HTTP calls.
// The callback must await all SDK work (passing signal); never race a timeout
// against an SDK that may still save auth after the lock has been released.
export async function withSharedSessionLifecycle(action) {
  if (typeof action !== 'function') throw new TypeError('Lifecycle callback is required.');
  return withSessionLock(async (signal) => {
    let active = true;
    const checkActive = () => {
      if (!active) throw new Error('Shared session lifecycle has ended.');
      signal.throwIfAborted();
    };
    const authSnapshot = () => {
      checkActive();
      return { ...authGuard(), token: currentToken() };
    };
    const isCurrent = (snapshot) => {
      checkActive();
      const now = authSnapshot();
      return now.epoch === snapshot.epoch && now.token === snapshot.token && now.blocked === snapshot.blocked;
    };
    const lifecycle = {
      signal,
      authSnapshot,
      isCurrent,
      commitAuth(snapshot, save, explicitOAuth = false) {
        if (!isCurrent(snapshot) || (snapshot.blocked && !explicitOAuth)) return false;
        // Only a newly completed explicit OAuth may lift a logout tombstone.
        save();
        if (explicitOAuth) setAuthGuard(false);
        return true;
      },
      async syncSharedSession(getToken) {
        checkActive();
        if (typeof getToken !== 'function') throw new TypeError('getToken is required.');
        // Local state is only a cancellation guard, NEVER a grant of access.
        if (authGuard().blocked) return false;
        await post('/pursuit-session/context', signal);
        checkActive();
        if (authGuard().blocked) return false;
        // Fresh only after lock + context Cookie; no captured issuer token.
        const token = getToken();
        if (token == null || token === '') return false;
        if (typeof token !== 'string') throw new TypeError('Expected a session token.');
        await post('/pursuit-session/session', signal, token);
        return true;
      },
      async logoutSharedSession(clearLocalAuth) {
        checkActive();
        if (typeof clearLocalAuth !== 'function') throw new TypeError('clearLocalAuth is required.');
        await post('/pursuit-session/logout', signal);
        checkActive();
        // Block any older page/SDK late write before clearing PB, inside lock.
        setAuthGuard(true);
        clearLocalAuth();
      },
    };
    try { return await action(lifecycle); } finally { active = false; }
  });
}

export async function syncSharedSession(getToken) {
  return withSharedSessionLifecycle((lifecycle) => lifecycle.syncSharedSession(getToken));
}

export async function logoutSharedSession(clearLocalAuth) {
  return withSharedSessionLifecycle((lifecycle) => lifecycle.logoutSharedSession(clearLocalAuth));
}

function currentToken() {
  const raw = window.localStorage.getItem(AUTH_KEY);
  if (!raw) return '';
  try {
    const stored = JSON.parse(raw);
    return typeof stored?.token === 'string' ? stored.token : '';
  } catch {
    return '';
  }
}

function pageOrigins(dataset) {
  const main = new URL(dataset.mainOrigin);
  const child = new URL(dataset.childOrigin);
  if (
    main.origin !== window.location.origin || main.origin !== dataset.mainOrigin ||
    main.protocol !== 'https:' || child.protocol !== 'https:' ||
    child.origin !== dataset.childOrigin ||
    !child.hostname.endsWith(`.${main.hostname}`)
  ) {
    throw new Error('Invalid shared session page origin.');
  }
  return { child: child.origin };
}

function setupPage() {
  if (typeof document === 'undefined' || typeof window === 'undefined') return;
  const dataset = document.body?.dataset;
  if (!dataset || !['bootstrap', 'logout'].includes(dataset.sessionMode)) return;
  const status = document.getElementById('shared-session-status');
  const reportError = () => {
    if (!status) return;
    status.textContent = 'Shared session failed. Your main login was not cleared. Refresh this page to retry.';
    if (dataset.sessionMode === 'bootstrap') {
      const login = document.createElement('a');
      login.href = `/login?from=${encodeURIComponent(BOOTSTRAP_PATH)}`;
      login.textContent = 'Check main sign-in';
      status.append(' ', login);
    }
  };
  try {
    const { child } = pageOrigins(dataset);
    if (dataset.sessionMode === 'bootstrap') {
      void syncSharedSession(currentToken).then((granted) => {
        window.location.replace(granted
          ? `${child}/`
          : `/login?from=${encodeURIComponent(BOOTSTRAP_PATH)}`);
      }).catch(reportError);
    } else {
      const button = document.getElementById('shared-session-logout');
      if (!button) throw new Error('Missing logout confirmation button.');
      button.addEventListener('click', async () => {
        if (button.disabled) return;
        button.disabled = true;
        if (status) status.textContent = 'Signing out…';
        try {
          await logoutSharedSession(() => window.localStorage.removeItem(AUTH_KEY));
          window.location.replace('/login');
        } catch {
          reportError();
          button.disabled = false;
        }
      });
    }
  } catch {
    reportError();
  }
}

// Importing from the ordinary main SPA must have no page side effects.
setupPage();
