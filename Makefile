DIR := /var/www/crypto
BACK_DIR := $(DIR)/trader
BIN_NAME := trader
BIN_PATH := $(BACK_DIR)/$(BIN_NAME)
SERVICE_NAME := trader-bot

.PHONY: update build restart deploy logs status stop start enable

update:
	cd $(DIR) && git pull

build:
	cd $(BACK_DIR) && go build

restart:
	systemctl restart $(SERVICE_NAME)

start:
	systemctl start $(SERVICE_NAME)

stop:
	systemctl stop $(SERVICE_NAME)

status:
	systemctl status $(SERVICE_NAME)

logs:
	journalctl -u $(SERVICE_NAME) -f

enable:
	systemctl enable $(SERVICE_NAME)

deploy: update build restart
