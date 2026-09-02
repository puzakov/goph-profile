package domain

// Routing keys событий брокера сообщений.
const (
	RoutingKeyUpload  = "avatar.uploaded"
	RoutingKeyProcess = "avatar.process"
	RoutingKeyDelete  = "avatar.deleted"
)

// ProcessingOp — операция обработки изображения.
type ProcessingOp string

// Поддерживаемые операции обработки.
const (
	OpResize100 ProcessingOp = "resize_100x100" // миниатюра 100x100
	OpResize300 ProcessingOp = "resize_300x300" // миниатюра 300x300
)

// AvatarUploadEvent публикуется сервером после успешной загрузки оригинала.
// Воркер по нему инициирует конвейер обработки.
type AvatarUploadEvent struct {
	AvatarID string `json:"avatar_id"`
	UserID   string `json:"user_id"`
	S3Key    string `json:"s3_key"`
}

// AvatarProcessEvent публикуется воркером после AvatarUploadEvent
// и содержит операции, которые нужно выполнить над изображением.
type AvatarProcessEvent struct {
	AvatarID   string         `json:"avatar_id"`
	Operations []ProcessingOp `json:"operations"`
}

// AvatarDeleteEvent публикуется сервером при удалении аватарки.
// Воркер удаляет перечисленные ключи из S3.
type AvatarDeleteEvent struct {
	AvatarID string   `json:"avatar_id"`
	S3Keys   []string `json:"s3_keys"`
}
