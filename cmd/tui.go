package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cdalar/boxctl/internal/client"
	bspinner "github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// `boxctl tui`: your boxes the way k9s shows a cluster (Bubble Tea). A
// header with an info block, the current view's hotkeys and a logo; a
// bordered table per resource view (tuiviews.go), switched with a `:`
// command and filtered with `/`; describe/json/logs pages you drill
// into with esc to come back; breadcrumbs and flash messages at the
// bottom. It is a front end to the same client calls the subcommands
// make -- List, Pause, Resume, Keep, Destroy, Create, Expose, Unexpose,
// Exec -- and shells out to `boxctl ssh` for a terminal and to `boxctl
// claude --repo` from `:repos` (tuirepos.go), so nothing here changes
// how those commands behave.

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Browse and manage your boxes in a k9s-style terminal UI",
	Long: `Opens a live, full-screen view of your boxes, laid out like k9s.

  :boxes :urls :images :sizes :backups :repos   switch view (also :q, :help, :create)
  /text        filter the rows; esc clears
  j/k, arrows  move; g/G top/bottom; shift+letter sorts by that column
  enter, d     describe the box; y its JSON; l a --goal run's log
  s            open a terminal on the box (boxctl ssh)
  enter        in :repos (your GitHub repos, newest push first, from gh),
               start Claude on a fresh box with the repo cloned (boxctl claude --repo)
  c            create a box (name, image and size are asked)
  p / r / K    pause / resume / keep (hold off the idle reaper's deletion)
  e            expose a port at a public URL; ctrl-d in :urls takes it down
  r            in :backups, restore the backup as a new box (asks for a name)
  ctrl-d       delete the box (asks first)
  0 / 1 / 2    all boxes / running only / paused only
  esc          back; ctrl-c or :q quits`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !isTTY() || !term.IsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("boxctl tui needs a terminal; use `boxctl ls` on a pipe")
		}
		// Restore polls with a spinner on stderr; the screen is ours.
		client.QuietProgress()
		p := tea.NewProgram(newTUI(newClient()), tea.WithAltScreen())
		final, err := p.Run()
		if err != nil {
			return err
		}
		if m, ok := final.(tuiModel); ok && m.fatal != nil {
			return m.fatal
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(tuiCmd)
}

// tuiRefreshEvery paces the list's own refreshes while nothing else is
// going on; an action's completion refreshes at once.
const tuiRefreshEvery = 5 * time.Second

// tuiFlashFor is how long a flash message stays on the crumbs line (a
// variable so tests, which run every command, needn't wait for it).
var tuiFlashFor = 5 * time.Second

// Messages from the commands the model runs.
type (
	tuiListMsg struct {
		resource string // boxes, images, sizes, backups, repos
		vms      []client.VM
		images   []client.Image
		sizes    []client.Size
		backups  []client.Backup
		repos    []ghRepo
		err      error
	}
	tuiActionMsg struct {
		name, verb string
		err        error
		result     string // what to flash on success, "" for the default
	}
	tuiSSHDoneMsg struct {
		name   string
		err    error
		stderr string
	}
	tuiClaudeDoneMsg struct { // `boxctl claude --repo` from :repos ended
		repo   string
		err    error
		stderr string
	}
	tuiCreatedMsg struct {
		vm  *client.VM
		err error
	}
	tuiTextMsg struct { // a text page's content arrived (logs)
		title string
		box   string
		lines []string
		err   error
	}
	tuiFlashGoneMsg int
	tuiTickMsg      time.Time
)

// pageKind is what a page of the navigation stack shows.
type pageKind int

const (
	pageTable pageKind = iota
	pageText
)

// page is one level of the navigation stack: a table view with its own
// cursor and sort, or a text page (describe, json, logs, help).
type page struct {
	kind     pageKind
	view     *tableView // pageTable
	cursor   int
	selected string // the row key under the cursor, to find it again after a refresh
	sortCol  int    // -1: the view's own order
	sortDesc bool

	title  string   // pageText: the crumb
	box    string   // pageText: the box it describes, "" when not about one
	lines  []string // pageText
	scroll int
	// refresh regenerates lines from the model's data after a refresh
	// (describe/json pages); nil for fixed text.
	refresh func(m *tuiModel) []string
}

