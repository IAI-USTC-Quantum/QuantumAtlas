"""Strict tag validation and fail-closed GitHub release gates (stdlib only)."""

import argparse
import json
import os
import re
import urllib.error
import urllib.request


# Go module tags and Docker tags must be portable too. SemVer build metadata
# (+...) is intentionally not supported for server releases; prereleases are.
SEMVER = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?")


def validate_version(tag):
    version = tag.removeprefix("v")
    match = SEMVER.fullmatch(version)
    if not tag.startswith("v") or not match:
        raise ValueError("require vMAJOR.MINOR.PATCH[-prerelease] (no PEP 440 or +build metadata)")
    prerelease = match[4]
    if prerelease and any(part.isdigit() and len(part) > 1 and part[0] == "0" for part in prerelease.split(".")):
        raise ValueError("numeric prerelease identifiers must not have leading zeroes")
    return version, bool(prerelease)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        # GitHub API calls must not forward Authorization to another origin
        # (or even another path). A moved repository needs operator review.
        return None


class GitHub:
    def __init__(self):
        self.token = os.environ["GH_TOKEN"]
        self.repo = os.environ["GITHUB_REPOSITORY"]
        self.base = os.environ.get("GITHUB_API_URL", "https://api.github.com")
        if not self.token or not re.fullmatch(r"[\w.-]+/[\w.-]+", self.repo) or not self.base.startswith("https://"):
            raise ValueError("require an authenticated HTTPS GitHub repository API")

    def request(self, path, payload=None):
        request = urllib.request.Request(
            f"{self.base}/repos/{self.repo}/{path}",
            data=None if payload is None else json.dumps(payload).encode(),
            method="GET" if payload is None else "PATCH",
            headers={"Authorization": f"Bearer {self.token}", "Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28", "Content-Type": "application/json"},
        )
        try:
            with urllib.request.build_opener(NoRedirect()).open(request, timeout=30) as response:
                return json.load(response)
        except urllib.error.HTTPError as error:
            # Even 404 on the releases LIST is a failed query, not evidence
            # that this tag is absent (bad credentials/access can look like 404).
            raise RuntimeError(f"GitHub release query/update failed: HTTP {error.code}") from None
        except (urllib.error.URLError, TimeoutError, ValueError) as error:
            raise RuntimeError(f"GitHub release query/update failed: {type(error).__name__}") from None

    def find(self, tag):
        # Authenticated list includes drafts. Never use `gh release view ||`
        # to treat an arbitrary network/auth/rate-limit failure as a new tag.
        found = None
        for page in range(1, 101):
            releases = self.request(f"releases?per_page=100&page={page}")
            if not isinstance(releases, list):
                raise ValueError("invalid GitHub releases response")
            for release in releases:
                if not isinstance(release, dict) or not isinstance(release.get("tag_name"), str):
                    raise ValueError("invalid GitHub release object")
                if release["tag_name"] == tag:
                    if type(release.get("draft")) is not bool or type(release.get("prerelease")) is not bool or type(release.get("id")) is not int:
                        raise ValueError("incomplete GitHub release object")
                    if found is not None:
                        raise ValueError("ambiguous duplicate releases for tag")
                    found = release
            if len(releases) < 100:
                return found
        raise ValueError("too many releases to establish safe tag state")


def guard(api, tag):
    release = api.find(tag)
    if release is not None and not release["draft"]:
        raise ValueError(f"{tag} is already public: refusing asset overwrite/re-upload")
    return release


def publish(api, tag, prerelease):
    release = guard(api, tag)
    if release is None:
        raise ValueError("validated draft release is missing")
    if release["prerelease"] != prerelease:
        raise ValueError("draft prerelease flag does not match SemVer tag")
    result = api.request(f"releases/{release['id']}", {"draft": False, "make_latest": "false"})
    if result.get("draft") is not False or result.get("tag_name") != tag or result.get("prerelease") != prerelease:
        raise ValueError("GitHub did not confirm the expected public release")
    return result


def public_stable(api, tag):
    release = api.find(tag)
    if release is None or release["draft"] or release["prerelease"]:
        raise ValueError("latest promotion requires an already-public stable release")
    return release


def emit(key, value):
    print(f"{key}={value}")
    if output := os.environ.get("GITHUB_OUTPUT"):
        with open(output, "a") as stream:
            stream.write(f"{key}={value}\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("version", "guard", "publish", "public-stable", "latest", "status"))
    parser.add_argument("tag")
    args = parser.parse_args()
    version, prerelease = validate_version(args.tag)
    if args.command == "version":
        emit("version", version)
        emit("tag", args.tag)
        emit("is_prerelease", str(prerelease).lower())
        return
    api = GitHub()
    if args.command == "guard":
        release = guard(api, args.tag)
        emit("release_state", "absent" if release is None else "draft")
    elif args.command == "publish":
        emit("release_url", publish(api, args.tag, prerelease)["html_url"])
    elif args.command in ("public-stable", "latest"):
        if prerelease:
            raise ValueError("prereleases must never promote latest")
        release = public_stable(api, args.tag)
        if args.command == "latest":
            api.request(f"releases/{release['id']}", {"make_latest": "true"})
    else:
        release = api.find(args.tag)
        emit("release_state", "absent" if release is None else "draft" if release["draft"] else "public")


if __name__ == "__main__":
    main()
