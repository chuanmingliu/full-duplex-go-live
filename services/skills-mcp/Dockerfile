# Package a verified static linux/amd64 binary from the release bundle.
FROM scratch
COPY --chmod=0555 skills-mcp /skills-mcp
# The sandbox creates an empty private directory beneath this writable mount.
ENV TMPDIR=/data
ENTRYPOINT ["/skills-mcp"]
