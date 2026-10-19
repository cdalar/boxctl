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
// fields. The images and sizes come from the server when the form opens
// (what `boxctl images`/`sizes` list), with create's own defaults
// selected: defaultImage, and the size the server marks default.

// tuiCatalogMsg is the images and sizes the form offers, once listed.
type tuiCatalogMsg struct {
	images []client.Image
	sizes  []client.Size
	err    error
}

type tuiCreateForm struct {
	name   textinput.Model
	images []client.Image
	sizes  []client.Size
	image  int // index into images
	size   int // index into sizes
	field  int // 0 name, 1 image, 2 size
	loaded bool
	err    string // why the catalog couldn't be listed, if it couldn't
}

const (
	createFieldName = iota
	createFieldImage
	createFieldSize
	createFields
)

func newTUICreateForm() *tuiCreateForm {
	n := textinput.New()
	n.Prompt = ""
	n.Placeholder = "a generated name, like quiet-otter-1a2b"
	n.CharLimit = boxNameMax
	return &tuiCreateForm{name: n}
}

// openCreate shows the form and lists what it can offer.
func (m tuiModel) openCreate() (tea.Model, tea.Cmd) {
	if m.creating {
		m.setStatus("a box is already being created", true)
		return m, nil
	}
	m.create = newTUICreateForm()
	m.mode = modeCreate
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
		if img.Name == defaultImage {
			f.image = i
		}
	}
	for i, s := range f.sizes {
		if s.Default {
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
		return defaultImage
	}
	return f.images[f.image].Name
}

func (f *tuiCreateForm) sizeName() string {
	if len(f.sizes) == 0 {
		return ""
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
		m.mode = modeList
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

// submitCreate makes the box in the background and goes back to the
// list, where it shows up as provisioning on the next refresh.
func (m tuiModel) submitCreate() (tea.Model, tea.Cmd) {
	f := m.create
	if !f.loaded {
		m.setStatus("still listing images and sizes…", true)
		return m, nil
	}
	name := strings.TrimSpace(f.name.Value())
	image, size := f.imageName(), f.sizeName()
	m.mode = modeList
	m.create = nil
	m.creating = true
	if name == "" {
		m.setStatus(fmt.Sprintf("creating a box (%s, %s)…", image, orDefault(size)), false)
	} else {
		m.setStatus(fmt.Sprintf("creating %s (%s, %s)…", name, image, orDefault(size)), false)
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
	return m, tea.Batch(create, m.spinIfBusy())
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

func (f *tuiCreateForm) view(width int) string {
	var b strings.Builder
	b.WriteString(tuiStyles.title.Render("Create a box") + "\n\n")
	mark := func(field int) string {
		if f.field == field {
			return tuiStyles.accent.Render("▸ ")
		}
		return "  "
	}
	label := func(s string) string { return tuiStyles.dim.Render(padRight(s, 7)) }

	b.WriteString(fit(mark(createFieldName)+label("Name")+f.name.View(), width) + "\n")

	choice := func(field int, name, hint string) string {
		s := "‹ " + name + " ›"
		if f.field == field {
			s = tuiStyles.key.Render(s)
		}
		if hint != "" {
			s += "  " + tuiStyles.dim.Render(hint)
		}
		return s
	}
	switch {
	case !f.loaded:
		b.WriteString(mark(createFieldImage) + label("Image") + tuiStyles.dim.Render("listing…") + "\n")
		b.WriteString(mark(createFieldSize) + label("Size") + tuiStyles.dim.Render("listing…") + "\n")
	case f.err != "":
		b.WriteString(mark(createFieldImage) + label("Image") + defaultImage + "  " + tuiStyles.err.Render("couldn't list images: "+f.err) + "\n")
		b.WriteString(mark(createFieldSize) + label("Size") + "the server's default" + "\n")
	default:
		if len(f.images) > 0 {
			img := f.images[f.image]
			b.WriteString(fit(mark(createFieldImage)+label("Image")+choice(createFieldImage, img.Name, img.Description), width) + "\n")
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
			b.WriteString(fit(mark(createFieldSize)+label("Size")+choice(createFieldSize, s.Name, hint), width) + "\n")
		}
	}
	b.WriteString("\n" + tuiStyles.dim.Render("Like `boxctl create [name] --image … --size …`. The box boots in a few seconds; the list shows it as it comes up.") + "\n")
	return b.String()
}
