appname  ?= mxflex

DATE     ?= $(shell date -u +%F)
VERSION  ?= $(shell git describe --tag --long --always --dirty 2>/dev/null)
GIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null)

FLAGS   := -ldflags "-X main.git=$(GIT) -X main.date=$(DATE)"

build = GOOS=$(1) GOARCH=$(2) go build -o build/$(appname)$(3) $(FLAGS)
tar = cd ./build && tar -czf $(1)_$(2).tar.gz $(appname)$(3) && rm $(appname)$(3)
zip = cd ./build && zip $(1)_$(2).zip $(appname)$(3) && rm $(appname)$(3)

.PHONY: all windows darwin linux clean build

info:
	@echo "────────────────────────────────"
	@echo "Go:       $(subst go version ,,$(shell go version))"
	@echo "Date:     $(DATE)"
	@echo "Git:      $(GIT)"
	@echo "Version:  $(VERSION)"
	@echo "────────────────────────────────"

package: info clean darwin linux
	cp -R ./html ./build
	cd ./build && zip $(appname)-$(VERSION).zip *.*

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