// tuiDialog is a modal: a y/N question, or a one-line prompt.
type tuiDialog struct {
	title  string
	body   []string
	input  *textinput.Model // a prompt; nil for a question
	yes    func(m tuiModel) (tea.Model, tea.Cmd)
	submit func(m tuiModel, value string) (tea.Model, tea.Cmd)
}

type tuiModel struct {
	c *client.Client

	vms     []client.VM // every box, as ls sorts them
	images  []client.Image
	sizes   []client.Size
	backups []client.Backup
	repos   []ghRepo        // GitHub's, newest push first (tuirepos.go)
	reposAt time.Time       // when repos came from GitHub, or the cache did
	loaded  map[string]bool // resources listed at least once
	loading map[string]bool // listings in flight

	pages       []page // the navigation stack; the last is shown
	stateFilter stateFilter
	filter      textinput.Model
	filtering   bool // typing in the filter prompt
	command     textinput.Model
	commanding  bool // typing in the : prompt
	dialog      *tuiDialog
	create      *tuiCreateForm
	creating    bool
	pending     map[string]string // box -> verb in flight
	spin        bspinner.Model
	spinning    bool

	flash    string
	flashErr bool
	flashID  int

	width, height int
	refreshedAt   time.Time
	fatal         error
}

func newTUI(c *client.Client) tuiModel {
	f := textinput.New()
	f.Prompt = ""
	f.Placeholder = "filter"
	f.CharLimit = 80
	cmd := textinput.New()
	cmd.Prompt = ""
	cmd.Placeholder = "boxes, urls, images, sizes, backups, repos, create, help, q"
	cmd.CharLimit = 80
	sp := bspinner.New(bspinner.WithSpinner(bspinner.Spinner{Frames: spinnerFrames, FPS: 80 * time.Millisecond}))
	sp.Style = k9s.logo
	return tuiModel{
		c:           c,
		loaded:      map[string]bool{},
		loading:     map[string]bool{},
		pages:       []page{{kind: pageTable, view: boxesView, sortCol: -1}},
		stateFilter: showAllBoxes,
		filter:      f,
		command:     cmd,
		pending:     map[string]string{},
		spin:        sp,
	}
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(m.list("boxes"), m.tick(), m.spin.Tick)
}

// top is the page shown.
func (m *tuiModel) top() *page { return &m.pages[len(m.pages)-1] }

// push shows p on top of the current page.
func (m *tuiModel) push(p page) { m.pages = append(m.pages, p) }

// pop goes back one page; the root stays.
func (m *tuiModel) pop() {
	if len(m.pages) > 1 {
		m.pages = m.pages[:len(m.pages)-1]
	}
}

// list fetches one resource.
func (m *tuiModel) list(resource string) tea.Cmd {
	if resource == "repos" {
		return m.listRepos(false)
	}
	if m.loading[resource] {
		return nil
	}
	m.loading[resource] = true
	c := m.c
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		msg := tuiListMsg{resource: resource}
		switch resource {
		case "boxes":
			msg.vms, msg.err = c.List(ctx)
		case "images":
			msg.images, msg.err = c.ListImages(ctx)
		case "sizes":
			msg.sizes, msg.err = c.ListSizes(ctx)
		case "backups":
			msg.backups, msg.err = c.ListBackups(ctx)
			sortBackups(msg.backups)
		}
		return msg
	}
}

// refresh lists the boxes (the header and most pages need them) and the
// top page's resource when it's another -- unless that view is listed
// on demand (:repos asks GitHub), which R does on its own.
func (m *tuiModel) refresh() tea.Cmd {
	cmds := []tea.Cmd{m.list("boxes")}
	if t := m.top(); t.kind == pageTable && t.view.resource != "boxes" && !t.view.onDemand {
		cmds = append(cmds, m.list(t.view.resource))
	}
	cmds = append(cmds, m.spinIfBusy())
	return tea.Batch(cmds...)
}

func (m tuiModel) tick() tea.Cmd {
	return tea.Tick(tuiRefreshEvery, func(t time.Time) tea.Msg { return tuiTickMsg(t) })
}

// busy reports whether something is in flight, i.e. the spinner should
// turn.
func (m tuiModel) busy() bool {
	return len(m.loading) > 0 || m.creating || len(m.pending) > 0
}

