package routes

import (
	"net/http/httptest"
	"testing"

	"github.com/pocketbase/pocketbase/core"
)

func TestImmutableOriginalNamespacesAreNotConvenienceImagePaths(t *testing.T) {
	for _, path := range []string{
		"qa_example/parses/pr_example/files/images/producer-original.jpg",
		"qa_example/parses/pr_example/files/nested/images/original.json",
		"qa_example/parses/pr_example/files/read",
		"qa_example/parses/pr_example/files/images/zip",
		"qa_example/sources/src_example/pdf",
	} {
		t.Run(path, func(t *testing.T) {
			event := &core.RequestEvent{}
			event.Request = httptest.NewRequest("GET", "/api/papers/"+path, nil)
			recorder := httptest.NewRecorder()
			event.Response = recorder
			handled, err := dispatchContentGET(event, nil, nil, nil, nil, path)
			if handled || err != nil || recorder.Body.Len() != 0 {
				t.Fatalf("immutable original namespace stolen: handled=%v error=%v response=%s", handled, err, recorder.Body.String())
			}
		})
	}
}
