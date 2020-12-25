#!/bin/bash

VERSION='21.01.01.1'
GIT_VER=$(git rev-list HEAD| wc -l)
CUR_BR=$(git branch -a| grep '*'|awk '{print $NF}'|sed 's/)//g')
GIT_COMMIT=$(git rev-parse --short HEAD)

make clean;make

if [ -f /etc/redhat-release ]
then
	rpmbuild -D "_builddir $(pwd)" -D "VERSION ${VERSION}" -D"GIT_VER ${GIT_VER}" -D "CUR_BR ${CUR_BR}" -D "GIT_COMMIT ${GIT_COMMIT}" -bb flexblockplugin.spec
else
	checkinstall -D --strip=no --install=no --backup=no --provides=flexblockplugin --pkgname=flexblockplugin --pkgversion=${VERSION}-${CUR_BR}-${GIT_COMMIT} --pkgrelease=${GIT_VER}
fi


