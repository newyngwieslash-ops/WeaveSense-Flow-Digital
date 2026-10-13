package app

import "testing"

func TestResearchParksOnQueuedSynthesisTask(t *testing.T) {
	if id, ok := asyncOperationID([]byte(`{"status":"succeeded","synthesis":{"status":"queued","operation":{"id":"mt_1","status":"queued"}}}`)); !ok || id != "mt_1" {
		t.Fatalf("queued synthesis must park: %q %v", id, ok)
	}
	if _, ok := asyncOperationID([]byte(`{"status":"succeeded","synthesis":{"body":"x","claims":[]}}`)); ok {
		t.Fatal("inline synthesis must not park")
	}
}
