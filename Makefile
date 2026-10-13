.PHONY: build deploy-web
build:
	CGO_ENABLED=0 go build -buildvcs=false -o wiregram ./cmd/wiregram

# Uploads web/ to the Cloudflare Worker named in wrangler.toml.
deploy-web:
	npx wrangler@4 deploy
