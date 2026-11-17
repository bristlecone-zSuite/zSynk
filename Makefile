GO ?= go
BIN := bin/synk
# Analysis tools must be built with the same toolchain that compiles the module.
TOOLCHAIN := $(shell awk '/^toolchain/ {print $$2}' go.mod)
TOOLRUN := GOTOOLCHAIN=$(TOOLCHAIN) $(GO) run
# xcodebuild needs the full Xcode. When the calling shell's selection
# works, leave it alone — CI runners and other machines choose their own,
# possibly newer Xcode than an alphabetical guess would. Only when
# xcodebuild is unusable (plain shell with Command Line Tools selected)
# does the Makefile pick the installed Xcode itself.
XCODE_STATUS := $(shell xcodebuild -version >/dev/null 2>&1 && echo ok || echo missing)
ifneq ($(XCODE_STATUS),ok)
export DEVELOPER_DIR := $(shell ls -d /Applications/Xcode*.app/Contents/Developer 2>/dev/null | head -1)
endif

.PHONY: test race check lint build vet fmt doctor app app-test lint-swift

test:
	$(GO) test ./...

race:
	$(GO) test -race -count=1 ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

lint:
	test -z "$$(gofmt -l .)"
	# v0.8.1 can't read Go 1.27.2 export data; this is the same master commit
	# test.yml pins. Move both back to a release once one includes the fix.
	$(TOOLRUN) honnef.co/go/tools/cmd/staticcheck@v0.7.0-0.dev.0.20261009230814-452d5bb86b45 ./...
	$(TOOLRUN) golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

check: vet race lint

build:
	mkdir -p bin
	$(GO) build -o $(BIN) ./cmd/synk

doctor: build
	SYNK_DIR=.data ./$(BIN) init --name "dev"
	SYNK_DIR=.data ./$(BIN) doctor

# The Mac app: generate the Xcode project, build the engine it carries,
# and run the app's tests, which drive two real daemons.
app: build
	xcodegen generate
	xcodebuild -project Synk.xcodeproj -scheme Synk -configuration Debug \
		-destination 'platform=macOS' build CODE_SIGNING_ALLOWED=NO

app-test: build
	xcodegen generate
	xcodebuild -project Synk.xcodeproj -scheme Synk -destination 'platform=macOS' \
		test CODE_SIGNING_ALLOWED=NO

lint-swift:
	swiftlint lint --quiet apps/

# A distributable signed build. Pass the maintainer's Developer ID
# Application identity to sign for distribution:
#
#   make release SYNK_CODE_SIGN_IDENTITY="Developer ID Application: Name (TEAMID)"
#
# Without one the bundle is ad-hoc signed: same machinery, verified seals,
# but it only runs on the machine that built it. The post-build phase signs
# the bundled engine and re-signs the app, and the seals are verified here —
# the app would silently break Gatekeeper assessment otherwise.
SYNK_CODE_SIGN_IDENTITY ?=
ifeq ($(SYNK_CODE_SIGN_IDENTITY),)
SIGN_ARGS := CODE_SIGN_IDENTITY=-
else
SIGN_ARGS := CODE_SIGN_IDENTITY='$(SYNK_CODE_SIGN_IDENTITY)' CODE_SIGNING_ALLOWED=YES
endif

release: build
	xcodegen generate
	xcodebuild -project Synk.xcodeproj -scheme Synk -configuration Release \
		-destination 'platform=macOS' -derivedDataPath .build/release build $(SIGN_ARGS)
	codesign --verify --deep --strict .build/release/Build/Products/Release/Synk.app
	@echo "Verified: .build/release/Build/Products/Release/Synk.app"

# The iOS framework is generated from the mobile package with gomobile.
# The x/mobile version comes from go.mod so the framework matches the
# module that builds the rest of Synk. The framework is a build product:
# it is git-ignored and every build regenerates it.
XMOBILE := $(shell awk '$$2 == "golang.org/x/mobile" {print $$3}' go.mod)

ios-framework:
	mkdir -p apps/ios/Frameworks bin
	GOBIN=$(CURDIR)/bin $(GO) install golang.org/x/mobile/cmd/gomobile@$(XMOBILE)
	GOBIN=$(CURDIR)/bin $(GO) install golang.org/x/mobile/cmd/gobind@$(XMOBILE)
	PATH=$(CURDIR)/bin:$(PATH) bin/gomobile bind -target=ios \
		-o apps/ios/Frameworks/SynkCore.xcframework \
		github.com/bristlecone-zSuite/zSynk/mobile

# The iOS app: generate the project and build it for the simulator. Run
# make ios-framework first (CI does, as its own step so a bind failure
# shows on its own); this target does not regenerate the framework.
ios-build:
	xcodegen generate
	xcodebuild -project Synk.xcodeproj -scheme Synk-iOS \
		-destination 'generic/platform=iOS Simulator' build CODE_SIGNING_ALLOWED=NO \
		-derivedDataPath .build/ios

# The iOS tests run two embedded engines on the iOS runtime — the same
# story the Go tests prove at the protocol level, now through the bridge
# the app itself drives. The work lives in scripts/ios-test.sh so a red
# run explains itself in the step summary.
ios-test:
	sh scripts/ios-test.sh

.PHONY: release ios-framework ios-build ios-test
