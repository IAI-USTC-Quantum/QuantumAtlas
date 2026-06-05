package paperassets

import (
	"errors"
	"testing"
)

// TestParse exercises the structured arXiv id parser across every
// recognized form + an explicit reject set. This is the contract the
// post-A1 storage-key layout, the markdown LRO handler, and the bare-id
// disambiguator all rely on; if any field starts drifting (e.g.
// Category lost on round-trip, IsBare set for new-style) the bucket
// layout reshuffles silently and prior-art objects 404.
func TestParse(t *testing.T) {
	t.Parallel()

	type want struct {
		canonical  string
		category   string
		stem       string
		stemBase   string
		version    string
		yymm       string
		isOldStyle bool
		isBare     bool
	}

	cases := []struct {
		name    string
		in      string
		want    want
		wantErr bool
	}{
		// --- new-style (post-April-2007) ---
		{
			name: "new-style 4-digit",
			in:   "2501.0001v1",
			want: want{canonical: "2501.0001v1", stem: "2501.0001v1", stemBase: "2501.0001", version: "v1", yymm: "2501"},
		},
		{
			name: "new-style 5-digit",
			in:   "2403.12345v2",
			want: want{canonical: "2403.12345v2", stem: "2403.12345v2", stemBase: "2403.12345", version: "v2", yymm: "2403"},
		},
		{
			name: "new-style 6-digit",
			in:   "2401.123456v1",
			want: want{canonical: "2401.123456v1", stem: "2401.123456v1", stemBase: "2401.123456", version: "v1", yymm: "2401"},
		},
		{
			name: "new-style without version",
			in:   "2501.00010",
			want: want{canonical: "2501.00010", stem: "2501.00010", stemBase: "2501.00010", version: "", yymm: "2501"},
		},
		{
			name: "new-style with arXiv prefix",
			in:   "arXiv: 2501.0001v3",
			want: want{canonical: "2501.0001v3", stem: "2501.0001v3", stemBase: "2501.0001", version: "v3", yymm: "2501"},
		},
		{
			name: "new-style upper-case V suffix normalized to lower",
			in:   "2501.0001V5",
			want: want{canonical: "2501.0001v5", stem: "2501.0001v5", stemBase: "2501.0001", version: "v5", yymm: "2501"},
		},

		// --- old-style canonical with category prefix ---
		{
			name: "old-style quant-ph",
			in:   "quant-ph/9508027v1",
			want: want{
				canonical: "quant-ph/9508027v1", category: "quant-ph",
				stem: "9508027v1", stemBase: "9508027", version: "v1", yymm: "9508",
				isOldStyle: true,
			},
		},
		{
			name: "old-style hep-th two-digit version",
			in:   "hep-th/0207001v12",
			want: want{
				canonical: "hep-th/0207001v12", category: "hep-th",
				stem: "0207001v12", stemBase: "0207001", version: "v12", yymm: "0207",
				isOldStyle: true,
			},
		},
		{
			name: "old-style cs.AI upper-case subcat",
			in:   "cs.AI/0101001v1",
			want: want{
				canonical: "cs.AI/0101001v1", category: "cs.AI",
				stem: "0101001v1", stemBase: "0101001", version: "v1", yymm: "0101",
				isOldStyle: true,
			},
		},
		{
			name: "old-style q-bio.NC upper-case subcat",
			in:   "q-bio.NC/0506013v2",
			want: want{
				canonical: "q-bio.NC/0506013v2", category: "q-bio.NC",
				stem: "0506013v2", stemBase: "0506013", version: "v2", yymm: "0506",
				isOldStyle: true,
			},
		},
		{
			name: "old-style math.AG upper-case subcat",
			in:   "math.AG/0207065v1",
			want: want{
				canonical: "math.AG/0207065v1", category: "math.AG",
				stem: "0207065v1", stemBase: "0207065", version: "v1", yymm: "0207",
				isOldStyle: true,
			},
		},
		{
			name: "old-style physics.atom-ph hyphenated subcat",
			in:   "physics.atom-ph/0001001v2",
			want: want{
				canonical: "physics.atom-ph/0001001v2", category: "physics.atom-ph",
				stem: "0001001v2", stemBase: "0001001", version: "v2", yymm: "0001",
				isOldStyle: true,
			},
		},
		{
			name: "old-style cond-mat.stat-mech hyphenated subcat",
			in:   "cond-mat.stat-mech/9912001v1",
			want: want{
				canonical: "cond-mat.stat-mech/9912001v1", category: "cond-mat.stat-mech",
				stem: "9912001v1", stemBase: "9912001", version: "v1", yymm: "9912",
				isOldStyle: true,
			},
		},
		{
			name: "old-style nlin.CD upper-case subcat",
			in:   "nlin.CD/0207065v1",
			want: want{
				canonical: "nlin.CD/0207065v1", category: "nlin.CD",
				stem: "0207065v1", stemBase: "0207065", version: "v1", yymm: "0207",
				isOldStyle: true,
			},
		},
		{
			name: "old-style math-ph no subcat",
			in:   "math-ph/0001001v1",
			want: want{
				canonical: "math-ph/0001001v1", category: "math-ph",
				stem: "0001001v1", stemBase: "0001001", version: "v1", yymm: "0001",
				isOldStyle: true,
			},
		},
		{
			name: "old-style gr-qc without version",
			in:   "gr-qc/0207065",
			want: want{
				canonical: "gr-qc/0207065", category: "gr-qc",
				stem: "0207065", stemBase: "0207065", version: "", yymm: "0207",
				isOldStyle: true,
			},
		},

		// --- old-style bare (catalog form, pre-A1) ---
		{
			name: "bare v1",
			in:   "9508027v1",
			want: want{
				canonical: "9508027v1",
				stem:      "9508027v1", stemBase: "9508027", version: "v1", yymm: "9508",
				isOldStyle: true, isBare: true,
			},
		},
		{
			name: "bare v3 0207065",
			in:   "0207065v3",
			want: want{
				canonical: "0207065v3",
				stem:      "0207065v3", stemBase: "0207065", version: "v3", yymm: "0207",
				isOldStyle: true, isBare: true,
			},
		},
		{
			name: "bare without version",
			in:   "9508027",
			want: want{
				canonical: "9508027",
				stem:      "9508027", stemBase: "9508027", version: "", yymm: "9508",
				isOldStyle: true, isBare: true,
			},
		},

		// --- rejects ---
		{name: "empty", in: "", wantErr: true},
		{name: "whitespace only", in: "   ", wantErr: true},
		{name: "garbage string", in: "foobar", wantErr: true},
		{name: "bare too short", in: "950802v1", wantErr: true},
		{name: "bare too long", in: "95080277v1", wantErr: true},
		{name: "bare with letters", in: "950abc7v1", wantErr: true},
		{name: "new-style too few digits", in: "2501.001v1", wantErr: true},
		{name: "new-style too many digits", in: "2501.1234567v1", wantErr: true},
		{name: "new-style with category", in: "quant-ph/2501.00010v1", wantErr: true},
		{name: "url instead of id", in: "https://arxiv.org/abs/2501.00010", wantErr: true},
		{name: "category without id body", in: "quant-ph/", wantErr: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Parse(%q) = %#v, want error", tc.in, got)
				}
				if !errors.Is(err, ErrInvalidArxivID) {
					t.Fatalf("Parse(%q) err = %v, want errors.Is ErrInvalidArxivID", tc.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) unexpected error: %v", tc.in, err)
			}
			if got.Canonical != tc.want.canonical {
				t.Errorf("Canonical: got %q, want %q", got.Canonical, tc.want.canonical)
			}
			if got.Category != tc.want.category {
				t.Errorf("Category: got %q, want %q", got.Category, tc.want.category)
			}
			if got.Stem != tc.want.stem {
				t.Errorf("Stem: got %q, want %q", got.Stem, tc.want.stem)
			}
			if got.StemBase != tc.want.stemBase {
				t.Errorf("StemBase: got %q, want %q", got.StemBase, tc.want.stemBase)
			}
			if got.Version != tc.want.version {
				t.Errorf("Version: got %q, want %q", got.Version, tc.want.version)
			}
			if got.YYMM != tc.want.yymm {
				t.Errorf("YYMM: got %q, want %q", got.YYMM, tc.want.yymm)
			}
			if got.IsOldStyle != tc.want.isOldStyle {
				t.Errorf("IsOldStyle: got %v, want %v", got.IsOldStyle, tc.want.isOldStyle)
			}
			if got.IsBare != tc.want.isBare {
				t.Errorf("IsBare: got %v, want %v", got.IsBare, tc.want.isBare)
			}
			if !got.IsValid() {
				t.Errorf("IsValid() = false on parsed value %#v", got)
			}
		})
	}
}

