package agentprotocol

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const sid = "0a1b2c3d-1111-2222-3333-444455556666"

// --- frames -------------------------------------------------------------------------

func TestEncodeDecodeRoundTripKeepsTheBodyAndFillsTheEnvelope(t *testing.T) {
	raw, err := Encode(TypeResult, "frame-1", ResultFrame{CommandID: "c1", Outcome: OutcomeFailed, Error: "boom"})
	require.NoError(t, err)

	f, err := Decode(raw)
	require.NoError(t, err)
	require.Equal(t, TypeResult, f.Type)
	require.Equal(t, "frame-1", f.ID)
	require.Equal(t, ProtocolVersion, f.V)
	var res ResultFrame
	require.NoError(t, f.Into(&res))
	require.Equal(t, ResultFrame{CommandID: "c1", Outcome: OutcomeFailed, Error: "boom"}, res)

	// Without an id the field is absent, not empty.
	raw, err = Encode(TypeResult, "", ResultFrame{CommandID: "c1"})
	require.NoError(t, err)
	require.NotContains(t, string(raw), `"id"`)
}

func TestEncodeAcceptsAnEmptyBodyAndRejectsWhatIsNotAnObject(t *testing.T) {
	raw, err := Encode("ping", "", nil)
	require.NoError(t, err)
	f, err := Decode(raw)
	require.NoError(t, err)
	require.Equal(t, "ping", f.Type)

	_, err = Encode("x", "", "just a string")
	require.Error(t, err, "a frame body is a JSON object")
	_, err = Encode("x", "", make(chan int))
	require.Error(t, err, "an unmarshalable body is an error, not a panic")
}

func TestDecodeRejectsGarbage(t *testing.T) {
	_, err := Decode([]byte("not json"))
	require.ErrorContains(t, err, "decode envelope")
	_, err = Decode([]byte(`{"v":"one"}`))
	require.Error(t, err)
}

func TestSigningPayloadIsNonceAgentIDAndDecimalTime(t *testing.T) {
	got := SigningPayload([]byte{1, 2, 3}, "agent-9", 1700000000)
	require.Equal(t, append([]byte{1, 2, 3}, []byte("agent-91700000000")...), got)
	require.NotEqual(t, SigningPayload([]byte{1}, "agent-9", 5), SigningPayload([]byte{1}, "agent-9", 6), "the time is signed")
	require.NotEqual(t, SigningPayload([]byte{1}, "a", 5), SigningPayload([]byte{1}, "b", 5), "the agent id is signed")
}

func TestIsStreamTypeCoversTheInteractiveFamilyOnly(t *testing.T) {
	for _, typ := range []string{TypeExecOpen, TypeExecOutput, TypeFSData, TypeStreamAck, TypeTunnelOpen, TypeDumpRestoreRequest} {
		require.True(t, IsStreamType(typ), typ)
	}
	for _, typ := range []string{TypeResult, "", "ping", "exec"} {
		require.False(t, IsStreamType(typ), typ)
	}
}

// --- names derived from ids ---------------------------------------------------------

func TestShortCutsLongIDsAndKeepsShortOnes(t *testing.T) {
	require.Equal(t, "0a1b2c3d", Short(sid))
	require.Equal(t, "12345678", Short("12345678"))
	require.Equal(t, "abc", Short("abc"))
	require.Empty(t, Short(""))
}

