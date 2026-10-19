package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cdalar/boxctl/internal/client"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// The tui's create form: `boxctl create [name] --image --size`, as three
// fields in the box. The images and sizes come from the server when the
// form opens (what `boxctl images`/`sizes` list), with create's own
// defaults selected -- defaultImage, and the size the server marks
// default -- unless the form was opened from the images or sizes view,
// whose row is selected instead.

// tuiCatalogMsg is the images and sizes the form offers, once listed.
type tuiCatalogMsg struct {
	images []client.Image
	sizes  []client.Size
	err    error
}

type tuiCreateForm struct {
	name      textinput.Model
	images    []client.Image
	sizes     []client.Size
	image     int // index into images
	size      int // index into sizes
	field     int // 0 name, 1 image, 2 size
	loaded    bool
	err       string // why the catalog couldn't be listed, if it couldn't
	wantImage string // preselected, from the images view
	wantSize  string // preselected, from the sizes view
}

const (
	createFieldName = iota
	createFieldImage
	createFieldSize
	createFields
)

func newTUICreateForm(image, size string) *tuiCreateForm {
	n := textinput.New()
	n.Prompt = ""
	n.Placeholder = "a generated name, like quiet-otter-1a2b"
	n.CharLimit = boxNameMax
	return &tuiCreateForm{name: n, wantImage: image, wantSize: size}
}

// openCreate shows the form and lists what it can offer. image and
// size, when given, are preselected.
func (m tuiModel) openCreate(image, size string) (tea.Model, tea.Cmd) {
	if m.creating {
		return m, m.fail("a box is already being created")
	}
	m.create = newTUICreateForm(image, size)
	c := m.c
	list := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		images, err := c.ListImages(ctx)
		if err != nil {
			return tuiCatalogMsg{err: err}
		}
		sizes, err := c.ListSizes(ctx)
		if err != nil {
			return tuiCatalogMsg{err: err}
		}
		return tuiCatalogMsg{images: images, sizes: sizes}
	}
	return m, tea.Batch(list, m.create.name.Focus())
}

// setCatalog fills the choices in and selects the defaults.
func (f *tuiCreateForm) setCatalog(msg tuiCatalogMsg) {
	f.loaded = true
	if msg.err != nil {
		f.err = msg.err.Error()
		return
	}
	f.images, f.sizes = msg.images, msg.sizes
	for i, img := range f.images {
		if img.Name == defaultImage || img.Name == f.wantImage {
			f.image = i
		}
	}
	for i, s := range f.sizes {
		if s.Default && f.wantSize == "" || s.Name == f.wantSize {
			f.size = i
		}
	}
}

func (f *tuiCreateForm) update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	f.name, cmd = f.name.Update(msg)
	return cmd
}

// imageName/sizeName are the chosen names, create's defaults when the
// catalog couldn't be listed (an empty size is the server's default).
func (f *tuiCreateForm) imageName() string {
	if len(f.images) == 0 {
		if f.wantImage != "" {
			return f.wantImage
		}
		return defaultImage
	}
	return f.images[f.image].Name
}

func (f *tuiCreateForm) sizeName() string {
	if len(f.sizes) == 0 {
		return f.wantSize
	}
	return f.sizes[f.size].Name
}

// cycle moves the active choice field by n, wrapping around.
func (f *tuiCreateForm) cycle(n int) {
	switch f.field {
	case createFieldImage:
		if len(f.images) > 0 {
			f.image = ((f.image+n)%len(f.images) + len(f.images)) % len(f.images)
		}
	case createFieldSize:
		if len(f.sizes) > 0 {
			f.size = ((f.size+n)%len(f.sizes) + len(f.sizes)) % len(f.sizes)
		}
	}
}

func (m tuiModel) handleCreateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.create
	switch msg.Type {
	case tea.KeyEsc:
		m.create = nil
		return m, nil
	case tea.KeyTab, tea.KeyDown:
		f.field = (f.field + 1) % createFields
	case tea.KeyShiftTab, tea.KeyUp:
		f.field = (f.field + createFields - 1) % createFields
	case tea.KeyLeft:
		if f.field != createFieldName {
			f.cycle(-1)
			return m, nil
		}
	case tea.KeyRight:
		if f.field != createFieldName {
			f.cycle(1)
			return m, nil
		}
	case tea.KeyEnter:
		return m.submitCreate()
	}
	switch msg.Type {
	case tea.KeyTab, tea.KeyDown, tea.KeyShiftTab, tea.KeyUp:
		if f.field == createFieldName {
			return m, f.name.Focus()
		}
		f.name.Blur()
		return m, nil
	}
	if f.field == createFieldName {
		var cmd tea.Cmd
		f.name, cmd = f.name.Update(msg)
		return m, cmd
	}
	return m, nil
}

