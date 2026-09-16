package search

// Run against the sibling qatlas-search tests/scoring_contract_server.py fixture.
// This exercises real FastAPI JSON, compilation, filtering and ranking over TCP
// with synthetic papers/model output, not an external LLM or production registry.
import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

func TestRemoteScoringLiveContract(t *testing.T) {
	base := os.Getenv("QATLAS_SCORING_CONTRACT_URL")
	if base == "" {
		t.Skip("set QATLAS_SCORING_CONTRACT_URL to the offline Python fixture")
	}
	p := NewRemoteProvider(base, "contract-test-token", 5*time.Second)
	ctx := context.Background()
	capabilities, err := p.ScoringCapabilities(ctx)
	if err != nil || capabilities["generation_available"] != true {
		t.Fatalf("capabilities: %v %v", capabilities, err)
	}
	generated, err := p.GenerateScorer(ctx, ScorerGenerateRequest{Query: "quantum", Requirements: "Only papers since 2020; score citations minus two"})
	if err != nil || generated.Usage.LLMTokens != 41 || !validGeneratedScorer(generated.Scorer) {
		t.Fatalf("generation: %+v %v", generated, err)
	}
	out, err := p.SearchRanked(ctx, RankedSearchRequest{Text: "quantum", Sources: []string{"contract_fixture"}, MaxResults: 10, Scorer: generated.Scorer, Explain: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Hits) != 2 || out.Hits[0].Title != "Recent leader" || out.Hits[0].Score != 18 || out.Hits[1].Score != -2 || len(out.Hits[0].ScoreExplanation) == 0 {
		t.Fatalf("scoring order/filter/metadata: %+v", out)
	}
	// Real compiler positions must survive the Python -> Go error contract.
	_, err = p.SearchRanked(ctx, RankedSearchRequest{Text: "quantum", Sources: []string{"contract_fixture"}, MaxResults: 10, Scorer: json.RawMessage(`{"language":"qatlas-expr-v1","score":"unknown_feature"}`)}, nil)
	var remote *ScoringRemoteError
	if !errors.As(err, &remote) || remote.Status != 422 || remote.Detail.Position["line"] != 1 {
		t.Fatalf("compiler error: %#v", err)
	}
}