func TestDerivedNamesUseTheShortID(t *testing.T) {
	require.Equal(t, "nc-vol-0a1b2c3d-data", VolumeName(sid, "data"))
	require.Equal(t, "nebulactrl-0a1b2c3d", VolumeHostName(sid))
	require.Equal(t, "nc-0a1b2c3d", StackProject(sid))
	require.Equal(t, "nc-0a1b2c3d-db", StackContainerName(sid, "db"))
	require.Equal(t, "nc-ts-0a1b2c3d", TailnetContainerName(sid))
	require.Equal(t, "nc-ts-0a1b2c3d-state", TailnetStateVolumeName(sid))
	require.Equal(t, "nc-ts-0a1b2c3d-config", TailnetConfigVolumeName(sid))
	require.Equal(t, "nc-postdeploy-api-0a1b2c3d", PostDeployContainerName("api", sid))
	require.Equal(t, "nc-predeploy-api-0a1b2c3d", PreDeployContainerName("api", sid))

	require.Equal(t, MeshServerContainerPrefix+"0a1b2c3d", MeshServerContainer(sid))
	require.Equal(t, MeshLinkContainerPrefix+"0a1b2c3d", MeshLinkContainer(sid))
	require.Equal(t, "nc-mesh-0a1b2c3d-state", MeshStateVolume(sid))
	require.Equal(t, "nc-mesh-0a1b2c3d-config", MeshConfigVolume(sid))
	require.Equal(t, "mesh-0a1b2c3d", MeshReplicationAlias(sid))
	require.Equal(t, "nc-svc-0a1b2c3d", MeshServiceAlias(sid))

	require.Equal(t, "nc-vol-0a1b2c3d-data-r261101214530",
		RestoredVolumeName(sid, "data", time.Date(2026, 11, 1, 21, 45, 30, 0, time.UTC)))
	// The stamp is UTC whatever the zone of the caller.
	cet := time.FixedZone("CET", 3600)
	require.Equal(t, "nc-vol-0a1b2c3d-data-r261101214530", RestoredVolumeName(sid, "data", time.Date(2026, 11, 1, 22, 45, 30, 0, cet)))
}

func TestStackRouteIDIsStablePerComponentAndPort(t *testing.T) {
	a := StackRouteID(sid, "web", 8080)
	require.Equal(t, a, StackRouteID(sid, "web", 8080))
	require.NotEqual(t, a, StackRouteID(sid, "web", 9090))
	require.NotEqual(t, a, StackRouteID(sid, "api", 8080))
	require.NotEqual(t, a, StackRouteID("11111111-1111-1111-1111-111111111111", "web", 8080))
	require.Len(t, a, 36)
	// A service id that is no UUID falls back to a fixed namespace.
	require.Equal(t, StackRouteID("not-a-uuid", "web", 80), StackRouteID("also-not", "web", 80))
}

func TestPinnedImagesAreSortedAndReturnTheirRef(t *testing.T) {
	require.Equal(t, "zz.example/test-pin:1", PinImage("zz.example/test-pin:1"))
	PinImage("aa.example/test-pin:1")
	PinImage("zz.example/test-pin:1") // twice is once
	pinned := PinnedImages()
	var got []string
	for _, p := range pinned {
		if strings.HasSuffix(p, "/test-pin:1") {
			got = append(got, p)
		}
	}
	require.Equal(t, []string{"aa.example/test-pin:1", "zz.example/test-pin:1"}, got)
	require.IsNonDecreasing(t, pinned)
}

// --- cleanup ------------------------------------------------------------------------

func TestCleanupPayloadNormalizeFillsDefaults(t *testing.T) {
	p := CleanupPayload{UnusedImageMinAgeHours: -3}
	p.Normalize()
	require.Equal(t, CleanupModeStandard, p.Mode)
	require.Equal(t, CleanupTriggerManual, p.Trigger)
	require.Equal(t, DefaultUnusedImageMinAgeHours, p.UnusedImageMinAgeHours)
	require.Equal(t, DefaultBuildCacheMinAgeHours, p.BuildCacheMinAgeHours)
	require.Equal(t, time.Duration(DefaultUnusedImageMinAgeHours)*time.Hour, p.UnusedImageMinAge())
	require.Equal(t, time.Duration(DefaultBuildCacheMinAgeHours)*time.Hour, p.BuildCacheMinAge())

	custom := CleanupPayload{Mode: "aggressive", Trigger: "schedule", UnusedImageMinAgeHours: 5, BuildCacheMinAgeHours: 7}
	custom.Normalize()
	require.Equal(t, CleanupPayload{Mode: "aggressive", Trigger: "schedule", UnusedImageMinAgeHours: 5, BuildCacheMinAgeHours: 7}, custom, "explicit values stay")
	require.Equal(t, 5*time.Hour, custom.UnusedImageMinAge())
	require.Equal(t, 7*time.Hour, custom.BuildCacheMinAge())
}

