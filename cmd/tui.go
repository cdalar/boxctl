package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/cdalar/boxctl/internal/client"
	bspinner "github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// `boxctl tui`: the box list as a full-screen, live view (Bubble Tea),
// with the everyday commands on single keys. It is a front end to the
// same client calls the subcommands make -- List, Pause, Resume, Keep,
// Destroy, Create -- and shells out to `boxctl ssh` for a terminal, so
// nothing here changes how those commands behave. The listing's columns
// are ls's (runText, projectText, prText, sizeLabel, age) and its order
// is ls's too (stateRank, newest first).

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Browse and manage your boxes in a full-screen terminal UI",
	Long: `Opens a live, full-screen view of your boxes. Move with the arrow keys
(or j/k), press enter to open a terminal on the selected box, and use
single keys for the usual commands:

  enter, s   open a terminal (boxctl ssh)
  c          create a box (name, image and size are asked)
  p / r      pause / resume
  d          destroy (asks first)
  K          keep: hold the box off the idle reaper's deletion for a day
  a          also show paused and stopped boxes
  /          filter by name, image, project or label
  i          show or hide the selected box's details
  R          refresh now (the list refreshes itself every few seconds)
  ?          help
  q          quit`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !isTTY() || !term.IsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("boxctl tui needs a terminal; use `boxctl ls` on a pipe")
		}
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

// tuiMode is which keys the next keypress goes to.
type tuiMode int

const (
	modeList    tuiMode = iota // the table
	modeFilter                 // typing a filter in the footer
	modeConfirm                // a y/N question in the footer
	modeCreate                 // the create form
	modeHelp                   // the key reference
)

// Messages from the commands the model runs.
type (
	tuiBoxesMsg struct {
		vms []client.VM
		err error
	}
	tuiActionMsg struct {
		name, verb string
		err        error
	}
	tuiSSHDoneMsg struct {
		name   string
		err    error
		stderr string
	}
	tuiCreatedMsg struct {
		vm  *client.VM
		err error
	}
	tuiTickMsg time.Time
)

type tuiModel struct {
	c *client.Client

	vms  []client.VM // every box, sorted as ls sorts them
	rows []client.VM // the ones shown: after showAll and filter

	cursor   int
	selected string // the box under the cursor, to find it again after a refresh
	showAll  bool
	filter   textinput.Model
	mode     tuiMode
	confirm  tuiConfirm
	create   *tuiCreateForm
	detail   bool
	width    int
	height   int
	loading  bool              // a List in flight
	creating bool              // a Create in flight
	pending  map[string]string // box -> verb in flight ("pausing")
	spin     bspinner.Model
	spinning bool

	refreshedAt time.Time
	status      string // the last thing that happened, in the footer
	statusErr   bool
	fatal       error // makes RunE fail once the program ends
}

// tuiConfirm is a y/N question: what to do if the answer is yes.
type tuiConfirm struct {
	prompt string
	warn   string // an extra line above the prompt, for rm's unfinished-run check
	yes    func(m tuiModel) (tea.Model, tea.Cmd)
}

func newTUI(c *client.Client) tuiModel {
	f := textinput.New()
	f.Prompt = "/ "
	f.Placeholder = "name, image, project or label"
	f.CharLimit = 80
	sp := bspinner.New(bspinner.WithSpinner(bspinner.Spinner{Frames: spinnerFrames, FPS: 80 * time.Millisecond}))
	sp.Style = tuiStyles.accent
	return tuiModel{
		c:       c,
		filter:  f,
		detail:  true,
		pending: map[string]string{},
		spin:    sp,
		loading: true,
	}
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(m.fetch(), m.tick(), m.spin.Tick)
}

// fetch lists the boxes.
func (m tuiModel) fetch() tea.Cmd {
	c := m.c
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		vms, err := c.List(ctx)
		return tuiBoxesMsg{vms: vms, err: err}
	}
}

func (m tuiModel) tick() tea.Cmd {
	return tea.Tick(tuiRefreshEvery, func(t time.Time) tea.Msg { return tuiTickMsg(t) })
}

// busy reports whether something is in flight, i.e. the spinner should
// turn.
func (m tuiModel) busy() bool {
	return m.loading || m.creating || len(m.pending) > 0
}

