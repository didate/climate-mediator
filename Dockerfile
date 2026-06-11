FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY mapping.json ./
RUN CGO_ENABLED=0 go build -o /mediator ./cmd/server/

FROM alpine:3.20
RUN apk --no-cache add ca-certificates
COPY --from=builder /mediator /mediator
COPY --from=builder /app/mapping.json /mapping.json
ENTRYPOINT ["/mediator"]
