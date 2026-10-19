package cmd

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cdalar/boxctl/internal/client"
)

// The tui's resource views, k9s-style: each is a table over one of the
// listings the CLI has (`:boxes`, `:images`, `:sizes`, `:backups`,
// `:urls`), with its own columns, hotkeys and sort keys. A view's rows
// are computed from the model's data every render, so a refresh or a
// filter never has to patch a table.

// tableRow is one line of a view: key finds the thing again (a box
// name, a backup id, box:port), cells are the columns, sortKeys (if set)
// sort instead of the cells, and state colours the STATE column.
type tableRow struct {
	key      string
	cells    []string
	sortKeys []string
	state    string
}

func (r tableRow) sortKey(col int) string {
	if r.sortKeys != nil && col < len(r.sortKeys) && r.sortKeys[col] != "" {
		return r.sortKeys[col]
	}
	if col < len(r.cells) {
		return r.cells[col]
	}
	return ""
}

// column is a view column: title, and the shift+letter that sorts by it
// (0 for none).
type column struct {
	title string
	sort  rune
}

// hotkey is one entry of the header's legend and the help page.
type hotkey struct{ key, label string }

type tableView struct {
	name     string   // the `:name` that opens it, and its crumb
	aliases  []string // other `:names`
	title    string   // the table's title: "Boxes"
	resource string   // the listing it needs: boxes, images, sizes, backups
	columns  []column
	rows     func(m *tuiModel) []tableRow
	hotkeys  []hotkey // the view's own, before the common ones
	stateCol int      // the column coloured by row.state, -1 for none
	empty    string   // what to say with no rows at all
}

// commonHotkeys apply in every table view.
var commonHotkeys = []hotkey{
	{":", "command"}, {"/", "filter"}, {"?", "help"}, {"esc", "back"}, {"ctrl-c", "quit"},
}

var tuiViews = []*tableView{boxesView, urlsView, imagesView, sizesView, backupsView}

// findView is the view `:name` means, nil for none.
func findView(name string) *tableView {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, v := range tuiViews {
		if v.name == name {
			return v
		}
		for _, a := range v.aliases {
			if a == name {
				return v
			}
		}
	}
	return nil
}

var boxesView = &tableView{
	name:     "boxes",
	aliases:  []string{"box", "vms", "vm", "b"},
	title:    "Boxes",
	resource: "boxes",
	columns: []column{
		{"NAME", 'N'}, {"STATE", 'S'}, {"IMAGE", 'I'}, {"SIZE", 'Z'}, {"AGE", 'A'}, {"RUN", 'R'}, {"PROJECT", 'P'}, {"PR", 0},
	},
	stateCol: 1,
	empty:    "No boxes yet. Press c to create one.",
	hotkeys: []hotkey{
		{"s", "shell"}, {"d", "describe"}, {"y", "json"}, {"l", "logs"}, {"c", "create"},
		{"p", "pause"}, {"r", "resume"}, {"K", "keep"}, {"e", "expose"}, {"ctrl-d", "delete"},
		{"0", "all"}, {"1", "running"}, {"2", "paused"},
	},
	rows: func(m *tuiModel) []tableRow {
		var rows []tableRow
		for i := range m.vms {
			vm := &m.vms[i]
			if !m.stateFilter.allows(vm.State) {
				continue
			}
			image := vm.Image
			if image == "" {
				image = "-"
			}
			state := vm.State
			if v, ok := m.pending[vm.Name]; ok {
				state = presentTense(v)
			}
			rows = append(rows, tableRow{
				key:   vm.Name,
				state: vm.State,
				cells: []string{vm.Name, state, image, sizeLabel(*vm), age(vm.CreatedAt), runText(*vm), projectText(*vm), prText(*vm)},
				sortKeys: []string{"", strconv.Itoa(stateRank(vm.State)) + vm.State, "", fmt.Sprintf("%06d/%08d", vm.VCPU, vm.MemMiB),
					// Newest first when sorted ascending by AGE, like ls.
					fmt.Sprintf("%020d", -vm.CreatedAt.Unix()+1<<62)},
			})
		}
		return rows
	},
}

var urlsView = &tableView{
	name:     "urls",
	aliases:  []string{"url", "ingress", "expose", "exposed"},
	title:    "Public URLs",
	resource: "boxes",
	columns:  []column{{"BOX", 'B'}, {"PORT", 'P'}, {"URL", 'U'}, {"STATUS", 'S'}, {"AGE", 'A'}},
	stateCol: 3,
	empty:    "No public URLs. Press e on a box to expose a port.",
	hotkeys:  []hotkey{{"ctrl-d", "unexpose"}, {"d", "describe box"}},
	rows: func(m *tuiModel) []tableRow {
		var rows []tableRow
		for _, vm := range m.vms {
			for _, in := range vm.Ingress {
				rows = append(rows, tableRow{
					key:      vm.Name + ":" + strconv.Itoa(in.Port),
					state:    in.Status,
					cells:    []string{vm.Name, strconv.Itoa(in.Port), in.URL, in.Status, age(in.CreatedAt)},
					sortKeys: []string{"", fmt.Sprintf("%06d", in.Port), "", "", fmt.Sprintf("%020d", -in.CreatedAt.Unix()+1<<62)},
				})
			}
		}
		return rows
	},
}

