package headless

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"tarakan-client/internal/agent"
	repoctx "tarakan-client/internal/context"
	"tarakan-client/internal/reviewdoc"
)

type Event struct {
	Type       string          `json:"type"`
	Repository *repoctx.Info   `json:"repository,omitempty"`
	Agent      *agent.Provider `json:"agent,omitempty"`
	Content    string          `json:"content,omitempty"`
	Error      string          `json:"error,omitempty"`
}

func Run(ctx context.Context, output io.Writer, repository repoctx.Info, provider agent.Provider, prompt string) error {
	encoder := json.NewEncoder(output)
	if err := encoder.Encode(Event{Type: "session.started", Repository: &repository, Agent: &provider}); err != nil {
		return err
	}

	content, err := agent.Run(ctx, provider, agent.Request{
		Prompt:    reviewdoc.FreeformPrompt(prompt),
		Directory: repository.Root,
	})
	if err != nil {
		encodeErr := encoder.Encode(Event{Type: "session.error", Agent: &provider, Content: content, Error: err.Error()})
		if encodeErr != nil {
			return encodeErr
		}
		return err
	}
	if err := encoder.Encode(Event{Type: "session.completed", Agent: &provider, Content: content}); err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	return nil
}
