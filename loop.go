package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"time"
)

// loopConfig bundles the knobs that control how runLoop behaves. All come
// from flags or env vars in main.go.
type loopConfig struct {
	selfSrc  string
	maxSteps int
	sleep    time.Duration
	oneShot  bool
	selfMod  bool
}

// runLoop is the heart of the agent: build messages, ask the LLM for one tool
// call, dispatch it, log everything, repeat. The loop exits when a terminal
// tool (done / fail / edit_self) is called, when --once is set, when maxSteps
// is reached, or when the context is canceled.
func runLoop(ctx context.Context, llm *LLM, logger *EventLog, cfg loopConfig) (outcome, newBin string, err error) {
	sys := RenderSystemPrompt(cfg.selfSrc, runtime.GOOS, cfg.selfMod)
	msgs := []Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: firstUserPrompt()},
	}

	for step := 0; step < cfg.maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			logger.Write("canceled", map[string]any{"step": step, "err": err.Error()})
			return "canceled", "", nil
		}

		logger.Write("step_start", map[string]any{"step": step})

		call, raw, err := llm.ChatTool(ctx, msgs)
		if err != nil {
			logger.Write("llm_error", map[string]any{"step": step, "err": err.Error(), "raw": truncate(raw, 2000)})
			return "fail", "", fmt.Errorf("llm step %d: %w", step, err)
		}
		logger.Write("llm_response", map[string]any{"step": step, "raw": truncate(raw, 2000)})
		logger.Write("tool_call", map[string]any{"step": step, "tool": call.Tool, "args": string(call.Args)})

		res := dispatch(ctx, call, cfg, logger)
		logger.Write("tool_result", map[string]any{
			"step":     step,
			"tool":     call.Tool,
			"terminal": res.Terminal,
			"err":      errString(res.Err),
			"result":   truncate(res.Result, 4000),
		})

		// Append the model's raw reply and our tool_result as the next user turn.
		msgs = append(msgs,
			Message{Role: "assistant", Content: raw},
			Message{Role: "user", Content: formatToolResult(call.Tool, res)},
		)

		switch res.Terminal {
		case "handoff":
			return "handoff", res.NewBin, nil
		case "done":
			return "done", "", nil
		case "fail":
			return "fail", "", nil
		}

		slog.Info("step ok", "step", step, "tool", call.Tool)

		if cfg.oneShot {
			return "once", "", nil
		}
		if cfg.sleep > 0 {
			select {
			case <-ctx.Done():
				return "canceled", "", nil
			case <-time.After(cfg.sleep):
			}
		}
	}

	logger.Write("max_steps", map[string]any{"max_steps": cfg.maxSteps})
	return "max_steps", "", nil
}

func firstUserPrompt() string {
	return "Begin. Call list_self to survey the current source tree, then read a few files with read_self, then propose ONE small, compiling improvement toward the target system via edit_self. Keep the diff tight."
}

func formatToolResult(tool string, res ToolResult) string {
	// We only return plain strings to the model — no JSON wrapping — so it's
	// obvious in the transcript what it's reading.
	head := "tool_result[" + tool + "]"
	if res.Err != nil {
		return head + " error: " + res.Err.Error()
	}
	return head + ":\n" + res.Result
}

func errString(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}

// truncate cuts s to at most n bytes, appending a marker. Safe for non-UTF8.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n < 16 {
		return s[:n]
	}
	return s[:n-16] + fmt.Sprintf("...[+%dB truncated]", len(s)-n+16)
}

// rawJSON is a tiny helper for logging raw JSON payloads without double-encoding.
func rawJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%q", fmt.Sprint(v))
	}
	return string(b)
}
