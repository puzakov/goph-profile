// Package storage реализует работу с S3-совместимым хранилищем файлов (MinIO).
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ErrObjectNotFound — объект не найден в хранилище.
var ErrObjectNotFound = errors.New("object not found")

// ObjectInfo — метаданные объекта из хранилища.
type ObjectInfo struct {
	ETag        string
	Size        int64
	ContentType string
}

// AvatarStorage — S3-совместимое хранилище файлов аватарок.
type AvatarStorage interface {
	EnsureBucket(ctx context.Context) error
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
	Delete(ctx context.Context, key string) error
	Ping(ctx context.Context) error
}

type minioStorage struct {
	client *minio.Client
	bucket string
	region string
}

// NewMinioStorage создаёт хранилище на базе MinIO (совместимо с AWS S3).
func NewMinioStorage(endpoint, accessKey, secretKey, bucket, region string, useSSL bool) (AvatarStorage, error) {
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
		Region: region,
	})
	if err != nil {
		return nil, fmt.Errorf("create minio client: %w", err)
	}
	return &minioStorage{client: client, bucket: bucket, region: region}, nil
}

// EnsureBucket создаёт бакет, если его ещё нет. Идемпотентна.
func (s *minioStorage) EnsureBucket(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check bucket %s: %w", s.bucket, err)
	}
	if exists {
		return nil
	}
	if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: s.region}); err != nil {
		return fmt.Errorf("create bucket %s: %w", s.bucket, err)
	}
	return nil
}

func (s *minioStorage) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, r, size,
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("put object %s: %w", key, err)
	}
	return nil
}

func (s *minioStorage) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, ObjectInfo{}, mapMinioError(err)
	}
	stat, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		return nil, ObjectInfo{}, mapMinioError(err)
	}
	return obj, ObjectInfo{ETag: stat.ETag, Size: stat.Size, ContentType: stat.ContentType}, nil
}

func (s *minioStorage) Delete(ctx context.Context, key string) error {
	err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
	if err != nil {
		return mapMinioError(err)
	}
	return nil
}

func (s *minioStorage) Ping(ctx context.Context) error {
	_, err := s.client.BucketExists(ctx, s.bucket)
	return err
}

// mapMinioError преобразует ошибки MinIO в ошибки пакета.
func mapMinioError(err error) error {
	if err == nil {
		return nil
	}
	var resp minio.ErrorResponse
	if errors.As(err, &resp) && resp.Code == "NoSuchKey" {
		return ErrObjectNotFound
	}
	return err
}