// spinIfBusy starts the spinner when work begins and it isn't turning.
func (m *tuiModel) spinIfBusy() tea.Cmd {
	if m.busy() && !m.spinning {
		m.spinning = true
		return m.spin.Tick
	}
	return nil
}

// say flashes a message on the crumbs line for a while.
func (m *tuiModel) say(format string, a ...any) tea.Cmd {
	return m.setFlash(fmt.Sprintf(format, a...), false)
}

// fail flashes an error.
func (m *tuiModel) fail(format string, a ...any) tea.Cmd {
	return m.setFlash(fmt.Sprintf(format, a...), true)
}

func (m *tuiModel) setFlash(s string, isErr bool) tea.Cmd {
	m.flash, m.flashErr = s, isErr
	m.flashID++
	id := m.flashID
	return tea.Tick(tuiFlashFor, func(time.Time) tea.Msg { return tuiFlashGoneMsg(id) })
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tuiTickMsg:
		return m, tea.Batch(m.refresh(), m.tick())

	case bspinner.TickMsg:
		if !m.busy() {
			m.spinning = false
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case tuiFlashGoneMsg:
		if int(msg) == m.flashID {
			m.flash = ""
		}
		return m, nil

	case tuiListMsg:
		delete(m.loading, msg.resource)
		if msg.err != nil {
			return m, m.fail("listing %s: %v", msg.resource, msg.err)
		}
		m.loaded[msg.resource] = true
		switch msg.resource {
		case "boxes":
			m.vms = msg.vms
			sortBoxes(m.vms)
			m.refreshedAt = time.Now()
		case "images":
			m.images = msg.images
		case "sizes":
			m.sizes = msg.sizes
		case "backups":
			m.backups = msg.backups
		case "repos":
			m.repos = msg.repos
			m.reposAt = time.Now()
		}
		// A describe/json page follows its box.
		for i := range m.pages {
			if p := &m.pages[i]; p.kind == pageText && p.refresh != nil {
				if lines := p.refresh(&m); lines != nil {
					p.lines = lines
				}
			}
		}
		return m, nil

	case tuiActionMsg:
		delete(m.pending, msg.name)
		if msg.err != nil {
			return m, tea.Batch(m.fail("%s %s: %v", presentTense(msg.verb), msg.name, msg.err), m.refresh())
		}
		result := msg.result
		if result == "" {
			result = fmt.Sprintf("%s %s", pastTense(msg.verb), msg.name)
		}
		return m, tea.Batch(m.say("%s", result), m.refresh())

	case tuiCreatedMsg:
		m.creating = false
		if msg.err != nil {
			return m, tea.Batch(m.fail("creating a box: %v", msg.err), m.refresh())
		}
		m.selectRow(msg.vm.Name)
		return m, tea.Batch(m.say("created %s (%s, %s)", msg.vm.Name, msg.vm.State, sizeLabel(*msg.vm)), m.refresh())

	case tuiSSHDoneMsg:
		if msg.err != nil {
			// boxctl ssh's own error, as it printed it (PrintError's
			// first line), not exec's "exit status 1".
			reason := strings.TrimSpace(msg.stderr)
			if i := strings.IndexByte(reason, '\n'); i >= 0 {
				reason = reason[:i]
			}
			reason = strings.TrimPrefix(reason, "Error: ")
			if reason == "" {
				reason = msg.err.Error()
			}
			return m, tea.Batch(m.fail("ssh %s: %s", msg.name, reason), m.refresh())
		}
		return m, m.refresh()

	case tuiClaudeDoneMsg:
		if msg.err != nil {
			return m, tea.Batch(m.fail("claude on %s: %s", msg.repo, claudeFailure(msg.stderr, msg.err)), m.refresh())
		}
		return m, tea.Batch(m.say("claude on %s ended", msg.repo), m.refresh())

	case tuiTextMsg:
		delete(m.pending, "logs:"+msg.box)
		if msg.err != nil {
			return m, m.fail("%s: %v", msg.title, msg.err)
		}
		m.push(page{kind: pageText, title: msg.title, box: msg.box, lines: msg.lines})
		return m, nil

	case tuiCatalogMsg:
		if m.create != nil {
			m.create.setCatalog(msg)
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	// Anything else (cursor blinks and the like) belongs to whichever
	// input has focus.
	return m, m.updateInputs(msg)
}

// updateInputs passes msg to the focused text input, if any.
func (m *tuiModel) updateInputs(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	switch {
	case m.filtering:
		m.filter, cmd = m.filter.Update(msg)
	case m.commanding:
		m.command, cmd = m.command.Update(msg)
	case m.dialog != nil && m.dialog.input != nil:
		*m.dialog.input, cmd = m.dialog.input.Update(msg)
	case m.create != nil:
		cmd = m.create.update(msg)
	}
	return cmd
}

// selectRow puts the top table's cursor on key at the next render.
func (m *tuiModel) selectRow(key string) {
	if t := m.top(); t.kind == pageTable {
		t.selected = key
	}
}

// rows is the top table's rows as shown: the view's, filtered and
// sorted. The cursor is clamped and put back on the selected row here,
// since that is the one place the rows are known.
func (m *tuiModel) rows() []tableRow {
	t := m.top()
	if t.kind != pageTable {
		return nil
	}
	rows := filterRows(t.view.rows(m), m.filter.Value())
	sortRows(rows, t.sortCol, t.sortDesc)
	t.cursor = 0
	for i, r := range rows {
		if r.key == t.selected {
			t.cursor = i
			break
		}
	}
	if len(rows) > 0 {
		t.selected = rows[t.cursor].key
	}
	return rows
}

// currentRow is the row under the cursor, nil with none.
func (m *tuiModel) currentRow() *tableRow {
	rows := m.rows()
	t := m.top()
	if t.cursor < 0 || t.cursor >= len(rows) {
		return nil
	}
	return &rows[t.cursor]
}

// currentBox is the box the top page is about: the row's, in the boxes
// and urls views, or a text page's box.
func (m *tuiModel) currentBox() *client.VM {
	t := m.top()
	var name string
	switch {
	case t.kind == pageText:
		name = t.box
	case t.view == boxesView:
		if r := m.currentRow(); r != nil {
			name = r.key
		}
	case t.view == urlsView:
		if r := m.currentRow(); r != nil {
			name, _, _ = strings.Cut(r.key, ":")
		}
	}
	return m.box(name)
}

// box finds name among the boxes, nil when it isn't there.
func (m *tuiModel) box(name string) *client.VM {
	for i := range m.vms {
		if m.vms[i].Name == name {
			return &m.vms[i]
		}
	}
	return nil
}

// move shifts the cursor by n rows, within the list.
func (m *tuiModel) move(n int) {
	rows := m.rows()
	t := m.top()
	if len(rows) == 0 {
		return
	}
	t.cursor = min(max(t.cursor+n, 0), len(rows)-1)
	t.selected = rows[t.cursor].key
}

// moveTo puts the cursor on the first or the last row.
func (m *tuiModel) moveTo(last bool) {
	rows := m.rows()
	t := m.top()
	if len(rows) == 0 {
		return
	}
	t.cursor = 0
	if last {
		t.cursor = len(rows) - 1
	}
	t.selected = rows[t.cursor].key
}

// openView makes v the only page (k9s's `:resource`), listing what it
// needs.
func (m *tuiModel) openView(v *tableView) tea.Cmd {
	m.pages = []page{{kind: pageTable, view: v, sortCol: -1}}
	m.filter.SetValue("")
	return tea.Batch(m.list(v.resource), m.spinIfBusy())
}

// sortBackups is newest first, as `boxctl backups` lists them.
func sortBackups(backups []client.Backup) {
	for i := 1; i < len(backups); i++ {
		for j := i; j > 0 && backups[j].CreatedAt.After(backups[j-1].CreatedAt); j-- {
			backups[j], backups[j-1] = backups[j-1], backups[j]
		}
	}
}

// presentTense/pastTense word the flash: "pausing my-box", "paused
// my-box".
func presentTense(verb string) string {
	switch verb {
	case "pause":
		return "pausing"
	case "resume":
		return "resuming"
	case "keep":
		return "keeping"
	case "destroy":
		return "destroying"
	case "create":
		return "creating"
	case "expose":
		return "exposing"
	case "unexpose":
		return "unexposing"
	case "restore":
		return "restoring"
	}
	return verb + "ing"
}

func pastTense(verb string) string {
	switch verb {
	case "pause", "resume", "create", "expose", "unexpose", "restore":
		return verb + "d"
	case "keep":
		return "kept"
	case "destroy":
		return "destroyed"
	}
	return verb + "ed"
}
