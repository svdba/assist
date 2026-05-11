#!/bin/bash

export DISTDIR=${DISTDIR:-_dist}
export GOTOOLCHAIN=${GOVERSION:-go1.25.0}
export GOOS=${GOOS:-linux}
export GOARCH=amd64
export CGO_ENABLED=0
export GO111MODULE=on
export BINDIR=${BINDIR:-bin}

mkdir -p "${BINDIR}" "${DISTDIR}"

# Build binary
if [ -z "$1" ]; then
    go get -v -t ${PKG:-./...}
    go build ${GOFLAGS:--buildvcs=false} -ldflags "${LDFLAGS:--w -s}" -o "${BINDIR}/${BINNAME:-assist}" .
else
    # Generate run script (for development)
    cat - > "${BINDIR}/${BINNAME:-assist}.sh" <<EOF
#!/bin/bash

export LOG_LEVEL=debug
export TELEGRAM_BOT_TOKEN="${TELEGRAM_BOT_TOKEN}"
export TELEGRAM_API_URL="${TELEGRAM_API_URL:-https://api.telegram.org/}"
export SECRETARY_DATA_DIR=./data
mkdir -p \${SECRETARY_DATA_DIR}
${BINDIR}/${BINNAME:-assist} "\$@"
EOF
    chmod +x "${BINDIR}/${BINNAME:-assist}.sh"
fi

# Build installation archive for /opt/assist
if [ -z "$1" ]; then
    ARCHIVE_NAME="assist-${VERSION:-0.0.1}-linux-amd64.tar.gz"
    ARCHIVE_DIR="${DISTDIR}/assist"

    # Create archive structure
    mkdir -p "${ARCHIVE_DIR}/bin"
    mkdir -p "${ARCHIVE_DIR}/config"
    mkdir -p "${ARCHIVE_DIR}/data"

    # Copy bin files
    cp "${BINDIR}/${BINNAME:-assist}" "${ARCHIVE_DIR}/bin/"
    # Copy config files
    cp config/.env.example "${ARCHIVE_DIR}/config/.env" 2>/dev/null || true

    # Create run script
    cat > "${ARCHIVE_DIR}/run.sh" <<'EOF'
#!/bin/bash

cd "$(dirname "$0")"
EOF
cat config/.env.example |egrep -v "^#" | sed -E 's/^(.)/export \1/' >> "${ARCHIVE_DIR}/run.sh"
    cat >> "${ARCHIVE_DIR}/run.sh" <<'EOF'
# add custom env vars, e.g. proxy
# export https_proxy=http://127.0.0.1:3128
# export http_proxy=http://127.0.0.1:3128

exec ./bin/assist "$@"
EOF
    chmod +x "${ARCHIVE_DIR}/run.sh"

    # Create systemd service file
    cat > "${ARCHIVE_DIR}/assist.service" <<EOF
[Unit]
Description=Assist Telegram Secretary Bot
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/assist
EnvironmentFile=/opt/assist/config/.env
ExecStart=/opt/bin/assist
Restart=on-failure
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

    # Create README
    cat > "${ARCHIVE_DIR}/README.md" <<EOF
# Assist - Telegram Secretary Bot

## Installation

1. Extract archive to /opt/assist:
   \`\`\`bash
   sudo tar -xzf ${ARCHIVE_NAME} -C /opt
   \`\`\`

2. Just run to test
   \`\`\`bash
   sudo nano /opt/assist/run.sh
   # Set TELEGRAM_BOT_TOKEN=your_token_here
   \`\`\`

3. Configure token for service:
   \`\`\`bash
   sudo nano /opt/assist/config/.env
   # Set TELEGRAM_BOT_TOKEN=your_token_here
   \`\`\`

4.1 Install as systemd service:
   \`\`\`bash
   sudo cp /opt/assist/assist.service /etc/systemd/system/
   sudo systemctl daemon-reload
   sudo systemctl enable assist
   sudo systemctl start assist
   \`\`\`

4.2 Check status:
   \`\`\`bash
   sudo systemctl status assist
   journalctl -u assist -f
   \`\`\`
EOF

    # Create archive
    cd "${DISTDIR}"
    tar -czf "${ARCHIVE_NAME}" assist/
    cd - > /dev/null

    # Clean up temp directory
    rm -rf "${ARCHIVE_DIR}"

    echo "Archive created: ${DISTDIR}/${ARCHIVE_NAME}"
fi