// spinIfBusy starts the spinner when work begins and it isn't turning.
func (m *tuiModel) spinIfBusy() tea.Cmd {
	if m.busy() && !m.spinning {
		m.spinning = true
		return m.spin.Tick
	}
	return nil
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tuiTickMsg:
		var cmd tea.Cmd
		if !m.loading {
			m.loading = true
			cmd = m.fetch()
		}
		return m, tea.Batch(cmd, m.tick(), m.spinIfBusy())

	case bspinner.TickMsg:
		if !m.busy() {
			m.spinning = false
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case tuiBoxesMsg:
		m.loading = false
		if msg.err != nil {
			m.setStatus("listing boxes: "+msg.err.Error(), true)
			return m, nil
		}
		m.vms = msg.vms
		sortBoxes(m.vms)
		m.refreshedAt = time.Now()
		m.rebuildRows()
		return m, nil

	case tuiActionMsg:
		delete(m.pending, msg.name)
		if msg.err != nil {
			m.setStatus(fmt.Sprintf("%s %s: %v", msg.verb, msg.name, msg.err), true)
		} else {
			m.setStatus(fmt.Sprintf("%s %s", pastTense(msg.verb), msg.name), false)
		}
		return m, m.refreshNow()

	case tuiCreatedMsg:
		m.creating = false
		if msg.err != nil {
			m.setStatus("creating a box: "+msg.err.Error(), true)
		} else {
			m.setStatus(fmt.Sprintf("created %s (%s, %s)", msg.vm.Name, msg.vm.State, sizeLabel(*msg.vm)), false)
			m.selected = msg.vm.Name
		}
		return m, m.refreshNow()

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
			m.setStatus(fmt.Sprintf("ssh %s: %s", msg.name, reason), true)
		} else {
			m.setStatus(fmt.Sprintf("left %s", msg.name), false)
		}
		return m, m.refreshNow()

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
	switch m.mode {
	case modeFilter:
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		return cmd
	case modeCreate:
		if m.create != nil {
			return m.create.update(msg)
		}
	}
	return nil
}

// refreshNow lists again right away, unless a listing is already on its
// way.
func (m *tuiModel) refreshNow() tea.Cmd {
	if m.loading {
		return m.spinIfBusy()
	}
	m.loading = true
	return tea.Batch(m.fetch(), m.spinIfBusy())
}

func (m *tuiModel) setStatus(s string, isErr bool) {
	m.status = s
	m.statusErr = isErr
}

func (m tuiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	switch m.mode {
	case modeFilter:
		return m.handleFilterKey(msg)
	case modeConfirm:
		return m.handleConfirmKey(msg)
	case modeCreate:
		return m.handleCreateKey(msg)
	case modeHelp:
		m.mode = modeList
		return m, nil
	}

	// Keys typed quickly (or held) can arrive as one message with
	// several runes; each is a keypress of its own here.
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 && !msg.Paste {
		var cmds []tea.Cmd
		var next tea.Model = m
		for _, r := range msg.Runes {
			var cmd tea.Cmd
			next, cmd = next.(tuiModel).handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			cmds = append(cmds, cmd)
		}
		return next, tea.Batch(cmds...)
	}

	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "esc":
		// Clear a filter first; quit only without one.
		if m.filter.Value() != "" {
			m.filter.SetValue("")
			m.rebuildRows()
			return m, nil
		}
		return m, tea.Quit
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup":
		m.move(-m.tableRows())
	case "pgdown":
		m.move(m.tableRows())
	case "home", "g":
		m.cursor = 0
		m.remember()
	case "end", "G":
		m.cursor = len(m.rows) - 1
		m.remember()
	case "enter", "s":
		return m.ssh()
	case "c", "n":
		m.status = ""
		return m.openCreate()
	case "p":
		return m.act("pause", "running", func(ctx context.Context, name string) error {
			_, err := m.c.Pause(ctx, name)
			return err
		})
	case "r":
		return m.act("resume", "paused", func(ctx context.Context, name string) error {
			_, err := m.c.Resume(ctx, name)
			return err
		})
	case "K":
		return m.act("keep", "", func(ctx context.Context, name string) error {
			_, err := m.c.Keep(ctx, name)
			return err
		})
	case "d", "x", "delete", "backspace":
		return m.askDestroy()
	case "a":
		m.showAll = !m.showAll
		m.rebuildRows()
	case "/":
		m.status = ""
		m.mode = modeFilter
		return m, m.filter.Focus()
	case "i":
		m.detail = !m.detail
	case "R", "ctrl+r", "f5":
		return m, m.refreshNow()
	case "?":
		m.mode = modeHelp
	}
	return m, nil
}

