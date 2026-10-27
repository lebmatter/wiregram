package game

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"wiregram/internal/textfmt"
)

const tunnelWait = 45 * time.Second

var quickHost = regexp.MustCompile(`https://([a-z0-9-]+\.trycloudflare\.com)`)

// Tunnel is a cloudflared quick tunnel: a public trycloudflare.com name for a
// local address, with no account and no incoming connections.
type Tunnel struct {
	Host   string
	cancel context.CancelFunc
}

// StartTunnel runs cloudflared for local and waits until its name is up.
func StartTunnel(ctx context.Context, local string) (*Tunnel, error) {
	bin, err := exec.LookPath("cloudflared")
	if err != nil {
		return nil, errors.New("install cloudflared to challenge someone to a game")
	}
	metrics, err := freePort()
	if err != nil {
		return nil, err
	}
	tctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(tctx, bin, "tunnel", "--no-autoupdate",
		"--metrics", metrics, "--url", "http://"+local)
	cmd.WaitDelay = time.Second
	setParentDeath(cmd)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("start cloudflared: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start cloudflared: %w", err)
	}
	var (
		mu       sync.Mutex
		last     string
		seenHost string
	)
	exited := make(chan struct{})
	go func() {
		// cloudflared logs everything to stderr: keep the last line for
		// errors and the quick tunnel's name in case /quicktunnel is missing.
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			mu.Lock()
			last = line
			if m := quickHost.FindStringSubmatch(line); m != nil {
				seenHost = m[1]
			}
			mu.Unlock()
		}
		_ = cmd.Wait()
		close(exited)
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.After(tunnelWait)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	host := ""
	for {
		select {
		case <-exited:
			cancel()
			mu.Lock()
			defer mu.Unlock()
			return nil, fmt.Errorf("cloudflared stopped: %s", logMessage(last))
		case <-deadline:
			cancel()
			return nil, errors.New("cloudflared took too long to open a tunnel")
		case <-ctx.Done():
			cancel()
			return nil, fmt.Errorf("open tunnel: %w", ctx.Err())
		case <-tick.C:
		}
		if host == "" {
			host = quickTunnelHost(client, metrics)
			mu.Lock()
			if host == "" {
				host = seenHost
			}
			mu.Unlock()
		}
		if host != "" && ready(client, metrics) {
			return &Tunnel{Host: host, cancel: cancel}, nil
		}
	}
}

// Close stops cloudflared.
func (t *Tunnel) Close() { t.cancel() }

func freePort() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("find a port for cloudflared: %w", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		return "", fmt.Errorf("find a port for cloudflared: %w", err)
	}
	return addr, nil
}

func quickTunnelHost(c *http.Client, metrics string) string {
	resp, err := c.Get("http://" + metrics + "/quicktunnel")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var body struct {
		Hostname string `json:"hostname"`
	}
	if resp.StatusCode != http.StatusOK ||
		json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body) != nil {
		return ""
	}
	if !strings.HasSuffix(body.Hostname, ".trycloudflare.com") {
		return ""
	}
	return body.Hostname
}

func ready(c *http.Client, metrics string) bool {
	resp, err := c.Get("http://" + metrics + "/ready")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// logMessage is the readable part of a cloudflared log line, which starts
// with a timestamp and a level.
func logMessage(line string) string {
	f := strings.Fields(line)
	if len(f) > 2 && len(f[1]) == 3 && strings.ToUpper(f[1]) == f[1] {
		f = f[2:]
	}
	if msg := textfmt.Plain(strings.Join(f, " ")); msg != "" {
		return msg
	}
	return "no reason given"
}
