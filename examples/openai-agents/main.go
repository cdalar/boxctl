// Command openai-agents runs an OpenAI Agents API session whose
// environment is a boxctl box: OpenAI runs the agent harness, and
// `codex exec-server` inside the box dials out to receive its shell
// commands and file edits. The box needs no inbound port.
//
// It creates a box, installs the Codex CLI in it, creates a
// self-hosted session, starts the executor, sends one task and streams
// the agent's answer, then lists /workspace from the box itself as
// proof the work happened there. The session is deleted and the box
// destroyed at the end unless -keep is set.
//
// Needs `boxctl login` done, OPENAI_API_KEY (the application key, which
// never enters the box) and OPENAI_EXECUTOR_API_KEY (an environment key
// from the platform dashboard's Agents tab, with every other permission
// None -- the only credential the box gets). See README.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/cdalar/boxctl/internal/client"
	"github.com/cdalar/boxctl/internal/config"
	"github.com/gorilla/websocket"
	"github.com/openai/openai-go/v3"
)

const workspace = "/workspace"

// keyFile is where the environment key lands inside the box, as a
// systemd EnvironmentFile (CODEX_API_KEY=...) -- written through the
// terminal (see storeKey), read only by the executor's unit.
const keyFile = "/root/.codex-env"

func main() {
	name := flag.String("name", fmt.Sprintf("agents-demo-%04d", rand.IntN(10000)), "box name")
	size := flag.String("size", "small", "box size (boxctl sizes)")
	image := flag.String("image", "debian-slim", "box image (boxctl images)")
	model := flag.String("model", "gpt-6-astra", "agent model")
	task := flag.String("task", "Create fib.py that prints the first 10 Fibonacci numbers, run it, and report its exact output.", "what to ask the agent")
	keep := flag.Bool("keep", false, "keep the box and session afterwards instead of cleaning up")
	box := flag.String("box", "", "use this existing box, prepared with the onctl template openai-agents/codex-executor.sh, instead of creating one (never destroyed)")
	flag.Parse()

	if err := run(*name, *box, *size, *image, *model, *task, *keep); err != nil {
		log.Fatalf("error: %v", err)
	}
}

