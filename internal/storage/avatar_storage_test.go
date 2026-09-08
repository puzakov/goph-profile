package storage

import (
	"errors"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/require"
)

func TestMapMinioError_NoSuchKey(t *testing.T) {
	err := mapMinioError(minio.ErrorResponse{Code: "NoSuchKey"})
	require.ErrorIs(t, err, ErrObjectNotFound)
}

func TestMapMinioError_OtherErrorPassedThrough(t *testing.T) {
	original := errors.New("connection refused")
	err := mapMinioError(original)
	require.ErrorIs(t, err, original)
	require.NotErrorIs(t, err, ErrObjectNotFound)
}

func TestMapMinioError_Nil(t *testing.T) {
	require.NoError(t, mapMinioError(nil))
}
