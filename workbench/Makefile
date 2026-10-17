# Listening Observatory Workbench
#
#   make build              both programs for this machine, into build/
#   make test               the tests
#   make run                stand-in engine and Workbench locally (Ctrl-C ends both)
#   make deploy intranet    build for Linux and install on the intranet host
#   make deploy production  reserved, not set up yet
#   make admin intranet EMAIL=... NAME=...   create a master admin there, print a login link
#
# The intranet host's SSH name is not part of this repository. Put it in
# deploy/local.mk (ignored by git):
#
#   INTRANET_HOST = my-ssh-alias
#   INTRANET_ALLOW = 192.0.2.0/24 198.51.100.0/24   # networks that may reach the site

-include deploy/local.mk

GO      ?= go
BUILD   := build
LINUX   := $(BUILD)/linux-amd64
VERSION := $(shell git describe --always --dirty 2>/dev/null || echo unknown)

.PHONY: build test run deploy admin intranet production deploy-intranet deploy-production admin-intranet linux

# "make deploy intranet" and "make admin intranet": the second word is the
# target environment, not a goal of its own.
ENVIRONMENT := $(word 2,$(MAKECMDGOALS))
ifneq ($(filter deploy admin,$(firstword $(MAKECMDGOALS))),)
intranet production:
	@:
endif

build:
	$(GO) build -o $(BUILD)/ ./cmd/...

test:
	$(GO) vet ./...
	$(GO) test ./...

run: build
	@mkdir -p $(BUILD)/run
	@cd $(BUILD)/run && trap 'kill 0' EXIT INT TERM; \
	../listen-standin -socket engine.sock & sleep 0.5; \
	../workbench -db workbench.db -engine engine.sock -base-url http://127.0.0.1:8110

linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -o $(LINUX)/ ./cmd/...

deploy:
	@case "$(ENVIRONMENT)" in \
	intranet) $(MAKE) --no-print-directory deploy-intranet ;; \
	production) $(MAKE) --no-print-directory deploy-production ;; \
	*) echo "usage: make deploy intranet | make deploy production"; exit 2 ;; \
	esac

admin:
	@case "$(ENVIRONMENT)" in \
	intranet) $(MAKE) --no-print-directory admin-intranet EMAIL="$(EMAIL)" NAME="$(NAME)" ;; \
	*) echo "usage: make admin intranet EMAIL=... NAME=..."; exit 2 ;; \
	esac

deploy-intranet: test linux
	@test -n "$(INTRANET_HOST)" || { echo "set INTRANET_HOST in deploy/local.mk"; exit 2; }
	@echo "== deploying $(VERSION) to the intranet host"
	@dir=$$(ssh $(INTRANET_HOST) mktemp -d /tmp/low-deploy.XXXXXX) && \
	scp -q $(LINUX)/workbench $(INTRANET_HOST):$$dir/low-workbench && \
	printf '%s\n' $(INTRANET_ALLOW) | ssh $(INTRANET_HOST) "cat > $$dir/allow.txt" && \
	scp -q deploy/install.sh deploy/systemd/*.service deploy/nginx/*.conf $(INTRANET_HOST):$$dir/ && \
	ssh $(INTRANET_HOST) "sudo bash $$dir/install.sh; rc=\$$?; rm -rf $$dir; exit \$$rc"

deploy-production:
	@echo "production (low.vrontier.org) is not set up yet"; exit 2

admin-intranet:
	@test -n "$(INTRANET_HOST)" || { echo "set INTRANET_HOST in deploy/local.mk"; exit 2; }
	@test -n "$(EMAIL)" -a -n "$(NAME)" || { echo "usage: make admin intranet EMAIL=... NAME=..."; exit 2; }
	@ssh $(INTRANET_HOST) "sudo -u low sh -c '. /etc/low/workbench.env; /usr/local/bin/low-workbench -db /var/lib/low/workbench.db -base-url \$$LOW_BASE_URL admin -email \"$(EMAIL)\" -name \"$(NAME)\"'"
