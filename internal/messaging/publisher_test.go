package messaging

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRetryDelay_ExponentialBackoff(t *testing.T) {
	cases := []struct {
		retryCount int
		wantOK     bool
		wantDelay  string
	}{
		{0, true, "1s"},
		{1, true, "2s"},
		{2, true, "4s"},
		{3, true, "8s"},
		{4, true, "16s"},
		{5, false, ""},
		{-1, false, ""},
	}
	for _, c := range cases {
		delay, ok := RetryDelay(c.retryCount)
		require.Equal(t, c.wantOK, ok, "retryCount=%d", c.retryCount)
		if ok {
			require.Equal(t, c.wantDelay, delay.String(), "retryCount=%d", c.retryCount)
		}
	}
}

func TestNewMessageID_IsUnique(t *testing.T) {
	require.NotEqual(t, newMessageID(), newMessageID())
}
