package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"wiregram/internal/textfmt"
)

// maxResponseBytes caps a whole reply, and each event of a streamed one.
const maxResponseBytes = 1 << 20

var errTimeout = errors.New("provider timed out")

// OpenAI is any server with an OpenAI-compatible chat completions API:
// OpenAI, OpenRouter, Ollama and llama.cpp's llama-server among them.
type OpenAI struct {
	BaseURL string
	Key     string // "" sends no Authorization header
	// Timeout bounds the wait for the reply, or while streaming the wait for
	// each next piece of it, so a long reply that keeps coming never times out.
	Timeout time.Duration
}

type wireMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // a string, or []wirePart with images
}

type wirePart struct {
	Type     string   `json:"type"`
	Text     string   `json:"text,omitempty"`
	ImageURL *wireURL `json:"image_url,omitempty"`
}

type wireURL struct {
	URL string `json:"url"`
}

func wireMessages(messages []Message) []wireMessage {
	out := make([]wireMessage, 0, len(messages))
	for _, m := range messages {
		if len(m.Images) == 0 {
			out = append(out, wireMessage{Role: m.Role, Content: m.Text})
			continue
		}
		parts := []wirePart{{Type: "text", Text: m.Text}}
		for _, img := range m.Images {
			url := "data:" + img.MIME + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
			parts = append(parts, wirePart{Type: "image_url", ImageURL: &wireURL{URL: url}})
		}
		out = append(out, wireMessage{Role: m.Role, Content: parts})
	}
	return out
}

func (o *OpenAI) Chat(ctx context.Context, req Request) (Reply, error) {
	stream := req.OnText != nil
	body, err := json.Marshal(struct {
		Model     string        `json:"model"`
		Messages  []wireMessage `json:"messages"`
		MaxTokens int           `json:"max_tokens"`
		Stream    bool          `json:"stream,omitempty"`
	}{req.Model, wireMessages(req.Messages), req.MaxTokens, stream})
	if err != nil {
		return Reply{}, fmt.Errorf("encode request: %w", err)
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	timer := time.AfterFunc(o.Timeout, func() { cancel(errTimeout) })
	defer timer.Stop()

	url := strings.TrimRight(o.BaseURL, "/") + "/chat/completions"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Reply{}, fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if o.Key != "" {
		request.Header.Set("Authorization", "Bearer "+o.Key)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return Reply{}, o.failed(ctx, "reach provider", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return Reply{}, ErrUnauthorized
	}
	// A server that ignores stream answers with one JSON body.
	if stream && response.StatusCode == http.StatusOK &&
		strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		return o.readStream(ctx, response.Body, timer, req.OnText)
	}
	reply, err := o.readWhole(ctx, response)
	if err == nil && stream {
		req.OnText(reply.Text)
	}
	return reply, err
}

func (o *OpenAI) readWhole(ctx context.Context, response *http.Response) (Reply, error) {
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return Reply{}, o.failed(ctx, "read response", err)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Reply{}, fmt.Errorf("provider returned unreadable data (%s)", response.Status)
	}
	if parsed.Error.Message != "" {
		return Reply{}, fmt.Errorf("provider: %s", textfmt.Plain(parsed.Error.Message))
	}
	if response.StatusCode != http.StatusOK {
		return Reply{}, fmt.Errorf("provider returned %s", response.Status)
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return Reply{}, fmt.Errorf("provider returned an empty reply")
	}
	return Reply{Text: strings.TrimSpace(parsed.Choices[0].Message.Content)}, nil
}

// readStream reads server-sent events until [DONE], passing each piece of
// text on as it comes.
func (o *OpenAI) readStream(ctx context.Context, body io.Reader, timer *time.Timer,
	onText func(string)) (Reply, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), maxResponseBytes)
	var text strings.Builder
	for scanner.Scan() {
		timer.Reset(o.Timeout)
		data, ok := strings.CutPrefix(scanner.Text(), "data:")
		if !ok {
			continue // blank separators and ": keep-alive" comments
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var event struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return Reply{}, fmt.Errorf("provider returned unreadable data")
		}
		if event.Error.Message != "" {
			return Reply{}, fmt.Errorf("provider: %s", textfmt.Plain(event.Error.Message))
		}
		if len(event.Choices) == 0 || event.Choices[0].Delta.Content == "" {
			continue
		}
		piece := event.Choices[0].Delta.Content
		if text.Len()+len(piece) > maxResponseBytes {
			return Reply{}, fmt.Errorf("provider's reply is longer than %d KB", maxResponseBytes>>10)
		}
		text.WriteString(piece)
		onText(piece)
	}
	if err := scanner.Err(); err != nil {
		return Reply{}, o.failed(ctx, "read response", err)
	}
	reply := strings.TrimSpace(text.String())
	if reply == "" {
		return Reply{}, fmt.Errorf("provider returned an empty reply")
	}
	return Reply{Text: reply}, nil
}

// failed names a timeout as one, rather than as the cancelled request it
// shows up as.
func (o *OpenAI) failed(ctx context.Context, doing string, err error) error {
	if errors.Is(context.Cause(ctx), errTimeout) {
		return fmt.Errorf("provider did not respond within %s", o.Timeout)
	}
	return fmt.Errorf("%s: %w", doing, err)
}