// TestParse_CategoryAmbiguity locks in the documented fact that the
// same bare 7-digit body resolves to different real papers across
// different categories — this is exactly why the storage layout had
// to gain category subdirectories in A1.
func TestParse_CategoryAmbiguity(t *testing.T) {
	t.Parallel()
	cases := []string{
		"quant-ph/0207065v1",
		"hep-th/0207065v1",
		"math.AG/0207065v1",
		"gr-qc/0207065v1",
	}
	keys := map[string]struct{}{}
	for _, in := range cases {
		p, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", in, err)
		}
		key := AssetKeyFor("pdf", p)
		if _, dup := keys[key]; dup {
			t.Errorf("AssetKey collision for %q: %q already seen", in, key)
		}
		keys[key] = struct{}{}
	}
	if len(keys) != len(cases) {
		t.Fatalf("expected %d distinct keys, got %d: %v", len(cases), len(keys), keys)
	}
}

// TestAssetKeyFor_LegacyForBare locks in the back-compat invariant:
// bare ids render to the LEGACY layout (no category) because the
// category is unknown — that's the only thing we can address. After
// migration completes the bucket no longer has any objects at the
// legacy path, so bare reads will naturally start 404'ing and force
// upstream callers to disambiguate.
func TestAssetKeyFor_LegacyForBare(t *testing.T) {
	t.Parallel()
	p := MustParse("9508027v1")
	got := AssetKeyFor("pdf", p)
	want := "pdf/9508/9508027v1.pdf"
	if got != want {
		t.Errorf("AssetKeyFor(pdf, bare) = %q, want %q", got, want)
	}
	// And LegacyAssetKeyFor returns empty (bare IS the legacy form,
	// no further fallback exists).
	if got := LegacyAssetKeyFor("pdf", p); got != "" {
		t.Errorf("LegacyAssetKeyFor(pdf, bare) = %q, want empty", got)
	}
}

