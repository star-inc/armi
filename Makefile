BUILD_DIR := $(CURDIR)/build
BINARY := $(BUILD_DIR)/armi

.PHONY: build clean clean-deps image image-clean image-run dev test swagger

build:
	@mkdir -p $(BUILD_DIR)
	go build -o $(BINARY) ./cmd/armi

clean: clean-deps
	rm -rf $(BUILD_DIR)

clean-deps:
	go clean -cache

image:
	docker build -t armi:local .

image-clean:
	docker rmi armi:local || true

image-run: image-clean image
	docker run --rm -p 8080:8000 armi:local

dev:
	@go install github.com/air-verse/air@latest
	air

test: clean
	go test ./... -coverprofile=coverage.out
	go tool cover -html=coverage.out

swagger:
	@go install github.com/swaggo/swag/cmd/swag@latest
	swag init -g cmd/armi/main.go -o ./docs
