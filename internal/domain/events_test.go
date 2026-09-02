package domain

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAvatarUploadEvent_JSONRoundTrip(t *testing.T) {
	event := AvatarUploadEvent{
		AvatarID: "a1",
		UserID:   "user-1",
		S3Key:    "avatars/a1/original.png",
	}

	data, err := json.Marshal(event)
	require.NoError(t, err)
	require.JSONEq(t, `{"avatar_id":"a1","user_id":"user-1","s3_key":"avatars/a1/original.png"}`,
		string(data))

	var decoded AvatarUploadEvent
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, event, decoded)
}

func TestAvatarProcessEvent_JSONRoundTrip(t *testing.T) {
	event := AvatarProcessEvent{
		AvatarID:   "a1",
		Operations: []ProcessingOp{OpResize100, OpResize300},
	}

	data, err := json.Marshal(event)
	require.NoError(t, err)
	require.JSONEq(t,
		`{"avatar_id":"a1","operations":["resize_100x100","resize_300x300"]}`,
		string(data))

	var decoded AvatarProcessEvent
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, event, decoded)
}

func TestAvatarDeleteEvent_JSONRoundTrip(t *testing.T) {
	event := AvatarDeleteEvent{
		AvatarID: "a1",
		S3Keys:   []string{"avatars/a1/original.png", "thumbnails/a1/100x100.jpg"},
	}

	data, err := json.Marshal(event)
	require.NoError(t, err)
	require.JSONEq(t,
		`{"avatar_id":"a1","s3_keys":["avatars/a1/original.png","thumbnails/a1/100x100.jpg"]}`,
		string(data))

	var decoded AvatarDeleteEvent
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, event, decoded)
}

func TestSupportedMimeTypes(t *testing.T) {
	for _, mime := range []string{"image/jpeg", "image/png", "image/webp"} {
		if _, ok := SupportedMimeTypes[mime]; !ok {
			t.Errorf("mime %s should be supported", mime)
		}
	}
	if _, ok := SupportedMimeTypes["image/gif"]; ok {
		t.Error("gif не должен быть в списке поддерживаемых")
	}
}