// submitCreate makes the box in the background and closes the form;
// the box shows up as provisioning on the next refresh.
func (m tuiModel) submitCreate() (tea.Model, tea.Cmd) {
	f := m.create
	if !f.loaded {
		return m, m.fail("still listing images and sizes…")
	}
	name := strings.TrimSpace(f.name.Value())
	image, size := f.imageName(), f.sizeName()
	m.create = nil
	m.creating = true
	what := "a box"
	if name != "" {
		what = name
	}
	c := m.c
	create := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), createTimeoutTUI)
		defer cancel()
		if name == "" {
			n, err := newBoxName(ctx, "", "")
			if err != nil {
				return tuiCreatedMsg{err: err}
			}
			name = n.box()
		}
		vm, err := c.Create(ctx, name, "", image, size, nil)
		return tuiCreatedMsg{vm: vm, err: err}
	}
	return m, tea.Batch(create, m.say("creating %s (%s, %s)…", what, image, orDefault(size)), m.spinIfBusy())
}

// createTimeoutTUI bounds a create from the form; the client's own
// Create timeout (6 minutes, a minute past the server's) applies within
// it.
const createTimeoutTUI = 7 * time.Minute

func orDefault(size string) string {
	if size == "" {
		return "default size"
	}
	return size
}

// lines is the form, drawn inside the box.
func (f *tuiCreateForm) lines(width int) []string {
	mark := func(field int) string {
		if f.field == field {
			return k9s.prompt.Render(" ▸ ")
		}
		return "   "
	}
	label := func(s string) string { return k9s.infoKey.Render(padRight(s, 7)) }
	choice := func(field int, name, hint string) string {
		s := "‹ " + name + " ›"
		if f.field == field {
			s = k9s.hotKey.Render(s)
		}
		if hint != "" {
			s += "  " + k9s.dim.Render(hint)
		}
		return s
	}
	lines := []string{"", fit(mark(createFieldName)+label("Name")+f.name.View(), width)}
	switch {
	case !f.loaded:
		lines = append(lines,
			mark(createFieldImage)+label("Image")+k9s.dim.Render("listing…"),
			mark(createFieldSize)+label("Size")+k9s.dim.Render("listing…"))
	case f.err != "":
		lines = append(lines,
			mark(createFieldImage)+label("Image")+f.imageName()+"  "+k9s.flashErr.Render("couldn't list images: "+f.err),
			mark(createFieldSize)+label("Size")+orDefault(f.sizeName()))
	default:
		if len(f.images) > 0 {
			img := f.images[f.image]
			lines = append(lines, fit(mark(createFieldImage)+label("Image")+choice(createFieldImage, img.Name, img.Description), width))
		}
		if len(f.sizes) > 0 {
			s := f.sizes[f.size]
			hint := fmt.Sprintf("%d vCPU, %s", s.VCPU, memLabel(s.MemMiB))
			if s.DiskMiB > 0 {
				hint += ", " + memLabel(s.DiskMiB) + " disk"
			}
			if s.Default {
				hint += " (default)"
			}
			lines = append(lines, fit(mark(createFieldSize)+label("Size")+choice(createFieldSize, s.Name, hint), width))
		}
	}
	lines = append(lines, "",
		" "+k9s.hotKey.Render("<tab>")+" "+k9s.hotLabel.Render("next field")+"   "+
			k9s.hotKey.Render("<←/→>")+" "+k9s.hotLabel.Render("change")+"   "+
			k9s.hotKey.Render("<enter>")+" "+k9s.hotLabel.Render("create")+"   "+
			k9s.hotKey.Render("<esc>")+" "+k9s.hotLabel.Render("cancel"),
		"",
		" "+k9s.dim.Render("Like `boxctl create [name] --image … --size …`. The box boots in a few seconds and shows up as it comes up."))
	return lines
}
