package narrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/vrontier/listen/listener/internal/events"
)

// Config selects the language model (any OpenAI-compatible chat
// completions endpoint) and the narration rhythm.
type Config struct {
	BaseURL string // e.g. https://ai.example.org/v1
	Model   string
	APIKey  string
	Every   time.Duration // at most one narration per interval
	Mode    string        // observational | minimal | poetic
	Timeout time.Duration
}

// Narrator phrases the evidence while someone is watching and something has
// changed. It never decides what happened; see facts.go.
type Narrator struct {
	cfg       Config
	http      *http.Client
	stamper   *events.Stamper
	emit      func(events.Message)
	listeners func() int

	mu    sync.Mutex
	facts *facts
	last  time.Time
}

func New(cfg Config, stamper *events.Stamper, emit func(events.Message), listeners func() int) *Narrator {
	if cfg.Every == 0 {
		cfg.Every = 90 * time.Second
	}
	if cfg.Mode == "" {
		cfg.Mode = "observational"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &Narrator{
		cfg: cfg, http: &http.Client{Timeout: cfg.Timeout},
		stamper: stamper, emit: emit, listeners: listeners, facts: newFacts(),
	}
}

// Observe feeds every broadcast event into the evidence.
func (n *Narrator) Observe(msg events.Message) {
	if msg.Type == events.TypeNarrative {
		return
	}
	n.mu.Lock()
	n.facts.observe(msg)
	n.mu.Unlock()
}

// Run narrates until ctx is done.
func (n *Narrator) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.tick(ctx)
		}
	}
}

func (n *Narrator) tick(ctx context.Context) {
	if n.listeners() == 0 {
		return
	}
	n.mu.Lock()
	due := time.Since(n.last) >= n.cfg.Every && (n.facts.changed || n.last.IsZero()) && n.facts.feature.State != ""
	if !due {
		n.mu.Unlock()
		return
	}
	lines, ids, allowed := n.facts.build()
	n.facts.reset()
	n.last = time.Now()
	n.mu.Unlock()

	text, err := n.phrase(ctx, lines, allowed)
	if err != nil {
		log.Printf("narrate: %v", err)
		return
	}
	n.emit(n.stamper.Stamp(events.TypeNarrative, n.stamper.LastTimestamp(), 0, events.Narrative{
		Mode: n.cfg.Mode, Text: text, Evidence: ids, Model: n.cfg.Model,
	}))
}

// phrase asks the model, and asks once more if the text mentions anything
// the evidence doesn't contain or breaks the form.
func (n *Narrator) phrase(ctx context.Context, lines []string, allowed allowance) (string, error) {
	user := "Observations:\n- " + strings.Join(lines, "\n- ")
	msgs := []chatMessage{{Role: "system", Content: systemPrompt(n.cfg.Mode)}, {Role: "user", Content: user}}
	for attempt := 0; attempt < 2; attempt++ {
		raw, err := n.complete(ctx, msgs)
		if err != nil {
			return "", err
		}
		text := clean(raw)
		problem := allowed.check(text)
		if problem == "" {
			problem = formProblem(text, n.cfg.Mode)
		}
		if problem == "" {
			return text, nil
		}
		log.Printf("narrate: rejected %q (%s)", text, problem)
		msgs = append(msgs,
			chatMessage{Role: "assistant", Content: raw},
			chatMessage{Role: "user", Content: "That mentions " + problem + ", which is not in the observations or breaks the format. Rewrite it using only the observations above."})
	}
	return "", errors.New("no acceptable text after a retry")
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// complete calls an OpenAI-compatible /chat/completions endpoint.
func (n *Narrator) complete(ctx context.Context, msgs []chatMessage) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":       n.cfg.Model,
		"messages":    msgs,
		"max_tokens":  4096,
		"temperature": 0.4,
		// Hidden reasoning spends the whole budget on this short task.
		"reasoning_effort": "none",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(n.cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if n.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+n.cfg.APIKey)
	}
	resp, err := n.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("model: HTTP %d: %.200s", resp.StatusCode, b)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", fmt.Errorf("model: %w", err)
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("model: empty answer (finish %q)", firstFinish(out.Choices))
	}
	return out.Choices[0].Message.Content, nil
}

func firstFinish(c []struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
}) string {
	if len(c) == 0 {
		return ""
	}
	return c[0].FinishReason
}

func systemPrompt(mode string) string {
	const intro = "You write the live interpretation line for a quiet art installation that listens to an" +
		" environmental sound stream. You receive measured observations, listed roughly by prominence. "
	const rules = " Lead with what changed, returned or appeared; mention what merely continues only briefly." +
		" Name at most two frequencies. Use only facts from the observations: never invent sounds, sources," +
		" places, causes, feelings or numbers, and copy frequencies and motif numbers exactly as given. Do not" +
		" link facts by cause or by order unless the observations say so. No lists, headings, quotes or emojis." +
		" Do not mention observations, data, measurement or analysis."
	switch mode {
	case "minimal":
		return intro + "Write one short sentence, at most 15 words." + rules
	case "poetic":
		return intro + "Write exactly two sentences, at most 45 words in total, spare and quiet; you may use plain" +
			" imagery of sound, air and space." + rules
	default:
		return intro + "Write exactly two sentences, at most 45 words in total, calm and observational, as a" +
			" patient listener would notice things, not as an inventory." + rules
	}
}

var spaces = regexp.MustCompile(`\s+`)

// clean removes formatting a model may add despite instructions.
func clean(s string) string {
	s = strings.NewReplacer("**", "", "__", "", "`", "", "#", "").Replace(s)
	s = spaces.ReplaceAllString(strings.TrimSpace(s), " ")
	return strings.Trim(s, `"“”' `)
}

// formProblem rejects text that ignores the requested length.
func formProblem(text, mode string) string {
	words := len(strings.Fields(text))
	limit := 60
	if mode == "minimal" {
		limit = 22
	}
	if words == 0 || words > limit {
		return fmt.Sprintf("%d words", words)
	}
	if strings.Contains(text, "\n-") || strings.HasPrefix(text, "-") {
		return "a list"
	}
	return ""
}
