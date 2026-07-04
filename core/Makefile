BACK_DIR := ../back
FRONT_DIR := ../front

.PHONY: go js

go:
	protoc --go_out=$(BACK_DIR)/src/server --go_opt=paths=source_relative ./api/*.proto

js:
	protoc \
      --plugin=protoc-gen-ts_proto=$(FRONT_DIR)/node_modules/.bin/protoc-gen-ts_proto \
      --ts_proto_out=$(FRONT_DIR)/src \
      --ts_proto_opt=esModuleInterop=true,useOptionals=messages,useExactTypes=false,outputJsonMethods=false,enumsAsLiterals=true,stringEnums=false,outputEncodeMethods=true,env=browser \
      ./api/*.proto