func (m tuiModel) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.filter.SetValue("")
		m.filter.Blur()
		m.mode = modeList
		m.rebuildRows()
		return m, nil
	case tea.KeyEnter:
		m.filter.Blur()
		m.mode = modeList
		return m, nil
	}
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(msg)
	m.rebuildRows()
	return m, cmd
}

func (m tuiModel) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	yes := m.confirm.yes
	m.mode = modeList
	m.confirm = tuiConfirm{}
	if s := msg.String(); s == "y" || s == "Y" {
		return yes(m)
	}
	m.setStatus("cancelled", false)
	return m, nil
}

// move shifts the cursor by n rows, within the list.
func (m *tuiModel) move(n int) {
	if len(m.rows) == 0 {
		m.cursor = 0
		return
	}
	m.cursor = min(max(m.cursor+n, 0), len(m.rows)-1)
	m.remember()
}

// remember notes which box the cursor is on, so a refresh that reorders
// the list keeps the cursor on it.
func (m *tuiModel) remember() {
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		m.selected = m.rows[m.cursor].Name
	}
}

// current is the box under the cursor, nil with an empty list.
func (m tuiModel) current() *client.VM {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	return &m.rows[m.cursor]
}

// rebuildRows recomputes the visible rows from vms, showAll and the
// filter, and puts the cursor back on the selected box.
func (m *tuiModel) rebuildRows() {
	m.rows = visibleBoxes(m.vms, m.showAll, m.filter.Value())
	m.cursor = 0
	for i, vm := range m.rows {
		if vm.Name == m.selected {
			m.cursor = i
			break
		}
	}
	if len(m.rows) == 0 {
		m.cursor = 0
		return
	}
	m.selected = m.rows[m.cursor].Name
}

// visibleBoxes is the rows the table shows: live boxes only unless all
// (ls's rule), and with a filter only the boxes whose name, state,
// image, project, branch or a label contains it (case-insensitively).
// vms must already be sorted; the order is kept.
func visibleBoxes(vms []client.VM, all bool, filter string) []client.VM {
	filter = strings.ToLower(strings.TrimSpace(filter))
	var out []client.VM
	for _, vm := range vms {
		if !all && stateRank(vm.State) != 0 {
			continue
		}
		if filter != "" && !boxMatches(vm, filter) {
			continue
		}
		out = append(out, vm)
	}
	return out
}

// boxMatches reports whether filter (lowercase) is in one of vm's
// searchable fields.
func boxMatches(vm client.VM, filter string) bool {
	fields := []string{vm.Name, vm.State, vm.Image, vm.Size, vm.Provider, vm.IP, projectText(vm), branchText(vm), runText(vm)}
	for k, v := range vm.Labels {
		fields = append(fields, k+"="+v)
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), filter) {
			return true
		}
	}
	return false
}

// sortBoxes orders vms as ls does: live, paused, other; newest first
// within a group; id as the tiebreak.
func sortBoxes(vms []client.VM) {
	sort.SliceStable(vms, func(i, j int) bool {
		ri, rj := stateRank(vms[i].State), stateRank(vms[j].State)
		if ri != rj {
			return ri < rj
		}
		if !vms[i].CreatedAt.Equal(vms[j].CreatedAt) {
			return vms[i].CreatedAt.After(vms[j].CreatedAt)
		}
		return vms[i].ID < vms[j].ID
	})
}

