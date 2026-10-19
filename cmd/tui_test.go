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
		{ID: "2", Name: "web", State: "running", Image: "debian-slim", Size: "small", CreatedAt: now.Add(-2 * time.Hour), Labels: map[string]string{"team": "web"},
			Ingress: []client.Ingress{{URL: "https://web-abc.boxctl.app", Port: 3000, Status: "active"}}},
		{ID: "3", Name: "goal-box", State: "running", Image: "claude-agent", CreatedAt: now.Add(-1 * time.Hour),
			Labels:    map[string]string{"boxctl.run": "goal", "boxctl.agent": "claude", "boxctl.repo": "cdalar/boxctl"},
			RunStatus: json.RawMessage(`{"status":"running"}`)},
		{ID: "4", Name: "booting", State: "provisioning", CreatedAt: now},
	}
}

func keys(rows []tableRow) string {
	var out []string
	for _, r := range rows {
		out = append(out, r.key)
	}
	return strings.Join(out, " ")
}

func TestTUIRowsSortAndFilter(t *testing.T) {
	m := newTUI(nil)
	m.vms = tuiFixture()
	sortBoxes(m.vms)
	if got, want := keys(m.rows()), "booting goal-box web old-paused"; got != want {
		t.Fatalf("rows = %q, want %q", got, want)
	}
	m.stateFilter = showLiveBoxes
	if got, want := keys(m.rows()), "booting goal-box web"; got != want {
		t.Errorf("running = %q, want %q", got, want)
	}
	m.stateFilter = showPausedBoxes
	if got, want := keys(m.rows()), "old-paused"; got != want {
		t.Errorf("paused = %q, want %q", got, want)
	}
	m.stateFilter = showAllBoxes
	for filter, want := range map[string]string{
		"WEB":           "web",            // name, case-insensitively
		"cdalar/boxctl": "goal-box",       // the project column
		"claude goal":   "goal-box",       // the run column
		"debian":        "web old-paused", // image
		"nothing":       "",
	} {
		m.filter.SetValue(filter)
		if got := keys(m.rows()); got != want {
			t.Errorf("filter %q = %q, want %q", filter, got, want)
		}
	}
	m.filter.SetValue("")
	// shift-n sorts by name, again the other way; shift-a by age keeps
	// newest first.
	m.top().sortCol, m.top().sortDesc = 0, false
	if got, want := keys(m.rows()), "booting goal-box old-paused web"; got != want {
		t.Errorf("by name = %q, want %q", got, want)
	}
	m.top().sortDesc = true
	if got, want := keys(m.rows()), "web old-paused goal-box booting"; got != want {
		t.Errorf("by name desc = %q, want %q", got, want)
	}
	m.top().sortCol, m.top().sortDesc = 4, false
	if got, want := keys(m.rows()), "booting goal-box web old-paused"; got != want {
		t.Errorf("by age = %q, want %q", got, want)
	}
	// The urls view has one row per exposed port.
	m.pages = []page{{kind: pageTable, view: urlsView, sortCol: -1}}
	if got, want := keys(m.rows()), "web:3000"; got != want {
		t.Errorf("urls = %q, want %q", got, want)
	}
}

