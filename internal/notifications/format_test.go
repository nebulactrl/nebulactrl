package notifications

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite golden files")

// golden compares got with testdata/<name>; -update rewrites the file.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing golden file; run go test -update")
	require.Equal(t, string(want), string(got))
}

func prettyJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, json.Indent(&buf, raw, "", "  "))
	buf.WriteByte('\n')
	return buf.Bytes()
}

var fixtureTime = time.Date(2026, 10, 26, 14, 30, 5, 0, time.UTC)

const fixtureDelivery = "00000000-0000-4000-8000-000000000001"

// fullMessage exercises every part of a message, with characters that need
// escaping in every format.
func fullMessage() Message {
	p := Payload{
		Type: TypeDeployFailed, Severity: SeverityError, Title: "api_v2 failed to deploy",
		Body: "Health check never passed (HTTP 503)! Use *care* in <b>prod</b> & [more].\nThe previous release is still serving.",
		URL:  "/p/11111111-1111-4111-8111-111111111111/production?service=22222222-2222-4222-8222-222222222222&tab=deployments",
		Fields: []MessageField{
			{"Project", "Shop & Co <test>"}, {"Environment", "production"}, {"Service", "api_v2"},
			{"Trigger", "git_push"}, {"Commit", "ab12cd3"}, {"Note", "line one\nline two"},
		},
		ProjectID: "11111111-1111-4111-8111-111111111111", ServiceID: "22222222-2222-4222-8222-222222222222",
		At: fixtureTime,
	}
	return message(p, uuid.MustParse(fixtureDelivery), "https://ctrl.example.com")
}

// minimalMessage has a title only: no body, fields or link.
func minimalMessage() Message {
	p := Payload{Type: TypeServerOnline, Severity: SeverityInfo, Title: "edge-1 is back online", At: fixtureTime}
	return message(p, uuid.MustParse(fixtureDelivery), "")
}

func TestFormatSlackGolden(t *testing.T) {
	for name, m := range map[string]Message{"full": fullMessage(), "minimal": minimalMessage()} {
		raw, err := FormatSlack(m)
		require.NoError(t, err)
		golden(t, "slack."+name+".json", prettyJSON(t, raw))
	}
}

func TestFormatSlackEscapesMentions(t *testing.T) {
	m := fullMessage()
	m.Body = "<!channel> and <@U123> and <https://evil.example|click>"
	raw, err := FormatSlack(m)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "<!channel>")
	require.NotContains(t, string(raw), "<@U123>")
	require.Contains(t, string(raw), "\\u0026lt;!channel\\u0026gt;")
}

func TestFormatDiscordGolden(t *testing.T) {
	for name, m := range map[string]Message{"full": fullMessage(), "minimal": minimalMessage()} {
		raw, err := FormatDiscord(m)
		require.NoError(t, err)
		golden(t, "discord."+name+".json", prettyJSON(t, raw))
	}
}

func TestFormatDiscordNeverPings(t *testing.T) {
	raw, err := FormatDiscord(fullMessage())
	require.NoError(t, err)
	var got struct {
		AllowedMentions struct {
			Parse []string `json:"parse"`
		} `json:"allowed_mentions"`
	}
	require.NoError(t, json.Unmarshal(raw, &got))
	require.NotNil(t, got.AllowedMentions.Parse)
	require.Empty(t, got.AllowedMentions.Parse)
}

func TestFormatTelegramGolden(t *testing.T) {
	for name, m := range map[string]Message{"full": fullMessage(), "minimal": minimalMessage()} {
		raw, err := FormatTelegram(m, "-1001234567890")
		require.NoError(t, err)
		golden(t, "telegram."+name+".json", prettyJSON(t, raw))
	}
}

