"""
Client for MinerU's async document extraction API.
"""

from __future__ import annotations

import zipfile
from pathlib import Path
from typing import Any, Dict, Optional

import requests


class MinerUError(RuntimeError):
    """Base class for MinerU API errors.

    Carries the structured `code` (e.g. ``"-60018"``), human ``msg``, and
    ``http_status`` so retry-loops can switch on the type even after the
    error has been re-raised through several layers.
    """

    def __init__(
        self,
        msg: str,
        *,
        code: str = "",
        http_status: int = 0,
    ) -> None:
        super().__init__(msg)
        self.code = code
        self.msg = msg
        self.http_status = http_status


class MinerURetryableError(MinerUError):
    """Transient failure — same request will likely succeed shortly.

    Mirrors Go ``ErrRetryable``: 5xx, 408, ``-10001``, ``-60001``,
    ``-60007..-60010``, ``-60020..-60022``.
    """


class MinerUDailyLimitError(MinerUError):
    """Today's free quota is exhausted. Sleep until next 00:01 or bail.

    Mirrors Go ``ErrDailyLimit``: HTTP 429, ``-60018``, ``-60019``, and
    free-form messages containing quota-hint keywords.
    """


class MinerUFatalError(MinerUError):
    """Non-retryable failure — bad token, bad PDF, too many pages.

    Mirrors Go ``ErrFatal``: HTTP 401/403 and the 17 documented fatal codes
    (``A0202`` / ``A0211`` / ``-500`` / ``-10002`` / ``-60002..-60017`` minus
    the retryable subset).
    """


# Source: MinerU error code table (mineru.net/apiManage/docs) cross-referenced
# with pdf2md's MINERU_RETRYABLE_ERROR_CODES. Keep in sync with
# internal/mineru/errors.go retryableErrorCodes.
_RETRYABLE_CODES = frozenset(
    {
        "-10001",  # 服务异常
        "-60001",  # 生成上传 URL 失败
        "-60007",  # 模型服务暂时不可用
        "-60008",  # 文件读取超时
        "-60009",  # 任务提交队列已满
        "-60010",  # 解析失败
        "-60020",  # 文件拆分失败
        "-60021",  # 读取文件页数失败
        "-60022",  # 网页读取失败
    }
)

_DAILY_LIMIT_CODES = frozenset({"-60018", "-60019"})

# Fatal codes with human-readable hints surfaced in the error message.
_FATAL_CODES: Dict[str, str] = {
    "A0202": "Token 错误，请检查 Token 是否正确",
    "A0211": "Token 过期，请更换新 Token",
    "-500": "传参错误",
    "-10002": "请求参数错误",
    "-60002": "文件格式识别失败",
    "-60003": "文件读取失败",
    "-60004": "文件为空",
    "-60005": "文件大小超出限制（最大 200MB）",
    "-60006": "文件页数超过限制（最多 200 页）",
    "-60011": "获取有效文件失败",
    "-60012": "找不到任务",
    "-60013": "没有权限访问该任务",
    "-60014": "运行中的任务暂不支持删除",
    "-60015": "文件转换失败",
    "-60016": "文件转换为指定格式失败",
    "-60017": "重试次数达到上限",
}

_DAILY_LIMIT_KEYWORDS = (
    "limit",
    "quota",
    "too many",
    "exceed",
    "5000",
    "restricted",
    "tomorrow",
    "next day",
    "daily",
    "today",
    "额度",
    "上限",
    "次日",
    "明日",
    "明天",
)


