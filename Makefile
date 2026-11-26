# Everyday commands. Run `make` to see the list.

COMPOSE := docker compose -f deploy/local/docker-compose.yml

# Values for `make send`, override like this: make send T=25 H=60
T ?= 21.5
H ?= 45
P ?= 101325
VM_NAME  ?= climate-vm
VM_ZONE  ?= us-central1-a
CLOUD_COMPOSE := docker compose -f deploy/local/docker-compose.cloud.yml
# Keep in sync with the version in .github/workflows/ci.yml
GOLANGCI_LINT_VERSION ?= v2.15.0

.DEFAULT_GOAL := help
.PHONY: help test lint vuln build run up down logs ps send metrics tunnel tunnel-keep cloud-up cloud-down

help: ## Show this list
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-12s %s\n", $$1, $$2}'

test: ## Run unit tests with the race detector
	go test -race -cover ./...

lint: ## Check formatting, run go vet and golangci-lint (same version as CI)
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed for:"; gofmt -l .; exit 1)
	go vet ./...
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

vuln: ## Scan dependencies for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

build: ## Build the binary into bin/
	go build -o bin/climate-app ./cmd/app

run: ## Run the service on this machine (needs a broker on 127.0.0.1:1883)
	go run ./cmd/app

up: ## Start the whole local stack (broker, service, Prometheus, Grafana)
	$(COMPOSE) up -d --build
	@echo "Service: http://localhost:2112/metrics  API: http://localhost:8080/api/v1/latest  Prometheus: http://localhost:9090  Grafana: http://localhost:3000 (admin/admin)"
	@echo "Next: make send"

down: ## Stop the local stack
	$(COMPOSE) down

logs: ## Follow the service logs
	$(COMPOSE) logs -f climate-app

ps: ## Show the state of the local stack
	$(COMPOSE) ps

send: ## Publish a test reading to the local broker (T=temperature H=humidity P=pressure in Pa)
	$(COMPOSE) exec mosquitto mosquitto_pub -h localhost -t home/room/climate \
		-m '{"temperature":$(T),"humidity":$(H),"pressure":$(P)}'
	@echo "Sent: $(T) C, $(H) %, $(P) Pa"

metrics: ## Show the room_* metrics exposed by the service
	@curl -s localhost:2112/metrics | grep '^room_'

tunnel: ## Forward the cloud metrics (localhost:12112) and the API (localhost:18080); keep it running
	gcloud compute ssh $(VM_NAME) --zone=$(VM_ZONE) --tunnel-through-iap -- \
		-N -L 12112:127.0.0.1:2112 -L 18080:127.0.0.1:8080 \
		-o ServerAliveInterval=30 -o ServerAliveCountMax=3 -o ExitOnForwardFailure=yes

tunnel-keep: ## Like `make tunnel`, but reconnects by itself after a drop (for overnight runs)
	@trap 'echo "Stopped."; exit 0' INT; \
	while true; do \
		$(MAKE) --no-print-directory tunnel; \
		echo "Tunnel closed, reconnecting in 5 s (Ctrl+C to stop)"; \
		sleep 5; \
	done

cloud-up: ## Grafana + Prometheus on this machine, reading the cloud VM (needs `make tunnel`)
	$(CLOUD_COMPOSE) up -d
	@echo "Grafana: http://localhost:3000 (admin/admin)"

cloud-down: ## Stop the cloud-mode Grafana and Prometheus
	$(CLOUD_COMPOSE) down
