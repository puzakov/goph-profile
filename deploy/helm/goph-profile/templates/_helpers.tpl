{{/*
Базовое имя релиза. Если релиз назван как чарт (helm install goph-profile ...),
префикс не дублируется: goph-profile, а не goph-profile-goph-profile.
*/}}
{{- define "goph-profile.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "goph-profile.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "goph-profile.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* ------------------------------------------------------------------ */}}
{{/* Имена компонентов и их метки.                                      */}}
{{/*                                                                    */}}
{{/* Каждый компонент (server, worker, postgres, ...) получает          */}}
{{/* собственную пару меток name+instance: она уникальна внутри релиза   */}}
{{/* и годится как селектор Deployment'а, сервиса и ServiceMonitor'а.    */}}
{{/* Строки подключения собираются из тех же имён, поэтому смена имени   */}}
{{/* релиза не разрывает связи между компонентами.                      */}}
{{/*                                                                    */}}
{{/* Вызов: include "goph-profile.componentName" (dict "root" . "component" "server") */}}
{{/* ------------------------------------------------------------------ */}}

{{- define "goph-profile.componentName" -}}
{{- printf "%s-%s" (include "goph-profile.fullname" .root) .component | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Полный набор меток объекта. */}}
{{- define "goph-profile.componentLabels" -}}
helm.sh/chart: {{ include "goph-profile.chart" .root }}
app.kubernetes.io/name: {{ include "goph-profile.componentName" . }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
app.kubernetes.io/version: {{ .root.Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
app.kubernetes.io/part-of: goph-profile
{{- end }}

{{/*
Метки селектора. Только стабильные значения: version и chart меняются
при обновлении релиза, а selector у Deployment неизменяем.
*/}}
{{- define "goph-profile.componentSelectorLabels" -}}
app.kubernetes.io/name: {{ include "goph-profile.componentName" . }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
{{- end }}

{{/* ------------------------------------------------------------------ */}}
{{/* Строки подключения.                                                */}}
{{/* Явное значение в values перекрывает сборку из имён сервисов: так    */}}
{{/* стенд подключается к внешним БД, брокеру и S3 (values-prod).        */}}
{{/* ------------------------------------------------------------------ */}}

{{- define "goph-profile.databaseUrl" -}}
{{- if .Values.secrets.databaseUrl }}
{{- .Values.secrets.databaseUrl }}
{{- else }}
{{- printf "postgres://%s:%s@%s:5432/%s?sslmode=disable"
      .Values.secrets.postgresUser .Values.secrets.postgresPassword
      (include "goph-profile.componentName" (dict "root" . "component" "postgres"))
      .Values.secrets.postgresDb }}
{{- end }}
{{- end }}

{{- define "goph-profile.rabbitmqUrl" -}}
{{- if .Values.secrets.rabbitmqUrl }}
{{- .Values.secrets.rabbitmqUrl }}
{{- else }}
{{- printf "amqp://%s:%s@%s:5672/"
      .Values.secrets.rabbitmqDefaultUser .Values.secrets.rabbitmqDefaultPassword
      (include "goph-profile.componentName" (dict "root" . "component" "rabbitmq")) }}
{{- end }}
{{- end }}

{{- define "goph-profile.s3Endpoint" -}}
{{- if .Values.config.s3.endpoint }}
{{- .Values.config.s3.endpoint }}
{{- else }}
{{- printf "%s:9000" (include "goph-profile.componentName" (dict "root" . "component" "minio")) }}
{{- end }}
{{- end }}

{{- define "goph-profile.rabbitmqManagementUrl" -}}
{{- if .Values.config.rabbitmqManagement.url }}
{{- .Values.config.rabbitmqManagement.url }}
{{- else }}
{{- printf "http://%s:15672" (include "goph-profile.componentName" (dict "root" . "component" "rabbitmq")) }}
{{- end }}
{{- end }}

{{/*
Имя service account: явное из values или собранное из имени релиза.
Одно на все компоненты — права у них одинаковые (никаких).
*/}}
{{- define "goph-profile.serviceAccountName" -}}
{{- if .Values.serviceAccount.name }}
{{- .Values.serviceAccount.name }}
{{- else }}
{{- include "goph-profile.componentName" (dict "root" . "component" "sa") }}
{{- end }}
{{- end }}

{{/* Образ приложения: один для server, worker и Job'а миграций. */}}
{{- define "goph-profile.image" -}}
{{- printf "%s:%s" .Values.image.repository (.Values.image.tag | toString) }}
{{- end }}

{{/*
Секретные переменные приложения — общий блок для server и worker.
Вынесен в шаблон, чтобы список не разъезжался между двумя Deployment'ами
и Job'ом миграций.
*/}}
{{- define "goph-profile.appSecretEnv" -}}
- name: DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ include "goph-profile.fullname" . }}-secret
      key: DATABASE_URL
- name: RABBITMQ_URL
  valueFrom:
    secretKeyRef:
      name: {{ include "goph-profile.fullname" . }}-secret
      key: RABBITMQ_URL
- name: S3_ACCESS_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "goph-profile.fullname" . }}-secret
      key: S3_ACCESS_KEY
- name: S3_SECRET_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "goph-profile.fullname" . }}-secret
      key: S3_SECRET_KEY
{{- end }}

{{/*
Контекст безопасности приложения: процесс без привилегий.
Одинаков для server, worker и Job'а миграций — образ один, требования те же.

Разделён на два уровня намеренно: allowPrivilegeEscalation и capabilities
описаны в схеме только у контейнера, а не у пода. В PodSecurityContext они
не проходят валидацию API-сервера, и это видно лишь при применении —
ни helm lint, ни helm template такого не ловят.
*/}}

{{/* Уровень пода: пользователь и профиль seccomp. */}}
{{- define "goph-profile.appPodSecurityContext" -}}
runAsNonRoot: true
runAsUser: 65534
runAsGroup: 65534
seccompProfile:
  type: RuntimeDefault
{{- end }}

{{/* Уровень контейнера: запрет эскалации привилегий и сброс capabilities. */}}
{{- define "goph-profile.appContainerSecurityContext" -}}
allowPrivilegeEscalation: false
capabilities:
  drop: [ALL]
{{- end }}
