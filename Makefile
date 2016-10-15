###############################################################################
#
# Makefile: Makefile to build the goProbe traffic monitor
#
# Written by Lennart Elsen and Fabian Kohn, August 2014
# Copyright (c) 2014 Open Systems AG, Switzerland
# All Rights Reserved.
#
# Package for network traffic statistics capture (goProbe), storage (goDB)
# and retrieval (goquery)
#
################################################################################
# This code has been developed by Open Systems AG
#
# goProbe is free software; you can redistribute it and/or modify
# it under the terms of the GNU General Public License as published by
# the Free Software Foundation; either version 2 of the License, or
# (at your option) any later version.
#
# goProbe is distributed in the hope that it will be useful,
# but WITHOUT ANY WARRANTY; without even the implied warranty of
# MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
# GNU General Public License for more details.
#
# You should have received a copy of the GNU General Public License
# along with goProbe; if not, write to the Free Software
# Foundation, Inc., 59 Temple Place, Suite 330, Boston, MA  02111-1307  USA

SHELL := /bin/bash

PKG    = goProbe
PREFIX = /opt/ntm

# downloader used for grabbing the external code. Change this if it does not
# correspond to the usual way you download files on your system
DOWNLOAD	= curl --progress-bar -L --url

# GoLang main version
GO_PRODUCT	    = goProbe
GO_QUERY        = goQuery

GOLANG		    = go1.7.1.linux-amd64
GOLANG_SITE	  = https://storage.googleapis.com/golang
GO_SRCDIR	    = $(PWD)/addon/gocode/src

# for providing the go compiler with the right env vars
export GOROOT := $(PWD)/go
export PATH := $(GOROOT)/bin:$(PATH)
export GOPATH := $(PWD)/addon/gocode

# gopacket and gopcap
GOPACKET	    = 1.1.9
GOPACKET_REV	= 1.1.9
GOPACKET_SITE	= https://github.com/google/gopacket/archive
GOPACKETDIR	    = code.google.com/p

# pcap libraries
PCAP_VERSION = 1.5.3
PCAP		 = libpcap-$(PCAP_VERSION)
PCAP_SITE	 = http://www.tcpdump.org/release
PCAP_DIR	 := $(PWD)/$(PCAP)

# libprotoident libraries
LIBTRACE	    = libtrace-3.0.20
LIBTRACE_SITE	= http://research.wand.net.nz/software/libtrace
LIBTRACE_DIR	:= $(PWD)/$(LIBTRACE)

LIBPROTOIDENT	= libprotoident-2.0.7
LPIDENT_SITE	= http://research.wand.net.nz/software/libprotoident
LPIDENT_DIR	    := $(PWD)/$(LIBPROTOIDENT)
export LD_LIBRARY_PATH := $(PWD)/$(PCAP):$(PWD)/$(LIBPROTOIDENT)/lib/.libs:$(PWD)/$(LIBTRACE)/lib/.libs:$(PWD)/addon/dpi

configure:

	## GO SETUP ##
	echo "*** downloading $(GOLANG) ***"
	$(DOWNLOAD) $(GOLANG_SITE)/$(GOLANG).tar.gz -O
	# Useful for debugging:
	# cp ~/$(GOLANG).tar.gz .

	echo "*** unpacking $(GOLANG) ***"
	tar xf $(GOLANG).tar.gz

	echo "*** downloading gopacket_$(GOPACKET) ***"
	$(DOWNLOAD) $(GOPACKET_SITE)/v$(GOPACKET).tar.gz -O

	echo "*** unpacking/patching dependency gopacket_$(GOPACKET) ***"
	tar xf v$(GOPACKET).tar.gz
	mv gopacket-$(GOPACKET) gopacket

	patch -Np0 < addon/gopacket-v$(GOPACKET).patch

	# change the library path inside pcap.go
	sed -i -e 's#LIBPCAPPATH#$(PCAP_DIR)#g' gopacket/pcap/pcap.go

	mkdir -p $(GO_SRCDIR)/$(GOPACKETDIR)
	mv gopacket $(GO_SRCDIR)/$(GOPACKETDIR)

	echo "*** downloading $(PCAP) ***"
	$(DOWNLOAD) $(PCAP_SITE)/$(PCAP).tar.gz -O
	echo "*** unpacking/patching/configuring dependency $(PCAP) ***"
	tar xf $(PCAP).tar.gz
	patch -Np0 < addon/libpcap-$(PCAP_VERSION).patch
	cd $(PCAP); sh configure --prefix=$(PREFIX)/$(PKG) --quiet >> /dev/null

	echo "*** downloading $(LIBTRACE) ***"
	$(DOWNLOAD) $(LIBTRACE_SITE)/$(LIBTRACE).tar.bz2 -O
	tar xf $(LIBTRACE).tar.bz2

	echo "*** downloading/patching $(LIBPROTOIDENT) ***"
	$(DOWNLOAD) $(LPIDENT_SITE)/$(LIBPROTOIDENT).tar.gz -O

	tar xf $(LIBPROTOIDENT).tar.gz
	patch -Np0 < addon/libprotoident.patch

