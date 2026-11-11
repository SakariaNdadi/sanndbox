// SPDX-License-Identifier: Apache-2.0
// Portions adapted from https://github.com/fbsamples/whatsapp-business-jaspers-market
// Copyright (c) Meta Platforms, Inc. and affiliates.
// Modified: Ported from Node.js to Go for the Communications Sanndbox System.
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

//go:embed templates/index.html docs/swagger.json
var templateFS embed.FS

// @title WhatsApp Business Cloud API & Jasper's Market Sanndbox
// @version 2.0.0
// @description Meta WhatsApp Business Platform Sanndbox (:9083) modeled after fbsamples/whatsapp-business-jaspers-market/services. Implements Graph API /{apiVersion}/{phoneNumberId}/messages, /{wabaId}/message_templates, /{phoneNumberId}/media, /webhook (GET verify + POST X-Hub-Signature-256), entry_source_attributed telemetry, 15s status follow-up cache, WABA Setup & .env generator, and Template Studio.
// @BasePath /

const (
	UserStartMessage             = "Get started"
	UserTryOutMessage            = "Try it out"
	AppDefaultMessage            = "Welcome to Jasper's Market! What can we help you with today?"
	AppTryAnotherMessage         = "Is there anything else we can help you with?"
	ReplyInteractiveWithMediaCTA = "Shop online"
	ReplyMediaCardCarouselCTA    = "Get recipe ideas"
	ReplyOfferCTA                = "Current promo"
	ReplyInteractiveMediaID      = "reply-interactive-with-media"
	ReplyMediaCarouselID         = "reply-media-card-carousel"
	ReplyOfferID                 = "reply-offer"
	MediaIDPlaceholder           = "1234567890"
	maxWhatsAppTextCharacters    = 4096
	maxImageSizeBytes            = 16 * 1024 * 1024
	maxVideoSizeBytes            = 16 * 1024 * 1024
	maxAudioSizeBytes            = 16 * 1024 * 1024
	maxStickerSizeBytes          = 500 * 1024
	maxDocSizeBytes              = 100 * 1024 * 1024
)

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

type IMEvaluationReport struct {
	OverallStatus  EvaluationStatus      `json:"overallStatus"`
	CharacterCount int                   `json:"characterCount"`
	EventType      string                `json:"eventType"`
	Criteria       []EvaluationCriterion `json:"criteria"`
}

type IMMediaAttachment struct {
	ID         string `json:"id,omitempty"`
	Filename   string `json:"filename"`
	MIMEType   string `json:"mimeType"`
	SizeBytes  int64  `json:"sizeBytes"`
	PreviewURL string `json:"previewUrl,omitempty"`
}

type InteractiveReplyButton struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type TemplateCarouselCard struct {
	CardIndex  int    `json:"cardIndex"`
	ImageID    string `json:"imageId"`
	PreviewURL string `json:"previewUrl,omitempty"`
	BodyText   string `json:"bodyText"`
	ButtonURL  string `json:"buttonUrl"`
	ButtonCTA  string `json:"buttonCta"`
}

type TemplatePreviewData struct {
	Name                      string                 `json:"name"`
	Language                  string                 `json:"language"`
	Category                  string                 `json:"category"`
	HeaderFormat              string                 `json:"headerFormat,omitempty"`
	HeaderText                string                 `json:"headerText,omitempty"`
	HeaderMediaID             string                 `json:"headerMediaId,omitempty"`
	HeaderPreviewURL          string                 `json:"headerPreviewUrl,omitempty"`
	DocumentFilename          string                 `json:"documentFilename,omitempty"`
	BodyText                  string                 `json:"bodyText"`
	BodyParams                []string               `json:"bodyParams,omitempty"`
	FooterText                string                 `json:"footerText,omitempty"`
	IsLTO                     bool                   `json:"isLto,omitempty"`
	LTOText                   string                 `json:"ltoText,omitempty"`
	OfferCode                 string                 `json:"offerCode,omitempty"`
	ExpirationMs              int64                  `json:"expirationMs,omitempty"`
	OTPCode                   string                 `json:"otpCode,omitempty"`
	OTPType                   string                 `json:"otpType,omitempty"`
	AutofillText              string                 `json:"autofillText,omitempty"`
	CodeExpirationMinutes     int                    `json:"codeExpirationMinutes,omitempty"`
	AddSecurityRecommendation bool                   `json:"addSecurityRecommendation,omitempty"`
	PackageName               string                 `json:"packageName,omitempty"`
	SignatureHash             string                 `json:"signatureHash,omitempty"`
	ButtonCTA                 string                 `json:"buttonCta,omitempty"`
	ButtonURL                 string                 `json:"buttonUrl,omitempty"`
	QuickReplyButtons         []string               `json:"quickReplyButtons,omitempty"`
	CarouselCards             []TemplateCarouselCard `json:"carouselCards,omitempty"`
}

type AttributionEvent struct {
	EventName             string `json:"event_name"`
	SchemaVersion         int    `json:"schema_version"`
	SourceSurface         string `json:"source_surface"`
	InboundMessageID      string `json:"inbound_message_id"`
	ReceiverPhoneNumberID string `json:"receiver_phone_number_id"`
	EventTimestamp        string `json:"event_timestamp"`
	WebhookDeliveryCount  int    `json:"webhook_delivery_count"`
}

type IMSubmissionRequest struct {
	SessionID          string                   `json:"sessionId,omitempty"`
	Direction          string                   `json:"direction,omitempty"`
	Platform           string                   `json:"platform,omitempty"`
	EventType          string                   `json:"eventType,omitempty"`
	CaseKey            string                   `json:"caseKey,omitempty"`
	Sender             string                   `json:"sender"`
	Recipient          string                   `json:"recipient"`
	Text               string                   `json:"text"`
	Media              *IMMediaAttachment       `json:"media,omitempty"`
	InteractiveButtons []InteractiveReplyButton `json:"interactiveButtons,omitempty"`
	TemplatePreview    *TemplatePreviewData     `json:"templatePreview,omitempty"`
	RawGraphPayload    string                   `json:"rawGraphPayload,omitempty"`
	Attribution        *AttributionEvent        `json:"attribution,omitempty"`
	SignatureStatus    string                   `json:"signatureStatus,omitempty"`
}

type IMRecord struct {
	ID                 string                   `json:"id"`
	WAMID              string                   `json:"wamid"`
	SessionID          string                   `json:"sessionId,omitempty"`
	Timestamp          string                   `json:"timestamp"`
	Direction          string                   `json:"direction"`
	Source             string                   `json:"source"`
	Platform           string                   `json:"platform"`
	EventType          string                   `json:"eventType"`
	CaseKey            string                   `json:"caseKey,omitempty"`
	DeliveryStatus     string                   `json:"deliveryStatus"`
	FollowUpArmed      bool                     `json:"followUpArmed"`
	Sender             string                   `json:"sender"`
	Recipient          string                   `json:"recipient"`
	Text               string                   `json:"text"`
	Media              *IMMediaAttachment       `json:"media,omitempty"`
	InteractiveButtons []InteractiveReplyButton `json:"interactiveButtons,omitempty"`
	TemplatePreview    *TemplatePreviewData     `json:"templatePreview,omitempty"`
	RawGraphPayload    string                   `json:"rawGraphPayload,omitempty"`
	Attribution        *AttributionEvent        `json:"attribution,omitempty"`
	Report             IMEvaluationReport       `json:"report"`
}

type WABASanndboxConfig struct {
	AppID                 string            `json:"appId"`
	AppSecret             string            `json:"appSecret"`
	AccessToken           string            `json:"accessToken"`
	VerifyToken           string            `json:"verifyToken"`
	WABAID                string            `json:"wabaId"`
	PhoneNumberID         string            `json:"phoneNumberId"`
	APIVersion            string            `json:"apiVersion"`
	GraphBaseURL          string            `json:"graphBaseUrl"`
	GroceriesMediaID      string            `json:"groceriesMediaId"`
	StrawberriesMediaID   string            `json:"strawberriesMediaId"`
	SheetPanDinnerMediaID string            `json:"sheetPanDinnerMediaId"`
	SaladBowlMediaID      string            `json:"saladBowlMediaId"`
	ValidationErrors      []string          `json:"validationErrors"`
	IsReady               bool              `json:"isReady"`
	EnvFilePreview        string            `json:"envFilePreview"`
	TemplateScriptPreview string            `json:"templateScriptPreview"`
	MediaAssets           []WABAMediaRecord `json:"mediaAssets"`
}

type WABAMediaRecord struct {
	MediaID       string `json:"mediaId"`
	EnvKey        string `json:"envKey,omitempty"`
	Filename      string `json:"filename"`
	MIMEType      string `json:"mimeType"`
	SizeBytes     int64  `json:"sizeBytes"`
	PreviewURL    string `json:"previewUrl,omitempty"`
	PhoneNumberID string `json:"phoneNumberId"`
	UploadHandle  string `json:"uploadHandle"`
	ExpiresAt     string `json:"expiresAt"`
}

type WABATemplateDefinition struct {
	ID                        string                 `json:"id"`
	Name                      string                 `json:"name"`
	Language                  string                 `json:"language"`
	Category                  string                 `json:"category"`
	Status                    string                 `json:"status"`
	HeaderFormat              string                 `json:"headerFormat"`
	HeaderText                string                 `json:"headerText,omitempty"`
	HeaderMediaID             string                 `json:"headerMediaId"`
	HeaderPreviewURL          string                 `json:"headerPreviewUrl,omitempty"`
	DocumentFilename          string                 `json:"documentFilename,omitempty"`
	BodyText                  string                 `json:"bodyText"`
	BodyParams                []string               `json:"bodyParams,omitempty"`
	FooterText                string                 `json:"footerText"`
	IsLTO                     bool                   `json:"isLto"`
	LTOText                   string                 `json:"ltoText"`
	OfferCode                 string                 `json:"offerCode"`
	OTPCode                   string                 `json:"otpCode,omitempty"`
	OTPType                   string                 `json:"otpType,omitempty"`
	AutofillText              string                 `json:"autofillText,omitempty"`
	CodeExpirationMinutes     int                    `json:"codeExpirationMinutes,omitempty"`
	AddSecurityRecommendation bool                   `json:"addSecurityRecommendation,omitempty"`
	PackageName               string                 `json:"packageName,omitempty"`
	SignatureHash             string                 `json:"signatureHash,omitempty"`
	ButtonCTA                 string                 `json:"buttonCta"`
	ButtonURL                 string                 `json:"buttonUrl"`
	QuickReplyButtons         []string               `json:"quickReplyButtons,omitempty"`
	CarouselCards             []TemplateCarouselCard `json:"carouselCards,omitempty"`
	RawComponents             any                    `json:"rawComponents,omitempty"`
}

var supportedWhatsAppEventTypes = map[string]string{
	"text":        "Standard text message (up to 4096 chars)",
	"image":       "Image attachment (JPEG/PNG up to 16MB, 1024-char caption)",
	"document":    "Document attachment (PDF/DOCX/CSV up to 100MB, 1024-char caption)",
	"audio":       "Audio message / voice note (OGG/MP3/AAC up to 16MB)",
	"video":       "Video attachment (MP4/3GP up to 16MB, 1024-char caption)",
	"sticker":     "WebP sticker payload (image/webp up to 500KB)",
	"location":    "Geographic coordinates payload (latitude/longitude/name)",
	"interactive": "Interactive reply buttons / list message (max 1024 body chars, max 3 buttons)",
	"template":    "Pre-approved HSM / LTO / Media Card Carousel business template",
	"reaction":    "Emoji reaction linked to a message_id",
}

var supportedIMMediaTypes = map[string]string{
	"image/jpeg":      "image",
	"image/jpg":       "image",
	"image/png":       "image",
	"image/webp":      "sticker",
	"video/mp4":       "video",
	"audio/ogg":       "audio",
	"audio/mpeg":      "audio",
	"application/pdf": "document",
	"text/csv":        "document",
}

type WhatsAppConstraintEvaluator struct{}

func NewPlatformConstraintEvaluator() *WhatsAppConstraintEvaluator {
	return &WhatsAppConstraintEvaluator{}
}