// act runs one of the client's per-box calls on the selected box, in
// the background, with the box marked pending meanwhile. wantState, when
// not empty, is the state the box must be in (pause needs running), so
// a sure-to-fail request isn't sent.
func (m tuiModel) act(verb, wantState string, fn func(ctx context.Context, name string) error) (tea.Model, tea.Cmd) {
	vm := m.current()
	if vm == nil {
		return m, nil
	}
	if v, ok := m.pending[vm.Name]; ok {
		m.setStatus(fmt.Sprintf("%s is being %s", vm.Name, pastTense(v)), true)
		return m, nil
	}
	if wantState != "" && vm.State != wantState {
		m.setStatus(fmt.Sprintf("%s is %s, not %s; can't %s it", vm.Name, vm.State, wantState, verb), true)
		return m, nil
	}
	name := vm.Name
	m.pending[name] = verb
	m.setStatus(fmt.Sprintf("%s %s…", presentTense(verb), name), false)
	run := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		return tuiActionMsg{name: name, verb: verb, err: fn(ctx, name)}
	}
	return m, tea.Batch(run, m.spinIfBusy())
}

// askDestroy is rm's two confirmations in one: the usual question, with
// the unfinished-goal-run warning above it when it applies (from the
// list already loaded, as rm reads the one List call).
func (m tuiModel) askDestroy() (tea.Model, tea.Cmd) {
	vm := m.current()
	if vm == nil {
		return m, nil
	}
	if v, ok := m.pending[vm.Name]; ok {
		m.setStatus(fmt.Sprintf("%s is being %s", vm.Name, pastTense(v)), true)
		return m, nil
	}
	name := vm.Name
	m.confirm = tuiConfirm{
		prompt: fmt.Sprintf("Destroy %s? This cannot be undone. [y/N]", name),
		yes: func(m tuiModel) (tea.Model, tea.Cmd) {
			return m.act("destroy", "", m.c.Destroy)
		},
	}
	if len(filterUnfinished([]client.VM{*vm}, nil)) > 0 {
		m.confirm.warn = fmt.Sprintf("%s is not done (%s): its work may not be fetched yet.", name, runText(*vm))
	}
	m.mode = modeConfirm
	return m, nil
}

// ssh hands the terminal to `boxctl ssh <name>` -- a separate process,
// so its stdin reader (which runSSH can't cancel) dies with it instead
// of swallowing this program's next keypress. Its stderr is kept to show
// why it failed, since the alternate screen hides whatever it printed.
func (m tuiModel) ssh() (tea.Model, tea.Cmd) {
	vm := m.current()
	if vm == nil {
		return m, nil
	}
	if vm.State == "paused" {
		m.setStatus(fmt.Sprintf("%s is paused; press r to resume it first", vm.Name), true)
		return m, nil
	}
	exe, err := os.Executable()
	if err != nil {
		m.setStatus("finding boxctl: "+err.Error(), true)
		return m, nil
	}
	args := []string{"ssh", vm.Name}
	if apiURLFlag != "" {
		args = append(args, "--api-url", apiURLFlag)
	}
	cmd := exec.Command(exe, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	name := vm.Name
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return tuiSSHDoneMsg{name: name, err: err, stderr: stderr.String()}
	})
}

// presentTense/pastTense word the status line: "pausing my-box", "paused
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
	}
	return verb + "ing"
}

func pastTense(verb string) string {
	switch verb {
	case "pause", "resume", "create":
		return verb + "d"
	case "keep":
		return "kept"
	case "destroy":
		return "destroyed"
	}
	return verb + "ed"
}

// --- rendering ---

var tuiStyles = struct {
	title, accent, dim, header, selected, err, ok, warn, key lipgloss.Style
	state                                                    map[string]lipgloss.Style
}{
	title:    lipgloss.NewStyle().Bold(true),
	accent:   lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
	dim:      lipgloss.NewStyle().Faint(true),
	header:   lipgloss.NewStyle().Bold(true).Underline(true),
	selected: lipgloss.NewStyle().Reverse(true).Bold(true),
	err:      lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
	ok:       lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
	warn:     lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
	key:      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")),
	state: map[string]lipgloss.Style{
		"running":      lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		"paused":       lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		"provisioning": lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		"stopping":     lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
	},
}

