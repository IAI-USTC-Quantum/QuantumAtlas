# Deterministic Middle reading windows

`Read(middleJSON []byte, Options) (*Response, error)` builds a **derived Markdown
reading response**, not raw Middle JSON or Structured Content. It uses the
existing `mineru.ParseMiddleJSON` schema gate and producer-index normalization.
No inference, provider calls, filesystem writes, or external fetches occur here.

- Default budget: 30,000 Unicode code points; maximum: 100,000.
- Public page/block numbers are 1-based. Gaps in producer block indexes stay
  gaps; array positions are never invented as locators.
- An explicit page selects that page; an additional block selects that exact
  block. An unspecified selection reads the document. The cursor retains the
  original selection and limit, so subsequent requests may supply only cursor.
- The opaque cursor pins paper, source, revision, source-PDF SHA, Middle bytes
  SHA, optional bundle SHA, renderer version, and canonical rendered text SHA.
  Changed artifacts/resolvers or conflicting explicit selectors are rejected.
- HTTP callers **must call `PeekCursor` before loading the artifact** to choose
  the pinned revision, instead of resolving today's current revision. The peek
  is not authentication or authorization: independently authorize every pin;
  final `Read` verifies the loaded bytes and rendering.
- Markdown is assembled once conceptually, then sliced. Concatenating all
  returned `content` strings exactly reconstructs the selected canonical text.
  Oversized blocks split at code-point offsets without loss/duplication. Page
  markers and separators are not duplicated on continuation. Range offsets
  count rendered block text only, excluding page-marker/separator prefixes,
  and use half-open intervals. Marker-only slices have no invented block range.
- The stable `ImageURL(member)` callback receives original safe relative member
  paths, including `images/` and nested directories. It should generate a
  revision-pinned download URL and escape each path component appropriately.

## Rendering scope and fidelity

This is a versioned **Go QAtlas renderer**, inspired by MinerU 4.0.10 Doclib
single-block reading semantics at upstream commit
`ed50cc15bc2c9bfb00520dadfe61979866e62236`. It is not a byte-identical port of every
DocVortex release and does not invoke Python. Rich span text/styles, inline math,
links/code, headings/anchors, display equations, visual bodies/captions,
recursive lists/indexes, code/algorithms, and HTML tables are handled from raw
block content. Simple tables become GFM; complex tables preserve sanitized HTML
geometry and relative embedded-image links. Unsupported future block types are
shown as bounded inert raw content with warnings, not dropped as whole documents.

Deliberate limits:

- No document-wide cross-page paragraph merging, crop generation, OCR,
  inference, or custom configurable LaTeX delimiter profiles.
- Sidecar images work via per-revision URLs. Missing assets and embedded base64
  images produce explicit warnings/placeholders; source PDF / raw artifacts
  remain the route for originals. Images are never fetched by this package.
- Complex HTML is sanitized (scripts, event handlers, unsafe URLs/styles are not
  served as active content). Some rich CSS/HTML presentation is therefore lost.
- A split very long block may leave a Markdown/HTML/math fragment incomplete in
  one window. Treat windows as text for agents or reassemble before rendering;
  do not assume every individual slice is an independent HTML-safe document.
- Empty selected pages have empty content and no fabricated block locator.
- Full rendering is deterministic but currently repeated per request rather
  than cached; cursors are routing data, not signed grants or access tokens.

## Tests

The focused package tests exercise Unicode continuation down to a one-character
budget, exact non-contiguous selectors/locators, malformed or wrong-pin cursors,
rich rendering, complex tables, original image paths, unknown blocks, and unsafe
markup. Genuine-artifact coverage is explicit and opt-in:

```sh
GOPROXY=off GOTOOLCHAIN=local go test ./internal/paperread
QATLAS_REAL_MINERU_ZIP=/path/to/existing/full.zip \
  GOPROXY=off GOTOOLCHAIN=local go test -v ./internal/paperread
```

The existing realpaper remote ZIP contains `middle_json.json` produced by MinerU
3.4.4, exported as the supported DocVortex Middle profile: 17 pages and 7 visual
image bodies. That is genuine producer evidence, not proof of every possible
4.0.10 engine/block subtype. Tests never parse a PDF or call a remote service.
