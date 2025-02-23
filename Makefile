# Copyright Contributors to the Open Cluster Management project
BINARYDIR := bin

KUBECTL?=kubectl
KUSTOMIZE?=kustomize

ETCD_NS?=multicluster-controlplane-etcd
HUB_NAME?=multicluster-controlplane

IMAGE_REGISTRY?=quay.io/open-cluster-management
IMAGE_TAG?=latest
export IMAGE_NAME?=$(IMAGE_REGISTRY)/multicluster-controlplane:$(IMAGE_TAG)

check-copyright: 
	@hack/check/check-copyright.sh

check: check-copyright 

verify-gocilint:
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.45.2
	go vet ./...
	golangci-lint run --timeout=3m ./...

verify: verify-gocilint

all: clean vendor build run
.PHONY: all

run:
	hack/start-multicluster-controlplane.sh
.PHONY: run

build-bin-release:
	$(rm -rf bin)
	$(mkdir -p bin)
	GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -gcflags=-trimpath=x/y -o bin/multicluster-controlplane ./cmd/main.go && tar -czf bin/multicluster_controlplane_darwin_amd64.tar.gz LICENSE -C bin/ multicluster-controlplane
	GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -gcflags=-trimpath=x/y -o bin/multicluster-controlplane ./cmd/main.go && tar -czf bin/multicluster_controlplane_darwin_arm64.tar.gz LICENSE -C bin/ multicluster-controlplane
	GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -gcflags=-trimpath=x/y -o bin/multicluster-controlplane ./cmd/main.go && tar -czf bin/multicluster_controlplane_linux_amd64.tar.gz LICENSE -C bin/ multicluster-controlplane
	GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -gcflags=-trimpath=x/y -o bin/multicluster-controlplane ./cmd/main.go && tar -czf bin/multicluster_controlplane_linux_arm64.tar.gz LICENSE -C bin/ multicluster-controlplane
	GOOS=linux GOARCH=ppc64le go build -ldflags="-s -w" -gcflags=-trimpath=x/y -o bin/multicluster-controlplane ./cmd/main.go && tar -czf bin/multicluster_controlplane_linux_ppc64le.tar.gz LICENSE -C bin/ multicluster-controlplane
	GOOS=linux GOARCH=s390x go build -ldflags="-s -w" -gcflags=-trimpath=x/y -o bin/multicluster-controlplane ./cmd/main.go && tar -czf bin/multicluster_controlplane_linux_s390x.tar.gz LICENSE -C bin/ multicluster-controlplane
	GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -gcflags=-trimpath=x/y -o bin/multicluster-controlplane.exe ./cmd/main.go && zip -q bin/multicluster_controlplane_windows_amd64.zip LICENSE -j bin/multicluster-controlplane.exe

build: 
	$(shell if [ ! -e $(BINARYDIR) ];then mkdir -p $(BINARYDIR); fi)
	go build -o bin/multicluster-controlplane cmd/server/main.go 
.PHONY: build

image:
	docker build -f Dockerfile -t $(IMAGE_NAME) .
.PHONY: image

push:
	docker push $(IMAGE_NAME)
.PHONY: push

clean:
	rm -rf bin .ocmconfig vendor
.PHONY: clean

vendor: 
	go mod tidy 
	go mod vendor
.PHONY: vendor

update:
	bash -x hack/crd-update/copy-crds.sh
.PHONY: update

deploy-etcd: 
	$(KUBECTL) get ns $(ETCD_NS); if [ $$? -ne 0 ] ; then $(KUBECTL) create ns $(ETCD_NS); fi
	hack/deploy-etcd.sh

deploy:
	hack/deploy-multicluster-controlplane.sh 

destroy:
	$(KUSTOMIZE) build hack/deploy/controlplane | $(KUBECTL) delete --namespace $(HUB_NAME) --ignore-not-found -f -
	$(KUBECTL) delete ns $(HUB_NAME) --ignore-not-found
	rm -r hack/deploy/cert-$(HUB_NAME)

deploy-work-manager-addon:
	$(KUBECTL) apply -k hack/deploy/addon/work-manager/hub --kubeconfig=hack/deploy/cert-$(HUB_NAME)/kubeconfig
	cp hack/deploy/addon/work-manager/manager/kustomization.yaml hack/deploy/addon/work-manager/manager/kustomization.yaml.tmp
	cd hack/deploy/addon/work-manager/manager && $(KUSTOMIZE) edit set namespace $(HUB_NAME)
	$(KUSTOMIZE) build hack/deploy/addon/work-manager/manager | $(KUBECTL) apply -f -
	mv hack/deploy/addon/work-manager/manager/kustomization.yaml.tmp hack/deploy/addon/work-manager/manager/kustomization.yaml

deploy-managed-serviceaccount-addon:
	$(KUBECTL) apply -k hack/deploy/addon/managed-serviceaccount/hub --kubeconfig=hack/deploy/cert-$(HUB_NAME)/kubeconfig
	cp hack/deploy/addon/managed-serviceaccount/manager/kustomization.yaml hack/deploy/addon/managed-serviceaccount/manager/kustomization.yaml.tmp
	cd hack/deploy/addon/managed-serviceaccount/manager && $(KUSTOMIZE) edit set namespace $(HUB_NAME)
	$(KUSTOMIZE) build hack/deploy/addon/managed-serviceaccount/manager | $(KUBECTL) apply -f -
	mv hack/deploy/addon/managed-serviceaccount/manager/kustomization.yaml.tmp hack/deploy/addon/managed-serviceaccount/manager/kustomization.yaml

deploy-policy-addon:
	$(KUBECTL) apply -k hack/deploy/addon/policy/hub --kubeconfig=hack/deploy/cert-$(HUB_NAME)/kubeconfig
	cp hack/deploy/addon/policy/manager/kustomization.yaml hack/deploy/addon/policy/manager/kustomization.yaml.tmp
	cd hack/deploy/addon/policy/manager && $(KUSTOMIZE) edit set namespace $(HUB_NAME)
	$(KUSTOMIZE) build hack/deploy/addon/policy/manager | $(KUBECTL) apply -f -
	mv hack/deploy/addon/policy/manager/kustomization.yaml.tmp hack/deploy/addon/policy/manager/kustomization.yaml


deploy-all: deploy deploy-work-manager-addon deploy-managed-serviceaccount-addon deploy-policy-addon

# test
export CONTROLPLANE_NUMBER ?= 2
export VERBOSE ?= 5

cleanup-e2e:
	./test/e2e/hack/cleanup.sh
.PHONY: cleanup-e2e

build-e2e-test:
	go test -c ./test/e2e -o _output/e2e.test
.PHONY: build-e2e-test

test-e2e: cleanup-e2e build-e2e-test
	./test/e2e/hack/e2e.sh
.PHONY: test-e2e

cleanup-integration:
	./test/integration/hack/cleanup.sh
.PHONY: cleanup-integration

test-integration: cleanup-integration build
	./test/integration/hack/integration.sh
.PHONY: test-integration
