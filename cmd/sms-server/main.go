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
	"unicode/utf16"
)

//go:embed templates/index.html docs/swagger.json
var templateFS embed.FS

// @title SMS Sanndbox API
// @version 1.0.0
// @description HTTP (:8082) SMS Sanndbox with GSM-7 and UCS-2 Encoding and Segmentation Evaluation Engine.
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

type SMSEvaluationReport struct {
	OverallStatus    EvaluationStatus      `json:"overallStatus"`
	Encoding         string                `json:"encoding"`
	TotalSeptetsOrCU int                   `json:"totalUnits"`
	SegmentCount     int                   `json:"segmentCount"`
	PerSegmentLimit  int                   `json:"perSegmentLimit"`
	Criteria         []EvaluationCriterion `json:"criteria"`
}

type SMSSubmissionRequest struct {
	SessionID string `json:"sessionId,omitempty"`
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Message   string `json:"message"`
}

type SMSRecord struct {
	ID        string              `json:"id"`
	SessionID string              `json:"sessionId,omitempty"`
	Timestamp string              `json:"timestamp"`
	Source    string              `json:"source"`
	Sender    string              `json:"sender"`
	Recipient string              `json:"recipient"`
	Message   string              `json:"message"`
	Report    SMSEvaluationReport `json:"report"`
}

type SMSEvaluator interface {
	Evaluate(submission SMSSubmissionRequest) SMSEvaluationReport
}

var gsm7BasicSet = map[rune]struct{}{
	'@': {}, '£': {}, '$': {}, '¥': {}, 'è': {}, 'é': {}, 'ù': {}, 'ì': {}, 'ò': {}, 'Ç': {}, '\n': {}, 'Ø': {}, 'ø': {}, '\r': {}, 'Å': {}, 'å': {},
	'Δ': {}, '_': {}, 'Φ': {}, 'Γ': {}, 'Λ': {}, 'Ω': {}, 'Π': {}, 'Ψ': {}, 'Σ': {}, 'Θ': {}, 'Ξ': {}, 'Æ': {}, 'æ': {}, 'ß': {}, 'É': {}, ' ': {},
	'!': {}, '"': {}, '#': {}, '¤': {}, '%': {}, '&': {}, '\'': {}, '(': {}, ')': {}, '*': {}, '+': {}, ',': {}, '-': {}, '.': {}, '/': {},
	'0': {}, '1': {}, '2': {}, '3': {}, '4': {}, '5': {}, '6': {}, '7': {}, '8': {}, '9': {}, ':': {}, ';': {}, '<': {}, '=': {}, '>': {}, '?': {},
	'¡': {}, 'A': {}, 'B': {}, 'C': {}, 'D': {}, 'E': {}, 'F': {}, 'G': {}, 'H': {}, 'I': {}, 'J': {}, 'K': {}, 'L': {}, 'M': {}, 'N': {}, 'O': {},
	'P': {}, 'Q': {}, 'R': {}, 'S': {}, 'T': {}, 'U': {}, 'V': {}, 'W': {}, 'X': {}, 'Y': {}, 'Z': {}, 'Ä': {}, 'Ö': {}, 'Ñ': {}, 'Ü': {}, '§': {},
	'¿': {}, 'a': {}, 'b': {}, 'c': {}, 'd': {}, 'e': {}, 'f': {}, 'g': {}, 'h': {}, 'i': {}, 'j': {}, 'k': {}, 'l': {}, 'm': {}, 'n': {}, 'o': {},
	'p': {}, 'q': {}, 'r': {}, 's': {}, 't': {}, 'u': {}, 'v': {}, 'w': {}, 'x': {}, 'y': {}, 'z': {}, 'ä': {}, 'ö': {}, 'ñ': {}, 'ü': {}, 'à': {},
}

var gsm7ExtensionSet = map[rune]struct{}{
	'^': {}, '{': {}, '}': {}, '\\': {}, '[': {}, '~': {}, ']': {}, '|': {}, '€': {}, '\f': {},
}

type StandardSMSEvaluator struct{}

func NewStandardSMSEvaluator() *StandardSMSEvaluator {
	return &StandardSMSEvaluator{}
}

