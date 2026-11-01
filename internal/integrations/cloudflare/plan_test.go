package cloudflare

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

var testDomain = uuid.MustParse("11111111-1111-1111-1111-111111111111")

func owned(r Record) Existing {
	r.Comment = Marker(testDomain)
	return Existing{Record: r, Owned: true}
}

func foreign(r Record) Existing {
	return Existing{Record: r}
}

func a(id, ip string, proxied bool) Record {
	return Record{ID: id, Type: "A", Name: "app.example.com", Content: ip, Proxied: proxied}
}

func cname(id, target string) Record {
	return Record{ID: id, Type: "CNAME", Name: "app.example.com", Content: target, Proxied: true}
}

func plan(want []Target, recs []Existing, adopt bool) Plan {
	return BuildPlan(PlanInput{OwnerID: testDomain, Host: "app.example.com", Want: want, Records: recs, Adopt: adopt})
}

func wantA(proxied bool) []Target {
	return []Target{{Type: "A", Content: "203.0.113.10", Proxied: proxied}}
}

func TestMarkerRoundTrip(t *testing.T) {
	require.Equal(t, "nebulactrl:"+testDomain.String(), Marker(testDomain))
	id, ok := ParseMarker(Marker(testDomain))
	require.True(t, ok)
	require.Equal(t, testDomain, id)
	for _, c := range []string{"", "hello", "nebulactrl:", "nebulactrl:not-a-uuid", "NebulaCtrl managed"} {
		_, ok := ParseMarker(c)
		require.False(t, ok, c)
	}
}

func TestTargets(t *testing.T) {
	v4, ok := TargetForIP(" 203.0.113.10 ", true)
	require.True(t, ok)
	require.Equal(t, Target{Type: "A", Content: "203.0.113.10", Proxied: true}, v4)
	v6, ok := TargetForIP("2001:db8::1", false)
	require.True(t, ok)
	require.Equal(t, "AAAA", v6.Type)
	mapped, _ := TargetForIP("::ffff:203.0.113.10", false)
	require.Equal(t, Target{Type: "A", Content: "203.0.113.10"}, mapped)
	_, ok = TargetForIP("not-an-ip", false)
	require.False(t, ok)
	_, ok = TargetForIP("0.0.0.0", false)
	require.False(t, ok)
	require.True(t, Target{Type: "A", Content: "10.0.0.5"}.IsPrivate())
	require.False(t, Target{Type: "A", Content: "203.0.113.10"}.IsPrivate())
	tun := TargetForCNAME("ABC.cfargotunnel.com.")
	require.Equal(t, Target{Type: "CNAME", Content: "abc.cfargotunnel.com", Proxied: true}, tun, "tunnel records are always proxied")
}

func TestMatchZoneLongestSuffix(t *testing.T) {
	zones := []Zone{{ID: "1", Name: "example.com"}, {ID: "2", Name: "apps.example.com"}, {ID: "3", Name: "other.org"}}
	z, ok := MatchZone(zones, "web.apps.example.com")
	require.True(t, ok)
	require.Equal(t, "2", z.ID)
	z, ok = MatchZone(zones, "WEB.example.com.")
	require.True(t, ok)
	require.Equal(t, "1", z.ID)
	z, ok = MatchZone(zones, "example.com")
	require.True(t, ok, "the apex belongs to its zone")
	require.Equal(t, "1", z.ID)
	z, ok = MatchZone(zones, "*.apps.example.com")
	require.True(t, ok, "a wildcard host is matched by its base")
	require.Equal(t, "2", z.ID)
	_, ok = MatchZone(zones, "notexample.com")
	require.False(t, ok, "a suffix without a dot boundary is not a parent domain")
	_, ok = MatchZone(zones, "x.unknown.dev")
	require.False(t, ok)
}

func TestPlanCreatesWhenNothingExists(t *testing.T) {
	p := plan(wantA(false), nil, false)
	require.Equal(t, StatusMissing, p.Current)
	require.Equal(t, StatusManaged, p.After)
	require.Len(t, p.Actions, 1)
	act := p.Actions[0]
	require.Equal(t, ActionCreate, act.Kind)
	require.Equal(t, RecordInput{
		Type: "A", Name: "app.example.com", Content: "203.0.113.10", TTL: 1,
		Comment: Marker(testDomain), Tags: []string{MarkerTag},
	}, act.Input)

}

