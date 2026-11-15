package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func invalidTailscaleOwnershipBodies() map[string]string {
	const valid = `{"id":"user-1","loginName":"alice@example.com","type":"member","role":"member"}`
	wrap := func(record string) string { return `{"users":[` + record + `]}` }
	cases := map[string]string{
		"duplicate envelope":         `{"users":[],"users":[` + valid + `]}`,
		"aliased envelope":           `{"users":[` + valid + `],"Users":[]}`,
		"duplicate escaped envelope": `{"users":[],"` + string(rune(92)) + `u0075sers":[` + valid + `]}`,
		"null record":                `{"users":[null]}`,
		"non-object record":          `{"users":[[]]}`,
		"trailing document":          wrap(valid) + `{}`,
	}
	for _, field := range []string{"id", "loginName", "type", "role"} {
		value := map[string]string{"id": `"other-id"`, "loginName": `"bob@example.com"`, "type": `"shared"`, "role": `"admin"`}[field]
		cases["duplicate "+field] = wrap(valid[:len(valid)-1] + `,"` + field + `":` + value + `}`)
		alias := strings.ToUpper(field[:1]) + field[1:]
		cases["alias "+field] = wrap(valid[:len(valid)-1] + `,"` + alias + `":` + value + `}`)
		cases["alias only "+field] = wrap(strings.Replace(valid, `"`+field+`":`, `"`+alias+`":`, 1))
	}
	cases["escaped duplicate id"] = wrap(valid[:len(valid)-1] + `,"` + string(rune(92)) + `u0069d":"other-id"}`)
	for name, id := range map[string]string{
		"high surrogate": `"user-\ud800"`, "different high surrogate": `"user-\ud801"`,
		"low surrogate": `"user-\udc00"`, "broken pair": `"user-\ud800A"`,
		"invalid utf8": "\"user-" + string([]byte{0xff}) + "\"",
		"control":      `"user\u0000x"`, "overlong": `"` + strings.Repeat("x", 257) + `"`,
	} {
		cases["id "+name] = wrap(strings.Replace(valid, `"user-1"`, id, 1))
	}
	for name, login := range map[string]string{
		"bare": `"alice"`, "control": `"alice\u0000@example.com"`,
		"surrogate": `"alice\ud800@example.com"`, "padded": `" alice@example.com"`,
		"overlong":       `"` + strings.Repeat("a", 250) + `@example.com"`,
		"invalid utf8":   "\"alice" + string([]byte{0xff}) + "@example.com\"",
		"format control": `"alice​@example.com"`,
	} {
		cases["login "+name] = wrap(strings.Replace(valid, `"alice@example.com"`, login, 1))
	}
	return cases
}

func TestTailscaleUsersOwnershipWireRejections(t *testing.T) {
	for name, body := range invalidTailscaleOwnershipBodies() {
		t.Run(name, func(t *testing.T) {
			client, closeClient := tailscaleClientWithBodies(t, body, `{"devices":[]}`, `{"acls":[]}`)
			defer closeClient()
			result, err := client.Load(context.Background(), nil)
			var loadErr *controlPlaneLoadError
			if !errors.As(err, &loadErr) || loadErr.Stage != controlPlaneStageUsers || result != nil {
				t.Fatalf("invalid Users response published or wrong stage: result present=%v error=%v", result != nil, err)
			}
		})
	}
}

func TestCache_TailscaleOwnershipWireRejections(t *testing.T) {
	// Both a warm snapshot and a cold cache must reject every ambiguous/lossy
	// response, rather than retain a partial ownership map from a failed load.
	for name, body := range invalidTailscaleOwnershipBodies() {
		t.Run(name, func(t *testing.T) {
			u := newTestUpstreams(t)
			goodFixture := newTailscaleFixture(t)
			warm := NewCache(goodFixture.client, u.npm, time.Hour, discardLogger())
			if err := warm.refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			before := warm.Get()
			client, closeClient := tailscaleClientWithBodies(t, body, `{"devices":[]}`, `{"acls":[]}`)
			defer closeClient()
			warm.controlPlane = client
			cold := NewCache(client, u.npm, time.Hour, discardLogger())
			for _, cache := range []*Cache{warm, cold} {
				var loadErr *snapshotLoadError
				if err := cache.refresh(context.Background()); !errors.As(err, &loadErr) || loadErr.Stage != snapshotStageTailscaleUsers {
					t.Fatalf("wrong rejection: %v", err)
				}
			}
			want := map[string]TailscaleUserIdentity{"alice@example.com": {ID: "user-1", DirectMember: true}}
			if warm.Get() != before || cold.Get() != nil || !reflect.DeepEqual(before.TailscaleUsersByLogin, want) {
				t.Fatal("invalid ownership changed snapshot")
			}
		})
	}
}

func TestTailscaleUsersLosslessIdentityCompatibility(t *testing.T) {
	for name, tc := range map[string]struct{ raw, want string }{
		"numeric":                    {`123`, "123"},
		"opaque":                     {`"opaque:user/id"`, "opaque:user/id"},
		"normalized padding":         {`"  user-1  "`, "user-1"},
		"valid literal unicode":      {`"user-😀"`, "user-😀"},
		"valid scalar pair":          {`"user-` + string(rune(92)) + `ud83d` + string(rune(92)) + `ude00"`, "user-😀"},
		"literal escape":             {`"user-\\ud800"`, `user-\ud800`},
		"literal replacement scalar": {`"user-�"`, "user-�"},
		"maximum length":             {`"` + strings.Repeat("x", 256) + `"`, strings.Repeat("x", 256)},
	} {
		t.Run(name, func(t *testing.T) {
			body := `{"users":[{"id":` + tc.raw + `,"loginName":"Alice@example.com","type":"member","role":"owner","displayName":"Profile","unused":{"id":"nested","ID":"unused"}}],"otherProfileField":true}`
			client, closeClient := tailscaleClientWithBodies(t, body, `{"devices":[]}`, `{"acls":[]}`)
			defer closeClient()
			result, err := client.Load(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			got := result.TailscaleUsersByLogin["Alice@example.com"]
			if got.ID != tc.want || !got.DirectMember || len(result.TailscaleUsersByLogin) != 1 {
				t.Fatal("ownership compatibility changed")
			}
			if _, ok := result.TailscaleUsersByLogin["alice@example.com"]; ok {
				t.Fatal("login case folded")
			}
			snapshot := storageTestSnapshot()
			snapshot.Collections[0].Login = "Alice@example.com"
			snapshot.Collections[0].UserID = tc.want
			if _, err := serializePersonalSnapshot(snapshot); err != nil {
				t.Fatal("provider/storage ID contract differs", err)
			}
		})
	}
}

func TestTailscaleUsersValidationRejectsLossyRawIDs(t *testing.T) {
	for _, raw := range []string{`"user-\ud800"`, `"user-\ud801"`, `"user\u0000x"`, `"` + strings.Repeat("x", 257) + `"`} {
		result, err := validateTailscaleUsers([]tailscaleUserDTO{{ID: json.RawMessage(raw), LoginName: "alice@example.com", Type: json.RawMessage(`"member"`), Role: json.RawMessage(`"member"`)}})
		if err == nil || result != nil {
			t.Fatal("direct validator accepted invalid ID")
		}
	}
}
