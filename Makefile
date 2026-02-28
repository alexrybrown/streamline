.PHONY: generate build test lint mocks clean docker-up docker-down

generate:
	buf generate

build: generate
	go build ./cmd/...

test:
	go test ./... -v -race -timeout=60s

lint:
	buf lint
	golangci-lint run ./...

mocks:
	mockery

clean:
	rm -rf gen/ bin/

docker-up:
	docker compose -f deployments/docker/docker-compose.yml up -d

docker-down:
	docker compose -f deployments/docker/docker-compose.yml down

download-bbb:
	@mkdir -p assets
	@if [ ! -f assets/bbb.mp4 ]; then \
		echo "Downloading Big Buck Bunny (1080p)..."; \
		curl -L -o assets/bbb.mp4 "http://commondatastorage.googleapis.com/gtv-videos-bucket/sample/BigBuckBunny.mp4"; \
	fi
