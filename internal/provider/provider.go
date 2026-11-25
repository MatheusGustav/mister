// Package provider defines the model-provider seam: neutral message types and
// the interface every model backend implements.
package provider

import (
	"context"
	"encoding/json"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type BlockType string

const (
	BlockText       BlockType = "text"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
	// BlockOpaque is a provider-native block (thinking, server tool use and its
	// result) that only the provider that produced it can read.
	BlockOpaque BlockType = "opaque"
)

// Image is a picture a tool hands back to the model.
type Image struct {
	// Label says what the picture shows; the model reads it right before the picture.
	Label     string `json:"label,omitempty"`
	MediaType string `json:"media_type"`
	Data      []byte `json:"data"`
}

// Block is one piece of a message. Text carries the text of a text block and
// the content of a tool result.
type Block struct {
	Type      BlockType       `json:"type"`
	Text      string          `json:"text,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	// Images are the pictures of a tool result, after its text.
	Images []Image `json:"images,omitempty"`
	// Raw is the provider-native form of an assistant block. The provider named
	// in Message.Provider replays it byte for byte.
	Raw json.RawMessage `json:"raw,omitempty"`
}

type Message struct {
	Role     Role    `json:"role"`
	Blocks   []Block `json:"blocks"`
	Provider string  `json:"provider,omitempty"`
}

func UserText(text string) Message {
	return Message{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: text}}}
}

func ToolResult(toolUseID, content string, isError bool) Block {
	return Block{Type: BlockToolResult, ToolUseID: toolUseID, Text: content, IsError: isError}
}

// Text joins the text blocks of the message.
func (m Message) Text() string {
	var out string
	for _, b := range m.Blocks {
		if b.Type == BlockText {
			out += b.Text
		}
	}
	return out
}

func (m Message) ToolUses() []Block {
	var out []Block
	for _, b := range m.Blocks {
		if b.Type == BlockToolUse {
			out = append(out, b)
		}
	}
	return out
}

// Size approximates the bytes the message holds in memory.
func (m Message) Size() int {
	n := 0
	for _, b := range m.Blocks {
		n += len(b.Text) + len(b.Input) + len(b.Raw) + len(b.ToolUseID) + len(b.ToolName)
		for _, img := range b.Images {
			n += len(img.Label) + len(img.Data)
		}
	}
	return n
}

// ToolSpec is the definition of a client-side tool as the model sees it.
type ToolSpec struct {
	Name        string
	Description string
	// Schema is a JSON Schema object ("type": "object").
	Schema map[string]any
}

type WebSearch struct {
	MaxUses int
}

type Request struct {
	Model     string
	Effort    string
	MaxTokens int
	// System and Tools form the fixed prefix of a session: they must not change
	// between requests of the same agent, and Tools must come sorted by name.
	System string
	Tools  []ToolSpec
	// WebSearch asks for the provider's native web search. Ignored by providers
	// without the capability.
	WebSearch *WebSearch
	Messages  []Message
	// OnText receives the text of the answer as it is generated.
	OnText func(delta string)
}

type StopReason string

const (
	StopEndTurn   StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	// StopPause means the provider paused a server-side tool loop: send the
	// conversation again, unchanged, to resume.
	StopPause   StopReason = "pause_turn"
	StopRefusal StopReason = "refusal"
	StopOther   StopReason = "other"
)

type Usage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

// ContextTokens is the size of the prompt the request sent.
func (u Usage) ContextTokens() int64 {
	return u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens
}

type Response struct {
	Message Message
	Stop    StopReason
	// StopDetail explains a refusal, when the provider says why.
	StopDetail string
	Usage      Usage
}

type Capabilities struct {
	NativeWebSearch bool
}

type Provider interface {
	Name() string
	Capabilities() Capabilities
	Complete(ctx context.Context, req Request) (*Response, error)
}
