package statuspublic

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/statuspages"
)

func TestParseColor(t *testing.T) {
	for in, want := range map[string]string{
		"red":          "#b3261e",
		"Green":        "#1e7e4a",
		"brightgreen":  "#2db36f",
		"#FFF":         "#ffffff",
		"abc":          "#aabbcc",
		"ABC123":       "#abc123",
		"#12345":       "",
		"12":           "",
		"javascript:x": "",
		`"><script>`:   "",
		"url(#x)":      "",
		"":             "",
	} {
		require.Equalf(t, want, parseColor(in), "%q", in)
	}
}

func TestTextColorFollowsTheFill(t *testing.T) {
	require.Equal(t, "#fff", textColorOn("#1e7e4a"))
	require.Equal(t, "#fff", textColorOn("#000000"))
	require.Equal(t, "#14161a", textColorOn("#ffffff"))
	require.Equal(t, "#14161a", textColorOn("#ffff00"))
	require.Equal(t, "#fff", textColorOn("bogus"))
}

func opts(q string) badgeOpts {
	v, _ := url.ParseQuery(q)
	return parseBadgeOpts(v)
}

func TestBadgeStatusWording(t *testing.T) {
	text := func(state, q string) string {
		return string(statusBadge("API", state, opts(q)))
	}
	require.Contains(t, text(statuspages.StateOperational, ""), "API: Up")
	require.Contains(t, text(statuspages.StateMajorOutage, ""), "API: Down")
	require.Contains(t, text(statuspages.StateMaintenance, ""), "API: Maintenance")
	require.Contains(t, text(statuspages.StateDegraded, ""), "API: Degraded")
	require.Contains(t, text(statuspages.StatePartialOutage, ""), "API: Partial outage")
	require.Contains(t, text(statuspages.StateUnknown, ""), "API: No data")
	require.Contains(t, text("whatever", ""), "API: No data")

	require.Contains(t, text(statuspages.StateOperational, "upLabel=Healthy"), "API: Healthy")
	require.Contains(t, text(statuspages.StateMajorOutage, "downLabel=Outage"), "API: Outage")
	require.Contains(t, text(statuspages.StateMaintenance, "maintenanceLabel=Planned"), "API: Planned")
	require.Contains(t, text(statuspages.StateOperational, "downLabel=Outage"), "API: Up", "each label only replaces its own state")
	require.Contains(t, text(statuspages.StateOperational, "label=Service"), "Service: Up")
	require.Contains(t, text(statuspages.StateOperational, "upLabel=+++"), "API: Up", "a blank label is ignored")
}

func TestBadgeColorsAndStyles(t *testing.T) {
	flat := string(statusBadge("API", statuspages.StateOperational, opts("")))
	require.Contains(t, flat, `height="20"`)
	require.Contains(t, flat, `rx="3"`)
	require.Contains(t, flat, `fill="`+badgeColors[statuspages.StateOperational]+`"`)
	require.Contains(t, flat, `fill="`+defaultLabelColor+`"`)
	require.NotContains(t, flat, "font-weight=\"bold\"")

	square := string(statusBadge("API", statuspages.StateOperational, opts("style=flat-square")))
	require.Contains(t, square, `rx="0"`)
	require.Contains(t, square, `height="20"`)

	big := string(statusBadge("API", statuspages.StateOperational, opts("style=for-the-badge")))
	require.Contains(t, big, `height="28"`)
	require.Contains(t, big, `font-weight="bold"`)
	require.Contains(t, big, ">API<", "the text is upper-cased in this style")
	require.Contains(t, big, "API: UP")

	require.Equal(t, flat, string(statusBadge("API", statuspages.StateOperational, opts("style=bogus"))), "an unknown style is flat")

	custom := string(statusBadge("API", statuspages.StateOperational, opts("color=%23abcdef&labelColor=red")))
	require.Contains(t, custom, `fill="#abcdef"`)
	require.Contains(t, custom, `fill="#b3261e"`)
	require.NotContains(t, custom, badgeColors[statuspages.StateOperational])
	bad := string(statusBadge("API", statuspages.StateOperational, opts("color=nonsense&labelColor=%22%3E")))
	require.Contains(t, bad, badgeColors[statuspages.StateOperational], "an invalid colour is ignored")
	require.Contains(t, bad, defaultLabelColor)
}

func TestUptimeBadge(t *testing.T) {
	fillOf := func(b []byte) string {
		m := regexp.MustCompile(`<rect x="\d+" width="\d+" height="\d+" fill="(#[0-9a-f]{6})"/>`).FindSubmatch(b)
		require.NotNil(t, m, string(b))
		return string(m[1])
	}
	for _, c := range []struct {
		a     *float64
		value string
		state string
	}{
		{new(1.0), "100%", statuspages.StateOperational},
		{new(0.99), "99%", statuspages.StateOperational},
		{new(0.97), "97%", statuspages.StateDegraded},
		{new(0.95), "95%", statuspages.StateDegraded},
		{new(0.5), "50%", statuspages.StateMajorOutage},
		{nil, "N/A", statuspages.StateUnknown},
	} {
		b := uptimeBadge("API uptime (24h)", c.a, opts(""))
		require.Contains(t, string(b), "API uptime (24h): "+c.value)
		require.Equal(t, badgeColors[c.state], fillOf(b), c.value)
	}
	require.Contains(t, string(uptimeBadge("x", new(1.0), opts("label=Uptime&color=blue"))), "Uptime: 100%")
	require.Equal(t, "#1f6fb2", fillOf(uptimeBadge("x", new(1.0), opts("color=blue"))))
}

func TestBadgeTextIsBoundedAndEscaped(t *testing.T) {
	long := strings.Repeat("é", 200)
	b := string(statusBadge(long, statuspages.StateOperational, opts("")))
	require.Contains(t, b, strings.Repeat("é", maxBadgeLabel-1)+"…")
	require.NotContains(t, b, strings.Repeat("é", maxBadgeLabel))

	w := regexp.MustCompile(`width="(\d+)"`).FindStringSubmatch(b)
	require.NotNil(t, w)

	e := string(statusBadge(`<a href="x">&`, statuspages.StateOperational, opts("upLabel=%3Cb%3E")))
	require.NotContains(t, e, "<a href")
	require.NotContains(t, e, "<b>")
	require.Contains(t, e, "&lt;a href=&#34;x&#34;&gt;&amp;")
}
