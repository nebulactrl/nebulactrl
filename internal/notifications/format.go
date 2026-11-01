package notifications

import (
	"encoding/json"
	"strings"
)

// toneHex is the accent colour of a tone, in the brand palette.
func toneHex(tone string) string {
	switch tone {
	case toneBad:
		return "#ef6b6b"
	case toneWarn:
		return "#e5b84c"
	case toneOK:
		return "#4cc38a"
	}
	return "#6d6bff"
}

func toneInt(tone string) int {
	var n int
	for _, c := range toneHex(tone)[1:] {
		n = n*16 + hexVal(byte(c))
	}
	return n
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	}
	return int(c-'A') + 10
}

const linkLabel = "Open in NebulaCtrl"

// --- Slack (incoming webhook, Block Kit) ------------------------------------

type slackText struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Emoji *bool  `json:"emoji,omitempty"`
}

type slackElement struct {
	Type string     `json:"type"`
	Text *slackText `json:"text,omitempty"`
	URL  string     `json:"url,omitempty"`
}

// slackContext is a context-block element: its text is a plain string.
type slackContext struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type slackBlock struct {
	Type     string      `json:"type"`
	Text     *slackText  `json:"text,omitempty"`
	Fields   []slackText `json:"fields,omitempty"`
	Elements []any       `json:"elements,omitempty"`
}

type slackAttachment struct {
	Color  string       `json:"color"`
	Blocks []slackBlock `json:"blocks"`
}

type slackPayload struct {
	Text        string            `json:"text"`
	Attachments []slackAttachment `json:"attachments"`
}

// slackEscape escapes the three characters Slack treats as control syntax, so
// event text can neither mention @channel nor build a link.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// FormatSlack renders msg as an incoming-webhook body: a coloured attachment
// carrying Block Kit blocks (header, body, field grid, link button, context).
func FormatSlack(m Message) ([]byte, error) {
	noEmoji := false
	blocks := []slackBlock{{Type: "header", Text: &slackText{Type: "plain_text", Text: truncate(m.Title, 150), Emoji: &noEmoji}}}
	if m.Body != "" {
		blocks = append(blocks, slackBlock{Type: "section", Text: &slackText{Type: "mrkdwn", Text: truncate(slackEscape(m.Body), 3000)}})
	}
	// A section carries at most 10 fields.
	for i := 0; i < len(m.Fields); i += 10 {
		var fs []slackText
		for _, f := range m.Fields[i:min(i+10, len(m.Fields))] {
			fs = append(fs, slackText{Type: "mrkdwn", Text: truncate("*"+slackEscape(f.Name)+"*\n"+slackEscape(f.Value), 2000)})
		}
		blocks = append(blocks, slackBlock{Type: "section", Fields: fs})
	}
	if m.URL != "" {
		blocks = append(blocks, slackBlock{Type: "actions", Elements: []any{slackElement{
			Type: "button", Text: &slackText{Type: "plain_text", Text: linkLabel, Emoji: &noEmoji}, URL: m.URL,
		}}})
	}
	blocks = append(blocks, slackBlock{Type: "context", Elements: []any{slackContext{
		Type: "mrkdwn", Text: "NebulaCtrl \u00b7 " + slackEscape(m.Type) + " \u00b7 " + m.At.Format("2006-01-02 15:04 UTC"),
	}}})
	return json.Marshal(slackPayload{
		Text:        toneLabel(m.Tone) + ": " + slackEscape(m.Title),
		Attachments: []slackAttachment{{Color: toneHex(m.Tone), Blocks: blocks}},
	})
}

// --- Discord (webhook, embeds) ----------------------------------------------

type discordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type discordFooter struct {
	Text string `json:"text"`
}

type discordEmbed struct {
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	URL         string         `json:"url,omitempty"`
	Color       int            `json:"color"`
	Fields      []discordField `json:"fields,omitempty"`
	Footer      discordFooter  `json:"footer"`
	Timestamp   string         `json:"timestamp"`
}

type discordAllowedMentions struct {
	Parse []string `json:"parse"`
}

type discordPayload struct {
	Username        string                 `json:"username"`
	AllowedMentions discordAllowedMentions `json:"allowed_mentions"`
	Embeds          []discordEmbed         `json:"embeds"`
}

// FormatDiscord renders msg as one embed. allowed_mentions is empty, so event
// text can not ping @everyone or a role.
func FormatDiscord(m Message) ([]byte, error) {
	e := discordEmbed{
		Title:       truncate(m.Title, 256),
		Description: truncate(m.Body, 4096),
		URL:         m.URL,
		Color:       toneInt(m.Tone),
		Footer:      discordFooter{Text: "NebulaCtrl \u00b7 " + m.Type},
		Timestamp:   m.At.Format("2006-01-02T15:04:05Z"),
	}
	for _, f := range m.Fields {
		e.Fields = append(e.Fields, discordField{Name: truncate(f.Name, 256), Value: truncate(f.Value, 1024), Inline: len(f.Value) <= 40})
	}
	if len(e.Fields) > 25 {
		e.Fields = e.Fields[:25]
	}
	return json.Marshal(discordPayload{
		Username:        "NebulaCtrl",
		AllowedMentions: discordAllowedMentions{Parse: []string{}},
		Embeds:          []discordEmbed{e},
	})
}

// --- Telegram (Bot API sendMessage, MarkdownV2) ------------------------------

// telegramMax is the Bot API limit of one message's text.
const telegramMax = 4096

// EscapeMarkdownV2 escapes every character Telegram's MarkdownV2 reserves
// (https://core.telegram.org/bots/api#markdownv2-style).
func EscapeMarkdownV2(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		if strings.ContainsRune("\\_*[]()~`>#+-=|{}.!", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// escapeMarkdownLinkURL escapes the characters that end or break an inline
// link target: only ")" and "\" need it there.
func escapeMarkdownLinkURL(s string) string {
	return strings.NewReplacer("\\", "\\\\", ")", "\\)").Replace(s)
}

type telegramPayload struct {
	ChatID                string `json:"chat_id"`
	Text                  string `json:"text"`
	ParseMode             string `json:"parse_mode"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview"`
}

// telegramText builds the message text; body and field values are shortened
// step by step until the text fits telegramMax.
func telegramText(m Message) string {
	build := func(bodyMax, valMax int) string {
		var b strings.Builder
		b.WriteString("*" + EscapeMarkdownV2(m.Title) + "*")
		if m.Body != "" {
			b.WriteString("\n\n" + EscapeMarkdownV2(truncate(m.Body, bodyMax)))
		}
		if len(m.Fields) > 0 {
			b.WriteString("\n")
			for _, f := range m.Fields {
				b.WriteString("\n*" + EscapeMarkdownV2(f.Name) + ":* " + EscapeMarkdownV2(truncate(f.Value, valMax)))
			}
		}
		if m.URL != "" {
			b.WriteString("\n\n[" + EscapeMarkdownV2(linkLabel) + "](" + escapeMarkdownLinkURL(m.URL) + ")")
		}
		return b.String()
	}
	limits := [][2]int{{maxBody, maxFieldValue}, {600, 120}, {200, 60}, {1, 20}}
	for i, lim := range limits {
		if t := build(lim[0], lim[1]); i == len(limits)-1 || len([]rune(t)) <= telegramMax {
			return t
		}
	}
	return ""
}

// FormatTelegram renders msg as a sendMessage body for chatID.
func FormatTelegram(m Message, chatID string) ([]byte, error) {
	return json.Marshal(telegramPayload{ChatID: chatID, Text: telegramText(m), ParseMode: "MarkdownV2", DisableWebPagePreview: true})
}