compile:

	## GO CODE COMPILATION ##
	# first, compile libpcap and libprotoident because the go code depends on it
	echo "*** compiling $(PCAP) ***"
	cd $(PCAP); make -s > /dev/null; rm libpcap.a; ln -sf libpcap.so.$(PCAP_VERSION) libpcap.so; ln -sf libpcap.so.$(PCAP_VERSION) libpcap.so.1

	echo "*** compiling lz4 ***"
	cd addon/lz4; make -s > /dev/null

	echo "*** configuring/compiling $(LIBTRACE) ***"
	cd $(LIBTRACE); sh configure --prefix=$(PREFIX)/$(PKG) CFLAGS='-I$(PCAP_DIR)' CPPFLAGS='-I$(PCAP_DIR)' LDFLAGS='-L$(PCAP_DIR) -lpcap' --quiet >> /dev/null
	cd $(LIBTRACE); make -s >> /dev/null

	echo "*** configuring/compiling dependency $(LIBPROTOIDENT) ***"
	cd $(LIBPROTOIDENT); sh configure --with-tools=no --prefix=$(PREFIX)/$(PKG) CXXFLAGS='-I$(LIBTRACE_DIR)/lib' LDFLAGS='-L$(LIBTRACE_DIR)/lib/.libs' --quiet >> /dev/null
	cd $(LIBPROTOIDENT); make -j2 -s >> /dev/null

	echo "*** compiling ProtoId C-Wrapper ***"
	cd addon/dpi; make -s >> /dev/null

	# make the protocol-category mappings available to goquery using the
	# compiled helper binary and a bash script for IP protocols
	addon/dpi/serialize_prot_list > $(GO_SRCDIR)/OSAG/goDB/GPDPIProtocols.go
	addon/dpi/serialize_ipprot_list.sh >> $(GO_SRCDIR)/OSAG/goDB/GPDPIProtocols.go

	# make all reverse lookup keys lowercase
	sed -i 's/\"\(.*\)\": \([0-9]*\)/\L"\1": \2/g' $(GO_SRCDIR)/OSAG/goDB/GPDPIProtocols.go

	echo "*** compiling $(GO_PRODUCT) ***"
	cd $(GO_SRCDIR)/OSAG/capture; CGO_CFLAGS='-I$(PCAP_DIR)' CGO_LDFLAGS='-L$(PCAP_DIR)' go build -a -o $(GO_PRODUCT)   # build the goProbe binary

	echo "*** compiling $(GO_QUERY) ***"
	cd $(GO_SRCDIR)/OSAG/query; go build -tags public -ldflags="-X main.goprobeConfigPath=$(PREFIX)/$(PKG)/etc/goprobe.conf" -a -o $(GO_QUERY)

install: go_install