func (evaluator *WhatsAppConstraintEvaluator) Evaluate(submission IMSubmissionRequest, cfg WABASanndboxConfig) IMEvaluationReport {
	charCount := utf8.RuneCountInString(submission.Text)
	var criteria []EvaluationCriterion

	eventType := strings.ToLower(strings.TrimSpace(submission.EventType))
	if eventType == "" {
		if submission.TemplatePreview != nil {
			eventType = "template"
		} else if len(submission.InteractiveButtons) > 0 {
			eventType = "interactive"
		} else if submission.Media != nil {
			if category, ok := supportedIMMediaTypes[submission.Media.MIMEType]; ok {
				eventType = category
			} else {
				eventType = "document"
			}
		} else {
			eventType = "text"
		}
	}

	if eventDescription, validEvent := supportedWhatsAppEventTypes[eventType]; validEvent {
		criteria = append(criteria, EvaluationCriterion{
			Category: "WABA Event Type",
			Target:   "messages." + eventType,
			Status:   StatusPass,
			Details:  fmt.Sprintf("Official WhatsApp Cloud API event '%s': %s", eventType, eventDescription),
		})
	} else {
		criteria = append(criteria, EvaluationCriterion{
			Category: "WABA Event Type",
			Target:   eventType,
			Status:   StatusFail,
			Details:  fmt.Sprintf("Unrecognized WhatsApp event type '%s'", eventType),
		})
	}

	switch submission.SignatureStatus {
	case "VALID":
		criteria = append(criteria, EvaluationCriterion{
			Category: "Webhook HMAC (app.js)",
			Target:   "X-Hub-Signature-256",
			Status:   StatusPass,
			Details:  "HMAC-SHA256 signature verified against APP_SECRET",
		})
	case "INVALID":
		criteria = append(criteria, EvaluationCriterion{
			Category: "Webhook HMAC (app.js)",
			Target:   "X-Hub-Signature-256",
			Status:   StatusFail,
			Details:  "Signature mismatch against APP_SECRET; webhook rejected in production",
		})
	}

	if submission.Attribution != nil {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Source Attribution",
			Target:   submission.Attribution.SourceSurface,
			Status:   StatusPass,
			Details:  fmt.Sprintf("Attributed '%s' (wamid=%s, deliveries=%d)", submission.Attribution.SourceSurface, submission.Attribution.InboundMessageID, submission.Attribution.WebhookDeliveryCount),
		})
	}

	if submission.CaseKey != "" {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Audit Case Grouping",
			Target:   submission.CaseKey,
			Status:   StatusPass,
			Details:  fmt.Sprintf("Case key '%s' attached; subsequent hits group under existing investigation case", submission.CaseKey),
		})
	} else if strings.Contains(strings.ToLower(submission.Text), "case key") || strings.Contains(strings.ToLower(submission.Text), "fraud") {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Audit Case Grouping",
			Target:   "Legacy Alert Back-Fill",
			Status:   StatusWarn,
			Details:  "No caseKey supplied: legacy alert triggers one fresh alert on next hit before normal grouping applies",
		})
	}

	if len(submission.InteractiveButtons) > 0 {
		if len(submission.InteractiveButtons) > 3 {
			criteria = append(criteria, EvaluationCriterion{
				Category: "Interactive Reply CTA",
				Target:   "Max 3 Reply Buttons",
				Status:   StatusFail,
				Details:  fmt.Sprintf("Interactive message has %d buttons; WhatsApp Cloud API permits max 3 reply buttons", len(submission.InteractiveButtons)),
			})
		} else {
			criteria = append(criteria, EvaluationCriterion{
				Category: "Interactive Reply CTA",
				Target:   fmt.Sprintf("%d Reply Buttons", len(submission.InteractiveButtons)),
				Status:   StatusPass,
				Details:  "Interactive reply buttons conform to GraphApi.messageWithInteractiveReply schema",
			})
		}
	}

	if submission.TemplatePreview != nil {
		tpl := submission.TemplatePreview
		if tpl.HeaderMediaID == MediaIDPlaceholder || strings.HasPrefix(tpl.HeaderMediaID, "ADD_") {
			criteria = append(criteria, EvaluationCriterion{
				Category: "Template Media ID",
				Target:   tpl.Name,
				Status:   StatusFail,
				Details:  fmt.Sprintf("Template '%s' uses placeholder media ID '%s'; config.checkEnvVariables() rejects placeholders", tpl.Name, tpl.HeaderMediaID),
			})
		} else {
			criteria = append(criteria, EvaluationCriterion{
				Category: "Template Payload",
				Target:   tpl.Name + " (" + tpl.Language + ")",
				Status:   StatusPass,
				Details:  fmt.Sprintf("Template '%s' [%s] validated with phone-scoped runtime media IDs", tpl.Name, strings.ToUpper(tpl.Category)),
			})
		}

		if strings.EqualFold(tpl.Category, "UTILITY") {
			lowerBody := strings.ToLower(tpl.BodyText)
			if tpl.IsLTO || tpl.OfferCode != "" {
				criteria = append(criteria, EvaluationCriterion{
					Category: "Utility Category Policy",
					Target:   "Non-Promotional Rule",
					Status:   StatusFail,
					Details:  "UTILITY templates cannot contain Limited-Time Offers (LTO) or coupon copy_code buttons; Meta rejects or reclassifies to MARKETING",
				})
			} else if strings.Contains(lowerBody, "% off") || strings.Contains(lowerBody, "discount") || strings.Contains(lowerBody, "flash sale") || strings.Contains(lowerBody, "coupon") || strings.Contains(lowerBody, "buy now") {
				criteria = append(criteria, EvaluationCriterion{
					Category: "Utility Category Policy",
					Target:   "Category Auto-Migration",
					Status:   StatusWarn,
					Details:  "UTILITY template contains promotional upsell phrasing; Meta automated review reclassifies UTILITY -> MARKETING pricing tier",
				})
			} else {
				criteria = append(criteria, EvaluationCriterion{
					Category: "Utility Category Policy",
					Target:   "Transactional Scope",
					Status:   StatusPass,
					Details:  "Strictly transactional content tied to an active order, delivery, appointment, or account alert (conforms to Meta Utility policy)",
				})
			}

			if strings.Contains(submission.Text, "{{") && strings.Contains(submission.Text, "}}") {
				criteria = append(criteria, EvaluationCriterion{
					Category: "Utility Template Variables",
					Target:   "Positional {{1}}..{{n}}",
					Status:   StatusFail,
					Details:  "Unresolved {{n}} variable placeholder in body; Graph API rejects with (#132000) Number of parameters does not match",
				})
			} else if len(tpl.BodyParams) > 0 {
				criteria = append(criteria, EvaluationCriterion{
					Category: "Utility Template Variables",
					Target:   fmt.Sprintf("%d Runtime Parameter(s)", len(tpl.BodyParams)),
					Status:   StatusPass,
					Details:  fmt.Sprintf("Interpolated transactional variables [%s] into template body", strings.Join(tpl.BodyParams, ", ")),
				})
			}
		}

		if strings.EqualFold(tpl.Category, "AUTHENTICATION") {
			if tpl.HeaderFormat == "IMAGE" || tpl.HeaderFormat == "DOCUMENT" || tpl.HeaderFormat == "CAROUSEL" || tpl.IsLTO {
				criteria = append(criteria, EvaluationCriterion{
					Category: "Authentication OTP Policy",
					Target:   "Media & Header Prohibition",
					Status:   StatusFail,
					Details:  "AUTHENTICATION templates strictly forbid media headers (IMAGE/DOCUMENT/VIDEO), carousels, and LTO components",
				})
			} else {
				criteria = append(criteria, EvaluationCriterion{
					Category: "Authentication OTP Policy",
					Target:   "Preset Authentication Structure",
					Status:   StatusPass,
					Details:  "Complies with Meta fixed-text authentication structure (no media headers, URLs, or promotional text)",
				})
			}

			otpValue := tpl.OTPCode
			if otpValue == "" && len(tpl.BodyParams) > 0 {
				otpValue = tpl.BodyParams[0]
			}
			otpType := tpl.OTPType
			if otpType == "" {
				otpType = "COPY_CODE"
			}
			if otpValue == "" {
				criteria = append(criteria, EvaluationCriterion{
					Category: "OTP Parameter Validation",
					Target:   "{{1}} Passcode Value",
					Status:   StatusFail,
					Details:  "Missing OTP code parameter for AUTHENTICATION template body and button component",
				})
			} else if len(otpValue) > 15 || strings.Contains(otpValue, " ") {
				criteria = append(criteria, EvaluationCriterion{
					Category: "OTP Parameter Validation",
					Target:   "Max 15 Alphanumeric Chars",
					Status:   StatusFail,
					Details:  fmt.Sprintf("OTP code '%s' (%d chars) violates Meta 15-character no-whitespace authentication limit", otpValue, len(otpValue)),
				})
			} else {
				criteria = append(criteria, EvaluationCriterion{
					Category: "OTP Parameter Validation",
					Target:   fmt.Sprintf("OTP '%s' (%s)", otpValue, otpType),
					Status:   StatusPass,
					Details:  fmt.Sprintf("Valid %d-char verification code bound to %s authentication button", len(otpValue), otpType),
				})
			}

			if tpl.CodeExpirationMinutes > 90 || tpl.CodeExpirationMinutes < 0 {
				criteria = append(criteria, EvaluationCriterion{
					Category: "OTP Expiration Window",
					Target:   "1..90 Minutes Limit",
					Status:   StatusFail,
					Details:  fmt.Sprintf("code_expiration_minutes (%d) outside Meta allowed range (1 to 90 minutes)", tpl.CodeExpirationMinutes),
				})
			} else if tpl.CodeExpirationMinutes > 0 {
				criteria = append(criteria, EvaluationCriterion{
					Category: "OTP Expiration Window",
					Target:   fmt.Sprintf("%d Minutes TTL", tpl.CodeExpirationMinutes),
					Status:   StatusPass,
					Details:  fmt.Sprintf("Footer displays 'This code expires in %d minutes.' per Meta Authentication spec", tpl.CodeExpirationMinutes),
				})
			}

			if strings.EqualFold(otpType, "ONE_TAP") {
				if tpl.PackageName == "" || len(tpl.SignatureHash) != 11 {
					criteria = append(criteria, EvaluationCriterion{
						Category: "One-Tap Autofill Handshake",
						Target:   "package_name + signature_hash",
						Status:   StatusFail,
						Details:  "ONE_TAP authentication requires valid Android package_name and exact 11-character app signature_hash",
					})
				} else {
					criteria = append(criteria, EvaluationCriterion{
						Category: "One-Tap Autofill Handshake",
						Target:   tpl.PackageName + " (" + tpl.SignatureHash + ")",
						Status:   StatusPass,
						Details:  "Verified Android package_name and 11-char key hash for zero/one-tap app autofill",
					})
				}
			}
		}
	}

	if charCount == 0 && submission.Media == nil && submission.TemplatePreview == nil && eventType != "location" && eventType != "reaction" {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Text Constraints",
			Target:   "4096 Character Limit",
			Status:   StatusFail,
			Details:  "Payload contains neither text, template, nor media attachment",
		})
	} else if eventType == "interactive" && charCount > 1024 {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Text Constraints",
			Target:   "Interactive Body (1024 Chars)",
			Status:   StatusFail,
			Details:  fmt.Sprintf("Interactive body length (%d chars) exceeds WhatsApp 1024 character limit", charCount),
		})
	} else if charCount > maxWhatsAppTextCharacters {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Text Constraints",
			Target:   "4096 Character Limit",
			Status:   StatusFail,
			Details:  fmt.Sprintf("Text length (%d chars) exceeds WhatsApp hard 4096 character limit", charCount),
		})
	} else if charCount > 3500 {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Text Constraints",
			Target:   "4096 Character Limit",
			Status:   StatusWarn,
			Details:  fmt.Sprintf("Text length (%d/4096 chars) approaching truncation boundary", charCount),
		})
	} else {
		criteria = append(criteria, EvaluationCriterion{
			Category: "Text Constraints",
			Target:   "4096 Character Limit",
			Status:   StatusPass,
			Details:  fmt.Sprintf("Text length (%d/4096 chars) within WhatsApp Cloud API specification", charCount),
		})
	}

	if submission.Media != nil {
		mediaCategory, allowed := supportedIMMediaTypes[submission.Media.MIMEType]
		if !allowed {
			criteria = append(criteria, EvaluationCriterion{
				Category: "Media Validation",
				Target:   submission.Media.Filename,
				Status:   StatusFail,
				Details:  fmt.Sprintf("Unsupported MIME type '%s' for WhatsApp Cloud API", submission.Media.MIMEType),
			})
		} else {
			criteria = append(criteria, EvaluationCriterion{
				Category: "Media Validation",
				Target:   submission.Media.Filename,
				Status:   StatusPass,
				Details:  fmt.Sprintf("MIME type '%s' (%s) permitted by WhatsApp Cloud API", submission.Media.MIMEType, mediaCategory),
			})

			switch mediaCategory {
			case "sticker":
				if submission.Media.SizeBytes > maxStickerSizeBytes {
					criteria = append(criteria, EvaluationCriterion{
						Category: "Size Limits",
						Target:   "500KB Sticker Ceiling",
						Status:   StatusFail,
						Details:  fmt.Sprintf("Sticker size (%d bytes) exceeds 500KB WhatsApp limit", submission.Media.SizeBytes),
					})
				} else {
					criteria = append(criteria, EvaluationCriterion{
						Category: "Size Limits",
						Target:   "500KB Sticker Ceiling",
						Status:   StatusPass,
						Details:  fmt.Sprintf("Sticker size (%d bytes) within 500KB limit", submission.Media.SizeBytes),
					})
				}
			case "image", "video", "audio":
				if submission.Media.SizeBytes > maxImageSizeBytes {
					criteria = append(criteria, EvaluationCriterion{
						Category: "Size Limits",
						Target:   "16MB Media Ceiling",
						Status:   StatusFail,
						Details:  fmt.Sprintf("%s size (%d bytes) exceeds 16MB (16,777,216 bytes) WhatsApp limit", mediaCategory, submission.Media.SizeBytes),
					})
				} else if submission.Media.SizeBytes > 12*1024*1024 {
					criteria = append(criteria, EvaluationCriterion{
						Category: "Size Limits",
						Target:   "16MB Media Ceiling",
						Status:   StatusWarn,
						Details:  fmt.Sprintf("%s size (%d bytes) exceeds 12MB; approaches 16MB ceiling", mediaCategory, submission.Media.SizeBytes),
					})
				} else {
					criteria = append(criteria, EvaluationCriterion{
						Category: "Size Limits",
						Target:   "16MB Media Ceiling",
						Status:   StatusPass,
						Details:  fmt.Sprintf("%s size (%d bytes) within 16MB limit", mediaCategory, submission.Media.SizeBytes),
					})
				}
			case "document":
				if submission.Media.SizeBytes > maxDocSizeBytes {
					criteria = append(criteria, EvaluationCriterion{
						Category: "Size Limits",
						Target:   "100MB Document Ceiling",
						Status:   StatusFail,
						Details:  fmt.Sprintf("Document size (%d bytes) exceeds 100MB WhatsApp limit", submission.Media.SizeBytes),
					})
				} else {
					criteria = append(criteria, EvaluationCriterion{
						Category: "Size Limits",
						Target:   "100MB Document Ceiling",
						Status:   StatusPass,
						Details:  fmt.Sprintf("Document size (%d bytes) within 100MB limit", submission.Media.SizeBytes),
					})
				}
			}

			if charCount > 1024 {
				criteria = append(criteria, EvaluationCriterion{
					Category: "Media Caption Limit",
					Target:   "1024 Caption Chars",
					Status:   StatusWarn,
					Details:  fmt.Sprintf("Media caption (%d chars) exceeds 1024-char caption limit; requires split message", charCount),
				})
			}
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

	return IMEvaluationReport{
		OverallStatus:  overall,
		CharacterCount: charCount,
		EventType:      eventType,
		Criteria:       criteria,
	}
}

func interpolateTemplateParams(rawText string, params []string) string {
	result := rawText
	for idx, val := range params {
		placeholder := fmt.Sprintf("{{%d}}", idx+1)
		result = strings.ReplaceAll(result, placeholder, val)
	}
	return result
}

type SanndboxStateManager struct {
	mutex              sync.RWMutex
	config             WABASanndboxConfig
	templates          []WABATemplateDefinition
	followUpCache      map[string]time.Time
	attributionCounter map[string]int
}

func loadDotEnvFile(filePath string) {
	rawBytes, err := os.ReadFile(filePath)
	if err != nil {
		return
	}
	for _, rawLine := range strings.Split(string(rawBytes), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		envKey := strings.TrimSpace(key)
		envVal := strings.Trim(strings.TrimSpace(val), `"'`)
		if envKey != "" && os.Getenv(envKey) == "" {
			_ = os.Setenv(envKey, envVal)
		}
	}
}

func envOrDefault(key string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func NewSanndboxStateManager() *SanndboxStateManager {
	loadDotEnvFile(".env")
	expiry := time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339)
	groceriesImg := "https://images.unsplash.com/photo-1542838132-92c53300491e?auto=format&fit=crop&w=900&q=80"
	strawberriesImg := "https://images.unsplash.com/photo-1464965911861-746a04b4bca6?auto=format&fit=crop&w=900&q=80"
	sheetPanImg := "https://images.unsplash.com/photo-1544025162-d76694265947?auto=format&fit=crop&w=640&q=80"
	saladBowlImg := "https://images.unsplash.com/photo-1512621776951-a57141f2eefd?auto=format&fit=crop&w=640&q=80"

	initialMedia := []WABAMediaRecord{
		{
			MediaID:       "media_groceries_8841209",
			EnvKey:        "GROCERIES_MEDIA_ID",
			Filename:      "groceries.jpg",
			MIMEType:      "image/jpeg",
			SizeBytes:     412800,
			PreviewURL:    groceriesImg,
			PhoneNumberID: "106540352242922",
			UploadHandle:  "4::aW1hZ2UvanBlZw==:groceries_h",
			ExpiresAt:     expiry,
		},
		{
			MediaID:       "media_strawberries_9920144",
			EnvKey:        "STRAWBERRIES_MEDIA_ID",
			Filename:      "strawberries.jpg",
			MIMEType:      "image/jpeg",
			SizeBytes:     389120,
			PreviewURL:    strawberriesImg,
			PhoneNumberID: "106540352242922",
			UploadHandle:  "4::aW1hZ2UvanBlZw==:strawberries_h",
			ExpiresAt:     expiry,
		},
		{
			MediaID:       "media_sheet_pan_7731902",
			EnvKey:        "SHEET_PAN_DINNER_MEDIA_ID",
			Filename:      "sheet_pan_dinner.jpg",
			MIMEType:      "image/jpeg",
			SizeBytes:     518400,
			PreviewURL:    sheetPanImg,
			PhoneNumberID: "106540352242922",
			UploadHandle:  "4::aW1hZ2UvanBlZw==:sheet_pan_h",
			ExpiresAt:     expiry,
		},
		{
			MediaID:       "media_salad_bowl_6619081",
			EnvKey:        "SALAD_BOWL_MEDIA_ID",
			Filename:      "salad_bowl.jpg",
			MIMEType:      "image/jpeg",
			SizeBytes:     462000,
			PreviewURL:    saladBowlImg,
			PhoneNumberID: "106540352242922",
			UploadHandle:  "4::aW1hZ2UvanBlZw==:salad_bowl_h",
			ExpiresAt:     expiry,
		},
	}

	cfg := WABASanndboxConfig{
		AppID:                 envOrDefault("APP_ID", "1401488693436528"),
		AppSecret:             envOrDefault("APP_SECRET", ""),
		AccessToken:           envOrDefault("ACCESS_TOKEN", ""),
		VerifyToken:           envOrDefault("VERIFY_TOKEN", "jaspers_market_verify_token_2026"),
		WABAID:                envOrDefault("WABA_ID", "102938475610293"),
		PhoneNumberID:         envOrDefault("PHONE_NUMBER_ID", "106540352242922"),
		APIVersion:            envOrDefault("GRAPH_API_VERSION", "v21.0"),
		GraphBaseURL:          envOrDefault("GRAPH_API_BASE_URL", "http://localhost:9083"),
		GroceriesMediaID:      envOrDefault("GROCERIES_MEDIA_ID", "media_groceries_8841209"),
		StrawberriesMediaID:   envOrDefault("STRAWBERRIES_MEDIA_ID", "media_strawberries_9920144"),
		SheetPanDinnerMediaID: envOrDefault("SHEET_PAN_DINNER_MEDIA_ID", "media_sheet_pan_7731902"),
		SaladBowlMediaID:      envOrDefault("SALAD_BOWL_MEDIA_ID", "media_salad_bowl_6619081"),
		MediaAssets:           initialMedia,
	}

	templates := []WABATemplateDefinition{
		{
			ID:                        "tpl_auth_otp_verification",
			Name:                      "auth_otp_verification",
			Language:                  "en_US",
			Category:                  "AUTHENTICATION",
			Status:                    "APPROVED",
			HeaderFormat:              "NONE",
			BodyText:                  "{{1}} is your verification code. For your security, do not share this code.",
			BodyParams:                []string{"749201"},
			FooterText:                "This code expires in 10 minutes.",
			OTPCode:                   "749201",
			OTPType:                   "COPY_CODE",
			CodeExpirationMinutes:     10,
			AddSecurityRecommendation: true,
		},
		{
			ID:                        "tpl_auth_2fa_onetap_autofill",
			Name:                      "auth_2fa_onetap_autofill",
			Language:                  "en_US",
			Category:                  "AUTHENTICATION",
			Status:                    "APPROVED",
			HeaderFormat:              "NONE",
			BodyText:                  "{{1}} is your verification code. For your security, do not share this code.",
			BodyParams:                []string{"883920"},
			FooterText:                "This code expires in 5 minutes.",
			OTPCode:                   "883920",
			OTPType:                   "ONE_TAP",
			AutofillText:              "Autofill code",
			CodeExpirationMinutes:     5,
			AddSecurityRecommendation: true,
			PackageName:               "com.jaspersmarket.app",
			SignatureHash:             "K8a9f2P1xQz",
		},
		{
			ID:               "tpl_grocery_delivery_utility",
			Name:             "grocery_delivery_utility",
			Language:         "en_US",
			Category:         "UTILITY",
			Status:           "APPROVED",
			HeaderFormat:     "IMAGE",
			HeaderMediaID:    cfg.GroceriesMediaID,
			HeaderPreviewURL: groceriesImg,
			BodyText:         "Free delivery for all online orders with Jasper’s Market",
			FooterText:       "developers.facebook.com",
			ButtonCTA:        "Get free delivery",
			ButtonURL:        "https://developers.facebook.com/documentation/business-messaging/whatsapp/templates/utility-templates/utility-templates",
		},
		{
			ID:                "tpl_order_confirmation_receipt",
			Name:              "order_confirmation_receipt",
			Language:          "en_US",
			Category:          "UTILITY",
			Status:            "APPROVED",
			HeaderFormat:      "DOCUMENT",
			HeaderMediaID:     "media_receipt_jm88412",
			DocumentFilename:  "Jasper_Order_Receipt_JM88412.pdf",
			BodyText:          "Hi {{1}}, your Jasper's Market order #{{2}} for {{3}} is confirmed and being packed. Your itemized PDF receipt is attached above.",
			BodyParams:        []string{"Alex Rivera", "JM-88412", "$42.50"},
			FooterText:        "Jasper's Market Order Fulfillment",
			ButtonCTA:         "View Order Details",
			ButtonURL:         "https://developers.facebook.com/documentation/business-messaging/whatsapp/templates/utility-templates/utility-templates",
			QuickReplyButtons: []string{"Modify delivery window", "Leave at front door"},
		},
		{
			ID:                "tpl_delivery_eta_update",
			Name:              "delivery_eta_update",
			Language:          "en_US",
			Category:          "UTILITY",
			Status:            "APPROVED",
			HeaderFormat:      "IMAGE",
			HeaderMediaID:     cfg.GroceriesMediaID,
			HeaderPreviewURL:  groceriesImg,
			BodyText:          "Delivery update for order #{{1}}: Courier {{2}} is 10 minutes away with your fresh groceries. Estimated arrival: {{3}}.",
			BodyParams:        []string{"JM-88412", "Marco S.", "4:15 PM – 4:30 PM"},
			FooterText:        "Jasper's Market Live Logistics",
			ButtonCTA:         "Track Live Map",
			ButtonURL:         "https://developers.facebook.com/documentation/business-messaging/whatsapp/templates/utility-templates/utility-templates",
			QuickReplyButtons: []string{"Gate code: #1234", "Call on arrival"},
		},
		{
			ID:                "tpl_payment_security_verification",
			Name:              "payment_security_verification",
			Language:          "en_US",
			Category:          "UTILITY",
			Status:            "APPROVED",
			HeaderFormat:      "TEXT",
			HeaderText:        "Payment & Account Alert",
			BodyText:          "We processed a {{1}} payment on Visa ending in {{2}} for order #{{3}}. Tap below to confirm or report unauthorized activity.",
			BodyParams:        []string{"$42.50", "4242", "JM-88412"},
			FooterText:        "Jasper's Market Payments Security",
			QuickReplyButtons: []string{"Confirm payment", "Report issue"},
		},
		{
			ID:                "tpl_appointment_reminder_utility",
			Name:              "appointment_reminder_utility",
			Language:          "en_US",
			Category:          "UTILITY",
			Status:            "APPROVED",
			HeaderFormat:      "TEXT",
			HeaderText:        "Curbside Pickup Slot Reminder",
			BodyText:          "Hi {{1}}, your Curbside Express pickup at {{2}} is scheduled for {{3}} (Bay #4). Reply below if your arrival time changes.",
			BodyParams:        []string{"Alex Rivera", "Jasper's Downtown Market", "Today at 5:00 PM"},
			FooterText:        "Jasper's Market Curbside Concierge",
			QuickReplyButtons: []string{"Confirm slot", "Running 15m late", "Reschedule pickup"},
		},
		{
			ID:                "tpl_subscription_billing_statement",
			Name:              "subscription_billing_statement",
			Language:          "en_US",
			Category:          "UTILITY",
			Status:            "APPROVED",
			HeaderFormat:      "DOCUMENT",
			HeaderMediaID:     "media_sub_statement_03",
			DocumentFilename:  "Weekly_Organic_Box_Statement.pdf",
			BodyText:          "Your Weekly Organic Harvest Box subscription ({{1}}) renews on {{2}} for {{3}}. Manage your box items or skip this week below.",
			BodyParams:        []string{"Plan #SUB-4410", "Nov 15, 2026", "$34.00"},
			FooterText:        "Jasper's Market Subscriptions",
			ButtonCTA:         "Manage Subscription",
			ButtonURL:         "https://developers.facebook.com/documentation/business-messaging/whatsapp/templates/utility-templates/utility-templates",
			QuickReplyButtons: []string{"Keep my box", "Skip next week"},
		},
		{
			ID:               "tpl_strawberries_limited_offer",
			Name:             "strawberries_limited_offer",
			Language:         "en_US",
			Category:         "MARKETING",
			Status:           "APPROVED",
			HeaderFormat:     "IMAGE",
			HeaderMediaID:    cfg.StrawberriesMediaID,
			HeaderPreviewURL: strawberriesImg,
			BodyText:         "Fresh strawberries at Jasper's Market are now 20% off! Get them while they last",
			IsLTO:            true,
			LTOText:          "Expiring offer!",
			OfferCode:        "BERRIES20",
			ButtonCTA:        "Shop now",
			ButtonURL:        "https://developers.facebook.com/documentation/business-messaging/whatsapp/templates/marketing-templates/limited-time-offer-templates",
		},
		{
			ID:           "tpl_recipe_media_carousel",
			Name:         "recipe_media_carousel",
			Language:     "en_US",
			Category:     "MARKETING",
			Status:       "APPROVED",
			HeaderFormat: "CAROUSEL",
			BodyText:     "Our in-house chefs have prepared some delicious and fresh summer recipes.",
			CarouselCards: []TemplateCarouselCard{
				{
					CardIndex:  0,
					ImageID:    cfg.SheetPanDinnerMediaID,
					PreviewURL: sheetPanImg,
					BodyText:   "Simple and Healthy Sheet Pan Dinner to Feed the Whole Family",
					ButtonCTA:  "Get this recipe",
					ButtonURL:  "https://developers.facebook.com/documentation/business-messaging/whatsapp/templates/marketing-templates/media-card-carousel-templates",
				},
				{
					CardIndex:  1,
					ImageID:    cfg.SaladBowlMediaID,
					PreviewURL: saladBowlImg,
					BodyText:   "3 Plant-Powered Salad Bowls to Fuel Your Week",
					ButtonCTA:  "Get this recipe",
					ButtonURL:  "https://developers.facebook.com/documentation/business-messaging/whatsapp/templates/marketing-templates/media-card-carousel-templates",
				},
			},
		},
		{
			ID:               "tpl_fraud_audit_case_alert",
			Name:             "fraud_audit_case_alert",
			Language:         "en_US",
			Category:         "UTILITY",
			Status:           "APPROVED",
			HeaderFormat:     "DOCUMENT",
			HeaderMediaID:    "media_sar_str_bundle_0118",
			DocumentFilename: "SAR_STR_Audit_Case_Bundle.pdf",
			BodyText:         "• Retiring the old per-lender fraud system (item 5) deletes data in every lender's database. I'd need your OK first.\n• Moving the counting into the database (item 12) is a larger rewrite of how rules are evaluated.\n• Alerts created before this change have no case key, so each one gets one fresh alert on its next hit, then normal grouping applies.\n• Still missing from the audit: deactivation timestamps, a start date for back-fills, a per-lender breakdown in the simulator, investigation case management (notes, attachments, SAR/STR tracking), and the remaining event types the audit listed.",
			FooterText:       "Risk & Fraud Operations Audit",
			ButtonCTA:        "Open Case Simulator",
			ButtonURL:        "http://localhost:9083",
		},
	}

	manager := &SanndboxStateManager{
		config:             cfg,
		templates:          templates,
		followUpCache:      make(map[string]time.Time),
		attributionCounter: make(map[string]int),
	}
	manager.recomputeValidationLocked()
	return manager
}

func (manager *SanndboxStateManager) ResolveMediaPreviewURL(mediaID string) string {
	manager.mutex.RLock()
	defer manager.mutex.RUnlock()
	for _, asset := range manager.config.MediaAssets {
		if asset.MediaID == mediaID && asset.PreviewURL != "" {
			return asset.PreviewURL
		}
	}
	return "https://images.unsplash.com/photo-1542838132-92c53300491e?auto=format&fit=crop&w=900&q=80"
}

func (manager *SanndboxStateManager) recomputeValidationLocked() {
	var errs []string
	if strings.TrimSpace(manager.config.AccessToken) == "" || strings.HasPrefix(manager.config.AccessToken, "ADD_") {
		errs = append(errs, "Missing or placeholder ACCESS_TOKEN")
	}
	if strings.TrimSpace(manager.config.AppSecret) == "" || strings.HasPrefix(manager.config.AppSecret, "ADD_") {
		errs = append(errs, "Missing or placeholder APP_SECRET")
	}
	if strings.TrimSpace(manager.config.VerifyToken) == "" || strings.HasPrefix(manager.config.VerifyToken, "ADD_") {
		errs = append(errs, "Missing or placeholder VERIFY_TOKEN")
	}

	mediaVars := map[string]string{
		"GROCERIES_MEDIA_ID":        manager.config.GroceriesMediaID,
		"STRAWBERRIES_MEDIA_ID":     manager.config.StrawberriesMediaID,
		"SHEET_PAN_DINNER_MEDIA_ID": manager.config.SheetPanDinnerMediaID,
		"SALAD_BOWL_MEDIA_ID":       manager.config.SaladBowlMediaID,
	}
	for key, val := range mediaVars {
		trimmed := strings.TrimSpace(val)
		if trimmed == "" || trimmed == MediaIDPlaceholder || trimmed == "ADD_"+key+"_HERE" {
			errs = append(errs, fmt.Sprintf("Invalid media ID %s='%s' (rejected by services/config.js checkEnvVariables)", key, val))
		}
	}

	manager.config.ValidationErrors = errs
	manager.config.IsReady = len(errs) == 0
	manager.config.EnvFilePreview = fmt.Sprintf(`# Facebook App Configuration (compatible with fbsamples/whatsapp-business-jaspers-market)
ACCESS_TOKEN=%s
APP_SECRET=%s
APP_ID=%s
WABA_ID=%s
PHONE_NUMBER_ID=%s
VERIFY_TOKEN=%s

# Server Configuration
PORT=8080
REDIS_HOST=localhost
REDIS_PORT=6379
GRAPH_API_BASE_URL=%s

# WhatsApp media IDs uploaded for your sender phone number (expire after 30 days)
GROCERIES_MEDIA_ID=%s
STRAWBERRIES_MEDIA_ID=%s
SHEET_PAN_DINNER_MEDIA_ID=%s
SALAD_BOWL_MEDIA_ID=%s`,
		manager.config.AccessToken,
		manager.config.AppSecret,
		manager.config.AppID,
		manager.config.WABAID,
		manager.config.PhoneNumberID,
		manager.config.VerifyToken,
		manager.config.GraphBaseURL,
		manager.config.GroceriesMediaID,
		manager.config.StrawberriesMediaID,
		manager.config.SheetPanDinnerMediaID,
		manager.config.SaladBowlMediaID,
	)

	manager.config.TemplateScriptPreview = fmt.Sprintf(`#!/bin/bash
# Provision templates against Sanndbox (%s) or Production (https://graph.facebook.com)
GRAPH_BASE="%s"
APIVERSION="%s"
APPTOKEN="%s"
WABAID="%s"

curl -X POST "${GRAPH_BASE}/${APIVERSION}/${WABAID}/message_templates" \
  -H "Authorization: Bearer ${APPTOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"name":"grocery_delivery_utility","language":"en_US","category":"marketing","components":[{"type":"header","format":"image"},{"type":"body","text":"Free delivery for all online orders with Jasper'\''s Market"},{"type":"footer","text":"developers.facebook.com"}]}'`,
		manager.config.GraphBaseURL,
		manager.config.GraphBaseURL,
		manager.config.APIVersion,
		manager.config.AccessToken,
		manager.config.WABAID,
	)
}

func (manager *SanndboxStateManager) GetConfig() WABASanndboxConfig {
	manager.mutex.RLock()
	defer manager.mutex.RUnlock()
	return manager.config
}

func (manager *SanndboxStateManager) UpdateConfig(updated WABASanndboxConfig) WABASanndboxConfig {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	if updated.AppID != "" {
		manager.config.AppID = updated.AppID
	}
	if updated.AppSecret != "" {
		manager.config.AppSecret = updated.AppSecret
	}
	if updated.AccessToken != "" {
		manager.config.AccessToken = updated.AccessToken
	}
	if updated.VerifyToken != "" {
		manager.config.VerifyToken = updated.VerifyToken
	}
	if updated.WABAID != "" {
		manager.config.WABAID = updated.WABAID
	}
	if updated.PhoneNumberID != "" {
		manager.config.PhoneNumberID = updated.PhoneNumberID
	}
	if updated.APIVersion != "" {
		manager.config.APIVersion = updated.APIVersion
	}
	if updated.GraphBaseURL != "" {
		manager.config.GraphBaseURL = updated.GraphBaseURL
	}
	manager.config.GroceriesMediaID = updated.GroceriesMediaID
	manager.config.StrawberriesMediaID = updated.StrawberriesMediaID
	manager.config.SheetPanDinnerMediaID = updated.SheetPanDinnerMediaID
	manager.config.SaladBowlMediaID = updated.SaladBowlMediaID
	manager.recomputeValidationLocked()
	return manager.config
}

func (manager *SanndboxStateManager) RegisterMedia(filename string, mimeType string, sizeBytes int64, envKey string, previewURL string) WABAMediaRecord {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	mediaID := "media_" + generateIdentifier()
	if previewURL == "" && strings.HasPrefix(mimeType, "image/") {
		switch envKey {
		case "GROCERIES_MEDIA_ID":
			previewURL = "https://images.unsplash.com/photo-1542838132-92c53300491e?auto=format&fit=crop&w=900&q=80"
		case "STRAWBERRIES_MEDIA_ID":
			previewURL = "https://images.unsplash.com/photo-1464965911861-746a04b4bca6?auto=format&fit=crop&w=900&q=80"
		case "SHEET_PAN_DINNER_MEDIA_ID":
			previewURL = "https://images.unsplash.com/photo-1544025162-d76694265947?auto=format&fit=crop&w=640&q=80"
		case "SALAD_BOWL_MEDIA_ID":
			previewURL = "https://images.unsplash.com/photo-1512621776951-a57141f2eefd?auto=format&fit=crop&w=640&q=80"
		default:
			previewURL = "https://images.unsplash.com/photo-1542838132-92c53300491e?auto=format&fit=crop&w=900&q=80"
		}
	}
	record := WABAMediaRecord{
		MediaID:       mediaID,
		EnvKey:        envKey,
		Filename:      filename,
		MIMEType:      mimeType,
		SizeBytes:     sizeBytes,
		PreviewURL:    previewURL,
		PhoneNumberID: manager.config.PhoneNumberID,
		UploadHandle:  "4::" + generateIdentifier(),
		ExpiresAt:     time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339),
	}
	manager.config.MediaAssets = append([]WABAMediaRecord{record}, manager.config.MediaAssets...)
	switch envKey {
	case "GROCERIES_MEDIA_ID":
		manager.config.GroceriesMediaID = mediaID
	case "STRAWBERRIES_MEDIA_ID":
		manager.config.StrawberriesMediaID = mediaID
	case "SHEET_PAN_DINNER_MEDIA_ID":
		manager.config.SheetPanDinnerMediaID = mediaID
	case "SALAD_BOWL_MEDIA_ID":
		manager.config.SaladBowlMediaID = mediaID
	}
	manager.recomputeValidationLocked()
	return record
}

