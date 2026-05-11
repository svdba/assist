.PHONY: help
.EXPORT_ALL_VARIABLES:

BINNAME = assist
.DEFAULT_GOAL := $(BINNAME)-help

VERSION = 0.0.1
DAY     = $(shell git rev-list --max-count=1 --no-merges --pretty='format:%cs' HEAD . | tail -1)
SHA     = $(shell git rev-list --max-count=1 --no-merges --pretty='format:%h' HEAD . | tail -1)
BUILD   = $(DAY)-svas-$(SHA)-$(VERSION)

DISTDIR     = _dist
BINDIR      = bin
GOVERSION   = go1.25.5
LDFLAGS     = -w -s
GOFLAGS     = -buildvcs=false
VERSION_PKG = github.com/svdba/$(BINNAME)/version

LDFLAGS += -X $(VERSION_PKG).AppName=$(BINNAME)
LDFLAGS += -X $(VERSION_PKG).Version=$(VERSION)
LDFLAGS += -X $(VERSION_PKG).BuildDate=$(DAY)
LDFLAGS += -X $(VERSION_PKG).BuildSHA=$(SHA)

# ------------------------------------------------------------------------------
# build local only

.PHONY: build
build: $(BINDIR)/$(BINNAME) $(BINDIR)/$(BINNAME).sh
$(BINDIR)/$(BINNAME):
	bash -x build.sh

$(BINDIR)/$(BINNAME).exe:
	GOOS=windows BINNAME=$(BINNAME).exe bash -x build.sh

$(BINDIR)/$(BINNAME).sh:
	bash build.sh runscript

# ------------------------------------------------------------------------------
# clean

.PHONY: clean
clean:
	if [ ! -z "$(BINDIR)" ]; then rm -rf ./$(BINDIR); fi
	if [ ! -z "$(DISTDIR)" ]; then rm -rf ./$(DISTDIR); fi
	rm -f ./$(BINNAME)

# ------------------------------------------------------------------------------
