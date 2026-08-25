package main

import "testing"

// TestPapersBackfillMetadataCmdFlags pins the flag surface (names +
// defaults) of `papers backfill-metadata` so an accidental rename or
// default change is caught by unit tests, not by operators.
func TestPapersBackfillMetadataCmdFlags(t *testing.T) {
	cmd := newPapersBackfillMetadataCmd()

	batch := cmd.Flags().Lookup("batch")
	if batch == nil || batch.DefValue != "100" {
		t.Errorf("--batch default = %v, want 100", batch)
	}
	rps := cmd.Flags().Lookup("rps")
	if rps == nil || rps.DefValue != "0.33" {
		t.Errorf("--rps default = %v, want 0.33", rps)
	}
	limit := cmd.Flags().Lookup("limit")
	if limit == nil || limit.DefValue != "0" {
		t.Errorf("--limit default = %v, want 0", limit)
	}
	dryRun := cmd.Flags().Lookup("dry-run")
	if dryRun == nil || dryRun.DefValue != "false" {
		t.Errorf("--dry-run default = %v, want false", dryRun)
	}
	source := cmd.Flags().Lookup("source")
	if source == nil || source.DefValue != "arxiv" {
		t.Errorf("--source default = %v, want arxiv", source)
	}

	if err := cmd.Flags().Parse([]string{"--batch=50", "--rps=1", "--limit=500", "--dry-run"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	if got := cmd.Flags().Changed("batch"); !got {
		t.Error("--batch not marked changed after parse")
	}
}

// TestBackfillSourceDefaults pins the per-source retuning of --batch /
// --rps: the OpenAlex polite pool allows ~10 req/s where arXiv 429s
// aggressively, so openalex mode defaults to 50 DOIs at 5 req/s unless
// the operator set the flags explicitly. arxiv mode never retunes.
func TestBackfillSourceDefaults(t *testing.T) {
	cases := []struct {
		name                     string
		source                   string
		batchChanged, rpsChanged bool
		wantBatch                int
		wantRPS                  float64
	}{
		{"arxiv untouched", "arxiv", false, false, 100, 0.33},
		{"arxiv explicit kept", "arxiv", true, true, 100, 0.33},
		{"openalex retuned", "openalex", false, false, 50, 5},
		{"openalex batch kept", "openalex", true, false, 100, 5},
		{"openalex rps kept", "openalex", false, true, 50, 0.33},
	}
	for _, tc := range cases {
		f := papersBackfillMetadataFlags{source: tc.source, batch: 100, rps: 0.33}
		f.applySourceDefaults(tc.batchChanged, tc.rpsChanged)
		if f.batch != tc.wantBatch || f.rps != tc.wantRPS {
			t.Errorf("%s: batch=%d rps=%.2f, want %d/%.2f", tc.name, f.batch, f.rps, tc.wantBatch, tc.wantRPS)
		}
	}
}

// TestPapersCommandHasBackfill guards the subcommand wiring on the
// `papers` group.
func TestPapersCommandHasBackfill(t *testing.T) {
	root := NewPapersCommand()
	for _, name := range []string{"sync", "backfill-metadata"} {
		if c, _, err := root.Find([]string{name}); err != nil || c == nil || c.Name() != name {
			t.Errorf("papers %s: not wired (cmd=%v err=%v)", name, c, err)
		}
	}
}
