FROM node:22-alpine AS frontend
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm test && npm run build

FROM golang:1.26.10-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /web/dist ./web/dist
RUN CGO_ENABLED=0 go build -ldflags "-s -w" -o /mail-server ./cmd/mail

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /mail-server /mail-server
ENV MAIL_ADDR=0.0.0.0:4290
EXPOSE 4290
USER nonroot:nonroot
ENTRYPOINT ["/mail-server"]
