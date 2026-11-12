package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"
)

//go:embed templates/index.html docs/swagger.json
var templateFS embed.FS

// @title Email Sanndbox API
// @version 1.0.0
// @description SMTP (:2525) & HTTP (:8081) Email Sanndbox with Static HTML Visibility Matrix and Attachment Threat Scanner.
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

type EvaluationReport struct {
	OverallStatus EvaluationStatus      `json:"overallStatus"`
	Criteria      []EvaluationCriterion `json:"criteria"`
}

type AttachmentPayload struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Base64Data  string `json:"base64Data"`
}

type EmailSubmissionRequest struct {
	SessionID   string              `json:"sessionId,omitempty"`
	Sender      string              `json:"sender"`
	Recipients  []string            `json:"recipients"`
	Subject     string              `json:"subject"`
	HTMLBody    string              `json:"htmlBody"`
	Attachments []AttachmentPayload `json:"attachments"`
}

type EmailRecord struct {
	ID          string              `json:"id"`
	SessionID   string              `json:"sessionId,omitempty"`
	Timestamp   string              `json:"timestamp"`
	Source      string              `json:"source"`
	Sender      string              `json:"sender"`
	Recipients  []string            `json:"recipients"`
	Subject     string              `json:"subject"`
	HTMLBody    string              `json:"htmlBody"`
	Attachments []AttachmentPayload `json:"attachments"`
	Report      EvaluationReport    `json:"report"`
}

type EmailEvaluator interface {
	Evaluate(submission EmailSubmissionRequest) EvaluationReport
}

type HTMLVisibilityAnalyzer struct{}

func (analyzer HTMLVisibilityAnalyzer) Analyze(htmlContent string) []EvaluationCriterion {
	lowerHTML := strings.ToLower(htmlContent)
	var criteria []EvaluationCriterion

	hasScript := strings.Contains(lowerHTML, "<script")
	hasFlexOrGrid := strings.Contains(lowerHTML, "display:flex") || strings.Contains(lowerHTML, "display: flex") || strings.Contains(lowerHTML, "display:grid") || strings.Contains(lowerHTML, "display: grid")
	hasExternalStylesheet := strings.Contains(lowerHTML, `rel="stylesheet"`) || strings.Contains(lowerHTML, `rel='stylesheet'`)
	hasViewportMeta := strings.Contains(lowerHTML, `name="viewport"`) || strings.Contains(lowerHTML, `name='viewport'`)
	sizeBytes := len(htmlContent)

	if hasScript {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Client Rendering",
			Target:   "All Email Clients",
			Status:   StatusFail,
			Details:  "Executable <script> tags stripped or blocked across all major email clients",
		})
	} else {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Client Rendering",
			Target:   "Script Sanitization",
			Status:   StatusPass,
			Details:  "No prohibited script tags detected",
		})
	}

	if hasFlexOrGrid {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Client Rendering",
			Target:   "Outlook Desktop (Word Engine)",
			Status:   StatusWarn,
			Details:  "CSS Flexbox/Grid layout properties degrade in Outlook Desktop; table-based layout recommended",
		})
	} else {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Client Rendering",
			Target:   "Outlook Desktop (Word Engine)",
			Status:   StatusPass,
			Details:  "Compatible box-model properties for Word rendering engine",
		})
	}

	if hasExternalStylesheet {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Client Rendering",
			Target:   "Gmail Web & Mobile",
			Status:   StatusWarn,
			Details:  "External <link rel='stylesheet'> stripped by Gmail; inline styles required",
		})
	}

	if sizeBytes > 102400 {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Client Rendering",
			Target:   "Gmail Clipping Threshold",
			Status:   StatusWarn,
			Details:  fmt.Sprintf("HTML payload (%d bytes) exceeds 102KB Gmail clipping threshold", sizeBytes),
		})
	} else {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Client Rendering",
			Target:   "Gmail Clipping Threshold",
			Status:   StatusPass,
			Details:  fmt.Sprintf("HTML payload (%d bytes) within 102KB rendering limit", sizeBytes),
		})
	}

	if !hasViewportMeta {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Client Rendering",
			Target:   "Apple Mail / iOS Responsive",
			Status:   StatusWarn,
			Details:  "Missing viewport meta tag for mobile scaling",
		})
	} else {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Client Rendering",
			Target:   "Apple Mail / iOS Responsive",
			Status:   StatusPass,
			Details:  "Viewport meta declaration present",
		})
	}

	return criteria
}

