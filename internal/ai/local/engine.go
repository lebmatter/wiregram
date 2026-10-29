package local

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	llama "github.com/goccy/go-llama"

	"wiregram/internal/ai"
)

const (
	// contextTokens is the context window wiregram opens: what a summary
	// part and its reply need, and small enough that a 3B model's cache fits
	// beside its weights in go-llama's 4 GiB.
	contextTokens = 8192
	// idleUnload frees the model's memory once it has gone unused this long.
	idleUnload = 5 * time.Minute
)

// Engine runs catalog models in-process and downloads them into its folder.
// One model is loaded at a time, and requests to it take turns.
type Engine struct {
	dir    string
	client *http.Client

	mu       sync.Mutex // the loaded model; held for a whole request
	inst     *llama.Llama
	model    *llama.Model
	ctx      *llama.Context
	loaded   string // the ID of the model loaded, "" for none
	lastUsed time.Time
	idle     *time.Timer

	dlMu      sync.Mutex
	downloads map[string]*download
	failed    map[string]error
}

// New is an engine over the models in dir.
func New(dir string) *Engine {
	return &Engine{
		dir: dir,
		client: &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return fmt.Errorf("refusing to download over %s", req.URL.Scheme)
			}
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		}},
		downloads: make(map[string]*download),
		failed:    make(map[string]error),
	}
}

// Close stops the downloads and frees the loaded model.
func (e *Engine) Close() {
	e.dlMu.Lock()
	for _, d := range e.downloads {
		d.cancel()
	}
	e.dlMu.Unlock()
	e.mu.Lock()
	e.unload()
	e.mu.Unlock()
}

// load makes m the loaded model. e.mu is held.
func (e *Engine) load(m Model) error {
	if e.loaded == m.ID {
		return nil
	}
	e.unload()
	if slowBuild() {
		return fmt.Errorf("this wiregram was built without GOAMD64=v2, which local models " +
			"need to run at a usable speed; build it with make")
	}
	if !e.Installed(m) {
		return fmt.Errorf("%s is not downloaded; get it in Settings > AI", m.Name)
	}
	inst, err := llama.New(llama.WithPreopenDir(e.dir))
	if err != nil {
		return fmt.Errorf("start the local engine: %w", err)
	}
	model, err := inst.LoadModel(m.File)
	if err != nil {
		inst.Close()
		return fmt.Errorf("load %s: %w", m.Name, err)
	}
	ctx, err := model.NewContext(llama.ContextParams{NCtx: uint32(min(m.Context, contextTokens))})
	if err != nil {
		inst.Close()
		return fmt.Errorf("load %s: %w", m.Name, err)
	}
	e.inst, e.model, e.ctx, e.loaded = inst, model, ctx, m.ID
	e.idle = time.AfterFunc(idleUnload, e.unloadIdle)
	return nil
}

// unload frees the loaded model, if any. e.mu is held.
func (e *Engine) unload() {
	if e.inst == nil {
		return
	}
	e.idle.Stop()
	e.inst.Close() // frees its models and contexts too
	e.inst, e.model, e.ctx, e.loaded = nil, nil, nil, ""
}

// slowBuild reports an amd64 build for GOAMD64=v1, Go's default, where
// go-llama has no SIMD and a reply takes many minutes.
func slowBuild() bool {
	if runtime.GOARCH != "amd64" {
		return false
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range info.Settings {
		if s.Key == "GOAMD64" {
			return s.Value == "v1"
		}
	}
	return true
}

func (e *Engine) unloadIdle() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.inst == nil {
		return
	}
	if wait := idleUnload - time.Since(e.lastUsed); wait > 0 {
		e.idle.Reset(wait)
		return
	}
	e.unload()
}

func (e *Engine) Chat(ctx context.Context, req ai.Request) (ai.Reply, error) {
	m, ok := Find(req.Model)
	if !ok {
		return ai.Reply{}, fmt.Errorf("%q is not a local model", req.Model)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return ai.Reply{}, err // cancelled while waiting its turn
	}
	if err := e.load(m); err != nil {
		return ai.Reply{}, err
	}
	defer func() { e.lastUsed = time.Now() }()

	msgs := make([]llama.Message, len(req.Messages))
	for i, msg := range req.Messages {
		msgs[i] = llama.Message{Role: msg.Role, Content: msg.Text} // no model here sees images
	}
	prompt, err := e.model.ApplyChatTemplate(msgs, "", true)
	if err != nil {
		return ai.Reply{}, fmt.Errorf("%s: %w", m.Name, err)
	}
	tokens, err := e.model.Tokenize(prompt, false, true)
	if err != nil {
		return ai.Reply{}, fmt.Errorf("%s: %w", m.Name, err)
	}
	room := min(m.Context, contextTokens) - len(tokens)
	if room < 64 {
		return ai.Reply{}, fmt.Errorf("this is too long for %s to read", m.Name)
	}
	stop := context.AfterFunc(ctx, func() { e.ctx.Interrupt() })
	defer stop()
	res, err := e.ctx.Stream(prompt, llama.Params{
		NPredict:      min(req.MaxTokens, room),
		Temperature:   0.7,
		TopK:          40,
		TopP:          0.9,
		MinP:          0.05,
		RepeatPenalty: 1.1,
		RepeatLastN:   64,
		Seed:          rand.Uint32(),
		CachePrompt:   true,
	}, req.OnText)
	if ctx.Err() != nil {
		return ai.Reply{}, ctx.Err()
	}
	if err != nil {
		return ai.Reply{}, fmt.Errorf("%s: %w", m.Name, err)
	}
	text := strings.TrimSpace(res.Text)
	if text == "" {
		return ai.Reply{}, errors.New(m.Name + " returned an empty reply")
	}
	return ai.Reply{Text: text}, nil
}
