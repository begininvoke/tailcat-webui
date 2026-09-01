.PHONY: generate verify-generated check-secrets web-install web-build test lint build verify dev

generate:
	go generate ./ent

verify-generated: generate
	git diff --exit-code -- ent

check-secrets:
	./scripts/check-secrets_test.sh
	./scripts/check-secrets.sh

web-install:
	cd web && pnpm install --frozen-lockfile

web-build:
	cd web && pnpm build
	rm -rf webdist/dist
	cp -R web/dist webdist/dist

test:
	go test -race ./...
	cd web && pnpm test

lint:
	go vet ./...
	cd web && pnpm lint

build: web-build
	CGO_ENABLED=0 go build -trimpath -o bin/tailcat-webui ./cmd/tailcat-webui

verify: verify-generated check-secrets lint test build
	diff -qr web/dist webdist/dist

dev:
	TAILCAT_WEBUI_DEMO_MODE=true TAILCAT_WEBUI_MASTER_KEY=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA= go run ./cmd/tailcat-webui