// tuiColumns are the table's columns: ls's, in ls's order.
var tuiColumns = []struct {
	title string
	cell  func(vm client.VM) string
}{
	{"NAME", func(vm client.VM) string { return vm.Name }},
	{"STATE", func(vm client.VM) string { return vm.State }},
	{"IMAGE", func(vm client.VM) string {
		if vm.Image == "" {
			return "-"
		}
		return vm.Image
	}},
	{"SIZE", sizeLabel},
	{"AGE", func(vm client.VM) string { return age(vm.CreatedAt) }},
	{"RUN", runText},
	{"PROJECT", projectText},
	{"PR", prText},
}

const tuiStateColumn = 1

// Fixed line counts around the table: the title line and a blank one
// above, the table header, and the status and key lines below.
const (
	tuiChromeAbove = 3
	tuiChromeBelow = 2
)

// detailHeight is how many lines the detail pane takes (0 when hidden or
// there is no box to describe).
func (m tuiModel) detailHeight() int {
	if !m.detail || m.current() == nil {
		return 0
	}
	return len(m.detailLines(m.current())) + 1 // plus a blank line above
}

// tableRows is how many box rows fit on the screen.
func (m tuiModel) tableRows() int {
	if m.height == 0 {
		return 20
	}
	return max(m.height-tuiChromeAbove-tuiChromeBelow-m.detailHeight(), 1)
}

func (m tuiModel) View() string {
	if m.width == 0 {
		return "loading…"
	}
	var b strings.Builder
	b.WriteString(m.titleLine() + "\n\n")
	switch m.mode {
	case modeHelp:
		b.WriteString(m.helpView())
	case modeCreate:
		b.WriteString(m.create.view(m.width))
	default:
		b.WriteString(m.tableView())
		if m.detail {
			if vm := m.current(); vm != nil {
				b.WriteString("\n")
				for _, l := range m.detailLines(vm) {
					b.WriteString(fit(l, m.width) + "\n")
				}
			}
		}
	}
	// Push the footer to the bottom of the screen.
	lines := strings.Count(b.String(), "\n")
	for ; lines < m.height-tuiChromeBelow; lines++ {
		b.WriteString("\n")
	}
	b.WriteString(m.statusLine() + "\n")
	b.WriteString(m.keyLine())
	return b.String()
}

func (m tuiModel) titleLine() string {
	left := tuiStyles.title.Render("boxctl")
	if m.busy() {
		left += " " + m.spin.View()
	}
	var parts []string
	live := 0
	for _, vm := range m.vms {
		if stateRank(vm.State) == 0 {
			live++
		}
	}
	parts = append(parts, boxCount(len(m.vms)))
	if hidden := len(m.vms) - live; hidden > 0 && !m.showAll {
		parts = append(parts, fmt.Sprintf("%d not running (a shows them)", hidden))
	}
	if f := m.filter.Value(); f != "" && m.mode != modeFilter {
		parts = append(parts, fmt.Sprintf("filter %q", f))
	}
	if !m.refreshedAt.IsZero() {
		parts = append(parts, "refreshed "+shortAgo(time.Since(m.refreshedAt)))
	}
	right := tuiStyles.dim.Render(strings.Join(parts, " · "))
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return fit(left, m.width)
	}
	return left + strings.Repeat(" ", gap) + right
}

