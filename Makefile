IMG ?= ghcr.io/turborel/turborel-operator:nightly
CHART_DIR ?= ../charts/charts/turborel-operator
LOCALBIN := $(CURDIR)/bin
CONTROLLER_GEN := $(LOCALBIN)/controller-gen
ENVTEST := $(LOCALBIN)/setup-envtest
GOVULNCHECK := $(LOCALBIN)/govulncheck
CONTROLLER_TOOLS_VERSION := v0.18.0
ENVTEST_VERSION := release-0.21
ENVTEST_K8S_VERSION ?= 1.33.0

.PHONY: security all build fmt vet generate manifests test test-integration test-e2e docker-build docker-push chart tools setup-envtest
all: build

build: generate manifests fmt vet
	go build -trimpath -o bin/manager ./cmd

fmt:
	gofmt -w api cmd internal

vet:
	go vet ./...

generate: $(CONTROLLER_GEN)
	$(CONTROLLER_GEN) object paths="./..."

manifests: $(CONTROLLER_GEN)
	mkdir -p .test/crds .test/rbac
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd paths="./..." output:crd:artifacts:config=.test/crds output:rbac:artifacts:config=.test/rbac

test: generate manifests vet
	go test -race -shuffle=on -coverprofile=cover.out ./...

test-integration: generate manifests setup-envtest
	KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -race -tags=integration -count=1 ./api/...

# Uses its own kind cluster and kubeconfig. TURBOREL_IMAGE must name a local image.
test-e2e: generate manifests
	./hack/e2e.sh

docker-build:
	docker build -t $(IMG) .

docker-push:
	docker push $(IMG)

# The chart lives in turborel/charts; the Go API is the source of its CRDs.
chart: manifests
	test -f $(CHART_DIR)/Chart.yaml
	cp .test/crds/*.yaml $(CHART_DIR)/crds/
	helm lint $(CHART_DIR)

setup-envtest: $(ENVTEST)
	$(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path

tools: $(CONTROLLER_GEN) $(ENVTEST)

$(LOCALBIN):
	mkdir -p $(LOCALBIN)

$(CONTROLLER_GEN): | $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

$(ENVTEST): | $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)

security: $(GOVULNCHECK)
	$(GOVULNCHECK) ./...

$(GOVULNCHECK): | $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install golang.org/x/vuln/cmd/govulncheck@v1.10.0
