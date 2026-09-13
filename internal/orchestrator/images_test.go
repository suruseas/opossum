package orchestrator_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/suruseas/opossum/internal/orchestrator"
)

// The JSON form of `images` must carry the same columns as the table
// (SERVICE, IMAGE, SOURCE, PRESENT — as a real boolean, not "yes"/"no") and
// round-trip through encoding/json.
func TestImagesRendersJSON(t *testing.T) {
	rt, _ := fakeShim(t)
	setShimEnv(rt, "IMAGE_ABSENT=postgres:16") // the pulled image isn't present locally
	var out bytes.Buffer
	if err := orchestrator.New(imageProject(), rt, "opossum", &out).Images(orchestrator.ImagesOptions{Format: "json"}); err != nil {
		t.Fatalf("Images: %v", err)
	}
	// imageProject defines "web" (built) and "db" (pulled); StartupOrder is
	// alphabetical for independent services, so db sorts before web.
	want := []orchestrator.ImageStatus{
		{Service: "db", Image: "postgres:16", Source: "pulled", Present: false},
		{Service: "web", Image: "demo-web:latest", Source: "built", Present: true},
	}
	var got []orchestrator.ImageStatus
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
