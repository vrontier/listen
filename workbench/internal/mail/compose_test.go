package mail

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"strings"
	"testing"
	"time"
)

// parts reads a composed message as a mail program does: the text, the
// HTML and the inline image.
func parts(t *testing.T, raw []byte) (h mail.Header, text, html string, img []byte, imgID string) {
	t.Helper()
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var walk func(ctype string, r io.Reader)
	walk = func(ctype string, r io.Reader) {
		mt, params, err := mime.ParseMediaType(ctype)
		if err != nil {
			t.Fatal(err)
		}
		mr := multipart.NewReader(r, params["boundary"])
		for {
			p, err := mr.NextPart() // decodes quoted-printable
			if err == io.EOF {
				return
			}
			if err != nil {
				t.Fatalf("%s: %v", mt, err)
			}
			ct := p.Header.Get("Content-Type")
			b, _ := io.ReadAll(p)
			switch {
			case strings.HasPrefix(ct, "multipart/"):
				walk(ct, bytes.NewReader(b))
			case strings.HasPrefix(ct, "text/plain"):
				text = string(b)
			case strings.HasPrefix(ct, "text/html"):
				html = string(b)
			case ct == "image/png":
				img, _ = io.ReadAll(base64Reader(b))
				imgID = p.Header.Get("Content-ID")
			}
		}
	}
	walk(m.Header.Get("Content-Type"), m.Body)
	return m.Header, text, html, img, imgID
}

func TestCompose(t *testing.T) {
	look := Look{Base: "https://low.example.org"}
	body := "Hello Ada,\n\nto log in to the Listening Observatory Workbench, open this link within 15 minutes:\n\nhttps://low.example.org/login/abc?next=%2F\n\nIf you did not ask for it, ignore this message.\n"
	raw, err := Compose("Workbench <low@example.org>", "ada@example.org", "Your login link", body, look, time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	h, text, html, img, id := parts(t, raw)
	if !strings.HasPrefix(h.Get("Content-Type"), "multipart/alternative") || h.Get("Message-Id") == "" || !strings.HasSuffix(h.Get("Message-Id"), "@example.org>") {
		t.Errorf("headers: %v", h)
	}
	if strings.ReplaceAll(text, "\r\n", "\n") != body {
		t.Errorf("the text part changed:\n%q", text)
	}
	for _, want := range []string{
		`<title>Your login link</title>`,
		`>Log in</a>`,
		`href="https://low.example.org/login/abc?next=%2F"`,
		`src="cid:emblem@workbench"`,
		`Listening Observatory</span>`, `>Workbench</span>`,
		`at <a href="https://low.example.org" style="color:#59696d;">low.example.org</a>`,
		`<p style="margin:0 0 14px;">Hello Ada,</p>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML lacks %s", want)
		}
	}
	if !bytes.Equal(img, emblemPNG) || id != "<emblem@workbench>" {
		t.Errorf("emblem: %d bytes, id %q", len(img), id)
	}
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("a line of %d characters", len(line))
		}
	}
	if os.Getenv("MAIL_PREVIEW") != "" {
		os.WriteFile(os.Getenv("MAIL_PREVIEW")+"/login.eml", raw, 0o644)
		os.WriteFile(os.Getenv("MAIL_PREVIEW")+"/login.html", []byte(strings.ReplaceAll(html, "cid:emblem@workbench", "emblem.png")), 0o644)
	}
}

func TestComposeEscapesAndQuotes(t *testing.T) {
	body := "Hello Bo,\n\nin run 3 of your analysis \"<b>Hum</b> & Co\", the analysis of Kiruna failed in the engine:\n\n  connection refused\n\nIts other sources go on. Restart it on the analysis's page:\nhttps://low.example.org/analyses/7\n"
	raw, err := Compose("low@example.org", "bo@example.org", "Run 3 of Grüße: Kiruna failed", body, Look{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h, _, html, _, _ := parts(t, raw)
	if subj, _ := new(mime.WordDecoder).DecodeHeader(h.Get("Subject")); subj != "Run 3 of Grüße: Kiruna failed" {
		t.Errorf("subject %q", subj)
	}
	for _, want := range []string{
		`&#34;&lt;b&gt;Hum&lt;/b&gt; &amp; Co&#34;`,
		`border-left:3px solid #ab693d;border-radius:4px;">connection refused</p>`,
		`page:<br><a href="https://low.example.org/analyses/7"`,
		`Sent by the Listening Observatory Workbench.`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML lacks %s", want)
		}
	}
	if strings.Contains(html, "<b>Hum") {
		t.Error("text not escaped")
	}
	if _, err := Compose("low@example.org", "bo@example.org", "a\r\nBcc: x@example.org", "x", Look{}, time.Now()); err == nil {
		t.Error("line break in a header accepted")
	}
}

func TestPieces(t *testing.T) {
	got := pieces("See https://a.example/x, then (https://b.example/y).")
	want := []piece{{Text: "See "}, {Text: "https://a.example/x", Link: "https://a.example/x"}, {Text: ", then (https://b.example/y)."}}
	// "(https…" does not start with the scheme: left as text, which is fine.
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: %+v, want %+v", i, got[i], want[i])
		}
	}
}

func base64Reader(b []byte) io.Reader {
	return base64.NewDecoder(base64.StdEncoding, strings.NewReader(strings.NewReplacer("\r", "", "\n", "").Replace(string(b))))
}
