package opt

import (
	"encoding/json"
	"testing"
)

func TestNullable(t *testing.T) {
	var in struct {
		A Nullable[int] `json:"a"`
		B Nullable[int] `json:"b"`
		C Nullable[int] `json:"c"`
	}
	if err := json.Unmarshal([]byte(`{"a": 5, "b": null}`), &in); err != nil {
		t.Fatal(err)
	}
	if !in.A.Set || in.A.Null || *in.A.Ptr() != 5 {
		t.Fatalf("a = %+v", in.A)
	}
	if !in.B.Set || !in.B.Null || in.B.Ptr() != nil {
		t.Fatalf("b = %+v", in.B)
	}
	if in.C.Set {
		t.Fatalf("c must be unset: %+v", in.C)
	}
	if err := json.Unmarshal([]byte(`{"a": "x"}`), &in); err == nil {
		t.Fatal("wrong type must fail")
	}
}
