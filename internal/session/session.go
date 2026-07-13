package session

import "time"

type Role string

const (
	RoleSystem Role = "system"
	RoleUser   Role = "user"
	RoleAgent  Role = "agent"
)

type Message struct {
	Role      Role      `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type Transcript struct {
	messages []Message
}

func (t *Transcript) Append(role Role, content string) {
	t.messages = append(t.messages, Message{
		Role:      role,
		Content:   content,
		CreatedAt: time.Now(),
	})
}

func (t *Transcript) Clear() {
	t.messages = nil
}

func (t Transcript) Messages() []Message {
	return append([]Message(nil), t.messages...)
}
