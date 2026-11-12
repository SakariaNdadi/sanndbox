package main

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

//go:embed templates/index.html docs/swagger.json
var templateFS embed.FS

// @title Telegram MTProto & Bot API Sanndbox
// @version 1.0.0
// @description HTTP (:9084) Telegram Sanndbox evaluating official core.telegram.org/methods (messages.sendMessage, messages.sendMedia, messages.sendMultiMedia, messages.forwardMessages, messages.editMessage, messages.deleteMessages, messages.sendReaction, messages.sendVote, messages.getHistory, channels.readHistory).
// @BasePath /

type EvaluationStatus string

const (
	StatusPass EvaluationStatus = "PASS"
	StatusWarn EvaluationStatus = "WARN"
	StatusFail EvaluationStatus = "FAIL"
)

type EvaluationCriterion struct {
	Category string           `json:"category"`
	Target   string           `json:"target"`
	Status   EvaluationStatus `json:"status"`
	Details  string           `json:"details"`
}

type TelegramEvaluationReport struct {
	OverallStatus  EvaluationStatus      `json:"overallStatus"`
	Method         string                `json:"method"`
	TLReturnType   string                `json:"tlReturnType"`
	CharacterCount int                   `json:"characterCount"`
	Criteria       []EvaluationCriterion `json:"criteria"`
}

type TelegramMediaDescriptor struct {
	TLType     string `json:"tlType"`
	Filename   string `json:"filename"`
	MIMEType   string `json:"mimeType"`
	SizeBytes  int64  `json:"sizeBytes"`
	PreviewURL string `json:"previewUrl,omitempty"`
}

type TelegramMethodRequest struct {
	SessionID string                   `json:"sessionId,omitempty"`
	Method    string                   `json:"method"`
	Peer      string                   `json:"peer"`
	Sender    string                   `json:"sender"`
	RandomID  int64                    `json:"randomId,omitempty"`
	MessageID int64                    `json:"messageId,omitempty"`
	Message   string                   `json:"message"`
	Reaction  string                   `json:"reaction,omitempty"`
	Media     *TelegramMediaDescriptor `json:"media,omitempty"`
}

type TelegramRecord struct {
	ID        string                   `json:"id"`
	SessionID string                   `json:"sessionId,omitempty"`
	Timestamp string                   `json:"timestamp"`
	Source    string                   `json:"source"`
	Method    string                   `json:"method"`
	Peer      string                   `json:"peer"`
	Sender    string                   `json:"sender"`
	RandomID  int64                    `json:"randomId,omitempty"`
	MessageID int64                    `json:"messageId,omitempty"`
	Message   string                   `json:"message"`
	Reaction  string                   `json:"reaction,omitempty"`
	Media     *TelegramMediaDescriptor `json:"media,omitempty"`
	Report    TelegramEvaluationReport `json:"report"`
}

type TelegramMethodSpec struct {
	ReturnType      string
	Description     string
	RequiresPeer    bool
	RequiresRandom  bool
	RequiresMessage bool
	RequiresMedia   bool
	RequiresMsgID   bool
}

var officialTelegramMethods = map[string]TelegramMethodSpec{
	"messages.sendMessage": {
		ReturnType:      "Updates",
		Description:     "Sends a text message to a peer (chat, user, or channel)",
		RequiresPeer:    true,
		RequiresRandom:  true,
		RequiresMessage: true,
	},
	"messages.sendMedia": {
		ReturnType:     "Updates",
		Description:    "Sends a media message (InputMediaUploadedPhoto / InputMediaUploadedDocument)",
		RequiresPeer:   true,
		RequiresRandom: true,
		RequiresMedia:  true,
	},
	"messages.sendMultiMedia": {
		ReturnType:     "Updates",
		Description:    "Sends an album of grouped media items (up to 10 InputSingleMedia items)",
		RequiresPeer:   true,
		RequiresRandom: true,
		RequiresMedia:  true,
	},
	"messages.forwardMessages": {
		ReturnType:     "Updates",
		Description:    "Forwards messages by ID from from_peer to to_peer with random_id",
		RequiresPeer:   true,
		RequiresRandom: true,
		RequiresMsgID:  true,
	},
	"messages.editMessage": {
		ReturnType:      "Updates",
		Description:     "Edits text, media, or reply markup of an existing message ID",
		RequiresPeer:    true,
		RequiresMsgID:   true,
		RequiresMessage: true,
	},
	"messages.deleteMessages": {
		ReturnType:    "messages.AffectedMessages",
		Description:   "Deletes messages by ID list and returns pts / pts_count",
		RequiresMsgID: true,
	},
	"messages.sendReaction": {
		ReturnType:    "Updates",
		Description:   "Reacts to a message with ReactionEmoji or ReactionCustomEmoji",
		RequiresPeer:  true,
		RequiresMsgID: true,
	},
	"messages.sendVote": {
		ReturnType:    "Updates",
		Description:   "Votes in a Poll attached to msg_id",
		RequiresPeer:  true,
		RequiresMsgID: true,
	},
	"messages.getHistory": {
		ReturnType:   "messages.Messages",
		Description:  "Returns message history for a chat peer with offset_id / limit",
		RequiresPeer: true,
	},
	"channels.readHistory": {
		ReturnType:   "Bool",
		Description:  "Marks channel/supergroup history as read up to max_id",
		RequiresPeer: true,
	},
}

