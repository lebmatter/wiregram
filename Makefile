.PHONY: build deploy-web docs deploy-docs
build:
	CGO_ENABLED=0 go build -buildvcs=false -o wiregram ./cmd/wiregram

# Uploads web/ to the Cloudflare Worker named in wrangler.toml.
deploy-web:
	npx wrangler@4 deploy

# Builds the VitePress docs site in docs/ into docs/.vitepress/dist.
docs:
	cd docs && npm ci && npm run build

# Uploads the built docs to the Cloudflare Worker named in docs/wrangler.toml.
deploy-docs: docs
	cd docs && npx wrangler@4 deploy
