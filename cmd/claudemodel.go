package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cdalar/boxctl/internal/client"
	"github.com/cdalar/boxctl/internal/config"
)

// Which model Claude runs on the box, and with what effort: --model and
// --effort, passed to claude on the box as its own --model and --effort
// flags (Claude Code 2.1.322 takes both, for claude -p and an interactive
// session alike, and a flag beats the effortLevel/model of the
// settings.json copied there -- so the copied settings stay the fallback
// without being rewritten).
//
// Precedence, highest first: an explicit `-- --model x` (or --effort)
// passed through to claude, --model/--effort, the harness's
// harnessModelFile, BOXCTL_CLAUDE_MODEL/BOXCTL_CLAUDE_EFFORT, the
// claude_model/claude_effort keys of ~/.boxctl/config.json, and last
// whatever the copied ~/.claude/settings.json says -- for which boxctl
// passes nothing. The model name is passed through as given: an alias
// (fable, sonnet) is resolved by the Claude Code on the box.

const (
	envClaudeModel  = "BOXCTL_CLAUDE_MODEL"
	envClaudeEffort = "BOXCTL_CLAUDE_EFFORT"
	// harnessModelFile, in a harness directory, sets the run's model and
	// effort: {"model": "...", "effort": "..."}, either optional. It
	// configures boxctl and isn't copied to the box.
	harnessModelFile = "model.json"
	labelModel       = "boxctl.model"
	labelEffort      = "boxctl.effort"
)

// claudeEfforts are the effort levels claude --effort takes.
var claudeEfforts = []string{"low", "medium", "high", "xhigh", "max"}

var claudeModelFlag, claudeEffortFlag string

// harnessModel is a harness's harnessModelFile.
type harnessModel struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

// loadHarnessModel reads dir's harnessModelFile; nil when there's none.
func loadHarnessModel(dir string) (*harnessModel, error) {
	data, err := os.ReadFile(filepath.Join(dir, harnessModelFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m harnessModel
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("harness %s: %s: %w", dir, harnessModelFile, err)
	}
	return &m, nil
}

// modelChoice is the model and effort a run uses, and where each came
// from; "" for nothing chosen (the copied settings, or Claude's default).
type modelChoice struct {
	model, effort         string
	modelFrom, effortFrom string
	// passedModel, passedEffort: the value is in the passthrough
	// arguments already, so args adds nothing for it.
	passedModel, passedEffort bool
}

// modelSources are where a model or effort can come from, highest
// precedence first, below the passthrough arguments.
type modelSource struct{ modelFrom, effortFrom, model, effort string }

func modelSources(h *harness, cfg *config.Config) []modelSource {
	srcs := []modelSource{{"--model", "--effort", claudeModelFlag, claudeEffortFlag}}
	if h != nil && h.model != nil {
		from := "harness " + h.label
		srcs = append(srcs, modelSource{from, from, h.model.Model, h.model.Effort})
	}
	srcs = append(srcs, modelSource{envClaudeModel, envClaudeEffort, os.Getenv(envClaudeModel), os.Getenv(envClaudeEffort)})
	if cfg != nil {
		srcs = append(srcs, modelSource{"claude_model in ~/.boxctl/config.json", "claude_effort in ~/.boxctl/config.json", cfg.ClaudeModel, cfg.ClaudeEffort})
	}
	return srcs
}

// resolveModel picks the run's model and effort from args (what's passed
// through to claude) and srcs, and refuses an effort level claude doesn't
// take -- before any box exists. A passed-through value is claude's to
// check.
func resolveModel(args []string, srcs []modelSource) (modelChoice, error) {
	var c modelChoice
	if v, ok := passedFlag(args, "--model"); ok {
		c.model, c.modelFrom, c.passedModel = v, "-- --model", true
	}
	if v, ok := passedFlag(args, "--effort"); ok {
		c.effort, c.effortFrom, c.passedEffort = v, "-- --effort", true
	}
	for _, s := range srcs {
		if m := strings.TrimSpace(s.model); c.model == "" && m != "" {
			c.model, c.modelFrom = m, s.modelFrom
		}
		if e := strings.TrimSpace(s.effort); c.effort == "" && e != "" {
			c.effort, c.effortFrom = strings.ToLower(e), s.effortFrom
		}
	}
	if c.effort != "" && !c.passedEffort && !slices.Contains(claudeEfforts, c.effort) {
		return modelChoice{}, fmt.Errorf("effort %q (from %s): use one of %s", c.effort, c.effortFrom, strings.Join(claudeEfforts, ", "))
	}
	return c, nil
}

// passedFlag finds flag's value in args: "flag value" or "flag=value".
func passedFlag(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return v, true
		}
	}
	return "", false
}

