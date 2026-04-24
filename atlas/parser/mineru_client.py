"""
Client for MinerU's async document extraction API.
"""

from __future__ import annotations

import zipfile
from pathlib import Path
from typing import Any, Dict, Optional

import requests


class MinerUError(RuntimeError):
    """Raised when MinerU returns an unsuccessful response."""


class MinerUClient:
    """Small wrapper around MinerU's token-based precision extraction API."""

    def __init__(
        self,
        token: str,
        *,
        base_url: str = "https://mineru.net",
        timeout: tuple[float, float] = (10, 120),
    ) -> None:
        self.token = token
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self.session = requests.Session()
        self.session.headers.update(
            {
                "Authorization": f"Bearer {token}",
                "Accept": "*/*",
            }
        )

    def submit_url_task(
        self,
        *,
        url: str,
        data_id: Optional[str] = None,
        model_version: str = "vlm",
        language: str = "ch",
        enable_formula: bool = True,
        enable_table: bool = True,
        is_ocr: bool = False,
        no_cache: bool = False,
    ) -> str:
        """Submit a URL extraction task and return MinerU's task id."""
        payload: Dict[str, Any] = {
            "url": url,
            "model_version": model_version,
            "language": language,
            "enable_formula": enable_formula,
            "enable_table": enable_table,
            "is_ocr": is_ocr,
            "no_cache": no_cache,
        }
        if data_id:
            payload["data_id"] = data_id

        response = self.session.post(
            f"{self.base_url}/api/v4/extract/task",
            json=payload,
            headers={"Content-Type": "application/json"},
            timeout=self.timeout,
        )
        return self._task_id_from_response(response)

    def get_task(self, task_id: str) -> Dict[str, Any]:
        """Return the latest state for one MinerU extraction task."""
        response = self.session.get(
            f"{self.base_url}/api/v4/extract/task/{task_id}",
            timeout=self.timeout,
        )
        payload = self._json_response(response)
        data = payload.get("data")
        if not isinstance(data, dict):
            raise MinerUError("MinerU response did not include task data")
        return data

    def download_markdown_from_zip(self, full_zip_url: str, output_path: str | Path) -> Path:
        """Download MinerU's result zip and extract the first full.md file."""
        output_path = Path(output_path)
        output_path.parent.mkdir(parents=True, exist_ok=True)
        zip_path = output_path.with_suffix(output_path.suffix + ".mineru.zip")

        response = requests.get(full_zip_url, stream=True, timeout=(10, 300))
        response.raise_for_status()
        with open(zip_path, "wb") as out:
            for chunk in response.iter_content(1024 * 64):
                if chunk:
                    out.write(chunk)

        with zipfile.ZipFile(zip_path) as archive:
            markdown_names = [
                name
                for name in archive.namelist()
                if name.endswith("full.md") or name.endswith("/full.md")
            ]
            if not markdown_names:
                raise MinerUError("MinerU result zip did not contain full.md")
            markdown = archive.read(markdown_names[0]).decode("utf-8")

        output_path.write_text(markdown, encoding="utf-8")
        return output_path

    def _task_id_from_response(self, response: requests.Response) -> str:
        payload = self._json_response(response)
        data = payload.get("data")
        if not isinstance(data, dict) or not data.get("task_id"):
            raise MinerUError("MinerU response did not include task_id")
        return str(data["task_id"])

    def _json_response(self, response: requests.Response) -> Dict[str, Any]:
        response.raise_for_status()
        payload = response.json()
        if payload.get("code") != 0:
            raise MinerUError(str(payload.get("msg") or payload))
        return payload
