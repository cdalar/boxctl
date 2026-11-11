package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// maxImportDepth is how deep @imports are followed, as Claude Code does
// (five hops from CLAUDE.md).
const maxImportDepth = 5

// importExcluded are the top-level entries of ~/.claude that are never
// copied, imported or not: credentials, history, this machine's projects
// and plugins, and settings (which go through boxSettings).
var importExcluded = map[string]bool{
	"projects": true, "plugins": true, "history.jsonl": true,
	"settings.json": true, "settings.local.json": true,
	"todos": true, "shell-snapshots": true, "statsig": true, "ide": true,
}

var (
	fencedBlock = regexp.MustCompile("(?s)(^|\n)[ \t]*(```|~~~).*?(\n[ \t]*(```|~~~)[^\n]*|$)")
	codeSpan    = regexp.MustCompile("`[^`\n]*`")
	// importToken is an @ at the start of a line or after whitespace --
	// so an email address's @ isn't one -- and the run of characters after.
	importToken = regexp.MustCompile(`(?:^|\s)@(\S+)`)
)

// importRefs returns the @path tokens of markdown text, outside code
// blocks and code spans, trailing punctuation removed.
func importRefs(text string) []string {
	text = fencedBlock.ReplaceAllString(text, "\n")
	text = codeSpan.ReplaceAllString(text, " ")
	var refs []string
	for _, m := range importToken.FindAllStringSubmatch(text, -1) {
		ref := strings.TrimRight(m[1], ".,;:!?)]}\"'")
		if ref != "" {
			refs = append(refs, ref)
		}
	}
	return refs
}

// looksLikePath says whether an @token is plausibly meant as a file --
// worth a warning when it isn't there -- rather than an @mention in prose.
func looksLikePath(ref string) bool {
	return strings.ContainsAny(ref, "/\\") || strings.HasPrefix(ref, "~") || filepath.Ext(ref) != ""
}

// importedConfigFiles follows the @imports of src/CLAUDE.md, the way
// Claude Code resolves them (relative to the importing file, ~/ allowed,
// five levels deep, loops ignored), and returns the files to copy as
// paths relative to src, in order found, plus one warning per import
// that is skipped: missing, outside src, excluded, or too deep. Only
// regular files under src that aren't excluded are returned; an import
// is never followed into an excluded path.
func importedConfigFiles(src, home string) (files, warnings []string) {
	realSrc, err := filepath.EvalSymlinks(src)
	if err != nil {
		return nil, nil
	}
	seen := map[string]bool{"CLAUDE.md": true}
	warned := map[string]bool{}
	warn := func(msg string) {
		if !warned[msg] {
			warned[msg] = true
			warnings = append(warnings, msg)
		}
	}
	var walk func(rel string, depth int)
	walk = func(rel string, depth int) {
		data, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(rel)))
		if err != nil {
			return
		}
		for _, ref := range importRefs(string(data)) {
			var abs string
			switch {
			case ref == "~" || strings.HasPrefix(ref, "~/"):
				abs = filepath.Join(home, ref[1:])
			case filepath.IsAbs(ref):
				abs = filepath.Clean(ref)
			default:
				abs = filepath.Join(src, filepath.FromSlash(filepath.Dir(rel)), filepath.FromSlash(ref))
			}
			real, err := filepath.EvalSymlinks(abs)
			if err != nil {
				if looksLikePath(ref) {
					warn("skipping import @" + ref + " in " + rel + ": file not found")
				}
				continue
			}
			st, err := os.Stat(real)
			if err != nil || !st.Mode().IsRegular() {
				if looksLikePath(ref) {
					warn("skipping import @" + ref + " in " + rel + ": not a file")
				}
				continue
			}
			rs, err := filepath.Rel(realSrc, real)
			if err != nil || rs == ".." || strings.HasPrefix(rs, ".."+string(filepath.Separator)) {
				warn("skipping import @" + ref + " in " + rel + ": outside ~/.claude")
				continue
			}
			// The path as it sits under src (not the symlink's target),
			// so the box gets the name the importer used.
			target := rs
			if r, err := filepath.Rel(src, abs); err == nil && !strings.HasPrefix(r, "..") {
				target = r
			}
			target = filepath.ToSlash(target)
			if importPathExcluded(target) || importPathExcluded(filepath.ToSlash(rs)) {
				warn("skipping import @" + ref + " in " + rel + ": not copied to the box")
				continue
			}
			if seen[target] {
				continue
			}
			if depth >= maxImportDepth {
				warn("skipping import @" + ref + " in " + rel + ": more than " + "5 levels deep")
				continue
			}
			seen[target] = true
			files = append(files, target)
			walk(target, depth+1)
		}
	}
	walk("CLAUDE.md", 0)
	return files, warnings
}

// importPathExcluded says whether rel (slash-separated, under ~/.claude)
// is never copied: an excluded top-level entry, a credentials file, a
// configSkippedDirs directory, or .DS_Store.
func importPathExcluded(rel string) bool {
	parts := strings.Split(rel, "/")
	if importExcluded[parts[0]] || strings.HasPrefix(parts[0], ".credentials") {
		return true
	}
	for _, p := range parts[:len(parts)-1] {
		if configSkippedDirs[p] {
			return true
		}
	}
	return parts[len(parts)-1] == ".DS_Store"
}

// configFilesWithImports is configFiles plus the files CLAUDE.md imports
// (importedConfigFiles) that aren't already in the list. rules names
// the rule files that went along -- CLAUDE.md first, then the imports
// -- for the "Copied" line.
func configFilesWithImports(src, home string) (files []string, size int64, rules, warnings []string, err error) {
	files, size, err = configFiles(src)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	have := map[string]bool{}
	for _, f := range files {
		have[f] = true
	}
	if have["CLAUDE.md"] {
		rules = append(rules, "CLAUDE.md")
	}
	var imported []string
	imported, warnings = importedConfigFiles(src, home)
	for _, f := range imported {
		rules = append(rules, f)
		if have[f] {
			continue
		}
		have[f] = true
		files = append(files, f)
		if st, err := os.Stat(filepath.Join(src, filepath.FromSlash(f))); err == nil {
			size += st.Size()
		}
	}
	return files, size, rules, warnings, nil
}
