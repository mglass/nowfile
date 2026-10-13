// Package mail sends nowfile's sign-in mail. With a Postmark server token it
// sends through the Postmark REST API; without one it writes each message to
// the log, so a dev server prints the magic link instead of mailing it.
package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// Message is one plain-text email.
type Message struct {
	To      string
	Subject string
	Text    string
}

// Sender delivers a Message.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// New returns a Postmark sender when token is set, otherwise a Log sender.
func New(token, from string) Sender {
	if token == "" {
		return Log{}
	}
	return &Postmark{Token: token, From: from}
}

// PostmarkEndpoint is Postmark's single-message send URL.
const PostmarkEndpoint = "https://api.postmarkapp.com/email"

// Postmark sends through the Postmark REST API.
type Postmark struct {
	Token string
	From  string
	// Endpoint defaults to PostmarkEndpoint; tests point it elsewhere.
	Endpoint string
	// Client defaults to an http.Client with a 10 second timeout.
	Client *http.Client
}

// Send posts m to Postmark and returns an error for any non-2xx response.
func (p *Postmark) Send(ctx context.Context, m Message) error {
	body, err := json.Marshal(map[string]string{
		"From":          p.From,
		"To":            m.To,
		"Subject":       m.Subject,
		"TextBody":      m.Text,
		"MessageStream": "outbound",
	})
	if err != nil {
		return err
	}
	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = PostmarkEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Postmark-Server-Token", p.Token)
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("mail: postmark: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("mail: postmark: %s: %s", resp.Status, bytes.TrimSpace(msg))
	}
	return nil
}

// Log writes each message to the standard logger instead of sending it.
type Log struct{}

// Send logs m, body included, and never fails.
func (Log) Send(_ context.Context, m Message) error {
	log.Printf("mail: not sent (NOWFILE_POSTMARK_TOKEN unset)\nTo: %s\nSubject: %s\n\n%s", m.To, m.Subject, m.Text)
	return nil
}
