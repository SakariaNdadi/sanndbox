package main

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed templates/index.html docs/swagger.json
var templateFS embed.FS

// @title Communications Sanndbox Unified Observability Portal
// @version 2.0.0
// @description Grafana-inspired real-time observability hub (:9080) aggregating live SSE streams and telemetry across Email (:9081), SMS (:9082), WhatsApp Business (:9083), and Telegram Desktop (:9084).
// @BasePath /

type UnifiedCriterion struct {
	Category string `json:"category"`
	Target   string `json:"target"`
	Status   string `json:"status"`
	Details  string `json:"details"`
}

type UnifiedFeedItem struct {
	UID         string             `json:"uid"`
	ID          string             `json:"id"`
	Channel     string             `json:"channel"`
	Port        int                `json:"port"`
	Timestamp   string             `json:"timestamp"`
	Source      string             `json:"source"`
	Sender      string             `json:"sender"`
	Recipient   string             `json:"recipient"`
	Title       string             `json:"title"`
	Preview     string             `json:"preview"`
	ProtocolTag string             `json:"protocolTag"`
	Status      string             `json:"status"`
	Seen        bool               `json:"seen"`
	SeenAt      string             `json:"seenAt,omitempty"`
	CheckCount  int                `json:"checkCount"`
	Criteria    []UnifiedCriterion `json:"criteria"`
	RawPayload  any                `json:"rawPayload,omitempty"`
}

type ChannelHealthStatus struct {
	Channel   string `json:"channel"`
	Port      int    `json:"port"`
	Connected bool   `json:"connected"`
	LastSeen  string `json:"lastSeen"`
	TotalSeen int    `json:"totalSeen"`
}

type FeedSnapshotResponse struct {
	Items   []UnifiedFeedItem              `json:"items"`
	Sources map[string]ChannelHealthStatus `json:"sources"`
}

type HubBroadcaster struct {
	mutex       sync.RWMutex
	subscribers map[chan string]struct{}
}

func NewHubBroadcaster() *HubBroadcaster {
	return &HubBroadcaster{
		subscribers: make(map[chan string]struct{}),
	}
}

func (broadcaster *HubBroadcaster) Subscribe() chan string {
	ch := make(chan string, 32)
	broadcaster.mutex.Lock()
	broadcaster.subscribers[ch] = struct{}{}
	broadcaster.mutex.Unlock()
	return ch
}

func (broadcaster *HubBroadcaster) Unsubscribe(ch chan string) {
	broadcaster.mutex.Lock()
	delete(broadcaster.subscribers, ch)
	close(ch)
	broadcaster.mutex.Unlock()
}

func (broadcaster *HubBroadcaster) Broadcast(payload string) {
	broadcaster.mutex.RLock()
	defer broadcaster.mutex.RUnlock()
	for sub := range broadcaster.subscribers {
		select {
		case sub <- payload:
		default:
		}
	}
}

type TelemetryAggregator struct {
	mutex       sync.RWMutex
	items       []UnifiedFeedItem
	seenUIDs    map[string]struct{}
	readUIDs    map[string]string
	health      map[string]ChannelHealthStatus
	broadcaster *HubBroadcaster
	endpoints   map[string][]string
}

