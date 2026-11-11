FROM golang:1.23-alpine AS builder
RUN apk add --no-cache ca-certificates tzdata
RUN go install github.com/swaggo/swag/cmd/swag@v1.16.4
WORKDIR /workspace
COPY go.mod ./
COPY cmd/ ./cmd/
ARG SERVICE_NAME
RUN mkdir -p cmd/${SERVICE_NAME}/docs && [ -f cmd/${SERVICE_NAME}/docs/swagger.json ] || echo "{}" > cmd/${SERVICE_NAME}/docs/swagger.json
RUN swag init -d cmd/${SERVICE_NAME} -g main.go -o cmd/${SERVICE_NAME}/docs --outputTypes json,yaml
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /out/server ./cmd/${SERVICE_NAME}

FROM scratch
COPY LICENSE NOTICE /
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /out/server /server
USER 65534:65534
ENTRYPOINT ["/server"]
