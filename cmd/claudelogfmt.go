package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// logFormatter turns `claude -p --output-format stream-json` lines into
// readable text. It is an io.Writer fed the raw stream in any chunking;
// lines that aren't Claude's events (status.json, the blank line before
// the final status) pass through unchanged, and Claude's events this
// doesn't know (rate_limit_event, hooks, anything newer) are dropped.
//
// Tool calls that run together all come before any of their results, so
// a result is labelled with its call's tool (tools, by tool_use id). The
// final result line repeats Claude's last text; lastText drops that.
type logFormatter struct {
	w        io.Writer
	buf      []byte
	tools    map[string]string
	lastText string
}

func (f *logFormatter) Write(p []byte) (int, error) {
	f.buf = append(f.buf, p...)
	for {
		i := bytes.IndexByte(f.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := string(f.buf[:i])
		f.buf = f.buf[i+1:]
		if _, err := io.WriteString(f.w, f.formatLine(line)); err != nil {
			return len(p), err
		}
	}
}

// Flush writes a final line that had no newline.
func (f *logFormatter) Flush() error {
	if len(f.buf) == 0 {
		return nil
	}
	line := string(f.buf)
	f.buf = nil
	_, err := io.WriteString(f.w, f.formatLine(line))
	return err
}

type logEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	Message   struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	Result     string  `json:"result"`
	IsError    bool    `json:"is_error"`
	DurationMS int64   `json:"duration_ms"`
	NumTurns   int     `json:"num_turns"`
	CostUSD    float64 `json:"total_cost_usd"`
}

type logBlock struct {
	Type      string                 `json:"type"`
	Text      string                 `json:"text"`
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Input     map[string]interface{} `json:"input"`
	ToolUseID string                 `json:"tool_use_id"`
	Content   json.RawMessage        `json:"content"`
	IsError   bool                   `json:"is_error"`
}

// formatLine renders one line, newline included.
func (f *logFormatter) formatLine(line string) string {
	raw := line + "\n"
	if !strings.HasPrefix(strings.TrimSpace(line), "{") {
		return raw
	}
	var ev logEvent
	if json.Unmarshal([]byte(line), &ev) != nil || ev.Type == "" {
		return raw
	}
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			return fmt.Sprintf("── session started (%s)\n", ev.Model)
		}
		return ""
	case "assistant", "user":
		var blocks []logBlock
		if json.Unmarshal(ev.Message.Content, &blocks) != nil {
			return "" // a user turn that is plain text
		}
		var b strings.Builder
		for _, bl := range blocks {
			switch bl.Type {
			case "text":
				if t := strings.TrimSpace(bl.Text); t != "" {
					b.WriteString(t + "\n")
					f.lastText = t
				}
			case "tool_use":
				if f.tools == nil {
					f.tools = map[string]string{}
				}
				f.tools[bl.ID] = bl.Name
				fmt.Fprintf(&b, "▶ %s %s\n", bl.Name, toolSummary(bl.Input))
			case "tool_result":
				mark := "  ✔ "
				if bl.IsError {
					mark = "  ✘ "
				}
				if name := f.tools[bl.ToolUseID]; name != "" {
					mark += name + ": "
				}
				b.WriteString(mark + truncateLine(resultText(bl.Content), 200) + "\n")
			}
		}
		return b.String()
	case "result":
		mark := "✔"
		if ev.IsError || (ev.Subtype != "" && ev.Subtype != "success") {
			mark = "✘"
		}
		s := fmt.Sprintf("── %s %s after %d turns, %ds, $%.2f\n", mark, ev.Subtype, ev.NumTurns, ev.DurationMS/1000, ev.CostUSD)
		if t := strings.TrimSpace(ev.Result); t != "" && t != f.lastText {
			s += t + "\n"
		}
		return s
	}
	if ev.SessionID != "" {
		return "" // one of Claude's events, just not one worth showing
	}
	return raw
}

// toolSummary is a tool call's input on one line: the argument people
// recognise (command, path, pattern) if there is one, else all of them.
func toolSummary(in map[string]interface{}) string {
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "description"} {
		if v, ok := in[k].(string); ok {
			return truncateLine(v, 200)
		}
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, in[k]))
	}
	return truncateLine(strings.Join(parts, " "), 200)
}

// resultText is a tool result's text: a string, or a list of text blocks.
func resultText(c json.RawMessage) string {
	var s string
	if json.Unmarshal(c, &s) == nil {
		return s
	}
	var blocks []logBlock
	if json.Unmarshal(c, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, " ")
}

// truncateLine flattens s to one line of at most n runes.
func truncateLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