// shortAgo is "now", "12s ago", "3m ago".
func shortAgo(d time.Duration) string {
	switch {
	case d < 2*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	default:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
}

func (m tuiModel) tableView() string {
	if len(m.rows) == 0 {
		switch {
		case m.loading && m.refreshedAt.IsZero():
			return tuiStyles.dim.Render("Loading your boxes…") + "\n"
		case m.filter.Value() != "":
			return tuiStyles.dim.Render("No boxes match the filter.") + "\n"
		case len(m.vms) > 0:
			return tuiStyles.dim.Render("No running boxes. Press a to show the others.") + "\n"
		default:
			return tuiStyles.dim.Render("No boxes yet. Press c to create one.") + "\n"
		}
	}
	// Column widths from the plain cells, over every visible row, so the
	// table doesn't shift as it scrolls.
	cells := make([][]string, len(m.rows))
	widths := make([]int, len(tuiColumns))
	for i, col := range tuiColumns {
		widths[i] = lipgloss.Width(col.title)
	}
	for r, vm := range m.rows {
		cells[r] = make([]string, len(tuiColumns))
		for i, col := range tuiColumns {
			s := col.cell(vm)
			if v, ok := m.pending[vm.Name]; ok && i == tuiStateColumn {
				s = presentTense(v)
			}
			cells[r][i] = s
			widths[i] = max(widths[i], lipgloss.Width(s))
		}
	}
	var b strings.Builder
	var header []string
	for i, col := range tuiColumns {
		header = append(header, padRight(col.title, widths[i]))
	}
	b.WriteString(fit(tuiStyles.header.Render(strings.Join(header, "  ")), m.width) + "\n")

	// The window of rows that keeps the cursor on screen.
	rows := m.tableRows()
	top := 0
	if m.cursor >= rows {
		top = m.cursor - rows + 1
	}
	end := min(top+rows, len(m.rows))
	for r := top; r < end; r++ {
		vm := m.rows[r]
		var line string
		if r == m.cursor {
			var cs []string
			for i, s := range cells[r] {
				cs = append(cs, padRight(s, widths[i]))
			}
			line = tuiStyles.selected.Render(fit(strings.Join(cs, "  "), m.width))
		} else {
			var cs []string
			for i, s := range cells[r] {
				s = padRight(s, widths[i])
				if i == tuiStateColumn {
					if _, ok := m.pending[vm.Name]; ok {
						s = tuiStyles.accent.Render(s)
					} else if st, ok := tuiStyles.state[vm.State]; ok {
						s = st.Render(s)
					}
				}
				cs = append(cs, s)
			}
			line = fit(strings.Join(cs, "  "), m.width)
		}
		b.WriteString(line + "\n")
	}
	if top > 0 || end < len(m.rows) {
		b.WriteString(tuiStyles.dim.Render(fmt.Sprintf("%d-%d of %d", top+1, end, len(m.rows))) + "\n")
	}
	return b.String()
}

// detailLines describes vm under the table: what ls -o wide and the
// dashboard's box page show, only the parts that apply.
func (m tuiModel) detailLines(vm *client.VM) []string {
	var lines []string
	title := tuiStyles.title.Render(vm.Name)
	parts := []string{vm.State}
	if vm.IP != "" {
		parts = append(parts, vm.IP)
	}
	if vm.Provider != "" {
		parts = append(parts, vm.Provider)
	}
	if vm.State == "running" {
		if vm.Ready {
			parts = append(parts, "ready")
		} else {
			parts = append(parts, "not ready yet")
		}
	}
	if vm.VCPU > 0 {
		parts = append(parts, fmt.Sprintf("%s (%d vCPU, %s)", sizeLabel(*vm), vm.VCPU, memLabel(vm.MemMiB)))
	}
	if vm.Image != "" {
		parts = append(parts, vm.Image)
	}
	lines = append(lines, title+"  "+tuiStyles.dim.Render(strings.Join(parts, " · ")))

	var when []string
	if !vm.CreatedAt.IsZero() {
		when = append(when, "created "+vm.CreatedAt.Local().Format(time.DateTime)+" ("+age(vm.CreatedAt)+")")
	}
	if vm.LastActivityAt != nil {
		when = append(when, "last used "+age(*vm.LastActivityAt)+" ago")
	}
	if vm.KeepUntil != nil && vm.KeepUntil.After(time.Now()) {
		when = append(when, "kept until "+vm.KeepUntil.Local().Format(time.DateTime))
	} else if vm.DeleteAt != nil {
		when = append(when, tuiStyles.warn.Render("idle: deleted "+vm.DeleteAt.Local().Format(time.DateTime)+" unless resumed (K keeps it a day)"))
	}
	if len(when) > 0 {
		lines = append(lines, "  "+strings.Join(when, " · "))
	}

	if run := runText(*vm); run != "-" {
		rl := []string{"run: " + run}
		if b := branchText(*vm); b != "-" {
			rl = append(rl, "branch "+b)
		}
		if st, _ := parseGoalStatus(vm.RunStatus); st != nil {
			rl = append(rl, st.PRURLs...)
			if len(st.PRURLs) == 0 && st.PRURL != "" {
				rl = append(rl, st.PRURL)
			}
			if st.Error != "" {
				rl = append(rl, tuiStyles.err.Render(st.Error))
			}
		}
		lines = append(lines, "  "+strings.Join(rl, " · "))
	}

	if len(vm.Labels) > 0 {
		keys := make([]string, 0, len(vm.Labels))
		for k := range vm.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var kv []string
		for _, k := range keys {
			kv = append(kv, k+"="+vm.Labels[k])
		}
		lines = append(lines, "  "+tuiStyles.dim.Render("labels: "+strings.Join(kv, " ")))
	}

	for _, in := range vm.Ingress {
		s := fmt.Sprintf("  public: %s → port %d (%s)", in.URL, in.Port, in.Status)
		lines = append(lines, s)
	}
	return lines
}

func (m tuiModel) statusLine() string {
	switch m.mode {
	case modeFilter:
		return fit(m.filter.View(), m.width)
	case modeConfirm:
		s := tuiStyles.warn.Render(m.confirm.prompt)
		if m.confirm.warn != "" {
			s = tuiStyles.warn.Render("! "+m.confirm.warn) + "  " + s
		}
		return fit(s, m.width)
	}
	if m.status == "" {
		return ""
	}
	if m.statusErr {
		return fit(tuiStyles.err.Render("✘ "+m.status), m.width)
	}
	return fit(tuiStyles.ok.Render("✔ ")+m.status, m.width)
}

// keyLine is the footer's key reference, cut to what fits.
func (m tuiModel) keyLine() string {
	var keys [][2]string
	switch m.mode {
	case modeFilter:
		keys = [][2]string{{"enter", "keep filter"}, {"esc", "clear"}}
	case modeConfirm:
		keys = [][2]string{{"y", "yes"}, {"any other key", "no"}}
	case modeCreate:
		keys = [][2]string{{"tab", "next field"}, {"←/→", "change"}, {"enter", "create"}, {"esc", "cancel"}}
	case modeHelp:
		keys = [][2]string{{"any key", "back"}}
	default:
		keys = [][2]string{{"enter", "ssh"}, {"c", "create"}, {"p", "pause"}, {"r", "resume"}, {"d", "destroy"}, {"K", "keep"}, {"a", "all"}, {"/", "filter"}, {"i", "details"}, {"R", "refresh"}, {"?", "help"}, {"q", "quit"}}
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, tuiStyles.key.Render(k[0])+" "+tuiStyles.dim.Render(k[1]))
	}
	// Whole items only: drop from the end until the line fits.
	for len(parts) > 1 && lipgloss.Width(strings.Join(parts, "  ")) > m.width {
		parts = parts[:len(parts)-1]
	}
	return fit(strings.Join(parts, "  "), m.width)
}

