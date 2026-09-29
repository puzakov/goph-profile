package telemetry

// Имена сервисов в трейсах — единственная точка, где они заданы.
//
// Значение уходит в атрибут service.name ресурса (по нему Jaeger группирует
// спаны в сервис), в имя инструмента HTTP-слоя и в трейсеры самого процесса.
const (
	// ServiceServer — HTTP-сервер (cmd/server).
	ServiceServer = "avatar-server"
	// ServiceWorker — воркер фоновой обработки (cmd/worker).
	ServiceWorker = "avatar-worker"
)