type AttachmentThreatScanner struct{}

var magicSignatures = map[string][]byte{
	"application/pdf": {0x25, 0x50, 0x44, 0x46, 0x2D},
	"image/png":       {0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A},
	"image/jpeg":      {0xFF, 0xD8, 0xFF},
	"image/gif":       {0x47, 0x49, 0x46, 0x38},
}

const eicarSignature = "X5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*"

func (scanner AttachmentThreatScanner) Scan(attachments []AttachmentPayload) []EvaluationCriterion {
	if len(attachments) == 0 {
		return []EvaluationCriterion{
			{
				Category: "Threat Scanner",
				Target:   "Attachments",
				Status:   StatusPass,
				Details:  "No attachments present",
			},
		}
	}

	var criteria []EvaluationCriterion
	for _, attachment := range attachments {
		rawBytes, err := base64.StdEncoding.DecodeString(attachment.Base64Data)
		if err != nil {
			criteria = append(criteria, EvaluationCriterion{
				Category: "Threat Scanner",
				Target:   attachment.Filename,
				Status:   StatusFail,
				Details:  "Corrupted Base64 encoding in attachment payload",
			})
			continue
		}

		expectedMagic, supportedMIME := magicSignatures[attachment.ContentType]
		if !supportedMIME {
			criteria = append(criteria, EvaluationCriterion{
				Category: "Threat Scanner",
				Target:   attachment.Filename,
				Status:   StatusFail,
				Details:  fmt.Sprintf("Disallowed MIME type '%s'; only PDF, PNG, JPEG, GIF permitted", attachment.ContentType),
			})
			continue
		}

		if !bytes.HasPrefix(rawBytes, expectedMagic) {
			criteria = append(criteria, EvaluationCriterion{
				Category: "Threat Scanner",
				Target:   attachment.Filename,
				Status:   StatusFail,
				Details:  fmt.Sprintf("Magic byte mismatch for declared MIME '%s'", attachment.ContentType),
			})
			continue
		}

		payloadString := string(rawBytes)
		if strings.Contains(payloadString, eicarSignature) {
			criteria = append(criteria, EvaluationCriterion{
				Category: "Threat Scanner",
				Target:   attachment.Filename,
				Status:   StatusFail,
				Details:  "Malware signature detected: EICAR-STANDARD-ANTIVIRUS-TEST-FILE",
			})
			continue
		}

		if attachment.ContentType == "application/pdf" && (strings.Contains(payloadString, "/JavaScript") || strings.Contains(payloadString, "/JS") || strings.Contains(payloadString, "/Launch")) {
			criteria = append(criteria, EvaluationCriterion{
				Category: "Threat Scanner",
				Target:   attachment.Filename,
				Status:   StatusFail,
				Details:  "Malicious PDF action dictionary detected (/JavaScript or /Launch)",
			})
			continue
		}

		if strings.HasPrefix(attachment.ContentType, "image/") && (strings.Contains(strings.ToLower(payloadString), "<script") || strings.Contains(payloadString, "<?php")) {
			criteria = append(criteria, EvaluationCriterion{
				Category: "Threat Scanner",
				Target:   attachment.Filename,
				Status:   StatusFail,
				Details:  "Embedded script payload detected inside image container",
			})
			continue
		}

		criteria = append(criteria, EvaluationCriterion{
			Category: "Threat Scanner",
			Target:   attachment.Filename,
			Status:   StatusPass,
			Details:  fmt.Sprintf("Verified magic bytes (%s) and passed heuristic malware scan (%d bytes)", attachment.ContentType, len(rawBytes)),
		})
	}

	return criteria
}

type CompositeEmailEvaluator struct {
	htmlAnalyzer  HTMLVisibilityAnalyzer
	threatScanner AttachmentThreatScanner
}

func NewCompositeEmailEvaluator() *CompositeEmailEvaluator {
	return &CompositeEmailEvaluator{
		htmlAnalyzer:  HTMLVisibilityAnalyzer{},
		threatScanner: AttachmentThreatScanner{},
	}
}

