"""Task models and JSON-backed persistence for shares and ingests."""

from __future__ import annotations

import json
import logging
import os
import tempfile
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, List, Optional

from pydantic import BaseModel, Field

logger = logging.getLogger(__name__)


def _atomic_write_json(path: Path, data: dict) -> None:
    """Write JSON atomically via a temp file and os.replace."""
    path.parent.mkdir(parents=True, exist_ok=True)
    payload = json.dumps(data, indent=2, ensure_ascii=False)
    fd, tmp_path_str = tempfile.mkstemp(dir=str(path.parent), suffix=".tmp")
    tmp_path = Path(tmp_path_str)
    try:
        os.write(fd, payload.encode("utf-8"))
        os.close(fd)
        os.replace(tmp_path, path)
    except Exception:
        try:
            os.close(fd)
        except OSError:
            pass
        if tmp_path.exists():
            tmp_path.unlink(missing_ok=True)
        raise


class ShareRecord(BaseModel):
    """Share link metadata."""

    token: str
    paths: List[str]
    created_by: Optional[str] = None
    created_at: str
    expires_at: Optional[str] = None
    label: Optional[str] = None


class StepStatus(BaseModel):
    """Per-step status for ingest tasks."""

    status: str = "pending"
    message: Optional[str] = None
    progress: Optional[Dict[str, Any]] = None
    started_at: Optional[str] = None
    finished_at: Optional[str] = None
    error: Optional[str] = None
    result: Optional[Dict[str, Any]] = None


class IngestTask(BaseModel):
    """Background paper ingest task with step tracking."""

    task_id: str
    arxiv_id: str
    status: str
    message: Optional[str] = None
    requester: Optional[str] = None
    options: Dict[str, Any] = Field(default_factory=dict)
    steps: Dict[str, StepStatus] = Field(default_factory=dict)
    submitted_at: str
    started_at: Optional[str] = None
    finished_at: Optional[str] = None
    updated_at: Optional[str] = None


class ShareStore:
    """JSON file persistence for shares: {base_dir}/{token}.json"""

    def __init__(self, base_dir: Path):
        self.base_dir = base_dir
        self.base_dir.mkdir(parents=True, exist_ok=True)

    def _path(self, token: str) -> Path:
        return self.base_dir / f"{token}.json"

    def save(self, share: ShareRecord) -> None:
        _atomic_write_json(self._path(share.token), share.model_dump())

    def get(self, token: str) -> Optional[ShareRecord]:
        path = self._path(token)
        if not path.is_file():
            return None
        return ShareRecord.model_validate_json(path.read_text(encoding="utf-8"))

    def delete(self, token: str) -> bool:
        path = self._path(token)
        if not path.is_file():
            return False
        path.unlink()
        return True

    def list_all(self) -> List[ShareRecord]:
        out: List[ShareRecord] = []
        for path in self.base_dir.glob("*.json"):
            try:
                out.append(ShareRecord.model_validate_json(path.read_text(encoding="utf-8")))
            except Exception as e:
                logger.warning("skip corrupt share file %s: %s", path, e)
        out.sort(key=lambda s: s.created_at, reverse=True)
        return out

    def is_valid(self, token: str) -> bool:
        rec = self.get(token)
        if rec is None:
            return False
        if rec.expires_at is None:
            return True
        try:
            exp = datetime.fromisoformat(rec.expires_at.replace("Z", "+00:00"))
        except ValueError:
            return False
        now = datetime.now(timezone.utc)
        if exp.tzinfo is None:
            exp = exp.replace(tzinfo=timezone.utc)
        return now <= exp


class IngestStore:
    """JSON persistence for ingest tasks: {base_dir}/{task_id}.json"""

    def __init__(self, base_dir: Path):
        self.base_dir = base_dir
        self.base_dir.mkdir(parents=True, exist_ok=True)

    def _path(self, task_id: str) -> Path:
        return self.base_dir / f"{task_id}.json"

    def save(self, task: IngestTask) -> None:
        _atomic_write_json(self._path(task.task_id), task.model_dump(mode="json"))

    def get(self, task_id: str) -> Optional[IngestTask]:
        path = self._path(task_id)
        if not path.is_file():
            return None
        return IngestTask.model_validate_json(path.read_text(encoding="utf-8"))

    def list_all(self, limit: int = 50) -> List[IngestTask]:
        tasks: List[IngestTask] = []
        for path in self.base_dir.glob("*.json"):
            try:
                tasks.append(IngestTask.model_validate_json(path.read_text(encoding="utf-8")))
            except Exception as e:
                logger.warning("skip corrupt ingest file %s: %s", path, e)
        tasks.sort(key=lambda x: x.submitted_at, reverse=True)
        return tasks[:limit]
