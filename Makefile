WORKPLACE := $(shell pwd)

.PHONY: build-image clean-image run

build:
	@mkdir -p $(WORKPLACE)/build
	cd cmd/armi && go build -o $(WORKPLACE)/build

clean: clean-deps
	rm -rf $(WORKPLACE)/build

clean-deps:
	go clean -cache

image:
	docker build -t armi:local .

image-clean:
	docker rmi armi:local || true

image-run: image-clean image
	docker run --rm -p 8080:8080 armi:local

dev:
	@go install github.com/air-verse/air@latest
	cd $(WORKPLACE) && air

test: clean
	go test ./... -coverprofile=coverage.out
	go tool cover -html=coverage.out

swagger:
	@go install github.com/swaggo/swag/cmd/swag@latest
	swag init -g cmd/armi/main.go -o ./docs
