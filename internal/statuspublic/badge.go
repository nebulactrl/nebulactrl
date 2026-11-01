package statuspublic

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nebulactrl/nebulactrl/internal/statuspages"
)

// Badges follow the shape of Uptime Kuma's: /badge/status.svg for the page,
// /badge/<slug>/status.svg and /badge/<slug>/uptime.svg?window=24h|90d for a
// component, restyled with label, upLabel, downLabel, maintenanceLabel, style,
// color and labelColor. Every value is validated or escaped; nothing from the
// query reaches the SVG unescaped.

// badgeColors are the value fills of the states, dark enough for white text.
var badgeColors = map[string]string{
	statuspages.StateOperational:   "#1e7e4a",
	statuspages.StateDegraded:      "#9a6700",
	statuspages.StatePartialOutage: "#b04a00",
	statuspages.StateMajorOutage:   "#b3261e",
	statuspages.StateMaintenance:   "#1f6fb2",
	statuspages.StateUnknown:       "#5b6270",
}

const defaultLabelColor = "#3d4450"

// namedColors are the colour names a badge accepts besides hex values.
var namedColors = map[string]string{
	"brightgreen": "#2db36f", "green": "#1e7e4a", "yellowgreen": "#5a8a1f", "yellow": "#d9a000",
	"orange": "#b04a00", "red": "#b3261e", "blue": "#1f6fb2", "lightgrey": "#9aa1ac", "lightgray": "#9aa1ac",
	"grey": "#5b6270", "gray": "#5b6270", "black": "#14161a", "white": "#ffffff",
	"success": "#1e7e4a", "important": "#b04a00", "critical": "#b3261e", "informational": "#1f6fb2", "inactive": "#9aa1ac",
}