func NewTelemetryAggregator(broadcaster *HubBroadcaster) *TelemetryAggregator {
	agg := &TelemetryAggregator{
		items:       make([]UnifiedFeedItem, 0, 256),
		seenUIDs:    make(map[string]struct{}),
		readUIDs:    make(map[string]string),
		health:      make(map[string]ChannelHealthStatus),
		broadcaster: broadcaster,
		endpoints: map[string][]string{
			"EMAIL":    resolveChannelURLs("EMAIL_SERVER_URL", "http://email-server:9081", "http://localhost:9081"),
			"SMS":      resolveChannelURLs("SMS_SERVER_URL", "http://sms-server:9082", "http://localhost:9082"),
			"WHATSAPP": resolveChannelURLs("WHATSAPP_SERVER_URL", "http://whatsapp-server:9083", "http://localhost:9083"),
			"TELEGRAM": resolveChannelURLs("TELEGRAM_SERVER_URL", "http://telegram-server:9084", "http://localhost:9084"),
		},
	}
	agg.health["EMAIL"] = ChannelHealthStatus{Channel: "EMAIL", Port: 9081}
	agg.health["SMS"] = ChannelHealthStatus{Channel: "SMS", Port: 9082}
	agg.health["WHATSAPP"] = ChannelHealthStatus{Channel: "WHATSAPP", Port: 9083}
	agg.health["TELEGRAM"] = ChannelHealthStatus{Channel: "TELEGRAM", Port: 9084}
	return agg
}

func resolveChannelURLs(envKey string, dockerURL string, localURL string) []string {
	if custom := strings.TrimSpace(os.Getenv(envKey)); custom != "" {
		return []string{strings.TrimRight(custom, "/"), dockerURL, localURL}
	}
	return []string{dockerURL, localURL}
}

func (agg *TelemetryAggregator) ActiveBaseURL(channel string) string {
	candidates := agg.endpoints[channel]
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	for _, base := range candidates {
		resp, err := client.Get(base + "/openapi.json")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode < 500 {
				return base
			}
		}
	}
	if len(candidates) > 0 {
		return candidates[len(candidates)-1]
	}
	return ""
}

func (agg *TelemetryAggregator) setConnected(channel string, port int, connected bool) {
	agg.mutex.Lock()
	defer agg.mutex.Unlock()
	st := agg.health[channel]
	st.Channel = channel
	st.Port = port
	st.Connected = connected
	if connected {
		st.LastSeen = time.Now().UTC().Format(time.RFC3339)
	}
	agg.health[channel] = st
}

func (agg *TelemetryAggregator) IngestNormalized(item UnifiedFeedItem, broadcast bool) bool {
	if item.ID == "" {
		return false
	}
	item.UID = item.Channel + ":" + item.ID

	agg.mutex.Lock()
	if seenAt, isRead := agg.readUIDs[item.UID]; isRead {
		item.Seen = true
		item.SeenAt = seenAt
	}
	if _, exists := agg.seenUIDs[item.UID]; exists {
		for idx := range agg.items {
			if agg.items[idx].UID == item.UID {
				item.Seen = agg.items[idx].Seen || item.Seen
				if item.SeenAt == "" {
					item.SeenAt = agg.items[idx].SeenAt
				}
				agg.items[idx] = item
				break
			}
		}
		agg.mutex.Unlock()
		return false
	}

	agg.seenUIDs[item.UID] = struct{}{}
	agg.items = append([]UnifiedFeedItem{item}, agg.items...)
	if len(agg.items) > 300 {
		evicted := agg.items[len(agg.items)-1]
		delete(agg.seenUIDs, evicted.UID)
		agg.items = agg.items[:300]
	}

	sort.SliceStable(agg.items, func(i, j int) bool {
		return agg.items[i].Timestamp > agg.items[j].Timestamp
	})

	st := agg.health[item.Channel]
	st.Channel = item.Channel
	st.Port = item.Port
	st.Connected = true
	st.LastSeen = time.Now().UTC().Format(time.RFC3339)
	st.TotalSeen++
	agg.health[item.Channel] = st
	agg.mutex.Unlock()

	if broadcast {
		if encoded, err := json.Marshal(item); err == nil {
			agg.broadcaster.Broadcast(string(encoded))
		}
	}
	return true
}

