// Command plan sets your nowfile Status from a terminal and reads anyone's
// nowfile over finger.
//
//	plan "lunch, back at 1"            set Status
//	plan --until 13:00 "lunch"         set Status until 13:00 today
//	plan --until 30m "heads down"      ... or for a duration, or until an RFC 3339 time
//	plan --clear                       clear Status
//	plan show [name@host]              read a nowfile: finger first, then HTTPS
//	plan login                         save base_url, token, and name to the config
//
// The config is ~/.config/nowfile/config ($XDG_CONFIG_HOME/nowfile/config
// if set), key=value lines: base_url, token, and optionally name, which
// "plan show" uses when given no argument.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const usage = `usage:
  plan [--until TIME] "status line"   set Status; TIME is 13:00 (today), 30m or 2h, or RFC 3339
  plan --clear                         clear Status
  plan show [name@host] [--port 79]    read a nowfile over finger, falling back to HTTPS
  plan login [--base-url URL] [--token TOKEN] [--name NAME]
                                       save the config; prompts for anything not given
`

// defaultBaseURL is the public server.
const defaultBaseURL = "https://nowfile.org"

// fingerTimeout bounds connecting to a finger server; fingerReadTimeout
// bounds the whole exchange once connected.
const (
	fingerTimeout     = 3 * time.Second
	fingerReadTimeout = 5 * time.Second
)

func main() {
	a := &app{
		stdin:      bufio.NewReader(os.Stdin),
		stdout:     os.Stdout,
		client:     &http.Client{Timeout: 10 * time.Second},
		now:        time.Now,
		configPath: defaultConfigPath(),
	}
	if err := a.run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "plan:", err)
		os.Exit(1)
	}
}

type app struct {
	stdin      *bufio.Reader
	stdout     io.Writer
	client     *http.Client
	now        func() time.Time
	configPath string
}

func (a *app) run(ctx context.Context, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "show":
			return a.show(ctx, args[1:])
		case "login", "init":
			return a.login(ctx, args[1:])
		case "help", "-h", "--help":
			fmt.Fprint(a.stdout, usage)
			return nil
		}
	}
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	untilArg := fs.String("until", "", "")
	clear := fs.Bool("clear", false, "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%v\n%s", err, usage)
	}
	cfg, err := loadConfig(a.configPath)
	if err != nil {
		return err
	}
	if *clear {
		if fs.NArg() > 0 || *untilArg != "" {
			return errors.New("--clear takes no status line or --until")
		}
		return a.call(ctx, cfg, http.MethodDelete, "/api/v1/slots/Status", nil)
	}
	line := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if line == "" {
		return errors.New("no status line\n" + usage)
	}
	body := map[string]string{"value": line}
	if *untilArg != "" {
		until, err := parseUntil(*untilArg, a.now())
		if err != nil {
			return err
		}
		body["until"] = until.Format(time.RFC3339)
	}
	return a.call(ctx, cfg, http.MethodPut, "/api/v1/slots/Status", body)
}

// parseUntil reads an expiry as given to --until: "13:00" is that time
// today in now's zone, "30m" or "2h" is a duration from now, and anything
// else must be RFC 3339. A clock time already past today is an error rather
// than tomorrow, so a typo never sets a day-long Status.
func parseUntil(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if h, m, ok := strings.Cut(s, ":"); ok && len(m) == 2 && len(h) >= 1 && len(h) <= 2 {
		hh, err1 := strconv.Atoi(h)
		mm, err2 := strconv.Atoi(m)
		if err1 == nil && err2 == nil && hh >= 0 && hh < 24 && mm >= 0 && mm < 60 {
			t := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location())
			if !t.After(now) {
				return time.Time{}, fmt.Errorf("--until %s has already passed today", s)
			}
			return t, nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return time.Time{}, fmt.Errorf("--until %s must be a positive duration", s)
		}
		return now.Add(d).Truncate(time.Second), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		if !t.After(now) {
			return time.Time{}, fmt.Errorf("--until %s is in the past", s)
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--until %q: want a time today like 13:00, a duration like 30m or 2h, or RFC 3339", s)
}

// call makes one authenticated API request and turns a non-2xx response
// into an error carrying the server's message.
func (a *app) call(ctx context.Context, cfg config, method, path string, body any) error {
	if cfg.BaseURL == "" || cfg.Token == "" {
		return fmt.Errorf("not logged in: run plan login (config %s)", a.configPath)
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, cfg.BaseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		return nil
	}
	var e struct{ Error string }
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("%s: run plan login with a current token", e.Error)
		}
		return errors.New(e.Error)
	}
	return fmt.Errorf("%s %s: %s", method, path, resp.Status)
}

// show prints a nowfile document. It speaks finger to host on port (79 by
// default) and falls back to HTTPS when the port is blocked or refuses.
func (a *app) show(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("plan show", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	port := fs.Int("port", 79, "")
	// Accept the target before or after the flag.
	var target string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		target, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%v\n%s", err, usage)
	}
	if target == "" && fs.NArg() == 1 {
		target = fs.Arg(0)
	} else if fs.NArg() > 0 {
		return errors.New("usage: plan show [name@host] [--port 79]")
	}

	cfg, err := loadConfig(a.configPath)
	if err != nil {
		return err
	}
	name, host, ok := strings.Cut(target, "@")
	if target == "" {
		if cfg.Name == "" || cfg.BaseURL == "" {
			return errors.New("plan show needs name@host, or name and base_url in the config (plan login)")
		}
		u, err := url.Parse(cfg.BaseURL)
		if err != nil {
			return fmt.Errorf("base_url: %w", err)
		}
		name, host, ok = cfg.Name, u.Hostname(), true
	}
	if !ok || name == "" || host == "" || strings.Contains(host, "@") {
		return fmt.Errorf("%q: want name@host", target)
	}

	text, ferr := fingerQuery(ctx, net.JoinHostPort(host, strconv.Itoa(*port)), name)
	if ferr == nil {
		_, err := io.WriteString(a.stdout, text)
		return err
	}
	// Some networks block port 79. The fallback uses the configured
	// base_url when it names this host, so a dev server on another port
	// works, and https://host otherwise.
	base := "https://" + host
	if u, err := url.Parse(cfg.BaseURL); err == nil && cfg.BaseURL != "" && strings.EqualFold(u.Hostname(), host) {
		base = strings.TrimRight(cfg.BaseURL, "/")
	}
	text, herr := a.fetchText(ctx, base+"/"+url.PathEscape(name)+".txt")
	if herr != nil {
		return fmt.Errorf("finger: %v; https: %v", ferr, herr)
	}
	_, err = io.WriteString(a.stdout, text)
	return err
}

// fingerQuery sends one finger query and returns the response with CRLF
// turned into LF.
func fingerQuery(ctx context.Context, addr, name string) (string, error) {
	d := net.Dialer{Timeout: fingerTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(fingerReadTimeout))
	if _, err := io.WriteString(conn, name+"\r\n"); err != nil {
		return "", err
	}
	b, err := io.ReadAll(io.LimitReader(conn, 64<<10))
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		return "", errors.New("empty response")
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n"), nil
}

func (a *app) fetchText(ctx context.Context, u string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return string(b), err
}

// login writes the config from flags, prompting for whatever is missing,
// after checking the token against the server.
func (a *app) login(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("plan login", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	baseURL := fs.String("base-url", "", "")
	token := fs.String("token", "", "")
	name := fs.String("name", "", "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%v\n%s", err, usage)
	}
	if fs.NArg() > 0 {
		return errors.New("usage: plan login [--base-url URL] [--token TOKEN] [--name NAME]")
	}
	old, err := loadConfig(a.configPath)
	if err != nil {
		return err
	}
	prompt := func(label, def string) (string, error) {
		if def != "" {
			fmt.Fprintf(a.stdout, "%s [%s]: ", label, def)
		} else {
			fmt.Fprintf(a.stdout, "%s: ", label)
		}
		line, err := a.stdin.ReadString('\n')
		if err != nil && line == "" {
			if errors.Is(err, io.EOF) && def != "" {
				return def, nil
			}
			return "", fmt.Errorf("reading %s: %w", label, err)
		}
		if line = strings.TrimSpace(line); line == "" {
			return def, nil
		}
		return line, nil
	}
	cfg := config{BaseURL: *baseURL, Token: *token, Name: *name}
	if cfg.BaseURL == "" {
		def := old.BaseURL
		if def == "" {
			def = defaultBaseURL
		}
		if cfg.BaseURL, err = prompt("Server", def); err != nil {
			return err
		}
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if u, err := url.Parse(cfg.BaseURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("base URL %q: want https://host", cfg.BaseURL)
	}
	if cfg.Token == "" {
		if cfg.Token, err = prompt("Token (from your account page)", ""); err != nil {
			return err
		}
	}
	if cfg.Token == "" {
		return errors.New("a token is required")
	}
	if cfg.Name == "" {
		if cfg.Name, err = prompt("Your username, for plan show (optional)", old.Name); err != nil {
			return err
		}
	}
	cfg.Name = strings.ToLower(cfg.Name)
	if err := a.call(ctx, cfg, http.MethodGet, "/api/v1/me", nil); err != nil {
		return fmt.Errorf("checking token with %s: %w", cfg.BaseURL, err)
	}
	if err := saveConfig(a.configPath, cfg); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "saved %s\n", a.configPath)
	return nil
}