func TestCleanupStepNamesAreCapped(t *testing.T) {
	var s CleanupStep
	for i := range CleanupMaxRemovedNames + 10 {
		s.AddRemoved(string(rune('a' + i%26)))
	}
	require.Len(t, s.Removed, CleanupMaxRemovedNames)
	s.AddRemoved("late")
	require.NotContains(t, s.Removed, "late")
}

// --- backups and volumes ------------------------------------------------------------

func TestEngineSupport(t *testing.T) {
	for _, e := range []string{"postgres", "mysql", "mariadb", "mongodb"} {
		require.True(t, RestoreSupported(e), e)
		require.True(t, BackupSupported(e), e)
	}
	for _, e := range []string{"valkey", "redis"} {
		require.False(t, RestoreSupported(e), e+": dumped, but not restored by the agent")
		require.True(t, BackupSupported(e), e)
	}
	for _, e := range []string{"", "oracle", "Postgres"} {
		require.False(t, BackupSupported(e), e)
		require.False(t, RestoreSupported(e), e)
	}
}

func TestDumpFormatSupport(t *testing.T) {
	cases := []struct {
		engine, format string
		want           bool
	}{
		{"postgres", DumpPGCustom, true}, {"postgres", DumpPGTar, true}, {"postgres", DumpSQL, true}, {"postgres", DumpMongo, false},
		{"mysql", DumpSQL, true}, {"mysql", DumpPGCustom, false},
		{"mariadb", DumpSQL, true}, {"mariadb", DumpMongo, false},
		{"mongodb", DumpMongo, true}, {"mongodb", DumpSQL, false},
		{"redis", DumpSQL, false}, {"", DumpSQL, false},
	}
	for _, c := range cases {
		require.Equal(t, c.want, DumpFormatSupported(c.engine, c.format), c.engine+"/"+c.format)
	}
}

func TestValidObjectKey(t *testing.T) {
	for _, ok := range []string{"a", "backups/db/2026-11-01.dump", "a.b/c-d_e"} {
		require.NoError(t, ValidObjectKey(ok), ok)
	}
	for name, bad := range map[string]string{
		"empty": "", "absolute": "/etc/passwd", "dotdot": "a/../b", "dot": "a/./b", "empty segment": "a//b", "trailing slash": "a/",
		"backslash": `a\b`, "nul": "a\x00b", "leading dotdot": "../x", "too long": strings.Repeat("a", 1025),
	} {
		require.Error(t, ValidObjectKey(bad), name)
	}
	require.NoError(t, ValidObjectKey(strings.Repeat("a", 1024)))
}

func TestMarshalResultDataEncodesAnyResult(t *testing.T) {
	raw := MarshalResultData(BackupSimpleResult{Outcome: OutcomeSucceeded, Message: "ok"})
	var out BackupSimpleResult
	require.NoError(t, json.Unmarshal(raw, &out))
	require.Equal(t, BackupSimpleResult{Outcome: OutcomeSucceeded, Message: "ok"}, out)
	require.JSONEq(t, `{"outcome":"succeeded","message":"ok"}`, string(raw))
	require.Empty(t, MarshalResultData(make(chan int)), "an unencodable result is empty, never a partial frame")
}

func TestVolumeRepoValidate(t *testing.T) {
	ok := VolumeRepo{Destination: BackupDestination{Kind: DestS3}, Path: "team/volumes/" + sid}
	require.NoError(t, ok.Validate())
	local := ok
	local.Destination.Kind = DestLocal
	require.NoError(t, local.Validate())

	badPath := ok
	badPath.Path = "../escape"
	require.ErrorContains(t, badPath.Validate(), "invalid repository path")
	badKind := ok
	badKind.Destination.Kind = "ftp"
	require.ErrorContains(t, badKind.Validate(), `unknown destination kind "ftp"`)
}

