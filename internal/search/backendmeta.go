package search

// backendmeta.go: a static, compile-time mirror of the qatlas-search
// backend catalog. The live source of truth is the microservice's
// GET /v1/backends (RemoteProvider.ListBackends); this table is the
// fallback when the microservice is unreachable and the validation
// vocabulary for the /api/me/search-keys writes (a key can only be
// stored for a backend that actually has a user-key slot). Keep it in
// sync with qatlas-search's backends registry (singletons + class attrs)
// — the order mirrors the Python registry's display order.

// BackendCategory groups the backend picker in the SPA.
const (
	BackendCategoryAcademic = "academic"
	BackendCategoryWeb      = "web"
)

// BackendMeta describes one search backend for the user-facing catalog.
// UserKey marks backends whose API key a user may configure in the
// dashboard (RequiresKey=false + UserKey=true means "works without a
// key, key optional").
type BackendMeta struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Category    string `json:"category"`
	RequiresKey bool   `json:"requires_key"`
	UserKey     bool   `json:"user_key"`
}

// BackendCatalog is the fallback backend table; order mirrors the
// qatlas-search registry.
var BackendCatalog = []BackendMeta{
	{Name: "arxiv", Label: "arXiv", Category: BackendCategoryAcademic},
	{Name: "openalex", Label: "OpenAlex", Category: BackendCategoryAcademic, UserKey: true},
	{Name: "semantic_scholar", Label: "Semantic Scholar", Category: BackendCategoryAcademic, UserKey: true},
	{Name: "crossref", Label: "Crossref", Category: BackendCategoryAcademic},
	{Name: "pubmed", Label: "PubMed", Category: BackendCategoryAcademic, UserKey: true},
	{Name: "europepmc", Label: "Europe PMC", Category: BackendCategoryAcademic},
	{Name: "dblp", Label: "DBLP", Category: BackendCategoryAcademic},
	{Name: "doaj", Label: "DOAJ", Category: BackendCategoryAcademic},
	{Name: "openaire", Label: "OpenAIRE", Category: BackendCategoryAcademic},
	{Name: "core", Label: "CORE", Category: BackendCategoryAcademic, RequiresKey: true, UserKey: true},
	{Name: "ads", Label: "NASA ADS", Category: BackendCategoryAcademic, RequiresKey: true, UserKey: true},
	{Name: "springer", Label: "Springer Nature", Category: BackendCategoryAcademic, RequiresKey: true, UserKey: true},
	{Name: "ieee", Label: "IEEE Xplore", Category: BackendCategoryAcademic, RequiresKey: true, UserKey: true},
	{Name: "scopus", Label: "Scopus", Category: BackendCategoryAcademic, RequiresKey: true, UserKey: true},
	{Name: "catalog", Label: "Catalog (QuantumAtlas)", Category: BackendCategoryAcademic},
	{Name: "rag", Label: "RAG (qatlas-rag)", Category: BackendCategoryAcademic},
	{Name: "wikipedia", Label: "Wikipedia", Category: BackendCategoryWeb},
	{Name: "searxng", Label: "SearXNG", Category: BackendCategoryWeb},
	{Name: "tavily", Label: "Tavily", Category: BackendCategoryWeb, RequiresKey: true, UserKey: true},
	{Name: "exa", Label: "Exa", Category: BackendCategoryWeb, RequiresKey: true, UserKey: true},
	{Name: "serper", Label: "Google (Serper)", Category: BackendCategoryWeb, RequiresKey: true, UserKey: true},
	{Name: "brave", Label: "Brave Search", Category: BackendCategoryWeb, RequiresKey: true, UserKey: true},
	{Name: "kagi", Label: "Kagi", Category: BackendCategoryWeb, RequiresKey: true, UserKey: true},
}

// LookupBackendMeta finds a catalog entry by backend name.
func LookupBackendMeta(name string) (BackendMeta, bool) {
	for _, m := range BackendCatalog {
		if m.Name == name {
			return m, true
		}
	}
	return BackendMeta{}, false
}

// UserKeyBackendNames lists the backend names that accept a per-user
// API key (the validation vocabulary of /api/me/search-keys).
func UserKeyBackendNames() []string {
	var out []string
	for _, m := range BackendCatalog {
		if m.UserKey {
			out = append(out, m.Name)
		}
	}
	return out
}
