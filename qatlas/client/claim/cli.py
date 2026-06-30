"""``qatlas contrib claim <paper_id>`` — entry point + orchestration.

Fetches the paper markdown (suspend-and-wait), then starts a localhost WebUI +
agent so the human can draft Claims and file gitea issues. FastAPI / uvicorn are
an optional dependency (``qatlas[contrib]``); a missing install yields a clear
install hint rather than a traceback.
"""

from __future__ import annotations

import argparse
import os
import sys
import threading
import webbrowser

from qatlas.client import _common
from .drafter import ClaimDrafter
from .gitea import GiteaClient, GiteaConfig
from .models import PaperRef
from .source import PaperUnavailable, fetch_markdown


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="qatlas contrib claim",
        description="Draft Claims against a paper in a localhost WebUI and file gitea issues.",
    )
    p.add_argument("paper_id", help="arXiv id or DOI of the paper-of-record")
    p.add_argument("--gitea-url", default=None, help="gitea base URL (env GITEA_HOST / QATLAS_GITEA_URL)")
    p.add_argument("--gitea-repo", default=None, help="owner/name (default agony/qatlas-lean or QATLAS_GITEA_REPO)")
    p.add_argument("--gitea-token", default=None, help="gitea token (env GITEA_ACCESS_TOKEN / QATLAS_GITEA_TOKEN)")
    p.add_argument("--port", type=int, default=8731, help="localhost port for the WebUI")
    p.add_argument("--host", default="127.0.0.1", help="bind host (default 127.0.0.1)")
    p.add_argument("--no-browser", action="store_true", help="don't auto-open a browser")
    p.add_argument("--budget", type=float, default=600.0, help="seconds to wait for paper markdown")
    _common.add_common_http_args(p)
    return p


def _gitea_config(args: argparse.Namespace) -> GiteaConfig:
    url = args.gitea_url or os.getenv("QATLAS_GITEA_URL") or os.getenv("GITEA_HOST")
    repo = args.gitea_repo or os.getenv("QATLAS_GITEA_REPO") or "agony/qatlas-lean"
    token = args.gitea_token or os.getenv("QATLAS_GITEA_TOKEN") or os.getenv("GITEA_ACCESS_TOKEN")
    if not url:
        raise SystemExit(
            "ERROR: gitea base URL not set. Pass --gitea-url or set GITEA_HOST / QATLAS_GITEA_URL."
        )
    if not token:
        raise SystemExit(
            "ERROR: gitea token not set. Pass --gitea-token or set GITEA_ACCESS_TOKEN / QATLAS_GITEA_TOKEN."
        )
    return GiteaConfig(base_url=url, repo=repo, token=token, verify=_common.request_verify(args))


def _paper_ref(paper_id: str, markdown: str) -> PaperRef:
    title = None
    for ln in markdown.splitlines():
        s = ln.strip()
        if s.startswith("# "):
            title = s[2:].strip()
            break
    return PaperRef(id=paper_id, doi_or_arxiv=paper_id, title=title)


def main(argv: list[str] | None = None) -> int:
    if argv is None:
        argv = sys.argv[1:]
    args = build_parser().parse_args(argv)

    # FastAPI / uvicorn are the optional 'contrib' extras. Probe them up front
    # so a missing install yields a clear hint, not a mid-session traceback.
    try:
        import fastapi  # noqa: F401
        import uvicorn  # noqa: F401
    except ModuleNotFoundError as exc:
        print(
            f"ERROR: `qatlas contrib claim` needs the optional WebUI dependencies "
            f"(missing: {exc.name}).\nInstall them with:  uv tool install 'quantum-atlas[contrib]'\n"
            f"or:  pip install 'quantum-atlas[contrib]'",
            file=sys.stderr,
        )
        return 2

    from .server import SessionState, run

    base_url = _common.base_url_from_args(args)
    headers = _common.auth_headers(args)
    verify = _common.request_verify(args)
    gitea_cfg = _gitea_config(args)

    print(f"Fetching markdown for {args.paper_id} from {base_url} …", file=sys.stderr)
    try:
        markdown = fetch_markdown(
            base_url, headers, verify, args.paper_id, budget_s=args.budget
        )
    except PaperUnavailable as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1

    paper = _paper_ref(args.paper_id, markdown)
    drafter = ClaimDrafter(paper)
    state = SessionState(
        paper=paper,
        markdown=markdown,
        base_url=base_url,
        auth_headers=headers,
        verify=verify,
        gitea=GiteaClient(gitea_cfg),
        drafter=drafter,
        mailto=os.getenv("QATLAS_OPENALEX_MAILTO"),
    )

    url = f"http://{args.host}:{args.port}/"
    print(f"\n  Claim WebUI:  {url}", file=sys.stderr)
    print(f"  Agent:        {drafter.backend_name}", file=sys.stderr)
    print(f"  Filing to:    {gitea_cfg.repo} @ {gitea_cfg.base_url}", file=sys.stderr)
    print("  Ctrl-C to stop.\n", file=sys.stderr)

    if not args.no_browser:
        threading.Timer(0.7, lambda: webbrowser.open(url)).start()

    try:
        run(state, args.host, args.port)
    except KeyboardInterrupt:
        pass
    return 0


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
