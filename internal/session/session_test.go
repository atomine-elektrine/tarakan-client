package session

import "testing"

func TestTranscriptAppendAndClear(t *testing.T) {
	var transcript Transcript
	transcript.Append(RoleUser, "scan authentication")

	messages := transcript.Messages()
	if len(messages) != 1 || messages[0].Content != "scan authentication" {
		t.Fatalf("unexpected messages: %#v", messages)
	}

	messages[0].Content = "changed"
	if transcript.Messages()[0].Content != "scan authentication" {
		t.Fatal("Messages returned the transcript's backing slice")
	}

	transcript.Clear()
	if len(transcript.Messages()) != 0 {
		t.Fatal("Clear did not empty transcript")
	}
}
