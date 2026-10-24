package server

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBufferChatCompletionFromSSEMetadata(t *testing.T) {
	stream := bufferedChatTestStream(`{"type":"response.completed","response":{"id":"resp_final","model":"gpt-5.5","created_at":1760000000.0,"output":[{"type":"message","content":[{"type":"output_text","text":"Final text"}]}]}}`)
	response, err := bufferChatCompletionFromSSE(strings.NewReader(stream), "gpt-5")
	require.NoError(t, err)
	assert.Equal(t, "chatcmpl-resp_final", response.ID)
	assert.Equal(t, "gpt-5.5", response.Model)
	assert.Equal(t, int64(1760000000), response.Created)
	assert.Equal(t, "stop", response.Choices[0].FinishReason)
	assert.Nil(t, response.Choices[0].Message.ToolCalls)
}

func TestBufferChatCompletionFromSSEEmptyFinalOutputKeepsDeltas(t *testing.T) {
	stream := bufferedChatTestStream(`{"type":"response.completed","response":{"output":[]}}`,
		`{"type":"response.output_item.added","item":{"id":"fc_ping","type":"function_call","name":"ping","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_ping","delta":"{}"}`,
	)
	response, err := bufferChatCompletionFromSSE(strings.NewReader(stream), "gpt-5")
	require.NoError(t, err)
	require.Len(t, response.Choices[0].Message.ToolCalls, 1)
	assert.Equal(t, "call_fc_ping", response.Choices[0].Message.ToolCalls[0].ID)
	assert.Equal(t, "{}", response.Choices[0].Message.ToolCalls[0].Function.Arguments)
}

func TestBufferChatCompletionFromSSEFraming(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream string
	}{
		{
			name: "event names with CRLF and comments",
			stream: ": heartbeat\r\nevent: response.output_text.delta\r\ndata: {\"delta\":\"Hello\"}\r\n\r\n" +
				"event: response.completed\r\ndata: {}\r\n\r\n",
		},
		{
			name: "multiline data",
			stream: "data: {\"type\":\"response.output_text.delta\",\n" +
				"data: \"delta\":\"Hello\"}\n\n" +
				"data: " + bufferedChatCompleted + "\n\n",
		},
		{
			name: "final event without trailing newline",
			stream: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\n" +
				"data: " + bufferedChatCompleted,
		},
		{
			name: "ignore events after the terminal response",
			stream: bufferedChatTestStream(bufferedChatCompleted, `{"type":"response.output_text.delta","delta":"Hello"}`) +
				"data: malformed trailing event\n\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := bufferChatCompletionFromSSE(strings.NewReader(tc.stream), "gpt-5")
			require.NoError(t, err)
			assert.Equal(t, "Hello", extractCompletionText(response))
			assert.Equal(t, "stop", response.Choices[0].FinishReason)
		})
	}
}