func TestPlanOwnedRecord(t *testing.T) {
	want := wantA(false)

	p := plan(want, []Existing{owned(a("r1", "203.0.113.10", false))}, false)
	require.Equal(t, StatusManaged, p.Current)
	require.Empty(t, p.Actions, "a matching owned record needs no change")

	// The server moved: the owned record is retargeted in place.
	p = plan(want, []Existing{owned(a("r1", "198.51.100.7", false))}, false)
	require.Equal(t, StatusWrongTarget, p.Current)
	require.Equal(t, StatusManaged, p.After)
	require.Len(t, p.Actions, 1)
	require.Equal(t, ActionUpdate, p.Actions[0].Kind)
	require.Equal(t, "r1", p.Actions[0].Record.ID)
	require.Equal(t, "203.0.113.10", p.Actions[0].Input.Content)
	require.False(t, p.Actions[0].Adopt)

	// Proxy toggled.
	p = plan(wantA(true), []Existing{owned(a("r1", "203.0.113.10", false))}, false)
	require.Equal(t, StatusWrongTarget, p.Current)
	require.Len(t, p.Actions, 1)
	require.True(t, p.Actions[0].Input.Proxied)

	// A tunnel replaces the address record: delete first, then create the CNAME.
	p = plan([]Target{TargetForCNAME("t1.cfargotunnel.com")}, []Existing{owned(a("r1", "203.0.113.10", false))}, false)
	require.Len(t, p.Actions, 2)
	require.Equal(t, ActionDelete, p.Actions[0].Kind)
	require.Equal(t, ActionCreate, p.Actions[1].Kind)
	require.Equal(t, "CNAME", p.Actions[1].Input.Type)
	require.True(t, p.Actions[1].Input.Proxied)

	// Duplicates of our own record are cleaned up.
	p = plan(want, []Existing{owned(a("r1", "203.0.113.10", false)), owned(a("r2", "198.51.100.9", false))}, false)
	require.Len(t, p.Actions, 1)
	require.Equal(t, ActionDelete, p.Actions[0].Kind)
	require.Equal(t, "r2", p.Actions[0].Record.ID)
}

func TestPlanNeverTouchesUnmarkedRecords(t *testing.T) {
	want := wantA(false)

	// Right target, not ours: present, untouched.
	p := plan(want, []Existing{foreign(a("f1", "203.0.113.10", true))}, false)
	require.Equal(t, StatusPresent, p.Current)
	require.Empty(t, p.Actions)

	// Wrong target, not ours: reported, untouched, adoptable.
	p = plan(want, []Existing{foreign(a("f1", "198.51.100.7", false))}, false)
	require.Equal(t, StatusConflict, p.Current)
	require.Equal(t, StatusConflict, p.After)
	require.True(t, p.NeedsAdopt)
	require.Contains(t, p.Message, "198.51.100.7", "the conflict says what exists and where it points")
	require.Empty(t, p.Actions, "without adopt nothing may change")

	// An unmarked AAAA next to the wanted A is irrelevant: the A record is created.
	p = plan(want, []Existing{foreign(Record{ID: "f2", Type: "AAAA", Name: "app.example.com", Content: "2001:db8::1"})}, false)
	require.Equal(t, StatusMissing, p.Current)
	require.Len(t, p.Actions, 1)
	require.Equal(t, ActionCreate, p.Actions[0].Kind)
}