var hexColor = regexp.MustCompile(`^#?([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// parseColor returns a #rrggbb value for a colour name or hex string, or "" when
// it is neither.
func parseColor(s string) string {
	s = strings.TrimSpace(s)
	if c, ok := namedColors[strings.ToLower(s)]; ok {
		return c
	}
	m := hexColor.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	h := strings.ToLower(m[1])
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	return "#" + h
}

// textColorOn picks white or near-black text for a #rrggbb fill by luminance.
func textColorOn(fill string) string {
	if len(fill) != 7 {
		return "#fff"
	}
	var lum float64
	for i, w := range [3]float64{0.2126, 0.7152, 0.0722} {
		v, err := strconv.ParseUint(fill[1+2*i:3+2*i], 16, 8)
		if err != nil {
			return "#fff"
		}
		c := float64(v) / 255
		if c <= 0.03928 {
			c /= 12.92
		} else {
			c = math.Pow((c+0.055)/1.055, 2.4)
		}
		lum += w * c
	}
	if lum > 0.4 {
		return "#14161a"
	}
	return "#fff"
}

// badgeStyle is the geometry of one style.
type badgeStyle struct {
	height, radius, fontSize, textY int
	charW                           float64
	pad                             int
	upper                           bool
	bold                            bool
}

var badgeStyles = map[string]badgeStyle{
	"flat":          {height: 20, radius: 3, fontSize: 11, textY: 14, charW: 6.6, pad: 10},
	"flat-square":   {height: 20, radius: 0, fontSize: 11, textY: 14, charW: 6.6, pad: 10},
	"for-the-badge": {height: 28, radius: 3, fontSize: 10, textY: 18, charW: 8, pad: 20, upper: true, bold: true},
}

// Limits on the text of a badge; longer text is cut with an ellipsis.
const (
	maxBadgeLabel = 60
	maxBadgeValue = 40
)

// badgeOpts are the query parameters of a badge request.
type badgeOpts struct {
	label, upLabel, downLabel, maintenanceLabel string
	style                                       string
	color, labelColor                           string // #rrggbb, "" for the default
}

func parseBadgeOpts(q url.Values) badgeOpts {
	o := badgeOpts{
		label: q.Get("label"), upLabel: q.Get("upLabel"), downLabel: q.Get("downLabel"), maintenanceLabel: q.Get("maintenanceLabel"),
		style: q.Get("style"), color: parseColor(q.Get("color")), labelColor: parseColor(q.Get("labelColor")),
	}
	if _, ok := badgeStyles[o.style]; !ok {
		o.style = "flat"
	}
	return o
}

// stateText is the value text of a state, with the caller's wording for up,
// down and maintenance.
func (o badgeOpts) stateText(state string) string {
	pick := func(custom, def string) string {
		if c := strings.TrimSpace(custom); c != "" {
			return c
		}
		return def
	}
	switch stateKey(state) {
	case statuspages.StateOperational:
		return pick(o.upLabel, "Up")
	case statuspages.StateDegraded:
		return "Degraded"
	case statuspages.StatePartialOutage:
		return "Partial outage"
	case statuspages.StateMajorOutage:
		return pick(o.downLabel, "Down")
	case statuspages.StateMaintenance:
		return pick(o.maintenanceLabel, "Maintenance")
	}
	return "No data"
}

// labelOr returns the caller's label, or def.
func (o badgeOpts) labelOr(def string) string {
	if l := strings.TrimSpace(o.label); l != "" {
		return l
	}
	return def
}

// statusBadge is the badge of a state: the component's (label = its name) or,
// for the page, label "status".
func statusBadge(label, state string, o badgeOpts) []byte {
	fill := badgeColors[stateKey(state)]
	if o.color != "" {
		fill = o.color
	}
	return badgeSVG(o.labelOr(label), o.stateText(state), fill, o)
}

// uptimeBadge is the badge of an availability fraction over a window ("24h",
// "90d"); nil availability reads "N/A".
func uptimeBadge(label string, avail *float64, o badgeOpts) []byte {
	value, state := "N/A", statuspages.StateUnknown
	if avail != nil {
		value = percentText(*avail)
		switch a := *avail; {
		case a >= 0.99:
			state = statuspages.StateOperational
		case a >= 0.95:
			state = statuspages.StateDegraded
		default:
			state = statuspages.StateMajorOutage
		}
	}
	fill := badgeColors[state]
	if o.color != "" {
		fill = o.color
	}
	return badgeSVG(o.labelOr(label), value, fill, o)
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// badgeSVG draws a two-part badge: label on a grey ground, value on fill. It
// uses presentation attributes only, so it is valid under the listener's CSP
// when opened on its own.
func badgeSVG(label, value, fill string, o badgeOpts) []byte {
	st := badgeStyles[o.style]
	if st.height == 0 {
		st = badgeStyles["flat"]
	}
	label, value = truncate(label, maxBadgeLabel), truncate(value, maxBadgeValue)
	if st.upper {
		label, value = strings.ToUpper(label), strings.ToUpper(value)
	}
	width := func(s string) int { return int(math.Ceil(float64(utf8.RuneCountInString(s))*st.charW)) + st.pad }
	lw, vw := width(label), width(value)
	w := lw + vw
	labelFill := defaultLabelColor
	if o.labelColor != "" {
		labelFill = o.labelColor
	}
	title := esc(label + ": " + value)
	weight := "normal"
	if st.bold {
		weight = "bold"
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" role="img" aria-label="%s">`, w, st.height, title)
	fmt.Fprintf(&b, `<title>%s</title>`, title)
	fmt.Fprintf(&b, `<clipPath id="r"><rect width="%d" height="%d" rx="%d" fill="#fff"/></clipPath>`, w, st.height, st.radius)
	fmt.Fprintf(&b, `<g clip-path="url(#r)"><rect width="%d" height="%d" fill="%s"/><rect x="%d" width="%d" height="%d" fill="%s"/></g>`, lw, st.height, labelFill, lw, vw, st.height, fill)
	fmt.Fprintf(&b, `<g text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" font-size="%d" font-weight="%s">`, st.fontSize, weight)
	fmt.Fprintf(&b, `<text x="%d" y="%d" fill="%s" textLength="%d" lengthAdjust="spacingAndGlyphs">%s</text>`, lw/2, st.textY, textColorOn(labelFill), lw-st.pad, esc(label))
	fmt.Fprintf(&b, `<text x="%d" y="%d" fill="%s" textLength="%d" lengthAdjust="spacingAndGlyphs">%s</text>`, lw+vw/2, st.textY, textColorOn(fill), vw-st.pad, esc(value))
	b.WriteString(`</g></svg>`)
	return b.Bytes()
}
