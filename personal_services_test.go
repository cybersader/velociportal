package main

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func personalTestStore(t *testing.T) *PersonalServicesStore {
	t.Helper()
	s, err := NewPersonalServicesStore(&PersonalServicesConfig{Origin: "http://portal.example:8081", Host: "portal.example:8081"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func personalTestEntry(n int) PersonalService {
	return PersonalService{ID: fmt.Sprintf("ps_%032x", n+1), Name: fmt.Sprintf("Tool %d", n), URL: "https://server.tailnet.ts.net/app", Icon: "generic"}
}

func personalTestMutation(state PersonalServicesState, action string) PersonalServicesMutation {
	return PersonalServicesMutation{Action: action, Generation: state.Generation, Revision: state.Revision, Name: "Tool", URL: "https://server.tailnet.ts.net/app"}
}

func TestPersonalStoreCASIsolationAndEmpty(t *testing.T) {
	s := personalTestStore(t)
	a, err := s.Read("alice@example.com")
	if err != nil || a.Initialized || a.Revision != "0" || a.Document.Version != 1 || a.Document.Entries == nil || len(s.collections) != 0 {
		t.Fatal("GET allocated or malformed absent state")
	}
	create := personalTestMutation(a, "create")
	a, err = s.Mutate("alice@example.com", create)
	if err != nil || !a.Initialized || a.Revision != "1" || !validPersonalID(a.Document.Entries[0].ID) {
		t.Fatal("create failed", err)
	}
	if _, err := s.Mutate("alice@example.com", create); !errors.Is(err, errPersonalConflict) {
		t.Fatal("stale create did not conflict")
	}
	b, _ := s.Read("bob@example.com")
	if b.Initialized || len(b.Document.Entries) != 0 || b.Generation != a.Generation {
		t.Fatal("identity isolation")
	}
	id := a.Document.Entries[0].ID
	a.Document.Entries[0].Name = "caller corruption"
	original, _ := s.Read("alice@example.com")
	if original.Document.Entries[0].Name != "Tool" {
		t.Fatal("Read exposed stored slice")
	}
	update := personalTestMutation(a, "update")
	update.ID = id
	update.Name = "Changed"
	a, err = s.Mutate("alice@example.com", update)
	if err != nil || a.Revision != "2" || a.Document.Entries[0].Name != "Changed" {
		t.Fatal(err)
	}
	remove := PersonalServicesMutation{Action: "delete", Generation: a.Generation, Revision: a.Revision, ID: id}
	a, err = s.Mutate("alice@example.com", remove)
	if err != nil || !a.Initialized || a.Revision != "3" || len(a.Document.Entries) != 0 {
		t.Fatal("delete must keep initialized empty", err)
	}
	restore := PersonalServicesMutation{Action: "restore", Generation: a.Generation, Revision: a.Revision, Document: &PersonalServicesDocument{Version: 1, Entries: []PersonalService{personalTestEntry(0)}}}
	if _, err := s.Mutate("alice@example.com", restore); !errors.Is(err, errPersonalConflict) {
		t.Fatal("restore resurrected initialized empty")
	}
	if _, err := s.Read(" alice@example.com"); !errors.Is(err, errPersonalInvalid) {
		t.Fatal("padded identity")
	}
	for _, login := range []string{"alice", "alice@", "tag:machine@example.com", "a​@example.com", "a\x00@example.com", strings.Repeat("x", 250) + "@example.com"} {
		if _, err := s.Read(login); !errors.Is(err, errPersonalInvalid) {
			t.Fatalf("accepted noncanonical identity %q", login)
		}
	}
}

func TestPersonalStoreRestoreGenerationRaces(t *testing.T) {
	s := personalTestStore(t)
	state, _ := s.Read("alice@example.com")
	document := PersonalServicesDocument{Version: 1, Entries: []PersonalService{personalTestEntry(0)}}
	request := PersonalServicesMutation{Action: "restore", Generation: state.Generation, Revision: "0", Document: &document}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.Mutate("alice@example.com", request); results <- err }()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, errPersonalConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("restore not atomic")
	}
	document.Entries[0].Name = "changed outside store"
	state, _ = s.Read("alice@example.com")
	if state.Document.Entries[0].Name != "Tool 0" || state.Document.Entries[0].ID != personalTestEntry(0).ID {
		t.Fatal("restore did not preserve private ID/copy")
	}
	restarted := personalTestStore(t)
	if restarted.generation == s.generation {
		t.Fatal("generation reused")
	}
	if _, err := restarted.Mutate("alice@example.com", request); !errors.Is(err, errPersonalConflict) {
		t.Fatal("old generation accepted")
	}
	newState, _ := restarted.Read("alice@example.com")
	request.Generation = newState.Generation
	if _, err := restarted.Mutate("alice@example.com", request); err != nil {
		t.Fatal("explicit new-generation restore failed", err)
	}
}

func TestPersonalStoreBoundsAndAtomicFailure(t *testing.T) {
	s := personalTestStore(t)
	for i := 0; i < maxPersonalCollections; i++ {
		login := fmt.Sprintf("user%d@example.com", i)
		state, _ := s.Read(login)
		_, err := s.Mutate(login, PersonalServicesMutation{Action: "restore", Generation: state.Generation, Revision: state.Revision, Document: &PersonalServicesDocument{Version: 1, Entries: []PersonalService{}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	absent, _ := s.Read("extra@example.com")
	if absent.Initialized || len(s.collections) != maxPersonalCollections {
		t.Fatal("full map GET behavior")
	}
	if _, err := s.Mutate("extra@example.com", personalTestMutation(absent, "create")); !errors.Is(err, errPersonalCapacity) {
		t.Fatal("collection cap")
	}
	state, _ := s.Read("user0@example.com")
	if _, err := s.Mutate("user0@example.com", personalTestMutation(state, "create")); err != nil {
		t.Fatal("existing collection cannot mutate at cap", err)
	}
	for _, bad := range []string{"bad", "PS_00000000000000000000000000000001", "ps_0000000000000000000000000000000G"} {
		entry := personalTestEntry(0)
		entry.ID = bad
		if err := validatePersonalDocument(PersonalServicesDocument{Version: 1, Entries: []PersonalService{entry}}); !errors.Is(err, errPersonalInvalid) {
			t.Fatal("invalid restore ID")
		}
	}
	duplicate := personalTestEntry(0)
	if err := validatePersonalDocument(PersonalServicesDocument{Version: 1, Entries: []PersonalService{duplicate, duplicate}}); !errors.Is(err, errPersonalInvalid) {
		t.Fatal("duplicate restore ID")
	}
	entries := make([]PersonalService, maxPersonalEntries)
	for i := range entries {
		entries[i] = personalTestEntry(i)
	}
	if err := validatePersonalDocument(PersonalServicesDocument{Version: 1, Entries: entries}); err != nil {
		t.Fatal(err)
	}
	if err := validatePersonalDocument(PersonalServicesDocument{Version: 1, Entries: append(entries, personalTestEntry(129))}); !errors.Is(err, errPersonalCapacity) {
		t.Fatal("entry cap")
	}
	for i := range entries {
		entries[i].URL = "https://server.tailnet.ts.net/" + strings.Repeat("a", 1000)
	}
	if err := validatePersonalDocument(PersonalServicesDocument{Version: 1, Entries: entries}); !errors.Is(err, errPersonalCapacity) {
		t.Fatal("canonical byte cap")
	}
	fresh := personalTestStore(t)
	before, _ := fresh.Read("alice@example.com")
	request := personalTestMutation(before, "create")
	request.Icon = "https://icons.example/icon.svg"
	if _, err := fresh.Mutate("alice@example.com", request); !errors.Is(err, errPersonalInvalid) {
		t.Fatal("arbitrary icon")
	}
	after, _ := fresh.Read("alice@example.com")
	if !reflect.DeepEqual(before, after) || len(fresh.collections) != 0 {
		t.Fatal("failed validation mutated state")
	}
}

func TestPersonalURLCanonicalBounds(t *testing.T) {
	for _, good := range []struct {
		url  string
		port int
	}{
		{"https://server.tailnet.ts.net", 443}, {"https://server.tailnet.ts.net/", 443}, {"https://server.tailnet.ts.net:8443/app/start", 8443}, {"https://server.tailnet.ts.net:1/app", 1}, {"https://server.tailnet.ts.net:65535/app", 65535}, {"https://server.tailnet.ts.net/%E2%98%83", 443},
	} {
		host, port, err := validatePersonalURL(good.url)
		if err != nil || host != "server.tailnet.ts.net" || port != good.port {
			t.Fatalf("good URL %q: %s %d %v", good.url, host, port, err)
		}
	}
	for _, bad := range []string{
		"", " https://server.tailnet.ts.net", "https://server.tailnet.ts.net ", "http://server.tailnet.ts.net", "HTTPS://server.tailnet.ts.net", "https://SERVER.tailnet.ts.net", "https://server.tailnet.ts.net.", "https://server.ts.net", "https://example.com", "https://server.tailnet.ts.net.evil.example", "https://*.tailnet.ts.net", "https://100.64.0.1", "https://[fd7a:115c:a1e0::1]", "https://a@server.tailnet.ts.net", "https://server.tailnet.ts.net?", "https://server.tailnet.ts.net?pair=secret", "https://server.tailnet.ts.net#", "https://server.tailnet.ts.net/#secret", "https://server.tailnet.ts.net:443", "https://server.tailnet.ts.net:0443", "https://server.tailnet.ts.net:0", "https://server.tailnet.ts.net:65536", "https://server.tailnet.ts.net:", "https://server.tailnet.ts.net//evil", "https://server.tailnet.ts.net/../app", "https://server.tailnet.ts.net/./app", "https://server.tailnet.ts.net/%2e%2e/app", "https://server.tailnet.ts.net/%2Fapp", "https://server.tailnet.ts.net/%5Cevil", "https://server.tailnet.ts.net/%252Fapp", "https://server.tailnet.ts.net/%00", "https://server.tailnet.ts.net/%0a", "https://server.tailnet.ts.net/%20x", "https://server.tailnet.ts.net/%FF", "https://server.tailnet.ts.net/☃", "https://sérver.tailnet.ts.net", "https://server.tailnet.ts.net/​", "https://server.tailnet.ts.net/%E2%80%8B",
	} {
		if _, _, err := validatePersonalURL(bad); err == nil {
			t.Errorf("accepted noncanonical URL %q", bad)
		}
	}
	prefix := "https://server.tailnet.ts.net/"
	if _, _, err := validatePersonalURL(prefix + strings.Repeat("a", maxServiceMetadataURL-len(prefix))); err != nil {
		t.Fatal("URL bound rejected", err)
	}
	if _, _, err := validatePersonalURL(prefix + strings.Repeat("a", maxServiceMetadataURL-len(prefix)+1)); err == nil {
		t.Fatal("URL bound exceeded")
	}
	entry := personalTestEntry(0)
	entry.Name = strings.Repeat("界", 120)
	if err := validatePersonalDocument(PersonalServicesDocument{Version: 1, Entries: []PersonalService{entry}}); err != nil {
		t.Fatal("Unicode name bound", err)
	}
	entry.Name += "界"
	if err := validatePersonalDocument(PersonalServicesDocument{Version: 1, Entries: []PersonalService{entry}}); err == nil {
		t.Fatal("Unicode name bound exceeded")
	}
}

func personalProjectionFixture(t *testing.T) (*Identity, *CacheData, PersonalServicesState, time.Time) {
	now := time.Unix(1800000000, 0)
	data := machineMatcherFixture(t)
	data.UpdatedAt = now
	data.Policy.Grants[0].IPCapabilities = []grantIPCapability{mustGrantCapability(t, "tcp:443")}
	state := PersonalServicesState{Initialized: true, Document: PersonalServicesDocument{Version: 1, Entries: []PersonalService{personalTestEntry(0)}}}
	return &Identity{Login: "alice@example.com"}, data, state, now
}

func TestPersonalProjectionRequiresCurrentUniqueNodeAndPositiveTCPGrant(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Identity, *CacheData, *PersonalServicesState, *time.Time)
		active bool
	}{
		{"positive", func(*Identity, *CacheData, *PersonalServicesState, *time.Time) {}, true},
		{"no SSH needed", func(_ *Identity, d *CacheData, _ *PersonalServicesState, _ *time.Time) {
			d.Policy.SSH = SSHPolicy{}
			d.MachineSSHCapableByID = nil
		}, true},
		{"wrong port", func(_ *Identity, d *CacheData, _ *PersonalServicesState, _ *time.Time) {
			d.Policy.Grants[0].IPCapabilities = []grantIPCapability{mustGrantCapability(t, "tcp:22")}
		}, false},
		{"UDP only", func(_ *Identity, d *CacheData, _ *PersonalServicesState, _ *time.Time) {
			d.Policy.Grants[0].IPCapabilities = []grantIPCapability{mustGrantCapability(t, "udp:443")}
		}, false},
		{"ACL only", func(_ *Identity, d *CacheData, _ *PersonalServicesState, _ *time.Time) {
			d.Policy.Grants = nil
			d.Policy.ACLs = []ACLRule{{Action: "accept", Src: []string{"*"}, Dst: []string{"*"}}}
		}, false},
		{"absent policy", func(_ *Identity, d *CacheData, _ *PersonalServicesState, _ *time.Time) { d.Policy = nil }, false},
		{"headscale", func(_ *Identity, d *CacheData, _ *PersonalServicesState, _ *time.Time) {
			d.ControlPlane.Provider = controlPlaneHeadscale
		}, false},
		{"cold", func(_ *Identity, d *CacheData, _ *PersonalServicesState, _ *time.Time) { d.UpdatedAt = time.Time{} }, false},
		{"future", func(_ *Identity, d *CacheData, _ *PersonalServicesState, n *time.Time) {
			d.UpdatedAt = n.Add(time.Second)
		}, false},
		{"stale", func(_ *Identity, d *CacheData, _ *PersonalServicesState, n *time.Time) {
			d.UpdatedAt = n.Add(-90*time.Second - time.Nanosecond)
		}, false},
		{"boundary fresh", func(_ *Identity, d *CacheData, _ *PersonalServicesState, n *time.Time) {
			d.UpdatedAt = n.Add(-90 * time.Second)
		}, true},
		{"rename", func(_ *Identity, d *CacheData, _ *PersonalServicesState, _ *time.Time) {
			d.Nodes[0].Name = "renamed.tailnet.ts.net"
		}, false},
		{"ambiguous normalized", func(_ *Identity, d *CacheData, _ *PersonalServicesState, _ *time.Time) {
			node := d.Nodes[0]
			node.ID = "node-2"
			node.Name = "server.tailnet.ts.net"
			d.Nodes = append(d.Nodes, node)
		}, false},
		{"hostname replacement", func(_ *Identity, d *CacheData, _ *PersonalServicesState, _ *time.Time) { d.Nodes[0].ID = "replacement" }, true},
		{"missing", func(_ *Identity, d *CacheData, _ *PersonalServicesState, _ *time.Time) { d.Nodes = nil }, false},
		{"malformed hostname", func(_ *Identity, d *CacheData, _ *PersonalServicesState, _ *time.Time) {
			d.Nodes[0].Name = "server.tailnet.ts.net.."
		}, false},
		{"unsafe address", func(_ *Identity, d *CacheData, _ *PersonalServicesState, _ *time.Time) {
			d.Nodes[0].Addresses = []string{"192.168.1.2"}
		}, false},
		{"shared user", func(_ *Identity, d *CacheData, _ *PersonalServicesState, _ *time.Time) {
			d.GrantRoleSelectorsByLogin = nil
		}, false},
		{"padded login", func(i *Identity, _ *CacheData, _ *PersonalServicesState, _ *time.Time) {
			i.Login = " alice@example.com "
		}, false},
		{"case mismatch", func(i *Identity, _ *CacheData, _ *PersonalServicesState, _ *time.Time) { i.Login = "Alice@example.com" }, false},
		{"bare login", func(i *Identity, _ *CacheData, _ *PersonalServicesState, _ *time.Time) { i.Login = "alice" }, false},
		{"short policy login", func(_ *Identity, d *CacheData, _ *PersonalServicesState, _ *time.Time) {
			d.Policy.Grants[0].BrowserSrc = []string{"alice@"}
		}, false},
		{"machine source", func(_ *Identity, d *CacheData, _ *PersonalServicesState, _ *time.Time) {
			d.Policy.Grants[0].BrowserSrc = nil
			d.Policy.Grants[0].Src = []string{"tag:server"}
			d.Nodes[0].OwnerLogin = "alice@example.com"
		}, false},
		{"exact group", func(_ *Identity, d *CacheData, _ *PersonalServicesState, _ *time.Time) {
			d.Policy.Groups = map[string][]string{"group:humans": {"alice@example.com"}}
			d.Policy.Grants[0].BrowserSrc = []string{"group:humans"}
		}, true},
		{"bare group member", func(_ *Identity, d *CacheData, _ *PersonalServicesState, _ *time.Time) {
			d.Policy.Groups = map[string][]string{"group:humans": {"alice@"}}
			d.Policy.Grants[0].BrowserSrc = []string{"group:humans"}
		}, false},
		{"authoritative role", func(_ *Identity, d *CacheData, _ *PersonalServicesState, _ *time.Time) {
			d.Policy.Grants[0].BrowserSrc = []string{"autogroup:member"}
		}, true},
		{"role not implied", func(_ *Identity, d *CacheData, _ *PersonalServicesState, _ *time.Time) {
			d.Policy.Grants[0].BrowserSrc = []string{"autogroup:admin"}
		}, false},
		{"ingress port", func(_ *Identity, d *CacheData, s *PersonalServicesState, _ *time.Time) {
			s.Document.Entries[0].URL = "https://server.tailnet.ts.net:8443/app"
			d.Policy.Grants[0].IPCapabilities = []grantIPCapability{mustGrantCapability(t, "tcp:8443")}
		}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			identity, data, state, now := personalProjectionFixture(t)
			test.change(identity, data, &state, &now)
			before := copyPersonalDocument(state.Document)
			cards, statuses := ProjectPersonalServices(identity, data, state, now, 30*time.Second)
			if (len(cards) == 1) != test.active || len(statuses) != 1 || (statuses[0].Status == "active") != test.active {
				t.Fatalf("cards=%v statuses=%v", cards, statuses)
			}
			if !reflect.DeepEqual(before, state.Document) {
				t.Fatal("projection changed authored data")
			}
			if test.active && (cards[0].ID != 0 || cards[0].PersonalID != state.Document.Entries[0].ID || cards[0].Category != "" || cards[0].Order != nil) {
				t.Fatal("personal/NPM source collision")
			}
		})
	}
}