func run(name, box, size, image, model, task string, keep bool) error {
	if os.Getenv("OPENAI_API_KEY") == "" {
		return errors.New("OPENAI_API_KEY is not set")
	}
	executorKey := os.Getenv("OPENAI_EXECUTOR_API_KEY")
	if executorKey == "" {
		return errors.New("OPENAI_EXECUTOR_API_KEY is not set -- create an environment key on the Agents tab of platform.openai.com (see README.md)")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Token == "" {
		return errors.New("not logged in to boxctl -- run `boxctl login`")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	bc := client.New(cfg.APIURL, cfg.Token)
	oc := openai.NewClient()

	// 1. A box, with the Codex CLI in it: the one given, or a new one.
	if box != "" {
		name = box
		log.Printf("using box %s", name)
	} else {
		// Only a box this run creates is destroyed -- registered before
		// newBox so a failed install doesn't leave it behind.
		if !keep {
			defer func() {
				log.Printf("destroying box %s", name)
				if err := bc.Destroy(context.Background(), name); err != nil {
					log.Printf("destroying box %s: %v", name, err)
				}
			}()
		}
		if err := newBox(ctx, bc, name, size, image); err != nil {
			return err
		}
	}

	// 2. The environment key, typed into the box rather than put on any
	// command line (see storeKey).
	if err := storeKey(ctx, bc, name, executorKey); err != nil {
		return fmt.Errorf("storing the environment key: %w", err)
	}

	// 3. A session whose environment is this box.
	session, err := oc.Beta.Agents.Sessions.New(ctx, openai.BetaAgentSessionNewParams{
		Agent: openai.BetaAgentSessionNewParamsAgent{
			Model:        openai.String(model),
			Instructions: openai.String("You are a helpful coding assistant. Write clean code, run it, and report the actual output."),
		},
		Environment: openai.EnvironmentParamUnion{
			OfParamSelfHosted: &openai.EnvironmentParamSelfHosted{WorkspaceDirectory: workspace},
		},
	})
	if err != nil {
		return fmt.Errorf("creating session: %w", err)
	}
	log.Printf("session %s, environment %s", session.ID, session.Environment.ID)
	if !keep {
		defer func() {
			if _, err := oc.Beta.Agents.Sessions.Delete(context.Background(), session.ID); err != nil {
				log.Printf("deleting session %s: %v", session.ID, err)
			}
		}()
	}

	// 4. Subscribe before anything can happen, so the connection and the
	// turn's first events aren't missed (streams don't replay).
	events := oc.Beta.Agents.Sessions.Events.StreamStreaming(ctx, session.ID)
	defer func() { _ = events.Close() }()
	if err := events.Err(); err != nil {
		return fmt.Errorf("opening the event stream: %w", err)
	}

	// 5. The executor: dials out to OpenAI and stays running, as the
	// transient systemd unit codex-exec-server -- a backgrounded shell job
	// kept exec's SSH channel open until it timed out. The unit takes the
	// key from keyFile, so it's never on a command line. A template-
	// prepared box has codex-connect for exactly this.
	start := fmt.Sprintf("codex-connect %s %s",
		shellQuote(session.Environment.RemoteURL), shellQuote(session.Environment.ID))
	if box == "" {
		start = fmt.Sprintf("systemd-run --quiet --unit=codex-exec-server --working-directory=%s "+
			"--property=EnvironmentFile=%s \"$(command -v codex)\" exec-server --remote %s --environment-id %s",
			workspace, keyFile, shellQuote(session.Environment.RemoteURL), shellQuote(session.Environment.ID))
	}
	if err := execOK(ctx, bc, name, time.Minute, start); err != nil {
		return fmt.Errorf("starting the executor: %w", err)
	}
	log.Printf("executor started in %s", name)

	// 6. The task. The API holds it until the executor connects (up to
	// five minutes), so there's no need to wait for "connected" first.
	if err := oc.Beta.Agents.Sessions.Events.New(ctx, session.ID, openai.BetaAgentSessionEventNewParams{
		Events: []openai.AgentSessionInputParamUnion{{
			OfParamAgentSessionInputMessage: &openai.AgentSessionInputParamAgentSessionInputMessage{
				Input: []openai.AgentSessionInputMessageParam{{
					Content: []openai.InputContentParamUnion{{
						OfParamInputText: &openai.InputContentParamInputText{Text: task},
					}},
				}},
			},
		}},
	}); err != nil {
		return fmt.Errorf("sending the task: %w", err)
	}
	log.Printf("task sent: %s", task)

	if err := follow(events); err != nil {
		logTail(ctx, bc, name)
		return err
	}

	// 7. Proof: the agent's files are in the box, not somewhere else.
	res, err := bc.Exec(ctx, name, "ls -la "+workspace, 30*time.Second)
	if err != nil {
		return fmt.Errorf("listing %s: %w", workspace, err)
	}
	fmt.Printf("\n--- %s in box %s ---\n%s", workspace, name, res.Stdout)
	if keep {
		fmt.Printf("\nkept box %s and session %s\n", name, session.ID)
	}
	return nil
}

// newBox creates a box and installs the Codex CLI in it -- what the
// onctl template openai-agents/codex-executor.sh does for a box given
// with -box.
func newBox(ctx context.Context, bc *client.Client, name, size, image string) error {
	log.Printf("creating box %s (%s, %s)", name, size, image)
	if _, err := bc.Create(ctx, name, "", image, size); err != nil {
		return fmt.Errorf("creating box: %w", err)
	}
	if _, err := bc.WaitReady(ctx, name, 3*time.Minute); err != nil {
		return fmt.Errorf("waiting for box: %w", err)
	}
	log.Printf("installing the Codex CLI in %s (a minute or two)", name)
	if err := execOK(ctx, bc, name, 5*time.Minute,
		"export DEBIAN_FRONTEND=noninteractive LC_ALL=C.UTF-8; "+
			"apt-get update -qq && apt-get install -y -qq nodejs npm >/dev/null && "+
			"npm install -g --silent @openai/codex@alpha && mkdir -p "+workspace+" && codex --version"); err != nil {
		return fmt.Errorf("installing codex: %w", err)
	}
	return nil
}

// eventStream is the part of the SDK's SSE stream follow uses.
type eventStream interface {
	Next() bool
	Current() openai.AgentSessionEventUnion
	Err() error
}

// follow prints the agent's text as it streams and returns when the
// root turn ends -- nil only for a completed one. A subagent's turn
// ending doesn't end the stream.
func follow(events eventStream) error {
	streamed := map[string]bool{} // items whose text arrived as deltas
	for events.Next() {
		ev := events.Current()
		switch ev.Type {
		case "agent.session.environment.pending", "agent.session.environment.connected", "agent.session.environment.disconnected":
			log.Printf("environment: %s", strings.TrimPrefix(ev.Type, "agent.session.environment."))
		case "agent.session.turn.output_text.delta":
			streamed[ev.ItemID] = true
			fmt.Print(ev.Delta)
		case "agent.session.turn.output_text.done":
			if !streamed[ev.ItemID] {
				fmt.Print(ev.Text)
			}
			fmt.Println()
		case "agent.session.requires_action":
			log.Printf("session requires action (an environment connection is expected while the executor starts)")
		case "error", "agent.session.failed", "agent.session.environment.failed":
			return fmt.Errorf("%s: %s", ev.Type, ev.Error.Message)
		case "agent.session.turn.failed", "agent.session.turn.cancelled":
			if ev.Turn.SubagentID == "" {
				return fmt.Errorf("%s: %s", ev.Type, ev.Turn.Error.Message)
			}
		case "agent.session.turn.completed":
			if ev.Turn.SubagentID == "" {
				return nil
			}
		}
	}
	if err := events.Err(); err != nil {
		return fmt.Errorf("event stream: %w", err)
	}
	return errors.New("event stream closed before the turn ended")
}

// storeKey writes the environment key to keyFile inside the box by
// typing it into a silent `read` over the box's terminal. Every other
// way in -- exec, a template -- puts the command line in an argv on the
// host, where any local user's ps can see it; a pty's input stream is
// logged nowhere.
func storeKey(ctx context.Context, bc *client.Client, name, key string) error {
	ticket, vmID, err := bc.MintTerminalTicket(ctx, name)
	if err != nil {
		return err
	}
	wsURL := strings.Replace(bc.BaseURL(), "http", "ws", 1) +
		"/ws/terminal/" + url.PathEscape(vmID) + "?ticket=" + url.QueryEscape(ticket)
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	// Markers are printed with printf's %s so the literal only appears
	// in the command's output, never in the terminal's echo of the
	// command itself.
	send := func(s string) error { return conn.WriteMessage(websocket.BinaryMessage, []byte(s)) }
	if err := send("stty -echo; export HISTFILE=/dev/null; umask 077; printf 'KEY-%s\\n' READY; " +
		"IFS= read -r K && printf 'CODEX_API_KEY=%s\\n' \"$K\" > " + keyFile + " && unset K && printf 'KEY-%s\\n' SAVED; exit\n"); err != nil {
		return err
	}
	if err := waitFor(conn, "KEY-READY"); err != nil {
		return err
	}
	if err := send(key + "\n"); err != nil {
		return err
	}
	return waitFor(conn, "KEY-SAVED")
}

// waitFor reads the terminal until marker appears, or 30s pass.
func waitFor(conn *websocket.Conn, marker string) error {
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	var seen strings.Builder
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("waiting for %s: %w", marker, err)
		}
		seen.Write(msg)
		if strings.Contains(seen.String(), marker) {
			return nil
		}
	}
}

// execOK runs command in the box and fails on a nonzero exit, with the
// command's stderr in the error.
func execOK(ctx context.Context, bc *client.Client, name string, timeout time.Duration, command string) error {
	res, err := bc.Exec(ctx, name, command, timeout)
	if err != nil {
		return err
	}
	if res.Error != "" {
		return errors.New(res.Error)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}

// logTail prints the end of the executor's log after a failure -- the
// first place to look when the environment never connects.
func logTail(ctx context.Context, bc *client.Client, name string) {
	if res, err := bc.Exec(ctx, name, "systemctl status --no-pager codex-exec-server 2>&1 | head -5; journalctl -u codex-exec-server -n 20 --no-pager", 30*time.Second); err == nil {
		fmt.Fprintf(os.Stderr, "--- executor log ---\n%s", res.Stdout)
	}
}

// shellQuote single-quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
