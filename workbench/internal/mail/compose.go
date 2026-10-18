package mail

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html/template"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

// A message goes out in two forms (decided 2026-10-18): the plain text the
// Workbench writes, unchanged, and an HTML part in the Workbench's look made
// from that text, with the emblem as an inline image. A mail program shows
// the HTML part and falls back to the text. The HTML is derived, never
// written by hand, so the two cannot say different things:
//
//   - paragraphs are separated by empty lines; lines within one are kept;
//   - a paragraph that is only an address becomes a button, the address
//     below it for copying;
//   - an address elsewhere becomes a link;
//   - lines indented by two spaces are a quotation (a reason, a report).

//go:embed emblem.png
var emblemPNG []byte

const emblemID = "emblem@workbench"

// Look is what the HTML part needs besides the text.
type Look struct {
	Site string // "Listening Observatory Workbench"
	Base string // the Workbench's address, for the footer
}

func (l Look) site() string {
	if l.Site == "" {
		return "Listening Observatory Workbench"
	}
	return l.Site
}

// Compose returns the whole message: headers, the text and the HTML part.
func Compose(from, to, subject, text string, look Look, now time.Time) ([]byte, error) {
	if strings.ContainsAny(from+to+subject, "\r\n") {
		return nil, fmt.Errorf("mail: line break in header")
	}
	page, err := htmlOf(subject, text, look)
	if err != nil {
		return nil, err
	}

	var related bytes.Buffer
	rel := multipart.NewWriter(&related)
	if err := qpPart(rel, "text/html; charset=utf-8", page); err != nil {
		return nil, err
	}
	img, err := rel.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"image/png"},
		"Content-Transfer-Encoding": {"base64"},
		"Content-ID":                {"<" + emblemID + ">"},
		"Content-Disposition":       {`inline; filename="emblem.png"`},
	})
	if err != nil {
		return nil, err
	}
	img.Write(base64Lines(emblemPNG))
	rel.Close()

	var body bytes.Buffer
	alt := multipart.NewWriter(&body)
	if err := qpPart(alt, "text/plain; charset=utf-8", text); err != nil {
		return nil, err
	}
	p, err := alt.CreatePart(textproto.MIMEHeader{"Content-Type": {`multipart/related; type="text/html"; boundary=` + rel.Boundary()}})
	if err != nil {
		return nil, err
	}
	p.Write(related.Bytes())
	alt.Close()

	var msg bytes.Buffer
	h := func(k, v string) { msg.WriteString(k + ": " + v + "\r\n") }
	h("From", from)
	h("To", to)
	h("Subject", mime.QEncoding.Encode("utf-8", subject))
	h("Date", now.Format(time.RFC1123Z))
	h("Message-ID", messageID(from))
	h("MIME-Version", "1.0")
	h("Content-Type", "multipart/alternative; boundary="+alt.Boundary())
	msg.WriteString("\r\n")
	msg.Write(body.Bytes())
	return msg.Bytes(), nil
}

func qpPart(w *multipart.Writer, ctype, s string) error {
	p, err := w.CreatePart(textproto.MIMEHeader{"Content-Type": {ctype}, "Content-Transfer-Encoding": {"quoted-printable"}})
	if err != nil {
		return err
	}
	q := quotedprintable.NewWriter(p)
	if _, err := q.Write([]byte(strings.ReplaceAll(s, "\n", "\r\n"))); err != nil {
		return err
	}
	return q.Close()
}

func base64Lines(b []byte) []byte {
	s := base64.StdEncoding.EncodeToString(b)
	var out bytes.Buffer
	for len(s) > 76 {
		out.WriteString(s[:76] + "\r\n")
		s = s[76:]
	}
	out.WriteString(s + "\r\n")
	return out.Bytes()
}

func messageID(from string) string {
	domain := "workbench.invalid"
	if at := strings.LastIndex(from, "@"); at >= 0 {
		domain = strings.Trim(from[at+1:], "> ")
	}
	b := make([]byte, 12)
	rand.Read(b)
	return "<" + hex.EncodeToString(b) + "@" + domain + ">"
}

// --- the HTML part ---

type block struct {
	Button string // an address on its own: the button's link
	Label  string
	Quote  bool
	Lines  [][]piece
}

type piece struct {
	Text string
	Link string
}

func htmlOf(subject, text string, look Look) (string, error) {
	var blocks []block
	for _, para := range strings.Split(strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n")), "\n\n") {
		lines := strings.Split(strings.Trim(para, "\n"), "\n")
		if len(lines) == 1 && isURL(strings.TrimSpace(lines[0])) {
			u := strings.TrimSpace(lines[0])
			blocks = append(blocks, block{Button: u, Label: labelFor(u)})
			continue
		}
		b := block{Quote: true}
		for _, l := range lines {
			if !strings.HasPrefix(l, "  ") {
				b.Quote = false
			}
		}
		for _, l := range lines {
			b.Lines = append(b.Lines, pieces(strings.TrimSpace(l)))
		}
		blocks = append(blocks, b)
	}
	host := look.Base
	if u, err := url.Parse(look.Base); err == nil && u.Host != "" {
		host = u.Host
	}
	var out bytes.Buffer
	err := page.Execute(&out, map[string]any{
		"Subject": subject, "Blocks": blocks, "Site": look.site(),
		"Name": strings.TrimSuffix(look.site(), " Workbench"), "Sub": strings.HasSuffix(look.site(), " Workbench"),
		"Base": look.Base, "Host": host, "Emblem": template.URL("cid:" + emblemID),
	})
	return out.String(), err
}