const (
	maxTelegramTextChars    = 4096
	maxTelegramCaptionChars = 1024
	maxPhotoBytes           = 10 * 1024 * 1024
	maxBotAPIDocumentBytes  = 50 * 1024 * 1024
	maxMTProtoDocumentBytes = 2000 * 1024 * 1024
)

type TelegramConstraintEvaluator struct{}

func NewTelegramConstraintEvaluator() *TelegramConstraintEvaluator {
	return &TelegramConstraintEvaluator{}
}

func (evaluator *TelegramConstraintEvaluator) Evaluate(submission TelegramMethodRequest) TelegramEvaluationReport {
	charCount := utf8.RuneCountInString(submission.Message)
	method := strings.TrimSpace(submission.Method)
	if method == "" {
		method = "messages.sendMessage"
	}

	var criteria []EvaluationCriterion
	spec, exists := officialTelegramMethods[method]
	if !exists {
		criteria = append(criteria, EvaluationCriterion{
			Category: "TL Schema Method",
			Target:   method,
			Status:   StatusFail,
			Details:  fmt.Sprintf("Method '%s' not found in official core.telegram.org/methods registry", method),
		})
		return TelegramEvaluationReport{
			OverallStatus:  StatusFail,
			Method:         method,
			TLReturnType:   "rpc_error (400 METHOD_INVALID)",
			CharacterCount: charCount,
			Criteria:       criteria,
		}
	}

	criteria = append(criteria, EvaluationCriterion{
		Category: "TL Schema Method",
		Target:   fmt.Sprintf("%s -> %s", method, spec.ReturnType),
		Status:   StatusPass,
		Details:  spec.Description,
	})

	if spec.RequiresPeer {
		if strings.TrimSpace(submission.Peer) == "" {
			criteria = append(criteria, EvaluationCriterion{
				Category: "InputPeer Resolution",
				Target:   "peer:InputPeer",
				Status:   StatusFail,
				Details:  "400 PEER_ID_INVALID: required InputPeer (channel/chat/user) is empty",
			})
		} else {
			criteria = append(criteria, EvaluationCriterion{
				Category: "InputPeer Resolution",
				Target:   submission.Peer,
				Status:   StatusPass,
				Details:  fmt.Sprintf("Resolved InputPeer '%s' with valid access_hash", submission.Peer),
			})
		}
	}

	if spec.RequiresRandom {
		if submission.RandomID == 0 {
			criteria = append(criteria, EvaluationCriterion{
				Category: "Idempotency (random_id)",
				Target:   "random_id:long",
				Status:   StatusWarn,
				Details:  "random_id is 0; MTProto recommends non-zero 64-bit random_id to prevent duplicate message delivery on retry",
			})
		} else {
			criteria = append(criteria, EvaluationCriterion{
				Category: "Idempotency (random_id)",
				Target:   fmt.Sprintf("%d", submission.RandomID),
				Status:   StatusPass,
				Details:  fmt.Sprintf("64-bit random_id=%d verified for deduplication", submission.RandomID),
			})
		}
	}

	if spec.RequiresMsgID {
		if submission.MessageID <= 0 {
			criteria = append(criteria, EvaluationCriterion{
				Category: "Target Message ID",
				Target:   "id:int",
				Status:   StatusFail,
				Details:  "400 MESSAGE_ID_INVALID: method requires positive messageId reference",
			})
		} else {
			criteria = append(criteria, EvaluationCriterion{
				Category: "Target Message ID",
				Target:   fmt.Sprintf("msg_id=%d", submission.MessageID),
				Status:   StatusPass,
				Details:  fmt.Sprintf("Target message ID #%d valid in peer history", submission.MessageID),
			})
		}
	}

	if spec.RequiresMessage && charCount == 0 {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Message Entity Length",
			Target:   "message:string",
			Status:   StatusFail,
			Details:  "400 MESSAGE_EMPTY: message text is required for " + method,
		})
	} else if charCount > maxTelegramTextChars {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Message Entity Length",
			Target:   "4096 UTF-8 Limit",
			Status:   StatusFail,
			Details:  fmt.Sprintf("400 MESSAGE_TOO_LONG: length (%d chars) exceeds Telegram 4096 character limit", charCount),
		})
	} else if charCount > 0 {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Message Entity Length",
			Target:   "4096 UTF-8 Limit",
			Status:   StatusPass,
			Details:  fmt.Sprintf("Message text (%d/4096 chars) valid", charCount),
		})
	}

	if spec.RequiresMedia && submission.Media == nil {
		criteria = append(criteria, EvaluationCriterion{
			Category: "InputMedia Payload",
			Target:   "media:InputMedia",
			Status:   StatusFail,
			Details:  "400 MEDIA_EMPTY: method requires InputMedia descriptor",
		})
	}

	if submission.Media != nil {
		if charCount > maxTelegramCaptionChars {
			criteria = append(criteria, EvaluationCriterion{
				Category: "Media Caption Limit",
				Target:   "1024 Caption Chars",
				Status:   StatusFail,
				Details:  fmt.Sprintf("400 MEDIA_CAPTION_TOO_LONG: caption (%d chars) exceeds 1024-char limit", charCount),
			})
		} else if charCount > 0 {
			criteria = append(criteria, EvaluationCriterion{
				Category: "Media Caption Limit",
				Target:   "1024 Caption Chars",
				Status:   StatusPass,
				Details:  fmt.Sprintf("Media caption (%d/1024 chars) within limit", charCount),
			})
		}

		isPhoto := strings.HasPrefix(submission.Media.MIMEType, "image/") && submission.Media.TLType != "InputMediaUploadedDocument"
		if isPhoto && submission.Media.SizeBytes > maxPhotoBytes {
			criteria = append(criteria, EvaluationCriterion{
				Category: "MTProto Media Size",
				Target:   "InputMediaUploadedPhoto (10MB)",
				Status:   StatusFail,
				Details:  fmt.Sprintf("400 PHOTO_SAVE_FILE_INVALID: photo (%d bytes) exceeds 10MB compressed photo limit; send as InputMediaUploadedDocument instead", submission.Media.SizeBytes),
			})
		} else if submission.Media.SizeBytes > maxMTProtoDocumentBytes {
			criteria = append(criteria, EvaluationCriterion{
				Category: "MTProto Media Size",
				Target:   "2GB MTProto Limit",
				Status:   StatusFail,
				Details:  fmt.Sprintf("File size (%d bytes) exceeds 2GB MTProto document ceiling", submission.Media.SizeBytes),
			})
		} else if submission.Media.SizeBytes > maxBotAPIDocumentBytes {
			criteria = append(criteria, EvaluationCriterion{
				Category: "MTProto Media Size",
				Target:   "50MB Bot HTTP / 2GB MTProto",
				Status:   StatusWarn,
				Details:  fmt.Sprintf("File (%d bytes) exceeds 50MB Bot HTTP API limit; requires direct MTProto upload.saveBigFilePart", submission.Media.SizeBytes),
			})
		} else {
			criteria = append(criteria, EvaluationCriterion{
				Category: "MTProto Media Size",
				Target:   submission.Media.Filename,
				Status:   StatusPass,
				Details:  fmt.Sprintf("%s (%s, %d bytes) passes MTProto & Bot API size constraints", submission.Media.TLType, submission.Media.MIMEType, submission.Media.SizeBytes),
			})
		}
	}

	overall := StatusPass
	for _, criterion := range criteria {
		if criterion.Status == StatusFail {
			overall = StatusFail
			break
		}
		if criterion.Status == StatusWarn && overall == StatusPass {
			overall = StatusWarn
		}
	}

	return TelegramEvaluationReport{
		OverallStatus:  overall,
		Method:         method,
		TLReturnType:   spec.ReturnType,
		CharacterCount: charCount,
		Criteria:       criteria,
	}
}

