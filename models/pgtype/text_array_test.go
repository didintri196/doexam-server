package pgtype

import (
	"reflect"
	"testing"
)

func TestTextArrayScan(t *testing.T) {
	tests := []struct {
		in   any
		want TextArray
	}{
		{in: "{}", want: TextArray{}},
		{in: "{item.read,exam.write}", want: TextArray{"item.read", "exam.write"}},
		{in: []byte(`{"a,b","c\"d",e}`), want: TextArray{"a,b", `c"d`, "e"}},
		{in: nil, want: nil},
	}
	for _, tt := range tests {
		var got TextArray
		if err := got.Scan(tt.in); err != nil {
			t.Fatalf("Scan(%v): %v", tt.in, err)
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("Scan(%v) = %#v, want %#v", tt.in, got, tt.want)
		}
	}
}

func TestTextArrayRoundTrip(t *testing.T) {
	in := TextArray{"plain", "with,comma", `with"quote`, `back\slash`}
	v, _ := in.Value()
	var out TextArray
	if err := out.Scan(v); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip = %#v, want %#v", out, in)
	}
}