func (m tuiModel) helpView() string {
	help := []struct{ key, what string }{
		{"↑/k ↓/j", "move; pgup/pgdn, g/G jump"},
		{"enter, s", "open a terminal on the box (boxctl ssh)"},
		{"c", "create a box: name (empty for a generated one), image and size"},
		{"p", "pause the box (running only)"},
		{"r", "resume the box (paused only)"},
		{"d, x", "destroy the box, after a y/N question"},
		{"K", "keep: hold the box off the idle reaper's deletion for a day"},
		{"a", "also show paused and stopped boxes (ls -a)"},
		{"/", "filter by name, state, image, project, branch or label; esc clears"},
		{"i", "show or hide the selected box's details"},
		{"R", "refresh now; the list refreshes itself every " + tuiRefreshEvery.String()},
		{"q", "quit (esc clears the filter first)"},
	}
	var b strings.Builder
	b.WriteString(tuiStyles.title.Render("Keys") + "\n\n")
	for _, h := range help {
		b.WriteString("  " + tuiStyles.key.Render(padRight(h.key, 10)) + " " + h.what + "\n")
	}
	b.WriteString("\n" + tuiStyles.dim.Render("Everything here is what the matching boxctl subcommand does; the box list is boxctl ls's.") + "\n")
	return b.String()
}

// padRight pads s with spaces to w cells (no-op when it's wider).
func padRight(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// fit cuts s to w cells, ANSI styling kept.
func fit(s string, w int) string {
	if w <= 0 || lipgloss.Width(s) <= w {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(s)
}
