FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY mapping.json ./
RUN CGO_ENABLED=0 go build -o /mediator .

FROM alpine:3.20
RUN apk --no-cache add ca-certificates
COPY --from=builder /mediator /mediator
COPY --from=builder /app/mapping.json /mapping.json
ENTRYPOINT ["/mediator"]
