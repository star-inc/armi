TARGET_DIRECTORY := $(shell pwd)

target:
	@mkdir -p $(TARGET_DIRECTORY)/build
	cd cmd/armi && go build -o $(TARGET_DIRECTORY)/build

clean: clean-deps
	rm -rf $(WORKPLACE)/build

clean-deps:
	go clean -cache

dev:
	@go install github.com/air-verse/air@latest
	cd $(TARGET_DIRECTORY) && air