func (evaluator *StandardSMSEvaluator) Evaluate(submission SMSSubmissionRequest) SMSEvaluationReport {
	isGSM7 := true
	septetCount := 0
	extensionChars := 0

	for _, char := range submission.Message {
		if _, existsBasic := gsm7BasicSet[char]; existsBasic {
			septetCount++
		} else if _, existsExt := gsm7ExtensionSet[char]; existsExt {
			septetCount += 2
			extensionChars++
		} else {
			isGSM7 = false
			break
		}
	}

	var criteria []EvaluationCriterion
	encoding := "GSM-7"
	totalUnits := septetCount
	segmentCount := 1
	perSegmentLimit := 160

	if strings.TrimSpace(submission.Message) == "" {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Payload Validation",
			Target:   "Empty Body Check",
			Status:   StatusFail,
			Details:  "SMS message body cannot be empty",
		})
	}

	if isGSM7 {
		if totalUnits > 160 {
			perSegmentLimit = 153
			segmentCount = (totalUnits + 152) / 153
		}
		criteria = append(criteria, EvaluationCriterion{
			Category: "Character Set",
			Target:   "GSM 03.38 (7-bit)",
			Status:   StatusPass,
			Details:  fmt.Sprintf("Pure GSM-7 encoding (%d septets, %d escaped extension chars)", totalUnits, extensionChars),
		})
	} else {
		encoding = "UCS-2"
		utf16Units := utf16.Encode([]rune(submission.Message))
		totalUnits = len(utf16Units)
		perSegmentLimit = 70
		if totalUnits > 70 {
			perSegmentLimit = 67
			segmentCount = (totalUnits + 66) / 67
		}
		criteria = append(criteria, EvaluationCriterion{
			Category: "Character Set",
			Target:   "UCS-2 (16-bit Unicode)",
			Status:   StatusWarn,
			Details:  fmt.Sprintf("Non-GSM character detected; downgraded to UCS-2 (%d UTF-16 code units)", totalUnits),
		})
	}

	if segmentCount == 1 {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Segmentation",
			Target:   "Single PDU Frame",
			Status:   StatusPass,
			Details:  fmt.Sprintf("Fits in 1 segment (%d/%d units)", totalUnits, perSegmentLimit),
		})
	} else if segmentCount <= 6 {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Segmentation",
			Target:   "UDH Concatenated SMS",
			Status:   StatusWarn,
			Details:  fmt.Sprintf("Requires %d concatenated segments (%d units/segment after 6-byte UDH header)", segmentCount, perSegmentLimit),
		})
	} else {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Segmentation",
			Target:   "Carrier Concatenation Cap",
			Status:   StatusFail,
			Details:  fmt.Sprintf("Segment count (%d) exceeds 6-segment carrier deliverability threshold", segmentCount),
		})
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

	return SMSEvaluationReport{
		OverallStatus:    overall,
		Encoding:         encoding,
		TotalSeptetsOrCU: totalUnits,
		SegmentCount:     segmentCount,
		PerSegmentLimit:  perSegmentLimit,
		Criteria:         criteria,
	}
}

type InMemorySMSRepository struct {
	mutex   sync.RWMutex
	records []SMSRecord
}

func NewInMemorySMSRepository() *InMemorySMSRepository {
	return &InMemorySMSRepository{
		records: make([]SMSRecord, 0, 128),
	}
}

func (repository *InMemorySMSRepository) Save(record SMSRecord) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	repository.records = append([]SMSRecord{record}, repository.records...)
}