func (agg *TelemetryAggregator) Snapshot() FeedSnapshotResponse {
	agg.mutex.RLock()
	defer agg.mutex.RUnlock()

	copiedItems := make([]UnifiedFeedItem, len(agg.items))
	copy(copiedItems, agg.items)

	copiedSources := make(map[string]ChannelHealthStatus, len(agg.health))
	for k, v := range agg.health {
		copiedSources[k] = v
	}

	return FeedSnapshotResponse{
		Items:   copiedItems,
		Sources: copiedSources,
	}
}

func (agg *TelemetryAggregator) MarkSeen(uids []string, markAll bool) {
	now := time.Now().UTC().Format(time.RFC3339)
	var updatedItems []UnifiedFeedItem

	agg.mutex.Lock()
	if markAll {
		for idx := range agg.items {
			if !agg.items[idx].Seen {
				agg.items[idx].Seen = true
				agg.items[idx].SeenAt = now
				agg.readUIDs[agg.items[idx].UID] = now
				updatedItems = append(updatedItems, agg.items[idx])
			}
		}
	} else {
		for _, rawUID := range uids {
			uid := strings.TrimSpace(rawUID)
			if uid == "" {
				continue
			}
			agg.readUIDs[uid] = now
			for idx := range agg.items {
				if agg.items[idx].UID == uid && !agg.items[idx].Seen {
					agg.items[idx].Seen = true
					agg.items[idx].SeenAt = now
					updatedItems = append(updatedItems, agg.items[idx])
				}
			}
		}
	}
	agg.mutex.Unlock()

	for _, item := range updatedItems {
		if encoded, err := json.Marshal(item); err == nil {
			agg.broadcaster.Broadcast(string(encoded))
		}
	}
}

func (agg *TelemetryAggregator) ClearAll() {
	agg.mutex.Lock()
	agg.items = make([]UnifiedFeedItem, 0, 256)
	agg.seenUIDs = make(map[string]struct{})
	agg.readUIDs = make(map[string]string)
	for k, st := range agg.health {
		st.TotalSeen = 0
		agg.health[k] = st
	}
	agg.mutex.Unlock()
}

func (agg *TelemetryAggregator) StartCollectors() {
	go agg.collectChannelLoop("EMAIL", 9081, "/api/v1/emails", normalizeEmailPayload)
	go agg.collectChannelLoop("SMS", 9082, "/api/v1/sms", normalizeSMSPayload)
	go agg.collectChannelLoop("WHATSAPP", 9083, "/api/v1/whatsapp", normalizeWhatsAppPayload)
	go agg.collectChannelLoop("TELEGRAM", 9084, "/api/v1/telegram", normalizeTelegramPayload)
}

func (agg *TelemetryAggregator) PollAllSnapshots() {
	agg.fetchSnapshotOnce("EMAIL", 9081, "/api/v1/emails", normalizeEmailPayload)
	agg.fetchSnapshotOnce("SMS", 9082, "/api/v1/sms", normalizeSMSPayload)
	agg.fetchSnapshotOnce("WHATSAPP", 9083, "/api/v1/whatsapp", normalizeWhatsAppPayload)
	agg.fetchSnapshotOnce("TELEGRAM", 9084, "/api/v1/telegram", normalizeTelegramPayload)
}

func (agg *TelemetryAggregator) fetchSnapshotOnce(
	channel string,
	port int,
	listPath string,
	normalizer func(map[string]any) UnifiedFeedItem,
) {
	client := &http.Client{Timeout: 2 * time.Second}
	for _, base := range agg.endpoints[channel] {
		resp, err := client.Get(base + listPath)
		if err != nil {
			continue
		}
		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode >= 400 {
			continue
		}
		agg.setConnected(channel, port, true)
		var records []map[string]any
		if err := json.Unmarshal(bodyBytes, &records); err == nil {
			for i := len(records) - 1; i >= 0; i-- {
				item := normalizer(records[i])
				agg.IngestNormalized(item, false)
			}
		}
		return
	}
}

