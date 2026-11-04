package attempt

import (
	"context"
	"slices"
	"testing"

	modelMedia "doexam/models/media"
)

type fakeLinker struct{ asked []string }

func (f *fakeLinker) Refs(_ context.Context, _ string, ids []string) (map[string]modelMedia.Ref, error) {
	f.asked = ids
	out := map[string]modelMedia.Ref{}
	for _, id := range ids {
		out[id] = modelMedia.Ref{URL: "/api/v1/media/" + id + "?sig", Mime: "image/png"}
	}
	return out, nil
}

func TestMediaRefs(t *testing.T) {
	a, b := "0192a000-0000-7000-8000-00000000000a", "0192a000-0000-7000-8000-00000000000b"
	l := &fakeLinker{}
	svc := &AttemptService{media: l}
	refs, err := svc.mediaRefs(context.Background(), "t1",
		`<p>stem <img data-media-id="`+a+`"></p>`, `opsi`, `<img data-media-id="`+b+`"><img data-media-id="`+a+`">`)
	if err != nil || len(refs) != 2 || !slices.Equal(l.asked, []string{a, b, a}) && len(l.asked) != 3 {
		t.Fatalf("refs = %v err = %v asked = %v", refs, err, l.asked)
	}
	// no media: no lookup, empty map (never null in JSON)
	l.asked = nil
	refs, err = svc.mediaRefs(context.Background(), "t1", "<p>tanpa gambar</p>")
	if err != nil || refs == nil || len(refs) != 0 || l.asked != nil {
		t.Fatalf("no media: refs = %v asked = %v", refs, l.asked)
	}
	// tests build the service without a linker
	if refs, err := (&AttemptService{}).mediaRefs(context.Background(), "t1", `<img data-media-id="`+a+`">`); err != nil || len(refs) != 0 {
		t.Fatalf("nil linker: %v %v", refs, err)
	}
}
