package routes

// Boot smoke test: registering the /api/papers routes must not panic —
// the bare "/api/papers" list route and the "/api/papers/{path...}"
// catch-all overlap textually and Go's mux panics on conflicting
// patterns at registration time.

import (
	"net/http"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

func TestRegisterPapersBootNoPanic(t *testing.T) {
	app := pocketbase.New()
	se := &core.ServeEvent{
		App: app,
		Router: router.NewRouter(func(w http.ResponseWriter, r *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
			event := new(core.RequestEvent)
			event.App = app
			event.Response = w
			event.Request = r
			return event, nil
		}),
	}
	RegisterPapers(se, &config.Config{}, nil, nil, nil, nil, nil, nil, nil)
}