func TestPlanAdopt(t *testing.T) {
	want := wantA(false)

	p := plan(want, []Existing{foreign(a("f1", "198.51.100.7", false))}, true)
	require.Equal(t, StatusManaged, p.After)
	require.Len(t, p.Actions, 1)
	act := p.Actions[0]
	require.Equal(t, ActionUpdate, act.Kind)
	require.True(t, act.Adopt)
	require.Equal(t, "f1", act.Record.ID)
	require.Equal(t, Marker(testDomain), act.Input.Comment, "adopting marks the record")
	require.Equal(t, "203.0.113.10", act.Input.Content)

	// A record that already points here is adopted too: marked, with the
	// domain's proxy setting, so the domain becomes managed (and lockable).
	p = plan(wantA(true), []Existing{foreign(a("f1", "203.0.113.10", false))}, true)
	require.Equal(t, StatusPresent, p.Current)
	require.Equal(t, StatusManaged, p.After)
	require.Len(t, p.Actions, 1)
	require.True(t, p.Actions[0].Adopt)
	require.Equal(t, "f1", p.Actions[0].Record.ID)
	require.Equal(t, Marker(testDomain), p.Actions[0].Input.Comment)
	require.True(t, p.Actions[0].Input.Proxied)

	// A CNAME next to a wanted A record is replaced: it is deleted first, then
	// the A record is created.
	p = plan(want, []Existing{foreign(cname("f1", "elsewhere.example.net"))}, true)
	require.Equal(t, StatusManaged, p.After)
	require.Len(t, p.Actions, 2)
	require.Equal(t, ActionDelete, p.Actions[0].Kind)
	require.Equal(t, "f1", p.Actions[0].Record.ID)
	require.True(t, p.Actions[0].Adopt)
	require.Equal(t, ActionCreate, p.Actions[1].Kind)
	require.Equal(t, "A", p.Actions[1].Input.Type)

	// Two address records of the wanted type: one is pointed at the target, the other deleted.
	p = plan(want, []Existing{foreign(a("f1", "198.51.100.7", false)), foreign(a("f2", "198.51.100.8", false))}, true)
	require.Len(t, p.Actions, 2)
	require.Equal(t, ActionDelete, p.Actions[0].Kind)
	require.Equal(t, "f2", p.Actions[0].Record.ID)
	require.Equal(t, ActionUpdate, p.Actions[1].Kind)
	require.Equal(t, "f1", p.Actions[1].Record.ID)
}

func TestPlanUnmarkedBlockerNextToOwnedRecord(t *testing.T) {
	want := wantA(false)
	recs := []Existing{owned(a("r1", "203.0.113.10", false)), foreign(cname("f1", "elsewhere.example.net"))}
	p := plan(want, recs, false)
	require.Equal(t, StatusConflict, p.Current)
	require.True(t, p.NeedsAdopt)
	require.Empty(t, p.Actions, "an unmarked CNAME is never deleted without the replace")

	p = plan(want, recs, true)
	require.Equal(t, StatusManaged, p.After)
	require.Len(t, p.Actions, 1)
	require.Equal(t, ActionDelete, p.Actions[0].Kind)
	require.Equal(t, "f1", p.Actions[0].Record.ID)
}

func TestPlanWithoutTarget(t *testing.T) {
	require.Equal(t, StatusMissing, plan(nil, nil, false).Current)
	require.Equal(t, StatusManaged, plan(nil, []Existing{owned(a("r1", "203.0.113.10", false))}, false).Current)
	require.Equal(t, StatusPresent, plan(nil, []Existing{foreign(a("f1", "203.0.113.10", false))}, false).Current)
	require.Empty(t, plan(nil, nil, false).Actions)
}

func TestSSLWarnings(t *testing.T) {
	require.Len(t, sslWarnings("flexible"), 1)
	require.Contains(t, sslWarnings("Flexible")[0], "redirect loop")
	require.Len(t, sslWarnings("off"), 1)
	require.Empty(t, sslWarnings("full"))
	require.Empty(t, sslWarnings("strict"))
	require.Empty(t, sslWarnings(""), "an unreadable mode is not a warning")
}

var testPage = uuid.MustParse("22222222-2222-2222-2222-222222222222")

func aaaa(id, ip string, proxied bool) Record {
	return Record{ID: id, Type: "AAAA", Name: "app.example.com", Content: ip, Proxied: proxied}
}

// wantDual is what a server with an IPv4 and an IPv6 address needs.
func wantDual(proxied bool) []Target {
	return []Target{
		{Type: "A", Content: "203.0.113.10", Proxied: proxied},
		{Type: "AAAA", Content: "2001:db8::10", Proxied: proxied},
	}
}

