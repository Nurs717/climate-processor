# Everyday commands. Run `make` to see the list.

COMPOSE := docker compose -f deploy/local/docker-compose.yml

# Values for `make send`, override like this: make send T=25 H=60
T ?= 21.5
H ?= 45
P ?= 101325
VM_NAME  ?= climate-vm
VM_ZONE  ?= us-central1-a
CLOUD_COMPOSE := docker compose -f deploy/local/docker-compose.cloud.yml

.DEFAULT_GOAL := help
.PHONY: help test lint vuln build run up down logs ps send metrics tunnel cloud-up cloud-down

help: ## Show this list
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-9s %s\n", $$1, $$2}'

test: ## Run unit tests with the race detector
	go test -race -cover ./...

lint: ## Check formatting, then run go vet
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed for:"; gofmt -l .; exit 1)
	go vet ./...

vuln: ## Scan dependencies for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

build: ## Build the binary into bin/
	go build -o bin/climate-app ./cmd/app

run: ## Run the service on this machine (needs a broker on 127.0.0.1:1883)
	go run ./cmd/app

up: ## Start the whole local stack (broker, service, Prometheus, Grafana)
	$(COMPOSE) up -d --build
	@echo "Service: http://localhost:2112/metrics  Prometheus: http://localhost:9090  Grafana: http://localhost:3000 (admin/admin)"
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

tunnel: ## Forward the cloud service metrics to localhost:12112 (keep it running)
	gcloud compute ssh $(VM_NAME) --zone=$(VM_ZONE) --tunnel-through-iap -- -N -L 12112:127.0.0.1:2112

cloud-up: ## Grafana + Prometheus on this machine, reading the cloud VM (needs `make tunnel`)
	$(CLOUD_COMPOSE) up -d
	@echo "Grafana: http://localhost:3000 (admin/admin)"

cloud-down: ## Stop the cloud-mode Grafana and Prometheus
	$(CLOUD_COMPOSE) down