// fakeBoxServer answers GET /api/vms with vms, and records anything else.
func fakeBoxServer(t *testing.T, vms []client.VM) (*client.Client, *[]string) {
	t.Helper()
	var calls []string
	var restored map[string]string
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
		switch {
		case strings.HasSuffix(r.URL.Path, "/restore"):
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			_, _ = w.Write([]byte(`{"import_id":"imp-1"}`))
			restored = body
		case r.URL.Path == "/api/imports/imp-1":
			_, _ = w.Write([]byte(`{"status":"done","vm":{"name":"` + restored["name"] + `","state":"paused","size":"small"}}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
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
	case "ctrl+d":
		msg = tea.KeyMsg{Type: tea.KeyCtrlD}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	next, cmd := m.Update(msg)
	return next.(tuiModel), cmd
}

// typeIn sends s one key at a time.
func typeIn(t *testing.T, m tuiModel, s string) tuiModel {
	t.Helper()
	for _, r := range s {
		m, _ = press(t, m, string(r))
	}
	return m
}

// hasAction finds a tuiActionMsg among a command's messages.
func hasAction(cmd tea.Cmd) *tuiActionMsg {
	for _, msg := range runCmd(cmd) {
		if am, ok := msg.(tuiActionMsg); ok {
			return &am
		}
	}
	return nil
}

func TestTUIListAndKeys(t *testing.T) {
	tuiFlashFor = time.Millisecond
	c, calls := fakeBoxServer(t, tuiFixture())
	m := newTUI(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = next.(tuiModel)

	// The real listing, through the client.
	next, _ = m.Update(m.list("boxes")())
	m = next.(tuiModel)
	if m.loading["boxes"] {
		t.Fatal("still loading after the listing arrived")
	}
	view := m.View()
	for _, want := range []string{"web", "goal-box", "booting", "old-paused", "claude goal: running", "cdalar/boxctl",
		"Boxes(all)[4]", "<boxes>", "3 running, 1 paused", "<s>", "shell", "|_.__/"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}

	// 1 shows the running ones; the cursor stays on the first box.
	m, _ = press(t, m, "1")
	if strings.Contains(m.View(), "old-paused") || !strings.Contains(m.View(), "Boxes(running)[3]") {
		t.Errorf("1 didn't narrow to running boxes:\n%s", m.View())
	}
	m, _ = press(t, m, "0")
	if m.currentRow().key != "booting" {
		t.Errorf("cursor on %s, want booting", m.currentRow().key)
	}
	m, _ = press(t, m, "j")
	m, _ = press(t, m, "j")
	if m.currentRow().key != "web" {
		t.Errorf("cursor on %s after j j, want web", m.currentRow().key)
	}
	// Two keys that arrived in one message are two keypresses.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("kk")})
	m = next.(tuiModel)
	if m.currentRow().key != "booting" {
		t.Errorf("cursor on %s after kk, want booting", m.currentRow().key)
	}
	m, _ = press(t, m, "G")
	if m.currentRow().key != "old-paused" {
		t.Errorf("cursor on %s after G, want old-paused", m.currentRow().key)
	}

	// Pausing a paused box is refused here, not sent.
	m, cmd := press(t, m, "p")
	if hasAction(cmd) != nil || !m.flashErr || !strings.Contains(m.flash, "not running") {
		t.Errorf("p on a paused box: flash=%q", m.flash)
	}
	// So is ssh.
	m, cmd = press(t, m, "s")
	if cmd == nil || !strings.Contains(m.flash, "resume it first") {
		t.Errorf("s on a paused box: cmd=%v flash=%q", cmd, m.flash)
	}
	// Resuming is sent, and the box is pending meanwhile.
	m, cmd = press(t, m, "r")
	if hasAction(cmd) == nil || m.pending["old-paused"] != "resume" {
		t.Fatalf("r on a paused box: pending=%v", m.pending)
	}
	if !strings.Contains(m.View(), "resuming") {
		t.Error("pending box's row doesn't say resuming")
	}
	if len(*calls) != 1 || (*calls)[0] != "POST /api/vms/old-paused/resume" {
		t.Errorf("server saw %v", *calls)
	}
	*calls = nil
	// Keys on a pending box are refused until it's done.
	m, _ = press(t, m, "ctrl+d")
	if m.dialog != nil || !strings.Contains(m.flash, "being resumed") {
		t.Errorf("ctrl+d on a pending box: dialog=%v flash=%q", m.dialog, m.flash)
	}
	next, _ = m.Update(tuiActionMsg{name: "old-paused", verb: "resume"})
	m = next.(tuiModel)
	if _, ok := m.pending["old-paused"]; ok || m.flash != "resumed old-paused" {
		t.Errorf("after the action: pending=%v flash=%q", m.pending, m.flash)
	}

	// Delete asks first; n cancels.
	m, _ = press(t, m, "g")
	m, _ = press(t, m, "j") // goal-box, an unfinished goal run
	m, _ = press(t, m, "ctrl+d")
	if m.dialog == nil || !strings.Contains(m.View(), "Delete goal-box?") || !strings.Contains(m.View(), "is not done") {
		t.Errorf("ctrl+d: view:\n%s", m.View())
	}
	m, cmd = press(t, m, "n")
	if m.dialog != nil || hasAction(cmd) != nil {
		t.Errorf("n: dialog=%v", m.dialog)
	}
	// y sends it.
	m, _ = press(t, m, "ctrl+d")
	m, cmd = press(t, m, "y")
	if m.pending["goal-box"] != "destroy" {
		t.Fatalf("y: pending=%v", m.pending)
	}
	if am := hasAction(cmd); am == nil || am.err != nil {
		t.Fatalf("destroy's result: %#v", am)
	}
	if len(*calls) != 1 || (*calls)[0] != "DELETE /api/vms/goal-box" {
		t.Errorf("server saw %v", *calls)
	}
	*calls = nil

	// The filter narrows the rows as it's typed; esc clears it.
	m, _ = press(t, m, "/")
	m = typeIn(t, m, "web")
	if got := keys(m.rows()); got != "web" {
		t.Errorf("filtered rows = %q", got)
	}
	m, _ = press(t, m, "enter")
	if m.filtering || !strings.Contains(m.View(), "</web>") {
		t.Errorf("enter keeps the filter: view:\n%s", m.View())
	}
	m, cmd = press(t, m, "esc")
	if cmd != nil || keys(m.rows()) != "booting goal-box web old-paused" {
		t.Errorf("esc with a filter: rows=%q", keys(m.rows()))
	}

	// Describe and json drill in; esc comes back.
	m, _ = press(t, m, "g")
	m, _ = press(t, m, "j")
	m, _ = press(t, m, "j") // web
	m, _ = press(t, m, "enter")
	if len(m.pages) != 2 || m.top().kind != pageText || !strings.Contains(m.View(), "https://web-abc.boxctl.app") || !strings.Contains(m.View(), "<web>") {
		t.Errorf("enter didn't describe web:\n%s", m.View())
	}
	m, _ = press(t, m, "y")
	if len(m.pages) != 3 || !strings.Contains(m.View(), `"name": "web"`) || !strings.Contains(m.View(), "<web.json>") {
		t.Errorf("y didn't show web's json:\n%s", m.View())
	}
	// The page's box takes the box keys too.
	m, cmd = press(t, m, "p")
	if hasAction(cmd) == nil || m.pending["web"] != "pause" {
		t.Errorf("p on web's json page: pending=%v", m.pending)
	}
	next, _ = m.Update(tuiActionMsg{name: "web", verb: "pause"})
	m = next.(tuiModel)
	*calls = nil
	m, _ = press(t, m, "esc")
	m, _ = press(t, m, "esc")
	if len(m.pages) != 1 || m.top().view != boxesView {
		t.Errorf("esc esc didn't come back: %d pages", len(m.pages))
	}

	// Expose asks for a port.
	m, _ = press(t, m, "e")
	if m.dialog == nil || m.dialog.input == nil {
		t.Fatal("e opened no prompt")
	}
	m = typeIn(t, m, "8080")
	m, cmd = press(t, m, "enter")
	if am := hasAction(cmd); am == nil || am.verb != "expose" {
		t.Fatalf("expose: %#v", am)
	}
	if len(*calls) != 1 || (*calls)[0] != "POST /api/vms/web/ingress" {
		t.Errorf("server saw %v", *calls)
	}
	*calls = nil
	next, _ = m.Update(tuiActionMsg{name: "web", verb: "expose"})
	m = next.(tuiModel)

	// :urls switches view; ctrl+d there unexposes.
	m, _ = press(t, m, ":")
	m = typeIn(t, m, "urls")
	m, _ = press(t, m, "enter")
	if m.top().view != urlsView || !strings.Contains(m.View(), "Public URLs[1]") {
		t.Errorf(":urls: view:\n%s", m.View())
	}
	m, _ = press(t, m, "ctrl+d")
	m, cmd = press(t, m, "y")
	if am := hasAction(cmd); am == nil || am.verb != "unexpose" {
		t.Fatalf("unexpose: %#v", am)
	}
	if len(*calls) != 1 || (*calls)[0] != "DELETE /api/vms/web/ingress/3000" {
		t.Errorf("server saw %v", *calls)
	}

	// An unknown command is refused; :q quits.
	m, _ = press(t, m, ":")
	m = typeIn(t, m, "pods")
	m, _ = press(t, m, "enter")
	if !m.flashErr || !strings.Contains(m.flash, "unknown command") {
		t.Errorf(":pods: flash=%q", m.flash)
	}
	m, _ = press(t, m, ":")
	m = typeIn(t, m, "q")
	_, cmd = press(t, m, "enter")
	if cmd == nil {
		t.Fatal(":q returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error(":q didn't quit")
	}
}

func TestTUIRestore(t *testing.T) {
	tuiFlashFor = time.Millisecond
	c, calls := fakeBoxServer(t, tuiFixture())
	m := newTUI(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = next.(tuiModel)
	next, _ = m.Update(m.list("boxes")())
	m = next.(tuiModel)
	next, _ = m.Update(tuiListMsg{resource: "backups", backups: []client.Backup{
		{ID: "bk-1", VMName: "old-paused", SizeBytes: 1 << 20, CreatedAt: time.Now().Add(-time.Hour), UploadStatus: "uploaded"}}})
	m = next.(tuiModel)
	m, _ = press(t, m, ":")
	m = typeIn(t, m, "backups")
	m, _ = press(t, m, "enter")
	if m.top().view != backupsView || !strings.Contains(m.View(), "<r>") {
		t.Fatalf(":backups: view:\n%s", m.View())
	}
	// r asks for a name, the backed-up box's by default; that one still
	// exists, so it's refused.
	m, _ = press(t, m, "r")
	if m.dialog == nil || m.dialog.input == nil || m.dialog.input.Value() != "old-paused" {
		t.Fatalf("r: dialog=%#v", m.dialog)
	}
	m, cmd := press(t, m, "enter")
	if hasAction(cmd) != nil || !strings.Contains(m.flash, "already exists") {
		t.Errorf("restore over an existing box: flash=%q", m.flash)
	}
	// A new name and a size go through, and the box is pending meanwhile.
	m, _ = press(t, m, "r")
	m.dialog.input.SetValue("")
	m = typeIn(t, m, "old-again large")
	m, cmd = press(t, m, "enter")
	if m.pending["old-again"] != "restore" {
		t.Fatalf("pending=%v", m.pending)
	}
	am := hasAction(cmd)
	if am == nil || am.err != nil || !strings.Contains(am.result, "restored old-again (paused, small)") {
		t.Fatalf("restore's result: %#v", am)
	}
	if len(*calls) < 2 || (*calls)[0] != "POST /api/backups/bk-1/restore" || (*calls)[1] != "GET /api/imports/imp-1" {
		t.Errorf("server saw %v", *calls)
	}
}

func TestTUISSHError(t *testing.T) {
	m := newTUI(nil)
	next, _ := m.Update(tuiSSHDoneMsg{name: "web", err: errors.New("exit status 1"),
		stderr: "Error: opening a terminal on web: vm not running (HTTP 409)\n  run boxctl resume web\n"})
	m = next.(tuiModel)
	if !m.flashErr || m.flash != "ssh web: opening a terminal on web: vm not running (HTTP 409)" {
		t.Errorf("flash = %q", m.flash)
	}
}

func TestTUIDescribe(t *testing.T) {
	vms := tuiFixture()
	lines := strings.Join(describeBox(vms[2]), "\n")
	for _, want := range []string{"Name:           goal-box", "State:          running", "Kind:           claude goal: running", "boxctl.repo=cdalar/boxctl"} {
		if !strings.Contains(lines, want) {
			t.Errorf("describe lacks %q:\n%s", want, lines)
		}
	}
}

func TestTUICreateForm(t *testing.T) {
	tuiFlashFor = time.Millisecond
	c, calls := fakeBoxServer(t, nil)
	m := newTUI(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = next.(tuiModel)
	m, _ = press(t, m, "c")
	if m.create == nil {
		t.Fatal("c opened no form")
	}
	// Enter before the catalog is in is refused.
	m, _ = press(t, m, "enter")
	if m.create == nil || !strings.Contains(m.flash, "still listing") {
		t.Errorf("early enter: flash=%q", m.flash)
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
	m = typeIn(t, m, "my-box")
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
	for _, want := range []string{"my-box", "‹ large ›", "8 vCPU, 16GiB", defaultImage, "Create a box", "<create>"} {
		if !strings.Contains(view, want) {
			t.Errorf("form lacks %q:\n%s", want, view)
		}
	}
	m, cmd := press(t, m, "enter")
	if m.create != nil || !m.creating || cmd == nil {
		t.Fatalf("enter: creating=%v cmd=%v", m.creating, cmd)
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
	m, _ = press(t, m, "c")
	if m.create != nil || !strings.Contains(m.flash, "already being created") {
		t.Errorf("c while creating: flash=%q", m.flash)
	}

	// From the sizes view, c preselects the row's size.
	next, _ = m.Update(tuiCreatedMsg{vm: &client.VM{Name: "my-box", State: "running"}})
	m = next.(tuiModel)
	m.sizes = []client.Size{{Name: "small", Default: true}, {Name: "large"}}
	m.loaded["sizes"] = true
	m.pages = []page{{kind: pageTable, view: sizesView, sortCol: -1}}
	m, _ = press(t, m, "G")
	m, _ = press(t, m, "c")
	next, _ = m.Update(tuiCatalogMsg{images: []client.Image{{Name: defaultImage}}, sizes: m.sizes})
	m = next.(tuiModel)
	if m.create == nil || m.create.sizeName() != "large" {
		t.Errorf("c from sizes: form=%v", m.create)
	}
}