var imagesView = &tableView{
	name:     "images",
	aliases:  []string{"image", "img", "i"},
	title:    "Images",
	resource: "images",
	columns:  []column{{"NAME", 'N'}, {"PROVIDER", 'P'}, {"DESCRIPTION", 'D'}},
	stateCol: -1,
	empty:    "No images available.",
	hotkeys:  []hotkey{{"c", "create box from image"}},
	rows: func(m *tuiModel) []tableRow {
		var rows []tableRow
		for _, img := range m.images {
			rows = append(rows, tableRow{key: img.Name, cells: []string{img.Name, img.Provider, img.Description}})
		}
		return rows
	},
}

var sizesView = &tableView{
	name:     "sizes",
	aliases:  []string{"size", "sz"},
	title:    "Sizes",
	resource: "sizes",
	columns:  []column{{"NAME", 'N'}, {"VCPU", 'V'}, {"MEMORY", 'M'}, {"DISK", 'D'}, {"DEFAULT", 0}},
	stateCol: -1,
	empty:    "No sizes available.",
	hotkeys:  []hotkey{{"c", "create box of this size"}},
	rows: func(m *tuiModel) []tableRow {
		var rows []tableRow
		for _, s := range m.sizes {
			disk := "-"
			if s.DiskMiB > 0 {
				disk = memLabel(s.DiskMiB)
			}
			def := ""
			if s.Default {
				def = "yes"
			}
			rows = append(rows, tableRow{
				key:      s.Name,
				cells:    []string{s.Name, strconv.Itoa(s.VCPU), memLabel(s.MemMiB), disk, def},
				sortKeys: []string{"", fmt.Sprintf("%06d", s.VCPU), fmt.Sprintf("%08d", s.MemMiB), fmt.Sprintf("%08d", s.DiskMiB), ""},
			})
		}
		return rows
	},
}

var backupsView = &tableView{
	name:     "backups",
	aliases:  []string{"backup", "bk"},
	title:    "Backups",
	resource: "backups",
	columns:  []column{{"ID", 'I'}, {"BOX", 'B'}, {"SIZE", 'Z'}, {"STATUS", 'S'}, {"AGE", 'A'}},
	stateCol: 3,
	empty:    "No backups yet. Pause a box and run `boxctl download <name>` to make one.",
	hotkeys:  []hotkey{{"d", "describe"}},
	rows: func(m *tuiModel) []tableRow {
		var rows []tableRow
		for _, b := range m.backups {
			rows = append(rows, tableRow{
				key:      b.ID,
				state:    b.UploadStatus,
				cells:    []string{b.ID, b.VMName, client.FormatBytes(b.SizeBytes), b.UploadStatus, age(b.CreatedAt)},
				sortKeys: []string{"", "", fmt.Sprintf("%020d", b.SizeBytes), "", fmt.Sprintf("%020d", -b.CreatedAt.Unix()+1<<62)},
			})
		}
		return rows
	},
}

// stateFilter is the boxes view's 0/1/2 (k9s's namespace keys): every
// box, the live ones (ls's default) or the paused ones.
type stateFilter int

const (
	showAllBoxes stateFilter = iota
	showLiveBoxes
	showPausedBoxes
)

func (f stateFilter) allows(state string) bool {
	switch f {
	case showLiveBoxes:
		return stateRank(state) == 0
	case showPausedBoxes:
		return state == "paused"
	}
	return true
}

func (f stateFilter) String() string {
	switch f {
	case showLiveBoxes:
		return "running"
	case showPausedBoxes:
		return "paused"
	}
	return "all"
}

// sortRows orders rows by col (its sort key), descending if desc; a
// stable sort, so rows the key doesn't tell apart keep the view's own
// order. col -1 leaves them as they are.
func sortRows(rows []tableRow, col int, desc bool) {
	if col < 0 {
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].sortKey(col), rows[j].sortKey(col)
		if desc {
			return a > b
		}
		return a < b
	})
}

