"""MinerU API token pool with round-robin rotation and per-key daily-limit cooldown.

Mirrors the Go ``internal/mineru/keyring.go`` — keep the two in lockstep
so server-side and client-side rotation behave identically.

Usage::

    ring = KeyRing(tokens=["tok-a", "tok-b"], base_url="https://mineru.net")
    client, slot = ring.acquire()          # round-robin, skips cooled-down keys
    try:
        task_id = client.submit_url_task(url=pdf_url, ...)
    except MinerUDailyLimitError:
        ring.mark_daily_limit(slot)        # only this key sleeps until midnight
        client, slot = ring.acquire()      # try the next one
        ...

    ring.available_slots()                 # how many keys are free right now
    ring.soonest_recovery()                # earliest midnight reset across all keys
"""

from __future__ import annotations

import threading
import time
from datetime import datetime, timedelta
from typing import Optional

from qatlas.parser.mineru_client import MinerUClient


class KeyRing:
    """Round-robin MinerU API token pool with per-key daily-limit cooldown.

    Thread-safe: acquire / mark_daily_limit / property reads take an
    internal lock; the MinerUClient instances handed out are themselves
    safe for reuse within a single thread (requests.Session is NOT
    thread-safe, but each slot has its own Session).
    """

    def __init__(
        self,
        tokens: list[str],
        base_url: str = "https://mineru.net",
        *,
        now: Optional[callable] = None,
    ) -> None:
        self._now = now or time.time
        self._lock = threading.Lock()
        self._entries: list[_Entry] = []
        self._cursor = 0
        for tok in tokens:
            tok = tok.strip()
            if tok:
                self._entries.append(
                    _Entry(client=MinerUClient(tok, base_url=base_url), cooldown_until=0.0)
                )

    @property
    def size(self) -> int:
        """Number of non-empty tokens loaded."""
        with self._lock:
            return len(self._entries)

    def acquire(self) -> tuple[MinerUClient, int]:
        """Return (client, slot) for the next free key, or raise AllKeysExhausted."""
        with self._lock:
            n = len(self._entries)
            if n == 0:
                raise AllKeysExhausted("no MinerU API tokens configured")
            now = self._now()
            for i in range(n):
                idx = (self._cursor + i) % n
                e = self._entries[idx]
                if e.cooldown_until <= now:
                    self._cursor = (idx + 1) % n
                    return e.client, idx
            raise AllKeysExhausted(
                f"all {n} MinerU API keys are on daily-limit cooldown"
            )

    def mark_daily_limit(self, slot: int, until: Optional[float] = None) -> None:
        """Place *slot* on cooldown until *until* (epoch seconds).

        When *until* is None, defaults to the next local 00:01 — same
        logic as Go ``nextLocalDailyReset``.
        """
        if until is None:
            until = _next_local_daily_reset()
        with self._lock:
            if 0 <= slot < len(self._entries):
                e = self._entries[slot]
                if until > e.cooldown_until:
                    e.cooldown_until = until

    def soonest_recovery(self) -> Optional[float]:
        """Earliest cooldown_until across all keys, or None if some key is free."""
        with self._lock:
            now = self._now()
            earliest: Optional[float] = None
            for e in self._entries:
                if e.cooldown_until <= now:
                    return None  # at least one key is free
                if earliest is None or e.cooldown_until < earliest:
                    earliest = e.cooldown_until
            return earliest

    def available_slots(self) -> int:
        """Count of keys NOT currently in cooldown."""
        with self._lock:
            now = self._now()
            return sum(1 for e in self._entries if e.cooldown_until <= now)


class _Entry:
    __slots__ = ("client", "cooldown_until")

    def __init__(self, client: MinerUClient, cooldown_until: float) -> None:
        self.client = client
        self.cooldown_until = cooldown_until


class AllKeysExhausted(Exception):
    """Raised by KeyRing.acquire when every loaded key is on cooldown."""


def _next_local_daily_reset() -> float:
    """Return epoch seconds for the next local 00:01.

    Mirrors Go ``nextLocalDailyReset`` in internal/mineru/converter.go.
    """
    now = datetime.now()
    reset = now.replace(hour=0, minute=1, second=0, microsecond=0)
    if reset <= now:
        reset += timedelta(days=1)
    return reset.timestamp()