func TestVolumeKeepAny(t *testing.T) {
	require.False(t, VolumeKeep{}.Any())
	require.True(t, VolumeKeep{Daily: 7}.Any())
	require.True(t, VolumeKeep{Weekly: 4}.Any())
	require.True(t, VolumeKeep{Monthly: 1}.Any())
	require.False(t, VolumeKeep{Daily: -1}.Any(), "a negative limit is no limit")
}

func TestCleanVolumePath(t *testing.T) {
	good := map[string]string{
		"":                  "/",
		"/":                 "/",
		"/data":             "/data",
		"/data/":            "/data",
		"/a//b/./c":         "/a/b/c",
		"/a/b.c/..hidden/x": "/a/b.c/..hidden/x",
	}
	for in, want := range good {
		got, err := CleanVolumePath(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for name, bad := range map[string]string{
		"relative": "data/x", "dotdot": "/a/../b", "trailing dotdot": "/a/..", "nul": "/a\x00b", "too long": "/" + strings.Repeat("a", 4096),
	} {
		_, err := CleanVolumePath(bad)
		require.Error(t, err, name)
	}
}

// --- builds, submodules, config paths -----------------------------------------------

func TestValidBuildType(t *testing.T) {
	for _, ok := range []string{BuildAuto, BuildDockerfile, BuildRailpack, BuildStarterPrefix + "node", BuildStarterPrefix + "static"} {
		require.True(t, ValidBuildType(ok), ok)
	}
	for _, bad := range []string{"", "make", BuildStarterPrefix, BuildStarterPrefix + "cobol", "NODE"} {
		require.False(t, ValidBuildType(bad), bad)
	}
}

func TestSubmodules(t *testing.T) {
	for _, ok := range []string{"", SubmodulesAuto, SubmodulesOn, SubmodulesOff} {
		require.True(t, ValidSubmodules(ok), ok)
	}
	require.False(t, ValidSubmodules("recursive"))
	require.True(t, SubmodulesEnabled(""))
	require.True(t, SubmodulesEnabled(SubmodulesAuto))
	require.True(t, SubmodulesEnabled(SubmodulesOn))
	require.False(t, SubmodulesEnabled(SubmodulesOff))
}

func TestValidConfigPath(t *testing.T) {
	for _, ok := range []string{"nebula.toml", "deploy/prod.toml", "a/b/c.d"} {
		require.True(t, ValidConfigPath(ok), ok)
	}
	for name, bad := range map[string]string{
		"empty": "", "absolute": "/nebula.toml", "trailing slash": "a/", "dotdot": "a/../b", "dot": "./a", "empty segment": "a//b",
		"backslash": `a\b`, "control": "a\x01b", "del": "a\x7fb", "newline": "a\nb", "too long": strings.Repeat("a", 241),
	} {
		require.False(t, ValidConfigPath(bad), name)
	}
	require.True(t, ValidConfigPath(strings.Repeat("a", 240)))
}

// --- DNS-01 -------------------------------------------------------------------------

func TestDNSNames(t *testing.T) {
	require.Equal(t, "dns01:cred-1:CF_DNS_API_TOKEN", DNSSecretKey("cred-1", "CF_DNS_API_TOKEN"))
	require.Equal(t, "nc-dns-cloudflare", DNSResolverName(DNSCloudflare))
	require.Equal(t, "/data/acme-cloudflare.json", DNSResolverStorage(DNSCloudflare))
	r := DNSResolver{ID: "c1", Provider: DNSHetzner}
	require.Equal(t, "nc-dns-hetzner", r.Name())
	require.Equal(t, []string{"*.example.com"}, WildcardCert{Domain: "example.com"}.Names())
	require.Equal(t, []string{"*.example.com", "example.com"}, WildcardCert{Domain: "example.com", IncludeApex: true}.Names())

	cfg := ProxyConfig{DNSResolvers: []DNSResolver{r}}
	require.True(t, cfg.HasDNSResolver(DNSHetzner))
	require.False(t, cfg.HasDNSResolver(DNSCloudflare))
	require.False(t, ProxyConfig{}.HasDNSResolver(DNSHetzner))
}

func TestValidateDNSValue(t *testing.T) {
	require.NoError(t, ValidateDNSValue("tok_123"))
	require.NoError(t, ValidateDNSValue(strings.Repeat("a", MaxDNSValueLen)))
	require.ErrorContains(t, ValidateDNSValue(""), "empty")
	require.ErrorContains(t, ValidateDNSValue("  \t"), "empty")
	require.ErrorContains(t, ValidateDNSValue(strings.Repeat("a", MaxDNSValueLen+1)), "longer than")
	for _, bad := range []string{"a\nb", "a\rb", "a\x00b"} {
		require.ErrorContains(t, ValidateDNSValue(bad), "single line")
	}
}

func TestValidateCAServer(t *testing.T) {
	require.NoError(t, ValidateCAServer(LetsEncryptStagingCA))
	for name, bad := range map[string]string{
		"http": "http://ca.example/directory", "no host": "https:///directory", "credentials": "https://u:p@ca.example/dir",
		"space": "https://ca.example/a b", "newline": "https://ca.example/a\nb", "scheme only": "https://", "too long": "https://ca.example/" + strings.Repeat("a", 200),
		"unparsable": "https://ca.example/%zz",
	} {
		require.Error(t, ValidateCAServer(bad), name)
	}
}

func TestValidateDNSResolverRejectsBadIdentityAndDelay(t *testing.T) {
	base := DNSResolver{ID: "c1", Provider: DNSCloudflare, EnvNames: []string{"CF_DNS_API_TOKEN"}}
	require.NoError(t, ValidateDNSResolver(base))
	for name, mut := range map[string]func(*DNSResolver){
		"empty id":        func(r *DNSResolver) { r.ID = "" },
		"colon in id":     func(r *DNSResolver) { r.ID = "a:b" },
		"space in id":     func(r *DNSResolver) { r.ID = "a b" },
		"negative delay":  func(r *DNSResolver) { r.DelaySeconds = -1 },
		"delay too long":  func(r *DNSResolver) { r.DelaySeconds = MaxDNSDelaySeconds + 1 },
		"bad variable":    func(r *DNSResolver) { r.EnvNames = []string{"lower-case"} },
		"duplicate var":   func(r *DNSResolver) { r.EnvNames = []string{"CF_DNS_API_TOKEN", "CF_DNS_API_TOKEN"} },
		"unannounced env": func(r *DNSResolver) { r.Env = map[string]string{"CF_ZONE_API_TOKEN": "x"} },
	} {
		r := base
		mut(&r)
		require.Error(t, ValidateDNSResolver(r), name)
	}
	ok := base
	ok.DelaySeconds = MaxDNSDelaySeconds
	require.NoError(t, ValidateDNSResolver(ok))
}

func TestValidateWildcardCert(t *testing.T) {
	require.NoError(t, ValidateWildcardCert(WildcardCert{ID: "w", Provider: DNSCloudflare, Domain: "example.com"}))
	require.ErrorContains(t, ValidateWildcardCert(WildcardCert{Provider: DNSCloudflare, Domain: "example.com"}), "without id")
	require.ErrorContains(t, ValidateWildcardCert(WildcardCert{ID: "w", Provider: "nope", Domain: "example.com"}), "unknown DNS provider")
	require.ErrorContains(t, ValidateWildcardCert(WildcardCert{ID: "w", Provider: DNSCloudflare, Domain: "*.example.com"}), "invalid base domain")
	require.ErrorContains(t, ValidateWildcardCert(WildcardCert{ID: "w", Provider: DNSCloudflare, Domain: ""}), "invalid base domain")
}

func TestValidateDNS01RejectsDuplicateDomainsAndInvalidParts(t *testing.T) {
	a := DNSResolver{ID: "c1", Provider: DNSCloudflare, EnvNames: []string{"CF_DNS_API_TOKEN"}}
	b := DNSResolver{ID: "c2", Provider: DNSHetzner, EnvNames: []string{"HETZNER_API_TOKEN"}}
	w := WildcardCert{ID: "w1", Provider: DNSCloudflare, Domain: "example.com"}

	require.NoError(t, ValidateDNS01(ProxyConfig{DNSResolvers: []DNSResolver{a, b}, WildcardCerts: []WildcardCert{w}}, "ops@example.com"))

	dup := w
	dup.ID = "w2"
	require.ErrorContains(t, ValidateDNS01(ProxyConfig{DNSResolvers: []DNSResolver{a}, WildcardCerts: []WildcardCert{w, dup}}, "ops@example.com"), "configured twice")
	bad := w
	bad.Domain = "not a domain"
	require.ErrorContains(t, ValidateDNS01(ProxyConfig{DNSResolvers: []DNSResolver{a}, WildcardCerts: []WildcardCert{bad}}, "ops@example.com"), "invalid base domain")
	badResolver := a
	badResolver.ID = ""
	require.ErrorContains(t, ValidateDNS01(ProxyConfig{DNSResolvers: []DNSResolver{badResolver}}, "ops@example.com"), "invalid credential id")
}

// --- maintenance pages --------------------------------------------------------------

func TestValidateMaintenance(t *testing.T) {
	require.NoError(t, ValidateMaintenance(Maintenance{}))
	require.NoError(t, ValidateMaintenance(Maintenance{Title: strings.Repeat("é", MaxMaintenanceTitle), Message: "line one\nline two"}), "the bound counts characters, not bytes")
	require.NoError(t, ValidateMaintenance(Maintenance{Message: strings.Repeat("m", MaxMaintenanceMessage)}))

	cases := map[string]Maintenance{
		"title too long":     {Title: strings.Repeat("t", MaxMaintenanceTitle+1)},
		"message too long":   {Message: strings.Repeat("m", MaxMaintenanceMessage+1)},
		"title newline":      {Title: "a\nb"},
		"title control":      {Title: "bell\x07"},
		"message control":    {Message: "tab\there"},
		"message NUL":        {Message: "a\x00b"},
		"title invalid utf8": {Title: "a\xffb"},
		"message bad utf8":   {Message: "\xc3\x28"},
	}
	for name, m := range cases {
		require.Error(t, ValidateMaintenance(m), name)
	}
	require.ErrorContains(t, ValidateMaintenance(Maintenance{Title: "a\nb"}), "title contains a control character")
	require.ErrorContains(t, ValidateMaintenance(Maintenance{Title: strings.Repeat("t", 81)}), "title is longer than 80 characters")
	require.ErrorContains(t, ValidateMaintenance(Maintenance{Message: "a\xff"}), "message is not valid UTF-8")
}

func TestCleanMaintenanceText(t *testing.T) {
	require.Equal(t, "hello", CleanMaintenanceText("  hello \n", 10))
	require.Equal(t, "abcde", CleanMaintenanceText("abcdefgh", 5))
	require.Equal(t, "abc", CleanMaintenanceText("abc  def", 5), "the cut text is trimmed again")
	require.Equal(t, "ünï", CleanMaintenanceText("ünïcode", 3), "a cut never splits a character")
	require.Equal(t, "ab", CleanMaintenanceText("a\xffb", 10), "invalid UTF-8 is dropped")
	require.Empty(t, CleanMaintenanceText("   ", 10))
}

// --- misc ----------------------------------------------------------------------------

func TestLogRetentionWithDefaults(t *testing.T) {
	d := LogRetention{}.WithDefaults()
	require.Equal(t, LogRetention{Days: DefaultLogRetentionDays, MaxServiceBytes: DefaultLogMaxServiceBytes, MaxServerBytes: DefaultLogMaxServerBytes}, d)
	custom := LogRetention{Days: 3, MaxServiceBytes: 10, MaxServerBytes: 20}
	require.Equal(t, custom, custom.WithDefaults())
	require.Equal(t, DefaultLogRetentionDays, LogRetention{Days: -1, MaxServiceBytes: 10, MaxServerBytes: 20}.WithDefaults().Days)
}

func TestSecretKeys(t *testing.T) {
	require.Equal(t, "logdrain:d1:token", LogDrainSecretKey("d1", "token"))
	require.Equal(t, "logredact:svc1", LogRedactSecretKey("svc1"))
}

func TestValidTailnetProvider(t *testing.T) {
	require.True(t, ValidTailnetProvider(TailnetProviderTailscale))
	require.True(t, ValidTailnetProvider(TailnetProviderHeadscale))
	require.False(t, ValidTailnetProvider(""))
	require.False(t, ValidTailnetProvider("zerotier"))
}

func TestTaskCommandErrorsCarryTheirMessage(t *testing.T) {
	err := ValidTaskCommand(nil)
	require.EqualError(t, err, "the command is empty")
	require.EqualError(t, ValidTaskCommand([]string{"ls", "a\x00b"}), "the command contains a NUL byte")
	require.EqualError(t, ValidTaskCommand([]string{strings.Repeat("x", 65<<10)}), "the command is too long")
	require.EqualError(t, ValidTaskCommand(make([]string, 257)), "the command is empty", "an empty program name is not runnable")
	many := make([]string, 257)
	for i := range many {
		many[i] = "x"
	}
	require.EqualError(t, ValidTaskCommand(many), "the command is too long")
}

func TestReservedLabelPrefixesAreACopy(t *testing.T) {
	prefixes := ReservedLabelPrefixes()
	require.Equal(t, []string{"com.docker.", "nebulactrl.", "traefik."}, prefixes)
	prefixes[0] = "mutated"
	require.Equal(t, "com.docker.", ReservedLabelPrefixes()[0], "callers get their own slice")
}

func TestDockerOptionsWarningsNameEachRiskOnce(t *testing.T) {
	require.Nil(t, (*DockerOptions)(nil).Warnings())
	require.Empty(t, (&DockerOptions{}).Warnings())

	for name, o := range map[string]*DockerOptions{
		"host pid":         {PidHost: true},
		"seccomp":          {SeccompUnconfined: true},
		"apparmor":         {ApparmorUnconfined: true},
		"devices":          {Devices: []DeviceMapping{{}}},
		"gpus":             {GPUs: &GPURequest{}},
		"net admin":        {CapAdd: []string{"net_admin"}},
		"ptrace":           {CapAdd: []string{"cap_sys_ptrace"}},
		"read-only rootfs": {ReadOnlyRootfs: true},
	} {
		require.Len(t, o.Warnings(), 1, name)
	}
	both := (&DockerOptions{SeccompUnconfined: true, ApparmorUnconfined: true}).Warnings()
	require.Len(t, both, 1, "one note for both profiles")
	all := (&DockerOptions{Privileged: true, PidHost: true, CapAdd: []string{"SYS_ADMIN", "SYS_PTRACE"}, ReadOnlyRootfs: true, Devices: []DeviceMapping{{}}}).Warnings()
	require.Len(t, all, 6)
	require.Contains(t, all[len(all)-1], "Host devices")
}

// --- upstream names -------------------------------------------------------------------

func TestIsPrivateAddr(t *testing.T) {
	for _, private := range []string{"10.0.0.1", "172.16.5.4", "192.168.1.1", "100.64.0.1", "100.127.255.254", "fd00::1", "::ffff:10.0.0.1"} {
		require.True(t, IsPrivateAddr(netip.MustParseAddr(private)), private)
	}
	for _, public := range []string{"8.8.8.8", "100.63.255.255", "100.128.0.0", "2001:4860:4860::8888", "127.0.0.1"} {
		require.False(t, IsPrivateAddr(netip.MustParseAddr(public)), public)
	}
}

func TestCheckResolvedJudgesEveryAddressTheNameResolvedTo(t *testing.T) {
	addr := func(s ...string) []netip.Addr {
		var out []netip.Addr
		for _, a := range s {
			out = append(out, netip.MustParseAddr(a))
		}
		return out
	}
	p := DefaultUpstreamPolicy()

	require.NoError(t, CheckResolved("app.example.com", "8080", addr("93.184.216.34", "2606:2800:220:1::1"), p, false))
	require.NoError(t, CheckResolved("app.example.com", "8080", nil, p, false), "nothing resolved: nothing to judge")

	// One bad address among good ones refuses the name.
	err := CheckResolved("app.example.com", "8080", addr("93.184.216.34", "127.0.0.1"), p, true)
	require.ErrorContains(t, err, "app.example.com resolves to 127.0.0.1")
	err = CheckResolved("app.example.com", "8080", addr("169.254.169.254"), p, true)
	require.ErrorContains(t, err, "resolves to 169.254.169.254")
	require.ErrorContains(t, CheckResolved("app.example.com", "8080", addr("::ffff:127.0.0.1"), p, true), "resolves to 127.0.0.1", "a mapped address is judged as IPv4")

	// The server's policy can allow what is otherwise denied.
	require.NoError(t, CheckResolved("app.example.com", "8080", addr("127.0.0.1"), UpstreamPolicy{AllowLoopback: true}, false))

	// Private addresses need an administrator.
	err = CheckResolved("db.internal.example", "5432", addr("10.1.2.3"), p, false)
	require.ErrorContains(t, err, "private address 10.1.2.3")
	require.ErrorContains(t, err, "instance administrator")
	require.NoError(t, CheckResolved("db.internal.example", "5432", addr("10.1.2.3", "100.64.0.9"), p, true))
	require.Error(t, CheckResolved("db.internal.example", "5432", addr("100.64.0.9"), p, false), "the tailnet range is private too")

	// A bad port is reported through the same check.
	require.Error(t, CheckResolved("app.example.com", "0", addr("93.184.216.34"), p, true))
}

// --- stream flow control --------------------------------------------------------------

func TestWindowAdmitsUpToTheStreamWindow(t *testing.T) {
	w := NewWindow()
	ctx := context.Background()
	require.NoError(t, w.Acquire(ctx, StreamWindow/2))
	require.NoError(t, w.Acquire(ctx, StreamWindow/2), "exactly the window fits")

	// The window is full: a further chunk waits until the receiver acknowledges.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, w.Acquire(cancelled, 1), context.Canceled)

	w.Ack(StreamWindow / 2)
	require.NoError(t, w.Acquire(ctx, StreamWindow/2))
}

func TestWindowBlocksUntilAckedAndAdmitsOversizedChunkWhenIdle(t *testing.T) {
	ctx := context.Background()
	w := NewWindow()
	require.NoError(t, w.Acquire(ctx, StreamWindow*4), "a chunk bigger than the window goes out when nothing is in flight")

	got := make(chan error, 1)
	go func() { got <- w.Acquire(ctx, 10) }()
	w.Ack(StreamWindow * 4)
	require.NoError(t, <-got, "the waiter is admitted once the receiver has acknowledged")

	// Acks never drive the counter negative: after over-acking a full window still fits.
	w.Ack(1 << 30)
	require.NoError(t, w.Acquire(ctx, StreamWindow))
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, w.Acquire(cancelled, 1), context.Canceled, "a full window is not exceeded")
}

func TestWindowCloseFailsWaitersAndFutureAcquires(t *testing.T) {
	ctx := context.Background()
	w := NewWindow()
	require.NoError(t, w.Acquire(ctx, StreamWindow))

	got := make(chan error, 1)
	go func() { got <- w.Acquire(ctx, 1) }()
	w.Close()
	require.ErrorIs(t, <-got, ErrWindowClosed)
	require.ErrorIs(t, w.Acquire(ctx, 1), ErrWindowClosed)
	w.Ack(StreamWindow) // after close an ack is harmless
	require.ErrorIs(t, w.Acquire(ctx, 1), ErrWindowClosed)
}