func (agg *TelemetryAggregator) collectChannelLoop(
	channel string,
	port int,
	listPath string,
	normalizer func(map[string]any) UnifiedFeedItem,
) {
	candidates := agg.endpoints[channel]
	idx := 0
	for {
		baseURL := candidates[idx%len(candidates)]
		idx++

		agg.fetchSnapshotOnce(channel, port, listPath, normalizer)

		req, err := http.NewRequest("GET", baseURL+"/events", nil)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		req.Header.Set("Accept", "text/event-stream")

		resp, err := (&http.Client{Timeout: 0}).Do(req)
		if err != nil || resp.StatusCode >= 400 {
			if resp != nil {
				_ = resp.Body.Close()
			}
			agg.setConnected(channel, port, false)
			time.Sleep(2 * time.Second)
			continue
		}

		agg.setConnected(channel, port, true)
		reader := bufio.NewReader(resp.Body)
		var dataLines []string

		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				break
			}
			trimmed := strings.TrimRight(line, "\r\n")
			if trimmed == "" {
				if len(dataLines) > 0 {
					rawJSON := strings.Join(dataLines, "\n")
					dataLines = nil
					var parsed map[string]any
					if err := json.Unmarshal([]byte(rawJSON), &parsed); err == nil {
						item := normalizer(parsed)
						agg.IngestNormalized(item, true)
					} else {
						agg.fetchSnapshotOnce(channel, port, listPath, normalizer)
					}
				}
				continue
			}
			if strings.HasPrefix(trimmed, "data:") {
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
			}
		}

		_ = resp.Body.Close()
		agg.setConnected(channel, port, false)
		time.Sleep(1500 * time.Millisecond)
	}
}

func extractCriteria(reportMap map[string]any) []UnifiedCriterion {
	rawList, ok := reportMap["criteria"].([]any)
	if !ok {
		return nil
	}
	out := make([]UnifiedCriterion, 0, len(rawList))
	for _, entry := range rawList {
		if m, ok := entry.(map[string]any); ok {
			out = append(out, UnifiedCriterion{
				Category: strVal(m["category"]),
				Target:   strVal(m["target"]),
				Status:   strVal(m["status"]),
				Details:  strVal(m["details"]),
			})
		}
	}
	return out
}

