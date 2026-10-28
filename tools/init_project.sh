#!/bin/bash
# init_project.sh
# 

mkdir -p cmd/noteroot \
         internal/{config,log,notebooks,vault,httpapi,state} \
         docs/adr docs/schema \
         deploy scripts

cat > go.mod <<EOF
module noteroot

go 1.27
EOF

cat > .gitignore <<'EOF'
/bin/
/data/
*.corrupt
EOF