func (evaluator *CompositeEmailEvaluator) Evaluate(submission EmailSubmissionRequest) EvaluationReport {
	var allCriteria []EvaluationCriterion
	allCriteria = append(allCriteria, evaluator.htmlAnalyzer.Analyze(submission.HTMLBody)...)
	allCriteria = append(allCriteria, evaluator.threatScanner.Scan(submission.Attachments)...)

	overall := StatusPass
	for _, criterion := range allCriteria {
		if criterion.Status == StatusFail {
			overall = StatusFail
			break
		}
		if criterion.Status == StatusWarn && overall == StatusPass {
			overall = StatusWarn
		}
	}

	return EvaluationReport{
		OverallStatus: overall,
		Criteria:      allCriteria,
	}
}

type InMemoryEmailRepository struct {
	mutex   sync.RWMutex
	records []EmailRecord
}

func NewInMemoryEmailRepository() *InMemoryEmailRepository {
	return &InMemoryEmailRepository{
		records: make([]EmailRecord, 0, 128),
	}
}

func (repository *InMemoryEmailRepository) Save(record EmailRecord) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	repository.records = append([]EmailRecord{record}, repository.records...)
}

func (repository *InMemoryEmailRepository) ListBySession(sessionID string) []EmailRecord {
	repository.mutex.RLock()
	defer repository.mutex.RUnlock()
	var filtered []EmailRecord
	for _, record := range repository.records {
		if sessionID == "" || record.SessionID == "" || record.SessionID == sessionID {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

func (repository *InMemoryEmailRepository) ClearSession(sessionID string) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	if sessionID == "" {
		return
	}
	retained := make([]EmailRecord, 0, len(repository.records))
	for _, record := range repository.records {
		if record.SessionID != sessionID {
			retained = append(retained, record)
		}
	}
	repository.records = retained
}

func (repository *InMemoryEmailRepository) ClearAll() {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	repository.records = make([]EmailRecord, 0)
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

type EmailSanndboxService struct {
	repository  *InMemoryEmailRepository
	evaluator   EmailEvaluator
	broadcaster *SSEBroadcaster
	templates   *template.Template
}

func NewEmailSanndboxService(
	repository *InMemoryEmailRepository,
	evaluator EmailEvaluator,
	broadcaster *SSEBroadcaster,
	templates *template.Template,
) *EmailSanndboxService {
	return &EmailSanndboxService{
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

func (service *EmailSanndboxService) Ingest(submission EmailSubmissionRequest, source string) EmailRecord {
	report := service.evaluator.Evaluate(submission)
	record := EmailRecord{
		ID:          generateIdentifier(),
		SessionID:   submission.SessionID,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		Source:      source,
		Sender:      submission.Sender,
		Recipients:  submission.Recipients,
		Subject:     submission.Subject,
		HTMLBody:    submission.HTMLBody,
		Attachments: submission.Attachments,
		Report:      report,
	}
	service.repository.Save(record)

	var renderedBuffer bytes.Buffer
	_ = service.templates.ExecuteTemplate(&renderedBuffer, "message_card", record)
	jsonBytes, _ := json.Marshal(record)

	service.broadcaster.Broadcast(SSEEvent{
		SessionID:    submission.SessionID,
		RenderedHTML: renderedBuffer.String(),
		JSONPayload:  string(jsonBytes),
	})

	return record
}

type SMTPServer struct {
	listenAddr string
	service    *EmailSanndboxService
}

func NewSMTPServer(listenAddr string, service *EmailSanndboxService) *SMTPServer {
	return &SMTPServer{
		listenAddr: listenAddr,
		service:    service,
	}
}

func (smtpServer *SMTPServer) ListenAndServe() error {
	listener, err := net.Listen("tcp", smtpServer.listenAddr)
	if err != nil {
		return err
	}
	for {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			continue
		}
		go smtpServer.handleSession(connection)
	}
}

func (smtpServer *SMTPServer) handleSession(connection net.Conn) {
	defer func() { _ = connection.Close() }()
	_ = connection.SetDeadline(time.Now().Add(30 * time.Second))

	reader := bufio.NewReader(connection)
	writer := bufio.NewWriter(connection)

	writeLine := func(line string) {
		_, _ = writer.WriteString(line + "\r\n")
		_ = writer.Flush()
	}

	writeLine("220 email-Sanndbox.local ESMTP Service Ready")

	var sender string
	var recipients []string
	var dataBuffer bytes.Buffer
	inDataMode := false

	for {
		rawLine, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line := strings.TrimRight(rawLine, "\r\n")

		if inDataMode {
			if line == "." {
				inDataMode = false
				submission := parseRawMIME(sender, recipients, dataBuffer.Bytes())
				smtpServer.service.Ingest(submission, "SMTP :2525")
				dataBuffer.Reset()
				writeLine("250 2.0.0 OK Message queued for Sanndbox evaluation")
				continue
			}
			if strings.HasPrefix(line, "..") {
				line = line[1:]
			}
			dataBuffer.WriteString(line + "\r\n")
			continue
		}

		upperLine := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upperLine, "EHLO") || strings.HasPrefix(upperLine, "HELO"):
			writeLine("250-email-Sanndbox.local")
			writeLine("250 SIZE 20971520")
		case strings.HasPrefix(upperLine, "MAIL FROM:"):
			sender = extractAddress(line[10:])
			writeLine("250 2.1.0 OK")
		case strings.HasPrefix(upperLine, "RCPT TO:"):
			recipients = append(recipients, extractAddress(line[8:]))
			writeLine("250 2.1.5 OK")
		case upperLine == "DATA":
			inDataMode = true
			writeLine("354 End data with <CR><LF>.<CR><LF>")
		case upperLine == "RSET":
			sender = ""
			recipients = nil
			dataBuffer.Reset()
			writeLine("250 2.0.0 OK")
		case upperLine == "NOOP":
			writeLine("250 2.0.0 OK")
		case upperLine == "QUIT":
			writeLine("221 2.0.0 Bye")
			return
		default:
			writeLine("500 5.5.1 Command unrecognized")
		}
	}
}

func extractAddress(raw string) string {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "<")
	if idx := strings.Index(trimmed, ">"); idx != -1 {
		trimmed = trimmed[:idx]
	}
	return trimmed
}

func parseRawMIME(sender string, recipients []string, rawData []byte) EmailSubmissionRequest {
	submission := EmailSubmissionRequest{
		Sender:     sender,
		Recipients: recipients,
	}

	parsedMessage, err := mail.ReadMessage(bytes.NewReader(rawData))
	if err != nil {
		submission.Subject = "Raw Unstructured SMTP Payload"
		submission.HTMLBody = string(rawData)
		return submission
	}

	submission.Subject = parsedMessage.Header.Get("Subject")
	submission.SessionID = parsedMessage.Header.Get("X-Sanndbox-Session")
	contentType := parsedMessage.Header.Get("Content-Type")
	if contentType == "" {
		bodyBytes, _ := io.ReadAll(parsedMessage.Body)
		submission.HTMLBody = string(bodyBytes)
		return submission
	}

	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		bodyBytes, _ := io.ReadAll(parsedMessage.Body)
		submission.HTMLBody = string(bodyBytes)
		return submission
	}

	multipartReader := multipart.NewReader(parsedMessage.Body, params["boundary"])
	for {
		part, partErr := multipartReader.NextPart()
		if partErr != nil {
			break
		}
		partBytes, _ := io.ReadAll(part)
		partContentType := part.Header.Get("Content-Type")
		partMediaType, _, _ := mime.ParseMediaType(partContentType)
		filename := part.FileName()

		if filename != "" {
			if strings.EqualFold(part.Header.Get("Content-Transfer-Encoding"), "base64") {
				cleaned := strings.ReplaceAll(strings.ReplaceAll(string(partBytes), "\r", ""), "\n", "")
				submission.Attachments = append(submission.Attachments, AttachmentPayload{
					Filename:    filename,
					ContentType: partMediaType,
					Base64Data:  cleaned,
				})
			} else {
				submission.Attachments = append(submission.Attachments, AttachmentPayload{
					Filename:    filename,
					ContentType: partMediaType,
					Base64Data:  base64.StdEncoding.EncodeToString(partBytes),
				})
			}
		} else if strings.Contains(partMediaType, "html") || submission.HTMLBody == "" {
			submission.HTMLBody = string(partBytes)
		}
	}

	return submission
}

