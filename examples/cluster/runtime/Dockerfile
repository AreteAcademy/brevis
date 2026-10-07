# THE POD YOU BUILD, which is the whole of architecture C.
#
# The shipped agent image is a shell and the agent: enough to run `echo`, and
# nothing a real step needs. What makes it YOUR runtime is this file -- the
# tools your steps call, installed once, in a pod that stays up.
#
# A dbt runtime is the same three lines with different packages:
#
#   RUN apk add --no-cache python3 py3-pip \
#    && pip install --break-system-packages dbt-bigquery
#
# The agent is the floor. Brevis never builds this and never updates it: it
# sends a command to a pod you own, which is the point -- your dbt version is
# not coupled to the engine's release.
FROM brevis-agent:dev

# root to install, and back to 65532 to run. The manifest sets runAsNonRoot and
# the kubelet verifies the NUMBER, so the user has to be the one the image
# already has.
USER root
RUN apk add --no-cache python3 jq
USER brevis
