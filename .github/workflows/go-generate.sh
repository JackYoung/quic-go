#!/usr/bin/env bash

set -e

find . -type f -name "*.go" -exec shasum {} \; > checksums_before.txt
# delete all go-generated files generated (that adhere to the comment convention)
grep --include \*.go -lrIZ "^// Code generated .* DO NOT EDIT\.$" . | xargs --null rm
# delete all files generated by Genny
grep --include \*.go -lrIZ "This file was automatically generated by genny." . | xargs --null rm
# first generate Genny files to make the code compile
grep --include \*.go -lrI "//go:generate genny" | xargs -L 1 go generate
# now generate everything
go generate ./...
find . -type f -name "*.go" -exec shasum {} \; > checksums_after.txt
DIFF=$(diff checksums_before.txt checksums_after.txt) || true
echo "$DIFF"
if [ -n "$DIFF" ]; then
  exit 1
else
  echo "All generated files match."
fi