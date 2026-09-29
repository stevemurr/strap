package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
)

// Variant is one condition a probe samples under. Configure changes what the
// harness renders into the request, such as a prompt or a tool description;
// Model changes how the model is asked, such as thinking or sampling. Both
// start from the recorded session.
type Variant struct {
	Name      string
	Configure func(*harness.Config)
	Model     func(*harness.ModelConfig)
	// Request edits the built request directly, for what no configuration
	// reaches, such as a tool description written in code.
	Request func(*provider.Request)
	// After probes the call that follows the recorded one instead: the
	// request gets the recorded response, then the messages After returns for
	// it, such as a tool result the recorded harness did not produce.
	After func(provider.Message) []provider.Message
}

// ProbeOptions choose the model call to re-sample and how.
type ProbeOptions struct {
	Agent    identity.ActorID
	Call     int // The agent's model call, from 1.
	Samples  int // Per variant; default 10.
	Parallel int // Concurrent samples; default 4.
	// Model is the endpoint to sample; default the model the agent's role
	// recorded. Traces never record credentials.
	Model    *harness.ModelConfig
	Variants []Variant
	// Classify names a sample's outcome; default Classify.
	Classify func(req provider.Request, resp provider.Response, err error) string
	Timeout  time.Duration // Per sample; default 5 minutes.
	// Sampler builds the provider for a variant's model; default the model's
	// own HTTP provider. Tests supply a scripted one.
	Sampler func(harness.ModelConfig) (provider.Provider, error)
	// Raw keeps each sample's wire exchange: the request body and the raw
	// stream frames, to see what the model emitted when a response parses
	// empty or malformed.
	Raw bool
	// AllowStale probes a trace the current harness no longer reproduces.
	// Its earlier turns were made under other prompts or tools, so samples
	// describe a situation the harness can no longer produce.
	AllowStale bool
}

// ErrStale reports a trace recorded by a harness that builds a different
// request for the probed call.
var ErrStale = errors.New("the trace was recorded by a different harness")

// Sample is one response to a probed request.
type Sample struct {
	Outcome  string
	Response provider.Response
	Err      string
	Elapsed  time.Duration
	Request  json.RawMessage `json:",omitempty"` // The wire request body, with Raw.
	Frames   []string        `json:",omitempty"` // The raw stream frames, with Raw.
}

// VariantResult is what one variant's samples did.
type VariantResult struct {
	Name    string
	Request provider.Request // The request the harness built under the variant.
	// Recorded reports whether that request is the one recorded, which it
	// must be for a variant that changes only the model.
	Recorded bool
	Model    harness.ModelConfig
	Samples  []Sample
}