func (manager *SanndboxStateManager) ListTemplates() []WABATemplateDefinition {
	manager.mutex.RLock()
	defer manager.mutex.RUnlock()
	out := make([]WABATemplateDefinition, len(manager.templates))
	copy(out, manager.templates)
	return out
}

func (manager *SanndboxStateManager) FindTemplate(name string) *WABATemplateDefinition {
	manager.mutex.RLock()
	defer manager.mutex.RUnlock()
	for _, tpl := range manager.templates {
		if strings.EqualFold(tpl.Name, name) {
			matched := tpl
			return &matched
		}
	}
	return nil
}

func (manager *SanndboxStateManager) SaveTemplate(tpl WABATemplateDefinition) WABATemplateDefinition {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	if tpl.ID == "" {
		tpl.ID = "tpl_" + generateIdentifier()
	}
	if tpl.Language == "" {
		tpl.Language = "en_US"
	}
	if tpl.Category == "" {
		tpl.Category = "UTILITY"
	}
	tpl.Category = strings.ToUpper(tpl.Category)
	if tpl.Status == "" {
		tpl.Status = "APPROVED"
	}
	for index, existing := range manager.templates {
		if strings.EqualFold(existing.Name, tpl.Name) {
			manager.templates[index] = tpl
			return tpl
		}
	}
	manager.templates = append([]WABATemplateDefinition{tpl}, manager.templates...)
	return tpl
}