func TestEscapeMarkdownV2(t *testing.T) {
	// Every character the Bot API reserves is escaped; plain text is untouched.
	require.Equal(t, `a\_b\*c\[d\]e\(f\)g\~h\`+"`"+`i\>j\#k\+l\-m\=n\|o\{p\}q\.r\!s\\t`, EscapeMarkdownV2("a_b*c[d]e(f)g~h`i>j#k+l-m=n|o{p}q.r!s\\t"))
	require.Equal(t, "plain text 123", EscapeMarkdownV2("plain text 123"))
	require.Equal(t, "héllo wörld ✓", EscapeMarkdownV2("héllo wörld ✓"))
}

func TestTelegramLinkTargetEscapesParenthesis(t *testing.T) {
	m := fullMessage()
	m.URL = "https://ctrl.example.com/a(b)c\\d"
	text := telegramText(m)
	require.Contains(t, text, `(https://ctrl.example.com/a(b\)c\\d)`)
}

func TestTelegramStaysUnderLimit(t *testing.T) {
	m := fullMessage()
	m.Body = strings.Repeat("a.b-c ", 400) // every other char needs an escape
	for range 12 {
		m.Fields = append(m.Fields, MessageField{Name: "Field.name", Value: strings.Repeat("x-y.", 100)})
	}
	text := telegramText(m)
	require.LessOrEqual(t, utf8.RuneCountInString(text), telegramMax)
	// The cut never leaves a dangling escape: the trailing run of backslashes is even.
	trail := len(text) - len(strings.TrimRight(text, "\\"))
	require.Zero(t, trail%2)
}

func TestFormatWebhookGolden(t *testing.T) {
	for name, m := range map[string]Message{"full": fullMessage(), "minimal": minimalMessage()} {
		raw, err := FormatWebhook(m)
		require.NoError(t, err)
		golden(t, "webhook."+name+".json", prettyJSON(t, raw))
	}
}

func TestFormatEmailGolden(t *testing.T) {
	from := mail.Address{Name: "NebulaCtrl", Address: "alerts@example.com"}
	for name, m := range map[string]Message{"full": fullMessage(), "minimal": minimalMessage()} {
		em := RenderEmail(m)
		raw := BuildMIME(from, []string{"ops@example.com", "dev@example.com"}, em, fixtureTime, fixtureDelivery+"@example.com", "nebulactrl-boundary")
		golden(t, "email."+name+".eml", raw)
		golden(t, "email."+name+".html", []byte(em.HTML))
		golden(t, "email."+name+".txt", []byte(em.Text))
	}
}

func TestEmailHasBothPartsAndNoHeaderInjection(t *testing.T) {
	m := fullMessage()
	m.Title = "evil\r\nBcc: attacker@example.com"
	m.Payload.Title = m.Title
	em := RenderEmail(m)
	raw := BuildMIME(mail.Address{Name: "N", Address: "a@example.com"}, []string{"b@example.com"}, em, fixtureTime, "id@example.com", "bnd")
	head, _, _ := strings.Cut(string(raw), "\r\n\r\n")
	require.NotContains(t, head, "\r\nBcc:")
	require.Contains(t, string(raw), "Content-Type: text/plain; charset=utf-8")
	require.Contains(t, string(raw), "Content-Type: text/html; charset=utf-8")
	// Brand colours, no art: the HTML names the wordmark and uses the gradient.
	require.Contains(t, em.HTML, "NebulaCtrl")
	require.Contains(t, em.HTML, "#6d6bff")
}

func TestEmailEscapesHTML(t *testing.T) {
	em := RenderEmail(fullMessage())
	require.NotContains(t, em.HTML, "<b>prod</b>")
	require.Contains(t, em.HTML, "&lt;b&gt;prod&lt;/b&gt;")
	require.Contains(t, em.HTML, "Shop &amp; Co &lt;test&gt;")
}

func TestPayloadNormalization(t *testing.T) {
	long := strings.Repeat("é", 5000)
	p := newPayload(Event{Type: TypeDeployFailed, Title: "  t\x00itle\n", Body: long}, map[string]string{"b": "2", "Service": "s", "a": "1", "Project": "p", "empty": ""}, SeverityError, "/x", fixtureTime)
	require.Equal(t, "title", p.Title)
	require.LessOrEqual(t, utf8.RuneCountInString(p.Body), maxBody)
	require.True(t, strings.HasSuffix(p.Body, "\u2026"))
	// Known fields first, then alphabetical; empty values dropped.
	require.Equal(t, []MessageField{{"Project", "p"}, {"Service", "s"}, {"a", "1"}, {"b", "2"}}, p.Fields)
	// An empty title falls back to the type's label.
	require.Equal(t, "Deployment failed", newPayload(Event{Type: TypeDeployFailed}, nil, SeverityError, "", fixtureTime).Title)
}

func TestResolveURL(t *testing.T) {
	require.Equal(t, "https://ctrl.example.com/servers/1", resolveURL("/servers/1", "https://ctrl.example.com/"))
	require.Equal(t, "https://other.example/x", resolveURL("https://other.example/x", "https://ctrl.example.com"))
	require.Equal(t, "", resolveURL("/servers/1", ""), "no public URL: no link rather than a broken one")
	require.Equal(t, "", resolveURL("", "https://ctrl.example.com"))
}