func TestTargetsForIPs(t *testing.T) {
	got := TargetsForIPs("203.0.113.10, 2001:db8::10;203.0.113.10 nonsense 0.0.0.0", true)
	require.Equal(t, []Target{
		{Type: "A", Content: "203.0.113.10", Proxied: true},
		{Type: "AAAA", Content: "2001:db8::10", Proxied: true},
	}, got, "repeats and anything unparseable are dropped, the order is kept")
	require.Empty(t, TargetsForIPs("", false))
	require.Empty(t, TargetsForIPs("none", false))
	require.Equal(t, "203.0.113.10", TargetsForIPs("::ffff:203.0.113.10", false)[0].Content)
}

func TestPlanWithAnAddressRecordPerFamily(t *testing.T) {
	// Nothing yet: both records are created, marked.
	p := plan(wantDual(false), nil, false)
	require.Equal(t, StatusMissing, p.Current)
	require.Equal(t, StatusManaged, p.After)
	require.Len(t, p.Actions, 2)
	require.Equal(t, "A", p.Actions[0].Input.Type)
	require.Equal(t, "AAAA", p.Actions[1].Input.Type)
	for _, act := range p.Actions {
		require.Equal(t, ActionCreate, act.Kind)
		require.Equal(t, Marker(testDomain), act.Input.Comment)
	}

	// Both exist and are ours: nothing to do.
	both := []Existing{owned(a("r1", "203.0.113.10", false)), owned(aaaa("r2", "2001:db8::10", false))}
	p = plan(wantDual(false), both, false)
	require.Equal(t, StatusManaged, p.Current)
	require.Empty(t, p.Actions)
	require.Contains(t, p.Message, "203.0.113.10, 2001:db8::10")

	// Only the A record exists: the AAAA record is added, the A record is left alone.
	p = plan(wantDual(false), both[:1], false)
	require.Equal(t, StatusWrongTarget, p.Current)
	require.Equal(t, StatusManaged, p.After)
	require.Len(t, p.Actions, 1)
	require.Equal(t, ActionCreate, p.Actions[0].Kind)
	require.Equal(t, "AAAA", p.Actions[0].Input.Type)

	// The proxy toggles on both records in place.
	p = plan(wantDual(true), both, false)
	require.Len(t, p.Actions, 2)
	for i, act := range p.Actions {
		require.Equal(t, ActionUpdate, act.Kind)
		require.Equal(t, both[i].ID, act.Record.ID)
		require.True(t, act.Input.Proxied)
		require.False(t, act.Adopt)
	}

	// The server lost its IPv6 address: that record goes, the other stays.
	p = plan(wantA(false), both, false)
	require.Equal(t, StatusManaged, p.Current)
	require.Len(t, p.Actions, 1)
	require.Equal(t, ActionDelete, p.Actions[0].Kind)
	require.Equal(t, "r2", p.Actions[0].Record.ID)

	// The IPv4 address changed: the A record is retargeted, the AAAA record is kept.
	moved := []Existing{owned(a("r1", "198.51.100.7", false)), owned(aaaa("r2", "2001:db8::10", false))}
	p = plan(wantDual(false), moved, false)
	require.Len(t, p.Actions, 1)
	require.Equal(t, ActionUpdate, p.Actions[0].Kind)
	require.Equal(t, "r1", p.Actions[0].Record.ID)
	require.Equal(t, "203.0.113.10", p.Actions[0].Input.Content)
}

