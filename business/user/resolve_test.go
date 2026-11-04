package user

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	modelAuth "doexam/models/auth"
	modelUser "doexam/models/user"
	"doexam/pkg/errr"
)

func resolveUsers() []modelAuth.UserEntity {
	return []modelAuth.UserEntity{
		{ID: "u1", Username: "budi", FullName: "Budi", ExternalID: ptr("0051"), Email: ptr("budi@x.id"), Status: modelAuth.UserStatusActive},
		{ID: "u2", Username: "ani", FullName: "Ani", ExternalID: ptr("0052"), Status: modelAuth.UserStatusSuspended},
		// u3's username equals u4's external id → that identifier is ambiguous
		{ID: "u3", Username: "7777", FullName: "Tujuh"},
		{ID: "u4", Username: "dewi", FullName: "Dewi", ExternalID: ptr("7777")},
	}
}

func TestMatchIdentifiers(t *testing.T) {
	got := matchIdentifiers([]string{"BUDI", "0052", "budi@x.id", "nobody", "7777", "0051"}, resolveUsers())

	ids := []string{}
	for _, f := range got.Found {
		ids = append(ids, f.ID+":"+string(f.MatchedBy)+":"+f.Input)
	}
	// budi found once (by username; later "budi@x.id" and "0051" are the same user)
	want := []string{"u1:username:BUDI", "u2:external_id:0052"}
	if !slices.Equal(ids, want) {
		t.Fatalf("found = %v, want %v", ids, want)
	}
	if !slices.Equal(got.NotFound, []string{"nobody"}) {
		t.Fatalf("not_found = %v", got.NotFound)
	}
	if !slices.Equal(got.Ambiguous, []string{"7777"}) {
		t.Fatalf("ambiguous = %v", got.Ambiguous)
	}
	if got.Found[1].Status != modelAuth.UserStatusSuspended {
		t.Fatal("status must be reported so callers can skip suspended users")
	}
}

func TestCleanIdentifiers(t *testing.T) {
	got := cleanIdentifiers([]string{" budi ", "", "Budi", "ani", "  "})
	if !slices.Equal(got, []string{"budi", "ani"}) {
		t.Fatalf("got %v", got)
	}
}

func TestResolve(t *testing.T) {
	store := newStore()
	store.users = map[string]modelAuth.UserEntity{}
	for _, u := range resolveUsers() {
		store.users[u.ID] = u
	}
	svc, ctx := newService(store), context.Background()
	groupP := modelAuth.Principal{TenantID: tenantID, UserID: adminID, Permissions: []string{modelAuth.PermGroupManage}}
	sessionP := modelAuth.Principal{TenantID: tenantID, UserID: adminID, Permissions: []string{modelAuth.PermSessionManage}}

	for _, p := range []modelAuth.Principal{adminP, groupP, sessionP} {
		res, err := svc.Resolve(ctx, p, modelUser.ResolveInput{Identifiers: []string{"budi", "x"}})
		if err != nil || len(res.Found) != 1 || len(res.NotFound) != 1 {
			t.Fatalf("perms %v: res=%+v err=%v", p.Permissions, res, err)
		}
	}
	if _, err := svc.Resolve(ctx, noPermsP, modelUser.ResolveInput{Identifiers: []string{"budi"}}); !errors.Is(err, errr.ErrForbidden) {
		t.Fatalf("participant: err = %v, want forbidden", err)
	}
	var ve *errr.ValidationError
	if _, err := svc.Resolve(ctx, adminP, modelUser.ResolveInput{Identifiers: []string{" ", ""}}); !errors.As(err, &ve) || ve.Fields["identifiers"] == "" {
		t.Fatalf("empty: err = %v", err)
	}
	many := make([]string, modelUser.MaxResolveIdentifiers+1)
	for i := range many {
		many[i] = fmt.Sprintf("u%d", i)
	}
	if _, err := svc.Resolve(ctx, adminP, modelUser.ResolveInput{Identifiers: many}); !errors.As(err, &ve) {
		t.Fatalf("too many: err = %v", err)
	}
}

func TestResolveFile(t *testing.T) {
	store := newStore()
	store.users = map[string]modelAuth.UserEntity{}
	for _, u := range resolveUsers() {
		store.users[u.ID] = u
	}
	svc, ctx := newService(store), context.Background()

	cases := []struct {
		name, file, data string
		wantFound        int
		wantField        bool
	}{
		{name: "external_id column picked over first column", file: "a.csv", data: "Nama,External ID\nBudi,0051\nAni,0052\n", wantFound: 2},
		{name: "first column fallback", file: "a.csv", data: "peserta\nbudi\ndewi\n", wantFound: 2},
		{name: "semicolon csv", file: "a.csv", data: "username;nama\nbudi;B\n", wantFound: 1},
		{name: "not a spreadsheet", file: "a.txt", data: "budi", wantField: true},
		{name: "empty column", file: "a.csv", data: "username,nama\n,Budi\n", wantField: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := svc.ResolveFile(ctx, adminP, tc.file, []byte(tc.data))
			var ve *errr.ValidationError
			if tc.wantField {
				if !errors.As(err, &ve) || ve.Fields["file"] == "" {
					t.Fatalf("err = %v, want file field error", err)
				}
				return
			}
			if err != nil || len(res.Found) != tc.wantFound {
				t.Fatalf("res = %+v, err = %v", res, err)
			}
		})
	}
	if _, err := svc.ResolveFile(ctx, noPermsP, "a.csv", []byte("username\nbudi\n")); !errors.Is(err, errr.ErrForbidden) {
		t.Fatalf("participant: err = %v", err)
	}
}
