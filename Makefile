.PHONY: build deploy-web docs deploy-docs
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# GOAMD64=v2 gives go-llama its SIMD kernels; without them local models crawl.
build:
	CGO_ENABLED=0 GOAMD64=v2 go build -buildvcs=false -ldflags "-X main.version=$(VERSION)" \
		-o wiregram ./cmd/wiregram

# Uploads web/ to the Cloudflare Worker named in wrangler.toml.
deploy-web:
	npx wrangler@4 deploy

# Builds the VitePress docs site in docs/ into docs/.vitepress/dist.
docs:
	cd docs && npm ci && npm run build

# Uploads the built docs to the Cloudflare Worker named in docs/wrangler.toml.
deploy-docs: docs
	cd docs && npx wrangler@4 deploy
