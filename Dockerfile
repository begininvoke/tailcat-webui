# syntax=docker/dockerfile:1.7
FROM node:26-alpine AS web-builder
RUN npm install --global pnpm@11.3.0
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml web/.npmrc ./
RUN pnpm install --frozen-lockfile --ignore-scripts
COPY web/ ./
RUN pnpm build

FROM golang:1.27.0-alpine AS go-builder
RUN apk add --no-cache ca-certificates git
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=web-builder /src/web/dist /src/web/dist
RUN rm -rf webdist/dist && cp -R /src/web/dist webdist/dist
ARG VERSION=dev
ARG BUILD_TIME=unknown
ARG GIT_COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/ca-x/tailcat-webui/internal/version.Version=${VERSION} -X github.com/ca-x/tailcat-webui/internal/version.BuildTime=${BUILD_TIME} -X github.com/ca-x/tailcat-webui/internal/version.GitCommit=${GIT_COMMIT}" \
    -o /out/tailcat-webui ./cmd/tailcat-webui

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S tailcat \
    && adduser -S -G tailcat -h /data tailcat \
    && install -d -o tailcat -g tailcat -m 0700 /data
COPY --from=go-builder /out/tailcat-webui /usr/local/bin/tailcat-webui
USER tailcat
WORKDIR /data
ENV TAILCAT_WEBUI_ADDR=:8080 \
    TAILCAT_WEBUI_DATA_DIR=/data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/tailcat-webui"]