// filterRows keeps the rows with filter (lowercase) in any cell.
func filterRows(rows []tableRow, filter string) []tableRow {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return rows
	}
	var out []tableRow
	for _, r := range rows {
		for _, c := range r.cells {
			if strings.Contains(strings.ToLower(c), filter) {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// sortBoxes orders vms as ls does: live, paused, other; newest first
// within a group; id as the tiebreak. It is the boxes view's order
// before any shift+letter sort.
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

// describeBox is the `d` page: the box as `kubectl describe` would lay
// it out, everything the API says about it.
func describeBox(vm client.VM) []string {
	kv := func(k, v string) string { return fmt.Sprintf("%-16s%s", k+":", v) }
	var lines []string
	lines = append(lines, kv("Name", vm.Name))
	if vm.ID != "" && vm.ID != vm.Name {
		lines = append(lines, kv("ID", vm.ID))
	}
	lines = append(lines, kv("State", vm.State))
	ready := "no"
	if vm.Ready {
		ready = "yes"
	}
	lines = append(lines, kv("Ready", ready))
	lines = append(lines, kv("IP", orDash(vm.IP)))
	lines = append(lines, kv("Provider", orDash(vm.Provider)))
	lines = append(lines, kv("Image", orDash(vm.Image)))
	size := sizeLabel(vm)
	if vm.VCPU > 0 {
		size += fmt.Sprintf(" (%d vCPU, %s)", vm.VCPU, memLabel(vm.MemMiB))
	}
	lines = append(lines, kv("Size", size))
	lines = append(lines, kv("Created", stamp(vm.CreatedAt)+" ("+age(vm.CreatedAt)+" ago)"))
	if !vm.UpdatedAt.IsZero() {
		lines = append(lines, kv("Updated", stamp(vm.UpdatedAt)))
	}
	if vm.LastActivityAt != nil {
		lines = append(lines, kv("Last Used", stamp(*vm.LastActivityAt)+" ("+age(*vm.LastActivityAt)+" ago)"))
	}
	if vm.DeleteAt != nil {
		lines = append(lines, kv("Idle Delete At", stamp(*vm.DeleteAt)+"  (paused for being unused; K keeps it a day, resuming cancels it)"))
	}
	if vm.KeepUntil != nil {
		lines = append(lines, kv("Kept Until", stamp(*vm.KeepUntil)))
	}
	if st, err := parseGoalStatus(vm.RunStatus); err != nil {
		lines = append(lines, kv("Run Status", "unreadable: "+err.Error()))
	} else if st != nil {
		lines = append(lines, "", "Run:")
		lines = append(lines, "  "+kv("Kind", runText(vm)))
		if st.Branch != "" {
			lines = append(lines, "  "+kv("Branch", st.Branch))
		}
		urls := st.PRURLs
		if len(urls) == 0 && st.PRURL != "" {
			urls = []string{st.PRURL}
		}
		for i, u := range urls {
			k := "PR"
			if i > 0 {
				k = ""
			}
			lines = append(lines, "  "+kv(k, u))
		}
		if st.Error != "" {
			lines = append(lines, "  "+kv("Error", st.Error))
		}
		if st.Updated != "" {
			lines = append(lines, "  "+kv("Updated", st.Updated))
		}
		if vm.RunStatusAt != nil {
			lines = append(lines, "  "+kv("Reported", stamp(*vm.RunStatusAt)))
		}
	} else if run := vm.Labels["boxctl.run"]; run != "" {
		lines = append(lines, "", "Run:", "  "+kv("Kind", runText(vm)))
		if b := vm.Labels["boxctl.branch"]; b != "" {
			lines = append(lines, "  "+kv("Branch", b))
		}
	}
	if len(vm.Labels) > 0 {
		lines = append(lines, "", "Labels:")
		keys := make([]string, 0, len(vm.Labels))
		for k := range vm.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			lines = append(lines, "  "+k+"="+vm.Labels[k])
		}
	}
	if len(vm.Ingress) > 0 {
		lines = append(lines, "", "Public URLs:")
		for _, in := range vm.Ingress {
			s := fmt.Sprintf("  %-6d %s  (%s", in.Port, in.URL, in.Status)
			if in.HostHeader != "" {
				s += ", Host: " + in.HostHeader
			}
			lines = append(lines, s+")")
		}
	}
	return lines
}

// describeBackup is the backups view's `d` page.
func describeBackup(b client.Backup) []string {
	kv := func(k, v string) string { return fmt.Sprintf("%-16s%s", k+":", v) }
	lines := []string{
		kv("ID", b.ID),
		kv("Box", b.VMName),
		kv("Size", client.FormatBytes(b.SizeBytes)),
		kv("Upload", b.UploadStatus),
	}
	if b.UploadError != "" {
		lines = append(lines, kv("Upload Error", b.UploadError))
	}
	lines = append(lines, kv("Created", stamp(b.CreatedAt)+" ("+age(b.CreatedAt)+" ago)"))
	lines = append(lines, "", "Restore it as a new box with `boxctl restore "+b.ID+"`.")
	return lines
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func stamp(t time.Time) string { return t.Local().Format(time.DateTime) }
