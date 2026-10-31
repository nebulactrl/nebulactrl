package notifications

import (
	"bytes"
	"fmt"
	"html"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
)

// Email is a rendered notification mail.
type Email struct {
	Subject string
	Text    string
	HTML    string
}

// RenderEmail renders msg as a subject, a plain-text body and an HTML body. The
// HTML uses the brand palette and inline styles only (mail clients drop
// everything else); the brand is the wordmark and its gradient, never art.
func RenderEmail(m Message) Email {
	return Email{
		Subject: "[NebulaCtrl] " + truncate(m.Title, 150),
		Text:    emailText(m),
		HTML:    emailHTML(m),
	}
}

func emailText(m Message) string {
	var b strings.Builder
	b.WriteString(m.Title + "\n")
	b.WriteString(strings.Repeat("=", min(max(len([]rune(m.Title)), 8), 72)) + "\n")
	b.WriteString(toneLabel(m.Tone) + " \u00b7 " + m.Type + "\n")
	if m.Body != "" {
		b.WriteString("\n" + m.Body + "\n")
	}
	if len(m.Fields) > 0 {
		b.WriteString("\n")
		w := 0
		for _, f := range m.Fields {
			w = max(w, len([]rune(f.Name)))
		}
		for _, f := range m.Fields {
			b.WriteString(f.Name + ":" + strings.Repeat(" ", w-len([]rune(f.Name))+1) + strings.ReplaceAll(f.Value, "\n", " ") + "\n")
		}
	}
	if m.URL != "" {
		b.WriteString("\n" + m.buttonLabel() + ": " + m.URL + "\n")
	}
	b.WriteString("\n--\nSent by NebulaCtrl on " + m.At.Format("2006-01-02 15:04 UTC") + "\n")
	return b.String()
}

func emailHTML(m Message) string {
	e := html.EscapeString
	accent := toneHex(m.Tone)
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark">
<title>` + e(m.Title) + `</title>
</head>
<body style="margin:0;padding:0;background-color:#0b0e15;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background-color:#0b0e15;">
<tr><td align="center" style="padding:32px 16px;">
<table role="presentation" width="560" cellpadding="0" cellspacing="0" style="width:100%;max-width:560px;background-color:#141824;border:1px solid #262c3b;border-radius:12px;">
<tr><td style="height:4px;line-height:4px;font-size:0;background-color:#6d6bff;background-image:linear-gradient(90deg,#9a6bff,#6d6bff,#4fa8ff);border-radius:12px 12px 0 0;">&nbsp;</td></tr>
<tr><td style="padding:24px 28px 0 28px;font-family:-apple-system,'Segoe UI',Helvetica,Arial,sans-serif;font-size:13px;font-weight:600;letter-spacing:0.04em;color:#aab1c2;">NebulaCtrl</td></tr>
<tr><td style="padding:16px 28px 0 28px;font-family:-apple-system,'Segoe UI',Helvetica,Arial,sans-serif;">
<span style="display:inline-block;padding:3px 10px;border-radius:999px;border:1px solid ` + accent + `;color:` + accent + `;font-size:12px;font-weight:600;">` + e(toneLabel(m.Tone)) + `</span>
<span style="font-size:12px;color:#7d8599;margin-left:8px;">` + e(m.Type) + `</span>
</td></tr>
<tr><td style="padding:12px 28px 0 28px;font-family:-apple-system,'Segoe UI',Helvetica,Arial,sans-serif;font-size:22px;line-height:1.3;font-weight:600;color:#eef0f6;">` + e(m.Title) + `</td></tr>
`)
	if m.Body != "" {
		b.WriteString(`<tr><td style="padding:12px 28px 0 28px;font-family:-apple-system,'Segoe UI',Helvetica,Arial,sans-serif;font-size:15px;line-height:1.55;color:#c5cad8;">` +
			strings.ReplaceAll(e(m.Body), "\n", "<br>") + "</td></tr>\n")
	}
	if len(m.Fields) > 0 {
		b.WriteString(`<tr><td style="padding:20px 28px 0 28px;"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="border-top:1px solid #262c3b;">` + "\n")
		for _, f := range m.Fields {
			b.WriteString(`<tr><td style="padding:9px 12px 9px 0;border-bottom:1px solid #262c3b;font-family:-apple-system,'Segoe UI',Helvetica,Arial,sans-serif;font-size:13px;color:#7d8599;width:34%;vertical-align:top;">` + e(f.Name) +
				`</td><td style="padding:9px 0;border-bottom:1px solid #262c3b;font-family:'SF Mono',Menlo,Consolas,monospace;font-size:13px;color:#e1e4ee;vertical-align:top;word-break:break-word;">` +
				strings.ReplaceAll(e(f.Value), "\n", "<br>") + "</td></tr>\n")
		}
		b.WriteString("</table></td></tr>\n")
	}
	if m.URL != "" {
		b.WriteString(`<tr><td style="padding:24px 28px 0 28px;"><a href="` + e(m.URL) + `" style="display:inline-block;padding:10px 18px;border-radius:8px;background-color:#6d6bff;color:#ffffff;font-family:-apple-system,'Segoe UI',Helvetica,Arial,sans-serif;font-size:14px;font-weight:600;text-decoration:none;">` +
			e(m.buttonLabel()) + "</a></td></tr>\n")
	}
	b.WriteString(`<tr><td style="padding:28px 28px 24px 28px;font-family:-apple-system,'Segoe UI',Helvetica,Arial,sans-serif;font-size:12px;color:#7d8599;">Sent by NebulaCtrl on ` +
		e(m.At.Format("2006-01-02 15:04 UTC")) + m.footerReason() + `</td></tr>
</table>
</td></tr>
</table>
</body>
</html>
`)
	return b.String()
}

// stripCRLF removes line breaks so a value can not inject headers.
func stripCRLF(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

// BuildMIME assembles a multipart/alternative message (plain text first, HTML
// second, both quoted-printable UTF-8). boundary and msgID are parameters so
// output is reproducible in tests.
func BuildMIME(from mail.Address, to []string, em Email, date time.Time, msgID, boundary string) []byte {
	var b bytes.Buffer
	hdr := func(k, v string) { b.WriteString(k + ": " + stripCRLF(v) + "\r\n") }
	hdr("From", from.String())
	hdr("To", strings.Join(to, ", "))
	hdr("Subject", mime.QEncoding.Encode("utf-8", em.Subject))
	hdr("Date", date.Format(time.RFC1123Z))
	hdr("Message-ID", "<"+msgID+">")
	hdr("MIME-Version", "1.0")
	hdr("Auto-Submitted", "auto-generated")
	hdr("Content-Type", fmt.Sprintf("multipart/alternative; boundary=%q", boundary))
	b.WriteString("\r\n")
	part := func(ctype, body string) {
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: " + ctype + "; charset=utf-8\r\n")
		b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
		w := quotedprintable.NewWriter(&b)
		_, _ = w.Write([]byte(body))
		_ = w.Close()
		b.WriteString("\r\n")
	}
	part("text/plain", em.Text)
	part("text/html", em.HTML)
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes()
}

// buttonLabel is the text of the message's link: the action it opens, or the
// generic label.
func (m Message) buttonLabel() string {
	if m.LinkLabel != "" {
		return m.LinkLabel
	}
	return linkLabel
}

// footerReason ends the HTML footer sentence: why the recipient got the mail.
func (m Message) footerReason() string {
	if m.Transactional {
		return "."
	}
	return ". You get this because an email channel subscribes to " + html.EscapeString(m.Type) + "."
}