// Counts tallies outcomes, most frequent first.
func (v VariantResult) Counts() []OutcomeCount {
	n := map[string]int{}
	for _, s := range v.Samples {
		n[s.Outcome]++
	}
	var out []OutcomeCount
	for k, c := range n {
		out = append(out, OutcomeCount{k, c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Outcome < out[j].Outcome
	})
	return out
}

type OutcomeCount struct {
	Outcome string
	Count   int
}

// textCall matches a tool call written out as text: name( at a word start.
var textCall = regexp.MustCompile(`\b([a-z][a-z0-9_]*)\s*\(`)

// Classify names what a response did with the request's tools: called one
// ("call send_message"; create_agent names the role, as in
// "call create_agent(web_researcher)"), wrote one out as text instead
// ("text send_message"), answered in plain text ("text"), or failed ("error").
func Classify(req provider.Request, resp provider.Response, err error) string {
	if err != nil {
		var rejected *provider.ToolArgumentsError
		if errors.As(err, &rejected) {
			return "rejected " + rejected.Name
		}
		return "error"
	}
	if len(resp.ToolCalls) > 0 {
		names := make([]string, len(resp.ToolCalls))
		for i, c := range resp.ToolCalls {
			names[i] = c.Name
			// Which role an agent is created with is the manager's dispatch
			// decision, so it is part of the outcome.
			var created struct {
				Input roster.CreateRequest `json:"input"`
			}
			if c.Name == "create_agent" && json.Unmarshal(c.Arguments, &created) == nil && created.Input.Role != "" {
				names[i] += "(" + string(created.Input.Role) + ")"
			}
		}
		return "call " + strings.Join(names, ",")
	}
	tools := map[string]bool{}
	for _, t := range req.Tools {
		tools[t.Name] = true
	}
	for _, m := range textCall.FindAllStringSubmatch(resp.Content, -1) {
		if tools[m[1]] {
			return "text " + m[1]
		}
	}
	return "text"
}

// Probe replays a recording up to one model call under each variant, takes
// the request the harness built there, and samples the model on it. Nothing
// after that call runs, so each sample is exactly the recorded situation with
// one thing changed.
func Probe(ctx context.Context, rec *Recording, opts ProbeOptions) ([]VariantResult, error) {
	if opts.Samples <= 0 {
		opts.Samples = 10
	}
	if opts.Parallel <= 0 {
		opts.Parallel = 4
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Minute
	}
	if opts.Classify == nil {
		opts.Classify = Classify
	}
	if len(opts.Variants) == 0 {
		opts.Variants = []Variant{{Name: "recorded"}}
	}
	if n := len(outputsOf(rec, opts.Agent)); opts.Call < 1 || opts.Call > n {
		return nil, fmt.Errorf("%s made %d model calls; choose a call from 1 to %d", opts.Agent, n, n)
	}
	if !opts.AllowStale {
		req, err := capture(ctx, rec, opts.Agent, opts.Call, nil)
		if err != nil {
			return nil, err
		}
		p := &player{rec: rec}
		if d, changed := p.compare(opts.Agent, opts.Call, outputsOf(rec, opts.Agent)[opts.Call-1], req); changed {
			return nil, fmt.Errorf("%w: the current harness builds a different request for %s call %d (%s); record a fresh run, or allow a stale probe", ErrStale, opts.Agent, opts.Call, d.Detail)
		}
	}
	base := opts.Model
	if base == nil {
		role, ok := rec.RoleConfig(rec.Role(opts.Agent))
		if !ok || role.Model == nil {
			return nil, fmt.Errorf("the recording has no model for %s; supply one", opts.Agent)
		}
		base = role.Model
	}
	var out []VariantResult
	for _, v := range opts.Variants {
		req, err := capture(ctx, rec, opts.Agent, opts.Call, v.Configure)
		if err != nil {
			return out, fmt.Errorf("variant %s: %w", v.Name, err)
		}
		model := *base
		model.Generation = model.Generation.Clone()
		if v.Model != nil {
			v.Model(&model)
		}
		sampler := opts.Sampler
		if sampler == nil {
			sampler = func(m harness.ModelConfig) (provider.Provider, error) {
				return m.NewProvider(&http.Client{Timeout: opts.Timeout})
			}
		}
		live, err := sampler(model)
		if err != nil {
			return out, fmt.Errorf("variant %s: %w", v.Name, err)
		}
		recorded := outputsOf(rec, opts.Agent)[opts.Call-1]
		p := &player{rec: rec}
		_, changed := p.compare(opts.Agent, opts.Call, recorded, req)
		if v.Request != nil {
			v.Request(&req)
		}
		if v.After != nil {
			if recorded.Response == nil {
				return out, fmt.Errorf("variant %s: %s call %d has no recorded response to follow", v.Name, opts.Agent, opts.Call)
			}
			req.Messages = append(req.Messages, *recorded.Response)
			req.Messages = append(req.Messages, v.After(*recorded.Response)...)
		}
		result := VariantResult{Name: v.Name, Request: req, Recorded: !changed && v.Request == nil && v.After == nil, Model: model, Samples: make([]Sample, opts.Samples)}
		var wg sync.WaitGroup
		slots := make(chan struct{}, opts.Parallel)
		for i := range result.Samples {
			wg.Add(1)
			go func() {
				defer wg.Done()
				slots <- struct{}{}
				defer func() { <-slots }()
				sampleCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
				defer cancel()
				var body []byte
				var frames []string
				if opts.Raw {
					sampleCtx = provider.WithRawTap(sampleCtx, provider.RawTap{
						Request: func(b []byte) { body = b },
						Frame:   func(f string) { frames = append(frames, f) },
					})
				}
				started := time.Now()
				resp, err := live.Submit(sampleCtx, req, nil)
				s := Sample{Outcome: opts.Classify(req, resp, err), Response: resp, Elapsed: time.Since(started), Request: body, Frames: frames}
				if err != nil {
					s.Err = err.Error()
				}
				result.Samples[i] = s
			}()
		}
		wg.Wait()
		out = append(out, result)
		if err := ctx.Err(); err != nil {
			return out, err
		}
	}
	return out, nil
}

func outputsOf(rec *Recording, agentID identity.ActorID) []Output {
	var out []Output
	for _, o := range rec.Outputs {
		if o.Agent == agentID {
			out = append(out, o)
		}
	}
	return out
}

// currentPrompts replaces the recorded prompts with this build's. A replay
// reproduces a recording with the prompts it ran; a probe asks what the
// current harness does, so it renders the current ones.
func currentPrompts(c *harness.Config) {
	d := harness.DefaultConfig()
	c.Agent.Prompt, c.Manager.Prompt, c.Debugger.Prompt = d.Agent.Prompt, d.Manager.Prompt, d.Debugger.Prompt
	c.Implementor.Prompt, c.Auditor.Prompt = d.Implementor.Prompt, d.Auditor.Prompt
	c.WebResearcher.Prompt, c.DeepResearcher.Prompt, c.Experimenter.Prompt, c.Reviewer.Prompt = d.WebResearcher.Prompt, d.DeepResearcher.Prompt, d.Experimenter.Prompt, d.Reviewer.Prompt
}

// capture replays until the agent's call and returns the request the current
// harness built there.
func capture(ctx context.Context, rec *Recording, agentID identity.ActorID, call int, configure func(*harness.Config)) (provider.Request, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	t := &target{agent: agentID, call: call, requests: make(chan provider.Request, 1)}
	current := func(c *harness.Config) {
		currentPrompts(c)
		if configure != nil {
			configure(c)
		}
	}
	r, err := start(ctx, rec, Options{Configure: current, capture: t})
	if err != nil {
		return provider.Request{}, err
	}
	defer r.stop()
	sent := make(chan error, 1)
	go func() { sent <- r.sendUsers(ctx) }()
	select {
	case req := <-t.requests:
		return req, nil
	case err := <-sent:
		if err != nil {
			return provider.Request{}, err
		}
		select {
		case req := <-t.requests:
			return req, nil
		case <-ctx.Done():
			return provider.Request{}, fmt.Errorf("the replay never reached %s call %d", agentID, call)
		}
	case <-ctx.Done():
		return provider.Request{}, fmt.Errorf("the replay never reached %s call %d", agentID, call)
	}
}