func TestPlanWithSeveralTargetsNeverTouchesUnmarkedRecords(t *testing.T) {
	right := []Existing{foreign(a("f1", "203.0.113.10", false)), foreign(aaaa("f2", "2001:db8::10", false))}

	// Both records point to the right addresses already: present, untouched.
	p := plan(wantDual(true), right, false)
	require.Equal(t, StatusPresent, p.Current)
	require.Empty(t, p.Actions)

	// Adopting marks both (and applies the proxy setting).
	p = plan(wantDual(true), right, true)
	require.Equal(t, StatusPresent, p.Current)
	require.Equal(t, StatusManaged, p.After)
	require.Len(t, p.Actions, 2)
	for i, act := range p.Actions {
		require.Equal(t, ActionUpdate, act.Kind)
		require.True(t, act.Adopt)
		require.Equal(t, right[i].ID, act.Record.ID)
		require.Equal(t, Marker(testDomain), act.Input.Comment)
		require.True(t, act.Input.Proxied)
	}

	// Only an A record with a wrong address: reported; adopting takes it over and creates the AAAA record.
	wrong := []Existing{foreign(a("f1", "198.51.100.7", false))}
	p = plan(wantDual(false), wrong, false)
	require.Equal(t, StatusConflict, p.Current)
	require.True(t, p.NeedsAdopt)
	require.Empty(t, p.Actions)
	p = plan(wantDual(false), wrong, true)
	require.Len(t, p.Actions, 2)
	require.Equal(t, ActionUpdate, p.Actions[0].Kind)
	require.True(t, p.Actions[0].Adopt)
	require.Equal(t, "f1", p.Actions[0].Record.ID)
	require.Equal(t, "203.0.113.10", p.Actions[0].Input.Content)
	require.Equal(t, ActionCreate, p.Actions[1].Kind)
	require.Equal(t, "AAAA", p.Actions[1].Input.Type)

	// Two unmarked A records for one wanted A record: a conflict. The replace
	// points one at the target and deletes the other.
	twice := []Existing{foreign(a("f1", "198.51.100.7", false)), foreign(a("f2", "198.51.100.8", false))}
	p = plan(wantDual(false), twice, false)
	require.Equal(t, StatusConflict, p.Current)
	require.True(t, p.NeedsAdopt)
	require.Empty(t, p.Actions)
	p = plan(wantDual(false), twice, true)
	require.Equal(t, StatusManaged, p.After)
	require.Len(t, p.Actions, 3)
	require.Equal(t, ActionDelete, p.Actions[0].Kind)
	require.Equal(t, "f2", p.Actions[0].Record.ID)
	require.True(t, p.Actions[0].Adopt)
	require.Equal(t, ActionUpdate, p.Actions[1].Kind)
	require.Equal(t, "f1", p.Actions[1].Record.ID)
	require.Equal(t, ActionCreate, p.Actions[2].Kind)
	require.Equal(t, "AAAA", p.Actions[2].Input.Type)

	// A CNAME cannot sit next to either record: a conflict that names it; the
	// replace deletes it first, then creates both records.
	alias := []Existing{foreign(cname("f1", "elsewhere.example.net"))}
	p = plan(wantDual(false), alias, false)
	require.Equal(t, StatusConflict, p.Current)
	require.Contains(t, p.Message, "elsewhere.example.net")
	require.Empty(t, p.Actions)
	p = plan(wantDual(false), alias, true)
	require.Len(t, p.Actions, 3)
	require.Equal(t, ActionDelete, p.Actions[0].Kind)
	require.Equal(t, "f1", p.Actions[0].Record.ID)
	require.Equal(t, ActionCreate, p.Actions[1].Kind)
	require.Equal(t, ActionCreate, p.Actions[2].Kind)
}

// A service domain and a status page get the same plan for the same records;
// only the ownership marker names the owner.
func TestPlanIsTheSameForEveryOwnerKind(t *testing.T) {
	recs := func(owner uuid.UUID) []Existing {
		return []Existing{
			{Record: Record{ID: "r1", Type: "A", Name: "app.example.com", Content: "198.51.100.7", Comment: Marker(owner)}, Owned: true},
			{Record: Record{ID: "f1", Type: "AAAA", Name: "app.example.com", Content: "2001:db8::99"}},
		}
	}
	build := func(owner uuid.UUID, adopt bool) Plan {
		return BuildPlan(PlanInput{OwnerID: owner, Host: "app.example.com", Want: wantDual(true), Records: recs(owner), Adopt: adopt})
	}
	for _, adopt := range []bool{false, true} {
		dom, page := build(testDomain, adopt), build(testPage, adopt)
		require.NotEmpty(t, dom.Actions)
		for i := range dom.Actions {
			require.Equal(t, Marker(testDomain), dom.Actions[i].Input.Comment)
			require.Equal(t, Marker(testPage), page.Actions[i].Input.Comment)
			dom.Actions[i].Input.Comment, page.Actions[i].Input.Comment = "", ""
			dom.Actions[i].Record.Comment, page.Actions[i].Record.Comment = "", ""
		}
		require.Equal(t, dom, page)
	}
}