func strVal(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func normalizeEmailPayload(raw map[string]any) UnifiedFeedItem {
	report, _ := raw["report"].(map[string]any)
	criteria := extractCriteria(report)
	var recipients []string
	if rList, ok := raw["recipients"].([]any); ok {
		for _, r := range rList {
			recipients = append(recipients, strVal(r))
		}
	}
	htmlBody := strVal(raw["htmlBody"])
	plainPreview := stripHTMLTags(htmlBody)
	if len(plainPreview) > 140 {
		plainPreview = plainPreview[:140] + "..."
	}
	attCount := 0
	if atts, ok := raw["attachments"].([]any); ok {
		attCount = len(atts)
	}
	protoTag := fmt.Sprintf("HTML %dB", len(htmlBody))
	if attCount > 0 {
		protoTag = fmt.Sprintf("MIME • %d att", attCount)
	}

	return UnifiedFeedItem{
		ID:          strVal(raw["id"]),
		Channel:     "EMAIL",
		Port:        9081,
		Timestamp:   strVal(raw["timestamp"]),
		Source:      defaultStr(strVal(raw["source"]), "SMTP / HTTP"),
		Sender:      defaultStr(strVal(raw["sender"]), "unknown@sender"),
		Recipient:   defaultStr(strings.Join(recipients, ", "), "inbox@Sanndbox.local"),
		Title:       defaultStr(strVal(raw["subject"]), "(No Subject)"),
		Preview:     defaultStr(plainPreview, "HTML Email Payload"),
		ProtocolTag: protoTag,
		Status:      defaultStr(strVal(report["overallStatus"]), "PASS"),
		CheckCount:  len(criteria),
		Criteria:    criteria,
		RawPayload:  raw,
	}
}

func normalizeSMSPayload(raw map[string]any) UnifiedFeedItem {
	report, _ := raw["report"].(map[string]any)
	criteria := extractCriteria(report)
	encoding := defaultStr(strVal(report["encoding"]), "GSM-7")
	segCount := intVal(report["segmentCount"])
	totalUnits := intVal(report["totalUnits"])
	msgText := strVal(raw["message"])

	return UnifiedFeedItem{
		ID:          strVal(raw["id"]),
		Channel:     "SMS",
		Port:        9082,
		Timestamp:   strVal(raw["timestamp"]),
		Source:      defaultStr(strVal(raw["source"]), "HTTP :9082"),
		Sender:      defaultStr(strVal(raw["sender"]), "SMS-OTP"),
		Recipient:   defaultStr(strVal(raw["recipient"]), "+358401234567"),
		Title:       fmt.Sprintf("SMS PDU (%s • %d seg)", encoding, segCount),
		Preview:     msgText,
		ProtocolTag: fmt.Sprintf("%s • %d seg (%du)", encoding, segCount, totalUnits),
		Status:      defaultStr(strVal(report["overallStatus"]), "PASS"),
		CheckCount:  len(criteria),
		Criteria:    criteria,
		RawPayload:  raw,
	}
}

func normalizeWhatsAppPayload(raw map[string]any) UnifiedFeedItem {
	report, _ := raw["report"].(map[string]any)
	criteria := extractCriteria(report)
	eventType := defaultStr(strVal(raw["eventType"]), "text")
	direction := defaultStr(strVal(raw["direction"]), "OUTBOUND")
	textBody := strVal(raw["text"])

	title := fmt.Sprintf("WABA %s (%s)", strings.ToUpper(eventType), direction)
	if tpl, ok := raw["templatePreview"].(map[string]any); ok && strVal(tpl["name"]) != "" {
		title = fmt.Sprintf("Template: %s [%s]", strVal(tpl["name"]), defaultStr(strVal(tpl["category"]), "UTILITY"))
	}

	return UnifiedFeedItem{
		ID:          strVal(raw["id"]),
		Channel:     "WHATSAPP",
		Port:        9083,
		Timestamp:   strVal(raw["timestamp"]),
		Source:      defaultStr(strVal(raw["source"]), "Cloud API v21.0"),
		Sender:      defaultStr(strVal(raw["sender"]), "Jasper's Market"),
		Recipient:   defaultStr(strVal(raw["recipient"]), "+15550199882"),
		Title:       title,
		Preview:     textBody,
		ProtocolTag: fmt.Sprintf("WABA • %s", eventType),
		Status:      defaultStr(strVal(report["overallStatus"]), "PASS"),
		CheckCount:  len(criteria),
		Criteria:    criteria,
		RawPayload:  raw,
	}
}

func normalizeTelegramPayload(raw map[string]any) UnifiedFeedItem {
	report, _ := raw["report"].(map[string]any)
	criteria := extractCriteria(report)
	method := defaultStr(strVal(raw["method"]), "messages.sendMessage")
	tlReturn := defaultStr(strVal(report["tlReturnType"]), "Updates")
	msg := strVal(raw["message"])

	return UnifiedFeedItem{
		ID:          strVal(raw["id"]),
		Channel:     "TELEGRAM",
		Port:        9084,
		Timestamp:   strVal(raw["timestamp"]),
		Source:      defaultStr(strVal(raw["source"]), "MTProto :9084"),
		Sender:      defaultStr(strVal(raw["sender"]), "@FraudOpsMTProtoBot"),
		Recipient:   defaultStr(strVal(raw["peer"]), "@fraud_audit_ops"),
		Title:       fmt.Sprintf("MTProto %s", method),
		Preview:     msg,
		ProtocolTag: fmt.Sprintf("TL:%s", tlReturn),
		Status:      defaultStr(strVal(report["overallStatus"]), "PASS"),
		CheckCount:  len(criteria),
		Criteria:    criteria,
		RawPayload:  raw,
	}
}

func stripHTMLTags(input string) string {
	var out strings.Builder
	inTag := false
	for _, r := range input {
		switch {
		case r == '<':
			inTag = true
			out.WriteRune(' ')
		case r == '>':
			inTag = false
		case !inTag:
			out.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(out.String()), " ")
}

func defaultStr(val string, fallback string) string {
	if strings.TrimSpace(val) == "" {
		return fallback
	}
	return val
}

func intVal(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 1
	}
}

func (agg *TelemetryAggregator) DispatchSimulation(channel string) ([]UnifiedFeedItem, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	var dispatched []UnifiedFeedItem

	sendJSON := func(ch string, port int, path string, payload map[string]any, normalizer func(map[string]any) UnifiedFeedItem) error {
		base := agg.ActiveBaseURL(ch)
		rawBytes, _ := json.Marshal(payload)
		resp, err := client.Post(base+path, "application/json", bytes.NewReader(rawBytes))
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err == nil && strVal(decoded["id"]) != "" {
			item := normalizer(decoded)
			agg.IngestNormalized(item, true)
			dispatched = append(dispatched, item)
		} else {
			agg.PollAllSnapshots()
		}
		return nil
	}

	target := strings.ToUpper(strings.TrimSpace(channel))
	if target == "" || target == "ALL" || target == "EMAIL" {
		_ = sendJSON("EMAIL", 9081, "/api/v1/emails", map[string]any{
			"sender":     "orders@jaspersmarket.com",
			"recipients": []string{"ops@Sanndbox.local"},
			"subject":    "Order #JM-9042 Dispatched — Organic Produce Box",
			"htmlBody":   `<!DOCTYPE html><html><head><meta name="viewport" content="width=device-width, initial-scale=1.0"></head><body><table width="600" cellpadding="12"><tr><td><strong>Jasper's Market Order #JM-9042</strong> is out for delivery.</td></tr></table></body></html>`,
		}, normalizeEmailPayload)
	}

	if target == "" || target == "ALL" || target == "SMS" {
		_ = sendJSON("SMS", 9082, "/api/v1/sms", map[string]any{
			"sender":    "JASPERS-OTP",
			"recipient": "+358401234567",
			"message":   "Jasper's Market security code: 849201. Cost: 5 EUR. Valid for 10 minutes.",
		}, normalizeSMSPayload)
	}

	if target == "" || target == "ALL" || target == "WHATSAPP" {
		_ = sendJSON("WHATSAPP", 9083, "/api/v1/whatsapp", map[string]any{
			"direction": "OUTBOUND",
			"eventType": "template",
			"sender":    "Jasper's Market",
			"recipient": "+15550199882",
			"caseKey":   "CASE-WABA-2026-901",
			"text":      "Hi Alex, your Jasper's Market order #JM-9042 ($46.80) is out for delivery and arrives in 15 mins.",
		}, normalizeWhatsAppPayload)
	}

	if target == "" || target == "ALL" || target == "TELEGRAM" {
		_ = sendJSON("TELEGRAM", 9084, "/api/v1/telegram", map[string]any{
			"method":   "messages.sendMessage",
			"peer":     "@fraud_audit_ops",
			"sender":   "@FraudOpsMTProtoBot",
			"randomId": time.Now().UnixNano(),
			"message":  "[MTProto] Real-time rule check passed for case CASE-WABA-2026-901; DB counter synced.",
		}, normalizeTelegramPayload)
	}

	return dispatched, nil
}

func withCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Access-Control-Allow-Origin", "*")
		responseWriter.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		responseWriter.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if request.Method == http.MethodOptions {
			responseWriter.WriteHeader(http.StatusNoContent)
			return
		}
		next(responseWriter, request)
	}
}

