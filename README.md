# Communications Sanndbox System

Self-contained Go Communications Sanndbox with a central Server Launcher Hub and four independent channel servers (Email, SMS, WhatsApp Business / Jasper's Market, Telegram Desktop).

## Services & Ports

| Service | Protocol / Port | UI Emulation & Evaluation Matrix | OpenAPI 3.0 UI |
| :--- | :--- | :--- | :--- |
| **Portal Hub** | HTTP `:9080` | Central channel selector & producer reference | - |
| **Email Sanndbox** | SMTP `:2525` / HTTP `:9081` | 3-pane Webmail Inbox UI + HTML client rendering & attachment threat scanner | `http://localhost:9081/docs` |
| **SMS Sanndbox** | HTTP `:9082` | Smartphone Messages UI + GSM-7 (160/153) & UCS-2 (70/67) PDU segment evaluator | `http://localhost:9082/docs` |
| **WhatsApp Sanndbox** | HTTP `:9083` | Meta `whatsapp-business-jaspers-market` Reference Architecture (`/{apiVersion}/{phoneNumberId}/messages`, `/webhook`, `/{wabaId}/message_templates`, Setup & `.env` generator, Template Studio, 15s Redis follow-up cache, `entry_source_attributed` telemetry) | `http://localhost:9083/docs` |
| **Telegram Sanndbox** | HTTP `:9084` | Telegram Desktop UI + official [core.telegram.org/methods](https://core.telegram.org/methods) TL-Schema RPC evaluator | `http://localhost:9084/docs` |

---

## External Application Integration Examples

### 1. Email Server (`SMTP :2525` or `HTTP :9081`)

```bash
curl -X POST http://localhost:9081/api/v1/emails -H "Content-Type: application/json" -d '{"sender":"billing@acme.internal","recipients":["qa@Sanndbox.local"],"subject":"Invoice #4821","htmlBody":"<!DOCTYPE html><html><head><meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\"></head><body><table><tr><td>Invoice Ready</td></tr></table></body></html>"}'
```

### 2. SMS Server (`HTTP :9082`)

```bash
curl -X POST http://localhost:9082/api/v1/sms -H "Content-Type: application/json" -d '{"sender":"AUTH-OTP","recipient":"+358401234567","message":"Your verification code is 948201. Cost: 5 EUR."}'
```

### 3. WhatsApp Business Cloud API & Jasper's Market (`HTTP :9083`)

- **Send Graph API Template (`services/graph-api.js`)**:
```bash
curl -X POST http://localhost:9083/v21.0/106540352242922/messages -H "Content-Type: application/json" -d '{"messaging_product":"whatsapp","to":"+15550199882","type":"template","template":{"name":"strawberries_limited_offer","language":{"code":"en_US"}}}'
```

- **Simulate Inbound Customer Webhook (`app.js` & `services/conversation.js`)**:
```bash
curl -X POST http://localhost:9083/webhook -H "Content-Type: application/json" -d '{"object":"whatsapp_business_account","entry":[{"changes":[{"value":{"metadata":{"phone_number_id":"106540352242922"},"messages":[{"id":"wamid.inbound01","from":"+15550199882","timestamp":"1731320000","type":"text","text":{"body":"Get started"}}]}}]}]}'
```

### 4. Telegram MTProto Methods Server (`HTTP :9084`)

```bash
curl -X POST http://localhost:9084/api/v1/telegram -H "Content-Type: application/json" -d '{"method":"messages.sendMessage","peer":"inputPeerChannel#994812","sender":"@FraudOpsMTProtoBot","randomId":88492019283,"message":"[MTProto] Rule evaluation moved to DB (item 12); legacy alerts without case_key will emit 1 fresh alert before grouping."}'
```