func (manager *SanndboxStateManager) ArmFollowUp(wamid string) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	manager.followUpCache[wamid] = time.Now().Add(15 * time.Minute)
}

func (manager *SanndboxStateManager) ConsumeFollowUp(wamid string) bool {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	expiry, exists := manager.followUpCache[wamid]
	if !exists {
		return false
	}
	delete(manager.followUpCache, wamid)
	return time.Now().Before(expiry)
}

func (manager *SanndboxStateManager) BuildAttribution(inboundID string, textBody string, receiverPhoneID string) *AttributionEvent {
	trimmed := strings.TrimSpace(textBody)
	var surface string
	switch trimmed {
	case UserStartMessage:
		surface = "developer_docs"
	case UserTryOutMessage:
		surface = "simplified_devx_onboarding"
	default:
		return nil
	}
	if inboundID == "" {
		inboundID = "wamid." + generateIdentifier()
	}
	manager.mutex.Lock()
	manager.attributionCounter[inboundID]++
	count := manager.attributionCounter[inboundID]
	manager.mutex.Unlock()

	event := &AttributionEvent{
		EventName:             "entry_source_attributed",
		SchemaVersion:         1,
		SourceSurface:         surface,
		InboundMessageID:      inboundID,
		ReceiverPhoneNumberID: receiverPhoneID,
		EventTimestamp:        fmt.Sprintf("%d", time.Now().Unix()),
		WebhookDeliveryCount:  count,
	}
	encoded, _ := json.Marshal(event)
	fmt.Println(string(encoded))
	return event
}

