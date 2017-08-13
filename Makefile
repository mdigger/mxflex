appname := mxflex

DATE    := $(shell date -u +%Y-%m-%d)
VER     := $(or $(shell git describe --abbrev=0 --tags), "0.0")
BUILD   := $(shell git rev-list --all --count)
GIT     := $(shell git rev-parse --short HEAD)

LTAG_COM := $(or $(shell git rev-list --tags --max-count=1), "")
LTAG     := $(or $(shell git describe --tags $(LTAG_COM)), "")
REV      := $(or $(shell git rev-list $(LTAG).. --count), "0")

FLAGS   := -ldflags "-X main.date=$(DATE) -X main.version=$(VER).$(REV) -X main.build=$(BUILD) -X main.git=$(GIT)"

build = GOOS=$(1) GOARCH=$(2) go build -o build/$(appname)$(3) $(FLAGS)
tar = cd ./build && tar -czf $(1)_$(2).tar.gz $(appname)$(3) && rm $(appname)$(3)
zip = cd ./build && zip $(1)_$(2).zip $(appname)$(3) && rm $(appname)$(3)

.PHONY: all windows darwin linux clean build

info:
	@echo "---------------------"
	@echo "Date:       $(DATE)"
	@echo "Version:    $(VER)"
	@echo "Revision:   $(REV)"
	@echo "Build:      $(BUILD)"
	@echo "GIT:        $(GIT)"
	@echo "---------------------"

build:
	go build -race -o $(appname) $(FLAGS)

debug: build
	./$(appname) -mx mx://d3test:981211@89.185.246.134 -ext 3095,3099,3044 -csta -color > csta.log

clean:
	rm -rf build/

##### LINUX BUILDS #####
linux: build/linux_arm.tar.gz build/linux_arm64.tar.gz build/linux_386.tar.gz build/linux_amd64.tar.gz

build/linux_386.tar.gz: $(sources)
	$(call build,linux,386,)
	$(call tar,linux,386)

build/linux_amd64.tar.gz: $(sources)
	$(call build,linux,amd64,)
	$(call tar,linux,amd64)

build/linux_arm.tar.gz: $(sources)
	$(call build,linux,arm,)
	$(call tar,linux,arm)

build/linux_arm64.tar.gz: $(sources)
	$(call build,linux,arm64,)
	$(call tar,linux,arm64)

##### DARWIN (MAC) BUILDS #####
darwin: build/darwin_amd64.tar.gz

build/darwin_amd64.tar.gz: $(sources)
	$(call build,darwin,amd64,)
	$(call tar,darwin,amd64)


##### WINDOWS BUILDS #####
windows: build/windows_386.zip build/windows_amd64.zip

build/windows_386.zip: $(sources)
	$(call build,windows,386,.exe)
	$(call zip,windows,386,.exe)

build/windows_amd64.zip: $(sources)
	$(call build,windows,amd64,.exe)
	$(call zip,windows,amd64,.exe)
