package helps

import (
	"encoding/json"
	"testing"
)

func TestRepairObjectKeySeparatorsRestoresResponseCompleted(t *testing.T) {
	src := []byte(`{"type","response.completed","response",{"id","resp_0d4d13da22937119016ad2e5a053d887d0b7abf9efc5b51ddb","object","response","created_at":1792206240,"status","completed","output",[{"type","message","content",[{"type","output_text","text","hello, world"}]}]}}`)
	got, ok := RepairObjectKeySeparators(src)
	if !ok {
		t.Fatal("RepairObjectKeySeparators() ok = false")
	}
	want := `{"type":"response.completed","response":{"id":"resp_0d4d13da22937119016ad2e5a053d887d0b7abf9efc5b51ddb","object":"response","created_at":1792206240,"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello, world"}]}]}}`
	if string(got) != want {
		t.Fatalf("repaired = %s", got)
	}
}

func TestRepairObjectKeySeparatorsLeavesValidJSON(t *testing.T) {
	src := []byte(`{"a":"x,y","items":["p","q"],"n":1,"ok":true}`)
	if got, ok := RepairObjectKeySeparators(src); ok || got != nil {
		t.Fatalf("RepairObjectKeySeparators() = (%s, %t), want unchanged", got, ok)
	}
	if !json.Valid(src) {
		t.Fatal("fixture is not valid JSON")
	}
}

func TestRepairObjectKeySeparatorsRejectsUnrepairableJSON(t *testing.T) {
	src := []byte(`{"type":"response.completed","t":"`)
	if got, ok := RepairObjectKeySeparators(src); ok || got != nil {
		t.Fatalf("RepairObjectKeySeparators() = (%s, %t), want no repair", got, ok)
	}
}
