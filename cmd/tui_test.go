package cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cdalar/boxctl/internal/client"
	tea "github.com/charmbracelet/bubbletea"
)

func tuiFixture() []client.VM {
	now := time.Now()
	return []client.VM{
		{ID: "1", Name: "old-paused", State: "paused", Image: "debian-slim", CreatedAt: now.Add(-48 * time.Hour)},
		{ID: "2", Name: "web", State: "running", Image: "debian-slim", Size: "small", CreatedAt: now.Add(-2 * time.Hour), Labels: map[string]string{"team": "web"}},
		{ID: "3", Name: "goal-box", State: "running", Image: "claude-agent", CreatedAt: now.Add(-1 * time.Hour),
			Labels:    map[string]string{"boxctl.run": "goal", "boxctl.agent": "claude", "boxctl.repo": "cdalar/boxctl"},
			RunStatus: json.RawMessage(`{"status":"running"}`)},
		{ID: "4", Name: "booting", State: "provisioning", CreatedAt: now},
	}
}

func names(vms []client.VM) string {
	var out []string
	for _, vm := range vms {
		out = append(out, vm.Name)
	}
	return strings.Join(out, " ")
}

func TestTUISortAndVisible(t *testing.T) {
	vms := tuiFixture()
	sortBoxes(vms)
	if got, want := names(vms), "booting goal-box web old-paused"; got != want {
		t.Fatalf("sorted = %q, want %q", got, want)
	}
	if got, want := names(visibleBoxes(vms, false, "")), "booting goal-box web"; got != want {
		t.Errorf("live = %q, want %q", got, want)
	}
	if got, want := names(visibleBoxes(vms, true, "")), "booting goal-box web old-paused"; got != want {
		t.Errorf("all = %q, want %q", got, want)
	}
	for filter, want := range map[string]string{
		"WEB":           "web",            // name, case-insensitively
		"team=web":      "web",            // a label
		"cdalar/boxctl": "goal-box",       // the project column
		"claude goal":   "goal-box",       // the run column
		"debian":        "web old-paused", // image, paused one included with all
		"nothing":       "",
	} {
		if got := names(visibleBoxes(vms, true, filter)); got != want {
			t.Errorf("filter %q = %q, want %q", filter, got, want)
		}
	}
}

// fakeBoxServer answers GET /api/vms with vms, and records anything else.
func fakeBoxServer(t *testing.T, vms []client.VM) (*client.Client, *[]string) {
	t.Helper()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/vms" {
			_ = json.NewEncoder(w).Encode(vms)
			return
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return client.New(srv.URL, "tok"), &calls
}

// runCmd runs cmd and returns the messages it produces, a batch
// flattened.
func runCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for _, c := range batch {
		out = append(out, runCmd(c)...)
	}
	return out
}

