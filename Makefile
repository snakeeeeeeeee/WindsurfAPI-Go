BINARY_NAME=windsurfapi-go
BUILD_DIR=bin

.PHONY: build run test clean tidy dashboard docker-build docker-up docker-down

build:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/server
	@echo "✅ 编译完成: $(BUILD_DIR)/$(BINARY_NAME)"

dashboard:
	cd web/dashboard && npm run build

run: build
	./$(BUILD_DIR)/$(BINARY_NAME)

test:
	go test ./... -v

docker-build:
	docker build -t windsurfapi-go:local .

docker-up:
	docker compose up -d --build

docker-down:
	docker compose down

clean:
	rm -rf $(BUILD_DIR)
	rm -f data/windsurf.db

tidy:
	go mod tidy
