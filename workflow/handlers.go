package workflow

import (
	"context"
	"encoding/json"
	"log"
	"time"
)

type DummyInput struct {
	Message string `json:"message"`
	DelayMs int    `json:"delay_ms,omitempty"`
}

type DummyOutput struct {
	Message   string `json:"message"`
	Processed bool   `json:"processed"`
	Timestamp int64  `json:"timestamp"`
}

func DummyHandler(ctx context.Context, input []byte) ([]byte, error) {
	var in DummyInput
	if len(input) > 0 {
		json.Unmarshal(input, &in)
	}

	if in.DelayMs > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(in.DelayMs) * time.Millisecond):
		}
	}

	log.Printf("[DummyHandler] Processing: %s", in.Message)

	out := DummyOutput{
		Message:   "processed: " + in.Message,
		Processed: true,
		Timestamp: time.Now().UnixMilli(),
	}

	return json.Marshal(out)
}

func NoOpHandler(ctx context.Context, input []byte) ([]byte, error) {
	return []byte(`{"ok":true}`), nil
}

func EchoHandler(ctx context.Context, input []byte) ([]byte, error) {
	return input, nil
}

type DelayConfig struct {
	DelayMs int `json:"delay_ms"`
}

func DelayHandler(ctx context.Context, input []byte) ([]byte, error) {
	var cfg DelayConfig
	if len(input) > 0 {
		json.Unmarshal(input, &cfg)
	}

	if cfg.DelayMs <= 0 {
		cfg.DelayMs = 10
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Duration(cfg.DelayMs) * time.Millisecond):
	}

	return json.Marshal(map[string]interface{}{
		"delayed_ms": cfg.DelayMs,
		"timestamp":  time.Now().UnixMilli(),
	})
}

func CPUWorkHandler(ctx context.Context, input []byte) ([]byte, error) {
	sum := 0
	for i := 0; i < 10000; i++ {
		sum += i
	}

	return json.Marshal(map[string]interface{}{
		"result":    sum,
		"timestamp": time.Now().UnixMilli(),
	})
}

type FailConfig struct {
	Message string `json:"message"`
}

func FailHandler(ctx context.Context, input []byte) ([]byte, error) {
	var cfg FailConfig
	if len(input) > 0 {
		json.Unmarshal(input, &cfg)
	}
	if cfg.Message == "" {
		cfg.Message = "intentional failure for testing"
	}
	return nil, &TaskError{Message: cfg.Message}
}

type TaskError struct {
	Message string
}

func (e *TaskError) Error() string {
	return e.Message
}