func main() {
	parsedTemplates := template.Must(template.ParseFS(templateFS, "templates/index.html"))
	broadcaster := NewHubBroadcaster()
	aggregator := NewTelemetryAggregator(broadcaster)
	aggregator.StartCollectors()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = parsedTemplates.ExecuteTemplate(responseWriter, "index.html", nil)
	})

	mux.HandleFunc("GET /api/v1/feed", withCORS(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("refresh") == "1" {
			aggregator.PollAllSnapshots()
		}
		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(aggregator.Snapshot())
	}))

	mux.HandleFunc("POST /api/v1/seen", withCORS(func(responseWriter http.ResponseWriter, request *http.Request) {
		var payload struct {
			UID     string   `json:"uid"`
			UIDs    []string `json:"uids"`
			Channel string   `json:"channel"`
			ID      string   `json:"id"`
			All     bool     `json:"all"`
		}
		_ = json.NewDecoder(request.Body).Decode(&payload)
		var list []string
		if payload.UID != "" {
			list = append(list, payload.UID)
		}
		if payload.Channel != "" && payload.ID != "" {
			list = append(list, strings.ToUpper(payload.Channel)+":"+payload.ID)
		}
		list = append(list, payload.UIDs...)
		aggregator.MarkSeen(list, payload.All)
		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(map[string]any{"ok": true})
	}))

	mux.HandleFunc("OPTIONS /api/v1/seen", withCORS(func(responseWriter http.ResponseWriter, request *http.Request) {}))

	mux.HandleFunc("POST /api/v1/feed/clear", func(responseWriter http.ResponseWriter, request *http.Request) {
		aggregator.ClearAll()
		responseWriter.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/v1/simulate", func(responseWriter http.ResponseWriter, request *http.Request) {
		var req struct {
			Channel string `json:"channel"`
		}
		_ = json.NewDecoder(request.Body).Decode(&req)
		items, _ := aggregator.DispatchSimulation(req.Channel)
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(responseWriter).Encode(map[string]any{
			"dispatched": items,
			"snapshot":   aggregator.Snapshot(),
		})
	})

	mux.HandleFunc("GET /events", func(responseWriter http.ResponseWriter, request *http.Request) {
		flusher, ok := responseWriter.(http.Flusher)
		if !ok {
			http.Error(responseWriter, "Streaming unsupported", http.StatusInternalServerError)
			return
		}
		responseWriter.Header().Set("Content-Type", "text/event-stream")
		responseWriter.Header().Set("Cache-Control", "no-cache")
		responseWriter.Header().Set("Connection", "keep-alive")
		responseWriter.Header().Set("Access-Control-Allow-Origin", "*")

		sub := broadcaster.Subscribe()
		defer broadcaster.Unsubscribe(sub)

		_, _ = fmt.Fprintf(responseWriter, ": connected\n\n")
		flusher.Flush()

		for {
			select {
			case <-request.Context().Done():
				return
			case payload := <-sub:
				_, _ = fmt.Fprintf(responseWriter, "event: communication-event\ndata: %s\n\n", payload)
				flusher.Flush()
			}
		}
	})

	mux.HandleFunc("GET /openapi.json", func(responseWriter http.ResponseWriter, request *http.Request) {
		specBytes, _ := templateFS.ReadFile("docs/swagger.json")
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write(specBytes)
	})

	mux.HandleFunc("GET /docs", func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(responseWriter, `<!DOCTYPE html><html><head><meta charset="UTF-8"><title>Portal Observability API</title><link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui.css"></head><body><div id="swagger-ui"></div><script src="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"></script><script>window.onload=()=>{SwaggerUIBundle({url:'/openapi.json',dom_id:'#swagger-ui'})};</script></body></html>`)
	})

	fmt.Println("[portal-server] Grafana-Inspired Unified Observability Hub listening on http://localhost:9080")
	_ = http.ListenAndServe(":9080", mux)
}