type HTTPHandler struct {
	service     *EmailSanndboxService
	repository  *InMemoryEmailRepository
	broadcaster *SSEBroadcaster
	templates   *template.Template
}

// HandleIndex renders the Email Sanndbox UI.
// @Summary Render Email Sanndbox Dashboard
// @Produce html
// @Success 200 {string} string "HTML Dashboard"
// @Router / [get]
func (handler *HTTPHandler) HandleIndex(responseWriter http.ResponseWriter, request *http.Request) {
	responseWriter.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = handler.templates.ExecuteTemplate(responseWriter, "index.html", nil)
}

// HandleIngestEmail evaluates and stores a new email message via HTTP.
// @Summary Submit email for evaluation
// @Accept json
// @Produce json
// @Param payload body EmailSubmissionRequest true "Email Submission Payload"
// @Success 201 {object} EmailRecord
// @Router /api/v1/emails [post]
func (handler *HTTPHandler) HandleIngestEmail(responseWriter http.ResponseWriter, request *http.Request) {
	var submission EmailSubmissionRequest
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

// HandleListEmails returns evaluated emails for the current session or global stream.
// @Summary List evaluated emails
// @Produce json
// @Param sessionId query string false "Browser Tab Session ID"
// @Success 200 {array} EmailRecord
// @Router /api/v1/emails [get]
func (handler *HTTPHandler) HandleListEmails(responseWriter http.ResponseWriter, request *http.Request) {
	sessionID := request.URL.Query().Get("sessionId")
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(handler.repository.ListBySession(sessionID))
}

// HandleClearSession deletes every email message when all=1, otherwise only the messages of the given browser tab session.
// @Summary Clear cached email messages
// @Param sessionId query string false "Browser Tab Session ID"
// @Param all query bool false "Delete every message on this channel"
// @Success 204 {string} string "Cleared"
// @Router /api/v1/emails/clear [post]
func (handler *HTTPHandler) HandleClearSession(responseWriter http.ResponseWriter, request *http.Request) {
	if clearAll := request.URL.Query().Get("all"); clearAll == "1" || clearAll == "true" {
		handler.repository.ClearAll()
		handler.broadcaster.Broadcast(SSEEvent{EventName: "email-cleared", JSONPayload: `{"cleared":true}`})
		responseWriter.WriteHeader(http.StatusNoContent)
		return
	}
	sessionID := request.URL.Query().Get("sessionId")
	handler.repository.ClearSession(sessionID)
	responseWriter.WriteHeader(http.StatusNoContent)
}

// HandleSSE streams evaluated email events over Server-Sent Events.
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
				eventName = "email-evaluated"
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
    <title>Email Sanndbox | OpenAPI 3.0 Documentation</title>
    <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui.css">
    <style>
        body { margin: 0; background: #f8fafc; color: #0f172a; font-family: system-ui, sans-serif; transition: background 0.2s; }
        .topbar { display: none; }
        .doc-nav { display: flex; align-items: center; justify-content: space-between; padding: 12px 24px; background: #ffffff; border-bottom: 1px solid #e2e8f0; }
        .doc-nav a { color: #059669; text-decoration: none; font-weight: 600; font-size: 13px; }
        html.dark body { background: #0f172a; color: #f8fafc; }
        html.dark .doc-nav { background: #1e293b; border-bottom-color: #334155; }
        html.dark .swagger-ui { filter: invert(88%) hue-rotate(180deg); }
    </style>
</head>
<body>
    <div class="doc-nav">
        <a href="/">&larr; Back to Email Webmail UI (:9081)</a>
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
	repository := NewInMemoryEmailRepository()
	evaluator := NewCompositeEmailEvaluator()
	broadcaster := NewSSEBroadcaster()
	service := NewEmailSanndboxService(repository, evaluator, broadcaster, parsedTemplates)

	smtpServer := NewSMTPServer(":2525", service)
	go func() {
		fmt.Println("[email-server] SMTP listening on :2525")
		_ = smtpServer.ListenAndServe()
	}()

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
	mux.HandleFunc("POST /api/v1/emails", handler.HandleIngestEmail)
	mux.HandleFunc("GET /api/v1/emails", handler.HandleListEmails)
	mux.HandleFunc("POST /api/v1/emails/clear", handler.HandleClearSession)

	fmt.Println("[email-server] HTTP UI & API listening on http://localhost:9081 (Docs: http://localhost:9081/docs)")
	_ = http.ListenAndServe(":9081", mux)
}
