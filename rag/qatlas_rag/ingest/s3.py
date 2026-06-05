"""RustFS / S3 list + get for the qatlas-md bucket.

Two safety knobs around bucket listing (rubber-duck #8 in plan.md):

1. ListObjectsV2 is run **per YYMM prefix**, never against the whole
   bucket — this dodges the RustFS v1.0.0-beta.5 large-bucket bug.
2. After listing, the total object count is cross-checked against the
   admin datausage API.  Strict policy:

      delta == 0     : all ops (add / update / delete) allowed
      0 < delta <= 5 : add / update allowed, deletes FORBIDDEN
      delta >  5     : abort, require human review

   This is conservative on purpose: ListObjectsV2 returning fewer
   objects than actually exist would otherwise look like "this paper got
   deleted upstream → drop it from Qdrant", which is catastrophic.
"""

from __future__ import annotations

import logging
import re
from dataclasses import dataclass
from typing import Iterator

import boto3
from botocore.config import Config as BotoConfig

logger = logging.getLogger("qatlas_rag.ingest.s3")

# qatlas-md keys: <YYMM>/<canonical>v<n>.md
#   - YYMM is 4 digits or 4 digits letter-suffix (rare); MinerU uses 4 digits
#   - canonical is letters / digits / "-" / "/" (old IDs like quant-ph/9508027
#     get flattened to "quant-ph/9508027" in source but in our MD bucket they
#     end up as 9508/9508027v1.md per the storage layout doc.
_KEY_RE = re.compile(
    r"^(?P<yymm>\d{4})/(?P<canonical>[A-Za-z0-9._-]+)v(?P<version>\d+)\.md$"
)


@dataclass(frozen=True)
class ObjectMeta:
    """Minimum fields the differ needs per S3 object."""

    bucket: str
    key: str
    etag: str            # boto3 strips the surrounding quotes for us
    last_modified: str   # ISO 8601
    size: int

    @property
    def arxiv_id(self) -> str:
        """Derive '<canonical>v<n>' from the object key (e.g. '9508027v1')."""
        m = _KEY_RE.match(self.key)
        if not m:
            raise ValueError(f"key {self.key!r} does not match qatlas-md schema")
        return f"{m.group('canonical')}v{m.group('version')}"

    @property
    def canonical(self) -> str:
        m = _KEY_RE.match(self.key)
        if not m:
            raise ValueError(f"key {self.key!r} does not match qatlas-md schema")
        return m.group("canonical")

    @property
    def yymm(self) -> str:
        m = _KEY_RE.match(self.key)
        if not m:
            raise ValueError(f"key {self.key!r} does not match qatlas-md schema")
        return m.group("yymm")

    @property
    def version(self) -> int:
        m = _KEY_RE.match(self.key)
        if not m:
            raise ValueError(f"key {self.key!r} does not match qatlas-md schema")
        return int(m.group("version"))


class RustFsClient:
    """Boto3-based wrapper, virtual-host disabled for RustFS path-style."""

    def __init__(
        self,
        *,
        endpoint_url: str,
        region: str = "us-east-1",
        access_key: str,
        secret_key: str,
        signature_version: str = "s3v4",
    ) -> None:
        self.endpoint_url = endpoint_url
        self.region = region
        self._client = boto3.client(
            "s3",
            endpoint_url=endpoint_url,
            region_name=region,
            aws_access_key_id=access_key,
            aws_secret_access_key=secret_key,
            config=BotoConfig(
                signature_version=signature_version,
                s3={"addressing_style": "path"},
                retries={"max_attempts": 5, "mode": "standard"},
            ),
        )

    # --- list -------------------------------------------------------------

    def list_yymm_prefixes(self, bucket: str) -> list[str]:
        """Return ['9508/', '2401/', ...] using delimiter='/'."""
        paginator = self._client.get_paginator("list_objects_v2")
        prefixes: set[str] = set()
        for page in paginator.paginate(Bucket=bucket, Delimiter="/"):
            for cp in page.get("CommonPrefixes", []) or []:
                p = cp.get("Prefix")
                if p and re.match(r"^\d{4}/$", p):
                    prefixes.add(p)
        return sorted(prefixes)

    def list_objects_in_prefix(self, bucket: str, prefix: str) -> Iterator[ObjectMeta]:
        """Yield every MD object under a YYMM prefix."""
        paginator = self._client.get_paginator("list_objects_v2")
        for page in paginator.paginate(Bucket=bucket, Prefix=prefix):
            for obj in page.get("Contents", []) or []:
                key = obj["Key"]
                if not key.endswith(".md"):
                    continue
                yield ObjectMeta(
                    bucket=bucket,
                    key=key,
                    etag=(obj.get("ETag") or "").strip('"'),
                    last_modified=obj["LastModified"].isoformat(),
                    size=int(obj["Size"]),
                )

    def list_bucket(self, bucket: str) -> Iterator[ObjectMeta]:
        """Walk the entire bucket via YYMM prefixes (safer than whole-bucket list).

        Caller still needs to cross-check the resulting count with
        ``admin_datausage_count`` for the same bucket.
        """
        for prefix in self.list_yymm_prefixes(bucket):
            yield from self.list_objects_in_prefix(bucket, prefix)

    # --- get --------------------------------------------------------------

    def get_object_bytes(self, bucket: str, key: str) -> bytes:
        resp = self._client.get_object(Bucket=bucket, Key=key)
        return resp["Body"].read()

    # --- admin sanity (placeholder; see runner.py) -----------------------
    #
    # The RustFS admin datausage endpoint is not part of the S3 wire
    # protocol; it lives behind a separate admin API.  The runner is
    # responsible for invoking it via a different code path (httpx call)
    # and reconciling against the list count.  We expose just the listing
    # primitive here.


def policy_for_list_delta(delta: int) -> tuple[bool, bool]:
    """Return (allow_add_update, allow_delete) per the sanity policy."""
    abs_delta = abs(delta)
    if abs_delta == 0:
        return True, True
    if abs_delta <= 5:
        return True, False
    return False, False