go_install:

	rm -rf absolute

	# additional directories
	echo "*** creating binary tree ***"
	mkdir -p absolute$(PREFIX)/$(PKG)/bin    && chmod 755 absolute$(PREFIX)/$(PKG)/bin
	mkdir -p absolute$(PREFIX)/$(PKG)/etc    && chmod 755 absolute$(PREFIX)/$(PKG)/etc
	mkdir -p absolute$(PREFIX)/$(PKG)/shared && chmod 755 absolute$(PREFIX)/$(PKG)/shared
	mkdir -p absolute/etc/init.d             && chmod 755 absolute/etc/init.d
	mkdir -p absolute/etc/systemd/system     && chmod 755 absolute/etc/systemd/system

	echo "*** installing $(GO_PRODUCT) and $(GO_QUERY) ***"
	cd $(PCAP); make -s install DESTDIR=$(PWD)/absolute >> /dev/null

	cp $(GO_SRCDIR)/OSAG/capture/$(GO_PRODUCT) absolute$(PREFIX)/$(PKG)/bin
	cp $(GO_SRCDIR)/OSAG/query/$(GO_QUERY)     absolute$(PREFIX)/$(PKG)/bin
	cp addon/gp_status.pl                      absolute$(PREFIX)/$(PKG)/shared
	cp addon/goprobe.targets                   absolute$(PREFIX)/$(PKG)/shared

	# change the prefix variable in the init script
	cp addon/goprobe.init absolute/etc/init.d/goprobe.init
	sed "s#PREFIX=#PREFIX=$(PREFIX)#g" -i absolute/etc/init.d/goprobe.init

	# change the prefix variable in the systemd script
	cp addon/goprobe.service absolute/etc/systemd/system/goprobe.service
	sed "s#PREFIX#$(PREFIX)#g" -i absolute/etc/systemd/system/goprobe.service
	sed "s#PREFIX#$(PREFIX)#g" -i absolute$(PREFIX)/$(PKG)/shared/goprobe.targets

	echo "*** installing $(LIBTRACE) ***"
	cd $(LIBTRACE); make -s install DESTDIR=$(PWD)/absolute >> /dev/null

	echo "*** installing $(LIBPROTOIDENT) ***"
	cd $(LIBPROTOIDENT); make -s install DESTDIR=$(PWD)/absolute >> /dev/null

	# move the libProtoId.so library and the protocol list to the correct location
	cp addon/dpi/libProtoId.so absolute$(PREFIX)/$(PKG)/lib

	echo "*** generating example configuration ***"
	echo -e "{\n\t\"db_path\" : \"$(PREFIX)/$(PKG)/db\",\n\t\"interfaces\" : {\n\t\t\"eth0\" : {\n\t\t\t\"bpf_filter\" : \"not arp and not icmp\",\n\t\t\t\"buf_size\" : 2097152,\n\t\t\t\"promisc\" : false\n\t\t}\n\t}\n}" > absolute$(PREFIX)/$(PKG)/etc/goprobe.conf.example

	# set the appropriate permissions
	chmod -R 755 absolute$(PREFIX)/$(PKG)/bin \
		absolute$(PREFIX)/$(PKG)/shared \
		absolute$(PREFIX)/$(PKG)/etc \
		absolute$(PREFIX)/$(PKG)/lib \
		absolute/etc/init.d \

	echo "*** cleaning unneeded files ***"

	# binary cleaning
	# libpcap binaries
	rm -f absolute$(PREFIX)/$(PKG)/bin/pcap-config

	# libtrace binaries
	rm -f absolute$(PREFIX)/$(PKG)/bin/trace*
	rm -f absolute$(PREFIX)/$(PKG)/bin/wandiocat

	# library cleaning
	rm -f absolute$(PREFIX)/$(PKG)/lib/*.la
	rm -f absolute$(PREFIX)/$(PKG)/lib/*.lai
	rm -f absolute$(PREFIX)/$(PKG)/lib/*.a
	rm -rf absolute$(PREFIX)/$(PKG)/include
	rm -rf absolute$(PREFIX)/$(PKG)/share
	rm -rf absolute$(PREFIX)/$(PKG)/lib/libpacketdump*

	# strip binaries
	strip --strip-unneeded absolute$(PREFIX)/$(PKG)/bin/*

package: go_package

go_package:

	cd absolute; tar cjf $(PKG).tar.bz2 *; mv $(PKG).tar.bz2 ../

deploy:

	# commands for deploying goProbe on the same system on which it was compiled
	if [ "$(USER)" != "root" ]; \
	then \
		echo "*** [deploy] Error: command must be run as root"; \
	else \
		echo "*** syncing binary tree ***"; \
		rsync -a absolute/ /; \
		ln -sf $(PREFIX)/$(PKG)/bin/goQuery /usr/local/bin/goquery; \
		chown root.root /etc/init.d/goprobe.init; \
		chown root.root /etc/systemd/system/goprobe.service; \
		systemctl daemon-reload > /dev/null 2>&1; \
	fi

clean:

	echo "*** removing binary tree ***"
	rm -rf absolute

	echo "*** removing $(GOLANG), gopacket and $(PCAP) ***"
	rm -rf go $(GOLANG).tar.gz
	rm -rf $(GO_SRCDIR)/$(GOPACKETDIR) gopacket-$(GOPACKET_REV) gopacket-$(GOPACKET) gopacket $(GO_SRCDIR)/code.google.com v$(GOPACKET).tar.gz $(GO_SRCDIR)/OSAG/capture/$(GO_PRODUCT) $(GO_SRCDIR)/OSAG/query/$(GO_QUERY)
	rm -rf $(PCAP) $(PCAP).tar.gz

	echo "*** removing $(LIBTRACE) and $(LIBPROTOIDENT) ***"
	rm -rf $(LIBTRACE) $(LIBPROTOIDENT)
	rm -f $(LIBTRACE).tar.bz2 $(LIBPROTOIDENT).tar.gz
	rm -f l7protocols.json
	rm -f addon/dpi/serialize_prot_list

	rm -rf $(PKG).tar.bz2

	cd addon/lz4; make clean > /dev/null

all: clean configure compile install

.SILENT:
