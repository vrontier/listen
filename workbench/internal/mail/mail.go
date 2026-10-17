// Package mail sends the Workbench's few messages: invitations and login
// links.
package mail

import (
	"fmt"
	"log"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"time"
)

// Sender delivers one plain-text message.
type Sender interface {
	Send(to, subject, body string) error
}

// Log writes messages to the log instead of sending them. For development
// and for an installation without outgoing mail.
type Log struct{}

func (Log) Send(to, subject, body string) error {
	log.Printf("mail to %s: %s\n%s", to, subject, body)
	return nil
}

// SMTP sends through a mail server. Auth is used only when User is set.
type SMTP struct {
	Addr     string // host:port
	From     string
	User     string
	Password string
}

func (s SMTP) Send(to, subject, body string) error {
	if strings.ContainsAny(to+subject, "\r\n") {
		return fmt.Errorf("mail: line break in header")
	}
	host, _, err := net.SplitHostPort(s.Addr)
	if err != nil {
		return err
	}
	var auth smtp.Auth
	if s.User != "" {
		auth = smtp.PlainAuth("", s.User, s.Password, host)
	}
	msg := "From: " + s.From + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"Date: " + time.Now().Format(time.RFC1123Z) + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n\r\n" +
		strings.ReplaceAll(body, "\n", "\r\n")
	return smtp.SendMail(s.Addr, auth, s.From, []string{to}, []byte(msg))
}

// Memory keeps messages for tests.
type Memory struct {
	mu   sync.Mutex
	Sent []Message
}

type Message struct{ To, Subject, Body string }

func (m *Memory) Send(to, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Sent = append(m.Sent, Message{to, subject, body})
	return nil
}

// Last returns the latest message to an address.
func (m *Memory) Last(to string) (Message, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.Sent) - 1; i >= 0; i-- {
		if strings.EqualFold(m.Sent[i].To, to) {
			return m.Sent[i], true
		}
	}
	return Message{}, false
}

// Count is the number of messages to an address.
func (m *Memory) Count(to string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.Sent {
		if strings.EqualFold(s.To, to) {
			n++
		}
	}
	return n
}
