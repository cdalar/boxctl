package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

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
		if h.modelFrom != "" {
			from = "harness " + h.modelFrom
		}
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
	if v, ok := passedFlag(args, "--model"); ok && strings.TrimSpace(v) != "" {
		c.model, c.modelFrom, c.passedModel = v, "-- --model", true
	}
	if v, ok := passedFlag(args, "--effort"); ok && strings.TrimSpace(v) != "" {
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
	// Both end up in the box's labels, which the server refuses with a
	// control character in them -- after the box is asked for.
	if strings.ContainsFunc(c.model, unicode.IsControl) {
		return modelChoice{}, fmt.Errorf("model %q (from %s) has a control character in it", c.model, c.modelFrom)
	}
	if strings.ContainsFunc(c.effort, unicode.IsControl) {
		return modelChoice{}, fmt.Errorf("effort %q (from %s) has a control character in it", c.effort, c.effortFrom)
	}
	if c.effort != "" && !c.passedEffort && !slices.Contains(claudeEfforts, c.effort) {
		return modelChoice{}, fmt.Errorf("effort %q (from %s): use one of %s", c.effort, c.effortFrom, strings.Join(claudeEfforts, ", "))
	}
	return c, nil
}

// dropEmptyFlags removes a passed-through --model or --effort whose value
// is empty (--model "", --model=): resolveModel treats it as not
// given, so claude mustn't get it either -- after the --model boxctl
// adds, it would be the one claude takes.
func dropEmptyFlags(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if (a == "--model" || a == "--effort") && i+1 < len(args) && strings.TrimSpace(args[i+1]) == "" {
			i++
			continue
		}
		if v, ok := strings.CutPrefix(a, "--model="); ok && strings.TrimSpace(v) == "" {
			continue
		}
		if v, ok := strings.CutPrefix(a, "--effort="); ok && strings.TrimSpace(v) == "" {
			continue
		}
		out = append(out, a)
	}
	return out
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

// modelSetting is what a settings file on the box says the model and
// effort are: from names it in describe.
type modelSetting struct {
	from string
	harnessModel
}

// describe is the line saying what the run uses. settings are what the
// settings files on the box say, highest precedence first, which apply
// where nothing is chosen.
func (c modelChoice) describe(settings []modelSetting) string {
	part := func(v, from string, setting func(modelSetting) string) string {
		if v != "" {
			return v + " (" + from + ")"
		}
		for _, s := range settings {
			if v := setting(s); v != "" {
				return v + " (" + s.from + ")"
			}
		}
		return "Claude Code's default"
	}
	return "Model: " + part(c.model, c.modelFrom, func(s modelSetting) string { return s.Model }) +
		"; effort: " + part(c.effort, c.effortFrom, func(s modelSetting) string { return s.Effort })
}

// boxModelSettings are the settings files that decide the model and effort
// on the box when nothing is chosen: the harness's managed-settings.json
// (/etc/claude-code, which beats user settings), then your copied
// ~/.claude/settings.json.
func boxModelSettings(h *harness) []modelSetting {
	var out []modelSetting
	if h != nil && slices.Contains(h.managed, "managed-settings.json") {
		if m, ok := settingsModel(filepath.Join(h.dir, "managed-settings.json")); ok {
			out = append(out, modelSetting{"harness " + h.label + "'s managed-settings.json", m})
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if m, ok := settingsModel(filepath.Join(home, ".claude", "settings.json")); ok {
			out = append(out, modelSetting{"your Claude settings", m})
		}
	}
	return out
}

// settingsModel is the model and effortLevel of a Claude settings file.
func settingsModel(path string) (harnessModel, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return harnessModel{}, false
	}
	var s struct {
		Model       string `json:"model"`
		EffortLevel string `json:"effortLevel"`
	}
	if json.Unmarshal(data, &s) != nil {
		return harnessModel{}, false
	}
	return harnessModel{Model: s.Model, Effort: s.EffortLevel}, true
}

// runModel is the running claude command's choice, which a box created
// for it is labeled with (withModelLabels). Nil for an agent that has
// none (kilo): boxAgent.runs resets it.
var runModel *modelChoice

// chooseModel resolves the run's model and effort and makes them
// runModel, before any box is created; args comes back without an empty
// passed-through --model or --effort (dropEmptyFlags). It doesn't say
// what they are: announceModel does, once it's known a new session
// starts -- one already running keeps its own model.
func chooseModel(args []string) (modelChoice, []string, error) {
	c, err := resolveModel(args, modelSources(claudeHarness, cfg))
	if err != nil {
		return c, args, err
	}
	runModel = &c
	return c, dropEmptyFlags(args), nil
}

// announceModel prints the model and effort a new session gets.
func announceModel(c modelChoice) {
	sayNote("%s", c.describe(boxModelSettings(claudeHarness)))
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