func isURL(s string) bool {
	return (strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")) && !strings.ContainsAny(s, " \t")
}

// pieces splits a line into text and links.
func pieces(line string) []piece {
	var out []piece
	words := strings.SplitAfter(line, " ")
	var text strings.Builder
	for _, w := range words {
		bare := strings.TrimRight(w, " ")
		trail := ""
		for strings.HasSuffix(bare, ".") || strings.HasSuffix(bare, ",") || strings.HasSuffix(bare, ")") {
			trail = bare[len(bare)-1:] + trail
			bare = bare[:len(bare)-1]
		}
		if isURL(bare) {
			if text.Len() > 0 {
				out = append(out, piece{Text: text.String()})
				text.Reset()
			}
			out = append(out, piece{Text: bare, Link: bare})
			text.WriteString(trail + w[len(strings.TrimRight(w, " ")):])
			continue
		}
		text.WriteString(w)
	}
	if text.Len() > 0 {
		out = append(out, piece{Text: text.String()})
	}
	return out
}

// labelFor names a button by where its address leads.
func labelFor(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return "Open"
	}
	path := p.Path
	switch {
	case strings.HasPrefix(path, "/login/"):
		return "Log in"
	case strings.HasPrefix(path, "/invite/"):
		return "Accept the invitation"
	case strings.HasPrefix(path, "/account/email/"):
		return "Confirm the new address"
	case strings.HasPrefix(path, "/runs/"):
		return "Open the run"
	case strings.HasPrefix(path, "/analyses/"), strings.HasPrefix(path, "/research/"):
		return "Open the analysis"
	case strings.HasPrefix(path, "/ai/access"):
		return "AI, Access"
	case strings.HasPrefix(path, "/admin"), strings.HasPrefix(path, "/manage"):
		return "Admin"
	}
	return "Open"
}

// The colours are the Workbench's light theme (static/app.css); mail
// programs ignore most CSS, so everything is inline and laid out in tables.
var page = template.Must(template.New("mail").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="color-scheme" content="light"><meta name="supported-color-schemes" content="light"><title>{{.Subject}}</title></head>
<body style="margin:0;padding:0;background:#f4f3ee;color:#202d31;font-family:'Source Sans 3',-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:16px;line-height:1.5;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#f4f3ee;"><tr><td align="center" style="padding:24px 12px;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="max-width:560px;">
<tr><td style="padding:0 4px 16px;">
<table role="presentation" cellpadding="0" cellspacing="0" border="0"><tr>
<td style="vertical-align:middle;padding-right:10px;"><img src="{{.Emblem}}" width="32" height="32" alt="" style="display:block;border:0;"></td>
<td style="vertical-align:middle;"><a href="{{.Base}}" style="color:#202d31;text-decoration:none;"><span style="font-family:'Source Serif 4',Georgia,'Times New Roman',serif;font-weight:600;font-size:17px;line-height:1.2;">{{.Name}}</span>{{if .Sub}}<br><span style="font-size:11px;font-weight:600;letter-spacing:0.28em;text-transform:uppercase;color:#59696d;">Workbench</span>{{end}}</a></td>
</tr></table>
</td></tr>
<tr><td style="background:#ffffff;border:1px solid #d6dcd7;border-radius:6px;padding:8px 28px 24px;">
<h1 style="font-family:'Source Serif 4',Georgia,'Times New Roman',serif;font-weight:600;font-size:24px;line-height:1.25;margin:18px 0 12px;color:#202d31;">{{.Subject}}</h1>
{{range .Blocks}}{{if .Button}}<table role="presentation" cellpadding="0" cellspacing="0" border="0" style="margin:20px 0 6px;"><tr><td style="background:#236c68;border-radius:6px;"><a href="{{.Button}}" style="display:inline-block;padding:10px 20px;color:#ffffff;font-weight:600;font-size:16px;text-decoration:none;border-radius:6px;">{{.Label}}</a></td></tr></table>
<p style="margin:0 0 16px;font-size:13px;line-height:1.4;color:#59696d;word-break:break-all;">or open: <a href="{{.Button}}" style="color:#236c68;font-family:'IBM Plex Mono',ui-monospace,Menlo,Consolas,monospace;font-size:12px;">{{.Button}}</a></p>
{{else if .Quote}}<p style="margin:0 0 14px;padding:10px 14px;background:#f8f8f5;border-left:3px solid #ab693d;border-radius:4px;">{{range $i, $l := .Lines}}{{if $i}}<br>{{end}}{{range $l}}{{if .Link}}<a href="{{.Link}}" style="color:#236c68;word-break:break-all;">{{.Text}}</a>{{else}}{{.Text}}{{end}}{{end}}{{end}}</p>
{{else}}<p style="margin:0 0 14px;">{{range $i, $l := .Lines}}{{if $i}}<br>{{end}}{{range $l}}{{if .Link}}<a href="{{.Link}}" style="color:#236c68;word-break:break-all;">{{.Text}}</a>{{else}}{{.Text}}{{end}}{{end}}{{end}}</p>
{{end}}{{end}}</td></tr>
<tr><td style="padding:14px 4px 0;font-size:13px;line-height:1.5;color:#59696d;">Sent by the {{.Site}}{{if .Host}} at <a href="{{.Base}}" style="color:#59696d;">{{.Host}}</a>{{end}}.</td></tr>
</table>
</td></tr></table>
</body></html>
`))
