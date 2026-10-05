External PDF originals
======================

Papers without a DOI or arXiv identifier can be registered from a publicly
reachable HTTPS original. This does not synthesize a DOI/OpenAlex identity and
does not turn a title-only reference into an authoritative paper identity.
Registration requires ``papers:write`` (or the equivalent authenticated session),
PostgreSQL, object storage, and the local ``pdftotext`` executable.

Register an original
--------------------

``POST /api/papers/source-register`` accepts exactly one JSON object:

.. code-block:: json

   {
     "source_url": "https://papers.example.org/original.pdf",
     "title": "A Secure Quantum Algorithm for External Source Registration",
     "authors": ["Fixture Author"],
     "year": 2026
   }

The matching CLI command (available in updated ``qatlas-cli`` releases) is:

.. code-block:: console

   qatlas paper source-register https://papers.example.org/original.pdf \
     --title "A Secure Quantum Algorithm for External Source Registration" \
     --author "Fixture Author" --year 2026 --json

Repeat ``--author`` for multiple authors. All bibliographic options are required;
``--json`` retains the complete server response. After a transport failure the
CLI reports an UNKNOWN write outcome and never automatically retries: inspect
server state first. URL/SHA idempotence is not HTTP response replay.

Use the actual article title and author/year metadata from the source of record.
The title must match the PDF's first-page front matter strongly; an occurrence in
references, prose, abstract or a later page is not evidence of ownership.
Unextractable/image-only PDFs, unavailable ``pdftotext``, and insufficiently
distinctive titles fail closed. The shared verifier currently requires at least
24 normalized title characters and three title words.

Successful registration returns HTTP 200:

.. code-block:: json

   {
     "paper_id": "qa_<ulid>",
     "created": true,
     "external_id": "source_url:https://papers.example.org/original.pdf",
     "source_url": "https://papers.example.org/original.pdf",
     "source": {
       "source_id": "src_<ulid>",
       "origin": "source_url:https://papers.example.org/original.pdf",
       "sha256": "<64-character-lowercase-sha256>",
       "size_bytes": 12345,
       "created_at": "2026-10-05T03:00:00Z",
       "source_url": "https://papers.example.org/original.pdf",
       "retrieved_url": "https://papers.example.org/original.pdf",
       "retrieved_at": "2026-10-05T03:00:00Z",
       "pdf_endpoint": "/api/papers/qa_<ulid>/sources/src_<ulid>/pdf"
     }
   }

``created`` refers to the work, not the source revision. Use ``paper_id`` for
ordinary paper detail and the existing ``/sources`` list and source-pinned PDF
reader. These readers retain their normal ``papers:read`` and HTTP Range rules.
No parse revision or MinerU output is fabricated by this operation.

Identity and immutable revisions
--------------------------------

* IACR ePrint ``https://eprint.iacr.org/2026/1591``, ``/2026/1591.pdf`` and
  ``/2026/1591/pdf`` resolve to the single work identity ``eprint:2026/1591``.
  Its landing URL is fetched through the canonical direct ``.pdf`` URL.
* Other URLs use ``source_url:<normalized HTTPS URL>``. Hostname case and a
  trailing DNS dot are normalized, an empty path becomes ``/``, and query
  parameters are retained. Different URLs are different works even if their
  titles or byte hashes match. Redirect destinations do not silently change the
  submitted work identity.
* The same work and same PDF SHA-256 reuse the original ``source_id`` and first
  acquisition timestamps. A changed PDF produces another immutable source under
  the same ``qa_`` paper. Old source URLs, timestamps, byte hashes, objects and
  source IDs are never overwritten. For equivalent ePrint landing/PDF forms,
  the source retains the first successful acquisition's submitted URL.
* External works never insert or consult title-hash identities. Existing
  DOI/arXiv/title matching behavior is unchanged, including refusal to mint via
  title-only ``ResolveOrMint``.
* PDFs use content-addressed keys ``pdf/external-sources/<sha256>.pdf`` and
  create-only object writes. An existing object must pass size/hash verification
  before a retry is accepted; corrupt objects are not silently replaced.

Acquisition security and failures
---------------------------------

Only public HTTPS destinations are accepted. Userinfo, explicit ports (including
``:443``), fragments, control characters and ambiguous URL forms are rejected.
Every redirect is checked, with at most four followed redirects. The dialer
resolves DNS per connection, rejects all answers if any is private/non-public,
and connects to a numeric address from that exact validated result. TLS still
verifies the original hostname. This avoids DNS rebinding between a URL check
and the actual connection. Loopback, link-local/cloud metadata, private IPv4/
IPv6, carrier-grade NAT, multicast and IPv6 transition-address bypasses are
blocked. The transport does not inherit proxy environment variables and has no
production allow-private/test-transport switch.

The complete registration has a 45-second deadline, bounded DNS/TCP/TLS/header
waits, a 64 MiB PDF limit (also enforced for chunked responses), and a 64 KiB
JSON limit. At most two registrations acquire PDFs concurrently per process;
excess requests receive ``503`` with ``Retry-After: 5`` before acquisition,
rather than entering an unbounded queue. HTTP acquisition is direct: arbitrary
HTML landing-page discovery, browser login, JavaScript and publisher challenges
are not executed.

Errors use the existing JSON ``detail`` envelope:

* ``400``: invalid JSON, missing bibliographic fields, or rejected URL syntax.
* ``401`` / ``403``: missing authentication / missing ``papers:write``.
* ``422``: acquisition, SSRF policy, size/format, or title/provenance rejection.
* ``503``: catalog/object-store configuration unavailable or database failure.
* ``500``: immutable object persistence/integrity failure or other internal error.

A PostgreSQL transaction registers the paper, external identity and source
provenance atomically. Object storage precedes that transaction. If the database
fails, a verified unreferenced hash object may remain for a safe retry; it is not
deleted because a concurrent successful registration may already reference it.

Migration and deployment
------------------------

Migration ``00010_external_sources`` adds the unique ``papers.external_id``,
``eprint``/``source_url`` identity kinds, and source acquisition URL/time columns.
It preserves existing immutable source IDs/hashes and preserves generated
``paper_ref`` priority: OpenAlex, arXiv, DOI, then external identity. Existing
paper detail can therefore display the external reference without an alias
resolver or synthetic DOI.

Apply the migration with the updated binary's normal startup migration flow.
Downgrading to an old schema/binary while external works remain is not safe:
the down migration intentionally refuses rather than discard external-only
identities. Back up PostgreSQL and objects before the upgrade; retain the new
schema until external works are explicitly exported/adopted.

Default tests are offline. Disposable PostgreSQL integration tests require
``-tags integration`` and an explicitly supplied ``QATLAS_TEST_PG_DSN``; never
point this test variable at a production database. Test fixture transports can
map public-looking URLs to a local server only inside test code.
