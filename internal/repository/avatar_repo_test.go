package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"goph-profile/internal/domain"
)

func newMockRepo(t *testing.T) (AvatarRepository, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	t.Cleanup(mock.Close)
	return NewAvatarRepository(mock), mock
}

func TestCreate_InsertsAvatar(t *testing.T) {
	repo, mock := newMockRepo(t)
	now := time.Now()
	avatar := &domain.Avatar{
		ID: "11111111-1111-1111-1111-111111111111", UserID: "user-1",
		FileName: "photo.png", MimeType: "image/png", SizeBytes: 100,
		Width: 1, Height: 1, S3Key: "avatars/11111111-1111-1111-1111-111111111111/original.png",
		Status: domain.StatusPending,
	}

	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO avatars`)).
		WithArgs(avatar.ID, avatar.UserID, avatar.FileName, avatar.MimeType, avatar.SizeBytes,
			avatar.Width, avatar.Height, avatar.S3Key, []byte("[]"), domain.StatusPending).
		WillReturnRows(pgxmock.NewRows([]string{"created_at", "updated_at"}).
			AddRow(now, now))

	require.NoError(t, repo.Create(context.Background(), avatar))
	require.Equal(t, now, avatar.CreatedAt)
	require.Equal(t, now, avatar.UpdatedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetByID_NotFound(t *testing.T) {
	repo, mock := newMockRepo(t)

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT`)).
		WithArgs("missing").
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id", "file_name", "mime_type",
			"size_bytes", "width", "height", "s3_key", "thumbnail_s3_keys",
			"processing_status", "created_at", "updated_at"}))

	_, err := repo.GetByID(context.Background(), "missing")
	require.ErrorIs(t, err, domain.ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetByID_ScansAvatar(t *testing.T) {
	repo, mock := newMockRepo(t)
	now := time.Now()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT`)).
		WithArgs("a1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id", "file_name", "mime_type",
			"size_bytes", "width", "height", "s3_key", "thumbnail_s3_keys",
			"processing_status", "created_at", "updated_at"}).
			AddRow("a1", "user-1", "photo.jpg", "image/jpeg", int64(100), 10, 20,
				"avatars/a1/original.jpg", []byte(`[{"size":"100x100","key":"thumbnails/a1/100x100.jpg"}]`),
				domain.StatusReady, now, now))

	avatar, err := repo.GetByID(context.Background(), "a1")
	require.NoError(t, err)
	require.Equal(t, "a1", avatar.ID)
	require.Equal(t, domain.StatusReady, avatar.Status)
	require.Equal(t, 10, avatar.Width)
	require.Len(t, avatar.Thumbnails, 1)
	require.Equal(t, "100x100", avatar.Thumbnails[0].Size)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSoftDelete_MarksDeleted(t *testing.T) {
	repo, mock := newMockRepo(t)

	mock.ExpectExec(regexp.QuoteMeta(`UPDATE avatars`)).
		WithArgs("a1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	require.NoError(t, repo.SoftDelete(context.Background(), "a1"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSoftDelete_NotFound(t *testing.T) {
	repo, mock := newMockRepo(t)

	mock.ExpectExec(regexp.QuoteMeta(`UPDATE avatars`)).
		WithArgs("missing").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	require.ErrorIs(t, repo.SoftDelete(context.Background(), "missing"), domain.ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateStatus(t *testing.T) {
	repo, mock := newMockRepo(t)

	mock.ExpectExec(regexp.QuoteMeta(`UPDATE avatars`)).
		WithArgs("a1", domain.StatusProcessing).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	require.NoError(t, repo.UpdateStatus(context.Background(), "a1", domain.StatusProcessing))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateStatusAndThumbnails(t *testing.T) {
	repo, mock := newMockRepo(t)
	thumbnails := []domain.Thumbnail{
		{Size: "100x100", Key: "thumbnails/a1/100x100.jpg"},
	}

	mock.ExpectExec(regexp.QuoteMeta(`UPDATE avatars`)).
		WithArgs("a1", domain.StatusReady,
			[]byte(`[{"size":"100x100","key":"thumbnails/a1/100x100.jpg"}]`)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	require.NoError(t, repo.UpdateStatusAndThumbnails(context.Background(), "a1",
		domain.StatusReady, thumbnails))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMarkEventProcessed_NewEvent(t *testing.T) {
	repo, mock := newMockRepo(t)
	msgID := "22222222-2222-2222-2222-222222222222"

	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO event_dedup`)).
		WithArgs(msgID).
		WillReturnRows(pgxmock.NewRows([]string{"message_id"}).AddRow(msgID))

	processed, err := repo.MarkEventProcessed(context.Background(), msgID)
	require.NoError(t, err)
	require.True(t, processed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMarkEventProcessed_DuplicateEvent(t *testing.T) {
	repo, mock := newMockRepo(t)
	msgID := "22222222-2222-2222-2222-222222222222"

	// INSERT ... ON CONFLICT DO NOTHING не возвращает строку для дубликата.
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO event_dedup`)).
		WithArgs(msgID).
		WillReturnRows(pgxmock.NewRows([]string{"message_id"}))

	processed, err := repo.MarkEventProcessed(context.Background(), msgID)
	require.NoError(t, err)
	require.False(t, processed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetCurrentByUser_Found(t *testing.T) {
	repo, mock := newMockRepo(t)
	now := time.Now()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT`)).
		WithArgs("user-1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id", "file_name", "mime_type",
			"size_bytes", "width", "height", "s3_key", "thumbnail_s3_keys",
			"processing_status", "created_at", "updated_at"}).
			AddRow("a2", "user-1", "p.jpg", "image/jpeg", int64(10), 0, 0,
				"avatars/a2/original.jpg", []byte("[]"), domain.StatusPending, now, now))

	avatar, err := repo.GetCurrentByUser(context.Background(), "user-1")
	require.NoError(t, err)
	require.Equal(t, "a2", avatar.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetCurrentByUser_NotFound(t *testing.T) {
	repo, mock := newMockRepo(t)

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT`)).
		WithArgs("nobody").
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id", "file_name", "mime_type",
			"size_bytes", "width", "height", "s3_key", "thumbnail_s3_keys",
			"processing_status", "created_at", "updated_at"}))

	_, err := repo.GetCurrentByUser(context.Background(), "nobody")
	require.ErrorIs(t, err, domain.ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestListByUser_ReturnsAvatars(t *testing.T) {
	repo, mock := newMockRepo(t)
	now := time.Now()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT`)).
		WithArgs("user-1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id", "file_name", "mime_type",
			"size_bytes", "width", "height", "s3_key", "thumbnail_s3_keys",
			"processing_status", "created_at", "updated_at"}).
			AddRow("a1", "user-1", "p1.jpg", "image/jpeg", int64(1), 0, 0,
				"avatars/a1/original.jpg", []byte("[]"), domain.StatusReady, now, now).
			AddRow("a2", "user-1", "p2.jpg", "image/jpeg", int64(2), 0, 0,
				"avatars/a2/original.jpg", []byte("[]"), domain.StatusPending, now, now))

	avatars, err := repo.ListByUser(context.Background(), "user-1")
	require.NoError(t, err)
	require.Len(t, avatars, 2)
	require.Equal(t, "a1", avatars[0].ID)
	require.Equal(t, domain.StatusReady, avatars[0].Status)
	require.Equal(t, domain.StatusPending, avatars[1].Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPing(t *testing.T) {
	repo, mock := newMockRepo(t)
	mock.ExpectPing()
	require.NoError(t, repo.Ping(context.Background()))
	require.NoError(t, mock.ExpectationsWereMet())
}