type InMemoryTelegramRepository struct {
	mutex   sync.RWMutex
	records []TelegramRecord
}

func NewInMemoryTelegramRepository() *InMemoryTelegramRepository {
	return &InMemoryTelegramRepository{
		records: make([]TelegramRecord, 0, 128),
	}
}

func (repository *InMemoryTelegramRepository) Save(record TelegramRecord) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	repository.records = append([]TelegramRecord{record}, repository.records...)
}

func (repository *InMemoryTelegramRepository) ListBySession(sessionID string) []TelegramRecord {
	repository.mutex.RLock()
	defer repository.mutex.RUnlock()
	var filtered []TelegramRecord
	for _, record := range repository.records {
		if sessionID == "" || record.SessionID == "" || record.SessionID == sessionID {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

func (repository *InMemoryTelegramRepository) ClearSession(sessionID string) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	if sessionID == "" {
		return
	}
	retained := make([]TelegramRecord, 0, len(repository.records))
	for _, record := range repository.records {
		if record.SessionID != sessionID {
			retained = append(retained, record)
		}
	}
	repository.records = retained
}

func (repository *InMemoryTelegramRepository) ClearAll() {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	repository.records = make([]TelegramRecord, 0)
}

type SSEEvent struct {
	SessionID    string
	EventName    string
	RenderedHTML string
	JSONPayload  string
}

type SSEBroadcaster struct {
	mutex       sync.RWMutex
	subscribers map[chan SSEEvent]struct{}
}

func NewSSEBroadcaster() *SSEBroadcaster {
	return &SSEBroadcaster{
		subscribers: make(map[chan SSEEvent]struct{}),
	}
}

func (broadcaster *SSEBroadcaster) Subscribe() chan SSEEvent {
	channel := make(chan SSEEvent, 16)
	broadcaster.mutex.Lock()
	broadcaster.subscribers[channel] = struct{}{}
	broadcaster.mutex.Unlock()
	return channel
}

func (broadcaster *SSEBroadcaster) Unsubscribe(channel chan SSEEvent) {
	broadcaster.mutex.Lock()
	delete(broadcaster.subscribers, channel)
	close(channel)
	broadcaster.mutex.Unlock()
}

func (broadcaster *SSEBroadcaster) Broadcast(event SSEEvent) {
	broadcaster.mutex.RLock()
	defer broadcaster.mutex.RUnlock()
	for subscriber := range broadcaster.subscribers {
		select {
		case subscriber <- event:
		default:
		}
	}
}

type TelegramSanndboxService struct {
	repository  *InMemoryTelegramRepository
	evaluator   *TelegramConstraintEvaluator
	broadcaster *SSEBroadcaster
	templates   *template.Template
}

func NewTelegramSanndboxService(
	repository *InMemoryTelegramRepository,
	evaluator *TelegramConstraintEvaluator,
	broadcaster *SSEBroadcaster,
	templates *template.Template,
) *TelegramSanndboxService {
	return &TelegramSanndboxService{
		repository:  repository,
		evaluator:   evaluator,
		broadcaster: broadcaster,
		templates:   templates,
	}
}

func generateIdentifier() string {
	buffer := make([]byte, 8)
	_, _ = rand.Read(buffer)
	return hex.EncodeToString(buffer)
}

func (service *TelegramSanndboxService) Ingest(submission TelegramMethodRequest, source string) TelegramRecord {
	report := service.evaluator.Evaluate(submission)
	record := TelegramRecord{
		ID:        generateIdentifier(),
		SessionID: submission.SessionID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Source:    source,
		Method:    report.Method,
		Peer:      submission.Peer,
		Sender:    submission.Sender,
		RandomID:  submission.RandomID,
		MessageID: submission.MessageID,
		Message:   submission.Message,
		Reaction:  submission.Reaction,
		Media:     submission.Media,
		Report:    report,
	}
	service.repository.Save(record)

	var renderedBuffer bytes.Buffer
	_ = service.templates.ExecuteTemplate(&renderedBuffer, "tg_card", record)
	jsonBytes, _ := json.Marshal(record)

	service.broadcaster.Broadcast(SSEEvent{
		SessionID:    submission.SessionID,
		RenderedHTML: renderedBuffer.String(),
		JSONPayload:  string(jsonBytes),
	})

	return record
}

type HTTPHandler struct {
	service     *TelegramSanndboxService
	repository  *InMemoryTelegramRepository
	broadcaster *SSEBroadcaster
	templates   *template.Template
}

// HandleIndex renders the Telegram Desktop Sanndbox UI.
// @Summary Render Telegram Desktop Sanndbox UI
// @Produce html
// @Success 200 {string} string "HTML Dashboard"
// @Router / [get]
func (handler *HTTPHandler) HandleIndex(responseWriter http.ResponseWriter, request *http.Request) {
	responseWriter.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = handler.templates.ExecuteTemplate(responseWriter, "index.html", nil)
}

// HandleIngestTelegram evaluates and stores a Telegram MTProto method call.
// @Summary Invoke a Telegram MTProto method (core.telegram.org/methods)
// @Accept json
// @Produce json
// @Param payload body TelegramMethodRequest true "Telegram Method Invocation Payload"
// @Success 201 {object} TelegramRecord
// @Router /api/v1/telegram [post]
func (handler *HTTPHandler) HandleIngestTelegram(responseWriter http.ResponseWriter, request *http.Request) {
	var submission TelegramMethodRequest
	if err := json.NewDecoder(request.Body).Decode(&submission); err != nil {
		http.Error(responseWriter, err.Error(), http.StatusBadRequest)
		return
	}
	if submission.SessionID == "" {
		submission.SessionID = request.Header.Get("X-Sanndbox-Session")
	}
	source := "MTProto / HTTP API"
	if submission.SessionID != "" {
		source = "Telegram Desktop UI"
	}
	record := handler.service.Ingest(submission, source)
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(responseWriter).Encode(record)
}

// HandleListTelegram returns evaluated Telegram method records in memory.
// @Summary List evaluated Telegram method records
// @Produce json
// @Param sessionId query string false "Browser Tab Session ID"
// @Success 200 {array} TelegramRecord
// @Router /api/v1/telegram [get]
func (handler *HTTPHandler) HandleListTelegram(responseWriter http.ResponseWriter, request *http.Request) {
	sessionID := request.URL.Query().Get("sessionId")
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(handler.repository.ListBySession(sessionID))
}

// HandleClearSession deletes every Telegram message when all=1, otherwise only the messages of the given browser tab session.
// @Summary Clear cached Telegram records
// @Param sessionId query string false "Browser Tab Session ID"
// @Param all query bool false "Delete every message on this channel"
// @Success 204 {string} string "Cleared"
// @Router /api/v1/telegram/clear [post]
func (handler *HTTPHandler) HandleClearSession(responseWriter http.ResponseWriter, request *http.Request) {
	if clearAll := request.URL.Query().Get("all"); clearAll == "1" || clearAll == "true" {
		handler.repository.ClearAll()
		handler.broadcaster.Broadcast(SSEEvent{EventName: "telegram-cleared", JSONPayload: `{"cleared":true}`})
		responseWriter.WriteHeader(http.StatusNoContent)
		return
	}
	sessionID := request.URL.Query().Get("sessionId")
	handler.repository.ClearSession(sessionID)
	responseWriter.WriteHeader(http.StatusNoContent)
}

// HandleSSE streams evaluated Telegram events over Server-Sent Events.
// @Summary Real-time HTMX SSE stream
// @Produce text/event-stream
// @Param sessionId query string false "Browser Tab Session ID"
// @Success 200 {string} string "SSE Stream"
// @Router /events [get]
func (handler *HTTPHandler) HandleSSE(responseWriter http.ResponseWriter, request *http.Request) {
	flusher, ok := responseWriter.(http.Flusher)
	if !ok {
		http.Error(responseWriter, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	sessionID := request.URL.Query().Get("sessionId")

	responseWriter.Header().Set("Content-Type", "text/event-stream")
	responseWriter.Header().Set("Cache-Control", "no-cache")
	responseWriter.Header().Set("Connection", "keep-alive")

	subscriber := handler.broadcaster.Subscribe()
	defer handler.broadcaster.Unsubscribe(subscriber)

	_, _ = fmt.Fprintf(responseWriter, ": connected\n\n")
	flusher.Flush()

	for {
		select {
		case <-request.Context().Done():
			return
		case event := <-subscriber:
			if event.SessionID != "" && sessionID != "" && event.SessionID != sessionID {
				continue
			}
			eventName := event.EventName
			if eventName == "" {
				eventName = "tg-evaluated"
			}
			_, _ = fmt.Fprintf(responseWriter, "event: %s\ndata: %s\n\n", eventName, event.JSONPayload)
			flusher.Flush()
		}
	}
}

// HandleDocs renders interactive OpenAPI 3.0 documentation UI.
// @Summary Interactive OpenAPI 3.0 Documentation
// @Produce html
// @Success 200 {string} string "Swagger UI HTML"
// @Router /docs [get]
func (handler *HTTPHandler) HandleDocs(responseWriter http.ResponseWriter, request *http.Request) {
	responseWriter.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(responseWriter, `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Telegram MTProto Sanndbox | OpenAPI 3.0 Documentation</title>
    <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui.css">
    <style>
        body { margin: 0; background: #f8fafc; color: #0f172a; font-family: system-ui, sans-serif; transition: background 0.2s; }
        .topbar { display: none; }
        .doc-nav { display: flex; align-items: center; justify-content: space-between; padding: 12px 24px; background: #ffffff; border-bottom: 1px solid #e2e8f0; }
        .doc-nav a { color: #2481cc; text-decoration: none; font-weight: 600; font-size: 13px; }
        html.dark body { background: #0f172a; color: #f8fafc; }
        html.dark .doc-nav { background: #1e293b; border-bottom-color: #334155; }
        html.dark .swagger-ui { filter: invert(88%) hue-rotate(180deg); }
    </style>
</head>
<body>
    <div class="doc-nav">
        <a href="/">&larr; Back to Telegram Desktop UI (:9084)</a>
        <div style="display:flex;gap:6px;font-size:12px;">
            <button onclick="setDocTheme('system')" style="padding:4px 10px;border-radius:6px;border:1px solid #cbd5e1;cursor:pointer;">System</button>
            <button onclick="setDocTheme('light')" style="padding:4px 10px;border-radius:6px;border:1px solid #cbd5e1;cursor:pointer;">Light</button>
            <button onclick="setDocTheme('dark')" style="padding:4px 10px;border-radius:6px;border:1px solid #cbd5e1;cursor:pointer;">Dark</button>
        </div>
    </div>
    <div id="swagger-ui"></div>
    <script src="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"></script>
    <script>
        function setDocTheme(mode) {
            sessionStorage.setItem('Sanndbox_theme_mode', mode);
            const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
            const isDark = mode === 'dark' || (mode === 'system' && prefersDark);
            document.documentElement.classList.toggle('dark', isDark);
        }
        setDocTheme(sessionStorage.getItem('Sanndbox_theme_mode') || 'system');
        window.onload = () => {
            window.ui = SwaggerUIBundle({
                url: '/openapi.json',
                dom_id: '#swagger-ui',
                deepLinking: true,
                presets: [SwaggerUIBundle.presets.apis]
            });
        };
    </script>
</body>
</html>`)
}

func main() {
	parsedTemplates := template.Must(template.ParseFS(templateFS, "templates/index.html"))
	repository := NewInMemoryTelegramRepository()
	evaluator := NewTelegramConstraintEvaluator()
	broadcaster := NewSSEBroadcaster()
	service := NewTelegramSanndboxService(repository, evaluator, broadcaster, parsedTemplates)

	handler := &HTTPHandler{
		service:     service,
		repository:  repository,
		broadcaster: broadcaster,
		templates:   parsedTemplates,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", handler.HandleIndex)
	mux.HandleFunc("GET /docs", handler.HandleDocs)
	mux.HandleFunc("GET /docs/{$}", handler.HandleDocs)
	mux.HandleFunc("GET /openapi.json", func(responseWriter http.ResponseWriter, request *http.Request) {
		specBytes, _ := templateFS.ReadFile("docs/swagger.json")
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write(specBytes)
	})
	mux.HandleFunc("GET /events", handler.HandleSSE)
	mux.HandleFunc("POST /api/v1/telegram", handler.HandleIngestTelegram)
	mux.HandleFunc("GET /api/v1/telegram", handler.HandleListTelegram)
	mux.HandleFunc("POST /api/v1/telegram/clear", handler.HandleClearSession)

	fmt.Println("[telegram-server] HTTP UI & API listening on http://localhost:9084 (Docs: http://localhost:9084/docs)")
	_ = http.ListenAndServe(":9084", mux)
}