// args are what claude gets for the choice, besides what's passed through.
func (c modelChoice) args() []string {
	var a []string
	if c.model != "" && !c.passedModel {
		a = append(a, "--model", c.model)
	}
	if c.effort != "" && !c.passedEffort {
		a = append(a, "--effort", c.effort)
	}
	return a
}

// labels record the choice on the box: claude ls's MODEL, claude status's
// Model. An empty value is how "nothing chosen" is recorded on an
// existing box (SetLabels drops it); a new box just doesn't get it.
func (c modelChoice) labels() map[string]string {
	l := map[string]string{labelModel: "", labelEffort: ""}
	if len(c.model) <= 255 {
		l[labelModel] = c.model
	}
	l[labelEffort] = c.effort
	return l
}

// describe is the line printed before the box is created. settings is
// what the copied Claude settings say, which applies where nothing is
// chosen.
func (c modelChoice) describe(settings harnessModel) string {
	part := func(v, from, setting string) string {
		switch {
		case v != "":
			return v + " (" + from + ")"
		case setting != "":
			return setting + " (your Claude settings)"
		}
		return "Claude Code's default"
	}
	return "Model: " + part(c.model, c.modelFrom, settings.Model) + "; effort: " + part(c.effort, c.effortFrom, settings.Effort)
}

// localClaudeModel is what ~/.claude/settings.json -- copied to the box --
// says the model and effort are, for describe.
func localClaudeModel() harnessModel {
	home, err := os.UserHomeDir()
	if err != nil {
		return harnessModel{}
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		return harnessModel{}
	}
	var s struct {
		Model       string `json:"model"`
		EffortLevel string `json:"effortLevel"`
	}
	if json.Unmarshal(data, &s) != nil {
		return harnessModel{}
	}
	return harnessModel{Model: s.Model, Effort: s.EffortLevel}
}

// runModel is the running claude command's choice, which a box created
// for it is labeled with (withModelLabels). Nil for an agent that has
// none (kilo): boxAgent.runs resets it.
var runModel *modelChoice

// chooseModel resolves the run's model and effort, says what they are and
// makes them runModel. Everything here happens before a box is created.
func chooseModel(args []string) (modelChoice, error) {
	c, err := resolveModel(args, modelSources(claudeHarness, cfg))
	if err != nil {
		return c, err
	}
	sayNote("%s", c.describe(localClaudeModel()))
	runModel = &c
	return c, nil
}

// withModelLabels is labels plus the run's model and effort, for a new box.
func withModelLabels(labels map[string]string) map[string]string {
	if runModel == nil {
		return labels
	}
	out := map[string]string{}
	for k, v := range labels {
		out[k] = v
	}
	for k, v := range runModel.labels() {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

// relabelModel brings an existing box's model labels up to the run's,
// when it starts a new session there (a new box got them at create).
// Best effort: the run goes on without it.
func relabelModel(ctx context.Context, name string) {
	if runModel == nil {
		return
	}
	c := newClient()
	vms, err := c.List(ctx)
	if err != nil {
		return
	}
	i := slices.IndexFunc(vms, func(vm client.VM) bool { return vm.Name == name })
	if i < 0 {
		return
	}
	have, want := vms[i].Labels, runModel.labels()
	if have[labelModel] == want[labelModel] && have[labelEffort] == want[labelEffort] {
		return
	}
	labels := map[string]string{}
	for k, v := range have {
		labels[k] = v
	}
	for k, v := range want {
		if v == "" {
			delete(labels, k)
		} else {
			labels[k] = v
		}
	}
	if _, err := c.SetLabels(ctx, name, labels); err != nil {
		sayWarn("couldn't record the model on %s: %v", name, err)
	}
}

// modelText is a box's MODEL: its model label, with the effort after it;
// "-" with neither.
func modelText(vm client.VM) string {
	m, e := vm.Labels[labelModel], vm.Labels[labelEffort]
	switch {
	case m == "" && e == "":
		return "-"
	case e == "":
		return m
	case m == "":
		return "- (" + e + ")"
	}
	return m + " (" + e + ")"
}