type InMemoryIMRepository struct {
	mutex   sync.RWMutex
	records []IMRecord
}

func NewInMemoryIMRepository() *InMemoryIMRepository {
	return &InMemoryIMRepository{
		records: make([]IMRecord, 0, 128),
	}
}

func (repository *InMemoryIMRepository) Save(record IMRecord) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	repository.records = append([]IMRecord{record}, repository.records...)
}

func (repository *InMemoryIMRepository) UpdateDeliveryStatus(wamid string, status string) *IMRecord {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	for index := range repository.records {
		if repository.records[index].WAMID == wamid || repository.records[index].ID == wamid {
			repository.records[index].DeliveryStatus = status
			repository.records[index].FollowUpArmed = false
			updated := repository.records[index]
			return &updated
		}
	}
	return nil
}

func (repository *InMemoryIMRepository) ListBySession(sessionID string) []IMRecord {
	repository.mutex.RLock()
	defer repository.mutex.RUnlock()
	var filtered []IMRecord
	for _, record := range repository.records {
		if sessionID == "" || record.SessionID == "" || record.SessionID == sessionID {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

func (repository *InMemoryIMRepository) ClearSession(sessionID string) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	if sessionID == "" {
		return
	}
	retained := make([]IMRecord, 0, len(repository.records))
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

type IMSanndboxService struct {
	repository   *InMemoryIMRepository
	evaluator    *WhatsAppConstraintEvaluator
	stateManager *SanndboxStateManager
	broadcaster  *SSEBroadcaster
	templates    *template.Template
}

func NewIMSanndboxService(
	repository *InMemoryIMRepository,
	evaluator *WhatsAppConstraintEvaluator,
	stateManager *SanndboxStateManager,
	broadcaster *SSEBroadcaster,
	templates *template.Template,
) *IMSanndboxService {
	return &IMSanndboxService{
		repository:   repository,
		evaluator:    evaluator,
		stateManager: stateManager,
		broadcaster:  broadcaster,
		templates:    templates,
	}
}

func generateIdentifier() string {
	buffer := make([]byte, 8)
	_, _ = rand.Read(buffer)
	return hex.EncodeToString(buffer)
}

func (service *IMSanndboxService) Ingest(submission IMSubmissionRequest, source string, armFollowUp bool) IMRecord {
	cfg := service.stateManager.GetConfig()
	if submission.Attribution == nil && submission.Direction == "INBOUND" {
		submission.Attribution = service.stateManager.BuildAttribution("wamid."+generateIdentifier(), submission.Text, cfg.PhoneNumberID)
	}
	if submission.Media != nil && submission.Media.PreviewURL == "" && strings.HasPrefix(submission.Media.MIMEType, "image/") {
		submission.Media.PreviewURL = service.stateManager.ResolveMediaPreviewURL("")
	}
	if submission.TemplatePreview != nil {
		if len(submission.TemplatePreview.BodyParams) > 0 {
			interpolated := interpolateTemplateParams(submission.TemplatePreview.BodyText, submission.TemplatePreview.BodyParams)
			submission.TemplatePreview.BodyText = interpolated
			submission.Text = interpolated
		} else if submission.Text == "" {
			submission.Text = submission.TemplatePreview.BodyText
		}
		if submission.TemplatePreview.HeaderPreviewURL == "" && strings.EqualFold(submission.TemplatePreview.HeaderFormat, "IMAGE") {
			submission.TemplatePreview.HeaderPreviewURL = service.stateManager.ResolveMediaPreviewURL(submission.TemplatePreview.HeaderMediaID)
		}
		for idx := range submission.TemplatePreview.CarouselCards {
			if submission.TemplatePreview.CarouselCards[idx].PreviewURL == "" {
				submission.TemplatePreview.CarouselCards[idx].PreviewURL = service.stateManager.ResolveMediaPreviewURL(submission.TemplatePreview.CarouselCards[idx].ImageID)
			}
		}
	}
	report := service.evaluator.Evaluate(submission, cfg)
	id := generateIdentifier()
	wamid := "wamid.HBgM" + strings.ToUpper(id)
	if armFollowUp {
		service.stateManager.ArmFollowUp(wamid)
	}
	direction := submission.Direction
	if direction == "" {
		direction = "OUTBOUND"
	}

	record := IMRecord{
		ID:                 id,
		WAMID:              wamid,
		SessionID:          submission.SessionID,
		Timestamp:          time.Now().UTC().Format(time.RFC3339),
		Direction:          direction,
		Source:             source,
		Platform:           "WHATSAPP",
		EventType:          report.EventType,
		CaseKey:            submission.CaseKey,
		DeliveryStatus:     "sent",
		FollowUpArmed:      armFollowUp,
		Sender:             submission.Sender,
		Recipient:          submission.Recipient,
		Text:               submission.Text,
		Media:              submission.Media,
		InteractiveButtons: submission.InteractiveButtons,
		TemplatePreview:    submission.TemplatePreview,
		RawGraphPayload:    submission.RawGraphPayload,
		Attribution:        submission.Attribution,
		Report:             report,
	}
	service.repository.Save(record)

	var renderedBuffer bytes.Buffer
	_ = service.templates.ExecuteTemplate(&renderedBuffer, "im_card", record)
	jsonBytes, _ := json.Marshal(record)

	service.broadcaster.Broadcast(SSEEvent{
		SessionID:    submission.SessionID,
		RenderedHTML: renderedBuffer.String(),
		JSONPayload:  string(jsonBytes),
	})

	return record
}

func (service *IMSanndboxService) HandleJaspersMarketConversation(sessionID string, customerPhone string, buttonReplyID string, rawText string, signatureStatus string) []IMRecord {
	cfg := service.stateManager.GetConfig()
	var records []IMRecord

	inboundText := rawText
	if buttonReplyID != "" && inboundText == "" {
		inboundText = "[Button Click] " + buttonReplyID
	}
	inboundEventType := "text"
	if buttonReplyID != "" {
		inboundEventType = "interactive"
	}
	inboundRecord := service.Ingest(IMSubmissionRequest{
		SessionID:       sessionID,
		Direction:       "INBOUND",
		EventType:       inboundEventType,
		Sender:          customerPhone,
		Recipient:       cfg.PhoneNumberID + " (Jasper's Market)",
		Text:            inboundText,
		SignatureStatus: signatureStatus,
	}, "Webhook /webhook", false)
	records = append(records, inboundRecord)

	switch buttonReplyID {
	case ReplyInteractiveMediaID:
		tpl := service.stateManager.FindTemplate("grocery_delivery_utility")
		payloadMap := map[string]any{
			"messaging_product": "whatsapp",
			"recipient_type":    "individual",
			"to":                customerPhone,
			"type":              "template",
			"template": map[string]any{
				"name":     "grocery_delivery_utility",
				"language": map[string]string{"code": "en_US"},
				"components": []map[string]any{
					{
						"type": "header",
						"parameters": []map[string]any{
							{"type": "image", "image": map[string]string{"id": cfg.GroceriesMediaID}},
						},
					},
				},
			},
		}
		rawBytes, _ := json.MarshalIndent(payloadMap, "", "  ")
		rec := service.Ingest(IMSubmissionRequest{
			SessionID: sessionID,
			Direction: "OUTBOUND",
			EventType: "template",
			Sender:    "Jasper's Market (" + cfg.PhoneNumberID + ")",
			Recipient: customerPhone,
			Text:      tpl.BodyText,
			TemplatePreview: &TemplatePreviewData{
				Name:             tpl.Name,
				Language:         tpl.Language,
				Category:         tpl.Category,
				HeaderFormat:     tpl.HeaderFormat,
				HeaderMediaID:    cfg.GroceriesMediaID,
				HeaderPreviewURL: service.stateManager.ResolveMediaPreviewURL(cfg.GroceriesMediaID),
				BodyText:         tpl.BodyText,
				FooterText:       tpl.FooterText,
				ButtonCTA:        tpl.ButtonCTA,
				ButtonURL:        tpl.ButtonURL,
			},
			RawGraphPayload: string(rawBytes),
		}, "services/conversation.js", true)
		records = append(records, rec)

	case ReplyMediaCarouselID:
		tpl := service.stateManager.FindTemplate("recipe_media_carousel")
		payloadMap := map[string]any{
			"messaging_product": "whatsapp",
			"recipient_type":    "individual",
			"to":                customerPhone,
			"type":              "template",
			"template": map[string]any{
				"name":     "recipe_media_carousel",
				"language": map[string]string{"code": "en_US"},
				"components": []map[string]any{
					{
						"type": "carousel",
						"cards": []map[string]any{
							{
								"card_index": 0,
								"components": []map[string]any{
									{"type": "header", "parameters": []map[string]any{{"type": "image", "image": map[string]string{"id": cfg.SheetPanDinnerMediaID}}}},
								},
							},
							{
								"card_index": 1,
								"components": []map[string]any{
									{"type": "header", "parameters": []map[string]any{{"type": "image", "image": map[string]string{"id": cfg.SaladBowlMediaID}}}},
								},
							},
						},
					},
				},
			},
		}
		rawBytes, _ := json.MarshalIndent(payloadMap, "", "  ")
		rec := service.Ingest(IMSubmissionRequest{
			SessionID: sessionID,
			Direction: "OUTBOUND",
			EventType: "template",
			Sender:    "Jasper's Market (" + cfg.PhoneNumberID + ")",
			Recipient: customerPhone,
			Text:      tpl.BodyText,
			TemplatePreview: &TemplatePreviewData{
				Name:         tpl.Name,
				Language:     tpl.Language,
				Category:     tpl.Category,
				HeaderFormat: "CAROUSEL",
				BodyText:     tpl.BodyText,
				CarouselCards: []TemplateCarouselCard{
					{
						CardIndex:  0,
						ImageID:    cfg.SheetPanDinnerMediaID,
						PreviewURL: service.stateManager.ResolveMediaPreviewURL(cfg.SheetPanDinnerMediaID),
						BodyText:   "Simple and Healthy Sheet Pan Dinner to Feed the Whole Family",
						ButtonCTA:  "Get this recipe",
						ButtonURL:  "https://developers.facebook.com/documentation/business-messaging/whatsapp/templates/marketing-templates/media-card-carousel-templates",
					},
					{
						CardIndex:  1,
						ImageID:    cfg.SaladBowlMediaID,
						PreviewURL: service.stateManager.ResolveMediaPreviewURL(cfg.SaladBowlMediaID),
						BodyText:   "3 Plant-Powered Salad Bowls to Fuel Your Week",
						ButtonCTA:  "Get this recipe",
						ButtonURL:  "https://developers.facebook.com/documentation/business-messaging/whatsapp/templates/marketing-templates/media-card-carousel-templates",
					},
				},
			},
			RawGraphPayload: string(rawBytes),
		}, "services/conversation.js", true)
		records = append(records, rec)

	case ReplyOfferID:
		tpl := service.stateManager.FindTemplate("strawberries_limited_offer")
		expMs := time.Now().Add(48 * time.Hour).UnixMilli()
		payloadMap := map[string]any{
			"messaging_product": "whatsapp",
			"recipient_type":    "individual",
			"to":                customerPhone,
			"type":              "template",
			"template": map[string]any{
				"name":     "strawberries_limited_offer",
				"language": map[string]string{"code": "en_US"},
				"components": []map[string]any{
					{
						"type":       "header",
						"parameters": []map[string]any{{"type": "image", "image": map[string]string{"id": cfg.StrawberriesMediaID}}},
					},
					{
						"type":       "limited_time_offer",
						"parameters": []map[string]any{{"type": "limited_time_offer", "limited_time_offer": map[string]int64{"expiration_time_ms": expMs}}},
					},
					{
						"type":       "button",
						"sub_type":   "copy_code",
						"index":      0,
						"parameters": []map[string]any{{"type": "coupon_code", "coupon_code": "BERRIES20"}},
					},
				},
			},
		}
		rawBytes, _ := json.MarshalIndent(payloadMap, "", "  ")
		rec := service.Ingest(IMSubmissionRequest{
			SessionID: sessionID,
			Direction: "OUTBOUND",
			EventType: "template",
			Sender:    "Jasper's Market (" + cfg.PhoneNumberID + ")",
			Recipient: customerPhone,
			Text:      tpl.BodyText,
			TemplatePreview: &TemplatePreviewData{
				Name:             tpl.Name,
				Language:         tpl.Language,
				Category:         tpl.Category,
				HeaderFormat:     "IMAGE",
				HeaderMediaID:    cfg.StrawberriesMediaID,
				HeaderPreviewURL: service.stateManager.ResolveMediaPreviewURL(cfg.StrawberriesMediaID),
				BodyText:         tpl.BodyText,
				IsLTO:            true,
				LTOText:          "Expiring offer!",
				OfferCode:        "BERRIES20",
				ExpirationMs:     expMs,
				ButtonCTA:        tpl.ButtonCTA,
				ButtonURL:        tpl.ButtonURL,
			},
			RawGraphPayload: string(rawBytes),
		}, "services/conversation.js", true)
		records = append(records, rec)

	default:
		utilityReplies := map[string]string{
			"Confirm payment":        "✓ Payment of $42.50 on Visa •••• 4242 verified. Thank you! Your Jasper's Market order #JM-88412 is now out for packing.",
			"Report issue":           "⚠ Security Alert: We have placed a temporary hold on order #JM-88412 and notified Jasper's Market Fraud & Payments Security. A specialist will call you within 5 minutes.",
			"Modify delivery window": "📅 Delivery window options for order #JM-88412:\n• 5:00 PM – 6:00 PM\n• 6:00 PM – 7:00 PM\n• Tomorrow 9:00 AM – 10:00 AM\nReply with your preferred time slot.",
			"Leave at front door":    "📦 Delivery instructions updated for order #JM-88412: Courier Marco S. will leave your bags at the front door and send a photo confirmation upon drop-off.",
			"Gate code: #1234":       "🔑 Gate code #1234 saved and shared with courier Marco S. for order #JM-88412.",
			"Call on arrival":        "📞 Courier Marco S. has been notified to call +15550199882 upon arrival.",
			"Confirm slot":           "✓ Curbside pickup confirmed for Today at 5:00 PM (Bay #4). Text 'HERE' when you park!",
			"Running 15m late":       "⏱ No problem! We have extended your Curbside Express hold at Bay #4 until 5:30 PM.",
			"Reschedule pickup":      "📅 Your Curbside pickup can be moved to 6:30 PM today or 10:00 AM tomorrow. Reply with your choice.",
			"Keep my box":            "🥬 Great! Your Weekly Organic Harvest Box (#SUB-4410) will ship as scheduled on Nov 15, 2026.",
			"Skip next week":         "⏸ Your Weekly Organic Harvest Box (#SUB-4410) has been skipped for Nov 15, 2026. You will not be billed ($0.00).",
		}
		if replyMsg, isUtilityAction := utilityReplies[strings.TrimSpace(rawText)]; isUtilityAction {
			payloadMap := map[string]any{
				"messaging_product": "whatsapp",
				"to":                customerPhone,
				"type":              "text",
				"text":              map[string]string{"body": replyMsg},
			}
			rawBytes, _ := json.MarshalIndent(payloadMap, "", "  ")
			rec := service.Ingest(IMSubmissionRequest{
				SessionID:       sessionID,
				Direction:       "OUTBOUND",
				EventType:       "text",
				Sender:          "Jasper's Market (" + cfg.PhoneNumberID + ")",
				Recipient:       customerPhone,
				Text:            replyMsg,
				RawGraphPayload: string(rawBytes),
			}, "services/conversation.js (Utility Webhook Handler)", false)
			records = append(records, rec)
			return records
		}

		buttons := []InteractiveReplyButton{
			{ID: ReplyInteractiveMediaID, Title: ReplyInteractiveWithMediaCTA},
			{ID: ReplyMediaCarouselID, Title: ReplyMediaCardCarouselCTA},
			{ID: ReplyOfferID, Title: ReplyOfferCTA},
		}
		payloadMap := map[string]any{
			"messaging_product": "whatsapp",
			"to":                customerPhone,
			"type":              "interactive",
			"interactive": map[string]any{
				"type": "button",
				"body": map[string]string{"text": AppDefaultMessage},
				"action": map[string]any{
					"buttons": []map[string]any{
						{"type": "reply", "reply": map[string]string{"id": ReplyInteractiveMediaID, "title": ReplyInteractiveWithMediaCTA}},
						{"type": "reply", "reply": map[string]string{"id": ReplyMediaCarouselID, "title": ReplyMediaCardCarouselCTA}},
						{"type": "reply", "reply": map[string]string{"id": ReplyOfferID, "title": ReplyOfferCTA}},
					},
				},
			},
		}
		rawBytes, _ := json.MarshalIndent(payloadMap, "", "  ")
		rec := service.Ingest(IMSubmissionRequest{
			SessionID:          sessionID,
			Direction:          "OUTBOUND",
			EventType:          "interactive",
			Sender:             "Jasper's Market (" + cfg.PhoneNumberID + ")",
			Recipient:          customerPhone,
			Text:               AppDefaultMessage,
			InteractiveButtons: buttons,
			RawGraphPayload:    string(rawBytes),
		}, "services/conversation.js", false)
		records = append(records, rec)
	}

	return records
}

func (service *IMSanndboxService) HandleStatusReceipt(sessionID string, wamid string, status string, recipientPhone string) *IMRecord {
	cfg := service.stateManager.GetConfig()
	service.repository.UpdateDeliveryStatus(wamid, status)
	if status != "delivered" && status != "read" {
		return nil
	}
	if !service.stateManager.ConsumeFollowUp(wamid) {
		return nil
	}
	buttons := []InteractiveReplyButton{
		{ID: ReplyInteractiveMediaID, Title: ReplyInteractiveWithMediaCTA},
		{ID: ReplyMediaCarouselID, Title: ReplyMediaCardCarouselCTA},
		{ID: ReplyOfferID, Title: ReplyOfferCTA},
	}
	payloadMap := map[string]any{
		"messaging_product": "whatsapp",
		"to":                recipientPhone,
		"type":              "interactive",
		"interactive": map[string]any{
			"type": "button",
			"body": map[string]string{"text": AppTryAnotherMessage},
			"action": map[string]any{
				"buttons": []map[string]any{
					{"type": "reply", "reply": map[string]string{"id": ReplyInteractiveMediaID, "title": ReplyInteractiveWithMediaCTA}},
					{"type": "reply", "reply": map[string]string{"id": ReplyMediaCarouselID, "title": ReplyMediaCardCarouselCTA}},
					{"type": "reply", "reply": map[string]string{"id": ReplyOfferID, "title": ReplyOfferCTA}},
				},
			},
		},
	}
	rawBytes, _ := json.MarshalIndent(payloadMap, "", "  ")
	followUp := service.Ingest(IMSubmissionRequest{
		SessionID:          sessionID,
		Direction:          "OUTBOUND",
		EventType:          "interactive",
		Sender:             "Jasper's Market (" + cfg.PhoneNumberID + ")",
		Recipient:          recipientPhone,
		Text:               AppTryAnotherMessage,
		InteractiveButtons: buttons,
		RawGraphPayload:    string(rawBytes),
	}, "Conversation.handleStatus (15s Redis TTL Follow-Up)", false)
	return &followUp
}

type HTTPHandler struct {
	service      *IMSanndboxService
	repository   *InMemoryIMRepository
	stateManager *SanndboxStateManager
	broadcaster  *SSEBroadcaster
	templates    *template.Template
}

// HandleIndex renders the WhatsApp Business & Jasper's Market Sanndbox UI.
// @Summary Render WhatsApp Business & Jasper's Market Sanndbox UI
// @Produce html
// @Success 200 {string} string "HTML Dashboard"
// @Router / [get]
func (handler *HTTPHandler) HandleIndex(responseWriter http.ResponseWriter, request *http.Request) {
	responseWriter.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = handler.templates.ExecuteTemplate(responseWriter, "index.html", nil)
}

// HandleWebhookVerify implements Meta's GET /webhook verification challenge handshake.
// @Summary Verify WhatsApp Webhook Subscription (hub.verify_token & hub.challenge)
// @Param hub.mode query string true "Must be subscribe"
// @Param hub.verify_token query string true "Verify Token configured in Setup"
// @Param hub.challenge query string true "Challenge nonce echoed back on 200 OK"
// @Success 200 {string} string "hub.challenge"
// @Router /webhook [get]
func (handler *HTTPHandler) HandleWebhookVerify(responseWriter http.ResponseWriter, request *http.Request) {
	mode := request.URL.Query().Get("hub.mode")
	token := request.URL.Query().Get("hub.verify_token")
	challenge := request.URL.Query().Get("hub.challenge")
	cfg := handler.stateManager.GetConfig()

	if mode != "subscribe" || token != cfg.VerifyToken {
		http.Error(responseWriter, "Forbidden: verify_token mismatch", http.StatusForbidden)
		return
	}
	responseWriter.Header().Set("Content-Type", "text/plain; charset=utf-8")
	responseWriter.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(responseWriter, challenge)
}

// HandleWebhookReceive implements Meta's POST /webhook handler (app.js + services/conversation.js).
// @Summary Receive WhatsApp Cloud API Webhook events (messages & statuses)
// @Accept json
// @Produce plain
// @Success 200 {string} string "EVENT_RECEIVED"
// @Router /webhook [post]
func (handler *HTTPHandler) HandleWebhookReceive(responseWriter http.ResponseWriter, request *http.Request) {
	rawBody, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(responseWriter, err.Error(), http.StatusBadRequest)
		return
	}

	cfg := handler.stateManager.GetConfig()
	sigHeader := request.Header.Get("X-Hub-Signature-256")
	signatureStatus := "UNSPECIFIED"
	if sigHeader != "" {
		mac := hmac.New(sha256.New, []byte(cfg.AppSecret))
		mac.Write(rawBody)
		expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if hmac.Equal([]byte(sigHeader), []byte(expected)) {
			signatureStatus = "VALID"
		} else {
			signatureStatus = "INVALID"
		}
	}

	sessionID := request.Header.Get("X-Sanndbox-Session")
	var payload struct {
		Object string `json:"object"`
		Entry  []struct {
			Changes []struct {
				Value struct {
					Metadata struct {
						PhoneNumberID string `json:"phone_number_id"`
					} `json:"metadata"`
					Messages []struct {
						ID        string `json:"id"`
						From      string `json:"from"`
						Timestamp string `json:"timestamp"`
						Type      string `json:"type"`
						Text      *struct {
							Body string `json:"body"`
						} `json:"text"`
						Interactive *struct {
							ButtonReply *struct {
								ID    string `json:"id"`
								Title string `json:"title"`
							} `json:"button_reply"`
						} `json:"interactive"`
					} `json:"messages"`
					Statuses []struct {
						ID          string `json:"id"`
						Status      string `json:"status"`
						RecipientID string `json:"recipient_id"`
					} `json:"statuses"`
				} `json:"value"`
			} `json:"changes"`
		} `json:"entry"`
	}

	if err := json.Unmarshal(rawBody, &payload); err == nil && payload.Object == "whatsapp_business_account" {
		for _, entry := range payload.Entry {
			for _, change := range entry.Changes {
				for _, st := range change.Value.Statuses {
					handler.service.HandleStatusReceipt(sessionID, st.ID, st.Status, st.RecipientID)
				}
				for _, msg := range change.Value.Messages {
					var buttonID string
					var textBody string
					if msg.Type == "interactive" && msg.Interactive != nil && msg.Interactive.ButtonReply != nil {
						buttonID = msg.Interactive.ButtonReply.ID
						textBody = msg.Interactive.ButtonReply.Title
					} else if msg.Text != nil {
						textBody = msg.Text.Body
					}
					handler.service.HandleJaspersMarketConversation(sessionID, msg.From, buttonID, textBody, signatureStatus)
				}
			}
		}
	}

	responseWriter.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(responseWriter, "EVENT_RECEIVED")
}

// HandleGraphMessages emulates POST /{apiVersion}/{phoneNumberId}/messages from services/graph-api.js.
// @Summary Graph API Send Message endpoint (/{apiVersion}/{phoneNumberId}/messages)
// @Accept json
// @Produce json
// @Success 200 {object} map[string]any
// @Router /{apiVersion}/{phoneNumberId}/messages [post]
func (handler *HTTPHandler) HandleGraphMessages(responseWriter http.ResponseWriter, request *http.Request) {
	rawBody, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(responseWriter, err.Error(), http.StatusBadRequest)
		return
	}
	var generic map[string]any
	if err := json.Unmarshal(rawBody, &generic); err != nil {
		http.Error(responseWriter, err.Error(), http.StatusBadRequest)
		return
	}

	sessionID := request.Header.Get("X-Sanndbox-Session")
	if statusVal, ok := generic["status"].(string); ok && statusVal == "read" {
		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(map[string]any{"success": true})
		return
	}

	recipient, _ := generic["to"].(string)
	msgType, _ := generic["type"].(string)
	cfg := handler.stateManager.GetConfig()
	prettyJSON, _ := json.MarshalIndent(generic, "", "  ")

	submission := IMSubmissionRequest{
		SessionID:       sessionID,
		Direction:       "OUTBOUND",
		EventType:       msgType,
		Sender:          cfg.PhoneNumberID,
		Recipient:       recipient,
		RawGraphPayload: string(prettyJSON),
	}

	switch msgType {
	case "text":
		if textObj, ok := generic["text"].(map[string]any); ok {
			submission.Text, _ = textObj["body"].(string)
		}
	case "interactive":
		if interObj, ok := generic["interactive"].(map[string]any); ok {
			if bodyObj, ok := interObj["body"].(map[string]any); ok {
				submission.Text, _ = bodyObj["text"].(string)
			}
			if actionObj, ok := interObj["action"].(map[string]any); ok {
				if btns, ok := actionObj["buttons"].([]any); ok {
					for _, rawBtn := range btns {
						if btnMap, ok := rawBtn.(map[string]any); ok {
							if replyMap, ok := btnMap["reply"].(map[string]any); ok {
								idStr, _ := replyMap["id"].(string)
								titleStr, _ := replyMap["title"].(string)
								submission.InteractiveButtons = append(submission.InteractiveButtons, InteractiveReplyButton{
									ID:    idStr,
									Title: titleStr,
								})
							}
						}
					}
				}
			}
		}
	case "template":
		if tplObj, ok := generic["template"].(map[string]any); ok {
			tplName, _ := tplObj["name"].(string)
			var runtimeBodyParams []string
			if comps, ok := tplObj["components"].([]any); ok {
				for _, compRaw := range comps {
					if compMap, ok := compRaw.(map[string]any); ok {
						if strings.EqualFold(fmt.Sprintf("%v", compMap["type"]), "body") {
							if paramsRaw, ok := compMap["parameters"].([]any); ok {
								for _, paramItem := range paramsRaw {
									if paramMap, ok := paramItem.(map[string]any); ok {
										if txtVal, ok := paramMap["text"].(string); ok {
											runtimeBodyParams = append(runtimeBodyParams, txtVal)
										}
									}
								}
							}
						}
					}
				}
			}
			matched := handler.stateManager.FindTemplate(tplName)
			if matched != nil {
				activeParams := matched.BodyParams
				if len(runtimeBodyParams) > 0 {
					activeParams = runtimeBodyParams
				}
				activeOTP := matched.OTPCode
				if len(runtimeBodyParams) > 0 && strings.EqualFold(matched.Category, "AUTHENTICATION") {
					activeOTP = runtimeBodyParams[0]
				}
				submission.Text = matched.BodyText
				submission.TemplatePreview = &TemplatePreviewData{
					Name:                      matched.Name,
					Language:                  matched.Language,
					Category:                  matched.Category,
					HeaderFormat:              matched.HeaderFormat,
					HeaderText:                matched.HeaderText,
					HeaderMediaID:             matched.HeaderMediaID,
					HeaderPreviewURL:          matched.HeaderPreviewURL,
					DocumentFilename:          matched.DocumentFilename,
					BodyText:                  matched.BodyText,
					BodyParams:                activeParams,
					FooterText:                matched.FooterText,
					IsLTO:                     matched.IsLTO,
					LTOText:                   matched.LTOText,
					OfferCode:                 matched.OfferCode,
					OTPCode:                   activeOTP,
					OTPType:                   matched.OTPType,
					AutofillText:              matched.AutofillText,
					CodeExpirationMinutes:     matched.CodeExpirationMinutes,
					AddSecurityRecommendation: matched.AddSecurityRecommendation,
					PackageName:               matched.PackageName,
					SignatureHash:             matched.SignatureHash,
					ButtonCTA:                 matched.ButtonCTA,
					ButtonURL:                 matched.ButtonURL,
					QuickReplyButtons:         matched.QuickReplyButtons,
					CarouselCards:             matched.CarouselCards,
				}
			} else {
				submission.Text = "Template: " + tplName
				submission.TemplatePreview = &TemplatePreviewData{
					Name:       tplName,
					Language:   "en_US",
					Category:   "UTILITY",
					BodyText:   "Custom Graph API Template: " + tplName,
					BodyParams: runtimeBodyParams,
				}
			}
		}
	}

	record := handler.service.Ingest(submission, "Graph API /{phoneNumberId}/messages", msgType == "template")
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(map[string]any{
		"messaging_product": "whatsapp",
		"contacts": []map[string]string{
			{"input": recipient, "wa_id": strings.TrimPrefix(recipient, "+")},
		},
		"messages": []map[string]string{
			{"id": record.WAMID},
		},
	})
}

// HandleIngestIM evaluates and stores a new WhatsApp Cloud API message.
// @Summary Submit WhatsApp payload for Cloud API constraint evaluation
// @Accept json
// @Produce json
// @Param payload body IMSubmissionRequest true "WhatsApp Submission Payload"
// @Success 201 {object} IMRecord
// @Router /api/v1/whatsapp [post]
func (handler *HTTPHandler) HandleIngestIM(responseWriter http.ResponseWriter, request *http.Request) {
	var submission IMSubmissionRequest
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
	record := handler.service.Ingest(submission, source, submission.TemplatePreview != nil)
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(responseWriter).Encode(record)
}

// HandleBotInteract simulates an inbound user message or button click in Jasper's Market.
// @Summary Trigger Jasper's Market Conversation Bot flow or Status Receipt
// @Accept json
// @Produce json
// @Success 200 {array} IMRecord
// @Router /api/v1/whatsapp/interact [post]
func (handler *HTTPHandler) HandleBotInteract(responseWriter http.ResponseWriter, request *http.Request) {
	var req struct {
		SessionID     string `json:"sessionId"`
		CustomerPhone string `json:"customerPhone"`
		ButtonID      string `json:"buttonId"`
		Text          string `json:"text"`
		StatusWAMID   string `json:"statusWamid"`
		StatusValue   string `json:"statusValue"`
	}
	if err := json.NewDecoder(request.Body).Decode(&req); err != nil {
		http.Error(responseWriter, err.Error(), http.StatusBadRequest)
		return
	}
	if req.CustomerPhone == "" {
		req.CustomerPhone = "+15550199882"
	}
	responseWriter.Header().Set("Content-Type", "application/json")
	if req.StatusWAMID != "" {
		followUp := handler.service.HandleStatusReceipt(req.SessionID, req.StatusWAMID, req.StatusValue, req.CustomerPhone)
		if followUp != nil {
			_ = json.NewEncoder(responseWriter).Encode([]IMRecord{*followUp})
			return
		}
		_ = json.NewEncoder(responseWriter).Encode([]IMRecord{})
		return
	}
	records := handler.service.HandleJaspersMarketConversation(req.SessionID, req.CustomerPhone, req.ButtonID, req.Text, "VALID")
	_ = json.NewEncoder(responseWriter).Encode(records)
}

// HandleGetConfig returns the WABA Sanndbox Setup & .env configuration.
// @Summary Get WABA Setup Configuration & .env status
// @Produce json
// @Success 200 {object} WABASanndboxConfig
// @Router /api/v1/whatsapp/config [get]
func (handler *HTTPHandler) HandleGetConfig(responseWriter http.ResponseWriter, request *http.Request) {
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(handler.stateManager.GetConfig())
}

// HandleUpdateConfig updates the WABA Sanndbox Setup & runs checkEnvVariables().
// @Summary Update WABA Setup Configuration & validate media IDs
// @Accept json
// @Produce json
// @Success 200 {object} WABASanndboxConfig
// @Router /api/v1/whatsapp/config [post]
func (handler *HTTPHandler) HandleUpdateConfig(responseWriter http.ResponseWriter, request *http.Request) {
	var updated WABASanndboxConfig
	if err := json.NewDecoder(request.Body).Decode(&updated); err != nil {
		http.Error(responseWriter, err.Error(), http.StatusBadRequest)
		return
	}
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(handler.stateManager.UpdateConfig(updated))
}

// HandleUploadMedia mints a new phone-number-scoped WhatsApp Media ID.
// @Summary Upload runtime media and generate phone-scoped Media ID
// @Accept json
// @Produce json
// @Success 201 {object} WABAMediaRecord
// @Router /api/v1/whatsapp/media [post]
func (handler *HTTPHandler) HandleUploadMedia(responseWriter http.ResponseWriter, request *http.Request) {
	var req struct {
		Filename   string `json:"filename"`
		MIMEType   string `json:"mimeType"`
		SizeBytes  int64  `json:"sizeBytes"`
		EnvKey     string `json:"envKey"`
		PreviewURL string `json:"previewUrl"`
	}
	if err := json.NewDecoder(request.Body).Decode(&req); err != nil {
		http.Error(responseWriter, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Filename == "" {
		req.Filename = "custom_asset.jpg"
	}
	if req.MIMEType == "" {
		req.MIMEType = "image/jpeg"
	}
	if req.SizeBytes <= 0 {
		req.SizeBytes = 256000
	}
	record := handler.stateManager.RegisterMedia(req.Filename, req.MIMEType, req.SizeBytes, req.EnvKey, req.PreviewURL)
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(responseWriter).Encode(record)
}

// HandleListTemplates returns all registered WABA message templates.
// @Summary List WABA Message Templates (Jasper's Market + Custom)
// @Produce json
// @Success 200 {array} WABATemplateDefinition
// @Router /api/v1/whatsapp/templates [get]
func (handler *HTTPHandler) HandleListTemplates(responseWriter http.ResponseWriter, request *http.Request) {
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(handler.stateManager.ListTemplates())
}

// HandleCreateTemplate creates or updates a WABA message template (compatible with template.sh).
// @Summary Create WABA Message Template (/{apiVersion}/{wabaId}/message_templates)
// @Accept json
// @Produce json
// @Success 201 {object} WABATemplateDefinition
// @Router /api/v1/whatsapp/templates [post]
func (handler *HTTPHandler) HandleCreateTemplate(responseWriter http.ResponseWriter, request *http.Request) {
	var raw map[string]any
	if err := json.NewDecoder(request.Body).Decode(&raw); err != nil {
		http.Error(responseWriter, err.Error(), http.StatusBadRequest)
		return
	}

	name, _ := raw["name"].(string)
	language, _ := raw["language"].(string)
	category, _ := raw["category"].(string)
	headerFormat, _ := raw["headerFormat"].(string)
	headerText, _ := raw["headerText"].(string)
	headerMediaID, _ := raw["headerMediaId"].(string)
	headerPreviewURL, _ := raw["headerPreviewUrl"].(string)
	documentFilename, _ := raw["documentFilename"].(string)
	bodyText, _ := raw["bodyText"].(string)
	footerText, _ := raw["footerText"].(string)
	buttonCTA, _ := raw["buttonCta"].(string)
	buttonURL, _ := raw["buttonUrl"].(string)
	offerCode, _ := raw["offerCode"].(string)
	otpCode, _ := raw["otpCode"].(string)
	otpType, _ := raw["otpType"].(string)
	autofillText, _ := raw["autofillText"].(string)
	packageName, _ := raw["packageName"].(string)
	signatureHash, _ := raw["signatureHash"].(string)
	addSecurityRec, _ := raw["addSecurityRecommendation"].(bool)
	var codeExpirationMinutes int
	if expFloat, ok := raw["codeExpirationMinutes"].(float64); ok {
		codeExpirationMinutes = int(expFloat)
	}
	isLTO, _ := raw["isLto"].(bool)
	ltoText, _ := raw["ltoText"].(string)

	var bodyParams []string
	if rawParams, ok := raw["bodyParams"].([]any); ok {
		for _, item := range rawParams {
			if strVal, ok := item.(string); ok {
				bodyParams = append(bodyParams, strVal)
			}
		}
	}
	var quickReplyButtons []string
	if rawQRs, ok := raw["quickReplyButtons"].([]any); ok {
		for _, item := range rawQRs {
			if strVal, ok := item.(string); ok && strings.TrimSpace(strVal) != "" {
				quickReplyButtons = append(quickReplyButtons, strings.TrimSpace(strVal))
			}
		}
	}

	if components, ok := raw["components"].([]any); ok {
		for _, compRaw := range components {
			comp, ok := compRaw.(map[string]any)
			if !ok {
				continue
			}
			compType := strings.ToLower(fmt.Sprintf("%v", comp["type"]))
			switch compType {
			case "header":
				headerFormat = strings.ToUpper(fmt.Sprintf("%v", comp["format"]))
				if txt, ok := comp["text"].(string); ok {
					headerText = txt
				}
			case "body":
				if txt, ok := comp["text"].(string); ok {
					bodyText = txt
				}
				if secRec, ok := comp["add_security_recommendation"].(bool); ok && secRec {
					addSecurityRec = true
				}
			case "footer":
				if txt, ok := comp["text"].(string); ok {
					footerText = txt
				}
				if expMin, ok := comp["code_expiration_minutes"].(float64); ok {
					codeExpirationMinutes = int(expMin)
				}
			case "limited_time_offer":
				isLTO = true
				if ltoObj, ok := comp["limited_time_offer"].(map[string]any); ok {
					ltoText, _ = ltoObj["text"].(string)
				}
			case "buttons":
				if btns, ok := comp["buttons"].([]any); ok {
					for _, bRaw := range btns {
						if bMap, ok := bRaw.(map[string]any); ok {
							bType := strings.ToLower(fmt.Sprintf("%v", bMap["type"]))
							switch bType {
							case "url":
								buttonCTA, _ = bMap["text"].(string)
								buttonURL, _ = bMap["url"].(string)
							case "copy_code":
								offerCode, _ = bMap["example"].(string)
							case "otp":
								otpType = strings.ToUpper(fmt.Sprintf("%v", bMap["otp_type"]))
								autofillText, _ = bMap["autofill_text"].(string)
								packageName, _ = bMap["package_name"].(string)
								signatureHash, _ = bMap["signature_hash"].(string)
							case "quick_reply":
								if qrText, ok := bMap["text"].(string); ok && qrText != "" {
									quickReplyButtons = append(quickReplyButtons, qrText)
								}
							}
						}
					}
				}
			}
		}
	}

	if headerPreviewURL == "" && strings.EqualFold(headerFormat, "IMAGE") {
		headerPreviewURL = handler.stateManager.ResolveMediaPreviewURL(headerMediaID)
	}

	saved := handler.stateManager.SaveTemplate(WABATemplateDefinition{
		Name:                      name,
		Language:                  language,
		Category:                  category,
		Status:                    "APPROVED",
		HeaderFormat:              headerFormat,
		HeaderText:                headerText,
		HeaderMediaID:             headerMediaID,
		HeaderPreviewURL:          headerPreviewURL,
		DocumentFilename:          documentFilename,
		BodyText:                  bodyText,
		BodyParams:                bodyParams,
		FooterText:                footerText,
		IsLTO:                     isLTO,
		LTOText:                   ltoText,
		OfferCode:                 offerCode,
		OTPCode:                   otpCode,
		OTPType:                   otpType,
		AutofillText:              autofillText,
		CodeExpirationMinutes:     codeExpirationMinutes,
		AddSecurityRecommendation: addSecurityRec,
		PackageName:               packageName,
		SignatureHash:             signatureHash,
		ButtonCTA:                 buttonCTA,
		ButtonURL:                 buttonURL,
		QuickReplyButtons:         quickReplyButtons,
		RawComponents:             raw["components"],
	})

	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(responseWriter).Encode(saved)
}

// HandleListIM returns evaluated WhatsApp messages in memory.
// @Summary List evaluated WhatsApp records
// @Produce json
// @Param sessionId query string false "Browser Tab Session ID"
// @Success 200 {array} IMRecord
// @Router /api/v1/whatsapp [get]
func (handler *HTTPHandler) HandleListIM(responseWriter http.ResponseWriter, request *http.Request) {
	sessionID := request.URL.Query().Get("sessionId")
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(handler.repository.ListBySession(sessionID))
}

// HandleClearSession evicts all cached WhatsApp messages associated with a closed browser tab session.
// @Summary Clear session-scoped WhatsApp cache
// @Param sessionId query string true "Browser Tab Session ID"
// @Success 204 {string} string "Cleared"
// @Router /api/v1/whatsapp/clear [post]
func (handler *HTTPHandler) HandleClearSession(responseWriter http.ResponseWriter, request *http.Request) {
	sessionID := request.URL.Query().Get("sessionId")
	handler.repository.ClearSession(sessionID)
	responseWriter.WriteHeader(http.StatusNoContent)
}

// HandleSSE streams evaluated WhatsApp events over Server-Sent Events.
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
			_, _ = fmt.Fprintf(responseWriter, "event: im-evaluated\ndata: %s\n\n", event.JSONPayload)
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
    <title>WhatsApp Jasper's Market Sanndbox | OpenAPI 3.0 Documentation</title>
    <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui.css">
    <style>
        body { margin: 0; background: #f8fafc; color: #0f172a; font-family: system-ui, sans-serif; transition: background 0.2s; }
        .topbar { display: none; }
        .doc-nav { display: flex; align-items: center; justify-content: space-between; padding: 12px 24px; background: #ffffff; border-bottom: 1px solid #e2e8f0; }
        .doc-nav a { color: #008069; text-decoration: none; font-weight: 600; font-size: 13px; }
        html.dark body { background: #0f172a; color: #f8fafc; }
        html.dark .doc-nav { background: #1e293b; border-bottom-color: #334155; }
        html.dark .swagger-ui { filter: invert(88%) hue-rotate(180deg); }
    </style>
</head>
<body>
    <div class="doc-nav">
        <a href="/">&larr; Back to WhatsApp Jasper's Market Sanndbox (:9083)</a>
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
	repository := NewInMemoryIMRepository()
	evaluator := NewPlatformConstraintEvaluator()
	stateManager := NewSanndboxStateManager()
	broadcaster := NewSSEBroadcaster()
	service := NewIMSanndboxService(repository, evaluator, stateManager, broadcaster, parsedTemplates)

	handler := &HTTPHandler{
		service:      service,
		repository:   repository,
		stateManager: stateManager,
		broadcaster:  broadcaster,
		templates:    parsedTemplates,
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

	mux.HandleFunc("GET /webhook", handler.HandleWebhookVerify)
	mux.HandleFunc("POST /webhook", handler.HandleWebhookReceive)

	mux.HandleFunc("POST /{apiVersion}/{phoneNumberId}/messages", handler.HandleGraphMessages)
	mux.HandleFunc("POST /{phoneNumberId}/messages", handler.HandleGraphMessages)
	mux.HandleFunc("GET /{apiVersion}/{wabaId}/message_templates", handler.HandleListTemplates)
	mux.HandleFunc("POST /{apiVersion}/{wabaId}/message_templates", handler.HandleCreateTemplate)
	mux.HandleFunc("POST /{apiVersion}/{phoneNumberId}/media", handler.HandleUploadMedia)
	mux.HandleFunc("POST /{apiVersion}/{appId}/uploads", func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{
			"id": "upload:MTphd_" + generateIdentifier(),
			"h":  "4::" + generateIdentifier(),
		})
	})

	mux.HandleFunc("POST /api/v1/whatsapp", handler.HandleIngestIM)
	mux.HandleFunc("GET /api/v1/whatsapp", handler.HandleListIM)
	mux.HandleFunc("POST /api/v1/whatsapp/clear", handler.HandleClearSession)
	mux.HandleFunc("POST /api/v1/whatsapp/interact", handler.HandleBotInteract)
	mux.HandleFunc("GET /api/v1/whatsapp/config", handler.HandleGetConfig)
	mux.HandleFunc("POST /api/v1/whatsapp/config", handler.HandleUpdateConfig)
	mux.HandleFunc("POST /api/v1/whatsapp/media", handler.HandleUploadMedia)
	mux.HandleFunc("GET /api/v1/whatsapp/templates", handler.HandleListTemplates)
	mux.HandleFunc("POST /api/v1/whatsapp/templates", handler.HandleCreateTemplate)

	mux.HandleFunc("POST /api/v1/im", handler.HandleIngestIM)
	mux.HandleFunc("GET /api/v1/im", handler.HandleListIM)
	mux.HandleFunc("POST /api/v1/im/clear", handler.HandleClearSession)

	fmt.Println("[whatsapp-server] Meta Jasper's Market & Cloud API Sanndbox listening on http://localhost:9083 (Docs: http://localhost:9083/docs)")
	_ = http.ListenAndServe(":9083", mux)
}