def classify_mineru_error(
    *,
    code: str = "",
    msg: str = "",
    http_status: int = 0,
) -> MinerUError:
    """Build a typed MinerUError subclass from a response triplet.

    Mirrors Go ``classifyAPIError`` (internal/mineru/errors.go) — keep the
    two functions in lockstep so daemon-mode behaviour is identical across
    the Go server (result polling) and the Python CLI (submission daemon).
    """
    # HTTP 429: explicit throttle, regardless of body.
    if http_status == 429:
        return MinerUDailyLimitError(
            msg or "HTTP 429: throttled",
            code=code,
            http_status=http_status,
        )

    if code:
        if code in _DAILY_LIMIT_CODES:
            return MinerUDailyLimitError(msg, code=code, http_status=http_status)
        if code in _RETRYABLE_CODES:
            return MinerURetryableError(msg, code=code, http_status=http_status)
        hint = _FATAL_CODES.get(code)
        if hint is not None:
            blended = f"{msg} ({hint})" if msg else hint
            return MinerUFatalError(blended, code=code, http_status=http_status)

    # Token-level HTTP errors are fatal even without a structured code.
    if http_status in (401, 403):
        return MinerUFatalError(
            msg or f"HTTP {http_status}: authentication failed",
            code=code,
            http_status=http_status,
        )

    # Free-text quota hints.
    if msg and any(kw in msg.lower() for kw in _DAILY_LIMIT_KEYWORDS):
        return MinerUDailyLimitError(msg, code=code, http_status=http_status)

    # 5xx / 408 are retryable transport issues.
    if http_status >= 500 or http_status == 408:
        return MinerURetryableError(
            msg or f"HTTP {http_status}: transient transport error",
            code=code,
            http_status=http_status,
        )

    # Unclassified: surface the generic MinerUError so caller can decide.
    return MinerUError(msg or "unclassified MinerU error", code=code, http_status=http_status)


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

    def download_full_zip(self, full_zip_url: str, output_path: str | Path) -> Path:
        """Download MinerU's result zip verbatim to output_path and return it.

        This is what `qatlas mineru` uses (since v0.8.0) — the entire zip is
        pushed to the server's `upload-mineru` endpoint, which unpacks both
        the markdown and every image into their respective per-kind buckets.

        Earlier `download_markdown_from_zip` extracted only `full.md` and
        silently dropped images; this method is the strict-superset replacement
        that preserves the full bundle.
        """
        output_path = Path(output_path)
        output_path.parent.mkdir(parents=True, exist_ok=True)
        response = requests.get(full_zip_url, stream=True, timeout=(10, 300))
        response.raise_for_status()
        with open(output_path, "wb") as out:
            for chunk in response.iter_content(1024 * 64):
                if chunk:
                    out.write(chunk)
        return output_path

    def download_markdown_from_zip(self, full_zip_url: str, output_path: str | Path) -> Path:
        """Download MinerU's result zip and extract the first full.md file.

        Kept for backwards compatibility with anything that still pulls
        markdown directly from MinerU bypassing the server. New code should
        prefer :meth:`download_full_zip` so the images stay attached.
        """
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
        """Decode MinerU's {code,msg,data} envelope, classifying any failure.

        Both non-2xx HTTP and non-zero application ``code`` route through
        :func:`classify_mineru_error` so callers can match on
        ``MinerUDailyLimitError`` / ``MinerUFatalError`` / ``MinerURetryableError``.
        """
        status = response.status_code

        # Try to parse body as envelope first; some 4xx still carry useful code/msg.
        envelope: Optional[Dict[str, Any]] = None
        try:
            envelope = response.json()
            if not isinstance(envelope, dict):
                envelope = None
        except (ValueError, requests.exceptions.JSONDecodeError):
            envelope = None

        if status < 200 or status >= 300:
            code = ""
            msg = ""
            if envelope is not None:
                raw_code = envelope.get("code")
                code = "" if raw_code is None else str(raw_code)
                msg = str(envelope.get("msg") or "")
            if not msg:
                snippet = (response.text or "").strip()
                if len(snippet) > 200:
                    snippet = snippet[:200] + "..."
                msg = snippet
            raise classify_mineru_error(code=code, msg=msg, http_status=status)

        if envelope is None:
            raise MinerUError("MinerU response was not valid JSON", http_status=status)

        raw_code = envelope.get("code")
        if raw_code not in (0, "0", None):
            code_str = str(raw_code)
            msg = str(envelope.get("msg") or "")
            raise classify_mineru_error(code=code_str, msg=msg, http_status=0)

        return envelope