// press sends one key to the model.
func press(t *testing.T, m tuiModel, key string) (tuiModel, tea.Cmd) {
	t.Helper()
	var msg tea.KeyMsg
	switch key {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	next, cmd := m.Update(msg)
	return next.(tuiModel), cmd
}

func TestTUIListAndKeys(t *testing.T) {
	c, calls := fakeBoxServer(t, tuiFixture())
	m := newTUI(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = next.(tuiModel)

	// The real listing, through the client.
	next, _ = m.Update(m.fetch()())
	m = next.(tuiModel)
	if m.loading {
		t.Fatal("still loading after the listing arrived")
	}
	view := m.View()
	for _, want := range []string{"web", "goal-box", "booting", "claude goal: running", "cdalar/boxctl"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "old-paused") {
		t.Errorf("paused box shown without a:\n%s", view)
	}
	if !strings.Contains(view, "1 not running") {
		t.Errorf("view doesn't say a box is hidden:\n%s", view)
	}

	// a shows it; the cursor stays on the first box.
	m, _ = press(t, m, "a")
	if !strings.Contains(m.View(), "old-paused") {
		t.Error("paused box not shown after a")
	}
	if m.current().Name != "booting" {
		t.Errorf("cursor on %s, want booting", m.current().Name)
	}
	m, _ = press(t, m, "j")
	m, _ = press(t, m, "j")
	if m.current().Name != "web" {
		t.Errorf("cursor on %s after j j, want web", m.current().Name)
	}
	// Two keys that arrived in one message are two keypresses.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("kk")})
	m = next.(tuiModel)
	if m.current().Name != "booting" {
		t.Errorf("cursor on %s after kk, want booting", m.current().Name)
	}
	m, _ = press(t, m, "G")
	if m.current().Name != "old-paused" {
		t.Errorf("cursor on %s after G, want old-paused", m.current().Name)
	}

	// Pausing a paused box is refused here, not sent.
	m, cmd := press(t, m, "p")
	if cmd != nil || !m.statusErr || !strings.Contains(m.status, "not running") {
		t.Errorf("p on a paused box: cmd=%v status=%q", cmd, m.status)
	}
	// So is ssh.
	m, cmd = press(t, m, "enter")
	if cmd != nil || !strings.Contains(m.status, "resume it first") {
		t.Errorf("enter on a paused box: cmd=%v status=%q", cmd, m.status)
	}
	// Resuming is sent, and the box is pending meanwhile.
	m, cmd = press(t, m, "r")
	if cmd == nil || m.pending["old-paused"] != "resume" {
		t.Fatalf("r on a paused box: cmd=%v pending=%v", cmd, m.pending)
	}
	if !strings.Contains(m.View(), "resuming") {
		t.Error("pending box's row doesn't say resuming")
	}
	// Keys on a pending box are refused until it's done.
	m, cmd = press(t, m, "d")
	if cmd != nil || m.mode != modeList || !strings.Contains(m.status, "being resumed") {
		t.Errorf("d on a pending box: cmd=%v mode=%v status=%q", cmd, m.mode, m.status)
	}
	next, _ = m.Update(tuiActionMsg{name: "old-paused", verb: "resume"})
	m = next.(tuiModel)
	if _, ok := m.pending["old-paused"]; ok || m.status != "resumed old-paused" {
		t.Errorf("after the action: pending=%v status=%q", m.pending, m.status)
	}

	// Destroy asks first; n cancels.
	m, _ = press(t, m, "g")
	m, _ = press(t, m, "j") // goal-box, an unfinished goal run
	m, _ = press(t, m, "d")
	if m.mode != modeConfirm || !strings.Contains(m.View(), "Destroy goal-box?") || !strings.Contains(m.View(), "is not done") {
		t.Errorf("d: mode=%v view:\n%s", m.mode, m.View())
	}
	m, cmd = press(t, m, "n")
	if cmd != nil || m.mode != modeList || m.status != "cancelled" {
		t.Errorf("n: cmd=%v mode=%v status=%q", cmd, m.mode, m.status)
	}
	// y sends it.
	m, _ = press(t, m, "d")
	m, cmd = press(t, m, "y")
	if cmd == nil || m.pending["goal-box"] != "destroy" {
		t.Fatalf("y: cmd=%v pending=%v", cmd, m.pending)
	}
	var acted *tuiActionMsg
	for _, msg := range runCmd(cmd) {
		if am, ok := msg.(tuiActionMsg); ok {
			acted = &am
		}
	}
	if acted == nil || acted.err != nil {
		t.Fatalf("destroy's result: %#v", acted)
	}
	if len(*calls) != 1 || (*calls)[0] != "DELETE /api/vms/goal-box" {
		t.Errorf("server saw %v", *calls)
	}

	// The filter narrows the rows as it's typed; esc clears it.
	m, _ = press(t, m, "/")
	for _, r := range "web" {
		m, _ = press(t, m, string(r))
	}
	if got := names(m.rows); got != "web" {
		t.Errorf("filtered rows = %q", got)
	}
	m, _ = press(t, m, "enter")
	if m.mode != modeList || !strings.Contains(m.View(), `filter "web"`) {
		t.Errorf("enter keeps the filter: mode=%v view:\n%s", m.mode, m.View())
	}
	// esc in the list clears a kept filter rather than quitting.
	m, cmd = press(t, m, "esc")
	if cmd != nil || names(m.rows) != "booting goal-box web old-paused" {
		t.Errorf("esc with a filter: cmd=%v rows=%q", cmd, names(m.rows))
	}
	m, _ = press(t, m, "/")
	m, _ = press(t, m, "x")
	m, _ = press(t, m, "esc")
	if got := names(m.rows); got != "booting goal-box web old-paused" || m.filter.Value() != "" {
		t.Errorf("rows after clearing the filter = %q, filter %q", got, m.filter.Value())
	}

	// q quits.
	_, cmd = press(t, m, "q")
	if cmd == nil {
		t.Fatal("q returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q didn't quit")
	}
}

func TestTUISSHError(t *testing.T) {
	m := newTUI(nil)
	next, _ := m.Update(tuiSSHDoneMsg{name: "web", err: errors.New("exit status 1"),
		stderr: "Error: opening a terminal on web: vm not running (HTTP 409)\n  run boxctl resume web\n"})
	m = next.(tuiModel)
	if !m.statusErr || m.status != "ssh web: opening a terminal on web: vm not running (HTTP 409)" {
		t.Errorf("status = %q", m.status)
	}
	next, _ = m.Update(tuiSSHDoneMsg{name: "web"})
	m = next.(tuiModel)
	if m.statusErr || m.status != "left web" {
		t.Errorf("status = %q", m.status)
	}
}

func TestTUICreateForm(t *testing.T) {
	c, calls := fakeBoxServer(t, nil)
	m := newTUI(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = next.(tuiModel)
	m, _ = press(t, m, "c")
	if m.mode != modeCreate || m.create == nil {
		t.Fatalf("c: mode=%v", m.mode)
	}
	// Enter before the catalog is in is refused.
	m, _ = press(t, m, "enter")
	if m.mode != modeCreate || !strings.Contains(m.status, "still listing") {
		t.Errorf("early enter: mode=%v status=%q", m.mode, m.status)
	}
	next, _ = m.Update(tuiCatalogMsg{
		images: []client.Image{{Name: "claude-agent"}, {Name: defaultImage, Description: "Debian, slim"}},
		sizes:  []client.Size{{Name: "small", VCPU: 2, MemMiB: 2048, Default: true}, {Name: "large", VCPU: 8, MemMiB: 16384}},
	})
	m = next.(tuiModel)
	f := m.create
	if f.imageName() != defaultImage || f.sizeName() != "small" {
		t.Errorf("defaults: image=%s size=%s", f.imageName(), f.sizeName())
	}
	// Type a name, then tab to size and cycle it; left from the first
	// entry wraps around.
	for _, r := range "my-box" {
		m, _ = press(t, m, string(r))
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(tuiModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(tuiModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = next.(tuiModel)
	if f.field != createFieldSize || f.sizeName() != "large" {
		t.Errorf("after tab tab left: field=%d size=%s", f.field, f.sizeName())
	}
	view := m.View()
	for _, want := range []string{"my-box", "‹ large ›", "8 vCPU, 16GiB", defaultImage} {
		if !strings.Contains(view, want) {
			t.Errorf("form lacks %q:\n%s", want, view)
		}
	}
	m, cmd := press(t, m, "enter")
	if m.mode != modeList || !m.creating || cmd == nil {
		t.Fatalf("enter: mode=%v creating=%v cmd=%v", m.mode, m.creating, cmd)
	}
	// Run the create for real against the fake server (it answers {}).
	var got *tuiCreatedMsg
	for _, msg := range runCmd(cmd) {
		if r, ok := msg.(tuiCreatedMsg); ok {
			got = &r
		}
	}
	if got == nil || got.err != nil {
		t.Fatalf("create result: %#v", got)
	}
	if len(*calls) != 1 || (*calls)[0] != "POST /api/vms" {
		t.Errorf("server saw %v", *calls)
	}
	// esc leaves the form without creating.
	m, _ = press(t, m, "c")
	if m.mode != modeList || !strings.Contains(m.status, "already being created") {
		t.Errorf("c while creating: mode=%v status=%q", m.mode, m.status)
	}
}