func (repository *InMemorySMSRepository) ListBySession(sessionID string) []SMSRecord {
	repository.mutex.RLock()
	defer repository.mutex.RUnlock()
	var filtered []SMSRecord
	for _, record := range repository.records {
		if sessionID == "" || record.SessionID == "" || record.SessionID == sessionID {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

func (repository *InMemorySMSRepository) ClearSession(sessionID string) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	if sessionID == "" {
		return
	}
	retained := make([]SMSRecord, 0, len(repository.records))
	for _, record := range repository.records {
		if record.SessionID != sessionID {
			retained = append(retained, record)
		}
	}
	repository.records = retained
}

type SSEEvent struct {
	SessionID    string
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

type SMSSanndboxService struct {
	repository  *InMemorySMSRepository
	evaluator   SMSEvaluator
	broadcaster *SSEBroadcaster
	templates   *template.Template
}

func NewSMSSanndboxService(
	repository *InMemorySMSRepository,
	evaluator SMSEvaluator,
	broadcaster *SSEBroadcaster,
	templates *template.Template,
) *SMSSanndboxService {
	return &SMSSanndboxService{
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

func (service *SMSSanndboxService) Ingest(submission SMSSubmissionRequest, source string) SMSRecord {
	report := service.evaluator.Evaluate(submission)
	record := SMSRecord{
		ID:        generateIdentifier(),
		SessionID: submission.SessionID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Source:    source,
		Sender:    submission.Sender,
		Recipient: submission.Recipient,
		Message:   submission.Message,
		Report:    report,
	}
	service.repository.Save(record)

	var renderedBuffer bytes.Buffer
	_ = service.templates.ExecuteTemplate(&renderedBuffer, "sms_card", record)
	jsonBytes, _ := json.Marshal(record)

	service.broadcaster.Broadcast(SSEEvent{
		SessionID:    submission.SessionID,
		RenderedHTML: renderedBuffer.String(),
		JSONPayload:  string(jsonBytes),
	})

	return record
}

type HTTPHandler struct {
	service     *SMSSanndboxService
	repository  *InMemorySMSRepository
	broadcaster *SSEBroadcaster
	templates   *template.Template
}

// HandleIndex renders the SMS Sanndbox UI.
// @Summary Render SMS Sanndbox Dashboard
// @Produce html
// @Success 200 {string} string "HTML Dashboard"
// @Router / [get]
func (handler *HTTPHandler) HandleIndex(responseWriter http.ResponseWriter, request *http.Request) {
	responseWriter.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = handler.templates.ExecuteTemplate(responseWriter, "index.html", nil)
}

// HandleIngestSMS evaluates and stores a new SMS message.
// @Summary Submit SMS for GSM-7/UCS-2 evaluation
// @Accept json
// @Produce json
// @Param payload body SMSSubmissionRequest true "SMS Submission Payload"
// @Success 201 {object} SMSRecord
// @Router /api/v1/sms [post]
func (handler *HTTPHandler) HandleIngestSMS(responseWriter http.ResponseWriter, request *http.Request) {
	var submission SMSSubmissionRequest
	if err := json.NewDecoder(request.Body).Decode(&submission); err != nil {
		http.Error(responseWriter, err.Error(), http.StatusBadRequest)
		return
	}
	if submission.SessionID == "" {
		submission.SessionID = request.Header.Get("X-Sanndbox-Session")
	}
	source := "HTTP API"
	if submission.SessionID != "" {
		source = "UI Composer"
	}
	record := handler.service.Ingest(submission, source)
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(responseWriter).Encode(record)
}

// HandleListSMS returns evaluated SMS messages in memory.
// @Summary List evaluated SMS records
// @Produce json
// @Param sessionId query string false "Browser Tab Session ID"
// @Success 200 {array} SMSRecord
// @Router /api/v1/sms [get]
func (handler *HTTPHandler) HandleListSMS(responseWriter http.ResponseWriter, request *http.Request) {
	sessionID := request.URL.Query().Get("sessionId")
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(handler.repository.ListBySession(sessionID))
}

// HandleClearSession evicts all cached SMS messages associated with a closed browser tab session.
// @Summary Clear session-scoped SMS cache
// @Param sessionId query string true "Browser Tab Session ID"
// @Success 204 {string} string "Cleared"
// @Router /api/v1/sms/clear [post]
func (handler *HTTPHandler) HandleClearSession(responseWriter http.ResponseWriter, request *http.Request) {
	sessionID := request.URL.Query().Get("sessionId")
	handler.repository.ClearSession(sessionID)
	responseWriter.WriteHeader(http.StatusNoContent)
}

// HandleSSE streams evaluated SMS events over Server-Sent Events.
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
			_, _ = fmt.Fprintf(responseWriter, "event: sms-evaluated\ndata: %s\n\n", event.JSONPayload)
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
    <title>SMS Sanndbox | OpenAPI 3.0 Documentation</title>
    <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui.css">
    <style>
        body { margin: 0; background: #f8fafc; color: #0f172a; font-family: system-ui, sans-serif; transition: background 0.2s; }
        .topbar { display: none; }
        .doc-nav { display: flex; align-items: center; justify-content: space-between; padding: 12px 24px; background: #ffffff; border-bottom: 1px solid #e2e8f0; }
        .doc-nav a { color: #0284c7; text-decoration: none; font-weight: 600; font-size: 13px; }
        html.dark body { background: #0f172a; color: #f8fafc; }
        html.dark .doc-nav { background: #1e293b; border-bottom-color: #334155; }
        html.dark .swagger-ui { filter: invert(88%) hue-rotate(180deg); }
    </style>
</head>
<body>
    <div class="doc-nav">
        <a href="/">&larr; Back to Phone SMS UI (:9082)</a>
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
	repository := NewInMemorySMSRepository()
	evaluator := NewStandardSMSEvaluator()
	broadcaster := NewSSEBroadcaster()
	service := NewSMSSanndboxService(repository, evaluator, broadcaster, parsedTemplates)

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
	mux.HandleFunc("POST /api/v1/sms", handler.HandleIngestSMS)
	mux.HandleFunc("GET /api/v1/sms", handler.HandleListSMS)
	mux.HandleFunc("POST /api/v1/sms/clear", handler.HandleClearSession)

	fmt.Println("[sms-server] HTTP UI & API listening on http://localhost:9082 (Docs: http://localhost:9082/docs)")
	_ = http.ListenAndServe(":9082", mux)
}
