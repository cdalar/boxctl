package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestLogFormatter(t *testing.T) {
	// Shapes from a real `claude -p --output-format stream-json --verbose`
	// run: two tool calls together, their results after both.
	in := `{"status":"running"}
{"type":"system","subtype":"init","model":"m1","session_id":"s"}
{"type":"system","subtype":"hook_started","session_id":"s"}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a.txt"}}]},"session_id":"s"}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"cat nope"}}]},"session_id":"s"}
{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"},"session_id":"s"}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"Exit code 1","is_error":true}]},"session_id":"s"}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"a\nb"}]},"session_id":"s"}
{"type":"assistant","message":{"content":[{"type":"text","text":"All done."}]},"session_id":"s"}
{"type":"result","subtype":"success","result":"All done.","num_turns":2,"duration_ms":3000,"total_cost_usd":0.5,"session_id":"s"}
`
	var out bytes.Buffer
	f := &logFormatter{w: &out}
	for _, c := range []string{in[:30], in[30:]} { // split mid-line
		if _, err := f.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	want := `{"status":"running"}
── session started (m1)
▶ Read a.txt
▶ Bash cat nope
  ✘ Bash: Exit code 1
  ✔ Read: a b
All done.
── ✔ success after 2 turns, 3s, $0.50
`
	if got := out.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if strings.Count(out.String(), "All done.") != 1 {
		t.Error("the final text is repeated")
	}
}
