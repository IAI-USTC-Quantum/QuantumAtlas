package papers

import (
	"strconv"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
)

// ids bundles the several arxiv-id forms a paper_works row needs.
type ids struct {
	// ArxivID is the normalized identifier WITH version + category,
	// e.g. "quant-ph/9508027v1" or "2401.12345v1". Business primary key.
	ArxivID string
	// Canonical is version-stripped + category-stripped, e.g. "9508027"
	// or "2401.12345". Stored as arxiv_id_canonical.
	Canonical string
	// StorageKey is the bucket filename stem, e.g. "9508027v1" or
	// "2401.12345v1".
	StorageKey string
	// YYMM is the 4-digit shard, e.g. "9508" / "2401".
	YYMM string
}

// deriveIDs computes all id forms from a raw / canonical arxiv id.
func deriveIDs(arxivID string) ids {
	canonicalFull := paperassets.NormalizeIdentifier(arxivID)
	sk := paperassets.StorageKey(canonicalFull)
	return ids{
		ArxivID:    canonicalFull,
		Canonical:  paperassets.StripVersion(sk),
		StorageKey: sk,
		YYMM:       paperassets.Shard(sk),
	}
}

// bareArxivID returns the version-stripped canonical arxiv id used as
// papers.paper_arxiv_id — e.g. "2208.06941" or "quant-ph/9508027". The
// version lives per-asset in paper_assets.arxiv_version, not on the paper.
func bareArxivID(arxivID string) string {
	return paperassets.StripVersion(paperassets.NormalizeIdentifier(arxivID))
}

// arxivVersion returns the integer version suffix of an arxiv id (the "N"
// in "vN"), or 0 when the id carries no version. Upload ids always carry a
// version (paperassets.ValidateUploadID enforces vN), so a stored arxiv
// asset always has a positive version.
func arxivVersion(arxivID string) int {
	p, err := paperassets.Parse(arxivID)
	if err != nil || p.Version == "" {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimPrefix(p.Version, "v"))
	if err != nil {
		return 0
	}
	return n
}

// versionedArxivID reconstructs the full versioned id from a bare arxiv
// id + integer version — the inverse of bareArxivID + arxivVersion, e.g.
// ("2208.06941", 2) → "2208.06941v2". A zero version yields the bare id.
func versionedArxivID(bare string, version int) string {
	if version <= 0 {
		return bare
	}
	return bare + "v" + strconv.Itoa(version)
}

// bucketRelKey strips the leading "<kind>/" segment from an AssetKey,
// yielding the object key relative to a per-kind bucket, e.g.
// "pdf/9508/9508027.pdf" -> "9508/9508027.pdf".
func bucketRelKey(assetKey string) string {
	if i := strings.IndexByte(assetKey, '/'); i >= 0 {
		return assetKey[i+1:]
	}
	return assetKey
}

// ArxivAbsURL returns the canonical public arxiv.org PDF URL for an
// arxiv id, used for the compliance 307 redirect (we never serve our
// internal PDF bytes to end users). Version-stripped so arxiv serves the
// latest version.
func ArxivAbsURL(arxivID string) string {
	id := paperassets.StripVersion(paperassets.NormalizeIdentifier(arxivID))
	return "https://arxiv.org/pdf/" + id
}

// ArxivVersionedURL returns the arxiv.org PDF URL WITH its version
// suffix preserved, e.g. "https://arxiv.org/pdf/2401.12345v1". Used by
// the mineru-claim contract: contributors must fetch the exact version
// our catalog references so the sha256 verification on upload-mineru
// succeeds. arxiv treats version URLs as immutable — once "v1" is
// published its bytes never change, even if v2 supersedes it — which
// is what makes "ship arxiv URL + sha256 to the contributor" safe.
func ArxivVersionedURL(arxivID string) string {
	id := paperassets.NormalizeIdentifier(arxivID)
	return "https://arxiv.org/pdf/" + id
}