// TestLegacyAssetKeyFor_DualReadFallback locks in the dual-read
// contract used by paperassets.Store.Get: given a canonical old-style
// id, the legacy variant points at the pre-A1 bucket location so any
// not-yet-migrated objects are still findable.
func TestLegacyAssetKeyFor_DualReadFallback(t *testing.T) {
	t.Parallel()
	p := MustParse("quant-ph/9508027v1")
	newKey := AssetKeyFor("pdf", p)
	legacyKey := LegacyAssetKeyFor("pdf", p)
	if newKey == legacyKey {
		t.Fatalf("dual-read pointless: newKey == legacyKey == %q", newKey)
	}
	if want := "pdf/9508/quant-ph/9508027v1.pdf"; newKey != want {
		t.Errorf("newKey = %q, want %q", newKey, want)
	}
	if want := "pdf/9508/9508027v1.pdf"; legacyKey != want {
		t.Errorf("legacyKey = %q, want %q", legacyKey, want)
	}
}

// TestLegacyAssetKeyFor_NewStyleEmpty: new-style ids have always lived
// at the same path, so there is no legacy variant.
func TestLegacyAssetKeyFor_NewStyleEmpty(t *testing.T) {
	t.Parallel()
	p := MustParse("2501.00010v1")
	if got := LegacyAssetKeyFor("pdf", p); got != "" {
		t.Errorf("LegacyAssetKeyFor(pdf, new-style) = %q, want empty", got)
	}
}

// TestMustParse panics on invalid input — guard the contract.
func TestMustParse_PanicsOnInvalid(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("MustParse(invalid) did not panic")
		}
	}()
	_ = MustParse("not an arxiv id")
}
