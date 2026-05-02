FROM golang:1.26-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal

ARG APP
RUN go build -o /out/${APP} ./cmd/${APP}

FROM alpine:3.21

WORKDIR /app

ARG APP
COPY --from=builder /out/${APP} /app/${APP}
ENV APP=${APP}

CMD ["/bin/sh", "-c", "/app/${APP}"]
