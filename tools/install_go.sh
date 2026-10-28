#!/bin/bash
# install_go.sh

VERSION=1.27.2
case "$(uname -m)" in
  x86_64)  ARCH=linux-amd64 ;;
  aarch64) ARCH=linux-arm64 ;;
  *) echo "unsupported arch"; exit 1 ;;
esac

curl -fsSLO "https://go.dev/dl/go${VERSION}.${ARCH}.tar.gz"
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf "go${VERSION}.${ARCH}.tar.gz"
rm "go${VERSION}.${ARCH}.tar.gz"

cat >> ~/.profile <<'EOF'
export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin
EOF
source ~/.profile

go version    # expect: go version go1.27.2 linux/amd64
